package ani

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"testing"

	kkprojectv1 "github.com/kubesphere/kubekey/api/project/v1"
	"gopkg.in/yaml.v3"
)

const (
	fakeBackendNode  = "node-a"
	fakeBackendPodIP = "10.16.0.9"
	fakeBackendSvcIP = "10.96.153.157"
	fakeEnvoySvcIP   = "10.96.129.239"
)

func TestMain(m *testing.M) {
	switch filepath.Base(os.Args[0]) {
	case "kubectl":
		os.Exit(fakeKubectlMain())
	case "wget":
		os.Exit(fakeWgetMain())
	}
	os.Exit(m.Run())
}

func fakeRandom() string {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		panic(err)
	}
	return hex.EncodeToString(buf)
}

func fakeWgetMain() int {
	args := os.Args[1:]
	url := args[len(args)-1]
	stateDir := os.Getenv("FAKE_STATE_DIR")
	_ = os.MkdirAll(stateDir, 0o700)
	appendRequestedURL(stateDir, url)

	failTarget := os.Getenv("FAKE_WGET_FAIL")
	wrongTarget := os.Getenv("FAKE_WGET_WRONG")
	target := "unknown"
	switch {
	case strings.Contains(url, fakeBackendPodIP+":3000"):
		target = "pod-ip"
	case strings.Contains(url, fakeBackendSvcIP+"/"), strings.Contains(url, fakeBackendSvcIP+":3000"):
		target = "service-ip"
	case strings.Contains(url, "ani-smoke-backend."), strings.Contains(url, "ani-net-smoke-svc."):
		target = "dns"
	case strings.Contains(url, fakeEnvoySvcIP+":9090"):
		target = "envoy"
	}
	if failTarget == target {
		fmt.Fprintf(os.Stderr, "fake wget failure for %s\n", target)
		return 1
	}
	// FAKE_WGET_FLAKY=n makes the first n Envoy data-plane requests fail, the
	// way a node whose kube-proxy has not yet programmed the freshly ready
	// Envoy endpoints rejects the connection, before the cluster settles.
	if flaky := os.Getenv("FAKE_WGET_FLAKY"); flaky != "" && target == "envoy" {
		limit, err := strconv.Atoi(flaky)
		if err != nil {
			fmt.Fprintf(os.Stderr, "fake wget: bad FAKE_WGET_FLAKY %q\n", flaky)
			return 2
		}
		if envoyRequestCount(stateDir, url) <= limit {
			fmt.Fprintf(os.Stderr, "fake wget: envoy route not programmed yet\n")
			return 1
		}
	}
	if wrongTarget == target {
		fmt.Println("ANI-INSTALLER-WRONG")
		return 0
	}
	// FAKE_WGET_STDERR=target answers on stderr instead of stdout: the probe
	// asserts on the response body it received, so a look-alike message that
	// never reached stdout must not satisfy a marker.
	if os.Getenv("FAKE_WGET_STDERR") == target {
		fmt.Fprintln(os.Stderr, "ANI-INSTALLER-OK")
		fmt.Fprintln(os.Stderr, "ANI-ENVOY-OK")
		fmt.Println("ANI-INSTALLER-WRONG")
		return 1
	}
	// The generic network checker (R11) asserts the exact per-run token body;
	// the fake kubectl exported it when it executed the client pod script.
	if token := os.Getenv("FAKE_NET_TOKEN"); token != "" && target != "envoy" {
		fmt.Println(token)
		return 0
	}
	fmt.Println("ANI-INSTALLER-OK")
	return 0
}

