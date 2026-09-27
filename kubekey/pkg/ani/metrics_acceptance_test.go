package ani

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The external kubectl/HTTP surface is simulated; RunVerify, its production
// plan, the protocol parsers, ledger and real client-go DELETE request all run.
// Each scenario gets its own localhost API, dummy kubeconfig and temporary HOME.
const metricsFakeKubectl = `#!/usr/bin/env python3
import json,os,sys,yaml
from pathlib import Path
state=Path(os.environ['FAKE_STATE_DIR']);argv=sys.argv[1:]
for i,x in enumerate(argv):
 if x in ('get','create','replace','exec'):
  args=argv[i:];break
else:sys.exit('unsupported argv '+str(argv))
def file(name):return state/name
def read(name,default=''):
 try:return file(name).read_text().strip()
 except FileNotFoundError:return default
def write(name,text):file(name).write_text(str(text))
def objfile(kind,name):return file('object-'+kind+'-'+name)
def manifest(kind,name):
 try:return yaml.safe_load(file('manifest-'+kind+'-'+name).read_text())
 except FileNotFoundError:return None
def uid(kind,name):return read('object-'+kind+'-'+name)
def log():
 with file('kubectl-calls.log').open('a') as f:f.write(' '.join(args)+'\n')
log();verb=args[0]
if verb in ('create','replace'):
 path=Path(args[args.index('-f')+1]);obj=yaml.safe_load(path.read_text());kind=obj['kind'].lower();name=obj['metadata']['name']
 old=uid(kind,name)
 if verb=='create' and old:sys.exit('AlreadyExists')
 if verb=='replace':
  if old!=obj['metadata'].get('uid') or read('version-'+name,'1')!=obj['metadata'].get('resourceVersion'):sys.exit('Conflict')
  write('version-'+name,'2')
 else:write('object-'+kind+'-'+name,'uid-'+kind+'-'+name);write('version-'+name,'1')
 write('manifest-'+kind+'-'+name,path.read_text())
 if kind=='prometheusrule':write('rule-expr',obj['spec']['groups'][0]['rules'][0]['expr']);write('rule-attempt',obj['spec']['groups'][0]['rules'][0]['labels']['ani_attempt'])
 if kind=='alertmanagerconfig':write('route-receiver',obj['spec']['receivers'][0]['name']);write('route-attempt',obj['spec']['route']['matchers'][0]['value'])
 print(json.dumps({'kind':obj['kind'],'metadata':{'name':name,'uid':uid(kind,name)}}) if '-o' in args and args[args.index('-o')+1]=='json' else kind+'/'+name);sys.exit(0)
if verb=='get':
 resource=args[1];name=args[2] if len(args)>2 and not args[2].startswith('-') else ''
 output=args[args.index('-o')+1] if '-o' in args else ''
 path=output[len('jsonpath='):] if output.startswith('jsonpath=') else ''
 if resource=='namespace':print('uid-kube-system-audit');sys.exit(0)
 if resource=='nodes':
  print(json.dumps({'items':[{'metadata':{'name':'node'+str(i)},'status':{'conditions':[{'type':'Ready','status':'True'}]}} for i in (1,2,3)]}));sys.exit(0)
 if resource=='pod' and '-l' in args:
  selector=args[args.index('-l')+1];recv=selector.split('=',1)[1]
  if not uid('deployment',recv):sys.exit('receiver missing')
  print(json.dumps({'items':[{'metadata':{'name':recv+'-pod','labels':{'ani_attempt':manifest('deployment',recv)['metadata']['labels']['ani_attempt']}}}]}));sys.exit(0)
 if resource=='pod':
  if name.startswith('prometheus-ani-metrics-'):owner='prometheus-ani-metrics-prometheus';owneruid=read('sts-prom','uid-controller-prom');puid=read('pod-uid-'+name)
  elif name.startswith('alertmanager-ani-metrics-'):owner='alertmanager-ani-metrics-alertmanager';owneruid=read('sts-am','uid-controller-am');puid=read('pod-uid-'+name)
  else:owner='';owneruid='';puid=uid('pod',name)
  if 'ownerReferences' in path:
   if '.name' in path:print(owner)
   elif '.controller' in path:print('true')
   else:print(owneruid)
  elif 'persistentVolumeClaim.claimName' in path:print('prometheus-ani-metrics-prometheus-db-prometheus-ani-metrics-prometheus-0' if owner.startswith('prometheus') else 'alertmanager-ani-metrics-alertmanager-db-alertmanager-ani-metrics-alertmanager-0')
  elif 'Ready' in path:print('True')
  elif 'metadata.uid' in path:print(puid)
  else:sys.exit('unknown pod path '+path)
  sys.exit(0)
 if resource in ('statefulset','deployment','daemonset'):
  if 'metadata.uid' in path:
   if resource=='statefulset':print(read('sts-prom','uid-controller-prom') if name.startswith('prometheus') else read('sts-am','uid-controller-am'))
   elif resource=='deployment' and uid('deployment',name):print(uid('deployment',name))
   else:print('uid-'+resource+'-'+name)
  elif 'desiredNumberScheduled' in path or 'numberReady' in path:print('3')
  elif 'spec.replicas' in path or 'readyReplicas' in path:print('1')
  else:sys.exit('unknown workload path '+path)
  sys.exit(0)
 if resource=='pvc':
  if 'metadata.uid' in path:print(read('pvc-prom','uid-pvc-prom') if name.startswith('prometheus') else read('pvc-am','uid-pvc-am'))
  elif 'volumeName' in path:print('pv-prom' if name.startswith('prometheus') else 'pv-am')
  else:sys.exit('unknown pvc path '+path)
  sys.exit(0)
 if resource=='persistentvolume':print(read('pvc-prom','uid-pvc-prom') if name=='pv-prom' else read('pvc-am','uid-pvc-am'));sys.exit(0)
 if resource=='secret':print(read('secret-uid','uid-secret-am'));sys.exit(0)
 if resource in ('service','configmap','alertmanagerconfig','prometheusrule'):
  if 'resourceVersion' in path:print(read('version-'+name,'1'))
  elif name in ('ani-metrics-prometheus','ani-metrics-alertmanager'):print('uid-service-'+name)
  else:print(uid(resource,name))
  sys.exit(0)
 sys.exit('unknown get '+str(args))
if verb=='exec':
 marker=args.index('--');tail=args[marker+1:];script=sys.stdin.read();extra=tail[2:]
 def reply(series,typ='vector'):print(json.dumps({'status':'success','data':{'resultType':typ,'result':series}}))
 def vector(metric,value):return {'metric':metric,'value':[1720000000,str(value)]}
 if '/api/v1/query_range?' in script:
  query,start,end=extra[-3:];lost=os.getenv('FAKE_MARKER_LOST') and read('prom-deleted')
  series=[] if lost or os.getenv('FAKE_MARKER_PRE_MISSING') or not read('marker-value') else [{'metric':{'__name__':'ani_metrics_rebuild_marker','run_id':read('marker-attempt')},'values':[[int(start)+15,read('marker-value')]]}]
  reply(series,'matrix');sys.exit(0)
 if '/api/v1/query?' in script:
  query=extra[-1]
  if query.startswith('up{'):reply([vector({'job':'node-exporter','instance':'node'+str(i)},'1') for i in (1,2,3)])
  elif query=='count(node_uname_info)' or query=='count(kube_node_info)':reply([vector({},'3')])
  elif query.startswith('count(container_memory'):reply([vector({},'4')])
  elif query.startswith('ani_metrics_rebuild_marker'):
   reply([vector({'__name__':'ani_metrics_rebuild_marker','run_id':read('marker-attempt')},read('marker-value'))] if read('marker-value') and not os.getenv('FAKE_MARKER_PRE_MISSING') else [])
  elif query.startswith('count(ALERTS'):
   reply([vector({},'0' if read('rule-expr')=='vector(0) == 1' else '1')])
  elif query.startswith('ALERTS'):
   attempt=read('rule-attempt');series=[vector({'alertname':'AniMetricsLifecycleTest','run_id':attempt,'ani_attempt':attempt,'namespace':'ani-observability','alertstate':'firing'},'1')] if read('rule-expr')=='vector(1) == 1' else []
   reply(series)
  else:sys.exit('unknown query '+query)
  sys.exit(0)
 if 'X-Prometheus-Remote-Write-Version' in script:
  base,name,attempt,value,ts=extra[-5:];write('marker-attempt',attempt);write('marker-value',value);write('marker-ts',ts)
  print('wrote '+name+'='+value);sys.exit(0)
 if '/api/v2/status' in script:
  print(json.dumps({'config':{'original':'receiver: '+read('route-receiver')+' matcher: '+read('route-attempt')}}));sys.exit(0)
 if "os.listdir('/data')" in script:
  attempt=read('rule-attempt');status='resolved' if read('rule-expr')=='vector(0) == 1' else 'firing'
  fingerprint='wrong-fingerprint' if status=='resolved' and os.getenv('FAKE_BAD_FINGERPRINT') else 'fp-'+attempt
  alert={'status':status,'fingerprint':fingerprint,'labels':{'alertname':'AniMetricsLifecycleTest','run_id':attempt,'ani_attempt':attempt,'namespace':'ani-observability'}}
  if os.getenv('FAKE_BAD_ATTEMPT_LABEL'):alert['labels']['run_id']='older-attempt'
  print(json.dumps([json.dumps({'alerts':[alert]})]));sys.exit(0)
 if "method='DELETE'" in script:
  sid=extra[-1]
  if os.getenv('FAKE_SILENCE_CLEANUP_FAIL'):sys.exit('silence cleanup failed')
  if sid!=read('silence-id'):sys.exit('wrong silence ID')
  write('silence-expired','1');print('200');sys.exit(0)
 if '/api/v2/silences' in script:
  payload=json.loads(extra[-1]);sid='silence-'+payload['matchers'][1]['value'];write('silence-id',sid)
  payload['id']=sid;write('silence-content',json.dumps(payload));print(json.dumps({'silenceID':sid}));sys.exit(0)
 if '/api/v2/silence/' in script:
  sid=extra[-1]
  if sid!=read('silence-id') or (os.getenv('FAKE_SILENCE_LOST') and read('am-deleted')):sys.exit('silence missing')
  content=json.loads(read('silence-content'))
  if (os.getenv('FAKE_SILENCE_CONTENT_CHANGED') and read('am-deleted')) or os.getenv('FAKE_SILENCE_PRE_BAD'):content['endsAt']='2030-01-01T00:00:00Z'
  content['status']={'state':'expired' if read('silence-expired') and not os.getenv('FAKE_SILENCE_EXPIRE_STUCK') else 'active'}
  print(json.dumps(content));sys.exit(0)
 sys.exit('unknown exec script '+script[:80]+' extra '+str(extra))
sys.exit('unsupported '+str(args))
`

