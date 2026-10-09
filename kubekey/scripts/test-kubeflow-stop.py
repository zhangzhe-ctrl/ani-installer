"""Exercise the real stop verifier against externally sampled KFP state history."""
import importlib.util
import pathlib
import sys
import types
import unittest
from types import SimpleNamespace as N

# Only SDK imports and responses are doubles. The stop proof logic is real.
sys.modules.setdefault('kfp', types.ModuleType('kfp'))
kubernetes = types.ModuleType('kubernetes')
kubernetes.client = types.ModuleType('client')
sys.modules.setdefault('kubernetes', kubernetes)
spec = importlib.util.spec_from_file_location('stop_probe', pathlib.Path(__file__).resolve().parents[1] / 'ani/kubeflow/probes/run.py')
probe = importlib.util.module_from_spec(spec)
spec.loader.exec_module(probe)


class StopProof(unittest.TestCase):
    def setUp(self):
        self.record = {'execution': 'env-proof', 'creationReceipt': {'kfpRunId': 'run-proof', 'trainJobName': 'train-proof', 'trainJobUid': 'train-uid', 'controlPodUid': 'control-uid'},
                       'stop': {'trainJobUid': 'train-uid', 'trainJobResult': 'CONFIRMED', 'runResult': 'CONFIRMED', 'podUids': ['original-training-uid']}, 'trainJob': {}}
        self.job = {'metadata': {'uid': 'train-uid'}, 'spec': {'suspend': True}, 'status': {'conditions': [{'type': 'Suspended', 'status': 'True'}]}}
        self.workflow = {'metadata': {'uid': 'workflow-uid'}, 'spec': {'activeDeadlineSeconds': 0}, 'status': {'phase': 'Failed'}}
        self.history = [{'state': 'PENDING'}, {'state': 'RUNNING'}, {'state': 'FAILED'}]
        self.control = N(metadata=N(uid='control-uid', owner_references=[N(kind='Workflow', uid='workflow-uid')]))
        self.current_pods = []
        self.run_id = 'run-proof'

    def verify(self):
        custom = N(get_namespaced_custom_object=lambda *a, **kw: self.job,
                   list_namespaced_custom_object=lambda *a, **kw: {'items': [self.workflow]})
        core = N(list_namespaced_pod=lambda *a, **kw: N(items=self.current_pods if 'label_selector' in kw else [self.control]))
        pipeline = N(_run_api=N(api_client=N(sanitize_for_serialization=lambda value: value)))
        probe.verify_stopped(N(namespace='tenant'), self.record, pipeline, core, custom,
                             N(state='FAILED', state_history=self.history, run_id=self.run_id))

    def test_terminal_workflow_may_skip_transient_canceling_sample(self):
        self.verify()
        self.assertTrue(self.record['stop']['actualExternalPodsGone'])
        self.assertEqual(self.record['stop']['actualRunState'], 'FAILED')
        self.assertEqual(self.record['stop']['cancellationStateHistory'], 'not_observed')
        self.assertEqual(self.record['stop']['stateHistory'], self.history)

    def test_observed_canceling_is_preserved(self):
        self.history.insert(2, {'state': 'CANCELING'})
        self.verify()
        self.assertEqual(self.record['stop']['cancellationStateHistory'], 'observed')

    def test_failure_without_confirmed_stop_is_rejected(self):
        self.record['stop']['runResult'] = 'UNKNOWN'
        with self.assertRaises(ValueError): self.verify()

    def test_workflow_without_actual_termination_is_rejected(self):
        del self.workflow['spec']['activeDeadlineSeconds']
        with self.assertRaises(RuntimeError): self.verify()

    def test_trainjob_without_actual_suspend_is_rejected(self):
        self.job['spec']['suspend'] = False
        with self.assertRaises(RuntimeError): self.verify()

    def test_different_original_control_owner_is_rejected(self):
        self.control.metadata.owner_references[0].uid = 'foreign-workflow'
        with self.assertRaises(RuntimeError): self.verify()

    def test_terminal_history_must_correspond_to_actual_run(self):
        self.history[-1]['state'] = 'RUNNING'
        with self.assertRaises(RuntimeError): self.verify()

    def test_replacement_training_pod_after_stop_is_rejected(self):
        self.current_pods = [N(metadata=N(uid='replacement-training-uid'))]
        with self.assertRaises(RuntimeError): self.verify()

    def test_different_run_identity_is_rejected(self):
        self.run_id = 'foreign-run'
        with self.assertRaises(RuntimeError): self.verify()


if __name__ == '__main__': unittest.main()
