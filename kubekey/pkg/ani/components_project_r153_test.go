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
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// R15.3 live-defect regression: `kk run` only resolves a playbook from a
// local project directory, and a code release carries no builtin/ tree. The
// executor must therefore hand `kk run` a resolvable project: by default one
// materialized from this binary's embedded builtin project (R15.3 fix), or
// an explicit --project-addr. A build that can neither materialize nor was
// given an address must refuse BEFORE invoking kk — the node1 failure mode
// (kk run cannot find playbook, zero cluster effect) must not repeat.
// ---------------------------------------------------------------------------

// r153FakeMaterializer writes a marker project tree the way the embedded
// extractor does: <root>/builtin/core/playbooks/ani_components.yaml.
func r153FakeMaterializer(root string) error {
	path := filepath.Join(root, "builtin", "core", "playbooks", ComponentsPlaybookRelPath)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, []byte("# materialized by fake\n"), 0o400)
}

func TestComponentsExecuteRefusesUnresolvableProject(t *testing.T) {
	restore := componentsProjectMaterialize
	t.Cleanup(func() { componentsProjectMaterialize = restore })

	_, execute, kkLog, _ := r15PlanAndExecute(t, "  certManager: {enabled: true}\n  nats: {enabled: true}", "absent")
	componentsProjectMaterialize = nil
	execute.ProjectAddr = ""

	err := RunComponentsExecute(context.Background(), execute, os.Stdout)
	if err == nil {
		t.Fatal("a release without builtin/ must refuse to run kk against an unresolvable playbook path")
	}
	msg := strings.ToLower(err.Error())
	if !strings.Contains(msg, "project") {
		t.Fatalf("the refusal must name the missing project resolution, got: %v", err)
	}
	if _, statErr := os.Stat(kkLog); !os.IsNotExist(statErr) {
		contents, _ := os.ReadFile(kkLog)
		if strings.TrimSpace(string(contents)) != "" {
			t.Fatalf("kk must not be invoked after the refusal:\n%s", contents)
		}
	}
}

func TestComponentsExecuteMaterializesProjectAndPassesAddress(t *testing.T) {
	restore := componentsProjectMaterialize
	t.Cleanup(func() { componentsProjectMaterialize = restore })

	_, execute, kkLog, outDir := r15PlanAndExecute(t, "  certManager: {enabled: true}\n  nats: {enabled: true}", "absent")
	componentsProjectMaterialize = r153FakeMaterializer
	execute.ProjectAddr = ""

	if err := RunComponentsExecute(context.Background(), execute, os.Stdout); err != nil {
		t.Fatalf("execute with a materializable project must pass: %v", err)
	}
	logData, err := os.ReadFile(kkLog)
	if err != nil {
		t.Fatalf("read kk log: %v", err)
	}
	log := string(logData)
	idx := strings.Index(log, "--project-addr")
	if idx < 0 {
		t.Fatalf("the executor must hand kk run an explicit --project-addr:\n%s", log)
	}
	rest := strings.Fields(log[idx:])[1]
	if rest == "" {
		t.Fatalf("malformed --project-addr in:\n%s", log)
	}
	marker := filepath.Join(rest, "builtin", "core", "playbooks", ComponentsPlaybookRelPath)
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("the materialized project must contain the playbook kk run will resolve (%s): %v", marker, err)
	}
	if !strings.HasPrefix(rest, filepath.Join(outDir, "runtime")) {
		t.Fatalf("the project must live inside this run's own runtime root, got %s", rest)
	}
}

func TestComponentsExecuteExplicitProjectAddrWins(t *testing.T) {
	restore := componentsProjectMaterialize
	t.Cleanup(func() { componentsProjectMaterialize = restore })
	materialized := false

	_, execute, kkLog, _ := r15PlanAndExecute(t, "  certManager: {enabled: true}\n  nats: {enabled: true}", "absent")
	componentsProjectMaterialize = func(string) error { materialized = true; return nil }
	baseDir := filepath.Dir(execute.ConfigFile)
	given := filepath.Join(baseDir, "given-project")
	if err := os.MkdirAll(filepath.Join(given, "builtin", "core", "playbooks"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(given, "builtin", "core", "playbooks", ComponentsPlaybookRelPath), []byte("# explicit\n"), 0o400); err != nil {
		t.Fatal(err)
	}
	execute.ProjectAddr = given

	if err := RunComponentsExecute(context.Background(), execute, os.Stdout); err != nil {
		t.Fatalf("explicit --project-addr must still work: %v", err)
	}
	if materialized {
		t.Fatal("an explicit project address must not trigger materialization")
	}
	logData, _ := os.ReadFile(kkLog)
	if !strings.Contains(string(logData), "--project-addr "+given) {
		t.Fatalf("the explicit address must be passed verbatim:\n%s", logData)
	}
}