func appendRequestedURL(stateDir, url string) {
	if stateDir == "" {
		return
	}
	_ = os.MkdirAll(stateDir, 0o700)
	file, err := os.OpenFile(filepath.Join(stateDir, "requested.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return
	}
	defer file.Close()
	_, _ = fmt.Fprintln(file, url)
}

// envoyRequestCount reads back the request journal and returns how many
// Envoy data-plane requests have been made, including the caller's own.
func envoyRequestCount(stateDir, url string) int {
	content, err := os.ReadFile(filepath.Join(stateDir, "requested.log"))
	if err != nil {
		return 0
	}
	count := 0
	for _, line := range strings.Split(string(content), "\n") {
		if line == url {
			count++
		}
	}
	return count
}
func fakeKubectlMain() int {
	rawArgs := os.Args[1:]
	if len(rawArgs) >= 2 && rawArgs[0] == "--kubeconfig" {
		rawArgs = rawArgs[2:]
	}
	// R12: the checker scripts prepend --request-timeout to every kubectl call.
	if len(rawArgs) >= 1 && strings.HasPrefix(rawArgs[0], "--request-timeout=") {
		rawArgs = rawArgs[1:]
	}
	if len(rawArgs) == 0 {
		return 2
	}
	if rawArgs[0] == "create" {
		if len(rawArgs) >= 2 && rawArgs[1] == "namespace" {
			return 0
		}
		return fakeKubectlCreate()
	}
	if rawArgs[0] == "label" || rawArgs[0] == "delete" {
		return 0
	}
	if rawArgs[0] == "logs" && len(rawArgs) > 1 {
		name := rawArgs[1]
		stateDir := os.Getenv("FAKE_STATE_DIR")
		// Fidelity: a real `kubectl logs` sends the container's stdout to its
		// own stdout and the container's stderr to its own stderr. Merging them
		// here would let the probe's own capture hide a response that never
		// arrived on stdout, which is exactly what the marker gate forbids.
		out, errOut := os.ReadFile(filepath.Join(stateDir, name+".log"))
		errStream, errErr := os.ReadFile(filepath.Join(stateDir, name+".err"))
		// FAKE_LOGS_MOVE_TO_STDERR=<marker>: the container printed that line to
		// its own stderr, not to stdout. A probe that merges the two streams
		// would read the marker as a response it never received.
		if moved := os.Getenv("FAKE_LOGS_MOVE_TO_STDERR"); moved != "" && errOut == nil {
			stdout := string(out)
			if strings.Contains(stdout, moved) {
				stdout = strings.ReplaceAll(stdout, moved+"\n", "")
				fmt.Print(stdout)
				fmt.Fprintln(os.Stderr, moved)
				if errErr == nil && len(errStream) > 0 {
					fmt.Fprint(os.Stderr, string(errStream))
				}
				return 0
			}
		}
		if errOut == nil {
			fmt.Print(string(out))
			if errErr == nil && len(errStream) > 0 {
				fmt.Fprint(os.Stderr, string(errStream))
			}
			return 0
		}
		switch name {
		case "ani-smoke-network-client":
			fmt.Println("ANI-NETWORK-OK")
		case "ani-smoke-client":
			fmt.Println("ANI-INSTALLER-OK")
		}
		return 0
	}
	if rawArgs[0] != "get" && rawArgs[0] != "describe" {
		fmt.Fprintln(os.Stderr, "unsupported fake kubectl command:", strings.Join(rawArgs, " "))
		return 2
	}

	resource := ""
	name := ""
	rest := rawArgs[1:]
	if len(rest) > 0 && !strings.HasPrefix(rest[0], "-") {
		resource = rest[0]
		rest = rest[1:]
		if len(rest) > 0 && !strings.HasPrefix(rest[0], "-") {
			name = rest[0]
			rest = rest[1:]
		}
	}
	for len(rest) > 0 {
		if rest[0] == "-n" && len(rest) > 1 {
			rest = rest[2:]
			continue
		}
		if len(rest) > 1 && (rest[0] == "-o" || rest[0] == "-l") {
			rest = rest[2:]
			continue
		}
		break
	}
	scenario := os.Getenv("FAKE_CLUSTER")

	if rawArgs[0] == "describe" {
		fmt.Printf("fake describe for %s %s\n", resource, name)
		return 0
	}

	switch resource {
	case "events", "namespace", "namespaces":
		fmt.Println("fake events")
		return 0
	case "nodes":
		if strings.Contains(strings.Join(rawArgs, " "), "status.conditions") {
			fmt.Println("True\nTrue\nTrue")
			return 0
		}
		fmt.Println("node-a\nnode-b\nnode-c")
		return 0
	case "pods":
		fmt.Println("fake pods")
		return 0
	case "pod":
		return fakeKubectlPod(name, rawArgs)
	case "service":
		return fakeKubectlService(name, rawArgs, scenario)
	case "gateway":
		fmt.Println("9090")
		return 0
	case "endpointslice":
		return fakeKubectlEndpointSlice(name, rawArgs, scenario)
	}
	fmt.Fprintln(os.Stderr, "unsupported fake kubectl resource:", resource)
	return 2
}

func fakeKubectlPod(name string, rawArgs []string) int {
	command := strings.Join(rawArgs, " ")
	switch name {
	case "ani-smoke-backend":
		switch {
		case strings.Contains(command, "status.conditions"):
			fmt.Println("True")
		case strings.Contains(command, "status.phase"):
			fmt.Println("Running")
		case strings.Contains(command, "status.podIP"):
			fmt.Println(fakeBackendPodIP)
		case strings.Contains(command, "spec.nodeName"):
			fmt.Println(fakeBackendNode)
		case strings.Contains(command, "containers"):
			fmt.Println("192.0.2.10:5000/busybox:1.36")
		}
		return 0
	case "ani-smoke-network-client", "ani-smoke-client":
		if strings.Contains(command, "metadata.uid") {
			fmt.Println("uid-old-" + name)
			return 0
		}
		if strings.Contains(command, "status.phase") {
			fmt.Println("Succeeded")
			return 0
		}
		return 0
	}

	stateDir := os.Getenv("FAKE_STATE_DIR")
	if strings.Contains(command, "metadata.uid") {
		value, err := os.ReadFile(filepath.Join(stateDir, name+".uid"))
		if err != nil {
			return 1
		}
		fmt.Print(string(value))
		return 0
	}
	if strings.Contains(command, "creationTimestamp") {
		value, err := os.ReadFile(filepath.Join(stateDir, name+".created"))
		if err != nil {
			return 1
		}
		fmt.Print(string(value))
		return 0
	}
	if strings.Contains(command, "spec.nodeName") {
		value, err := os.ReadFile(filepath.Join(stateDir, name+".node"))
		if err != nil {
			return 1
		}
		fmt.Print(string(value))
		return 0
	}
	if strings.Contains(command, "status.podIP") {
		value, err := os.ReadFile(filepath.Join(stateDir, name+".podip"))
		if err != nil {
			return 1
		}
		fmt.Print(string(value))
		return 0
	}
	if strings.Contains(command, "containers") {
		value, err := os.ReadFile(filepath.Join(stateDir, name+".image"))
		if err != nil {
			return 1
		}
		fmt.Print(string(value))
		return 0
	}
	if strings.Contains(command, "status.phase") {
		value, err := os.ReadFile(filepath.Join(stateDir, name+".phase"))
		if err != nil {
			return 1
		}
		fmt.Print(string(value))
		return 0
	}
	if strings.Contains(command, "terminated.exitCode") {
		value, err := os.ReadFile(filepath.Join(stateDir, name+".exit"))
		if err != nil {
			return 1
		}
		fmt.Print(string(value))
		return 0
	}
	fmt.Println("fake pod")
	return 0
}

func fakeKubectlService(name string, rawArgs []string, scenario string) int {
	command := strings.Join(rawArgs, " ")
	if name == "" {
		if strings.Contains(command, " -o name") {
			if scenario == "service-zero" {
				fmt.Println("service/ani-smoke-backend")
				return 0
			}
			fmt.Println("service/ani-smoke-backend")
			if scenario == "service-multiple" {
				fmt.Println("service/ani-smoke-other")
			}
			fmt.Println("service/ani-smoke")
			return 0
		}
		fmt.Println("fake services")
		return 0
	}

	if strings.Contains(command, "owning-gateway-name}") {
		if name == "ani-smoke" || (scenario == "service-multiple" && name == "ani-smoke-other") {
			fmt.Println("ani-smoke")
			return 0
		}
		return 0
	}
	if strings.Contains(command, "owning-gateway-namespace") {
		if name == "ani-smoke" || (scenario == "service-multiple" && name == "ani-smoke-other") {
			fmt.Println("ani-installer-smoke")
			return 0
		}
		return 0
	}
	if strings.Contains(command, "managed-by") {
		if name == "ani-smoke" || (scenario == "service-multiple" && name == "ani-smoke-other") {
			fmt.Println("envoy-gateway")
		}
		return 0
	}
	if strings.Contains(command, "app\\.kubernetes\\.io/name") {
		if name == "ani-smoke" || (scenario == "service-multiple" && name == "ani-smoke-other") {
			fmt.Println("envoy")
		}
		return 0
	}
	if strings.Contains(command, "spec.clusterIP") {
		switch name {
		case "ani-net-smoke-svc":
			fmt.Println(fakeBackendSvcIP)
		case "ani-smoke-backend":
			fmt.Println(fakeBackendSvcIP)
		case "ani-smoke-other":
			fmt.Println("10.96.129.240")
		default:
			fmt.Println(fakeEnvoySvcIP)
		}
		return 0
	}
	if strings.Contains(command, "spec.ports") {
		if scenario == "service-no-9090" {
			fmt.Println("80")
			return 0
		}
		fmt.Println("9090")
		return 0
	}
	fmt.Println("fake service")
	return 0
}

func fakeKubectlEndpointSlice(name string, rawArgs []string, scenario string) int {
	command := strings.Join(rawArgs, " ")
	if name == "" {
		if strings.Contains(command, " -o name") {
			if scenario == "service-zero" || scenario == "service-multiple" {
				if scenario == "service-multiple" {
					fmt.Println("endpointslice.discovery.k8s.io/ani-smoke-envoy-1")
					fmt.Println("endpointslice.discovery.k8s.io/ani-smoke-envoy-2")
				}
				return 0
			}
			fmt.Println("endpointslice.discovery.k8s.io/ani-smoke-envoy")
			return 0
		}
		fmt.Println("fake endpointslices")
		return 0
	}
	if strings.Contains(command, "targetRef.kind") {
		fmt.Println("Pod")
		return 0
	}
	if strings.Contains(command, "conditions.ready") {
		fmt.Println("true")
		return 0
	}
	fmt.Println("fake endpointslice")
	return 0
}

func fakeKubectlCreate() int {
	manifestBytes := new(bytes.Buffer)
	if _, err := manifestBytes.ReadFrom(os.Stdin); err != nil {
		return 1
	}
	manifest := manifestBytes.String()
	stateDir := os.Getenv("FAKE_STATE_DIR")
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return 1
	}

	// R11: the generic network checker creates a run-scoped Service and
	// server/client Pods. The Service has no shell args block; the server
	// must report Running without actually serving; the client shell is
	// really executed so its wget calls go through the fake wget.
	if strings.Contains(manifest, "kind: Service") {
		name := fakeManifestName(manifest, "ani-net-smoke-svc")
		fmt.Println(name)
		return 0
	}
	if strings.Contains(manifest, "generateName: ani-net-smoke-server-") {
		return fakeRecordCreatedPod(manifest, stateDir, "ani-net-smoke-server-", "node-a", false)
	}
	if strings.Contains(manifest, "generateName: ani-net-smoke-client-") {
		return fakeRecordCreatedPod(manifest, stateDir, "ani-net-smoke-client-", "node-b", true)
	}

	shellArg, err := fakeExtractShellArg(manifest)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}

	generateName := "ani-smoke-client-"
	if strings.Contains(manifest, "name: network-client") {
		generateName = "ani-smoke-network-client-"
	}
	name := generateName + fakeRandom()
	uid := "uid-" + fakeRandom()
	node := "node-b"
	if strings.Contains(manifest, "nodeName: node-c") {
		node = "node-c"
	}
	image := "192.0.2.10:5000/busybox:1.36"
	created := "2026-09-15T00:00:00Z"

	cmd := exec.Command("/bin/sh", "-ec", shellArg)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	runErr := cmd.Run()
	phase := "Succeeded"
	exitCode := "0"
	if runErr != nil {
		phase = "Failed"
		exitCode = "1"
	}

	files := map[string]string{
		name + ".uid":      uid,
		name + ".created":  created,
		name + ".node":     node,
		name + ".image":    image,
		name + ".phase":    phase,
		name + ".exit":     exitCode,
		name + ".log":      stdout.String(),
		name + ".err":      stderr.String(),
		name + ".manifest": manifest,
	}
	for filename, value := range files {
		if err := os.WriteFile(filepath.Join(stateDir, filename), []byte(value), 0o600); err != nil {
			return 1
		}
	}
	fmt.Println(name)
	return 0
}

