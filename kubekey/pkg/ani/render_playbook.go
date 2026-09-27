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
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/kubesphere/kubekey/v4/pkg/converter/tmpl"
	"gopkg.in/yaml.v3"
)

// The render must represent the deployment it describes (F07). Two playbooks
// decide what a run does; this file reads those decisions out of the playbooks
// themselves and evaluates them with the executor's own condition engine, so a
// hand-written selection table cannot drift from what a real install runs.

// playbookRole is one role entry of a play: either a bare name or a mapping
// with a role and its when conditions.
type playbookRole struct {
	Name string
	When []string
}

// ParsePlaybookRoles reads every play of a playbook and returns its role entries
// in run order. A playbook that does not parse, or whose plays carry no roles, is
// an error: the selection check must never silently see an empty list.
func ParsePlaybookRoles(path string) ([]playbookRole, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read playbook %s: %w", path, err)
	}
	var plays []struct {
		Roles []yaml.Node `yaml:"roles"`
	}
	if err := yaml.Unmarshal(data, &plays); err != nil {
		return nil, fmt.Errorf("parse playbook %s: %w", path, err)
	}
	roles := []playbookRole{}
	for playIndex, play := range plays {
		for _, node := range play.Roles {
			switch node.Kind {
			case yaml.ScalarNode:
				roles = append(roles, playbookRole{Name: node.Value})
			case yaml.MappingNode:
				entry := struct {
					Role string    `yaml:"role"`
					When yaml.Node `yaml:"when"`
				}{}
				if err := node.Decode(&entry); err != nil {
					return nil, fmt.Errorf("play %d of %s: role entry does not parse: %w", playIndex+1, path, err)
				}
				if strings.TrimSpace(entry.Role) == "" {
					return nil, fmt.Errorf("play %d of %s: a role mapping without a role name", playIndex+1, path)
				}
				conditions, err := playbookWhenConditions(node, path, playIndex+1, entry.Role)
				if err != nil {
					return nil, err
				}
				roles = append(roles, playbookRole{Name: entry.Role, When: conditions})
			default:
				return nil, fmt.Errorf("play %d of %s: a role entry must be a name or a mapping, got %v",
					playIndex+1, path, node.Kind)
			}
		}
	}
	if len(roles) == 0 {
		return nil, fmt.Errorf("playbook %s declares no roles at all", path)
	}
	return roles, nil
}

// playbookWhenConditions extracts the when list of one role mapping. The raw
// nodes are decoded by key so a scalar string and a sequence both work, and an
// entry that carries an unrecognised shape is an error instead of an ignored
// condition.
func playbookWhenConditions(roleNode yaml.Node, path string, playNumber int, roleName string) ([]string, error) {
	for i := 0; i+1 < len(roleNode.Content); i += 2 {
		key := roleNode.Content[i]
		if key.Value != "when" {
			continue
		}
		value := roleNode.Content[i+1]
		switch value.Kind {
		case yaml.ScalarNode:
			return []string{value.Value}, nil
		case yaml.SequenceNode:
			conditions := make([]string, 0, len(value.Content))
			for _, item := range value.Content {
				if item.Kind != yaml.ScalarNode {
					return nil, fmt.Errorf("play %d of %s: role %s has a when condition that is not a scalar",
						playNumber, path, roleName)
				}
				conditions = append(conditions, item.Value)
			}
			return conditions, nil
		default:
			return nil, fmt.Errorf("play %d of %s: role %s has an unusable when condition", playNumber, path, roleName)
		}
	}
	return nil, nil
}

// ANIPlaybookSelection evaluates every ani/* role of a playbook against the
// production template context with the same engine the executor uses. It returns
// the playbook's own decision per ANI role, in playbook order.
func ANIPlaybookSelection(path string, spec map[string]any) (map[string]bool, []string, error) {
	roles, err := ParsePlaybookRoles(path)
	if err != nil {
		return nil, nil, err
	}
	selection := map[string]bool{}
	order := []string{}
	for _, role := range roles {
		short, an := aniRoleShortName(role.Name)
		if !an {
			continue
		}
		enabled := true
		if len(role.When) > 0 {
			enabled, err = tmpl.ParseBool(spec, role.When...)
			if err != nil {
				return nil, nil, fmt.Errorf("evaluate the when condition of %s: %w", role.Name, err)
			}
		}
		if _, seen := selection[short]; seen {
			return nil, nil, fmt.Errorf("playbook %s gates ani/%s more than once; the selection would be ambiguous", path, short)
		}
		selection[short] = enabled
		order = append(order, short)
	}
	if len(selection) == 0 {
		return nil, nil, fmt.Errorf("playbook %s lists no ani/ roles", path)
	}
	return selection, order, nil
}

