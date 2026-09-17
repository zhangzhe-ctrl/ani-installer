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

	"github.com/kubesphere/kubekey/v4/pkg/ani"
)

// NewANICommand creates the small ANI installation entrypoint.
func NewANICommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "ani",
		Short: "Install the ANI cluster from the fixed offline artifact",
	}
	cmd.AddCommand(newANIInstallCommand())
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