// fakeManifestName extracts metadata.name from a created manifest.
func fakeManifestName(manifest, fallback string) string {
	for _, line := range strings.Split(manifest, "\n") {
		if value, ok := strings.CutPrefix(strings.TrimSpace(line), "name: "); ok {
			return strings.TrimSpace(value)
		}
	}
	return fallback
}

// fakeRecordCreatedPod records a network-checker pod. For the server the
// shell arg is NOT executed (it would run a real httpd); the pod reports
// Running with a fixed PodIP. For the client the shell arg IS executed with
// the per-run token exported, so its wget calls run through the fake wget.
func fakeRecordCreatedPod(manifest, stateDir, generateName, node string, serve bool) int {
	name := generateName + fakeRandom()
	uid := "uid-" + fakeRandom()
	created := "2026-09-24T00:00:00Z"
	image := "192.0.2.10:5000/busybox:1.36"
	phase := "Succeeded"
	exitCode := "0"
	output := ""

	shellArg, argErr := fakeExtractShellArg(manifest)
	if serve && argErr == nil {
		token := fakeNetToken(manifest)
		if token == "" {
			fmt.Fprintln(os.Stderr, "fake kubectl: client manifest carries no run token")
			return 1
		}
		cmd := exec.Command("/bin/sh", "-ec", shellArg)
		cmd.Env = append(os.Environ(), "FAKE_NET_TOKEN="+token)
		var out bytes.Buffer
		cmd.Stdout = &out
		// The net client's stderr is kept out of its stdout record for the same
		// reason as the Envoy client's.
		cmd.Stderr = io.Discard
		runErr := cmd.Run()
		output = out.String()
		if runErr != nil {
			phase = "Failed"
			exitCode = "1"
		}
	} else if !serve {
		// Server: extract the shell arg only to validate the manifest shape;
		// report Running with the shared fake PodIP.
		phase = "Running"
	}

	files := map[string]string{
		name + ".uid":      uid,
		name + ".created":  created,
		name + ".node":     node,
		name + ".image":    image,
		name + ".phase":    phase,
		name + ".exit":     exitCode,
		name + ".log":      output,
		name + ".manifest": manifest,
	}
	if !serve {
		files[name+".podip"] = fakeBackendPodIP
	}
	for filename, value := range files {
		if err := os.WriteFile(filepath.Join(stateDir, filename), []byte(value), 0o600); err != nil {
			return 1
		}
	}
	fmt.Println(name)
	return 0
}

