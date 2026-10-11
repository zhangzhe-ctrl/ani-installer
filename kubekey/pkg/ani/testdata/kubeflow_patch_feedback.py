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
    def __init__(self, directory, change=None, reject=None, write_change=None):
        self.directory = pathlib.Path(directory)
        self.writes = []
        self.calls = []
        self.change = change
        self.reject = reject
        self.write_change = write_change
        self.live = {
            "metadata": {"name": "native", "namespace": "probe", "uid": "original-uid",
                         "resourceVersion": "7", "annotations": {"existing": "preserve"}},
            "spec": {"template": {"spec": {"containers": [{"name": "kernel", "image": "example.invalid/kernel:original"}]}}},
            "status": {"ready": True},
        }

    def owned(self, value):
        return copy.deepcopy(self.live)

    def call(self, args):
        self.calls.append(args)
        dry_run = "--dry-run=server" in args
        if self.reject == ("preflight" if dry_run else "write"):
            raise RuntimeError("Forbidden: admission rejected the request")
        if not dry_run and self.write_change:
            self.write_change(self.live)
        patch = json.loads(args[args.index("--patch") + 1])
        result = copy.deepcopy(self.live)
        # This is the external API's atomic document boundary. Operations run
        # on a private copy; a failed condition never persists earlier edits.
        def parent(path):
            parts = [part.replace("~1", "/").replace("~0", "~") for part in path.split("/")[1:]]
            value = result
            for part in parts[:-1]:
                value = value[part]
            return value, parts[-1]
        try:
            for operation in patch:
                target, field = parent(operation["path"])
                if operation["op"] == "test":
                    if target[field] != operation["value"]:
                        raise KeyError("condition differs")
                elif operation["op"] == "copy":
                    origin, key = parent(operation["from"])
                    target[field] = copy.deepcopy(origin[key])
                elif operation["op"] == "add":
                    target[field] = copy.deepcopy(operation["value"])
                elif operation["op"] == "remove":
                    del target[field]
                else:
                    raise AssertionError("unsupported API boundary operation")
        except KeyError as error:
            raise RuntimeError("The request is invalid: the server rejected our request") from error
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
            self.assertFalse(any(op["op"] == "test" and op["path"] == "/metadata/resourceVersion" for op in submitted))
            self.assertEqual(cluster.writes[-1]["result"], "CONFIRMED")

    def test_controller_status_update_after_final_read(self):
        def status_update(live):
            live["metadata"]["resourceVersion"] = "9"
            live["metadata"]["managedFields"] = [{"manager": "notebook-controller", "subresource": "status"}]
            live["status"]["controllerObservation"] = "updated-during-submit"
        with tempfile.TemporaryDirectory() as directory:
            cluster = ApiBoundary(directory, write_change=status_update)
            self.invoke(cluster)
            self.assertEqual(cluster.live["metadata"]["annotations"], {
                "existing": "preserve", "kubeflow-resource-stopped": "owned-stamp"})
            self.assertEqual(cluster.live["status"]["controllerObservation"], "updated-during-submit")
            self.assertNotIn("_ani_lifecycle_guard", cluster.live["metadata"])
            self.assertEqual(len(cluster.calls), 2)
            self.assertEqual(cluster.writes[-1]["result"], "CONFIRMED")

    def test_late_target_edits_are_rejected_atomically(self):
        for area in ("uid", "spec", "annotations", "labels", "ownerReferences", "finalizers"):
            def edit(live):
                live["metadata"]["resourceVersion"] = "9"
                if area == "spec":
                    live["spec"]["template"]["spec"]["containers"][0]["image"] = "example.invalid/kernel:foreign"
                elif area == "uid":
                    live["metadata"]["uid"] = "replacement-uid"
                elif area == "ownerReferences":
                    live["metadata"][area] = [{"apiVersion": "v1", "kind": "ConfigMap",
                                               "name": "foreign", "uid": "foreign-uid"}]
                elif area == "finalizers":
                    live["metadata"][area] = ["another-owner.example/retain"]
                else:
                    live["metadata"].setdefault(area, {})["another-owner"] = "preserve"
            with self.subTest(area=area), tempfile.TemporaryDirectory() as directory:
                cluster = ApiBoundary(directory, write_change=edit)
                with self.assertRaisesRegex(RuntimeError, "invalid"):
                    self.invoke(cluster)
                self.assertNotIn("kubeflow-resource-stopped", cluster.live["metadata"]["annotations"])
                self.assertNotIn("_ani_lifecycle_guard", cluster.live["metadata"])

    def test_late_added_annotation_map_is_not_overwritten(self):
        def edit(live):
            live["metadata"].update(resourceVersion="9", annotations={"another-owner": "preserve"})
        with tempfile.TemporaryDirectory() as directory:
            cluster = ApiBoundary(directory, write_change=edit)
            del cluster.live["metadata"]["annotations"]
            operations = [{"op": "add", "path": "/metadata/annotations",
                           "value": {"kubeflow-resource-stopped": "owned-stamp"}}]
            with self.assertRaisesRegex(RuntimeError, "invalid"):
                contracts.patch_owned(cluster, self.value, operations)
            self.assertEqual(cluster.live["metadata"]["annotations"], {"another-owner": "preserve"})

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
                    live["spec"]["template"]["spec"]["containers"][0]["image"] = "example.invalid/kernel:foreign"
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

    def test_unchanged_object_keeps_atomic_uid_spec_and_metadata_guards(self):
        with tempfile.TemporaryDirectory() as directory:
            cluster = ApiBoundary(directory)
            self.invoke(cluster)
            submitted = json.loads(cluster.calls[-1][cluster.calls[-1].index("--patch") + 1])
            self.assertEqual(submitted[0], {"op": "test", "path": "/metadata/uid", "value": "original-uid"})
            tests = {op["path"]: op["value"] for op in submitted if op["op"] == "test"}
            self.assertEqual(tests["/spec"], cluster.live["spec"])
            self.assertEqual(tests["/metadata/_ani_lifecycle_guard"], {
                "name": "native", "namespace": "probe", "uid": "original-uid",
                "annotations": {"existing": "preserve"}})
            self.assertNotIn("_ani_lifecycle_guard", cluster.live["metadata"])

    def test_existing_reserved_guard_is_rejected_before_any_request(self):
        with tempfile.TemporaryDirectory() as directory:
            cluster = ApiBoundary(directory)
            cluster.live["metadata"]["_ani_lifecycle_guard"] = "preserve"
            with self.assertRaisesRegex(ValueError, "reserved"):
                self.invoke(cluster)
            self.assertEqual(cluster.calls, [])
            self.assertEqual(cluster.writes, [])


unittest.main()
