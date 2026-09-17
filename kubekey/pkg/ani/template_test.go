package ani

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"text/template"
)

func TestKCNManifestTemplateUsesSiteInputs(t *testing.T) {
	path := filepath.Join("..", "..", "builtin", "core", "roles", "ani", "kcn", "templates", "install.yaml")
	data := map[string]any{
		"ani": map[string]any{
			"images": map[string]string{
				"docker.changqingyun.cn/kubercloud/kc-networking:v0.6.2": "192.0.2.11:5000/kubercloud/kc-networking:v0.6.2",
			},
			"node_addresses": []string{"192.0.2.11", "192.0.2.12", "192.0.2.13"},
			"network": map[string]any{
				"service_cidr": "10.96.0.0/16",
				"kcn": map[string]any{
					"managedDevices":   []string{"ens35"},
					"encapNetworks":    []string{"192.0.2.0/24"},
					"intranetNetworks": []string{"10.96.0.0/16"},
				},
			},
		},
	}

	tmpl, err := template.New("install.yaml").Funcs(template.FuncMap{
		"join": func(sep string, values []string) string { return strings.Join(values, sep) },
	}).ParseFiles(path)
	if err != nil {
		t.Fatalf("parse kcn template: %v", err)
	}
	builder := &strings.Builder{}
	if err := tmpl.Execute(builder, data); err != nil {
		t.Fatalf("execute kcn template: %v", err)
	}
	out := builder.String()

	for _, want := range []string{
		"--service-cluster-ip-range=10.96.0.0/16",
		"value: 192.0.2.11,192.0.2.12,192.0.2.13",
		"managedDevices: ens35",
		"encapNetworks: 192.0.2.0/24",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("rendered manifest missing %q", want)
		}
	}
	for _, stale := range []string{"33.3.1.201", "33.3.1.202", "33.3.1.203", "33.3.64.0/19"} {
		if strings.Contains(out, stale) {
			t.Fatalf("rendered manifest still contains stale site value %q", stale)
		}
	}
	for _, want := range []string{
		"name: ovn-nb",
		"name: ovn-northd",
		"name: ovn-sb",
		"/kc-networking/start-db.sh &",
		"/kc-networking/kc-networking-leader-checker --probeInterval=\"${OVN_LEADER_PROBE_INTERVAL}\"",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("rendered manifest missing KCN leader fix %q", want)
		}
	}
	for _, stale := range []string{"name: kcn-ovn-nb", "name: kcn-ovn-northd", "name: kcn-ovn-sb"} {
		if strings.Contains(out, stale) {
			t.Fatalf("rendered manifest still contains stale OVN Service name %q", stale)
		}
	}
	if err := os.WriteFile(filepath.Join(t.TempDir(), "kcn-install.yaml"), []byte(out), 0o600); err != nil {
		t.Fatalf("write rendered manifest: %v", err)
	}
}

func TestANIEnvoyDoesNotCleanComponentState(t *testing.T) {
	tasksPath := filepath.Join("..", "..", "builtin", "core", "roles", "ani", "envoy", "tasks", "main.yaml")
	tasksData, err := os.ReadFile(tasksPath)
	if err != nil {
		t.Fatalf("read Envoy tasks: %v", err)
	}
	tasks := string(tasksData)
	for _, forbidden := range []string{"cleanup", "orphan", "LSP", "lsp-del"} {
		if strings.Contains(tasks, forbidden) {
			t.Fatalf("Envoy tasks still contain out-of-scope component-state repair %q", forbidden)
		}
	}

	scriptPath := filepath.Join("..", "..", "builtin", "core", "roles", "ani", "envoy", "templates", "cleanup-orphan-lsp.py")
	if _, err := os.Stat(scriptPath); !os.IsNotExist(err) {
		t.Fatalf("cleanup script must not exist; stat error=%v", err)
	}

	playbookPath := filepath.Join("..", "..", "builtin", "core", "playbooks", "create_cluster.yaml")
	playbook, err := os.ReadFile(playbookPath)
	if err != nil {
		t.Fatalf("read create-cluster playbook: %v", err)
	}
	for _, want := range []string{`grep -F "v2.3.4"`, `grep -F "1.4.3"`} {
		if !strings.Contains(string(playbook), want) {
			t.Fatalf("create-cluster playbook does not verify fixed runtime version %q", want)
		}
	}
}