// fakeNetToken extracts the per-run body token from a checker manifest.
func fakeNetToken(manifest string) string {
	re := regexp.MustCompile("ANI-NET-[A-Za-z0-9-]+-BODY")
	if match := re.FindString(manifest); match != "" {
		return match
	}
	return ""
}

func fakeExtractShellArg(manifest string) (string, error) {
	lines := strings.Split(manifest, "\n")
	for index, line := range lines {
		if strings.TrimSpace(line) != "args:" {
			continue
		}
		if index+1 >= len(lines) || strings.TrimSpace(lines[index+1]) != "- |" {
			return "", errors.New("fake kubectl could not find args block")
		}
		contentIndex := index + 2
		if contentIndex >= len(lines) || strings.TrimSpace(lines[contentIndex]) == "" {
			return "", errors.New("fake kubectl found an empty args block")
		}
		contentLine := lines[contentIndex]
		indent := len(contentLine) - len(strings.TrimLeft(contentLine, " "))
		builder := strings.Builder{}
		for _, current := range lines[contentIndex:] {
			if strings.TrimSpace(current) == "" {
				builder.WriteString("\n")
				continue
			}
			currentIndent := len(current) - len(strings.TrimLeft(current, " "))
			if currentIndent < indent {
				break
			}
			builder.WriteString(current[indent:])
			builder.WriteString("\n")
		}
		return builder.String(), nil
	}
	return "", errors.New("fake kubectl could not find args")
}

