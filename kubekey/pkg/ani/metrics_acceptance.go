package ani

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/cockroachdb/errors"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// The two metadata labels have different jobs: run_id admits the config through
// the installed Operator selector; ani_attempt binds the alert and route to one
// verification invocation. A prior run's silence cannot suppress this alert.
const (
	metricsNS          = MetricsNamespace
	metricsAlert       = "AniMetricsLifecycleTest"
	metricsPythonImage = "library/python:3.13.11-alpine3.23"
	metricsPromURL     = "http://ani-metrics-prometheus.ani-observability.svc.cluster.local:9090"
	metricsAMURL       = "http://ani-metrics-alertmanager.ani-observability.svc.cluster.local:9093"
	metricsSecret      = "alertmanager-ani-metrics-alertmanager-generated"
)

type metricsOwned struct{ Kind, Name, UID string }
type metricsAttemptState struct {
	label, attempt, client, receiver, rule, config, receiverPod string
	outputDir, image                                            string
	owned                                                       []metricsOwned
	firingFingerprint                                           string
	markerValue                                                 string
	markerTS                                                    int64
	markerStart, markerEnd                                      int64
	markerQuery                                                 string
	markerConfirmed                                             bool
	silenceID                                                   string
	silenceJSON                                                 []byte // original POST request; never replaced by a GET
	silenceServerJSON                                           []byte // first validated server GET; immutable rebuild baseline
	silenceConfirmed                                            bool
	secretUID                                                   string
}

func newMetricsAttemptState(a *acceptanceAttempt, cluster string) *metricsAttemptState {
	suffix := strings.ReplaceAll(a.attempt, "-", "")
	if len(suffix) > 19 {
		suffix = suffix[len(suffix)-19:]
	}
	return &metricsAttemptState{
		label: "ani-" + cluster, attempt: suffix, outputDir: a.outputDir,
		image:  a.registry + "/" + metricsPythonImage,
		client: "ani-met-client-" + suffix, receiver: "ani-met-recv-" + suffix,
		rule: "ani-met-rule-" + suffix, config: "ani-met-amcfg-" + suffix,
	}
}

func metricsAcceptancePlan() acceptancePlan {
	steps := []acceptanceStep{
		{ID: "METRICS-01", Title: "metrics workloads and services are ready", Run: metrics01},
		{ID: "METRICS-02", Title: "node, KSM and cAdvisor series are queryable", Run: metrics02},
		{ID: "METRICS-03", Title: "this attempt's webhook receiver is ready", Run: metrics03},
		{ID: "METRICS-04", Title: "Operator loads this attempt's Alertmanager route", Run: metrics04},
		{ID: "METRICS-05", Title: "Prometheus firing alert reaches this receiver", Run: metrics05},
		{ID: "METRICS-06", Title: "same alert resolves with the same fingerprint", Run: metrics06},
		{ID: "METRICS-07", Title: "original remote-write sample is queryable before recreation", Run: metrics07},
		{ID: "METRICS-08", Title: "Prometheus rebuild preserves controller, PVC and original sample", Target: "prometheus", Run: metrics08},
		{ID: "METRICS-09", Title: "this attempt's silence is created and read by ID", Run: metrics09},
		{ID: "METRICS-10", Title: "Alertmanager rebuild preserves controller, PVC, Secret and silence", Target: "alertmanager", Run: metrics10},
		{ID: "METRICS-11", Title: "clean only this attempt's recorded objects and silence", Run: metrics11},
	}
	return acceptancePlan{Steps: steps}
}

func (a *acceptanceAttempt) metricsState() (*metricsAttemptState, error) {
	if a.metrics == nil {
		return nil, errors.New("metrics attempt state was not initialized")
	}
	return a.metrics, nil
}