func TestCodeAndArtifactBuildScriptsAreSeparate(t *testing.T) {
	root := filepath.Join("..", "..")
	read := func(name string) string {
		data, err := os.ReadFile(filepath.Join(root, "scripts", name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		return string(data)
	}
	code := read("build-code.sh")
	artifact := read("build-offline.sh")
	install := read("install.sh")
	verify := read("verify.sh")

	for _, want := range []string{
		`make -C "$ROOT" build-kk-dev`,
		`builtin/core/roles/ani/smoke/templates/probe.sh`,
		`"$OUTPUT/kk"`,
		`"$OUTPUT/install.sh"`,
		`"$OUTPUT/verify.sh"`,
		`"$OUTPUT/probe.sh"`,
	} {
		if !strings.Contains(code, want) {
			t.Fatalf("code build script missing %q", want)
		}
	}
	for _, want := range []string{
		`HAULER_BIN`,
		`REPOSITORY_ISO`,
		`KUBEKEY_ARTIFACT`,
		`HAULER_ARCHIVE`,
		`"$OUTPUT/bin/hauler"`,
		`"$OUTPUT/packages/kubekey-artifact.tgz"`,
		`"$OUTPUT/images/images.haul.tar.zst"`,
		`"$OUTPUT/repository/ubuntu-24.04-debs-amd64.iso"`,
	} {
		if !strings.Contains(artifact, want) {
			t.Fatalf("artifact build script missing %q", want)
		}
	}
	for _, forbidden := range []string{
		`make -C "$ROOT" build-kk-dev`,
		`install -m 0755 "$ROOT/scripts/install.sh"`,
		`install -m 0755 "$ROOT/scripts/verify.sh"`,
		`builtin/core/roles/ani/smoke/templates/probe.sh`,
		`mkdir -p "$OUTPUT/manifests"`,
		`cp -R "$ROOT/builtin/core/roles/ani`,
	} {
		if strings.Contains(artifact, forbidden) {
			t.Fatalf("artifact build script still builds or copies code release %q", forbidden)
		}
	}
	if !strings.Contains(install, `KK="$ROOT/kk"`) || !strings.Contains(install, `--package-root "$ARTIFACT_ROOT"`) {
		t.Fatal("install wrapper is not bound to the independent code release and explicit artifact root")
	}
	for _, want := range []string{`PROBE="$ROOT/probe.sh"`, `IMAGE_TABLE="$ARTIFACT_ROOT/images/images.tsv"`, `"/var/lib/ani-installer/$CLUSTER_NAME/logs/verify-`} {
		if !strings.Contains(verify, want) {
			t.Fatalf("verify script missing %q", want)
		}
	}
}

func TestDebianChronyPackageCheckUsesDpkg(t *testing.T) {
	path := filepath.Join("..", "..", "builtin", "core", "roles", "native", "repository", "tasks", "install_package.yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read repository package task: %v", err)
	}
	text := string(data)
	if !strings.Contains(text, `dpkg-query -W -f='${Status}' chrony`) ||
		!strings.Contains(text, `"install ok installed"`) {
		t.Fatal("Debian chrony check must compare dpkg-query Status to install ok installed")
	}
	if strings.Contains(text, `systemctl show -p LoadState --value chrony.service`) {
		t.Fatal("Debian chrony check must not infer package installation from chrony.service LoadState")
	}
}

func TestExecutableReleaseFilesUseLF(t *testing.T) {
	root := filepath.Join("..", "..")
	for _, rel := range []string{
		"scripts/install.sh",
		"scripts/verify.sh",
		filepath.Join("builtin", "core", "roles", "ani", "smoke", "templates", "probe.sh"),
		filepath.Join("builtin", "core", "roles", "native", "init", "templates", "init-os.sh"),
	} {
		data, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			t.Fatalf("read executable %s: %v", rel, err)
		}
		content := string(data)
		if !strings.HasPrefix(content, "#!/usr/bin/env bash\n") {
			t.Fatalf("executable %s must start with an LF-terminated bash shebang", rel)
		}
		if strings.Contains(content, "\r") {
			t.Fatalf("executable %s contains CRLF bytes", rel)
		}
	}
}
