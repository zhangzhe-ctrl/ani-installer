"""Local contract tests with synthetic OCI/Docker manifests and a fake skopeo CLI.
These do NOT prove public registries reachable, real skopeo compatible, or ANI running.
"""
import argparse
import contextlib
import hashlib
import importlib.util
import io
import json
import os
from pathlib import Path
import subprocess
import sys
import tarfile
import tempfile
import unittest
from unittest.mock import patch

ROOT=Path(__file__).resolve().parents[1]
SCRIPT=ROOT/'scripts/ani_images.py'
spec=importlib.util.spec_from_file_location('ani_images',SCRIPT)
m=importlib.util.module_from_spec(spec);spec.loader.exec_module(m)


def fixture():
    config=json.dumps({'architecture':'amd64','os':'linux','rootfs':{'type':'layers','diff_ids':[]}},separators=(',',':')).encode()
    layer=b'synthetic compressed blob, not a runnable application'
    descriptor=lambda b,mt:{'mediaType':mt,'digest':m.digest(b),'size':len(b)}
    manifest=json.dumps({'schemaVersion':2,'mediaType':'application/vnd.docker.distribution.manifest.v2+json',
        'config':descriptor(config,'application/vnd.docker.container.image.v1+json'),
        'layers':[descriptor(layer,'application/vnd.docker.image.rootfs.diff.tar.gzip')]},separators=(',',':')).encode()
    index=json.dumps({'schemaVersion':2,'mediaType':'application/vnd.oci.image.index.v1+json','manifests':[
        dict(descriptor(manifest,'application/vnd.docker.distribution.manifest.v2+json'),platform={'os':'linux','architecture':'amd64'}),
        {'digest':'sha256:'+'a'*64,'size':123,'platform':{'os':'linux','architecture':'arm64'}}]},separators=(',',':')).encode()
    return config,layer,manifest,index


