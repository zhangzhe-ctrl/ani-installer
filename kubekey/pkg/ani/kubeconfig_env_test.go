package ani

import (
	"bytes"
	"context"
	"testing"
)

func TestKubeKeyUsesInstallerKubeconfig(t *testing.T) {
	// The child kk is handed the run's own scope, never a caller's KUBECONFIG.
	kubeconfig := InstallRunScope("ani-lab").Kubeconfig
	for _, home := range []string{"/root", "/home/ubuntu"} {
		t.Run(home, func(t *testing.T) {
			t.Setenv("HOME", home)
			t.Setenv("KUBECONFIG", "/missing/caller-config")
			var output bytes.Buffer
			err := runKubeKeyLogged(context.Background(), &output, kubeconfig, "/bin/sh", "-c", `printf '%s' "$KUBECONFIG"`)
			if err != nil || output.String() != kubeconfig {
				t.Fatalf("installer kubeconfig: output=%q want=%q err=%v", output.String(), kubeconfig, err)
			}
		})
	}
}
