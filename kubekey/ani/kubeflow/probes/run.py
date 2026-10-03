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


def pod_log(core, name, namespace, container):
    # The generated client turns a single JSON log line into str(dict).
    # Logs are bytes, not a JSON API response; retain their original encoding.
    response = core.read_namespaced_pod_log(name, namespace, container=container,
                                            _preload_content=False, _request_timeout=30)
    try:
        body = response.data
        if len(body) > 1048576:
            raise RuntimeError("environment Pod log exceeds bounded size")
        return body.decode("utf-8")
    finally:
        response.close()
        response.release_conn()


def save(path, record):
    # Serialize before opening the exclusive temporary file. An unsupported
    # value must not leave a partial file that prevents the failure record.
    body = json.dumps(record, indent=2) + "\n"
    temporary = path.with_suffix(".new")
    with temporary.open("x") as stream:
        os.chmod(temporary, 0o600)
        stream.write(body)
        stream.flush()
        os.fsync(stream.fileno())
    os.replace(temporary, path)
    directory = os.open(path.parent, os.O_RDONLY | os.O_DIRECTORY)
    try:
        os.fsync(directory)
    finally:
        os.close(directory)


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
    if args.reconcile_success_report:
        return reconcile_success(args, report, record, pipeline, core, custom)
    if args.reconcile_stop_report:
        return reconcile_stop(args, report, record, pipeline, core, custom)
    if args.resume_stop_report:
        previous_path = pathlib.Path(args.resume_stop_report)
        previous = json.loads(previous_path.read_text())
        if args.mode != "stop" or previous["mode"] != "stop" or previous["status"] != "FAIL" or previous.get("stop") or previous["namespace"] != args.namespace:
            raise ValueError("only a failed stop client before its first stop mutation can resume")
        workspace, receipt = previous["workspace"], previous["creationReceipt"]
        if any(previous[key]["creationResult"] != "CONFIRMED" for key in ("workspace", "experiment", "run")) or receipt["execution"] != previous["execution"] or receipt["kfpRunId"] != previous["run"]["id"] or receipt["workspaceUid"] != workspace["uid"]:
            raise ValueError("original stop creation receipts differ")
        created = core.read_namespaced_persistent_volume_claim(workspace["name"], args.namespace, _request_timeout=30)
        job = custom.get_namespaced_custom_object("trainer.kubeflow.org", "v1alpha1", args.namespace, "trainjobs", receipt["trainJobName"], _request_timeout=30)
        run = pipeline.get_run(previous["run"]["id"])
        if created.metadata.uid != workspace["uid"] or created.metadata.owner_references or created.status.phase != "Bound" or job["metadata"]["uid"] != receipt["trainJobUid"] or job["metadata"].get("annotations", {}).get("ani.io/kfp-run-id") != run.run_id or job["spec"].get("suspend") or job["spec"]["runtimeRef"]["name"] != args.runtime or run.state != "RUNNING" or run.experiment_id != previous["experiment"]["id"]:
            raise ValueError("current stop execution identity/state differs")
        record.update(previous, status="IN_PROGRESS", resumption={"originalReportSha256": hashlib.sha256(previous_path.read_bytes()).hexdigest(), "newExecutionCreates": 0})
        save(report, record)
        return monitor(args, report, record, pipeline, core, custom, created, run)
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
    return monitor(args, report, record, pipeline, core, custom, created, run)


def monitor(args, report, record, pipeline, core, custom, created, run):
    execution, claim = record["execution"], record["workspace"]["name"]
    job_name = "ani-kfp-train-" + execution
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
                        log = pod_log(core, control.metadata.name, args.namespace, "main")
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
                    log = pod_log(core, pod.metadata.name, args.namespace, "node")
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
                # This locked SDK's generated CRD patch method always sends
                # merge-patch. UID/resourceVersion tests require JSON Patch.
                custom.api_client.call_api("/apis/trainer.kubeflow.org/v1alpha1/namespaces/{namespace}/trainjobs/{name}",
                    "PATCH", {"namespace": args.namespace, "name": job_name}, [],
                    {"Accept": "application/json", "Content-Type": "application/json-patch+json"},
                    body=patch, response_type="object", auth_settings=["BearerToken"],
                    _return_http_data_only=True, _request_timeout=30)
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
    expected = {"success": {"SUCCEEDED"}, "fail": {"FAILED"}, "stop": {"CANCELED", "FAILED"}}[args.mode]
    if observed.state not in expected or not record.get("trainJob") or not record.get("creationReceipt") or (args.mode == "stop" and not stopped):
        raise RuntimeError("actual run state/correlation differs")
    conditions = {c["type"]: c["status"] for c in record["trainJob"]["conditions"]}
    if args.mode == "success" and (conditions.get("Complete") != "True" or len(record.get("trainPods", [])) != 1 or record["trainPods"][0]["phase"] != "Succeeded"):
        raise RuntimeError("successful Run lacks the actual successful external CPU Pod")
    if args.mode == "fail" and (conditions.get("Failed") != "True" or not any(c.get("exitCode") == 42 for p in record.get("trainPods", []) for c in p["containers"])):
        raise RuntimeError("failed Run lacks the external CPU process exit 42")
    if args.mode == "stop":
        verify_stopped(args, record, pipeline, core, custom, observed)
    details = observed.run_details
    record["run"]["details"] = pipeline._run_api.api_client.sanitize_for_serialization(details) if details else None
    # Artifact records may contain signed URLs. Keep these private; the caller
    # publishes only reviewed IDs, hashes and MLMD links in sanitized evidence.
    final = core.read_namespaced_persistent_volume_claim(claim, args.namespace, _request_timeout=30)
    if final.metadata.uid != created.metadata.uid or final.metadata.owner_references:
        raise RuntimeError("retained workspace identity changed")
    record.update(status="RUN_STATE_CORRELATED", workspaceRetained=True,
                  fileReadback="NOT_ATTESTED_BY_THIS_CLIENT", acceptance="EAC_NOT_ATTESTED")
    save(report, record)