type metricsFakeAPI struct{ state string }

func (f metricsFakeAPI) read(name string) string {
	b, _ := os.ReadFile(filepath.Join(f.state, name))
	return strings.TrimSpace(string(b))
}
func (f metricsFakeAPI) write(name, value string) {
	_ = os.WriteFile(filepath.Join(f.state, name), []byte(value+"\n"), 0600)
}
func (f metricsFakeAPI) serve(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	name := parts[len(parts)-1]
	resource := parts[len(parts)-2]
	status := func(code int, reason string) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		_ = json.NewEncoder(w).Encode(map[string]any{"kind": "Status", "apiVersion": "v1", "status": "Failure", "reason": reason, "code": code, "message": reason})
	}
	if r.Method == "GET" {
		file := map[string]string{"pods": "pod", "configmaps": "configmap", "services": "service", "deployments": "deployment", "alertmanagerconfigs": "alertmanagerconfig", "prometheusrules": "prometheusrule"}[resource]
		if file == "" {
			status(404, "NotFound")
			return
		}
		uid := f.read("object-" + file + "-" + name)
		if uid == "" {
			status(404, "NotFound")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"apiVersion": "v1", "kind": file, "metadata": map[string]string{"name": name, "namespace": metricsNS, "uid": uid}})
		return
	}
	if r.Method != "DELETE" {
		status(405, "MethodNotAllowed")
		return
	}
	var body struct {
		Preconditions struct {
			UID string `json:"uid"`
		} `json:"preconditions"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	file := map[string]string{"pods": "pod", "configmaps": "configmap", "services": "service", "deployments": "deployment", "alertmanagerconfigs": "alertmanagerconfig", "prometheusrules": "prometheusrule"}[resource]
	if file == "" {
		status(404, "NotFound")
		return
	}
	target := resource == "pods" && (name == "prometheus-ani-metrics-prometheus-0" || name == "alertmanager-ani-metrics-alertmanager-0")
	key := "object-" + file + "-" + name
	if target {
		key = "pod-uid-" + name
	}
	current := f.read(key)
	if target && strings.HasPrefix(name, "prometheus") && os.Getenv("FAKE_REPLACE_AFTER_LAST_GET") != "" {
		current = "uid-intruder"
		f.write(key, current)
	}
	if current == "" {
		status(404, "NotFound")
		return
	}
	if body.Preconditions.UID == "" || body.Preconditions.UID != current {
		status(409, "Conflict")
		return
	}
	if target && strings.HasPrefix(name, "prometheus") && os.Getenv("FAKE_PROM_DELETE_HANG") != "" {
		f.write("prom-delete-started", body.Preconditions.UID)
		time.Sleep(500 * time.Millisecond)
	}
	if target && strings.HasPrefix(name, "alertmanager") && os.Getenv("FAKE_AM_FORBIDDEN") != "" {
		status(403, "Forbidden")
		return
	}
	if !target && os.Getenv("FAKE_CLEANUP_FAIL") != "" && resource == "prometheusrules" {
		status(500, "InternalError")
		return
	}
	log, _ := os.OpenFile(filepath.Join(f.state, "api-deletes.log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	_, _ = fmt.Fprintf(log, "%s %s uid=%s\n", resource, name, body.Preconditions.UID)
	_ = log.Close()
	if target {
		f.write(key, "uid-new-"+name)
		if strings.HasPrefix(name, "prometheus") {
			f.write("prom-deleted", "1")
			if v := os.Getenv("FAKE_NEW_PVC_UID"); v != "" {
				f.write("pvc-prom", v)
			}
			if v := os.Getenv("FAKE_NEW_STS_UID"); v != "" {
				f.write("sts-prom", v)
			}
		} else {
			f.write("am-deleted", "1")
			if v := os.Getenv("FAKE_NEW_SECRET_UID"); v != "" {
				f.write("secret-uid", v)
			}
		}
	} else {
		_ = os.Remove(filepath.Join(f.state, key))
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"kind":"Status","apiVersion":"v1","status":"Success","code":200}`))
}