type smokeProbeResult struct {
	exitCode int
	output   string
	stateDir string
	runDir   string
}

func runSmokeProbe(t *testing.T, scenario, failTarget, wrongTarget string, extraEnv ...string) smokeProbeResult {
	return runSmokeProbeScript(t, filepath.Join("..", "..", "builtin", "core", "roles", "ani", "smoke", "templates", "probe.sh"), "run-", scenario, failTarget, wrongTarget, extraEnv...)
}

// runNetProbeHTTP drives the R11 generic network checker through the same
// HTTP-level fake (the client pod shell really executes, its wget calls are
// intercepted and recorded).
func runNetProbeHTTP(t *testing.T, failTarget, wrongTarget string) smokeProbeResult {
	return runSmokeProbeScript(t, filepath.Join("..", "..", "builtin", "core", "roles", "ani", "smoke", "templates", "network-probe.sh"), "net-run-", "success", failTarget, wrongTarget)
}

func runSmokeProbeScript(t *testing.T, scriptRel, outPrefix, scenario, failTarget, wrongTarget string, extraEnv ...string) smokeProbeResult {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("probe integration test executes Linux shell commands")
	}
	probePath, err := filepath.Abs(scriptRel)
	if err != nil {
		t.Fatalf("resolve probe path: %v", err)
	}
	tmp := t.TempDir()
	binDir := filepath.Join(tmp, "bin")
	stateDir := filepath.Join(tmp, "state")
	outputBase := filepath.Join(tmp, "smoke-output")
	kubeconfig := filepath.Join(tmp, "admin.conf")
	for _, dir := range []string{binDir, stateDir, outputBase} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatalf("create temp dir: %v", err)
		}
	}
	if err := os.WriteFile(kubeconfig, []byte("fake kubeconfig\n"), 0o600); err != nil {
		t.Fatalf("write kubeconfig: %v", err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatalf("resolve test executable: %v", err)
	}
	if err := os.Symlink(executable, filepath.Join(binDir, "kubectl")); err != nil {
		t.Fatalf("create fake kubectl: %v", err)
	}
	if err := os.Symlink(executable, filepath.Join(binDir, "wget")); err != nil {
		t.Fatalf("create fake wget: %v", err)
	}

	cmd := exec.Command("bash", probePath)
	cmd.Env = append(os.Environ(),
		"PATH="+binDir+":"+os.Getenv("PATH"),
		"KUBECONFIG_FILE="+kubeconfig,
		"ANI_SMOKE_OUTPUT="+outputBase,
		"ANI_NETSMOKE_IMAGE=192.0.2.10:5000/busybox:1.36",
		"FAKE_STATE_DIR="+stateDir,
		"FAKE_CLUSTER="+scenario,
		"FAKE_WGET_FAIL="+failTarget,
		"FAKE_WGET_WRONG="+wrongTarget,
		// The retry window is a production timing choice; the harness asserts
		// behaviour, not wall-clock.
		"ANI_SMOKE_ENVOY_RETRY_SECONDS=0",
	)
	cmd.Env = append(cmd.Env, extraEnv...)
	output, err := cmd.CombinedOutput()
	result := smokeProbeResult{
		output:   string(output),
		stateDir: stateDir,
	}
	if err != nil {
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			t.Fatalf("run probe: %v\n%s", err, result.output)
		}
		result.exitCode = exitErr.ExitCode()
	} else {
		result.exitCode = 0
	}
	entries, _ := os.ReadDir(outputBase)
	for _, entry := range entries {
		if entry.IsDir() && strings.HasPrefix(entry.Name(), outPrefix) {
			result.runDir = filepath.Join(outputBase, entry.Name())
		}
	}
	return result
}

func readSmokeFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