def verify_stopped(args, record, pipeline, core, custom, observed):
    receipt, stop = record["creationReceipt"], record["stop"]
    if stop.get("trainJobResult") != "CONFIRMED" or stop.get("runResult") != "CONFIRMED" or stop["trainJobUid"] != receipt["trainJobUid"]:
        raise ValueError("both original stop requests must have confirmed receipts")
    job = custom.get_namespaced_custom_object("trainer.kubeflow.org", "v1alpha1", args.namespace, "trainjobs", receipt["trainJobName"], _request_timeout=30)
    if job["metadata"]["uid"] != receipt["trainJobUid"] or not job["spec"].get("suspend") or not any(c["type"] == "Suspended" and c["status"] == "True" for c in job.get("status", {}).get("conditions", [])):
        raise RuntimeError("original TrainJob is not actually suspended")
    workflows = custom.list_namespaced_custom_object("argoproj.io", "v1alpha1", args.namespace, "workflows", label_selector="pipeline/runid=" + observed.run_id, _request_timeout=30)["items"]
    if len(workflows) != 1 or workflows[0]["spec"].get("activeDeadlineSeconds") != 0 or workflows[0].get("status", {}).get("phase") != "Failed":
        raise RuntimeError("fixed KFP terminate operation did not terminate the actual Workflow")
    history = pipeline._run_api.api_client.sanitize_for_serialization(observed.state_history)
    states = [row["state"] for row in history or []]
    # KFP e4ebca3 terminates by activeDeadlineSeconds=0. Its IsTerminating
    # predicate excludes final Workflows, so persistence reports final Failed.
    # Require the real CANCELING transition; never relabel this as CANCELED.
    if observed.state not in ("FAILED", "CANCELED") or "CANCELING" not in states or states[-1] != observed.state:
        raise RuntimeError("terminal Run lacks the actual cancellation transition")
    pods = core.list_namespaced_pod(args.namespace, _request_timeout=30).items
    control = [p for p in pods if p.metadata.uid == receipt["controlPodUid"]]
    workflow_uid = workflows[0]["metadata"]["uid"]
    if len(control) != 1 or not any(o.kind == "Workflow" and o.uid == workflow_uid for o in control[0].metadata.owner_references or []):
        raise RuntimeError("terminated Workflow differs from the original control Pod receipt")
    deadline = time.monotonic() + 120
    while time.monotonic() < deadline:
        current = core.list_namespaced_pod(args.namespace, label_selector="ani.io/execution-id=" + record["execution"], _request_timeout=30).items
        if not set(stop["podUids"]) & {p.metadata.uid for p in current}:
            if current:
                raise RuntimeError("controller created a replacement Pod after stop")
            stop.update(result="CONFIRMED", actualExternalPodsGone=True, remainingPodUids=[], workflowUid=workflow_uid,
                        workflowActiveDeadlineSeconds=0, actualRunState=observed.state, stateHistory=history)
            record["trainJob"]["conditions"] = job["status"]["conditions"]
            return
        time.sleep(3)
    raise TimeoutError("actual external training Pod termination deadline")


def reconcile_stop(args, report, record, pipeline, core, custom):
    previous_path = pathlib.Path(args.reconcile_stop_report)
    previous = json.loads(previous_path.read_text())
    if args.mode != "stop" or previous["mode"] != "stop" or previous["status"] != "FAIL" or previous["namespace"] != args.namespace or any(previous[k]["creationResult"] != "CONFIRMED" for k in ("workspace", "experiment", "run")):
        raise ValueError("only the original confirmed stop execution can be reconciled")
    receipt, workspace = previous["creationReceipt"], previous["workspace"]
    if receipt["execution"] != previous["execution"] or receipt["kfpRunId"] != previous["run"]["id"] or receipt["workspaceUid"] != workspace["uid"] or receipt["trainJobUid"] != previous["trainJob"]["uid"] or receipt["trainJobName"] != "ani-kfp-train-" + previous["execution"]:
        raise ValueError("original stop creation identities differ")
    claim = core.read_namespaced_persistent_volume_claim(workspace["name"], args.namespace, _request_timeout=30)
    observed = pipeline.get_run(previous["run"]["id"])
    if claim.metadata.uid != workspace["uid"] or claim.metadata.owner_references or claim.status.phase != "Bound" or observed.experiment_id != previous["experiment"]["id"]:
        raise RuntimeError("current stopped workspace/experiment differs")
    record.update(previous)
    verify_stopped(args, record, pipeline, core, custom, observed)
    record["run"].update(state=observed.state, details=pipeline._run_api.api_client.sanitize_for_serialization(observed.run_details))
    record.update(status="RUN_STATE_CORRELATED", workspaceRetained=True, fileReadback="NOT_ATTESTED_BY_THIS_CLIENT",
                  reconciliation={"sourceReportSha256": hashlib.sha256(previous_path.read_bytes()).hexdigest(), "persistentWrites": 0}, acceptance="EAC_NOT_ATTESTED")
    save(report, record)


