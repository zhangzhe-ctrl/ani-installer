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

// ComponentsPlaybookRelPath is the R15.2 standalone components playbook,
// relative to builtin/core/playbooks/.
const ComponentsPlaybookRelPath = "ani_components.yaml"

// componentsPlaybookRunPath is how `kk run` is addressed: playbook paths in a
// local project resolve relative to the project root (pkg/project/local.go),
// and the repo/release layout puts the tree under builtin/core.
const componentsPlaybookRunPath = "builtin/core/playbooks/" + ComponentsPlaybookRelPath

// componentsProjectMaterialize writes a kk-run-resolvable project (the
// builtin/core playbook and role tree) into a run-local directory. It is
// registered only by builds compiled with -tags builtin, which carry the tree
// inside the binary; a build without that tag cannot materialize anything and
// the executor refuses before invoking kk (the R15.3 node1 defect: a code
// release has no builtin/ directory, so a bare `kk run builtin/...` finds no
// playbook and the run dies with zero cluster effect).
var componentsProjectMaterialize func(root string) error
