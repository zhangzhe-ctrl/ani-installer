"""Bounded environment probe, compiled remotely with the locked KFP SDK.

The external TrainJob executes with trainer-workload, no API token or S3
credentials. The KFP component is the separately trusted environment step.
This is not modeldev's formal workspace owner or business training adapter.
"""
from kfp import compiler, dsl, kubernetes
from kfp.dsl import Artifact, Dataset, Input, Metrics, Model, Output


def prepare_dataset(execution: str, dataset: Output[Dataset]):
    import json
    import pathlib
    value = {"execution": execution, "x": [0.0, 1.0, 2.0, 3.0], "y": [1.0, 3.0, 5.0, 7.0]}
    pathlib.Path(dataset.path).write_text(json.dumps(value, sort_keys=True))
    dataset.metadata["environment_probe"] = True
    dataset.metadata["execution"] = execution


def external_training(claim: str, claim_uid: str, execution: str, run_id: str,
                      namespace: str, runtime_name: str, mode: str, trainer_node: str,
                      dataset: Input[Dataset], model: Output[Model], link: Output[Artifact], metrics: Output[Metrics]):
    import errno
    import hashlib
    import json
    import os
    import pathlib
    import shutil
    import time
    from kubernetes import client, config
    from kubernetes.client.exceptions import ApiException

    if mode not in ("success", "fail", "stop") or not execution.startswith("env-"):
        raise ValueError("bounded environment probe mode or identity differs")
    config.load_incluster_config()
    core, custom = client.CoreV1Api(), client.CustomObjectsApi()
    live_claim = core.read_namespaced_persistent_volume_claim(claim, namespace)
    if live_claim.metadata.uid != claim_uid or live_claim.metadata.owner_references:
        raise RuntimeError("workspace UID/lifecycle differs from the create response")
    workspace = pathlib.Path("/workspace")
    input_dir, output_dir = workspace / "input", workspace / "output"
    input_dir.mkdir(mode=0o755)
    output_dir.mkdir(mode=0o755)
    payload = pathlib.Path(dataset.path).read_bytes()
    if json.loads(payload)["execution"] != execution:
        raise RuntimeError("downloaded input artifact belongs to another execution")
    (input_dir / "dataset.json").write_bytes(payload)
    os.chmod(input_dir / "dataset.json", 0o444)
    input_hash = hashlib.sha256(payload).hexdigest()
    training = r'''
import errno, hashlib, json, os, pathlib, sys, time
mode, execution, expected = sys.argv[1:]
payload = pathlib.Path('/input/dataset.json').read_bytes()
assert hashlib.sha256(payload).hexdigest() == expected
assert not pathlib.Path('/var/run/secrets/kubernetes.io/serviceaccount/token').exists()
assert not any(k in os.environ for k in ('AWS_ACCESS_KEY_ID', 'AWS_SECRET_ACCESS_KEY', 'MYSQL_ROOT_PASSWORD'))
try:
    pathlib.Path('/input/write-negative').write_text('must-not-write')
except OSError as error:
    assert error.errno in (errno.EROFS, errno.EACCES)
    readonly_errno = error.errno
else:
    raise RuntimeError('input mount was writable')
unique = pathlib.Path('/output/unique.json')
with unique.open('x') as stream:
    json.dump({'execution': execution, 'inputSha256': expected, 'readonlyErrno': readonly_errno}, stream)
    stream.flush(); os.fsync(stream.fileno())
if mode == 'fail':
    sys.exit(42)
if mode == 'stop':
    time.sleep(600)
    raise RuntimeError('stop probe was not stopped before its deadline')
data = json.loads(payload)
w, b = 0.0, 0.0
for _ in range(100):
    residuals = [w*x+b-y for x,y in zip(data['x'], data['y'])]
    w -= 0.02 * 2 * sum(r*x for r,x in zip(residuals,data['x']))/len(residuals)
    b -= 0.02 * 2 * sum(residuals)/len(residuals)
loss = sum((w*x+b-y)**2 for x,y in zip(data['x'],data['y']))/len(data['x'])
assert loss < 0.1
result = {'execution': execution, 'weight': w, 'bias': b, 'loss': loss, 'inputSha256': expected}
with pathlib.Path('/output/model.json').open('x') as stream:
    json.dump(result, stream, sort_keys=True); stream.flush(); os.fsync(stream.fileno())
print(json.dumps({'execution': execution, 'loss': loss, 'inputSha256': expected}))
'''
    name = "ani-kfp-train-" + execution
    job = {"apiVersion": "trainer.kubeflow.org/v1alpha1", "kind": "TrainJob",
        "metadata": {"name": name, "namespace": namespace,
            "labels": {"ani.io/execution-id": execution},
            "annotations": {"ani.io/kfp-run-id": run_id, "ani.io/workspace-name": claim, "ani.io/workspace-uid": claim_uid}},
        "spec": {"runtimeRef": {"name": runtime_name}, "trainer": {"numNodes": 1, "numProcPerNode": 1,
            "command": ["python", "-c", training], "args": [mode, execution, input_hash]},
            "labels": {"ani.io/execution-id": execution},
            "podTemplateOverrides": [{"targetJobs": [{"name": "node"}],
                "metadata": {"labels": {"ani.io/execution-id": execution}},
                "spec": {"serviceAccountName": "trainer-workload", "nodeSelector": {"kubernetes.io/hostname": trainer_node},
                    "volumes": [{"name": "workspace", "persistentVolumeClaim": {"claimName": claim}}],
                    "containers": [{"name": "node", "volumeMounts": [
                        {"name": "workspace", "mountPath": "/input", "subPath": "input", "readOnly": True},
                        {"name": "workspace", "mountPath": "/output", "subPath": "output", "readOnly": False}]}]}}]}}
    try:
        custom.get_namespaced_custom_object("trainer.kubeflow.org", "v1alpha1", namespace, "trainjobs", name)
    except ApiException as error:
        if error.status != 404:
            raise
    else:
        raise RuntimeError("probe name occupied; never delete/recreate by name")
    custom.create_namespaced_custom_object("trainer.kubeflow.org", "v1alpha1", namespace, "trainjobs", job, dry_run="All")
    correlation = {"schema": "ani.kubeflow.handoff-probe.v1", "execution": execution, "kfpRunId": run_id,
                   "namespace": namespace, "workspace": {"name": claim, "uid": claim_uid},
                   "trainJob": {"name": name, "creationResult": "UNKNOWN"}, "inputSha256": input_hash}
    (output_dir / "correlation.json").write_text(json.dumps(correlation, sort_keys=True))
    created = custom.create_namespaced_custom_object("trainer.kubeflow.org", "v1alpha1", namespace, "trainjobs", job)
    job_uid = created["metadata"]["uid"]
    correlation["trainJob"].update(uid=job_uid, creationResult="CONFIRMED")
    (output_dir / "correlation.json").write_text(json.dumps(correlation, sort_keys=True))
    deadline = time.monotonic() + 480
    while time.monotonic() < deadline:
        current = custom.get_namespaced_custom_object("trainer.kubeflow.org", "v1alpha1", namespace, "trainjobs", name)
        if current["metadata"]["uid"] != job_uid:
            raise RuntimeError("TrainJob UID changed during the probe")
        conditions = {c["type"]: c["status"] for c in current.get("status", {}).get("conditions", [])}
        correlation["trainJob"]["conditions"] = conditions
        (output_dir / "correlation.json").write_text(json.dumps(correlation, sort_keys=True))
        if conditions.get("Failed") == "True":
            raise RuntimeError("external TrainJob failed; retain this execution PVC and unique output")
        if conditions.get("Complete") == "True":
            break
        time.sleep(3)
    else:
        raise TimeoutError("external TrainJob deadline; preserve and reconcile, no blind replacement")
    if core.read_namespaced_persistent_volume_claim(claim, namespace).metadata.uid != claim_uid:
        raise RuntimeError("workspace identity changed across Pods")
    pods = core.list_namespaced_pod(namespace, label_selector="ani.io/execution-id=" + execution).items
    train_pods = [p for p in pods if p.spec.service_account_name == "trainer-workload"]
    if len(train_pods) != 1 or train_pods[0].status.phase != "Succeeded":
        raise RuntimeError("expected one successful external training Pod")
    training_pod = train_pods[0]
    if training_pod.spec.automount_service_account_token is not False or training_pod.spec.node_name != trainer_node:
        raise RuntimeError("training token/node contract differs")
    if any(v.secret or v.projected for v in training_pod.spec.volumes):
        raise RuntimeError("ordinary training Pod acquired a Secret/projected token")
    mounts = {v.mount_path: v for v in training_pod.spec.containers[0].volume_mounts}
    if not mounts["/input"].read_only or mounts["/input"].sub_path != "input" or mounts["/output"].sub_path != "output":
        raise RuntimeError("training mount contract differs")
    claims = [v.persistent_volume_claim.claim_name for v in training_pod.spec.volumes if v.persistent_volume_claim]
    if claims != [claim]:
        raise RuntimeError("training Pod mounted a different execution claim")
    value = json.loads((output_dir / "model.json").read_text())
    if value["execution"] != execution or value["inputSha256"] != input_hash:
        raise RuntimeError("external training output belongs to another execution")
    shutil.copyfile(output_dir / "model.json", model.path)
    correlation.update(trainPod={"name": training_pod.metadata.name, "uid": training_pod.metadata.uid, "node": training_pod.spec.node_name},
                       pipelineNode=os.environ.get("ANI_PROBE_NODE"), outputSha256=hashlib.sha256(pathlib.Path(model.path).read_bytes()).hexdigest(), loss=value["loss"])
    jobsets = custom.list_namespaced_custom_object("jobset.x-k8s.io", "v1alpha2", namespace, "jobsets")["items"]
    owned = [item for item in jobsets if any(reference["uid"] == job_uid for reference in item["metadata"].get("ownerReferences", []))]
    if len(owned) != 1:
        raise RuntimeError("external TrainJob has no unique UID-owned JobSet")
    correlation["jobSet"] = {"name": owned[0]["metadata"]["name"], "uid": owned[0]["metadata"]["uid"]}
    pathlib.Path(link.path).write_text(json.dumps(correlation, sort_keys=True))
    for key, value in {"kfp_run_id": run_id, "train_job_uid": job_uid, "workspace_uid": claim_uid, "execution": execution}.items():
        link.metadata[key] = value
        model.metadata[key] = value
    metrics.log_metric("loss", correlation["loss"])
    metrics.log_metric("single_process", 1)


