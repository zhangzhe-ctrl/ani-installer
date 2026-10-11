"""Exercise the native patch helper; only the API server boundary is replaced."""
import copy
import importlib.util
import json
import pathlib
import sys
import tempfile
import unittest

source = pathlib.Path(sys.argv.pop(1)).resolve()
sys.path.insert(0, str(source.parent.parent))
spec = importlib.util.spec_from_file_location("native_contracts", source)
contracts = importlib.util.module_from_spec(spec)
spec.loader.exec_module(contracts)


class ApiBoundary:
    def __init__(self, directory, change=None, reject=None):
        self.directory = pathlib.Path(directory)
        self.writes = []
        self.calls = []
        self.change = change
        self.reject = reject
        self.live = {
            "metadata": {"name": "native", "namespace": "probe", "uid": "original-uid",
                         "resourceVersion": "7", "annotations": {"existing": "preserve"}},
            "spec": {"template": {"spec": {"containers": [{"name": "kernel"}]}}},
            "status": {"ready": True},
        }

    def owned(self, value):
        return copy.deepcopy(self.live)

    def call(self, args):
        self.calls.append(args)
        dry_run = "--dry-run=server" in args
        if self.reject == ("preflight" if dry_run else "write"):
            raise RuntimeError("Forbidden: admission rejected the request")
        patch = json.loads(args[args.index("--patch") + 1])
        for operation in patch:
            if operation["op"] == "test":
                field = operation["path"].rsplit("/", 1)[1]
                if self.live["metadata"][field] != operation["value"]:
                    raise RuntimeError("The request is invalid: the server rejected our request")
        result = copy.deepcopy(self.live)
        result["metadata"]["annotations"]["kubeflow-resource-stopped"] = "owned-stamp"
        if dry_run:
            if self.change:
                self.change(self.live)
        else:
            self.live = result
        return json.dumps(result)


class LifecyclePatchFeedback(unittest.TestCase):
    value = {"apiVersion": "kubeflow.org/v1", "kind": "Notebook",
             "metadata": {"name": "native", "namespace": "probe"}}
    operations = [{"op": "add", "path": "/metadata/annotations/kubeflow-resource-stopped",
                   "value": "owned-stamp"}]

    def invoke(self, cluster):
        contracts.patch_owned(cluster, self.value, self.operations)

    def test_controller_status_update_after_preflight(self):
        def status_update(live):
            live["metadata"]["resourceVersion"] = "8"
            live["status"]["controllerObservation"] = "updated"
        with tempfile.TemporaryDirectory() as directory:
            cluster = ApiBoundary(directory, status_update)
            self.invoke(cluster)
            self.assertEqual(cluster.live["metadata"]["uid"], "original-uid")
            self.assertEqual(cluster.live["metadata"]["annotations"]["existing"], "preserve")
            self.assertEqual(cluster.live["metadata"]["annotations"]["kubeflow-resource-stopped"], "owned-stamp")
            submitted = json.loads(cluster.calls[-1][cluster.calls[-1].index("--patch") + 1])
            self.assertEqual(submitted[1]["value"], "8")
            self.assertEqual(cluster.writes[-1]["result"], "CONFIRMED")

    def test_replacement_is_rejected_before_mutation(self):
        def replace(live):
            live["metadata"].update(uid="replacement-uid", resourceVersion="8")
        with tempfile.TemporaryDirectory() as directory:
            cluster = ApiBoundary(directory, replace)
            with self.assertRaisesRegex(ValueError, "identity"):
                self.invoke(cluster)
            self.assertEqual(len(cluster.calls), 1)
            self.assertEqual(cluster.writes, [])

    def test_concurrent_target_edit_is_rejected_before_mutation(self):
        for area in ("spec", "annotations"):
            def edit(live):
                live["metadata"]["resourceVersion"] = "8"
                if area == "spec":
                    live["spec"]["another-owner"] = "preserve"
                else:
                    live["metadata"]["annotations"]["another-owner"] = "preserve"
            with self.subTest(area=area), tempfile.TemporaryDirectory() as directory:
                cluster = ApiBoundary(directory, edit)
                with self.assertRaisesRegex(ValueError, "changed"):
                    self.invoke(cluster)
                self.assertEqual(len(cluster.calls), 1)
                self.assertEqual(cluster.writes, [])

    def test_preflight_and_write_failures_propagate(self):
        for stage, calls in (("preflight", 1), ("write", 2)):
            with self.subTest(stage=stage), tempfile.TemporaryDirectory() as directory:
                cluster = ApiBoundary(directory, reject=stage)
                with self.assertRaisesRegex(RuntimeError, "Forbidden"):
                    self.invoke(cluster)
                self.assertEqual(len(cluster.calls), calls)
                if stage == "write":
                    self.assertEqual(cluster.writes[-1]["result"], "UNKNOWN")
                else:
                    self.assertEqual(cluster.writes, [])

    def test_unchanged_object_keeps_atomic_uid_and_version_guards(self):
        with tempfile.TemporaryDirectory() as directory:
            cluster = ApiBoundary(directory)
            self.invoke(cluster)
            submitted = json.loads(cluster.calls[-1][cluster.calls[-1].index("--patch") + 1])
            self.assertEqual(submitted[:2], [
                {"op": "test", "path": "/metadata/uid", "value": "original-uid"},
                {"op": "test", "path": "/metadata/resourceVersion", "value": "7"},
            ])


unittest.main()