class TestBundle(unittest.TestCase):
    def setUp(self):
        self.tmp=tempfile.TemporaryDirectory();self.root=Path(self.tmp.name)
        self.c,self.l,self.mf,self.idx=fixture()
        self.image=self.root/'image';self.image.mkdir()
        (self.image/'manifest.json').write_bytes(self.mf)
        for b in [self.c,self.l]:(self.image/m.digest(b).split(':')[1]).write_bytes(b)
    def tearDown(self):self.tmp.cleanup()
    def test_single_platform_manifest_and_blobs(self):
        self.assertEqual(m.verify_image(self.image,m.digest(self.mf),'linux/amd64'),m.digest(self.c))
    def test_corrupt_blob_refused(self):
        (self.image/m.digest(self.l).split(':')[1]).write_bytes(self.l[:-1]+b'X')
        with self.assertRaises(ValueError):m.verify_image(self.image,m.digest(self.mf),'linux/amd64')
    def test_platform_mismatch_refused(self):
        with self.assertRaises(ValueError):m.verify_image(self.image,m.digest(self.mf),'linux/arm64')
    def test_manifest_mismatch_refused(self):
        with self.assertRaises(ValueError):m.verify_image(self.image,'sha256:'+'0'*64,'linux/amd64')
    def test_bare_runtime_id_is_not_a_pull_reference(self):
        with self.assertRaises(ValueError):m.safe_ref('sha256:'+'1'*64)
    def test_no_shell_url_or_path_injection(self):
        for ref in ['$(id)','reg/img;id','https://user:pass@registry/foo','reg/../foo']:
            with self.subTest(ref=ref),self.assertRaises(ValueError):m.safe_ref(ref)
    def test_repository_with_registry_port(self):
        self.assertEqual(m.repository('host.example:5000/team/app:tag'),'host.example:5000/team/app')
    def test_index_resolved_then_child_is_pinned(self):
        args=argparse.Namespace(platform='linux/amd64',authfile=None,cert_dir=None,retries=0,timeout=3)
        with patch.object(m,'run',side_effect=[self.idx,self.mf]) as r:
            selected,chain=m.resolve_image({'sourceRef':'registry.example/a:t'},args,self.root)
        self.assertEqual(selected,'registry.example/a@'+m.digest(self.mf))
        self.assertNotEqual(chain[0][1],chain[1][1])
        self.assertIn('docker://registry.example/a@'+m.digest(self.mf),r.call_args_list[1].args[0])
    def test_requested_digest_mismatch_refused(self):
        args=argparse.Namespace(platform='linux/amd64',authfile=None,cert_dir=None,retries=0,timeout=3)
        with patch.object(m,'run',return_value=self.mf),self.assertRaises(ValueError):
            m.resolve_image({'sourceRef':'registry.example/a@sha256:'+'0'*64},args,self.root)
    def test_bundle_checksums_cover_every_file(self):
        m.write_json(self.root/'bundle.json',{'complete':True,'images':[],'platform':'linux/amd64'})
        m.write_sums(self.root)
        with contextlib.redirect_stdout(io.StringIO()):m.verify_bundle(self.root)
        (self.root/'extra').write_text('not in sums')
        with self.assertRaises(ValueError):m.verify_bundle(self.root)
    def test_partial_is_never_complete(self):
        m.write_json(self.root/'bundle.json',{'complete':False,'images':[],'platform':'linux/amd64'})
        m.write_sums(self.root)
        with self.assertRaises(ValueError):m.verify_bundle(self.root)
    def test_external_path_rejected(self):
        (self.root/'SHA256SUMS').write_text('0'*64+'  ../outside\n')
        with self.assertRaises(ValueError):m.verify_bundle(self.root)

    def _make_user_approved_replacement(self):
        folder=self.root/'images'/'app';folder.mkdir(parents=True)
        self.image.rename(folder/'image')
        old='sha256:'+'9'*64
        meta={'id':'app','manifestDigest':m.digest(self.mf),'configDigest':m.digest(self.c),
              'platform':'linux/amd64',
              'request':{'sourceRef':'registry.example/app:new',
                         'specRefs':['registry.example/app@'+old]},
              'captureComparisons':[{'captured':old,'relation':'not_matched_needs_review'}]}
        m.write_json(self.root/'bundle.json',{'complete':True,'images':[meta],
                     'platform':'linux/amd64','capturedRuntimeIdentityUnconfirmed':['app']})
        m.write_sums(self.root)
        return meta
    def test_user_approved_capture_difference_does_not_reject_valid_download(self):
        meta=self._make_user_approved_replacement()
        with patch.object(m,'run',side_effect=AssertionError('verify must not contact a registry')):
            with contextlib.redirect_stdout(io.StringIO()):doc=m.verify_bundle(self.root)
        self.assertEqual(doc['images'][0]['manifestDigest'],meta['manifestDigest'])
    def test_preview_uses_downloaded_digest_not_historical_spec_digest(self):
        meta=self._make_user_approved_replacement()
        args=argparse.Namespace(bundle=str(self.root),registry='harbor.test/ani',execute=False)
        output=io.StringIO()
        with patch.object(m,'run',side_effect=AssertionError('preview must not write')):
            with contextlib.redirect_stdout(output):self.assertEqual(m.push(args),0)
        self.assertIn('"targetDigestRef": "harbor.test/ani/app@'+meta['manifestDigest']+'"',output.getvalue())


FAKE=r'''#!/usr/bin/env python3
import os,sys,json,hashlib,pathlib
root=pathlib.Path(os.environ['FIXTURE'])
a=sys.argv[1:]
def d(b):return 'sha256:'+hashlib.sha256(b).hexdigest()
with (root/'calls.log').open('a') as f:f.write(json.dumps(a)+'\n')
if any('/missing' in x for x in a):sys.exit(17)
if a[0]=='inspect':
 if (root/'offline').exists():sys.exit(18)
 ref=a[-1];raw=(root/'manifest.json').read_bytes() if '@sha256:' in ref else (root/'index.json').read_bytes()
 sys.stdout.buffer.write(raw)
elif a[0]=='copy':
 if '--preserve-digests' not in a:sys.exit(19)
 source,target=a[-2:]
 if source.startswith('dir:') and target.startswith('docker://'):sys.exit(0)
 if not source.startswith('docker://') or not target.startswith('dir:'):sys.exit(20)
 dest=pathlib.Path(target[4:]);dest.mkdir(exist_ok=True)
 mf=(root/'manifest.json').read_bytes();(dest/'manifest.json').write_bytes(mf)
 for name in ['config.json','layer.gz']:
  b=(root/name).read_bytes();(dest/d(b).split(':')[1]).write_bytes(b)
else:sys.exit(21)
'''

