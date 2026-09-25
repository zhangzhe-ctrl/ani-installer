/*
Copyright 2026 The KubeSphere Contributors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package ani

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// R11 behaviour tests: the generic network smoke checker and the verify.sh
// stack branch are driven by a fake kubectl that records every call. Nothing
// reaches a cluster. The scripts under test are the real ones.
// ---------------------------------------------------------------------------

const (
	r11VerifyRel   = "../../scripts/verify.sh"
	r11NetProbeRel = "../../builtin/core/roles/ani/smoke/templates/network-probe.sh"
	r11EnvoyRel    = "../../builtin/core/roles/ani/smoke/templates/probe.sh"
)

// r11FullClientLog is the client log a fully successful run produces.
const r11FullClientLog = `NET-PODIP-OK
NET-SVCIP-OK
NET-DNS-OK
ANI-NETWORK-OK
`

// r11WrongBodyLog simulates HTTP 200 answers whose body is wrong: the client
// aborts with the body-mismatch exit code and never prints the markers.
const r11WrongBodyLog = `# wget: server responded HTTP/1.0 200 OK
# body mismatch: got 'something-else', want the run token
`

// r11DNSSFailLog simulates a DNS-resolution failure: the first two probes
// succeeded, the cluster-local name did not resolve.
const r11DNSSFailLog = `NET-PODIP-OK
NET-SVCIP-OK
`

// r11FakeKubectl writes a fake kubectl for network-probe.sh. It records every
// argv (and every created manifest) in KUBECTL_LOG and answers the checker's
// queries from plain env-configured state:
//   FAKE_STATE        state directory (call counters)
//   FAKE_SERVER_NODE  node the server pod reports     (default node1)
//   FAKE_CLIENT_NODES client nodes, space separated   (default "node2 node3")
//   FAKE_CLIENT_EXIT  client exit code                (default 0)
//   FAKE_CLIENT_LOG   file whose content is the client pod log
//   FAKE_PHASE        override phase answer ("Failed" simulates a failed pod)
//   FAKE_NO_IP        when set, podIP/clusterIP answers are empty
func r11FakeKubectl(t *testing.T) (binDir, logPath, stateDir string) {
	t.Helper()
	dir := t.TempDir()
	logPath = filepath.Join(dir, "kubectl-calls.log")
	stateDir = filepath.Join(dir, "state")
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		t.Fatalf("create state dir: %v", err)
	}
	body := `#!/usr/bin/env bash
printf '%s\n' "$*" >> "$KUBECTL_LOG"
state="${FAKE_STATE:?}"
if [[ "$*" == *"create -f -"* ]]; then
  input="$(cat)"
  printf '%s\n' "--- manifest ---" >> "$KUBECTL_LOG"
  printf '%s\n' "$input" >> "$KUBECTL_LOG"
  case "$input" in
    *"generateName: ani-net-smoke-server-"*)
      n=$(cat "$state/server-count" 2>/dev/null || echo 0); n=$((n+1)); echo "$n" > "$state/server-count"
      echo "ani-net-smoke-server-abc$n"; exit 0 ;;
    *"generateName: ani-net-smoke-client-"*)
      n=$(cat "$state/client-count" 2>/dev/null || echo 0); n=$((n+1)); echo "$n" > "$state/client-count"
      echo "ani-net-smoke-client-xyz$n"; exit 0 ;;
  esac
  exit 0
fi
case "$*" in
  *"create namespace"*|*"label namespace"*)
    exit 0 ;;
  *"get nodes"*"metadata.name"*)
    printf 'node1\nnode2\nnode3\n'; exit 0 ;;
  *"get nodes"*"Ready"*)
    printf 'True\nTrue\nTrue\n'; exit 0 ;;
  *"get service"*"clusterIP"*)
    if [ -n "${FAKE_NO_IP:-}" ]; then exit 0; fi
    echo "10.96.0.150"; exit 0 ;;
  *"get pod"*"status.podIP"*)
    if [ -n "${FAKE_NO_IP:-}" ]; then exit 0; fi
    echo "10.244.1.10"; exit 0 ;;
  *"get pod"*"spec.nodeName"*)
    if [[ "$*" == *"ani-net-smoke-server"* ]]; then
      echo "${FAKE_SERVER_NODE:-node1}"
    else
      # FAKE_SAME_NODE collapses every client onto the server node (T-R11-02).
      if [ -n "${FAKE_SAME_NODE:-}" ]; then echo "${FAKE_SERVER_NODE:-node1}"; exit 0; fi
      n=$(cat "$state/answer-count" 2>/dev/null || echo 0); n=$((n+1)); echo "$n" > "$state/answer-count"
      echo "${FAKE_CLIENT_NODES:-node2 node3}" | tr ' ' '\n' | sed -n "$(( (n-1) % 2 + 1 ))p"
    fi
    exit 0 ;;
  *"get pod"*"status.phase"*)
    if [ -n "${FAKE_PHASE:-}" ]; then echo "$FAKE_PHASE"; exit 0; fi
    if [[ "$*" == *"ani-net-smoke-server"* ]]; then echo "Running"; else echo "Succeeded"; fi
    exit 0 ;;
  *"get pod"*"terminated.exitCode"*)
    echo "${FAKE_CLIENT_EXIT:-0}"; exit 0 ;;
  *"logs"*)
    cat "${FAKE_CLIENT_LOG:?}"; exit 0 ;;
  *"get events"*|*"describe"*|*"get pod"*"-o yaml"*|*"get pods"*|*"get namespace"*)
    echo "fake diagnostics"; exit 0 ;;
  *"delete namespace"*)
    exit 0 ;;
esac
exit 0
`
	script := filepath.Join(dir, "kubectl")
	if err := os.WriteFile(script, []byte(body), 0o700); err != nil {
		t.Fatalf("write fake kubectl: %v", err)
	}
	noSleep := "#!/usr/bin/env bash\nexit 0\n"
	if err := os.WriteFile(filepath.Join(dir, "sleep"), []byte(noSleep), 0o700); err != nil {
		t.Fatalf("write fake sleep: %v", err)
	}
	return dir, logPath, stateDir
}

// r11RunChecker runs the real network-probe.sh with the fake kubectl on PATH.
func r11RunChecker(t *testing.T, env map[string]string) (string, string, int) {
	t.Helper()
	netProbe, err := filepath.Abs(r11NetProbeRel)
	if err != nil {
		t.Fatalf("abs: %v", err)
	}
	outBase := env["ANI_SMOKE_OUTPUT"]
	script := `set +e
export PATH="$BIN:$PATH"
export KUBECONFIG_FILE="$BASE/kubeconfig"
export ANI_SMOKE_OUTPUT="$OUT"
export KUBECTL_LOG="$KLOG"
bash "$NETPROBE"
rc=$?
echo "NETCHECK_RC=$rc"
`
	stdout, stderr, code := r02RunBash(t, script, map[string]string{
		"PATH":    env["BIN"] + ":" + os.Getenv("PATH"),
		"BASE":    env["BASE"],
		"OUT":     outBase,
		"KLOG":    env["KLOG"],
		"NETPROBE": netProbe,
		// checker inputs
		"ANI_NETSMOKE_IMAGE": env["ANI_NETSMOKE_IMAGE"],
		"FAKE_STATE":         env["FAKE_STATE"],
		"FAKE_SERVER_NODE":   env["FAKE_SERVER_NODE"],
		"FAKE_CLIENT_NODES":  env["FAKE_CLIENT_NODES"],
		"FAKE_CLIENT_EXIT":   env["FAKE_CLIENT_EXIT"],
		"FAKE_CLIENT_LOG":    env["FAKE_CLIENT_LOG"],
		"FAKE_PHASE":         env["FAKE_PHASE"],
		"FAKE_NO_IP":         env["FAKE_NO_IP"],
		"FAKE_SAME_NODE":     env["FAKE_SAME_NODE"],
	})
	if idx := strings.Index(stdout, "NETCHECK_RC="); idx >= 0 {
		code = int(stdout[idx+len("NETCHECK_RC=")] - '0')
	}
	return stdout, stderr, code
}

// r11Setup prepares kubeconfig, output dir and the default client log.
func r11Setup(t *testing.T) (baseDir, clientLogPath string) {
	t.Helper()
	baseDir = t.TempDir()
	if err := os.WriteFile(filepath.Join(baseDir, "kubeconfig"), []byte("fake\n"), 0o600); err != nil {
		t.Fatalf("write kubeconfig: %v", err)
	}
	clientLogPath = filepath.Join(baseDir, "client-log.txt")
	if err := os.WriteFile(clientLogPath, []byte(r11FullClientLog), 0o600); err != nil {
		t.Fatalf("write client log: %v", err)
	}
	return baseDir, clientLogPath
}

// r11Env assembles the standard environment for one checker run.
func r11Env(binDir, logPath, stateDir, baseDir, clientLog string) map[string]string {
	return map[string]string{
		"BIN":                binDir,
		"BASE":               baseDir,
		"KLOG":               logPath,
		"FAKE_STATE":         stateDir,
		"ANI_SMOKE_OUTPUT":   filepath.Join(baseDir, "output"),
		"ANI_NETSMOKE_IMAGE": "192.0.2.11:5000/library/busybox:1.37.0",
		"FAKE_CLIENT_LOG":    clientLog,
	}
}

// r11EnvPlus overlays extra fake-kubectl settings onto the standard env.
func r11EnvPlus(base map[string]string, extra map[string]string) map[string]string {
	merged := map[string]string{}
	for key, value := range base {
		merged[key] = value
	}
	for key, value := range extra {
		merged[key] = value
	}
	return merged
}

// T-R11-01 (positive path first, inside TestNetworkProbe): a healthy run
// passes with per-pair structured evidence and precisely deletes its own
// run namespace; nothing else is touched.
func TestNetworkProbe(t *testing.T) {
	t.Run("pass across scheduled nodes with per-pair evidence", func(t *testing.T) {
		baseDir, clientLog := r11Setup(t)
		binDir, logPath, stateDir := r11FakeKubectl(t)
		stdout, stderr, code := r11RunChecker(t, r11Env(binDir, logPath, stateDir, baseDir, clientLog))
		if code != 0 {
			t.Fatalf("healthy run must pass, got rc=%d\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
		}
		if !strings.Contains(stdout, "ANI-NETWORK-OK") {
			t.Fatalf("success must print the network marker:\n%s", stdout)
		}
		calls, err := os.ReadFile(logPath)
		if err != nil {
			t.Fatalf("read call log: %v", err)
		}
		// Real requests with content assertions happen inside the client pod
		// script; the manifest must carry PodIP, ClusterIP and DNS targets.
		for _, want := range []string{"http://10.244.1.10:3000/", "http://10.96.0.150:3000/", "http://ani-net-smoke-svc.", ".svc.cluster.local:3000/"} {
			if !strings.Contains(string(calls), want) {
				t.Fatalf("client manifest must target %s", want)
			}
		}
		// Find the run output dir and check the pair evidence.
		outRoot := filepath.Join(baseDir, "output")
		entries, err := os.ReadDir(outRoot)
		if err != nil {
			t.Fatalf("read output root: %v", err)
		}
		var summary string
		for _, entry := range entries {
			data, err := os.ReadFile(filepath.Join(outRoot, entry.Name(), "summary.txt"))
			if err != nil {
				continue
			}
			summary = string(data)
		}
		if summary == "" {
			t.Fatalf("no summary.txt written under %s", outRoot)
		}
		if got := strings.Count(summary, "result=pass at="); got != 2 {
			t.Fatalf("expected one pass line per cross-node pair (2), got %d:\n%s", got, summary)
		}
		if strings.Count(summary, "src=node2 dst=node1") != 1 || strings.Count(summary, "src=node3 dst=node1") != 1 {
			t.Fatalf("pair evidence must name the actual source/destination nodes:\n%s", summary)
		}
		if !strings.Contains(summary, "markers=4/4") {
			t.Fatalf("pair evidence must record the response markers:\n%s", summary)
		}
		if strings.Count(string(calls), "delete namespace") != 1 {
			t.Fatalf("success must clean up exactly the run namespace once:\n%s", calls)
		}
	})

	// T-R11-01a: a client that reaches HTTP 200 but gets a wrong body must
	// fail the whole probe — a 200 alone is never success.
	t.Run("http 200 with wrong body fails", func(t *testing.T) {
		baseDir, _ := r11Setup(t)
		clientLog := filepath.Join(baseDir, "client-log.txt")
		if err := os.WriteFile(clientLog, []byte(r11WrongBodyLog), 0o600); err != nil {
			t.Fatalf("write client log: %v", err)
		}
		binDir, logPath, stateDir := r11FakeKubectl(t)
		stdout, stderr, code := r11RunChecker(t, r11EnvPlus(r11Env(binDir, logPath, stateDir, baseDir, clientLog), map[string]string{
			"FAKE_CLIENT_EXIT": "11",
		}))
		if code == 0 {
			t.Fatalf("wrong body must fail the probe\nstdout:\n%s", stdout)
		}
		if strings.Contains(stdout, "ANI-NETWORK-OK") {
			t.Fatalf("a failed probe must not print the network marker:\n%s", stdout)
		}
		if combined := stdout + stderr; !strings.Contains(combined, "exited 11") || !strings.Contains(combined, "body mismatch") {
			t.Fatalf("failure must name the body-mismatch exit path:\n%s\n%s", stdout, stderr)
		}
		calls, _ := os.ReadFile(logPath)
		if strings.Contains(string(calls), "delete namespace") {
			t.Fatalf("a failed run must keep the scene for forensics:\n%s", calls)
		}
	})

	// T-R11-01b: nodes Ready but DNS resolution broken must end in failure.
	t.Run("dns failure fails even with pods running", func(t *testing.T) {
		baseDir, _ := r11Setup(t)
		clientLog := filepath.Join(baseDir, "client-log.txt")
		if err := os.WriteFile(clientLog, []byte(r11DNSSFailLog), 0o600); err != nil {
			t.Fatalf("write client log: %v", err)
		}
		binDir, logPath, stateDir := r11FakeKubectl(t)
		stdout, _, code := r11RunChecker(t, r11EnvPlus(r11Env(binDir, logPath, stateDir, baseDir, clientLog), map[string]string{
			"FAKE_CLIENT_EXIT": "14",
		}))
		if code == 0 {
			t.Fatalf("DNS failure must fail the probe\nstdout:\n%s", stdout)
		}
		if strings.Contains(stdout, "NET-DNS-OK") || strings.Contains(stdout, "ANI-NETWORK-OK") {
			t.Fatalf("DNS failure must not report any DNS/network success:\n%s", stdout)
		}
	})

	// T-R11-02: a client scheduled on the server node can never report a
	// cross-node pass — the checker must fail on the scheduling assertion.
	t.Run("same-node schedule fails instead of reporting a cross-node pass", func(t *testing.T) {
		baseDir, clientLog := r11Setup(t)
		binDir, logPath, stateDir := r11FakeKubectl(t)
		stdout, stderr, code := r11RunChecker(t, r11EnvPlus(r11Env(binDir, logPath, stateDir, baseDir, clientLog), map[string]string{
			"FAKE_SAME_NODE": "1",
		}))
		if code == 0 {
			t.Fatalf("same-node schedule must fail\nstdout:\n%s", stdout)
		}
		if combined := stdout + stderr; !strings.Contains(combined, "scheduled on the server node") {
			t.Fatalf("failure must name the scheduling assertion:\n%s\n%s", stdout, stderr)
		}
		if strings.Contains(stdout, "ANI-NETWORK-OK") || strings.Contains(stdout, "pair=") {
			t.Fatalf("no cross-node pair evidence may be printed:\n%s", stdout)
		}
	})

	// Missing image env must fail closed: the checker never invents an image.
	t.Run("missing locked image ref fails closed", func(t *testing.T) {
		baseDir, clientLog := r11Setup(t)
		binDir, logPath, stateDir := r11FakeKubectl(t)
		env := r11Env(binDir, logPath, stateDir, baseDir, clientLog)
		env["ANI_NETSMOKE_IMAGE"] = ""
		stdout, _, code := r11RunChecker(t, env)
		if code == 0 {
			t.Fatalf("missing image ref must fail closed\nstdout:\n%s", stdout)
		}
		calls, _ := os.ReadFile(logPath)
		if strings.Contains(string(calls), "create namespace") {
			t.Fatalf("the checker must validate inputs before creating anything:\n%s", calls)
		}
	})

	// HTTP-level matrix (T-R11-01): the client pod shell really executes and
	// its wget calls are intercepted, so a connect failure or an HTTP 200
	// with a wrong body must fail the probe for every probed target.
	t.Run("http-level pass covers podip serviceip and dns token bodies", func(t *testing.T) {
		result := runNetProbeHTTP(t, "", "")
		if result.exitCode != 0 {
			t.Fatalf("healthy run must pass\n%s", result.output)
		}
		requests := readSmokeFile(t, filepath.Join(result.stateDir, "requested.log"))
		for _, want := range []string{
			"http://" + fakeBackendPodIP + ":3000/\n",
			"http://" + fakeBackendSvcIP + ":3000/\n",
			"http://ani-net-smoke-svc.",
		} {
			if !strings.Contains(requests, want) {
				t.Fatalf("client did not request %s; requests:\n%s", want, requests)
			}
		}
		summary := readSmokeFile(t, filepath.Join(result.runDir, "summary.txt"))
		if strings.Count(summary, "result=pass at=") != 2 {
			t.Fatalf("expected two cross-node pair pass lines:\n%s", summary)
		}
	})

	for _, test := range []struct {
		name        string
		failTarget  string
		wrongTarget string
	}{
		{name: "http pod-ip-request-fails", failTarget: "pod-ip"},
		{name: "http service-ip-request-fails", failTarget: "service-ip"},
		{name: "http dns-request-fails", failTarget: "dns"},
		{name: "http pod-ip-response-is-wrong", wrongTarget: "pod-ip"},
		{name: "http service-ip-response-is-wrong", wrongTarget: "service-ip"},
		{name: "http dns-response-is-wrong", wrongTarget: "dns"},
	} {
		t.Run(test.name, func(t *testing.T) {
			result := runNetProbeHTTP(t, test.failTarget, test.wrongTarget)
			if result.exitCode == 0 {
				t.Fatalf("checker unexpectedly passed\n%s", result.output)
			}
			summary := readSmokeFile(t, filepath.Join(result.runDir, "summary.txt"))
			if strings.Contains(summary, "result=pass") {
				t.Fatalf("checker marked failure as pass; summary:\n%s", summary)
			}
			// Failure keeps the scene: the run namespace is never deleted.
			if strings.Contains(result.output, "delete namespace") {
				t.Fatalf("failed run must not delete its namespace:\n%s", result.output)
			}
		})
	}
}

// TestKubeOVNDoesNotUseKCNEnvoy (T-R11-03 + T-R11-04): the verify.sh stack
// branch runs the generic network smoke for both stacks and the Envoy batch
// only on the kcn path; kubeovn trajectories contain no Envoy/kcn query or
// write; an unknown stack is rejected.
func TestKubeOVNDoesNotUseKCNEnvoy(t *testing.T) {
	// r11StubProbe returns a probe stub that records its invocation.
	r11StubProbe := func(t *testing.T, dir, name, marker string) string {
		t.Helper()
		path := filepath.Join(dir, name)
		script := "#!/usr/bin/env bash\nprintf '%s\\n' \"$*\" >> \"$STUB_LOG\"\necho " + marker + "\n"
		if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
			t.Fatalf("write stub: %v", err)
		}
		return path
	}

	setup := func(t *testing.T) (binDir, logPath, stubLog, envoyStub, netStub, netFail, stateDir string) {
		binDir, logPath, stateDir = r11FakeKubectl(t)
		work := t.TempDir()
		stubLog = filepath.Join(work, "stub-calls.log")
		envoyStub = r11StubProbe(t, work, "envoy-probe", "ENVOY-STUB-OK")
		netStub = r11StubProbe(t, work, "net-probe", "NET-STUB-OK")
		netFail = filepath.Join(work, "net-probe-fail")
		if err := os.WriteFile(netFail, []byte("#!/usr/bin/env bash\nprintf '%s\\n' \"$*\" >> \"$STUB_LOG\"\nexit 7\n"), 0o700); err != nil {
			t.Fatalf("write failing stub: %v", err)
		}
		return
	}

	runBranch := func(t *testing.T, binDir, stack, envoyStub, netStub, logDir, stateDir string) (string, string, int) {
		t.Helper()
		verifyAbs, err := filepath.Abs(r11VerifyRel)
		if err != nil {
			t.Fatalf("abs: %v", err)
		}
		script := `set +e
export PATH="$BIN:$PATH"
export ANI_VERIFY_LIB_ONLY=1
export KUBECTL_LOG="$KLOG"
export STUB_LOG="$STUBLOG"
source "$VERIFY"
export KUBECONFIG_FILE="$OUT/kubeconfig"
rc=0
ani_run_network_checks "$STACK" "$ENVOY" "$NET" "$OUT" || rc=$?
echo "BRANCH_RC=$rc"
echo "NET=${ANI_NETWORK_RESULT:-unset}"
echo "ENVOY=${ANI_ENVOY_RESULT:-unset}"
`
		stdout, stderr, code := r02RunBash(t, script, map[string]string{
			"PATH":    binDir + ":" + os.Getenv("PATH"),
			"KLOG":    filepath.Join(t.TempDir(), "klog"),
			"STUBLOG": filepath.Join(t.TempDir(), "stublog"),
			"VERIFY":  verifyAbs,
			"OUT":     logDir,
			"STACK":   stack,
			"FAKE_STATE": stateDir,
			"ENVOY":   envoyStub,
			"NET":     netStub,
		})
		// The branch rc is echoed by the harness; trust that, not the wrapper.
		if idx := strings.Index(stdout, "BRANCH_RC="); idx >= 0 {
			code = int(stdout[idx+len("BRANCH_RC=")] - '0')
		}
		return stdout, stderr, code
	}

	t.Run("verify facts driven full trajectory is envoy-free for kubeovn", func(t *testing.T) {
		binDir, _, _, envoyStub, netStub, _, stateDir := setup(t)
		outDir := t.TempDir()
		verifyAbs, err := filepath.Abs(r11VerifyRel)
		if err != nil {
			t.Fatalf("abs: %v", err)
		}
		klog := filepath.Join(outDir, "klog")
		stublog := filepath.Join(outDir, "stublog")
		// The fake kubectl only creates its log on the first call; a branch
		// that legitimately needs no kubectl (healthy kubeovn, or a network
		// failure that stops early) leaves it absent — pre-create it.
		if err := os.WriteFile(klog, nil, 0o600); err != nil {
			t.Fatalf("pre-create klog: %v", err)
		}
		script := `set +e
export PATH="$BIN:$PATH"
export ANI_VERIFY_LIB_ONLY=1
export KUBECTL_LOG="$KLOG"
export STUB_LOG="$STUBLOG"
source "$VERIFY"
export KUBECONFIG_FILE="$OUT/kubeconfig"
rc=0
ani_run_network_checks kubeovn "$ENVOY" "$NET" "$OUT" || rc=$?
echo "BRANCH_RC=$rc"
echo "NET=${ANI_NETWORK_RESULT:-unset}"
echo "ENVOY=${ANI_ENVOY_RESULT:-unset}"
`
		stdout, stderr, code := r02RunBash(t, script, map[string]string{
			"PATH":  binDir + ":" + os.Getenv("PATH"),
			"KLOG":  klog,
			"STUBLOG": stublog,
			"VERIFY": verifyAbs,
			"OUT":   outDir,
			"ENVOY": envoyStub,
			"NET":   netStub,
			"FAKE_STATE": stateDir,
		})
		if code != 0 {
			t.Fatalf("kubeovn branch failed: %s\n%s", stdout, stderr)
		}
		if !strings.Contains(stdout, "NET=pass") {
			t.Fatalf("network result must be pass:\n%s", stdout)
		}
		if !strings.Contains(stdout, "ENVOY=not_run") {
			t.Fatalf("kubeovn must not report an Envoy result:\n%s", stdout)
		}
		calls, err := os.ReadFile(klog)
		if err != nil {
			t.Fatalf("read kubectl log: %v", err)
		}
		for _, forbidden := range []string{"envoy-gateway-system", "envoy-gateway", "ani-installer-smoke", "ani-smoke-backend", "gateway", "kcn"} {
			if strings.Contains(string(calls), forbidden) {
				t.Fatalf("kubeovn trajectory must not query or write %q:\n%s", forbidden, calls)
			}
		}
		// The probes record their outcome in per-probe stdout files, so the
		// presence/absence of those files is the evidence separation check.
		netEvidence, err := os.ReadFile(filepath.Join(outDir, "network-probe.stdout"))
		if err != nil || !strings.Contains(string(netEvidence), "NET-STUB-OK") {
			t.Fatalf("the generic network probe must run on the kubeovn stack: %v", err)
		}
		if _, err := os.Stat(filepath.Join(outDir, "envoy-probe.stdout")); !os.IsNotExist(err) {
			t.Fatalf("the envoy probe must not run on the kubeovn stack (evidence file exists)")
		}
	})

	// T-R11-04: the kcn branch produces independent network and Envoy
	// evidence; a network failure stops before any Envoy query.
	t.Run("kcn branch records separate evidence and stops on network failure", func(t *testing.T) {
		binDir, _, _, envoyStub, netStub, netFail, stateDir := setup(t)
		outDir := t.TempDir()
		verifyAbs, err := filepath.Abs(r11VerifyRel)
		if err != nil {
			t.Fatalf("abs: %v", err)
		}
		klog := filepath.Join(outDir, "klog")
		stublog := filepath.Join(outDir, "stublog")
		// The fake kubectl only creates its log on the first call; a branch
		// that legitimately needs no kubectl (healthy kubeovn, or a network
		// failure that stops early) leaves it absent — pre-create it.
		if err := os.WriteFile(klog, nil, 0o600); err != nil {
			t.Fatalf("pre-create klog: %v", err)
		}
		script := `set +e
export PATH="$BIN:$PATH"
export ANI_VERIFY_LIB_ONLY=1
export KUBECTL_LOG="$KLOG"
export STUB_LOG="$STUBLOG"
source "$VERIFY"
export KUBECONFIG_FILE="$OUT/kubeconfig"
rc=0
ani_run_network_checks kcn "$ENVOY" "$NETFAIL" "$OUT" || rc=$?
echo "BRANCH_RC=$rc NET=${ANI_NETWORK_RESULT:-unset} ENVOY=${ANI_ENVOY_RESULT:-unset}"
`
		stdout, _, _ := r02RunBash(t, script, map[string]string{
			"PATH":    binDir + ":" + os.Getenv("PATH"),
			"KLOG":    klog,
			"STUBLOG": stublog,
			"VERIFY":  verifyAbs,
			"OUT":     outDir,
			"ENVOY":   envoyStub,
			"NETFAIL": netFail,
			"FAKE_STATE": stateDir,
		})
		code := 0
		if idx := strings.Index(stdout, "BRANCH_RC="); idx >= 0 {
			code = int(stdout[idx+len("BRANCH_RC=")] - '0')
		} else {
			t.Fatalf("branch did not report its rc:\n%s", stdout)
		}
		if code == 0 {
			t.Fatalf("a network failure must stop the kcn branch:\n%s", stdout)
		}
		if !strings.Contains(stdout, "NET=fail ENVOY=not_run") {
			t.Fatalf("network failure must be recorded and Envoy must stay not_run:\n%s", stdout)
		}
		calls, err := os.ReadFile(klog)
		if err != nil {
			t.Fatalf("read kubectl log: %v", err)
		}
		if strings.Contains(string(calls), "envoy-gateway-system") {
			t.Fatalf("no Envoy query may run after a network failure:\n%s", calls)
		}

		// Healthy kcn run: both results recorded, evidence files separate.
		outDir2 := t.TempDir()
		stublog2 := filepath.Join(outDir2, "stublog")
		script = `set +e
export PATH="$BIN:$PATH"
export ANI_VERIFY_LIB_ONLY=1
export KUBECTL_LOG="$KLOG"
export STUB_LOG="$STUBLOG"
source "$VERIFY"
export KUBECONFIG_FILE="$OUT/kubeconfig"
rc=0
ani_run_network_checks kcn "$ENVOY" "$NET" "$OUT" || rc=$?
echo "BRANCH_RC=$rc NET=${ANI_NETWORK_RESULT:-unset} ENVOY=${ANI_ENVOY_RESULT:-unset}"
`
		stdout, _, code = r02RunBash(t, script, map[string]string{
			"PATH":    binDir + ":" + os.Getenv("PATH"),
			"KLOG":    filepath.Join(outDir2, "klog"),
			"STUBLOG": stublog2,
			"VERIFY":  verifyAbs,
			"OUT":     outDir2,
			"ENVOY":   envoyStub,
			"NET":     netStub,
			"FAKE_STATE": stateDir,
		})
		if code != 0 {
			t.Fatalf("healthy kcn branch must pass:\n%s", stdout)
		}
		if !strings.Contains(stdout, "NET=pass ENVOY=pass") {
			t.Fatalf("kcn must record both results independently:\n%s", stdout)
		}
		for _, evidence := range []string{"network-probe.stdout", "envoy-probe.stdout"} {
			if _, err := os.Stat(filepath.Join(outDir2, evidence)); err != nil {
				t.Fatalf("kcn evidence %s missing: %v", evidence, err)
			}
		}
	})

	// T-R11-04b: an unknown stack is rejected, results stay not_run.
	t.Run("unknown stack is rejected", func(t *testing.T) {
		binDir, _, _, envoyStub, netStub, _, stateDir := setup(t)
		outDir := t.TempDir()
		stdout, stderr, code := runBranch(t, binDir, "calico", envoyStub, netStub, outDir, stateDir)
		if code == 0 {
			t.Fatalf("unknown stack must be rejected:\n%s", stdout)
		}
		if !strings.Contains(stderr, "unknown network stack") {
			t.Fatalf("rejection must name the stack check:\n%s", stderr)
		}
		if !strings.Contains(stdout, "NET=not_run") || !strings.Contains(stdout, "ENVOY=not_run") {
			t.Fatalf("an untested stack must never carry a result:\n%s", stdout)
		}
	})
}