func metricsFixture(t *testing.T) (VerifyInput, string, string) {
	t.Helper()
	root := t.TempDir()
	state := filepath.Join(root, "state")
	bin := filepath.Join(root, "bin")
	home := filepath.Join(root, "home")
	for _, p := range []string{state, bin, home} {
		if err := os.MkdirAll(p, 0700); err != nil {
			t.Fatal(err)
		}
	}
	fake := metricsFakeAPI{state: state}
	server := httptest.NewServer(http.HandlerFunc(fake.serve))
	t.Cleanup(server.Close)
	kc := r13WriteKubeconfig(t, server.URL, filepath.Join(root, "dummy-kubeconfig"))
	if err := os.WriteFile(filepath.Join(bin, "kubectl"), []byte(metricsFakeKubectl), 0700); err != nil {
		t.Fatal(err)
	}
	fake.write("pod-uid-prometheus-ani-metrics-prometheus-0", "uid-old-prom")
	fake.write("pod-uid-alertmanager-ani-metrics-alertmanager-0", "uid-old-am")
	fake.write("pvc-prom", "uid-pvc-prom")
	fake.write("pvc-am", "uid-pvc-am")
	fake.write("secret-uid", "uid-secret-am")
	fake.write("object-prometheusrule-history", "uid-historical-rule")
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	t.Setenv("FAKE_STATE_DIR", state)
	t.Setenv("HOME", home)
	t.Setenv("KUBECONFIG", kc)
	t.Setenv("ANI_ACCEPTANCE_STATE_DIR", filepath.Join(root, "acceptance-state"))
	t.Setenv("ANI_INSTALL_LOCK", filepath.Join(root, "acceptance-state", "ani-install.lock"))
	t.Setenv("ANI_VERIFY_POD_RECREATE_TIMEOUT", "1s")
	run, stateFile := r13RunRecord(t, root, true, PhaseSucceeded, "metrics")
	input := VerifyInput{RunFile: run, StateFile: stateFile, Level: VerifyLevelAcceptance, Only: []string{"metrics"}, AllowPodRecreate: true, Kubeconfig: kc, Output: filepath.Join(root, "out"), ScriptDir: filepath.Join(root, "scripts")}
	return input, root, state
}
func metricsRunScenario(t *testing.T, knob, value string) (VerifyReport, string) {
	t.Helper()
	input, _, state := metricsFixture(t)
	if knob != "" {
		t.Setenv(knob, value)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	err := RunVerify(ctx, input, nil)
	report := r13ReadReport(t, r13FindAcceptanceReport(t, input.Output))
	if report.Overall == VerifyStatusPass && err != nil {
		t.Fatalf("pass report with error: %v", err)
	}
	if report.Overall != VerifyStatusPass && err == nil {
		t.Fatal("failed report returned nil")
	}
	return report, state
}
func metricsStepStatus(r VerifyReport, id string) string {
	if len(r.Results) != 1 {
		return ""
	}
	for _, s := range r.Results[0].Steps {
		if s.ID == id {
			return s.Status
		}
	}
	return ""
}
func metricsBusinessDeletes(t *testing.T, state string) []string {
	t.Helper()
	data, _ := os.ReadFile(filepath.Join(state, "api-deletes.log"))
	var out []string
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if strings.HasPrefix(line, "pods prometheus-ani-metrics-prometheus-0 ") || strings.HasPrefix(line, "pods alertmanager-ani-metrics-alertmanager-0 ") {
			out = append(out, line)
		}
	}
	return out
}
func TestMetricsRunVerifyIsolatedAcceptance(t *testing.T) {
	t.Run("METRICS-01-through-11-original-state-and-UID-cleanup", func(t *testing.T) {
		report, state := metricsRunScenario(t, "", "")
		if report.Overall != VerifyStatusPass {
			t.Fatalf("report: %+v", report.Results)
		}
		if len(report.Results[0].Steps) != 11 {
			t.Fatalf("steps=%d", len(report.Results[0].Steps))
		}
		for i := 1; i <= 11; i++ {
			id := fmt.Sprintf("METRICS-%02d", i)
			if got := metricsStepStatus(report, id); got != VerifyStatusPass {
				t.Fatalf("%s=%s", id, got)
			}
		}
		deletes := metricsBusinessDeletes(t, state)
		if len(deletes) != 2 || !strings.Contains(deletes[0], "uid=uid-old-prom") || !strings.Contains(deletes[1], "uid=uid-old-am") {
			t.Fatalf("wrong business deletes: %v", deletes)
		}
		if _, err := os.Stat(filepath.Join(state, "object-prometheusrule-history")); err != nil {
			t.Fatal("historical rule was touched", err)
		}
		if _, err := os.Stat(filepath.Join(state, "silence-expired")); err != nil {
			t.Fatal("this attempt silence was not expired")
		}
		if _, err := os.Stat(filepath.Join(state, "object-prometheusrule-"+report.Results[0].Steps[4].Evidence["ruleName"])); err == nil {
			t.Fatal("attempt rule not cleaned")
		}
	})
	for _, tc := range []struct {
		name, knob, value, step string
		deletes                 int
	}{
		{"wrong-fingerprint", "FAKE_BAD_FINGERPRINT", "1", "METRICS-06", 0},
		{"wrong-attempt-label", "FAKE_BAD_ATTEMPT_LABEL", "1", "METRICS-05", 0},
		{"original-sample-not-confirmed", "FAKE_MARKER_PRE_MISSING", "1", "METRICS-07", 0},
		{"original-sample-lost", "FAKE_MARKER_LOST", "1", "METRICS-08", 1},
		{"original-silence-not-confirmed", "FAKE_SILENCE_PRE_BAD", "1", "METRICS-09", 1},
		{"original-silence-lost", "FAKE_SILENCE_LOST", "1", "METRICS-10", 2},
		{"silence-content-changed", "FAKE_SILENCE_CONTENT_CHANGED", "1", "METRICS-10", 2},
		{"second-target-refused-stops-cleanup", "FAKE_AM_FORBIDDEN", "1", "METRICS-10", 1},
		{"same-name-replacement-409", "FAKE_REPLACE_AFTER_LAST_GET", "1", "METRICS-08", 0},
		{"storage-identity-changed", "FAKE_NEW_PVC_UID", "uid-replaced", "METRICS-08", 1},
		{"controller-identity-changed", "FAKE_NEW_STS_UID", "uid-replaced", "METRICS-08", 1},
		{"secret-identity-changed", "FAKE_NEW_SECRET_UID", "uid-replaced", "METRICS-10", 2},
		{"cleanup-failure-is-not-pass", "FAKE_CLEANUP_FAIL", "1", "METRICS-11", 2},
		{"silence-expiration-must-be-observed", "FAKE_SILENCE_EXPIRE_STUCK", "1", "METRICS-11", 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			report, state := metricsRunScenario(t, tc.knob, tc.value)
			if report.Overall == VerifyStatusPass || metricsStepStatus(report, tc.step) != VerifyStatusFailed {
				t.Fatalf("wanted %s fail, got %+v", tc.step, report.Results)
			}
			if n := len(metricsBusinessDeletes(t, state)); n != tc.deletes {
				t.Fatalf("business deletes=%d, want %d", n, tc.deletes)
			}
			if tc.step != "METRICS-11" && metricsStepStatus(report, "METRICS-11") != VerifyStatusNotRun {
				t.Fatal("failure did not preserve evidence and stop cleanup")
			}
			if tc.name == "original-sample-not-confirmed" || tc.name == "original-silence-not-confirmed" {
				var evidence map[string]string
				for _, step := range report.Results[0].Steps {
					if step.ID == tc.step {
						evidence = step.Evidence
					}
				}
				key := "markerValue"
				if tc.step == "METRICS-09" {
					key = "silenceID"
				}
				if evidence[key] == "" {
					t.Fatalf("%s failure lost original identity evidence: %v", tc.step, evidence)
				}
			}
		})
	}
}

