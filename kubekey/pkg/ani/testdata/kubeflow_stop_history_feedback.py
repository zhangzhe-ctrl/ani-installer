"""Exercise the production stop verifier; replace only its remote API boundary."""
import ast
import copy
import pathlib
import sys
import time
import types
import unittest

source = pathlib.Path(sys.argv.pop(1))
tree = ast.parse(source.read_text())
function = next(node for node in tree.body if isinstance(node, ast.FunctionDef) and node.name == "verify_stopped")
production = {"time": time}
exec(compile(ast.Module(body=[function], type_ignores=[]), str(source), "exec"), production)


class StopHistoryFeedback(unittest.TestCase):
    def setUp(self):
        self.history = [
            {"update_time": "2026-10-11T18:56:20+00:00", "state": "PENDING"},
            {"update_time": "2026-10-11T18:56:22+00:00"},
            {"update_time": "2026-10-11T18:56:23+00:00", "state": "RUNNING"},
            {"update_time": "2026-10-11T18:57:47+00:00", "state": "CANCELING"},
            {"update_time": "2026-10-11T18:57:57+00:00", "state": "FAILED"},
        ]
        self.record = {
            "execution": "env-original",
            "creationReceipt": {"trainJobUid": "train-original", "trainJobName": "original",
                                "kfpRunId": "run-original", "controlPodUid": "control-original"},
            "stop": {"trainJobResult": "CONFIRMED", "runResult": "CONFIRMED",
                     "trainJobUid": "train-original", "podUids": ["worker-original"]},
            "trainJob": {},
        }
        self.job = {"metadata": {"uid": "train-original"}, "spec": {"suspend": True},
                    "status": {"conditions": [{"type": "Suspended", "status": "True"}]}}
        self.workflow = {"metadata": {"uid": "workflow-original"},
                         "spec": {"activeDeadlineSeconds": 0}, "status": {"phase": "Failed"}}
        self.control = types.SimpleNamespace(metadata=types.SimpleNamespace(
            uid="control-original", owner_references=[types.SimpleNamespace(kind="Workflow", uid="workflow-original")]))
        self.current = []

    def verify(self):
        pipeline = types.SimpleNamespace(_run_api=types.SimpleNamespace(api_client=types.SimpleNamespace(
            sanitize_for_serialization=lambda value: copy.deepcopy(value))))
        custom = types.SimpleNamespace(
            get_namespaced_custom_object=lambda *args, **kwargs: copy.deepcopy(self.job),
            list_namespaced_custom_object=lambda *args, **kwargs: {"items": [copy.deepcopy(self.workflow)]})
        core = types.SimpleNamespace(list_namespaced_pod=lambda *args, **kwargs:
            types.SimpleNamespace(items=self.current if "label_selector" in kwargs else [self.control]))
        observed = types.SimpleNamespace(run_id="run-original", state="FAILED", state_history=self.history)
        production["verify_stopped"](types.SimpleNamespace(namespace="probe"), self.record, pipeline, core, custom, observed)

    def test_actual_sparse_history_preserved_with_original_stop_proof(self):
        before = copy.deepcopy(self.history)
        self.verify()
        self.assertEqual(self.record["stop"]["stateHistory"], before)
        self.assertNotIn("state", self.record["stop"]["stateHistory"][1])
        self.assertEqual(self.record["stop"]["result"], "CONFIRMED")
        self.assertTrue(self.record["stop"]["actualExternalPodsGone"])

    def test_absent_terminal_state_rejected(self):
        self.history[-1].pop("state")
        with self.assertRaises(RuntimeError):
            self.verify()

    def test_absent_running_state_rejected(self):
        self.history = [row for row in self.history if row.get("state") != "RUNNING"]
        with self.assertRaises(RuntimeError):
            self.verify()

    def test_replaced_trainjob_rejected(self):
        self.job["metadata"]["uid"] = "replacement"
        with self.assertRaises(RuntimeError):
            self.verify()

    def test_unconfirmed_terminate_request_rejected(self):
        self.record["stop"]["runResult"] = "UNKNOWN"
        with self.assertRaises(ValueError):
            self.verify()

    def test_workflow_not_terminated_rejected(self):
        self.workflow["spec"]["activeDeadlineSeconds"] = 600
        with self.assertRaises(RuntimeError):
            self.verify()

    def test_replacement_worker_after_stop_rejected(self):
        self.current = [types.SimpleNamespace(metadata=types.SimpleNamespace(uid="replacement"))]
        with self.assertRaises(RuntimeError):
            self.verify()


unittest.main()