// TestSmokeProbeUsesFreshClientsAndAccurateEnvoyService covers the kcn-only
// Envoy probe (R11 split): it must hit ONLY the Envoy Service of the
// ani-smoke Gateway and use a fresh client.
func TestSmokeProbeUsesFreshClientsAndAccurateEnvoyService(t *testing.T) {
	result := runSmokeProbe(t, "success", "", "")
	if result.exitCode != 0 {
		t.Fatalf("probe exit=%d\n%s", result.exitCode, result.output)
	}
	requests := readSmokeFile(t, filepath.Join(result.stateDir, "requested.log"))
	if !strings.Contains(requests, "http://"+fakeEnvoySvcIP+":9090/\n") {
		t.Fatalf("probe did not request the Envoy Service; requests:\n%s", requests)
	}
	// The generic Pod/Service/DNS probes moved to network-probe.sh (R11); the
	// Envoy probe must not grow them back.
	for _, stale := range []string{
		"http://" + fakeBackendPodIP + ":3000/\n",
		"http://" + fakeBackendSvcIP + "/\n",
		"http://ani-smoke-backend.ani-installer-smoke.svc.cluster.local/\n",
	} {
		if strings.Contains(requests, stale) {
			t.Fatalf("Envoy probe still performs a generic network request %q; requests:\n%s", stale, requests)
		}
	}

	summary := readSmokeFile(t, filepath.Join(result.runDir, "summary.txt"))
	if !strings.Contains(summary, "envoy_service=ani-smoke\n") {
		t.Fatalf("probe did not select the Envoy Service accurately; summary:\n%s", summary)
	}
	envoyUID := smokeSummaryValue(t, summary, "envoy_client_uid")
	if envoyUID == "" || envoyUID == smokeSummaryValue(t, summary, "old_client_uid") {
		t.Fatalf("probe reused an old client UID; summary:\n%s", summary)
	}
	if strings.Contains(summary, "network_result=") {
		t.Fatalf("the Envoy probe must not report a network result any more; summary:\n%s", summary)
	}
	envoyLog := readSmokeFile(t, filepath.Join(result.runDir, "envoy-client.log"))
	for _, marker := range []string{"ANI-INSTALLER-OK", "ANI-ENVOY-OK"} {
		if !strings.Contains(envoyLog, marker) {
			t.Fatalf("Envoy client log missing %q: %s", marker, envoyLog)
		}
	}
}