class TestCLI(unittest.TestCase):
    def setUp(self):
        self.tmp=tempfile.TemporaryDirectory();self.root=Path(self.tmp.name)
        c,l,mf,idx=fixture()
        for name,b in [('config.json',c),('layer.gz',l),('manifest.json',mf),('index.json',idx)]:
            (self.root/name).write_bytes(b)
        self.bin=self.root/'bin';self.bin.mkdir();(self.bin/'skopeo').write_text(FAKE);(self.bin/'skopeo').chmod(0o755)
        self.env=dict(os.environ,PATH=str(self.bin)+os.pathsep+os.environ['PATH'],FIXTURE=str(self.root))
        self.request=self.root/'request.json';self.out=self.root/'out'
        self.row={'id':'app','sourceRef':'registry.example/app:fixed','specRefs':['registry.example/app:fixed'],
                  'capturedRuntimeIDs':[m.digest(c)]}
        self.request.write_text(json.dumps({'images':[self.row]}))
        self.args=['export','--manifest',str(self.request),'--out',str(self.out),'--jobs','1','--timeout','5']
    def tearDown(self):self.tmp.cleanup()
    def call(self,args):return subprocess.run([sys.executable,str(SCRIPT)]+args,env=self.env,capture_output=True,text=True,timeout=30)
    def test_export_archive_verify_resume_no_network(self):
        p=self.call(self.args);self.assertEqual(p.returncode,0,p.stdout+p.stderr)
        archive=self.out/'ani-images-bundle.tar.gz'
        expected=(self.out/'ani-images-bundle.tar.gz.sha256').read_text().split()[0]
        self.assertEqual(expected,m.sha_file(archive))
        with tarfile.open(archive) as t:
            doc=json.load(t.extractfile('ani-images-bundle/bundle.json'))
        self.assertEqual(doc['images'][0]['captureComparisons'][0]['relation'],'matches_config')
        before=(self.root/'calls.log').read_text()
        (self.root/'offline').touch()
        p=self.call(self.args);self.assertEqual(p.returncode,0,p.stderr)
        self.assertEqual(before,(self.root/'calls.log').read_text())
        p=self.call(['verify','--bundle',str(self.out/'ani-images-bundle')]);self.assertEqual(p.returncode,0,p.stderr)
    def test_missing_image_partial_rc2_other_image_kept(self):
        missing=dict(self.row,id='missing',sourceRef='registry.example/missing:fixed')
        self.request.write_text(json.dumps({'images':[self.row,missing]}))
        p=self.call(self.args);self.assertEqual(p.returncode,2,p.stdout+p.stderr)
        self.assertTrue((self.out/'ani-images-PARTIAL.tar.gz').exists())
        self.assertFalse((self.out/'ani-images-bundle.tar.gz').exists())
        self.assertEqual(len(m.read_json(self.out/'ani-images-bundle/bundle.json')['images']),1)
    def test_input_change_refused_without_overwrite(self):
        self.assertEqual(self.call(self.args).returncode,0)
        self.row['sourceRef']='registry.example/app:new'
        self.request.write_text(json.dumps({'images':[self.row]}))
        self.assertEqual(self.call(self.args).returncode,1)
    def test_push_is_dry_run_until_explicit_execute(self):
        self.assertEqual(self.call(self.args).returncode,0)
        before=(self.root/'calls.log').read_text()
        args=['push','--bundle',str(self.out/'ani-images-bundle'),'--registry','harbor.example/app-project']
        p=self.call(args);self.assertEqual(p.returncode,0,p.stderr)
        self.assertEqual(before,(self.root/'calls.log').read_text())
        p=self.call(args+['--execute','--result',str(self.root/'push-result')])
        self.assertEqual(p.returncode,0,p.stderr)
        result=m.read_json(self.root/'push-result/image-map.json')
        self.assertTrue(result[0]['targetDigestRef'].startswith('harbor.example/app-project/app@sha256:'))

if __name__=='__main__':unittest.main(verbosity=2)
