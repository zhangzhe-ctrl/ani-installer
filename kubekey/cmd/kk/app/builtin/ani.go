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
	"github.com/spf13/cobra"

	"path/filepath"
	"strings"

	"github.com/kubesphere/kubekey/v4/pkg/ani"
)

// NewANICommand creates the small ANI installation entrypoint.
func NewANICommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "ani",
		Short: "Install the ANI cluster from the fixed offline artifact",
	}
	cmd.AddCommand(newANIInstallCommand())
	cmd.AddCommand(newANIValidateCommand())
	cmd.AddCommand(newANIRenderCommand())
	cmd.AddCommand(newANIVerifyCommand())
	cmd.AddCommand(newANIComponentsCommand())
	return cmd
}

// newANIComponentsCommand adds the R15 components-only entry: on a healthy,
// identity-known base cluster it adds declared components without touching
// the base. R15.1 delivers the plan and rejection paths; execution is R15.2.
func newANIComponentsCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "components",
		Short: "Add declared components to an installed ANI cluster",
	}
	cmd.AddCommand(newANIComponentsInstallCommand())
	cmd.AddCommand(newANIComponentsExecuteCommand())
	return cmd
}

// newANIComponentsExecuteCommand executes the scope of an R15.1 plan: the
// standalone components playbook, the new run's connections and report.
func newANIComponentsExecuteCommand() *cobra.Command {
	input := ani.ComponentsExecuteInput{}
	cmd := &cobra.Command{
		Use:   "execute",
		Short: "Execute the planned scope of a components plan (R15.2)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return ani.RunComponentsExecute(cmd.Context(), input, cmd.OutOrStdout())
		},
	}
	cmd.Flags().StringVar(&input.PlanFile, "plan", "", "path to the components plan JSON from the R15.1 plan step")
	cmd.Flags().StringVar(&input.ConfigFile, "config", "cluster.yaml", "path to the same site config the plan was built from")
	cmd.Flags().StringVar(&input.PackageRoot, "package-root", ".", "root of the original offline artifact")
	cmd.Flags().StringVar(&input.Kubeconfig, "kubeconfig", "/etc/kubernetes/admin.conf", "kubeconfig handed to the playbook run")
	cmd.Flags().StringVar(&input.Output, "output", "/var/lib/ani-installer/components", "runtime root and report output directory")
	cmd.Flags().StringVar(&input.KKBin, "kk", "", "kk binary that runs the playbook (default: kk on PATH)")
	cmd.Flags().StringVar(&input.ProjectAddr, "project-addr", "", "optional local playbook project root containing builtin/")
	_ = cmd.MarkFlagRequired("plan")
	return cmd
}

func newANIComponentsInstallCommand() *cobra.Command {
	input := ani.ComponentsInstallInput{}
	only := ""
	cmd := &cobra.Command{
		Use:   "install",
		Short: "Plan (R15.1) the addition of the --only components",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if strings.TrimSpace(only) != "" {
				input.Only = strings.Split(only, ",")
			}
			return ani.RunComponentsInstallPlan(cmd.Context(), input, cmd.OutOrStdout())
		},
	}
	cmd.Flags().StringVarP(&input.ConfigFile, "config", "c", "cluster.yaml", "path to the site config WITH the new components enabled")
	cmd.Flags().StringVar(&input.PackageRoot, "package-root", ".", "root of the original offline artifact (same one the base used)")
	cmd.Flags().StringVar(&only, "only", "", "comma-separated canonical component IDs to add (required)")
	cmd.Flags().StringVar(&input.BaseRunFile, "base-run", "", "path to the base install's run.json (identity/invariants source)")
	cmd.Flags().StringVar(&input.StateFile, "state", "", "optional base install state (run-state.json)")
	cmd.Flags().StringVar(&input.Kubeconfig, "kubeconfig", "/etc/kubernetes/admin.conf", "kubeconfig for the read-only live preflight")
	cmd.Flags().StringVar(&input.Output, "output", "/var/lib/ani-installer/components", "plan and new run-record output directory")
	_ = cmd.MarkFlagRequired("only")
	_ = cmd.MarkFlagRequired("base-run")
	return cmd
}

