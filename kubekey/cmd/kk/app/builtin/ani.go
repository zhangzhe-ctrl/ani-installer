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
	cmd.AddCommand(newANIMaterialsCommand())
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
	cmd.Flags().StringVar(&input.BaseRunFile, "base-run", "", "install-success run.json of the base (default: the record the plan itself was built against)")
	cmd.Flags().StringVar(&input.BaseStateFile, "base-state", "", "optional base run-state.json beside it (must agree with --base-run)")
	cmd.Flags().StringVar(&input.Kubeconfig, "kubeconfig", "/etc/kubernetes/admin.conf", "kubeconfig handed to the playbook run (exported as KUBECONFIG to every child step)")
	cmd.Flags().StringVar(&input.Output, "output", "/var/lib/ani-installer/components", "runtime root and report output directory")
	cmd.Flags().StringVar(&input.KKBin, "kk", "", "kk binary that runs the playbook (default: THIS executable, absolute; the plan must have been made by the same binary)")
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
		Short: "Verify an installed run or an executed components addition (smoke or declared acceptance)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if strings.TrimSpace(only) != "" {
				input.Only = strings.Split(only, ",")
			}
			return ani.RunVerify(cmd.Context(), input, cmd.OutOrStdout())
		},
	}
	cmd.Flags().StringVar(&input.RunFile, "run", "", "path to a run record: the first install's own success record, or a components execution record printed by `ani components execute`")
	cmd.Flags().StringVar(&input.StateFile, "state", "", "optional install state (run-state.json); default looks next to the run record")
	cmd.Flags().StringVar(&input.Level, "level", "", "verification level: smoke (read-only) or acceptance (declared pod recreation)")
	cmd.Flags().StringVar(&only, "only", "", "comma-separated component IDs to limit the scope (each must be attested by the record: installed by it, or confirmed ANI-owned by it)")
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
			// An empty --roles-dir is not a missing argument: the render then
			// uses the role tree this release actually carries (F12).
			return ani.RunRender(input, strings.TrimSpace(rolesDir), cmd.OutOrStdout())
		},
	}
	cmd.Flags().StringVarP(&input.ConfigFile, "config", "c", "cluster.yaml", "path to the site-specific ANI cluster config")
	cmd.Flags().StringVar(&input.PackageRoot, "package-root", ".", "root carrying ani/images.tsv (dev tree or artifact layout)")
	cmd.Flags().StringVar(&input.Output, "output", "", "directory for the rendered files")
	cmd.Flags().StringVar(&input.HelmBin, "helm", "", "Helm binary for chart expansion (default: <package-root>/bin/helm)")
	cmd.Flags().StringVar(&rolesDir, "roles-dir", "", "development override: render this role tree instead of the one this release carries")
	return cmd
}

// newANIValidateCommand adds the read-only configuration check (R06).
//
// It validates the site config and writes the non-secret config-validation
// record (run.json with materialsValidated=false). Artifact materials are not
// inspected here: the install preflight does that against the approved lock, so
// a validate record can never be mistaken for proof that materials were checked.
func newANIValidateCommand() *cobra.Command {
	input := ani.ValidateInput{}
	cmd := &cobra.Command{
		Use:   "validate",
		Short: "Validate the ANI site config (configuration only; materials are verified by install)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return ani.RunValidate(cmd.Context(), input, cmd.OutOrStdout())
		},
	}
	cmd.Flags().StringVarP(&input.ConfigFile, "config", "c", "cluster.yaml", "path to the site-specific ANI cluster config")
	cmd.Flags().StringVar(&input.PackageRoot, "package-root", ".", "root of the independently released ANI offline artifact (recorded, not inspected)")
	cmd.Flags().StringVar(&input.Output, "output", "", "directory for run.json and verify-facts.env (created if missing)")
	return cmd
}

// newANIMaterialsCommand groups the packaging-time material steps that
// build-offline.sh used to do with grep/awk heuristics (F06). They share the
// lock parser and the registry content gate the installer uses, so a package
// can be verified before it ships with exactly the rules that apply on the node.
func newANIMaterialsCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "materials",
		Short: "Verify and place artifact materials against the approved lock (packaging steps)",
	}
	cmd.AddCommand(newANIMaterialsPlaceChartsCommand())
	cmd.AddCommand(newANIMaterialsPlaceToolsCommand())
	cmd.AddCommand(newANIMaterialsInjectISOCommand())
	cmd.AddCommand(newANIMaterialsVerifyRegistryCommand())
	cmd.AddCommand(newANIMaterialsRecordEvidenceCommand())
	cmd.AddCommand(newANIMaterialsLandImagesCommand())
	return cmd
}

func newANIMaterialsLandImagesCommand() *cobra.Command {
	input := ani.MaterialsLandInput{}
	cmd := &cobra.Command{
		Use:   "land-images",
		Short: "Build a store that carries the approved image bytes, blob for blob, instead of a re-written lookalike",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return ani.RunMaterialsLandApprovedImages(input, cmd.OutOrStdout())
		},
	}
	cmd.Flags().StringVar(&input.ImagesTSVPath, "images-tsv", "ani/images.tsv", "approved image table whose pins decide what is correct")
	cmd.Flags().StringArrayVar(&input.EvidenceDirs, "evidence-dir", nil, "root holding the approved manifest bytes (repeatable, required)")
	cmd.Flags().StringArrayVar(&input.SourceStores, "source-store", nil, "existing store that may already carry the blobs (repeatable, read-only)")
	cmd.Flags().StringVar(&input.OutStore, "out-store", "", "new store directory to write (required; must be absent or empty)")
	cmd.Flags().StringVar(&input.LockPath, "lock", "", "approved materials lock to cross-check each landed pair against")
	cmd.Flags().StringArrayVar(&input.CheckStores, "check-store", nil, "input store whose own tags must not contradict the approved material (repeatable; defaults to --source-store)")
	_ = cmd.MarkFlagRequired("out-store")
	return cmd
}