def compile_probe(image, output, pipeline_node):
    dataset_component = dsl.component(prepare_dataset, base_image=image, install_kfp_package=False)
    training_component = dsl.component(external_training, base_image=image, install_kfp_package=False)

    @dsl.pipeline(name="ani-environment-handoff")
    def pipeline(claim: str, claim_uid: str, execution: str, namespace: str,
                 runtime_name: str, trainer_node: str, mode: str = "success"):
        dataset = dataset_component(execution=execution)
        trained = training_component(claim=claim, claim_uid=claim_uid, execution=execution,
            run_id=dsl.PIPELINE_JOB_ID_PLACEHOLDER, namespace=namespace, runtime_name=runtime_name,
            mode=mode, trainer_node=trainer_node, dataset=dataset.outputs["dataset"])
        trained.set_caching_options(False)
        dataset.set_caching_options(False)
        kubernetes.mount_pvc(trained, pvc_name=claim, mount_path="/workspace")
        kubernetes.add_node_selector(trained, "kubernetes.io/hostname", pipeline_node)
        kubernetes.use_field_path_as_env(trained, env_name="ANI_PROBE_NODE", field_path="spec.nodeName")
        trained.set_cpu_request("100m").set_memory_request("256Mi").set_cpu_limit("1").set_memory_limit("1Gi")
        dataset.set_cpu_request("100m").set_memory_request("128Mi").set_cpu_limit("500m").set_memory_limit("512Mi")
    compiler.Compiler().compile(pipeline_func=pipeline, package_path=output)


if __name__ == "__main__":
    import argparse
    parser = argparse.ArgumentParser()
    parser.add_argument("--image", required=True)
    parser.add_argument("--output", required=True)
    parser.add_argument("--pipeline-node", required=True)
    arguments = parser.parse_args()
    compile_probe(arguments.image, arguments.output, arguments.pipeline_node)