func smokeSummaryValue(t *testing.T, summary, key string) string {
	t.Helper()
	for _, line := range strings.Split(summary, "\n") {
		if value, ok := strings.CutPrefix(line, key+"="); ok {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

// TestSmokeProbeFailsOnRequestOrResponseFailure covers the kcn Envoy probe;
// the generic network failure matrix moved to TestNetworkProbe (R11 split).
func TestSmokeProbeFailsOnNetworkRequestOrResponseFailure(t *testing.T) {
	tests := []struct {
		name        string
		failTarget  string
		wrongTarget string
	}{
		{name: "envoy-request-fails", failTarget: "envoy"},
		{name: "envoy-response-is-wrong", wrongTarget: "envoy"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := runSmokeProbe(t, "success", test.failTarget, test.wrongTarget)
			if result.exitCode == 0 {
				t.Fatalf("probe unexpectedly passed\n%s", result.output)
			}
			summary := readSmokeFile(t, filepath.Join(result.runDir, "summary.txt"))
			if strings.Contains(summary, "result=pass") {
				t.Fatalf("probe marked failure as pass; summary:\n%s", summary)
			}
		})
	}
}

func TestSmokeProbeRejectsInaccurateEnvoyServiceSelection(t *testing.T) {
	tests := []struct {
		scenario string
		message  string
	}{
		{scenario: "service-zero", message: "got 0: none"},
		{scenario: "service-multiple", message: "got 2: ani-smoke-other ani-smoke"},
		{scenario: "service-no-9090", message: "expected exactly one port 9090"},
	}
	for _, test := range tests {
		t.Run(test.scenario, func(t *testing.T) {
			result := runSmokeProbe(t, test.scenario, "", "")
			if result.exitCode == 0 {
				t.Fatalf("probe unexpectedly selected an inaccurate Service\n%s", result.output)
			}
			if !strings.Contains(result.output, test.message) {
				t.Fatalf("probe failure missing %q; output:\n%s", test.message, result.output)
			}
		})
	}
}

func TestSmokeProbeDoesNotTrustOldSucceededClient(t *testing.T) {
	result := runSmokeProbe(t, "success", "envoy", "")
	if result.exitCode == 0 {
		t.Fatalf("probe unexpectedly passed with a failing new client\n%s", result.output)
	}
	summary := readSmokeFile(t, filepath.Join(result.runDir, "summary.txt"))
	oldUID := smokeSummaryValue(t, summary, "old_client_uid")
	newUID := smokeSummaryValue(t, summary, "envoy_client_uid")
	if oldUID != "uid-old-ani-smoke-client" || newUID == "" || newUID == oldUID {
		t.Fatalf("probe did not distinguish old and new clients; summary:\n%s", summary)
	}
}

func TestSmokeProbeRetriesAProgrammedLaterEnvoyPath(t *testing.T) {
	// Live 2026-09-26: the cluster was healthy and the Gateway Programmed, but
	// the single Envoy request raced the per-node service programming and the
	// whole install aborted. A probe may wait for the path, but every attempt
	// must be a real fresh client and the wait must stay bounded.
	result := runSmokeProbe(t, "success", "", "", "FAKE_WGET_FLAKY=2", "ANI_SMOKE_ENVOY_RETRY_SECONDS=0")
	if result.exitCode != 0 {
		t.Fatalf("probe exit=%d\n%s", result.exitCode, result.output)
	}
	summary := readSmokeFile(t, filepath.Join(result.runDir, "summary.txt"))
	if got := smokeSummaryValue(t, summary, "envoy_attempts"); got != "3" {
		t.Fatalf("probe recorded envoy_attempts=%q, want 3; summary:\n%s", got, summary)
	}
	requests := readSmokeFile(t, filepath.Join(result.stateDir, "requested.log"))
	if got := strings.Count(requests, "http://"+fakeEnvoySvcIP+":9090/\n"); got != 3 {
		t.Fatalf("probe made %d Envoy requests, want 3; requests:\n%s", got, requests)
	}
	// Each attempt is its own client Pod, and its own log is kept as evidence.
	uids := map[string]bool{}
	for _, attempt := range []string{"1", "2", "3"} {
		value := smokeSummaryValue(t, summary, "envoy_attempt_"+attempt+"_uid")
		if value == "" || uids[value] {
			t.Fatalf("attempt %s reused or lost its client UID (uid=%q); summary:\n%s", attempt, value, summary)
		}
		uids[value] = true
		if _, err := os.Stat(filepath.Join(result.runDir, "envoy-client-attempt-"+attempt+".log")); err != nil {
			t.Fatalf("attempt %s kept no log: %v", attempt, err)
		}
	}
	if body := readSmokeFile(t, filepath.Join(result.runDir, "envoy-client.log")); !strings.Contains(body, "ANI-ENVOY-OK") {
		t.Fatalf("final client log lost the markers:\n%s", body)
	}
}

func TestSmokeProbeEnvoyRetriesAreBounded(t *testing.T) {
	// The retry window may not become an open-ended pass: a data plane that
	// never serves the marker must still fail the probe, after exactly the
	// declared number of attempts.
	result := runSmokeProbe(t, "success", "", "", "FAKE_WGET_FLAKY=99", "ANI_SMOKE_ENVOY_RETRY_SECONDS=0")
	if result.exitCode == 0 {
		t.Fatal("probe passed a data plane that never answered")
	}
	requests := readSmokeFile(t, filepath.Join(result.stateDir, "requested.log"))
	if got := strings.Count(requests, "http://"+fakeEnvoySvcIP+":9090/\n"); got != 6 {
		t.Fatalf("probe made %d Envoy requests, want the 6-attempt bound; requests:\n%s", got, requests)
	}
	for _, want := range []string{"attempt 6/6", "did not finish successfully in 6"} {
		if !strings.Contains(result.output, want) {
			t.Fatalf("probe failure missing %q; output:\n%s", want, result.output)
		}
	}
}

// The retry window must be bounded by wall-clock too: an attempt count high
// enough to outlast the install step may not turn a broken path into a slow
// success.
func TestSmokeProbeEnvoyRetryWindowIsBoundedByTime(t *testing.T) {
	result := runSmokeProbe(t, "success", "", "", "FAKE_WGET_FLAKY=9999",
		"ANI_SMOKE_ENVOY_ATTEMPTS=1000", "ANI_SMOKE_ENVOY_RETRY_SECONDS=0", "ANI_SMOKE_ENVOY_TOTAL_SECONDS=1")
	if result.exitCode == 0 {
		t.Fatal("an unbounded attempt count with a dead data plane must not pass")
	}
	requests := readSmokeFile(t, filepath.Join(result.stateDir, "requested.log"))
	count := strings.Count(requests, "http://"+fakeEnvoySvcIP+":9090/\n")
	if count == 0 || count >= 1000 {
		t.Fatalf("the deadline never bit: %d Envoy requests under a 1s window", count)
	}
	summary := readSmokeFile(t, filepath.Join(result.runDir, "summary.txt"))
	if !strings.Contains(summary, "envoy_window_seconds=") {
		t.Fatalf("the summary does not record the window it used; summary:\n%s", summary)
	}
	if !strings.Contains(result.output, "window closed") {
		t.Fatalf("the failure must name the deadline; output:\n%s", result.output)
	}
}

// A marker that only appears on stderr is not a response body.
func TestSmokeProbeEnvoyMarkersRequireStdout(t *testing.T) {
	// The client Pod is Succeeded with exit 0, and its log stream carries both
	// marker strings — on stderr, with nothing on stdout. Only a probe that
	// asserts on the response it actually received may refuse this.
	result := runSmokeProbe(t, "success", "", "",
		"FAKE_LOGS_MOVE_TO_STDERR=ANI-ENVOY-OK", "ANI_SMOKE_ENVOY_ATTEMPTS=2")
	if result.exitCode == 0 {
		t.Fatal("stderr chatter that mimics the markers satisfied the probe")
	}
	if !strings.Contains(result.output, "attempt 2/2") {
		t.Fatalf("every attempt must be refused; output:\n%s", result.output)
	}
	for _, attempt := range []string{"1", "2"} {
		body := readSmokeFile(t, filepath.Join(result.runDir, "envoy-client-attempt-"+attempt+".log"))
		if !strings.Contains(body, "--- stdout ---") || !strings.Contains(body, "--- stderr ---") {
			t.Fatalf("attempt %s log does not keep stdout and stderr apart:\n%s", attempt, body)
		}
		if !strings.Contains(body, "ANI-ENVOY-OK") {
			t.Fatalf("attempt %s log lost the stderr line that must not count:\n%s", attempt, body)
		}
		if strings.Contains(strings.SplitAfter(body, "--- stderr ---")[0], "ANI-ENVOY-OK") {
			t.Fatalf("attempt %s shows the marker on stdout, so the case does not test what it claims:\n%s", attempt, body)
		}
	}
}

func TestSmokeProbePackagingWiring(t *testing.T) {
	root := filepath.Join("..", "..")
	role := readSmokeFile(t, filepath.Join(root, "builtin", "core", "roles", "ani", "smoke", "tasks", "main.yaml"))
	verify := readSmokeFile(t, filepath.Join(root, "scripts", "verify.sh"))
	build := readSmokeFile(t, filepath.Join(root, "scripts", "build-code.sh"))

	for _, want := range []string{
		"src: probe.sh",
		"KUBECONFIG_FILE=/etc/kubernetes/admin.conf ANI_SMOKE_OUTPUT=/etc/kubernetes/ani/smoke-output bash /etc/kubernetes/ani/smoke-probe.sh",
	} {
		if !strings.Contains(role, want) {
			t.Fatalf("smoke role missing wiring %q", want)
		}
	}
	for _, want := range []string{
		`PROBE="$ROOT/probe.sh"`,
		`NETPROBE="$ROOT/network-probe.sh"`,
		// R11: both stacks run the generic network smoke; only the kcn stack
		// adds the Envoy probe — the split lives in ani_run_network_checks.
		`ani_run_network_checks "$NETWORK_STACK" "$PROBE" "$NETPROBE" "$VERIFY_LOG_DIR"`,
	} {
		if !strings.Contains(verify, want) {
			t.Fatalf("verify script missing wiring %q", want)
		}
	}
	if !strings.Contains(build, "builtin/core/roles/ani/smoke/templates/probe.sh") {
		t.Fatal("offline build script does not require probe.sh")
	}
	if !strings.Contains(build, "builtin/core/roles/ani/smoke/templates/network-probe.sh") {
		t.Fatal("offline build script does not ship network-probe.sh")
	}
	for _, stale := range []string{"network-client-pod.yaml", "client-pod.yaml"} {
		if strings.Contains(role, stale) || strings.Contains(verify, stale) || strings.Contains(build, stale) {
			t.Fatalf("stale client template %q remains wired", stale)
		}
		if _, err := os.Stat(filepath.Join(root, "builtin", "core", "roles", "ani", "smoke", "templates", stale)); !os.IsNotExist(err) {
			t.Fatalf("stale client template %q still exists", stale)
		}
	}
}

func TestSmokeRoleParsesAsKubeKeyBlocks(t *testing.T) {
	root := filepath.Join("..", "..")
	data := readSmokeFile(t, filepath.Join(root, "builtin", "core", "roles", "ani", "smoke", "tasks", "main.yaml"))

	var blocks []kkprojectv1.Block
	if err := yaml.Unmarshal([]byte(data), &blocks); err != nil {
		t.Fatalf("smoke role must parse as KubeKey blocks: %v", err)
	}
	if len(blocks) == 0 {
		t.Fatal("smoke role contained no KubeKey blocks")
	}

	var probe *kkprojectv1.Block
	for i := range blocks {
		if blocks[i].Name == "ANI Smoke | Run active network and Envoy probe" {
			probe = &blocks[i]
			break
		}
	}
	if probe == nil {
		t.Fatal("active smoke probe block not found")
	}
	command, _ := probe.UnknownField["command"].(string)
	if command != "KUBECONFIG_FILE=/etc/kubernetes/admin.conf ANI_SMOKE_OUTPUT=/etc/kubernetes/ani/smoke-output bash /etc/kubernetes/ani/smoke-probe.sh" {
		t.Fatalf("active smoke probe command did not inline required environment: %q", command)
	}
}