def reconcile_success(args, report, record, pipeline, core, custom):
    """Read-only recovery of a completed execution whose terminal save failed."""
    source = pathlib.Path(args.reconcile_success_report).read_bytes()
    previous = json.loads(source)
    if (args.mode != "success" or previous["mode"] != "success" or previous["namespace"] != args.namespace
            or previous["status"] != "IN_PROGRESS" or previous["run"].get("state") != "SUCCEEDED"
            or any(previous[key].get("creationResult") != "CONFIRMED" for key in ("workspace", "experiment", "run"))):
        raise ValueError("only the recorded, confirmed successful execution can be reconciled")
    execution, workspace, receipt = previous["execution"], previous["workspace"], previous["creationReceipt"]
    if (not execution.startswith("env-") or workspace["name"] != "ani-kfp-workspace-" + execution
            or receipt["execution"] != execution or receipt["namespace"] != args.namespace
            or receipt["workspaceUid"] != workspace["uid"] or receipt["kfpRunId"] != previous["run"]["id"]
            or receipt["trainJobName"] != "ani-kfp-train-" + execution or receipt["trainJobUid"] != previous["trainJob"]["uid"]):
        raise ValueError("recorded creation response identities differ")
    claim = core.read_namespaced_persistent_volume_claim(workspace["name"], args.namespace, _request_timeout=30)
    job = custom.get_namespaced_custom_object("trainer.kubeflow.org", "v1alpha1", args.namespace,
                                             "trainjobs", receipt["trainJobName"], _request_timeout=30)
    observed = pipeline.get_run(previous["run"]["id"])
    if (claim.metadata.uid != workspace["uid"] or claim.metadata.owner_references or claim.status.phase != "Bound"
            or job["metadata"]["uid"] != receipt["trainJobUid"] or job["metadata"].get("annotations", {}).get("ani.io/kfp-run-id") != receipt["kfpRunId"]
            or job["spec"]["runtimeRef"]["name"] != args.runtime or observed.state != "SUCCEEDED"
            or observed.run_id != previous["run"]["id"] or observed.experiment_id != previous["experiment"]["id"]
            or not any(c["type"] == "Complete" and c["status"] == "True" for c in job.get("status", {}).get("conditions", []))):
        raise RuntimeError("current successful execution identities or conditions differ")
    if len(previous.get("trainPods", [])) != 1:
        raise ValueError("no unique recorded external training Pod")
    stored = previous["trainPods"][0]
    pod = core.read_namespaced_pod(stored["name"], args.namespace, _request_timeout=30)
    if (pod.metadata.uid != stored["uid"] or pod.status.phase != "Succeeded" or pod.spec.service_account_name != "trainer-workload"
            or pod.spec.automount_service_account_token is not False or pod.spec.node_name != args.trainer_node
            or len(pod.status.container_statuses or []) != 1 or pod.status.container_statuses[0].state.terminated.exit_code != 0
            or pod.status.container_statuses[0].image_id.rsplit("@", 1)[-1] != args.image.rsplit("@", 1)[-1]):
        raise RuntimeError("current training Pod differs from the original receipt")
    record.update(previous)
    record["run"]["details"] = pipeline._run_api.api_client.sanitize_for_serialization(observed.run_details) if observed.run_details else None
    record.update(status="RUN_STATE_CORRELATED", workspaceRetained=True, fileReadback="NOT_ATTESTED_BY_THIS_CLIENT",
                  acceptance="EAC_NOT_ATTESTED", reconciliation={"sourceReportSha256": hashlib.sha256(source).hexdigest(), "persistentWrites": 0})
    save(report, record)


if __name__ == "__main__":
    parser = argparse.ArgumentParser()
    for name in ("api", "api-ca", "api-token", "kfp", "kfp-ca", "kfp-token", "namespace",
                 "pipeline", "image", "runtime", "trainer-node", "storage-class", "size", "output"):
        parser.add_argument("--" + name, required=True)
    parser.add_argument("--mode", choices=("success", "fail", "stop"), required=True)
    parser.add_argument("--reconcile-success-report", help="read-only finalization of a confirmed completed execution; never recreates it")
    parser.add_argument("--reconcile-stop-report", help="read-only finalization after both original stop requests were confirmed")
    parser.add_argument("--resume-stop-report", help="resume a confirmed running stop execution before any stop mutation; never creates a replacement")
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