func TestMetricsFailedAttemptEvidenceSurvivesRetry(t *testing.T) {
	input, _, _ := metricsFixture(t)
	t.Setenv("FAKE_MARKER_PRE_MISSING", "1")
	firstCtx, firstCancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer firstCancel()
	if err := RunVerify(firstCtx, input, nil); err == nil {
		t.Fatal("first attempt unexpectedly passed")
	}
	first := r13ReadReport(t, r13FindAcceptanceReport(t, input.Output))
	if metricsStepStatus(first, "METRICS-07") != VerifyStatusFailed {
		t.Fatalf("first attempt did not fail at original sample: %+v", first.Results)
	}
	if err := os.Unsetenv("FAKE_MARKER_PRE_MISSING"); err != nil {
		t.Fatal(err)
	}
	secondCtx, secondCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer secondCancel()
	if err := RunVerify(secondCtx, input, nil); err != nil {
		t.Fatal(err)
	}
	parent := filepath.Join(input.Output, "acceptance-ani-ani-lab-20260924-130000-metrics")
	entries, err := os.ReadDir(parent)
	if err != nil {
		t.Fatal(err)
	}
	var markers []string
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		marker := filepath.Join(parent, entry.Name(), "marker-original.json")
		if data, err := os.ReadFile(marker); err == nil && len(data) > 0 {
			markers = append(markers, marker)
		}
	}
	if len(markers) != 2 || markers[0] == markers[1] {
		t.Fatalf("original evidence was overwritten or absent across attempts: %v", markers)
	}
}

