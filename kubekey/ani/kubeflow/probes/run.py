"""Restricted environment client; executes in the locked SDK image.

Explicit short-lived token files and public CAs are supplied by the lab caller.
No administrator kubeconfig, ambient credentials, transport retries or cleanup.
An unknown mutation is journaled before the request and never retried here.
"""
import argparse
import hashlib
import json
import os
import pathlib
import time
import uuid

import kfp
from kubernetes import client


def save(path, record):
    temporary = path.with_suffix(".new")
    with temporary.open("x") as stream:
        os.chmod(temporary, 0o600)
        json.dump(record, stream, indent=2)
        stream.write("\n")
        stream.flush()
        os.fsync(stream.fileno())
    os.replace(temporary, path)


def execute(args, report, record):
    config = client.Configuration()
    config.host, config.ssl_ca_cert = args.api, args.api_ca
    config.verify_ssl, config.retries = True, 0
    config.api_key["authorization"] = "Bearer " + pathlib.Path(args.api_token).read_text().strip()
    api = client.ApiClient(config)
    api.rest_client.pool_manager.connection_pool_kw["retries"] = 0
    core, custom = client.CoreV1Api(api), client.CustomObjectsApi(api)
    pipeline = kfp.Client(host=args.kfp, namespace=args.namespace,
                          existing_token=pathlib.Path(args.kfp_token).read_text().strip(),
                          ssl_ca_cert=args.kfp_ca, verify_ssl=True)
    clients = {}
    for key in ("_run_api", "_experiment_api", "_pipelines_api", "_upload_api"):
        generated = getattr(pipeline, key)
        clients[id(generated.api_client)] = generated.api_client
        generated.api_client.configuration.retries = 0
        generated.api_client.rest_client.pool_manager.connection_pool_kw["retries"] = 0
    for generated in clients.values():
        original_call = generated.call_api
        def bounded(*arguments, _original=original_call, **keywords):
            keywords["_request_timeout"] = keywords.get("_request_timeout") or (5, 30)
            return _original(*arguments, **keywords)
        generated.call_api = bounded
    execution = "env-" + uuid.uuid4().hex[:16]
    claim = "ani-kfp-workspace-" + execution
    job_name = "ani-kfp-train-" + execution
    record.update(execution=execution, namespace=args.namespace, mode=args.mode,
                  workspace={"name": claim, "creationResult": "UNKNOWN"})
    save(report, record)
    pvc = {"apiVersion": "v1", "kind": "PersistentVolumeClaim",
           "metadata": {"name": claim, "namespace": args.namespace,
                        "labels": {"ani.io/execution-id": execution}},
           "spec": {"storageClassName": args.storage_class, "accessModes": ["ReadWriteMany"],
                    "resources": {"requests": {"storage": args.size}}}}
    core.create_namespaced_persistent_volume_claim(args.namespace, pvc, dry_run="All", _request_timeout=30)
    created = core.create_namespaced_persistent_volume_claim(args.namespace, pvc, _request_timeout=30)
    record["workspace"].update(uid=created.metadata.uid, creationResult="CONFIRMED")
    save(report, record)
    deadline = time.monotonic() + 120
    while time.monotonic() < deadline:
        current = core.read_namespaced_persistent_volume_claim(claim, args.namespace, _request_timeout=30)
        if current.metadata.uid != created.metadata.uid or current.metadata.owner_references:
            raise RuntimeError("workspace identity/lifecycle changed")
        if current.status.phase == "Bound":
            break
        time.sleep(3)
    else:
        raise TimeoutError("workspace binding deadline")
    record["experiment"] = {"name": execution, "creationResult": "UNKNOWN"}
    save(report, record)
    experiment = pipeline.create_experiment(execution, namespace=args.namespace)
    record["experiment"].update(id=experiment.experiment_id, creationResult="CONFIRMED")
    text = pathlib.Path(args.pipeline).read_text()
    placeholder = "ANI_IMAGE_ani.local/kubeflow-execution:26.03-v1"
    if text.count(placeholder) != 2 or "@sha256:" not in args.image:
        raise ValueError("compiled offline probe image binding differs")
    text = text.replace(placeholder, args.image)
    package = report.parent / "pipeline.yaml"
    with package.open("x") as stream:
        stream.write(text)
    record["pipelineSha256"] = hashlib.sha256(package.read_bytes()).hexdigest()
    record["run"] = {"creationResult": "UNKNOWN"}
    save(report, record)
    run = pipeline.run_pipeline(experiment.experiment_id, execution, pipeline_package_path=str(package),
        params={"claim": claim, "claim_uid": created.metadata.uid, "execution": execution,
                "namespace": args.namespace, "runtime_name": args.runtime,
                "trainer_node": args.trainer_node, "mode": args.mode},
        enable_caching=False, service_account="pipeline-runner")
    record["run"].update(id=run.run_id, creationResult="CONFIRMED")
    save(report, record)
    deadline, stopped = time.monotonic() + 780, False
    while time.monotonic() < deadline:
        observed = pipeline.get_run(run.run_id)
        record["run"]["state"] = observed.state
        jobs = custom.list_namespaced_custom_object("trainer.kubeflow.org", "v1alpha1", args.namespace,
                    "trainjobs", label_selector="ani.io/execution-id=" + execution, _request_timeout=30)["items"]
        if len(jobs) > 1:
            raise RuntimeError("multiple external jobs for one execution")
        if jobs:
            job = jobs[0]
            if job["metadata"]["name"] != job_name or job["metadata"].get("annotations", {}).get("ani.io/kfp-run-id") != run.run_id:
                raise RuntimeError("external TrainJob / KFP Run correlation differs")
            if record.get("trainJob", {}).get("uid", job["metadata"]["uid"]) != job["metadata"]["uid"]:
                raise RuntimeError("TrainJob UID changed")
            record["trainJob"] = {"name": job_name, "uid": job["metadata"]["uid"],
                "conditions": job.get("status", {}).get("conditions", [])}
            # Read the trusted component's create response. Observing a label
            # or current name alone never grants stop/cleanup ownership.
            if not record.get("creationReceipt"):
                all_pods = core.list_namespaced_pod(args.namespace, _request_timeout=30).items
                for control in all_pods:
                    if control.spec.service_account_name != "pipeline-runner" or control.status.phase not in ("Running", "Succeeded", "Failed"):
                        continue
                    try:
                        log = core.read_namespaced_pod_log(control.metadata.name, args.namespace, container="main", _request_timeout=30)
                    except client.exceptions.ApiException as error:
                        if error.status in (400, 404):
                            continue
                        raise
                    for line in log.splitlines():
                        if not line.startswith("ANI_TRAINJOB_CREATE_RESPONSE "):
                            continue
                        receipt = json.loads(line.removeprefix("ANI_TRAINJOB_CREATE_RESPONSE "))
                        if receipt.get("execution") == execution and receipt.get("kfpRunId") == run.run_id:
                            if receipt.get("namespace") != args.namespace or receipt.get("workspaceUid") != created.metadata.uid or receipt.get("trainJobName") != job_name or receipt.get("trainJobUid") != job["metadata"]["uid"]:
                                raise RuntimeError("actual create response differs from observed external job")
                            record["creationReceipt"] = {**receipt, "controlPodUid": control.metadata.uid}
            pods = core.list_namespaced_pod(args.namespace, label_selector="ani.io/execution-id=" + execution,
                                           _request_timeout=30).items
            record["trainPods"] = [{"name": p.metadata.name, "uid": p.metadata.uid, "node": p.spec.node_name,
                "serviceAccount": p.spec.service_account_name, "tokenMount": p.spec.automount_service_account_token,
                "phase": p.status.phase, "containers": [{"name": c.name,
                    "exitCode": c.state.terminated.exit_code if c.state and c.state.terminated else None,
                    "imageID": c.image_id} for c in p.status.container_statuses or []]} for p in pods]
            output_fsynced = False
            if args.mode == "stop" and not stopped and record.get("creationReceipt"):
                for pod in pods:
                    if pod.status.phase != "Running":
                        continue
                    log = core.read_namespaced_pod_log(pod.metadata.name, args.namespace, container="node", _request_timeout=30)
                    for line in log.splitlines():
                        value = json.loads(line)
                        if value.get("execution") == execution and value.get("checkpoint") == "unique-output-fsynced":
                            output_fsynced = True
            if args.mode == "stop" and not stopped and output_fsynced:
                record["stop"] = {"result": "UNKNOWN", "trainJobUid": job["metadata"]["uid"],
                                  "podUids": [p.metadata.uid for p in pods]}
                save(report, record)
                patch = [{"op": "test", "path": "/metadata/uid", "value": job["metadata"]["uid"]},
                         {"op": "test", "path": "/metadata/resourceVersion", "value": job["metadata"]["resourceVersion"]},
                         {"op": "add", "path": "/spec/suspend", "value": True}]
                custom.patch_namespaced_custom_object("trainer.kubeflow.org", "v1alpha1", args.namespace,
                    "trainjobs", job_name, patch, _request_timeout=30)
                record["stop"]["trainJobResult"] = "CONFIRMED"
                save(report, record)
                pipeline.terminate_run(run.run_id)
                record["stop"]["runResult"] = "CONFIRMED"
                stopped = True
        save(report, record)
        if observed.state in ("SUCCEEDED", "FAILED", "CANCELED"):
            break
        time.sleep(5)
    else:
        raise TimeoutError("bounded KFP/external TrainJob deadline")
    expected = {"success": "SUCCEEDED", "fail": "FAILED", "stop": "CANCELED"}[args.mode]
    if observed.state != expected or not record.get("trainJob") or not record.get("creationReceipt") or (args.mode == "stop" and not stopped):
        raise RuntimeError("actual run state/correlation differs")
    details = observed.run_details
    record["run"]["details"] = details.to_dict() if details else None
    # Artifact records may contain signed URLs. Keep these private; the caller
    # publishes only reviewed IDs, hashes and MLMD links in sanitized evidence.
    final = core.read_namespaced_persistent_volume_claim(claim, args.namespace, _request_timeout=30)
    if final.metadata.uid != created.metadata.uid or final.metadata.owner_references:
        raise RuntimeError("retained workspace identity changed")
    record.update(status="RUN_STATE_CORRELATED", workspaceRetained=True,
                  fileReadback="NOT_ATTESTED_BY_THIS_CLIENT", acceptance="EAC_NOT_ATTESTED")
    save(report, record)


if __name__ == "__main__":
    parser = argparse.ArgumentParser()
    for name in ("api", "api-ca", "api-token", "kfp", "kfp-ca", "kfp-token", "namespace",
                 "pipeline", "image", "runtime", "trainer-node", "storage-class", "size", "output"):
        parser.add_argument("--" + name, required=True)
    parser.add_argument("--mode", choices=("success", "fail", "stop"), required=True)
    arguments = parser.parse_args()
    directory = pathlib.Path(arguments.output)
    directory.mkdir(mode=0o700)
    report = directory / "private-report.json"
    record = {"schema": "ani.kubeflow.restricted-client.v1", "status": "IN_PROGRESS", "cleanup": "NOT_REQUESTED"}
    try:
        execute(arguments, report, record)
    except BaseException as error:
        record.update(status="FAIL", errorType=type(error).__name__, errorStatus=getattr(error, "status", None))
        save(report, record)
        raise RuntimeError("restricted probe failed; inspect private record and reconcile existing UIDs") from None
    print(json.dumps({"status": record["status"], "execution": record["execution"], "report": str(report)}))
