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
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// F10 G6 regressions: the gate has to cover the shipped tree, and the checker
// that ships has to be the one that is tested. The R13 dispatcher tests ran an
// echo stub, which proves only that the dispatcher is harmless with a harmless
// stub; these tests drive the REAL rendered verify scripts of the components
// whose behaviour F01 changed.
// ---------------------------------------------------------------------------

// g6FakeKubectl records every invocation and answers the read-only node count
// with a single node, so a script that requires a 3-node base must stop at its
// first readiness gate. Everything else fails: a script that reaches for a
// mutating verb anyway is caught by the recorded trajectory.
func g6FakeKubectl(t *testing.T, binDir string) string {
	t.Helper()
	script := `#!/usr/bin/env bash
log="${FAKE_KUBECTL_LOG:?}"
printf '%s\n' "$*" >> "$log"
args="$*"
case "$args" in
  *"get nodes --no-headers"*)
    echo "node1   Ready   control-plane   1d   v1.35.8"
    exit 0 ;;
esac
echo "fake kubectl: refusing $args" >&2
exit 1
`
	path := filepath.Join(binDir, "kubectl")
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatalf("write fake kubectl: %v", err)
	}
	return path
}

// g6RenderVerify renders one role's verify.sh with the production context that
// TestComponentVerifyScriptsRenderAndParse uses, so the executed text is the
// shipped text rather than a hand-written stand-in.
func g6RenderVerify(t *testing.T, role string) string {
	t.Helper()
	files, err := RenderSite(filepath.Join("..", "..", "builtin", "core", "roles", "ani"),
		g6Cluster(t), "/opt/ani", g5Table(t, "images.tsv"))
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	name := role + "-verify.sh"
	for _, file := range files {
		if file.Name == name {
			target := filepath.Join(t.TempDir(), "verify.sh")
			if err := os.WriteFile(target, file.Rendered, 0o700); err != nil {
				t.Fatal(err)
			}
			return target
		}
	}
	t.Fatalf("the render produced no %s", name)
	return ""
}

func g6Cluster(t *testing.T) ClusterConfig {
	t.Helper()
	// Selecting a log backend also selects the collector: there is no separate
	// fluent-bit switch, so no combination can ask for collection without a
	// backend.
	site := strings.Replace(r06SiteA, "certManager: {enabled: true}",
		"certManager: {enabled: true}\n  metrics: {enabled: true, storageClass: ani-block}\n"+
			"  logging:\n    backend: loki\n    storageClass: ani-block\n    storageSize: 5Gi", 1)
	cluster, err := ParseClusterConfig([]byte(site))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if err := Validate(cluster); err != nil {
		t.Fatalf("validate: %v", err)
	}
	return cluster
}

// T-R13 replacement (F10): the real smoke-level scripts of the components that
// F01 touched must fail closed on an unready base without issuing a single
// mutating verb — that is what "verification never destroys state" means.
func TestG6RealSmokeScriptsMutateNothing(t *testing.T) {
	for _, role := range []string{"metrics", "fluent-bit"} {
		t.Run(role, func(t *testing.T) {
			script := g6RenderVerify(t, role)
			baseDir := t.TempDir()
			binDir := filepath.Join(baseDir, "bin")
			if err := os.MkdirAll(binDir, 0o700); err != nil {
				t.Fatal(err)
			}
			g6FakeKubectl(t, binDir)
			kubectlLog := filepath.Join(baseDir, "kubectl-calls.log")
			t.Setenv("FAKE_KUBECTL_LOG", kubectlLog)
			t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))
			t.Setenv("KUBECONFIG", filepath.Join(baseDir, "kubeconfig"))
			t.Setenv("ANI_VERIFY_LEVEL", "smoke")
			t.Setenv("ANI_VERIFY_OUTPUT_DIR", filepath.Join(baseDir, "out"))

			rc, combined := g6RunBash(t, script)
			if rc == 0 {
				t.Fatalf("the script must fail on a single-node base instead of passing silently:\n%s", combined)
			}
			calls, err := os.ReadFile(kubectlLog)
			if err != nil {
				t.Fatalf("read the kubectl trajectory: %v", err)
			}
			for _, line := range strings.Split(strings.TrimSpace(string(calls)), "\n") {
				if line == "" {
					continue
				}
				for _, verb := range []string{" delete ", " apply ", " patch ", " create ", " replace ",
					" scale ", " label ", " cordon ", " drain ", " taint ", " rollout ", " exec "} {
					if strings.Contains(" "+line+" ", verb) {
						t.Fatalf("a smoke run against an unready base must never issue %q, but ran:\n%s", verb, calls)
					}
				}
			}
			if !strings.Contains(string(calls), "get ") {
				t.Fatalf("the script must have started with read-only queries:\n%s", calls)
			}
		})
	}
}

// g6RunBash executes one script and reports its exit status with the combined
// output, so a failure can be read rather than guessed.
func g6RunBash(t *testing.T, script string) (int, string) {
	t.Helper()
	command := exec.Command("bash", script)
	combined, err := command.CombinedOutput()
	code := 0
	if err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) {
			t.Fatalf("run %s: %v\n%s", script, err, combined)
		}
		code = exit.ExitCode()
	}
	return code, string(combined)
}
