/*
Copyright 2026 The KubeSphere Contributors.
Licensed under Apache License, Version 2.0.
*/

package ani

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"text/template"
)

// execBash runs one packaged checker with an exact environment, so what a script
// resolves is decided by this test and not by whatever the developer's shell had.
func execBash(t *testing.T, script string, env ...string) (string, error) {
	t.Helper()
	cmd := exec.Command("bash", script)
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// ---------------------------------------------------------------------------
// C07 — one approved context has to reach the layer that actually calls the API.
//
// Passing `--kubeconfig A` down to the child `kk`, and having the role render a
// checker, proved nothing about the target: the checkers defaulted to
// /etc/kubernetes/admin.conf when their pin was absent, and Fluent Bit called
// bare `kubectl`, which resolves $KUBECONFIG and then $HOME/.kube/config. So the
// install layer and the verification layer could silently be on different
// clusters. These tests execute the REAL packaged checker text with two
// conflicting dummy configurations and read the target off the commands.
// ---------------------------------------------------------------------------

// c07Checker runs one packaged checker verbatim and returns every kubectl the
// script really issued, with the environment each one saw.
func c07Checker(t *testing.T, component string, env []string, home string) (stdout string, calls []string) {
	t.Helper()
	root := filepath.Join("..", "..", "builtin", "core", "roles", "ani", component, "templates", "verify.sh")
	var packed string
	// The checker is a template the installer renders before it lands, so the
	// test renders it the same way rather than feeding bash a half-substituted
	// file: Fluent Bit selects its backend from `.ani.components.logging`, and
	// an unrendered file exits before it ever reaches kubectl — which would have
	// made this test silently test nothing.
	packedSource, err := os.ReadFile(root)
	if err != nil {
		t.Fatalf("read the packaged checker %s: %v", root, err)
	}
	tmpl, err := template.New("verify.sh").Parse(string(packedSource))
	if err != nil {
		t.Fatalf("parse the checker template %s: %v", root, err)
	}
	rendered := &strings.Builder{}
	if err := tmpl.Execute(rendered, c07TemplateContext()); err != nil {
		t.Fatalf("render the checker template %s: %v", root, err)
	}
	packed = rendered.String()
	for _, bad := range []string{"{{", "<no value>"} {
		if strings.Contains(packed, bad) {
			t.Fatalf("the checker rendered with %q still in it, so the script under test is not the script that ships:\n%s", bad, packed)
		}
	}
	dir := t.TempDir()
	binDir := filepath.Join(dir, "bin")
	if err := os.MkdirAll(binDir, 0o700); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(dir, "kubectl.log")
	// A fake kubectl that records the target it was actually given: the flag when
	// the script passes one, and the environment it inherits otherwise. Bare
	// `kubectl` with no flag and no KUBECONFIG would show up as target=<unset>.
	fake := "#!/usr/bin/env bash\n" +
		"flag=\"\"\ncase \"$*\" in *--kubeconfig*) flag=\"$(printf '%s\\n' \"$*\" | sed -n 's/.*--kubeconfig \\([^ ]*\\).*/\\1/p')\";; esac\n" +
		"printf 'argv_target=%s env_target=%s home=%s :: %s\\n' \"${flag:-<none>}\" \"${KUBECONFIG:-<unset>}\" \"${HOME:-<unset>}\" \"$*\" >> \"" + logPath + "\"\n" +
		// Answer nothing usefully: the script will fail later, which is fine. What
		// matters is the target of the calls it did make.
		"exit 1\n"
	if err := os.WriteFile(filepath.Join(binDir, "kubectl"), []byte(fake), 0o700); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(dir, "verify.sh")
	if err := os.WriteFile(script, []byte(packed), 0o755); err != nil {
		t.Fatal(err)
	}
	cmdEnv := append(os.Environ(), "PATH="+binDir+":"+os.Getenv("PATH"), "HOME="+home)
	cmdEnv = append(cmdEnv, env...)
	out, _ := execBash(t, script, append(cmdEnv, "ANI_VERIFY_OUTPUT_DIR="+filepath.Join(dir, "out"))...)
	_ = out
	data, err := os.ReadFile(logPath)
	if err == nil {
		calls = strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	}
	return string(out), calls
}

func c07DummyKubeconfigs(t *testing.T) (a, b, home string) {
	t.Helper()
	root := t.TempDir()
	a = filepath.Join(root, "A.conf")
	b = filepath.Join(root, "B.conf")
	home = filepath.Join(root, "home")
	if err := os.MkdirAll(filepath.Join(home, ".kube"), 0o700); err != nil {
		t.Fatal(err)
	}
	docs := func(path, name string) string {
		return "apiVersion: v1\nkind: Config\nclusters:\n- name: " + name + "\n  cluster:\n    server: https://" + name + ".example:6443\ncontexts:\n- name: " + name + "\n  context:\n    cluster: " + name + "\n    user: " + name + "\ncurrent-context: " + name + "\nusers:\n- name: " + name + "\n"
	}
	if err := os.WriteFile(a, []byte(docs(a, "cluster-A")), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(b, []byte(docs(b, "cluster-B")), 0o600); err != nil {
		t.Fatal(err)
	}
	// The trap: a HOME whose default config points somewhere else entirely.
	if err := os.WriteFile(filepath.Join(home, ".kube", "config"), []byte(docs(filepath.Join(home, ".kube", "config"), "home-default")), 0o600); err != nil {
		t.Fatal(err)
	}
	return a, b, home
}

// c07AssertOneTarget requires that the checker really called the API (a script
// that died before its first query proves nothing) and that every call went to
// the pinned context.
func c07AssertOneTarget(t *testing.T, calls []string, want, stdout string) {
	t.Helper()
	if len(calls) == 0 {
		t.Fatalf("the checker never reached kubectl, so the target is untested.\noutput: %s", stdout)
	}
	for _, line := range calls {
		if strings.Contains(line, "cluster-B") || strings.Contains(line, "home-default") {
			t.Fatalf("a checker call resolved to a different context than the pinned one: %s", line)
		}
		if !strings.Contains(line, "env_target="+want) && !strings.Contains(line, "argv_target="+want) {
			t.Fatalf("the call did not carry the pinned kubeconfig %s: %s", want, line)
		}
	}
}

func TestC07_CheckersHonourThePinnedKubeconfig(t *testing.T) {
	a, _, home := c07DummyKubeconfigs(t)
	for _, component := range []string{"cert-manager", "metrics", "nats", "postgresql", "valkey", "fluent-bit", "loki", "opensearch"} {
		t.Run(component, func(t *testing.T) {
			stdout, calls := c07Checker(t, component,
				[]string{"ANI_VERIFY_KUBECONFIG=" + a, "KUBECONFIG=" + a, "ANI_SMOKE_OUTPUT=" + filepath.Join(home, "smoke")}, home)
			c07AssertOneTarget(t, calls, a, stdout)
		})
	}
}

func TestC07_NoCheckerSilentlyFallsBackToAdminConf(t *testing.T) {
	_, b, home := c07DummyKubeconfigs(t)
	for _, component := range []string{"cert-manager", "metrics", "nats", "postgresql", "valkey", "fluent-bit", "loki", "opensearch"} {
		t.Run(component, func(t *testing.T) {
			stdout, calls := c07Checker(t, component, []string{"KUBECONFIG=" + b}, home)
			if !strings.Contains(stdout, "ANI_VERIFY_KUBECONFIG") {
				t.Fatalf("the checker ran without naming the missing pin, so it can still default a target:\n%s\n calls: %v", stdout, calls)
			}
			if len(calls) != 0 {
				t.Fatalf("the checker reached the API through an inherited default: %v", calls)
			}
		})
	}
}

func TestC07_ConflictingContextsAreRefusedNotGuessed(t *testing.T) {
	a, b, home := c07DummyKubeconfigs(t)
	for _, component := range []string{"cert-manager", "nats", "postgresql", "fluent-bit", "loki", "opensearch", "metrics", "valkey"} {
		t.Run(component, func(t *testing.T) {
			stdout, calls := c07Checker(t, component,
				[]string{"ANI_VERIFY_KUBECONFIG=" + a, "KUBECONFIG=" + b}, home)
			if !strings.Contains(stdout, "ambiguous target") {
				t.Fatalf("two different contexts must be refused, not resolved by whichever layer wins:\n%s", stdout)
			}
			if len(calls) != 0 {
				t.Fatalf("an ambiguous target still reached the API: %v", calls)
			}
		})
	}
}

// c07TemplateContext is the installer-side render context a packaged checker
// needs: the image references resolve through the same map the roles use, and the
// logging/metrics blocks carry the values the selection would produce.
func c07TemplateContext() map[string]any {
	images := map[string]string{}
	for _, ref := range []string{
		"docker.io/alpine/openssl:3.5.4",
		"docker.io/library/busybox:1.37.0",
		"docker.io/library/postgres:17.11-bookworm",
		"docker.io/library/python:3.13.11-alpine3.23",
		"docker.io/natsio/nats-box:0.19.7",
		"docker.io/valkey/valkey:8.1.10-alpine",
	} {
		images[ref] = "registry.local:5000/" + strings.TrimPrefix(ref, "docker.io/")
	}
	return map[string]any{
		"ani": map[string]any{
			"images": images,
			"components": map[string]any{
				"logging": map[string]any{
					"enabled":         true,
					"backend":         "loki",
					"namespace":       "ani-observability",
					"storage_class":   "ani-block",
					"storage_size":    "10Gi",
					"retention_days":  "3",
					"retention_hours": 72,
					"retention_iso":   "PT72H",
				},
				"metrics": map[string]any{
					"namespace": "ani-observability",
					"run_id":    "c07-test",
				},
			},
		},
		"kubernetes": map[string]any{"cluster_name": "ani-lab"},
	}
}
