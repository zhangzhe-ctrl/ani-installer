#!/usr/bin/env python3
"""Exercise the shipped material CLI with approved component directories."""
import ast
import hashlib
import json
import pathlib
import shutil
import subprocess
import tempfile
import unittest

import yaml

ROOT = pathlib.Path(__file__).resolve().parent.parent
PROGRAM = ROOT / 'ani/kubeflow/prepare-bundle.py'
RELEASE = '26.03-kubeflow-stage2-v1'


class MaterialLayout(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = pathlib.Path(self.tmp.name) / 'source'
        self.destination = pathlib.Path(self.tmp.name) / 'material/manifests/kubeflow' / RELEASE
        self.destination.mkdir(parents=True)
        tree = ast.parse(PROGRAM.read_text())
        files = next(ast.literal_eval(n.value) for n in tree.body
                     if isinstance(n, ast.Assign) and any(isinstance(t, ast.Name) and t.id == 'FILES' for t in n.targets))
        for name, rel in files.items():
            target = self.root / rel
            target.parent.mkdir(parents=True, exist_ok=True)
            shutil.copyfile(ROOT / rel, target)
        # Only external wheel/archive bytes are replaced by small, independently
        # approved fixture materials. The real CLI and component assets run.
        wheel = b'isolated external wheel material'
        for lock, directory in [('execution-image.lock.json', 'wheels'), ('workspace-image.lock.json', 'workspace-wheels')]:
            path = self.root / files[lock]
            value = json.loads(path.read_text())
            value['wheels'] = [{'file': 'fixture.whl', 'sha256': hashlib.sha256(wheel).hexdigest()}]
            path.write_text(json.dumps(value))
            dest = self.destination / directory
            dest.mkdir()
            (dest / 'fixture.whl').write_bytes(wheel)
        grpc = b'isolated external grpc source archive'
        (self.destination / 'grpc-source.tar').write_bytes(grpc)
        lock = yaml.safe_load((ROOT / 'ani/components.lock.yaml').read_text())
        lock['tools']['ani-kfp-grpc-check']['sourceTarballSha256'] = hashlib.sha256(grpc).hexdigest()
        (self.root / 'ani/components.lock.yaml').write_text(yaml.safe_dump(lock))
        approval = json.loads((ROOT / 'pkg/ani/kubeflow-assets.lock.json').read_text())
        for name, rel in files.items():
            data = (self.root / rel).read_bytes()
            approval['files'][name] = hashlib.sha256(data).hexdigest()
            (self.destination / name).write_bytes(data)
        (self.root / 'pkg/ani').mkdir(parents=True)
        approved = json.dumps(approval).encode()
        (self.root / 'pkg/ani/kubeflow-assets.lock.json').write_bytes(approved)
        (self.destination / 'assets.lock.json').write_bytes(approved)
        self.higress = self.destination.parent.parent / 'higress/2.2.4'
        self.higress.mkdir(parents=True)
        approval_path = ROOT / 'pkg/ani/higress-assets.lock.json'
        shutil.copyfile(approval_path, self.root / 'pkg/ani/higress-assets.lock.json')
        shutil.copyfile(approval_path, self.higress / 'assets.lock.json')
        for name in json.loads(approval_path.read_text())['files']:
            rel = 'tasks/main.yaml' if name == 'tasks.yaml' else 'templates/' + name
            shutil.copyfile(ROOT / 'builtin/core/roles/ani/higress' / rel, self.higress / name)

    def verify(self):
        return subprocess.run(['python3', str(PROGRAM), '--source-root', str(self.root),
                               '--verify-output', str(self.destination)], text=True, capture_output=True)

    def test_approved_higress_and_kubeflow_can_coexist(self):
        result = self.verify()
        self.assertEqual(result.returncode, 0, result.stderr)

    def test_original_kubeflow_only_materials_remain_supported(self):
        path = self.root / 'ani/components.lock.yaml'
        value = yaml.safe_load(path.read_text())
        del value['batchHigress']
        path.write_text(yaml.safe_dump(value))
        shutil.rmtree(self.higress.parent)
        result = self.verify()
        self.assertEqual(result.returncode, 0, result.stderr)

    def test_unknown_component_is_rejected(self):
        (self.destination.parent.parent / 'unapproved').mkdir()
        result = self.verify()
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('unknown manifest material directory', result.stderr)

    def test_modified_higress_driver_is_rejected(self):
        (self.higress / 'manage.py').write_text('unapproved replacement')
        result = self.verify()
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('Higress material differs from source-approved bytes', result.stderr)

    def test_unknown_higress_release_is_rejected(self):
        (self.higress.parent / 'unapproved').mkdir()
        result = self.verify()
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('unknown Higress release material', result.stderr)


if __name__ == '__main__':
    unittest.main()