// newANIVerifyCommand adds the R13 verification dispatcher: it reads the run
// record (never the site YAML) and dispatches the smoke (read-only) and
// acceptance (declared, flag-gated mutation) levels separately, writing a
// per-run, per-level report.
func newANIVerifyCommand() *cobra.Command {
	input := ani.VerifyInput{}
	only := ""
	cmd := &cobra.Command{
		Use:   "verify",
		Short: "Verify an installed run (smoke or declared acceptance)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if strings.TrimSpace(only) != "" {
				input.Only = strings.Split(only, ",")
			}
			return ani.RunVerify(cmd.Context(), input, cmd.OutOrStdout())
		},
	}
	cmd.Flags().StringVar(&input.RunFile, "run", "", "path to the run.json record of a successful install")
	cmd.Flags().StringVar(&input.StateFile, "state", "", "optional install state (run-state.json); default looks next to the run record")
	cmd.Flags().StringVar(&input.Level, "level", "", "verification level: smoke (read-only) or acceptance (declared pod recreation)")
	cmd.Flags().StringVar(&only, "only", "", "comma-separated component IDs to limit the scope (must be part of the run record)")
	cmd.Flags().BoolVar(&input.AllowPodRecreate, "allow-pod-recreate", false, "acceptance only: allow the one declared, planned pod recreation per target")
	cmd.Flags().StringVar(&input.ScriptDir, "script-dir", "/etc/kubernetes/ani", "directory holding the packaged component verify scripts")
	cmd.Flags().StringVar(&input.Kubeconfig, "kubeconfig", "/etc/kubernetes/admin.conf", "kubeconfig for acceptance kubectl calls and smoke scripts")
	cmd.Flags().StringVar(&input.Output, "output", "/var/lib/ani-installer/verify", "report output directory")
	_ = cmd.MarkFlagRequired("run")
	_ = cmd.MarkFlagRequired("level")
	return cmd
}

func newANIInstallCommand() *cobra.Command {
	input := ani.InstallInput{}
	cmd := &cobra.Command{
		Use:   "install",
		Short: "Run the ANI offline installation from the fixed artifact",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return ani.RunInstall(cmd.Context(), input)
		},
	}
	cmd.Flags().StringVarP(&input.ConfigFile, "config", "c", "cluster.yaml", "path to the site-specific ANI cluster config")
	cmd.Flags().StringVar(&input.PackageRoot, "package-root", ".", "root of the independently released ANI offline artifact")
	return cmd
}

// newANIRenderCommand adds the offline render check (R08): it renders every
// enabled role file with the production context, runs the semantic checks and
// writes the result. It never touches a live cluster and never applies anything.
func newANIRenderCommand() *cobra.Command {
	input := ani.ValidateInput{}
	rolesDir := ""
	cmd := &cobra.Command{
		Use:   "render",
		Short: "Render the enabled ANI role files offline and run the semantic checks",
		RunE: func(cmd *cobra.Command, _ []string) error {
			roles := strings.TrimSpace(rolesDir)
			if roles == "" {
				roles = filepath.Join(input.PackageRoot, "builtin", "core", "roles", "ani")
			}
			return ani.RunRender(input, roles, cmd.OutOrStdout())
		},
	}
	cmd.Flags().StringVarP(&input.ConfigFile, "config", "c", "cluster.yaml", "path to the site-specific ANI cluster config")
	cmd.Flags().StringVar(&input.PackageRoot, "package-root", ".", "root carrying ani/images.tsv (dev tree or artifact layout)")
	cmd.Flags().StringVar(&input.Output, "output", "", "directory for the rendered files")
	cmd.Flags().StringVar(&rolesDir, "roles-dir", "", "ANI roles source directory (default: <package-root>/builtin/core/roles/ani)")
	return cmd
}

// newANIValidateCommand adds the read-only configuration check (R06).
//
// This build validates the site config and writes the non-secret run manifest
// plus the shell facts file; it does NOT inspect the artifact — material
// validation arrives with R07, and the command says so on every run so an
// intermediate version can never be reported as a complete validation.
func newANIValidateCommand() *cobra.Command {
	input := ani.ValidateInput{}
	cmd := &cobra.Command{
		Use:   "validate",
		Short: "Validate the ANI site config (configuration only; materials arrive with R07)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return ani.RunValidate(cmd.Context(), input, cmd.OutOrStdout())
		},
	}
	cmd.Flags().StringVarP(&input.ConfigFile, "config", "c", "cluster.yaml", "path to the site-specific ANI cluster config")
	cmd.Flags().StringVar(&input.PackageRoot, "package-root", ".", "root of the independently released ANI offline artifact (recorded, not inspected yet)")
	cmd.Flags().StringVar(&input.Output, "output", "", "directory for run.json and verify-facts.env (created if missing)")
	return cmd
}