func TestMetricsOldQuotaRefusesBeforeTemporaryWrites(t *testing.T) {
	input, _, state := metricsFixture(t)
	dir, err := acceptanceStateDir("ani-lab")
	if err != nil {
		t.Fatal(err)
	}
	rec := acceptanceLedger{State: ledgerStateAttempted, RunID: "ani-ani-lab-20260924-130000", Target: "prometheus", OldPodUID: "uid-prior"}
	if _, created, err := claimAcceptanceLedger(dir, rec.RunID, acceptanceTargets["prometheus"], rec); err != nil || !created {
		t.Fatalf("seed old quota: %v created=%t", err, created)
	}
	if err := RunVerify(context.Background(), input, nil); err == nil {
		t.Fatal("old Prometheus quota was accepted")
	}
	report := r13ReadReport(t, r13FindAcceptanceReport(t, input.Output))
	if report.Overall == VerifyStatusPass || !strings.Contains(report.Results[0].Detail, "already recorded") {
		t.Fatalf("old quota result: %+v", report.Results)
	}
	if data, _ := os.ReadFile(filepath.Join(state, "kubectl-calls.log")); strings.Contains(string(data), "create -f") {
		t.Fatal("preflight wrote temporary objects")
	}
	if n := len(metricsBusinessDeletes(t, state)); n != 0 {
		t.Fatalf("old quota made %d business deletes", n)
	}
}
func TestMetricsCancellationAfterDeleteRequestNeverReplays(t *testing.T) {
	input, _, state := metricsFixture(t)
	t.Setenv("FAKE_PROM_DELETE_HANG", "1")
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	go func() {
		for i := 0; i < 150; i++ {
			if _, err := os.Stat(filepath.Join(state, "prom-delete-started")); err == nil {
				cancel()
				return
			}
			time.Sleep(50 * time.Millisecond)
		}
	}()
	err := RunVerify(ctx, input, nil)
	if err == nil {
		t.Fatal("cancelled delete was reported pass")
	}
	report := r13ReadReport(t, r13FindAcceptanceReport(t, input.Output))
	if metricsStepStatus(report, "METRICS-08") != VerifyStatusFailed || metricsStepStatus(report, "METRICS-09") != VerifyStatusNotRun {
		t.Fatalf("cancellation did not stop plan: %+v", report.Results)
	}
	if _, err := os.Stat(filepath.Join(state, "prom-delete-started")); err != nil {
		t.Fatal("cancellation happened before the DELETE request", err)
	}
	time.Sleep(600 * time.Millisecond)
	data, _ := os.ReadFile(filepath.Join(state, "api-deletes.log"))
	if strings.Count(string(data), "pods prometheus-ani-metrics-prometheus-0") > 1 {
		t.Fatalf("delete request replayed: %s", data)
	}
	dir, _ := acceptanceStateDir("ani-lab")
	rec, found, err := peekAcceptanceLedger(dir, "ani-ani-lab-20260924-130000", acceptanceTargets["prometheus"])
	if err != nil || !found || rec.State != ledgerStateUnknown {
		t.Fatalf("cancelled delete ledger: %+v found=%t err=%v", rec, found, err)
	}
}

func TestMetricsLegacyHeavyScriptRefusesBeforeMutation(t *testing.T) {
	input, root, state := metricsFixture(t)
	script := filepath.Join("..", "..", "builtin", "core", "roles", "ani", "metrics", "templates", "verify.sh")
	cmd := exec.Command("bash", script)
	cmd.Env = append(os.Environ(), "ANI_VERIFY_LEVEL=acceptance", "ANI_VERIFY_KUBECONFIG="+input.Kubeconfig, "ANI_VERIFY_OUTPUT_DIR="+filepath.Join(root, "legacy-output"))
	out, err := cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(out), "kk ani verify --level acceptance") {
		t.Fatalf("legacy heavy entry did not refuse before change: err=%v out=%s", err, out)
	}
	if data, err := os.ReadFile(filepath.Join(state, "kubectl-calls.log")); err == nil && len(data) > 0 {
		t.Fatalf("legacy heavy entry called kubectl: %s", data)
	}
}