func newANIMaterialsRecordEvidenceCommand() *cobra.Command {
	input := ani.MaterialsEvidenceInput{}
	cmd := &cobra.Command{
		Use:   "record-evidence",
		Short: "Record the approved source manifests an image table pins, so the packaged store can be verified offline",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return ani.RunMaterialsRecordEvidence(input, cmd.OutOrStdout())
		},
	}
	cmd.Flags().StringVar(&input.ImagesTSVPath, "images-tsv", "ani/images.tsv", "path to the image table whose pins need evidence")
	cmd.Flags().StringArrayVar(&input.Sources, "from", nil, "content-addressed root holding the approved manifests (repeatable)")
	cmd.Flags().StringVar(&input.Out, "out", "", "evidence directory to write, usually <artifact>/images/evidence (required)")
	cmd.Flags().StringVar(&input.LockPath, "lock", "", "approved materials lock to cross-check every recorded pair against")
	_ = cmd.MarkFlagRequired("out")
	return cmd
}

func newANIMaterialsPlaceChartsCommand() *cobra.Command {
	input := ani.MaterialsChartInput{}
	cmd := &cobra.Command{
		Use:   "place-charts",
		Short: "Place every approved chart at its own lock path and verify the landed file",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return ani.RunMaterialsPlaceCharts(input, cmd.OutOrStdout())
		},
	}
	cmd.Flags().StringVar(&input.LockPath, "lock", "ani/components.lock.yaml", "path to the approved materials lock")
	cmd.Flags().StringVar(&input.ChartsDir, "charts-dir", "ani/charts", "directory of staged chart archives")
	cmd.Flags().StringVar(&input.ArtifactRoot, "artifact-root", "", "artifact output directory to place charts into (required)")
	_ = cmd.MarkFlagRequired("artifact-root")
	return cmd
}

func newANIMaterialsPlaceToolsCommand() *cobra.Command {
	input := ani.MaterialsToolInput{}
	cmd := &cobra.Command{
		Use:   "place-tools",
		Short: "Place every approved tool binary at its own lock path and verify the landed file",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return ani.RunMaterialsPlaceTools(input, cmd.OutOrStdout())
		},
	}
	cmd.Flags().StringVar(&input.LockPath, "lock", "ani/components.lock.yaml", "path to the approved materials lock")
	cmd.Flags().StringVar(&input.ArtifactRoot, "artifact-root", "", "artifact output directory to place binaries into (required)")
	cmd.Flags().StringArrayVar(&input.Sources, "source", nil, "approved tool as name=/path, one per lock tool entry")
	cmd.Flags().StringArrayVar(&input.Unapproved, "unapproved-bin", nil, "bin/<name>=/path for a binary the lock does not approve; reported as unapproved, never silently")
	_ = cmd.MarkFlagRequired("artifact-root")
	return cmd
}

func newANIMaterialsInjectISOCommand() *cobra.Command {
	input := ani.MaterialsISOInput{}
	cmd := &cobra.Command{
		Use:   "inject-repository-iso",
		Short: "Guarantee the artifact ships the approved repository ISO, loose and inside the artifact tarball",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return ani.RunMaterialsInjectRepositoryISO(cmd.Context(), input, cmd.OutOrStdout())
		},
	}
	cmd.Flags().StringVar(&input.ArtifactRoot, "artifact-root", "", "artifact output directory (required with --loose-copy)")
	cmd.Flags().StringVar(&input.ArtifactTar, "artifact-tar", "", "path to the KubeKey artifact tarball (required)")
	cmd.Flags().StringVar(&input.ISOFile, "iso", "", "path to the approved repository ISO (required)")
	cmd.Flags().StringVar(&input.ChecksumsFile, "checksums", "", "source-side ISO checksum record to approve against (required)")
	cmd.Flags().StringVar(&input.EntryPath, "entry-path", "", "path of the ISO inside the tarball, repository/<file> (required)")
	cmd.Flags().StringVar(&input.LooseCopyPath, "loose-copy", "", "artifact-relative path to also copy the ISO to")
	_ = cmd.MarkFlagRequired("artifact-tar")
	_ = cmd.MarkFlagRequired("iso")
	_ = cmd.MarkFlagRequired("checksums")
	_ = cmd.MarkFlagRequired("entry-path")
	return cmd
}

func newANIMaterialsVerifyRegistryCommand() *cobra.Command {
	input := ani.MaterialsRegistryVerifyInput{}
	cmd := &cobra.Command{
		Use:   "verify-registry",
		Short: "Verify every packaged image through the install's own registry content gate",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return ani.RunMaterialsVerifyRegistry(cmd.Context(), input, cmd.OutOrStdout())
		},
	}
	cmd.Flags().StringVar(&input.RegistryAddress, "registry-address", "", "host:port of the locally served store (required)")
	cmd.Flags().StringVar(&input.ImagesTSVPath, "images-tsv", "ani/images.tsv", "path to the image table")
	cmd.Flags().StringVar(&input.LockPath, "lock", "ani/components.lock.yaml", "path to the approved materials lock")
	cmd.Flags().StringArrayVar(&input.EvidenceDirs, "evidence-dir", nil, "directory holding the approved source manifests (repeatable); without it a package can only be approved by byte-exact pins")
	_ = cmd.MarkFlagRequired("registry-address")
	input.VerifyBlobBytes = true
	return cmd
}
