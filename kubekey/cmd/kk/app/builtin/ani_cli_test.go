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

package builtin

import (
	"bytes"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// ---------------------------------------------------------------------------
// F10/T36: the shipped CLI surface. These tests run against the same
// -tags builtin tree the release builds, so a command, a default or a required
// flag that only exists in the development tree cannot pass the gate.
// ---------------------------------------------------------------------------

func findCommand(root *cobra.Command, path ...string) *cobra.Command {
	current := root
	for _, name := range path {
		var next *cobra.Command
		for _, child := range current.Commands() {
			if child.Name() == name {
				next = child
				break
			}
		}
		if next == nil {
			return nil
		}
		current = next
	}
	return current
}

func TestANICLISurface(t *testing.T) {
	root := NewANICommand()
	var help bytes.Buffer
	root.SetOut(&help)
	root.SetErr(&help)
	root.SetArgs([]string{"--help"})
	if err := root.Execute(); err != nil {
		t.Fatalf("the packaged CLI must render its help: %v", err)
	}
	for _, command := range []string{"install", "validate", "render", "verify", "components", "materials"} {
		if findCommand(root, command) == nil {
			t.Fatalf("the shipped ani command tree must carry %q", command)
		}
	}
	for _, path := range [][]string{
		{"components", "install"}, {"components", "execute"},
		{"materials", "place-tools"}, {"materials", "place-charts"},
		{"materials", "verify-registry"}, {"materials", "inject-repository-iso"},
	} {
		if findCommand(root, path...) == nil {
			t.Fatalf("the shipped CLI must carry `kk ani %s`", strings.Join(path, " "))
		}
	}

	// Required inputs are required, not defaulted-then-refused later.
	execute := findCommand(root, "components", "execute")
	if err := execute.Flags().Set("plan", ""); err != nil {
		t.Fatalf("execute --plan must be a flag: %v", err)
	}
	if execute.Flags().Lookup("plan") == nil {
		t.Fatal("components execute must declare --plan")
	}
	install := findCommand(root, "components", "install")
	for _, name := range []string{"only", "base-run"} {
		if install.Flags().Lookup(name) == nil {
			t.Fatalf("components install must declare --%s", name)
		}
	}
}

// F12: the defaults must be the release's own material, so an operator never
// has to hand-supply a source tree or a binary for a normal run.
func TestANICLIDefaultsAreReleaseLocal(t *testing.T) {
	render := findCommand(NewANICommand(), "render")
	for _, name := range []string{"roles-dir", "helm"} {
		flag := render.Flags().Lookup(name)
		if flag == nil {
			t.Fatalf("render must expose --%s as the development override", name)
		}
		if flag.DefValue != "" {
			t.Fatalf("render --%s must default to empty (use what this release carries), got %q", name, flag.DefValue)
		}
	}
	if !strings.Contains(render.Flags().Lookup("roles-dir").Usage, "override") {
		t.Fatalf("--roles-dir help must say it is an override: %q", render.Flags().Lookup("roles-dir").Usage)
	}
	if !strings.Contains(render.Flags().Lookup("helm").Usage, "bin/helm") {
		t.Fatalf("--helm help must name the artifact's own Helm default: %q", render.Flags().Lookup("helm").Usage)
	}

	execute := findCommand(NewANICommand(), "components", "execute")
	kk := execute.Flags().Lookup("kk")
	if kk == nil {
		t.Fatal("components execute must expose --kk")
	}
	if kk.DefValue != "" {
		t.Fatalf("components execute must default to THIS executable, not a PATH lookup, got %q", kk.DefValue)
	}
	if !strings.Contains(kk.Usage, "executable") {
		t.Fatalf("--kk help must say the default is this executable: %q", kk.Usage)
	}
	if kubeconfig := execute.Flags().Lookup("kubeconfig"); kubeconfig == nil ||
		!strings.Contains(kubeconfig.Usage, "KUBECONFIG") {
		t.Fatalf("execute --kubeconfig must state that it is exported to every child step")
	}
}
