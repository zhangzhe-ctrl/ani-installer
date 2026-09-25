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
// R12/A12: the ANI checker's HTTP wait entry (verify.sh registry stage) must
// carry per-request curl timeouts AND a stage deadline independent of the
// image count, so a single hung API request cannot stretch the stage for
// every row in the table. Driven through the real verify.sh library with a
// fake curl; nothing touches a network.
// ---------------------------------------------------------------------------

const r12VerifyRel = "../../scripts/verify.sh"

func r12WriteImageTable(t *testing.T, rows ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "images.tsv")
	content := "original_ref\thauler_ref\tdigest\tuse\n" + strings.Join(rows, "\n") + "\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write image table: %v", err)
	}
	return path
}

func r12FakeCurl(t *testing.T, binDir string) string {
	t.Helper()
	script := `#!/usr/bin/env bash
printf '%s\n' "$*" >> "$FAKE_CURL_LOG"
if [ -n "${FAKE_CURL_MANIFEST_FAIL:-}" ] && [[ "$*" == *"/manifests/"* ]]; then
  echo "fake curl: manifest unreachable" >&2
  exit 22
fi
if [ -n "${FAKE_CURL_SLEEP:-}" ]; then
  sleep "$FAKE_CURL_SLEEP"
fi
exit 0
`
	path := filepath.Join(binDir, "curl")
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatalf("write fake curl: %v", err)
	}
	return path
}

// r12RunRegistryStage sources the verify.sh library and calls
// ani_verify_registry with a fake curl on PATH. Returns the harness stdout,
// stderr, the stage rc and the fake curl's call log path.
func r12RunRegistryStage(t *testing.T, table, deadline string, extraEnv map[string]string) (string, string, int, string) {
	t.Helper()
	verifyAbs, err := filepath.Abs(r12VerifyRel)
	if err != nil {
		t.Fatalf("abs: %v", err)
	}
	binDir := t.TempDir()
	r12FakeCurl(t, binDir)
	outDir := t.TempDir()
	curlLog := filepath.Join(outDir, "curl-calls.log")
	script := `set +e
export PATH="$BIN:$PATH"
export ANI_VERIFY_LIB_ONLY=1
export FAKE_CURL_LOG="$CLOG"
	source "$VERIFY"
rc=0
ani_verify_registry "$TABLE" "192.0.2.11" "5000" "$DEADLINE" || rc=$?
echo "STAGE_RC=$rc"
`
	env := map[string]string{
		"PATH":     binDir + ":" + os.Getenv("PATH"),
		"VERIFY":   verifyAbs,
		"TABLE":    table,
		"DEADLINE": deadline,
		"CLOG":     curlLog,
	}
	for key, value := range extraEnv {
		env[key] = value
	}
	stdout, stderr, _ := r02RunBash(t, script, env)
	code := 0
	if idx := strings.Index(stdout, "STAGE_RC="); idx >= 0 {
		code = int(stdout[idx+len("STAGE_RC=")] - '0')
	} else {
		t.Fatalf("stage did not report its rc:\n%s\n%s", stdout, stderr)
	}
	return stdout, stderr, code, curlLog
}

// T-R12-04: a per-request hang cannot stretch the stage beyond its deadline —
// after the ping consumed the budget, the next request is refused outright.
func TestRegistryStageDeadline(t *testing.T) {
	t.Run("stage deadline cuts off a hung request chain", func(t *testing.T) {
		table := r12WriteImageTable(t,
			"docker.io/library/busybox:1.37.0\t127.0.0.1:5000/library/busybox:1.37.0\tsha256:aa\tchecker",
			"docker.io/library/python:3.13.11-alpine3.23\t127.0.0.1:5000/library/python:3.13.11-alpine3.23\tsha256:bb\tchecker")
		// Deadline of 2s, but every request (including the ping) takes 3s:
		// the first manifest request must be refused by the deadline check.
		stdout, stderr, code, _ := r12RunRegistryStage(t, table, "2", map[string]string{"FAKE_CURL_SLEEP": "3"})
		if code == 0 {
			t.Fatalf("the stage must fail once its deadline is consumed:\n%s", stdout)
		}
		if !strings.Contains(stdout+stderr, "stage deadline") {
			t.Fatalf("the failure must name the stage deadline:\n%s\n%s", stdout, stderr)
		}
	})

	t.Run("healthy stage probes the ping and every manifest once", func(t *testing.T) {
		table := r12WriteImageTable(t,
			"docker.io/library/busybox:1.37.0\t127.0.0.1:5000/library/busybox:1.37.0\tsha256:aa\tchecker",
			"docker.io/library/python:3.13.11-alpine3.23\t127.0.0.1:5000/library/python:3.13.11-alpine3.23\tsha256:bb\tchecker")
		stdout, stderr, code, curlLog := r12RunRegistryStage(t, table, "300", nil)
		if code != 0 {
			t.Fatalf("healthy stage must pass:\n%s\n%s", stdout, stderr)
		}
		calls, err := os.ReadFile(curlLog)
		if err != nil {
			t.Fatalf("read curl log: %v", err)
		}
		callsStr := string(calls)
		if strings.Count(callsStr, "/v2/\n") != 1 {
			t.Fatalf("expected exactly one registry ping, calls:\n%s", callsStr)
		}
		for _, want := range []string{
			"http://192.0.2.11:5000/v2/library/busybox/manifests/1.37.0",
			"http://192.0.2.11:5000/v2/library/python/manifests/3.13.11-alpine3.23",
		} {
			if strings.Count(callsStr, want) != 1 {
				t.Fatalf("expected exactly one manifest probe for %s, calls:\n%s", want, callsStr)
			}
		}
	})

	t.Run("unreachable manifest fails with named image", func(t *testing.T) {
		table := r12WriteImageTable(t,
			"docker.io/library/busybox:1.37.0\t127.0.0.1:5000/library/busybox:1.37.0\tsha256:aa\tchecker")
		stdout, stderr, code, _ := r12RunRegistryStage(t, table, "300", map[string]string{"FAKE_CURL_MANIFEST_FAIL": "1"})
		if code == 0 {
			t.Fatalf("an unreachable manifest must fail the stage:\n%s\n%s", stdout, stderr)
		}
		if !strings.Contains(stdout+stderr, "busybox:1.37.0") {
			t.Fatalf("the failure must name the image:\n%s\n%s", stdout, stderr)
		}
	})
}
