package ani

import (
	"bytes"
	"context"
	"testing"
)

func TestKubeKeyUsesInstallerKubeconfig(t *testing.T) {
	for _, home := range []string{"/root", "/home/ubuntu"} {
		t.Run(home, func(t *testing.T) {
			t.Setenv("HOME", home)
			t.Setenv("KUBECONFIG", "/missing/caller-config")
			var output bytes.Buffer
			err := runKubeKeyLogged(context.Background(), &output, "/bin/sh", "-c", `printf '%s' "$KUBECONFIG"`)
			if err != nil || output.String() != "/etc/kubernetes/admin.conf" {
				t.Fatalf("installer kubeconfig: output=%q err=%v", output.String(), err)
			}
		})
	}
}
