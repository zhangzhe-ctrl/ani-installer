package ani

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Exercise the Python validation embedded in the production node preflight
// against actual CNI JSON files, including the pinned thick shim shape.
func TestB01MultusNodePreflightCNIConfigs(t *testing.T) {
	source, err := os.ReadFile(filepath.Join("..", "..", "builtin", "core", "roles", "ani", "multus", "templates", "preflight-node.sh"))
	if err != nil {
		t.Fatal(err)
	}
	body := string(source)
	startMarker := "python3 - \"$primary\" \"$plugin\" <<'PY'\n"
	start := strings.Index(body, startMarker)
	if start < 0 {
		t.Fatal("production preflight Python block missing")
	}
	body = body[start+len(startMarker):]
	end := strings.Index(body, "\nPY\n")
	if end < 0 {
		t.Fatal("production preflight Python block terminator missing")
	}
	body = body[:end]
	dir := t.TempDir()
	body = strings.ReplaceAll(body, "glob.glob('/etc/cni/net.d/*')", "glob.glob('"+dir+"/*')")
	body = strings.ReplaceAll(body, "open('/etc/cni/net.d/'", "open('"+dir+"/'")
	body = strings.ReplaceAll(body, "open('/etc/cni/net.d/00-multus.conf')", "open('"+dir+"/00-multus.conf')")
	primary := `{"name":"kcn","plugins":[{"type":"kc-networking"},{"type":"portmap"}]}`
	shim := `{"type":"multus-shim","name":"multus-cni-network","clusterNetwork":"/host/etc/cni/net.d/01-kc-networking.conflist","namespaceIsolation":true}`
	cases := []struct {
		name    string
		primary string
		shim    string
		extra   string
		wantOK  bool
	}{
		{name: "before Multus install", primary: primary, wantOK: true},
		{name: "pinned thick shim", primary: primary, shim: shim, wantOK: true},
		{name: "thin config", primary: primary, shim: `{"type":"multus","name":"multus-cni-network","delegates":[{"name":"kcn"}]}`},
		{name: "wrong primary path", primary: primary, shim: `{"type":"multus-shim","name":"multus-cni-network","clusterNetwork":"/host/etc/cni/net.d/01-kube-ovn.conflist","namespaceIsolation":true}`},
		{name: "recursive primary", primary: `{"name":"kcn","plugins":[{"type":"kc-networking"},{"type":"multus-shim"}]}`, shim: shim},
		{name: "namespace isolation off", primary: primary, shim: `{"type":"multus-shim","name":"multus-cni-network","clusterNetwork":"/host/etc/cni/net.d/01-kc-networking.conflist","namespaceIsolation":false}`},
		{name: "foreign config", primary: primary, shim: shim, extra: `{"type":"bridge"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, name := range []string{"01-kc-networking.conflist", "00-multus.conf", "02-foreign.conf"} {
				_ = os.Remove(filepath.Join(dir, name))
			}
			for name, content := range map[string]string{"01-kc-networking.conflist": tc.primary, "00-multus.conf": tc.shim, "02-foreign.conf": tc.extra} {
				if content == "" {
					continue
				}
				if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0600); err != nil {
					t.Fatal(err)
				}
			}
			cmd := exec.Command("python3", "-", "01-kc-networking.conflist", "kc-networking")
			cmd.Stdin = strings.NewReader(body)
			output, err := cmd.CombinedOutput()
			if (err == nil) != tc.wantOK {
				t.Fatalf("preflight err=%v, wantOK=%v: %s", err, tc.wantOK, output)
			}
		})
	}
}
