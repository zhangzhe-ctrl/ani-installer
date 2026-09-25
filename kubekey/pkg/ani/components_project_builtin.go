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
	"strings"

	"github.com/cockroachdb/errors"

	"github.com/kubesphere/kubekey/v4/builtin/core"
)

func init() {
	componentsProjectMaterialize = materializeBuiltinComponentsProject
}

// materializeBuiltinComponentsProject copies this binary's embedded builtin
// playbooks+roles tree (the same source the base install runs from via the
// builtin project annotation) into <root>/builtin/core, producing exactly
// the layout GetProjectPath expects for `kk run builtin/core/playbooks/...`.
// The project always matches the running binary, so a components run can
// never execute roles newer or older than the kk that planned it.
func materializeBuiltinComponentsProject(root string) error {
	dst := filepath.Join(root, "builtin", "core")
	return fs.WalkDir(core.BuiltinPlaybook, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return errors.Wrapf(err, "walk the embedded builtin project at %s", path)
		}
		if d.IsDir() {
			return nil
		}
		data, err := core.BuiltinPlaybook.ReadFile(path)
		if err != nil {
			return errors.Wrapf(err, "read embedded %s", path)
		}
		// Defense in depth: embedded paths are compile-time trusted, but the
		// written file must stay inside the run's own project directory.
		target := filepath.Join(dst, filepath.Clean("/"+filepath.FromSlash(path)))
		if !isUnder(dst, target) {
			return errors.Errorf("embedded path %q escapes the project root", path)
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return errors.Wrapf(err, "create directory for %s", target)
		}
		return errors.Wrapf(os.WriteFile(target, data, 0o400), "write %s", target)
	})
}

func isUnder(root, target string) bool {
	rel, err := filepath.Rel(root, target)
	return err == nil && !filepath.IsAbs(rel) && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
