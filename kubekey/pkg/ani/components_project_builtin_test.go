//go:build builtin
// +build builtin

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
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/kubesphere/kubekey/v4/builtin/core"
)

// TestComponentsProjectMaterializationReal proves the builtin-tagged hook
// turns this binary's embedded tree into exactly the layout `kk run`
// resolves (root/builtin/core/playbooks/… and root/builtin/core/roles/…),
// byte-identical to the embed, with every written file inside the project.
func TestComponentsProjectMaterializationReal(t *testing.T) {
	if componentsProjectMaterialize == nil {
		t.Fatal("builtin build must register the project materializer")
	}
	root := t.TempDir()
	if err := componentsProjectMaterialize(root); err != nil {
		t.Fatalf("materialize: %v", err)
	}

	core2 := filepath.Join(root, "builtin", "core")
	samples := []struct{ embedPath, diskPath string }{
		{"playbooks/" + ComponentsPlaybookRelPath, filepath.Join(core2, "playbooks", ComponentsPlaybookRelPath)},
		{"roles/ani/nats/tasks/main.yaml", filepath.Join(core2, "roles", "ani", "nats", "tasks", "main.yaml")},
		{"roles/ani/cert-manager/tasks/main.yaml", filepath.Join(core2, "roles", "ani", "cert-manager", "tasks", "main.yaml")},
	}
	for _, s := range samples {
		want, err := core.BuiltinPlaybook.ReadFile(s.embedPath)
		if err != nil {
			t.Fatalf("embedded %s missing: %v", s.embedPath, err)
		}
		got, err := os.ReadFile(s.diskPath)
		if err != nil {
			t.Fatalf("materialized %s missing: %v", s.diskPath, err)
		}
		if string(want) != string(got) {
			t.Fatalf("materialized %s differs from embedded %s", s.diskPath, s.embedPath)
		}
	}

	// The project must contain exactly the embedded file set — no extras, no
	// missing entries, nothing outside builtin/core.
	embedded := 0
	if err := fs.WalkDir(core.BuiltinPlaybook, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		embedded++
		rel := filepath.Join(core2, filepath.FromSlash(path))
		if _, err := os.Stat(rel); err != nil {
			t.Errorf("embedded %s not materialized at %s: %v", path, rel, err)
		}
		return nil
	}); err != nil {
		t.Fatalf("walk embed: %v", err)
	}
	written := 0
	if err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		written++
		rel, err := filepath.Rel(core2, path)
		if err != nil || rel == ".." || len(rel) >= 2 && rel[:2] == ".." {
			t.Errorf("materialized file %s escaped the project core directory", path)
		}
		return nil
	}); err != nil {
		t.Fatalf("walk output: %v", err)
	}
	if written != embedded {
		t.Fatalf("materialized %d files, embedded %d files — sets must match", written, embedded)
	}
}