func aniRoleShortName(name string) (string, bool) {
	if strings.HasPrefix(name, "ani/") {
		return strings.TrimPrefix(name, "ani/"), true
	}
	return name, false
}

// withFullComponentsRunScope gives a spec the shape the components playbook runs
// with: `.ani.components_run.scope` names every declared component as selected.
// A base render has no such scope (only the install playbook's own gates apply),
// and evaluating the components playbook without it would fail on a nil index
// instead of comparing the two gates.
func withFullComponentsRunScope(spec map[string]any) map[string]any {
	components := map[string]any{}
	if ani, ok := spec["ani"].(map[string]any); ok {
		if declared, ok := ani["components"].(map[string]any); ok {
			for name := range declared {
				components[name] = true
			}
		}
	}
	copied := map[string]any{}
	for key, value := range spec {
		copied[key] = value
	}
	ani := map[string]any{}
	if source, ok := spec["ani"].(map[string]any); ok {
		for key, value := range source {
			ani[key] = value
		}
	}
	ani["components_run"] = map[string]any{"scope": components}
	copied["ani"] = ani
	return copied
}

// assertPlaybookRoleSelection is the render's parity gate (F07): each ANI role's
// rendered state must equal what the playbooks decide, evaluated with the
// executor's own condition engine, and the two views must cover exactly the same
// role directories. A new role that no playbook runs, a role the render invents,
// and a selection table that disagrees with its gate are all failures.
func assertPlaybookRoleSelection(rolesDir string, spec map[string]any, cluster ClusterConfig) error {
	declared := map[string]bool{}
	for _, entry := range []struct {
		file     string
		specFile map[string]any
	}{
		{"create_cluster.yaml", spec},
		{ComponentsPlaybookRelPath, withFullComponentsRunScope(spec)},
	} {
		selection, _, err := ANIPlaybookSelection(filepath.Join(rolesDir, "..", "..", "playbooks", entry.file), entry.specFile)
		if err != nil {
			return err
		}
		for role, enabled := range selection {
			if previous, seen := declared[role]; seen && previous != enabled {
				return fmt.Errorf("role ani/%s is gated %t by one playbook and %t by another; the render cannot represent both",
					role, previous, enabled)
			}
			declared[role] = enabled
		}
	}

	onDisk, err := roleDirectories(rolesDir)
	if err != nil {
		return err
	}
	for role := range declared {
		if !onDisk[role] {
			return fmt.Errorf("a playbook runs ani/%s but %s has no such role directory", role, rolesDir)
		}
	}
	undeclared := []string{}
	for role := range onDisk {
		if _, seen := declared[role]; !seen {
			undeclared = append(undeclared, role)
		}
	}
	if len(undeclared) > 0 {
		sort.Strings(undeclared)
		return fmt.Errorf("the role directories %s are run by neither playbook; the render must not present them as deployed",
			strings.Join(undeclared, ", "))
	}
	for role, want := range declared {
		if got := aniRoleEnabled(role, cluster); got != want {
			return fmt.Errorf("role ani/%s: the playbook gate evaluates %t but the render's selection table says %t",
				role, want, got)
		}
	}
	return nil
}

// roleDirectories lists the role names present under an ANI roles root.
func roleDirectories(rolesDir string) (map[string]bool, error) {
	entries, err := os.ReadDir(rolesDir)
	if err != nil {
		return nil, fmt.Errorf("read the roles directory %s: %w", rolesDir, err)
	}
	out := map[string]bool{}
	for _, entry := range entries {
		if entry.IsDir() {
			out[entry.Name()] = true
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("the roles directory %s holds no role at all", rolesDir)
	}
	return out, nil
}