func metricsValidName(s string) bool {
	return regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`).MatchString(s) && len(s) <= 63
}

// create, never apply: a name collision cannot adopt an older attempt's object.
// Ownership is the actual UID returned by the API, persisted before cleanup can
// act on it. A failed create/get is left as evidence, not guessed at cleanup.
func (a *acceptanceAttempt) metricsCreate(kind, name string, obj any) error {
	m, err := a.metricsState()
	if err != nil {
		return err
	}
	if !metricsValidName(name) {
		return fmt.Errorf("invalid metrics object name %q", name)
	}
	data, err := json.MarshalIndent(obj, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(m.outputDir, kind+"-"+name+".yaml")
	if err := os.WriteFile(path, data, 0600); err != nil {
		return err
	}
	out, err := a.runner.run(a.ctx, "create", "-f", path, "-o", "json")
	if err != nil {
		return fmt.Errorf("create %s/%s: %w (%s)", kind, name, err, strings.TrimSpace(string(out)))
	}
	var created struct {
		Kind     string                     `json:"kind"`
		Metadata struct{ Name, UID string } `json:"metadata"`
	}
	if err := json.Unmarshal(out, &created); err != nil || strings.ToLower(created.Kind) != kind || created.Metadata.Name != name || created.Metadata.UID == "" {
		return fmt.Errorf("created %s/%s but response identity could not be registered; preserve it for investigation: %v %q", kind, name, err, strings.TrimSpace(string(out)))
	}
	uid := created.Metadata.UID
	m.owned = append(m.owned, metricsOwned{kind, name, uid})
	return m.saveOwned()
}

// Evidence is local to this attempt, durable before the external operation it
// describes. A failed probe still has its original value/ID in the report and
// in this directory; the durability ledger for Pod changes is separate.
func (m *metricsAttemptState) saveEvidence(name string, data []byte) error {
	path := filepath.Join(m.outputDir, name)
	f, err := os.OpenFile(path+".new", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	if _, err = f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = os.Rename(path+".new", path); err != nil {
		return err
	}
	return syncDir(m.outputDir)
}

func (m *metricsAttemptState) saveOwned() error {
	b, err := json.MarshalIndent(m.owned, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(m.outputDir, "owned.json")
	temp := path + ".new"
	f, err := os.OpenFile(temp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return err
	}
	if _, err = f.Write(b); err != nil {
		_ = f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = os.Rename(temp, path); err != nil {
		return err
	}
	d, err := os.Open(m.outputDir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

func (a *acceptanceAttempt) metricsPython(pod, script string, args ...string) (string, error) {
	if a.ctx.Err() != nil {
		return "", a.ctx.Err()
	}
	argv := a.runner.argv(append([]string{"exec", "-i", "-n", metricsNS, pod, "--", "python3", "-"}, args...)...)
	cmd := exec.CommandContext(a.ctx, argv[0], argv[1:]...)
	cmd.Stdin = strings.NewReader(script)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("python probe in %s failed: %w: %s", pod, err, strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}

func metricsPoll(ctx context.Context, interval time.Duration, check func() (bool, error)) error {
	var last error
	for {
		ok, err := check()
		if ok && err == nil {
			return nil
		}
		if err != nil {
			last = err
		}
		select {
		case <-ctx.Done():
			if last != nil {
				return fmt.Errorf("probe deadline/cancellation: %w (last: %v)", ctx.Err(), last)
			}
			return ctx.Err()
		case <-time.After(interval):
		}
	}
}

func metricsNumber(s string) (int, error) {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return 0, fmt.Errorf("expected integer, got %q: %w", s, err)
	}
	return n, nil
}
func metricsWorkloadReady(ctx context.Context, r kubectlRunner, kind, name string) (bool, error) {
	desired, err := r.jsonpath(ctx, kind, name, metricsNS, "{.spec.replicas}")
	if err != nil {
		return false, err
	}
	ready, err := r.jsonpath(ctx, kind, name, metricsNS, "{.status.readyReplicas}")
	if err != nil {
		return false, err
	}
	want, err := metricsNumber(desired)
	if err != nil {
		return false, err
	}
	got, err := metricsNumber(ready)
	if err != nil {
		return false, nil
	}
	return want > 0 && got == want, nil
}

// The only non-Pod cleanup uses dynamic client DeleteOptions with a UID
// precondition. A same-name replacement stays untouched (409), as for business
// Pod deletion; no label sweep or fallback by name exists.
func metricsDynamicClient(path string) (dynamic.Interface, error) {
	rules := &clientcmd.ClientConfigLoadingRules{ExplicitPath: path}
	raw, err := rules.Load()
	if err != nil {
		return nil, err
	}
	cc := clientcmd.NewNonInteractiveClientConfig(*raw, "", &clientcmd.ConfigOverrides{}, rules)
	cfg, err := cc.ClientConfig()
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(cfg.Host) == "" {
		return nil, errors.New("metrics cleanup kubeconfig has no API server")
	}
	cfg = rest.CopyConfig(cfg)
	cfg.Timeout = 30 * time.Second
	cfg.ContentType = "application/json"
	return dynamic.NewForConfig(cfg)
}
func metricsGVR(kind string) (schema.GroupVersionResource, error) {
	switch kind {
	case "pod":
		return schema.GroupVersionResource{Version: "v1", Resource: "pods"}, nil
	case "configmap":
		return schema.GroupVersionResource{Version: "v1", Resource: "configmaps"}, nil
	case "service":
		return schema.GroupVersionResource{Version: "v1", Resource: "services"}, nil
	case "deployment":
		return schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"}, nil
	case "alertmanagerconfig":
		return schema.GroupVersionResource{Group: "monitoring.coreos.com", Version: "v1alpha1", Resource: "alertmanagerconfigs"}, nil
	case "prometheusrule":
		return schema.GroupVersionResource{Group: "monitoring.coreos.com", Version: "v1", Resource: "prometheusrules"}, nil
	default:
		return schema.GroupVersionResource{}, fmt.Errorf("no UID-delete mapping for %s", kind)
	}
}
func (a *acceptanceAttempt) metricsDeleteOwned() error {
	m := a.metrics
	client, err := metricsDynamicClient(a.runner.kubeconfig)
	if err != nil {
		return err
	}
	for i := len(m.owned) - 1; i >= 0; i-- {
		o := m.owned[i]
		gvr, err := metricsGVR(o.Kind)
		if err != nil {
			return err
		}
		uid := types.UID(o.UID)
		ctx, cancel := context.WithTimeout(a.ctx, 30*time.Second)
		err = client.Resource(gvr).Namespace(metricsNS).Delete(ctx, o.Name, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &uid}})
		cancel()
		if err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("UID-bound cleanup of %s/%s uid=%s failed; remaining objects preserved: %w", o.Kind, o.Name, o.UID, err)
		}
		// A DELETE acceptance is asynchronous when finalizers are present. Do
		// not call an object cleaned until this exact UID is gone; a different
		// UID at the name is somebody else's object and is never touched.
		// These owned Pods and receiver templates use the normal 30-second
		// Kubernetes grace. Allow removal and controller/finalizer propagation
		// to finish after it; never shorten grace to satisfy a checker deadline.
		goneCtx, stop := context.WithTimeout(a.ctx, 2*time.Minute)
		err = metricsPoll(goneCtx, time.Second, func() (bool, error) {
			current, getErr := client.Resource(gvr).Namespace(metricsNS).Get(goneCtx, o.Name, metav1.GetOptions{})
			if apierrors.IsNotFound(getErr) {
				return true, nil
			}
			if getErr != nil {
				return false, getErr
			}
			if string(current.GetUID()) != o.UID {
				return true, nil
			}
			return false, fmt.Errorf("%s/%s uid=%s still exists after DELETE", o.Kind, o.Name, o.UID)
		})
		stop()
		if err != nil {
			return fmt.Errorf("UID-bound cleanup of %s/%s did not finish: %w", o.Kind, o.Name, err)
		}
		m.owned = m.owned[:i]
		if err := m.saveOwned(); err != nil {
			return fmt.Errorf("cleanup succeeded but ownership record was not updated: %w", err)
		}
	}
	return nil
}

const metricsQueryPython = `import json,sys,urllib.parse,urllib.request
base,query=sys.argv[1:3]
url=base+'/api/v1/query?'+urllib.parse.urlencode({'query':query})
with urllib.request.urlopen(url,timeout=30) as r: print(json.dumps(json.load(r)))
`
const metricsRangePython = `import json,sys,urllib.parse,urllib.request
base,query,start,end=sys.argv[1:5]
url=base+'/api/v1/query_range?'+urllib.parse.urlencode({'query':query,'start':start,'end':end,'step':'15'})
with urllib.request.urlopen(url,timeout=30) as r: print(json.dumps(json.load(r)))
`
const metricsStatusPython = `import json,sys,urllib.request
with urllib.request.urlopen(sys.argv[1]+'/api/v2/status',timeout=30) as r: print(json.dumps(json.load(r)))
`
const metricsSilenceCreatePython = `import json,sys,urllib.request
base,payload=sys.argv[1:3]
req=urllib.request.Request(base+'/api/v2/silences',data=payload.encode(),headers={'Content-Type':'application/json'})
with urllib.request.urlopen(req,timeout=30) as r: print(json.dumps(json.load(r)))
`
const metricsSilenceGetPython = `import json,sys,urllib.request
base,sid=sys.argv[1:3]
with urllib.request.urlopen(base+'/api/v2/silence/'+sid,timeout=30) as r: print(json.dumps(json.load(r)))
`
const metricsSilenceDeletePython = `import sys,urllib.request
base,sid=sys.argv[1:3]
req=urllib.request.Request(base+'/api/v2/silence/'+sid,method='DELETE')
with urllib.request.urlopen(req,timeout=30) as r: print(r.status)
`
const metricsReceiverDumpPython = `import json,os
out=[]
for name in sorted(os.listdir('/data')):
 if name.startswith('req-') and name.endswith('.json'):
  with open('/data/'+name) as f: out.append(f.read())
print(json.dumps(out))
`

// This is the original stdlib-only remote-write v1 encoder from the packaged
// metrics checker. It emits snappy literal protobuf, which Prometheus accepts;
// JSON POSTs to this endpoint do not write samples.
const metricsWritePython = `import struct,sys,urllib.request
def varint(n):
 out=bytearray()
 while True:
  b=n&127;n>>=7
  if n:out.append(b|128)
  else:out.append(b);return bytes(out)
def snappy(raw):
 out=bytearray(varint(len(raw)));i=0
 while i<len(raw):
  chunk=raw[i:i+65536];ln=len(chunk)
  if ln<=60:out.append((ln-1)<<2)
  else:
   nbytes=((ln-1).bit_length()+7)//8
   out.append((59+nbytes)<<2);out.extend((ln-1).to_bytes(nbytes,'little'))
  out.extend(chunk);i+=ln
 return bytes(out)
def ld(num,data):return varint((num<<3)|2)+varint(len(data))+data
def f64(num,raw8):return varint((num<<3)|1)+raw8
def vi(num,value):return varint((num<<3)|0)+varint(value)
base,name,run_id,value,ts=sys.argv[1:6]
sample=f64(1,struct.pack('<d',float(value)))+vi(2,int(ts))
series=ld(1,ld(1,b'__name__')+ld(2,name.encode()))+ld(1,ld(1,b'run_id')+ld(2,run_id.encode()))+ld(2,sample)
body=snappy(ld(1,series))
req=urllib.request.Request(base+'/api/v1/write',data=body,headers={'Content-Type':'application/x-protobuf','Content-Encoding':'snappy','X-Prometheus-Remote-Write-Version':'0.1.0'})
with urllib.request.urlopen(req,timeout=30) as r:
 if r.status not in (200,204):sys.exit('remote write returned '+str(r.status))
print('wrote '+name+'{run_id='+run_id+'}='+value+' at '+ts)
`

type metricsVector struct {
	Metric map[string]string   `json:"metric"`
	Value  []json.RawMessage   `json:"value"`
	Values [][]json.RawMessage `json:"values"`
}
type metricsPromReply struct {
	Status string `json:"status"`
	Data   struct {
		ResultType string          `json:"resultType"`
		Result     []metricsVector `json:"result"`
	} `json:"data"`
}

func metricsSampleString(raw json.RawMessage) string {
	var s string
	_ = json.Unmarshal(raw, &s)
	return s
}
func metricsValueEquals(raw json.RawMessage, want string) bool {
	got, err := strconv.ParseFloat(metricsSampleString(raw), 64)
	if err != nil {
		return false
	}
	expected, err := strconv.ParseFloat(want, 64)
	return err == nil && got == expected
}
func (a *acceptanceAttempt) metricsQuery(query string) (metricsPromReply, error) {
	m := a.metrics
	raw, err := a.metricsPython(m.client, metricsQueryPython, metricsPromURL, query)
	if err != nil {
		return metricsPromReply{}, err
	}
	var reply metricsPromReply
	if err = json.Unmarshal([]byte(raw), &reply); err != nil {
		return reply, fmt.Errorf("Prometheus query JSON: %w (%q)", err, raw)
	}
	if reply.Status != "success" || reply.Data.ResultType != "vector" {
		return reply, fmt.Errorf("Prometheus query %q status=%q type=%q", query, reply.Status, reply.Data.ResultType)
	}
	return reply, nil
}
func (a *acceptanceAttempt) metricsRange(query, value string, start, end int64) error {
	m := a.metrics
	raw, err := a.metricsPython(m.client, metricsRangePython, metricsPromURL, query, strconv.FormatInt(start, 10), strconv.FormatInt(end, 10))
	if err != nil {
		return err
	}
	var reply metricsPromReply
	if err = json.Unmarshal([]byte(raw), &reply); err != nil {
		return err
	}
	if reply.Status != "success" || reply.Data.ResultType != "matrix" {
		return fmt.Errorf("range query status=%q type=%q", reply.Status, reply.Data.ResultType)
	}
	for _, series := range reply.Data.Result {
		if series.Metric["run_id"] != m.attempt || series.Metric["__name__"] != "ani_metrics_rebuild_marker" {
			continue
		}
		for _, point := range series.Values {
			if len(point) < 2 || !metricsValueEquals(point[1], value) {
				continue
			}
			var ts float64
			if json.Unmarshal(point[0], &ts) != nil {
				continue
			}
			if ts >= float64(start) && ts <= float64(end) {
				return nil
			}
		}
	}
	return fmt.Errorf("original sample %s=%s with run_id=%s absent from range [%d,%d]", query, value, m.attempt, start, end)
}
func (a *acceptanceAttempt) metricsScalar(query string) (float64, error) {
	reply, err := a.metricsQuery(query)
	if err != nil {
		return 0, err
	}
	if len(reply.Data.Result) != 1 || len(reply.Data.Result[0].Value) < 2 {
		return 0, fmt.Errorf("query %q returned %d series, expected one scalar", query, len(reply.Data.Result))
	}
	return strconv.ParseFloat(metricsSampleString(reply.Data.Result[0].Value[1]), 64)
}
func (a *acceptanceAttempt) metricsRequests(status string) (string, error) {
	m := a.metrics
	raw, err := a.metricsPython(m.receiverPod, metricsReceiverDumpPython)
	if err != nil {
		return "", err
	}
	var bodies []string
	if err = json.Unmarshal([]byte(raw), &bodies); err != nil {
		return "", fmt.Errorf("receiver dump is not a JSON array: %w", err)
	}
	for _, body := range bodies {
		var envelope struct {
			Alerts []struct {
				Status, Fingerprint string
				Labels              map[string]string
			} `json:"alerts"`
		}
		if json.Unmarshal([]byte(body), &envelope) != nil {
			continue
		}
		for _, alert := range envelope.Alerts {
			if alert.Status == status && alert.Labels["alertname"] == metricsAlert && alert.Labels["run_id"] == m.attempt && alert.Labels["namespace"] == metricsNS && alert.Labels["ani_attempt"] == m.attempt && alert.Fingerprint != "" {
				return alert.Fingerprint, nil
			}
		}
	}
	return "", fmt.Errorf("receiver has no %s notification for alert %s and attempt %s", status, metricsAlert, m.attempt)
}

func metricsObject(api, kind, name string, labels map[string]string, spec any) map[string]any {
	return map[string]any{"apiVersion": api, "kind": kind, "metadata": map[string]any{"name": name, "namespace": metricsNS, "labels": labels}, "spec": spec}
}
func (m *metricsAttemptState) labels() map[string]string {
	return map[string]string{"run_id": m.label, "ani_attempt": m.attempt}
}
func (a *acceptanceAttempt) metricsReadyPod(name string) error {
	return metricsPoll(a.ctx, 2*time.Second, func() (bool, error) {
		ready, err := a.runner.jsonpath(a.ctx, "pod", name, metricsNS, `{.status.conditions[?(@.type=="Ready")].status}`)
		if err != nil {
			return false, err
		}
		if ready != "True" {
			return false, fmt.Errorf("pod %s Ready=%q", name, ready)
		}
		return true, nil
	})
}
func metrics01(a *acceptanceAttempt, r *VerifyStepResult) error {
	m, err := a.metricsState()
	if err != nil {
		return err
	}
	if !metricsValidName(m.label) || !metricsValidName(m.attempt) {
		return fmt.Errorf("invalid metrics run/attempt labels: %q/%q", m.label, m.attempt)
	}
	out, err := a.runner.run(a.ctx, "get", "nodes", "-o", "json")
	if err != nil {
		return err
	}
	var nodes struct {
		Items []struct {
			Metadata struct{ Name string }
			Status   struct {
				Conditions []struct{ Type, Status string }
			}
		}
	}
	if err = json.Unmarshal(out, &nodes); err != nil {
		return err
	}
	if len(nodes.Items) < 3 {
		return fmt.Errorf("expected at least three nodes, got %d", len(nodes.Items))
	}
	for _, node := range nodes.Items {
		ready := false
		for _, c := range node.Status.Conditions {
			if c.Type == "Ready" && c.Status == "True" {
				ready = true
			}
		}
		if !ready {
			return fmt.Errorf("node %s is not Ready", node.Metadata.Name)
		}
	}
	for _, obj := range []struct{ kind, name string }{{"statefulset", "prometheus-ani-metrics-prometheus"}, {"statefulset", "alertmanager-ani-metrics-alertmanager"}, {"deployment", "ani-metrics-operator"}, {"deployment", "ani-metrics-kube-state-metrics"}} {
		if err = metricsPoll(a.ctx, 2*time.Second, func() (bool, error) {
			ok, e := metricsWorkloadReady(a.ctx, a.runner, obj.kind, obj.name)
			if e != nil {
				return false, e
			}
			if !ok {
				return false, fmt.Errorf("%s/%s is not fully Ready", obj.kind, obj.name)
			}
			return true, nil
		}); err != nil {
			return err
		}
	}
	ready := ""
	if err := metricsPoll(a.ctx, 2*time.Second, func() (bool, error) {
		desired, e := a.runner.jsonpath(a.ctx, "daemonset", "ani-metrics-prometheus-node-exporter", metricsNS, "{.status.desiredNumberScheduled}")
		if e != nil {
			return false, e
		}
		current, e := a.runner.jsonpath(a.ctx, "daemonset", "ani-metrics-prometheus-node-exporter", metricsNS, "{.status.numberReady}")
		if e != nil {
			return false, e
		}
		want, e := metricsNumber(desired)
		if e != nil {
			return false, e
		}
		got, e := metricsNumber(current)
		if e != nil {
			return false, e
		}
		if want != len(nodes.Items) || got != want {
			return false, fmt.Errorf("node-exporter Ready %d/%d but nodes=%d", got, want, len(nodes.Items))
		}
		ready = current
		return true, nil
	}); err != nil {
		return err
	}
	for _, svc := range []string{"ani-metrics-prometheus", "ani-metrics-alertmanager"} {
		uid, err := a.runner.jsonpath(a.ctx, "service", svc, metricsNS, "{.metadata.uid}")
		if err != nil || uid == "" {
			return fmt.Errorf("metrics Service %s has no identity: %v", svc, err)
		}
	}
	a.evidence["nodes"] = strconv.Itoa(len(nodes.Items))
	r.Evidence["nodes"] = strconv.Itoa(len(nodes.Items))
	r.Evidence["nodeExporterReady"] = ready
	r.Detail = "all declared metrics workloads and both services are present and Ready"
	return nil
}
func metrics02(a *acceptanceAttempt, r *VerifyStepResult) error {
	m := a.metrics
	labels := m.labels()
	pod := metricsObject("v1", "Pod", m.client, labels, map[string]any{"restartPolicy": "Never", "containers": []any{map[string]any{"name": "client", "image": m.image, "imagePullPolicy": "IfNotPresent", "command": []string{"sleep", "36000"}}}})
	if err := a.metricsCreate("pod", m.client, pod); err != nil {
		return err
	}
	if err := a.metricsReadyPod(m.client); err != nil {
		return err
	}
	total, err := metricsNumber(a.evidence["nodes"])
	if err != nil {
		return err
	} // set by METRICS-01 on the shared attempt
	if err = metricsPoll(a.ctx, 2*time.Second, func() (bool, error) {
		reply, e := a.metricsQuery(`up{job="node-exporter"}`)
		if e != nil {
			return false, e
		}
		if len(reply.Data.Result) != total {
			return false, fmt.Errorf("node-exporter up series=%d, nodes=%d", len(reply.Data.Result), total)
		}
		instances := map[string]bool{}
		for _, s := range reply.Data.Result {
			if len(s.Value) < 2 || metricsSampleString(s.Value[1]) != "1" {
				return false, fmt.Errorf("node-exporter target is not up")
			}
			instance := s.Metric["instance"]
			if instance == "" || instances[instance] {
				return false, fmt.Errorf("node-exporter instances are missing or duplicated")
			}
			instances[instance] = true
		}
		return true, nil
	}); err != nil {
		return err
	}
	for _, query := range []string{"count(node_uname_info)", "count(kube_node_info)"} {
		if err = metricsPoll(a.ctx, 2*time.Second, func() (bool, error) {
			n, e := a.metricsScalar(query)
			if e != nil {
				return false, e
			}
			if n != float64(total) {
				return false, fmt.Errorf("%s=%v, nodes=%d", query, n, total)
			}
			return true, nil
		}); err != nil {
			return err
		}
	}
	query := `count(container_memory_working_set_bytes{container!="",container!="POD"})`
	if err = metricsPoll(a.ctx, 2*time.Second, func() (bool, error) {
		n, e := a.metricsScalar(query)
		if e != nil {
			return false, e
		}
		if n <= 0 {
			return false, fmt.Errorf("cAdvisor container series=%v", n)
		}
		r.Evidence["cadvisorSeries"] = fmt.Sprint(n)
		return true, nil
	}); err != nil {
		return err
	}
	r.Detail = "node-exporter, node_uname_info, kube_node_info and cAdvisor series were queried through Prometheus"
	return nil
}

const metricsReceiverServer = `import http.server,os,socketserver
os.makedirs('/data',exist_ok=True)
count=[0]
class Handler(http.server.BaseHTTPRequestHandler):
 def do_POST(self):
  body=self.rfile.read(int(self.headers.get('Content-Length') or 0))
  count[0]+=1
  with open('/data/req-%03d.json'%count[0],'wb') as f:f.write(body)
  self.send_response(200);self.send_header('Content-Type','application/json');self.end_headers();self.wfile.write(b'{}')
 def log_message(self,*args):pass
socketserver.TCPServer.allow_reuse_address=True
with socketserver.TCPServer(('0.0.0.0',8080),Handler) as s:s.serve_forever()
`

func metrics03(a *acceptanceAttempt, r *VerifyStepResult) error {
	m := a.metrics
	labels := m.labels()
	cm := map[string]any{"apiVersion": "v1", "kind": "ConfigMap", "metadata": map[string]any{"name": m.receiver, "namespace": metricsNS, "labels": labels}, "data": map[string]string{"server.py": metricsReceiverServer}}
	if err := a.metricsCreate("configmap", m.receiver, cm); err != nil {
		return err
	}
	dep := metricsObject("apps/v1", "Deployment", m.receiver, labels, map[string]any{
		"replicas": 1, "selector": map[string]any{"matchLabels": map[string]string{"app": m.receiver}},
		"template": map[string]any{"metadata": map[string]any{"labels": map[string]string{"app": m.receiver, "ani_attempt": m.attempt}}, "spec": map[string]any{"containers": []any{map[string]any{"name": "receiver", "image": m.image, "imagePullPolicy": "IfNotPresent", "command": []string{"python3", "/app/server.py"}, "ports": []any{map[string]any{"name": "http", "containerPort": 8080}}, "volumeMounts": []any{map[string]any{"name": "script", "mountPath": "/app", "readOnly": true}, map[string]any{"name": "data", "mountPath": "/data"}}}}, "volumes": []any{map[string]any{"name": "script", "configMap": map[string]any{"name": m.receiver}}, map[string]any{"name": "data", "emptyDir": map[string]any{}}}}},
	})
	if err := a.metricsCreate("deployment", m.receiver, dep); err != nil {
		return err
	}
	svc := metricsObject("v1", "Service", m.receiver, labels, map[string]any{"selector": map[string]string{"app": m.receiver}, "ports": []any{map[string]any{"name": "http", "port": 8080, "targetPort": 8080}}})
	if err := a.metricsCreate("service", m.receiver, svc); err != nil {
		return err
	}
	if err := metricsPoll(a.ctx, 2*time.Second, func() (bool, error) {
		ok, e := metricsWorkloadReady(a.ctx, a.runner, "deployment", m.receiver)
		if e != nil {
			return false, e
		}
		if !ok {
			return false, fmt.Errorf("receiver Deployment is not Ready")
		}
		return true, nil
	}); err != nil {
		return err
	}
	out, err := a.runner.run(a.ctx, "get", "pod", "-n", metricsNS, "-l", "app="+m.receiver, "-o", "json")
	if err != nil {
		return err
	}
	var pods struct {
		Items []struct {
			Metadata struct {
				Name   string
				Labels map[string]string
			}
		}
	}
	if err = json.Unmarshal(out, &pods); err != nil {
		return err
	}
	if len(pods.Items) != 1 || pods.Items[0].Metadata.Labels["ani_attempt"] != m.attempt {
		return fmt.Errorf("receiver selector did not resolve exactly this attempt's pod")
	}
	m.receiverPod = pods.Items[0].Metadata.Name
	if err := a.metricsReadyPod(m.receiverPod); err != nil {
		return err
	}
	r.Evidence["receiverPod"] = m.receiverPod
	r.Evidence["receiverName"] = m.receiver
	r.Detail = "receiver ConfigMap, Deployment and Service were created and UID-registered"
	return nil
}
func metrics04(a *acceptanceAttempt, r *VerifyStepResult) error {
	m := a.metrics
	labels := m.labels()
	cfg := metricsObject("monitoring.coreos.com/v1alpha1", "AlertmanagerConfig", m.config, labels, map[string]any{
		"route":     map[string]any{"receiver": m.receiver, "groupBy": []string{"alertname"}, "groupWait": "5s", "groupInterval": "5s", "repeatInterval": "5m", "matchers": []any{map[string]any{"name": "run_id", "value": m.attempt, "matchType": "="}, map[string]any{"name": "ani_attempt", "value": m.attempt, "matchType": "="}}},
		"receivers": []any{map[string]any{"name": m.receiver, "webhookConfigs": []any{map[string]any{"url": "http://" + m.receiver + "." + metricsNS + ".svc.cluster.local:8080/", "sendResolved": true}}}},
	})
	if err := a.metricsCreate("alertmanagerconfig", m.config, cfg); err != nil {
		return err
	}
	err := metricsPoll(a.ctx, 2*time.Second, func() (bool, error) {
		raw, e := a.metricsPython(m.client, metricsStatusPython, metricsAMURL)
		if e != nil {
			return false, e
		}
		var v struct {
			Config struct {
				Original string `json:"original"`
			} `json:"config"`
		}
		if e = json.Unmarshal([]byte(raw), &v); e != nil {
			return false, e
		}
		if !strings.Contains(v.Config.Original, m.receiver) || !strings.Contains(v.Config.Original, m.attempt) {
			return false, fmt.Errorf("Operator has not loaded route %s for attempt %s", m.receiver, m.attempt)
		}
		return true, nil
	})
	if err != nil {
		return err
	}
	r.Evidence["configName"] = m.config
	r.Detail = "Alertmanager status contains this attempt's receiver and route"
	return nil
}

func metricsRule(m *metricsAttemptState, expr string) map[string]any {
	labels := m.labels()
	labels["release"] = "ani-metrics"
	return metricsObject("monitoring.coreos.com/v1", "PrometheusRule", m.rule, labels, map[string]any{
		"groups": []any{map[string]any{"name": "ani-metrics-test-" + m.attempt, "interval": "5s", "rules": []any{map[string]any{"alert": metricsAlert, "expr": expr, "for": "0s", "labels": map[string]string{"run_id": m.attempt, "ani_attempt": m.attempt, "severity": "test", "namespace": metricsNS}, "annotations": map[string]string{"summary": "ANI metrics lifecycle test"}}}}},
	})
}
func metricsAlertQuery(m *metricsAttemptState) string {
	return fmt.Sprintf(`ALERTS{alertname="%s",run_id="%s",ani_attempt="%s"}`, metricsAlert, m.attempt, m.attempt)
}
func metrics05(a *acceptanceAttempt, r *VerifyStepResult) error {
	m := a.metrics
	if err := a.metricsCreate("prometheusrule", m.rule, metricsRule(m, "vector(1) == 1")); err != nil {
		return err
	}
	selector := metricsAlertQuery(m)
	if err := metricsPoll(a.ctx, 2*time.Second, func() (bool, error) {
		reply, e := a.metricsQuery(selector)
		if e != nil {
			return false, e
		}
		for _, s := range reply.Data.Result {
			if s.Metric["alertstate"] == "firing" && s.Metric["namespace"] == metricsNS {
				return true, nil
			}
		}
		return false, fmt.Errorf("Prometheus has no firing ALERTS series for %s", m.attempt)
	}); err != nil {
		return err
	}
	if err := metricsPoll(a.ctx, 2*time.Second, func() (bool, error) {
		fp, e := a.metricsRequests("firing")
		if e != nil {
			return false, e
		}
		m.firingFingerprint = fp
		return true, nil
	}); err != nil {
		return err
	}
	r.Evidence["ruleName"] = m.rule
	r.Evidence["firingFingerprint"] = m.firingFingerprint
	r.Evidence["alertQuery"] = selector
	r.Detail = "Prometheus firing series and receiver webhook carried this attempt's labels and fingerprint"
	return nil
}
func (a *acceptanceAttempt) metricsReplaceRule(expr string) error {
	m := a.metrics
	uid, err := a.runner.jsonpath(a.ctx, "prometheusrule", m.rule, metricsNS, "{.metadata.uid}")
	if err != nil {
		return err
	}
	found := false
	for _, o := range m.owned {
		if o.Kind == "prometheusrule" && o.Name == m.rule && o.UID == uid {
			found = true
		}
	}
	if !found {
		return fmt.Errorf("rule %s is not the UID this attempt created; refusing update", m.rule)
	}
	version, err := a.runner.jsonpath(a.ctx, "prometheusrule", m.rule, metricsNS, "{.metadata.resourceVersion}")
	if err != nil || version == "" {
		return fmt.Errorf("cannot bind rule update to resourceVersion: %v", err)
	}
	rule := metricsRule(m, expr)
	md := rule["metadata"].(map[string]any)
	md["uid"] = uid
	md["resourceVersion"] = version
	data, err := json.MarshalIndent(rule, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(m.outputDir, "prometheusrule-resolved-"+m.rule+".yaml")
	if err := os.WriteFile(path, data, 0600); err != nil {
		return err
	}
	_, err = a.runner.run(a.ctx, "replace", "-f", path)
	return err
}
func metrics06(a *acceptanceAttempt, r *VerifyStepResult) error {
	m := a.metrics
	if m.firingFingerprint == "" {
		return errors.New("no firing fingerprint from METRICS-05")
	}
	if err := a.metricsReplaceRule("vector(0) == 1"); err != nil {
		return err
	}
	selector := metricsAlertQuery(m)
	if err := metricsPoll(a.ctx, 2*time.Second, func() (bool, error) {
		n, e := a.metricsScalar("count(" + selector + ") or vector(0)")
		if e != nil {
			return false, e
		}
		if n != 0 {
			return false, fmt.Errorf("Prometheus still reports %v firing alerts", n)
		}
		return true, nil
	}); err != nil {
		return err
	}
	var resolved string
	if err := metricsPoll(a.ctx, 2*time.Second, func() (bool, error) {
		fp, e := a.metricsRequests("resolved")
		if e != nil {
			return false, e
		}
		resolved = fp
		return true, nil
	}); err != nil {
		return err
	}
	if resolved != m.firingFingerprint {
		return fmt.Errorf("resolved fingerprint %s differs from firing fingerprint %s", resolved, m.firingFingerprint)
	}
	r.Evidence["resolvedFingerprint"] = resolved
	r.Evidence["ruleName"] = m.rule
	r.Evidence["firingFingerprint"] = m.firingFingerprint
	r.Detail = "Prometheus alert resolved and receiver delivered the same fingerprint"
	return nil
}
func metrics07(a *acceptanceAttempt, r *VerifyStepResult) error {
	m := a.metrics
	if m.markerValue != "" {
		return errors.New("marker was already written; a new value would invalidate the original-sample check")
	}
	m.markerValue = strconv.FormatInt(time.Now().UnixNano()%1000000000000+1, 10)
	m.markerTS = time.Now().UTC().UnixMilli()
	m.markerStart = m.markerTS/1000 - 60
	m.markerEnd = m.markerTS/1000 + 180
	m.markerQuery = fmt.Sprintf(`ani_metrics_rebuild_marker{run_id="%s"}`, m.attempt)
	// Record the exact original sample identity before the remote write. These
	// fields remain in the failing step result if write/query later fails.
	r.Evidence["markerValue"] = m.markerValue
	r.Evidence["markerTSMillis"] = strconv.FormatInt(m.markerTS, 10)
	r.Evidence["rangeStart"] = strconv.FormatInt(m.markerStart, 10)
	r.Evidence["rangeEnd"] = strconv.FormatInt(m.markerEnd, 10)
	r.Evidence["markerQuery"] = m.markerQuery
	original, err := json.Marshal(r.Evidence)
	if err != nil {
		return err
	}
	if err := m.saveEvidence("marker-original.json", original); err != nil {
		return err
	}
	if _, err := a.metricsPython(m.client, metricsWritePython, metricsPromURL, "ani_metrics_rebuild_marker", m.attempt, m.markerValue, strconv.FormatInt(m.markerTS, 10)); err != nil {
		return err
	}
	if err := metricsPoll(a.ctx, 2*time.Second, func() (bool, error) {
		reply, e := a.metricsQuery(m.markerQuery)
		if e != nil {
			return false, e
		}
		for _, s := range reply.Data.Result {
			if s.Metric["run_id"] == m.attempt && s.Metric["__name__"] == "ani_metrics_rebuild_marker" && len(s.Value) >= 2 && metricsValueEquals(s.Value[1], m.markerValue) {
				return true, nil
			}
		}
		return false, fmt.Errorf("original marker is not queryable")
	}); err != nil {
		return err
	}
	if err := metricsPoll(a.ctx, 2*time.Second, func() (bool, error) {
		e := a.metricsRange(m.markerQuery, m.markerValue, m.markerStart, m.markerEnd)
		return e == nil, e
	}); err != nil {
		return err
	}
	m.markerConfirmed = true
	r.Detail = "remote-write sample and its original range were queryable before reconstruction"
	return nil
}
func metricsCopyRecreate(result *VerifyStepResult, step VerifyStepResult) error {
	result.QuotaState = step.QuotaState
	result.Detail = step.Detail
	for k, v := range step.Evidence {
		result.Evidence[k] = v
	}
	if step.Status != VerifyStatusPass {
		return errors.New(step.Detail)
	}
	return nil
}
func metrics08(a *acceptanceAttempt, r *VerifyStepResult) error {
	m := a.metrics
	if !m.markerConfirmed || m.markerValue == "" {
		return errors.New("METRICS-07 did not confirm the original sample")
	}
	t := acceptanceTargets["prometheus"]
	step := a.recreateOnce(t, persistenceCheck{
		Write: func(_ *acceptanceAttempt, _ acceptanceTarget, _ string) (string, bool) { return "", m.markerConfirmed },
		AfterRecreation: func(a *acceptanceAttempt, t acceptanceTarget, evidence map[string]string) (string, bool) {
			uid, err := a.runner.jsonpath(a.ctx, "statefulset", t.ControllerName, t.Namespace, "{.metadata.uid}")
			if err != nil || uid != evidence["controllerUid"] {
				return fmt.Sprintf("Prometheus StatefulSet UID changed from %s to %s: %v", evidence["controllerUid"], uid, err), false
			}
			newPod, newStorage, newController, _, checkErr := authorizeTarget(a.ctx, a.runner, t)
			if checkErr != nil || newPod != evidence["newPodUID"] || newStorage != evidence["oldPVCUID"] || newController != evidence["controllerUid"] {
				return fmt.Sprintf("replacement Prometheus Pod does not use the original owner and PVC: %v", checkErr), false
			}
			evidence["newControllerUID"] = uid
			return "", true
		},
		ReadBack: func(a *acceptanceAttempt, _ acceptanceTarget, _ string) (string, bool) {
			err := a.metricsRange(m.markerQuery, m.markerValue, m.markerStart, m.markerEnd)
			if err != nil {
				return fmt.Sprintf("the original Prometheus sample was lost after recreation: %v", err), false
			}
			return "", true
		},
	})
	if err := metricsCopyRecreate(r, step); err != nil {
		return err
	}
	r.Evidence["originalMarkerValue"] = m.markerValue
	r.Evidence["originalMarkerTSMillis"] = strconv.FormatInt(m.markerTS, 10)
	r.Detail = "new Ready Prometheus Pod UID, same StatefulSet/PVC UIDs and original sample in original range"
	return nil
}

type metricsSilenceMatcher struct {
	Name, Value string
	IsRegex     bool  `json:"isRegex"`
	IsEqual     *bool `json:"isEqual"` // absent means the API's default equality matcher
}

type metricsSilence struct {
	Matchers               []metricsSilenceMatcher `json:"matchers"`
	CreatedBy, Comment, ID string
	StartsAt, EndsAt       string
	UpdatedAt              string
	Annotations            map[string]string
	Status                 struct{ State string }
}

type metricsSilenceMatcherValue struct {
	Value            string
	IsRegex, IsEqual bool
}

func metricsSilenceMatcherSet(s metricsSilence) (map[string]metricsSilenceMatcherValue, error) {
	set := make(map[string]metricsSilenceMatcherValue, len(s.Matchers))
	for _, matcher := range s.Matchers {
		if matcher.Name == "" {
			return nil, errors.New("silence has a matcher with no name")
		}
		if _, exists := set[matcher.Name]; exists {
			return nil, fmt.Errorf("silence has duplicate matcher %s", matcher.Name)
		}
		isEqual := matcher.IsEqual == nil || *matcher.IsEqual
		set[matcher.Name] = metricsSilenceMatcherValue{matcher.Value, matcher.IsRegex, isEqual}
	}
	return set, nil
}

func metricsSilenceTime(field, raw string) (time.Time, error) {
	parsed, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		return time.Time{}, fmt.Errorf("silence %s invalid: %w", field, err)
	}
	return parsed, nil
}

// A newly-created silence may have StartsAt advanced to the server's now in
// Alertmanager v0.32.1. In that branch Silences.Set sets UpdatedAt to the same
// now. Only this first GET can establish that normalized server time.
func metricsSilenceMatchesRequest(raw []byte, m *metricsAttemptState) (metricsSilence, error) {
	var got, requested metricsSilence
	if err := json.Unmarshal(raw, &got); err != nil {
		return got, err
	}
	if err := json.Unmarshal(m.silenceJSON, &requested); err != nil {
		return got, err
	}
	if got.ID != m.silenceID || got.CreatedBy != requested.CreatedBy || got.Comment != requested.Comment {
		return got, fmt.Errorf("silence ID/owner/comment differs from request: id=%q owner=%q", got.ID, got.CreatedBy)
	}
	if len(got.Annotations) != 0 {
		return got, errors.New("silence acquired unexpected annotations")
	}
	want := map[string]metricsSilenceMatcherValue{
		"alertname":   {Value: metricsAlert, IsEqual: true},
		"run_id":      {Value: m.attempt, IsEqual: true},
		"ani_attempt": {Value: m.attempt, IsEqual: true},
	}
	matchers, err := metricsSilenceMatcherSet(got)
	if err != nil {
		return got, err
	}
	if !maps.Equal(matchers, want) {
		return got, fmt.Errorf("silence matchers differ from this attempt's exact equality conditions: %v", matchers)
	}
	requestedStart, err := metricsSilenceTime("requested startsAt", requested.StartsAt)
	if err != nil {
		return got, err
	}
	requestedEnd, err := metricsSilenceTime("requested endsAt", requested.EndsAt)
	if err != nil {
		return got, err
	}
	serverStart, err := metricsSilenceTime("server startsAt", got.StartsAt)
	if err != nil {
		return got, err
	}
	serverEnd, err := metricsSilenceTime("server endsAt", got.EndsAt)
	if err != nil {
		return got, err
	}
	updated, err := metricsSilenceTime("server updatedAt", got.UpdatedAt)
	if err != nil {
		return got, err
	}
	if !serverEnd.Equal(requestedEnd) || !serverStart.Before(serverEnd) {
		return got, errors.New("silence end or time window differs from request")
	}
	if serverStart.Equal(requestedStart) {
		if updated.After(serverStart) {
			return got, errors.New("unchanged silence start is earlier than server updatedAt")
		}
	} else if !serverStart.After(requestedStart) || !serverStart.Equal(updated) {
		return got, errors.New("silence startsAt is neither the requested time nor the server's creation time")
	}
	return got, nil
}

// METRICS-10 compares only with the first confirmed server GET. It never
// tolerates a second normalization or replaces the baseline after recreation.
func metricsSilenceMatchesBaseline(raw []byte, m *metricsAttemptState) error {
	if len(m.silenceServerJSON) == 0 || !m.silenceConfirmed {
		return errors.New("no confirmed pre-recreation server silence baseline")
	}
	var got, baseline metricsSilence
	if err := json.Unmarshal(raw, &got); err != nil {
		return err
	}
	if err := json.Unmarshal(m.silenceServerJSON, &baseline); err != nil {
		return err
	}
	if got.ID != m.silenceID || got.ID != baseline.ID || got.CreatedBy != baseline.CreatedBy || got.Comment != baseline.Comment || !maps.Equal(got.Annotations, baseline.Annotations) {
		return errors.New("silence ID, owner, comment or annotations changed from the server baseline")
	}
	gotMatchers, err := metricsSilenceMatcherSet(got)
	if err != nil {
		return err
	}
	baseMatchers, err := metricsSilenceMatcherSet(baseline)
	if err != nil {
		return err
	}
	if !maps.Equal(gotMatchers, baseMatchers) {
		return errors.New("silence matchers changed from the server baseline")
	}
	for _, field := range []struct{ name, current, original string }{
		{"startsAt", got.StartsAt, baseline.StartsAt},
		{"endsAt", got.EndsAt, baseline.EndsAt},
		{"updatedAt", got.UpdatedAt, baseline.UpdatedAt},
	} {
		current, err := metricsSilenceTime(field.name, field.current)
		if err != nil {
			return err
		}
		original, err := metricsSilenceTime("baseline "+field.name, field.original)
		if err != nil {
			return err
		}
		if !current.Equal(original) {
			return fmt.Errorf("silence %s changed from the server baseline", field.name)
		}
	}
	return nil
}
func metrics09(a *acceptanceAttempt, r *VerifyStepResult) error {
	m := a.metrics
	if m.silenceID != "" {
		return errors.New("silence already created in this attempt")
	}
	now := time.Now().UTC()
	payload := map[string]any{"matchers": []any{map[string]any{"name": "alertname", "value": metricsAlert, "isRegex": false}, map[string]any{"name": "run_id", "value": m.attempt, "isRegex": false}, map[string]any{"name": "ani_attempt", "value": m.attempt, "isRegex": false}}, "startsAt": now.Format(time.RFC3339), "endsAt": now.Add(2 * time.Hour).Format(time.RFC3339), "createdBy": "ani-installer-" + m.attempt, "comment": "ANI metrics persistence check " + m.attempt}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	m.silenceJSON = body
	r.Evidence["silencePayload"] = string(body)
	if err := m.saveEvidence("silence-original.json", body); err != nil {
		return err
	}
	raw, err := a.metricsPython(m.client, metricsSilenceCreatePython, metricsAMURL, string(body))
	if err != nil {
		return err
	}
	var created struct {
		SilenceID string `json:"silenceID"`
	}
	if err = json.Unmarshal([]byte(raw), &created); err != nil {
		return err
	}
	if created.SilenceID == "" {
		return errors.New("Alertmanager returned no silenceID")
	}
	m.silenceID = created.SilenceID
	r.Evidence["silenceID"] = m.silenceID
	// Persist the exact ID before any later action so failed attempts retain it.
	if err := m.saveEvidence("silence-id", []byte(m.silenceID+"\n")); err != nil {
		return err
	}
	raw, err = a.metricsPython(m.client, metricsSilenceGetPython, metricsAMURL, m.silenceID)
	if err != nil {
		return err
	}
	confirmed, err := metricsSilenceMatchesRequest([]byte(raw), m)
	if err != nil {
		return err
	}
	if err := m.saveEvidence("silence-server-baseline.json", []byte(raw)); err != nil {
		return err
	}
	m.silenceServerJSON = []byte(raw)
	m.silenceConfirmed = true
	r.Evidence["requestedStartsAt"] = now.Format(time.RFC3339)
	r.Evidence["serverStartsAt"] = confirmed.StartsAt
	r.Evidence["serverUpdatedAt"] = confirmed.UpdatedAt
	r.Evidence["serverEndsAt"] = confirmed.EndsAt
	r.Detail = "silence was created and read back by its original ID; server time baseline is fixed"
	return nil
}
func metrics10(a *acceptanceAttempt, r *VerifyStepResult) error {
	m := a.metrics
	if !m.silenceConfirmed || m.silenceID == "" {
		return errors.New("METRICS-09 did not confirm the original silence")
	}
	uid, err := a.runner.jsonpath(a.ctx, "secret", metricsSecret, metricsNS, "{.metadata.uid}")
	if err != nil || uid == "" {
		return fmt.Errorf("cannot bind generated Secret identity: %v", err)
	}
	m.secretUID = uid
	t := acceptanceTargets["alertmanager"]
	step := a.recreateOnce(t, persistenceCheck{
		Write: func(_ *acceptanceAttempt, _ acceptanceTarget, _ string) (string, bool) { return "", m.silenceConfirmed },
		AfterRecreation: func(a *acceptanceAttempt, t acceptanceTarget, evidence map[string]string) (string, bool) {
			controller, err := a.runner.jsonpath(a.ctx, "statefulset", t.ControllerName, t.Namespace, "{.metadata.uid}")
			if err != nil || controller != evidence["controllerUid"] {
				return fmt.Sprintf("Alertmanager StatefulSet UID changed from %s to %s: %v", evidence["controllerUid"], controller, err), false
			}
			secret, err := a.runner.jsonpath(a.ctx, "secret", metricsSecret, metricsNS, "{.metadata.uid}")
			if err != nil || secret != m.secretUID {
				return fmt.Sprintf("Alertmanager generated Secret UID changed from %s to %s: %v", m.secretUID, secret, err), false
			}
			newPod, newStorage, newController, _, checkErr := authorizeTarget(a.ctx, a.runner, t)
			if checkErr != nil || newPod != evidence["newPodUID"] || newStorage != evidence["oldPVCUID"] || newController != evidence["controllerUid"] {
				return fmt.Sprintf("replacement Alertmanager Pod does not use the original owner and PVC: %v", checkErr), false
			}
			evidence["oldSecretUID"] = m.secretUID
			evidence["newSecretUID"] = secret
			evidence["newControllerUID"] = controller
			return "", true
		},
		ReadBack: func(a *acceptanceAttempt, _ acceptanceTarget, _ string) (string, bool) {
			raw, err := a.metricsPython(m.client, metricsSilenceGetPython, metricsAMURL, m.silenceID)
			if err != nil {
				return fmt.Sprintf("original silence ID %s unreadable after reconstruction: %v", m.silenceID, err), false
			}
			if err := metricsSilenceMatchesBaseline([]byte(raw), m); err != nil {
				return fmt.Sprintf("original silence content changed: %v", err), false
			}
			return "", true
		},
	})
	if err := metricsCopyRecreate(r, step); err != nil {
		return err
	}
	r.Evidence["silenceID"] = m.silenceID
	r.Detail = "new Ready Alertmanager Pod UID, same StatefulSet/PVC/generated Secret UIDs and same silence ID/content"
	return nil
}
func metrics11(a *acceptanceAttempt, r *VerifyStepResult) error {
	m := a.metrics
	if m.silenceID == "" {
		return errors.New("there is no registered silence ID to clean")
	}
	raw, err := a.metricsPython(m.client, metricsSilenceDeletePython, metricsAMURL, m.silenceID)
	if err != nil {
		return fmt.Errorf("expire only this attempt's silence %s: %w", m.silenceID, err)
	}
	code, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || code < 200 || code >= 300 {
		return fmt.Errorf("silence cleanup returned %q, expected 2xx", raw)
	}
	// Alertmanager acknowledges expiration before every reader necessarily sees
	// it. Confirm this exact ID is expired before cleaning the receiver/rule.
	if err := metricsPoll(a.ctx, 2*time.Second, func() (bool, error) {
		body, e := a.metricsPython(m.client, metricsSilenceGetPython, metricsAMURL, m.silenceID)
		if e != nil {
			return false, e
		}
		var current struct {
			ID     string `json:"id"`
			Status struct {
				State string `json:"state"`
			} `json:"status"`
		}
		if e := json.Unmarshal([]byte(body), &current); e != nil {
			return false, e
		}
		if current.ID != m.silenceID || current.Status.State != "expired" {
			return false, fmt.Errorf("silence %s still has state %q", m.silenceID, current.Status.State)
		}
		return true, nil
	}); err != nil {
		return fmt.Errorf("cannot confirm this attempt's silence expired: %w", err)
	}
	cleaned := len(m.owned)
	if err := a.metricsDeleteOwned(); err != nil {
		return err
	}
	r.Evidence["silenceID"] = m.silenceID
	r.Evidence["cleanedObjects"] = strconv.Itoa(cleaned)
	r.Detail = "only this attempt's silence ID and UID-registered temporary objects were cleaned"
	return nil
}
