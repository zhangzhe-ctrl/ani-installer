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
				"docker.changqingyun.cn/kubercloud/kc-networking:dev": "192.0.2.11:5000/kubercloud/kc-networking:dev",
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
	// fix2 (2026-09-21): upstream dropped ConfigMap kcn-config entirely, so
	// 33.3.14.0/23 / 33.3.96.0/19 / 192.1.1.0/24 no longer exist even as
	// upstream hardcoded values; the checks stay as guards since the locally
	// restored ConfigMap must render injected values only.
	for _, stale := range []string{
		"33.3.1.201", "33.3.1.202", "33.3.1.203", "33.3.64.0/19",
		"33.3.14.0/23", "33.3.96.0/19", "192.1.1.0/24",
		"imagePullPolicy: Always",
	} {
		if strings.Contains(out, stale) {
			t.Fatalf("rendered manifest still contains stale site value %q", stale)
		}
	}
	// 2026-09-21 kcn dev fix2 upgrade: upstream ships three new CRDs
	// (learnedroutes / transitrouters / vpcattachments) alongside the dev
	// shapes already asserted below (kcn- prefixed OVN Services, start-db.sh).
	for _, want := range []string{
		"name: learnedroutes.networking.kubercloud.com",
		"name: transitrouters.networking.kubercloud.com",
		"name: vpcattachments.networking.kubercloud.com",
		"name: kcn-ovn-nb",
		"name: kcn-ovn-northd",
		"name: kcn-ovn-sb",
		"- /kc-networking/start-db.sh",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("rendered manifest missing KCN dev expectation %q", want)
		}
	}
	// The two-space indent pins the stale check to the Service metadata name;
	// the dev port names ("- name: ovn-nb") must stay bare and must not trip.
	for _, stale := range []string{
		"  name: ovn-nb", "  name: ovn-northd", "  name: ovn-sb",
		"start-db.sh &", "kc-networking-leader-checker",
	} {
		if strings.Contains(out, stale) {
			t.Fatalf("rendered manifest still contains stale v0.6.2 Service name or hand-patched leader fix %q", stale)
		}
	}
	if err := os.WriteFile(filepath.Join(t.TempDir(), "kcn-install.yaml"), []byte(out), 0o600); err != nil {
		t.Fatalf("write rendered manifest: %v", err)
	}
}

func TestKubeOVNManifestTemplateUsesSiteInputs(t *testing.T) {
	path := filepath.Join("..", "..", "builtin", "core", "roles", "ani", "kubeovn", "templates", "kubeovn-install.yaml")
	data := map[string]any{
		"ani": map[string]any{
			"images": map[string]string{
				"docker.io/kubeovn/kube-ovn:v1.16.6":        "192.0.2.11:5000/kubeovn/kube-ovn:v1.16.6",
				"docker.io/kubeovn/vpc-nat-gateway:v1.16.6": "192.0.2.11:5000/kubeovn/vpc-nat-gateway:v1.16.6",
			},
			"network": map[string]any{
				"pod_cidr":             "10.16.0.0/16",
				"service_cidr":         "10.96.0.0/16",
				"management_interface": "ens34",
				// R10/A05: gateway and join network render from the resolved
				// site values, never from hardcoded literals.
				"kubeovn": map[string]any{
					"default_gateway": "10.16.0.1",
					"join_cidr":       "172.19.0.0/16",
				},
			},
		},
	}

	tmpl, err := template.New("kubeovn-install.yaml").ParseFiles(path)
	if err != nil {
		t.Fatalf("parse kubeovn template: %v", err)
	}
	builder := &strings.Builder{}
	if err := tmpl.Execute(builder, data); err != nil {
		t.Fatalf("execute kubeovn template: %v", err)
	}
	out := builder.String()

	for _, want := range []string{
		"image: 192.0.2.11:5000/kubeovn/kube-ovn:v1.16.6",
		"image: 192.0.2.11:5000/kubeovn/vpc-nat-gateway:v1.16.6",
		"--image=192.0.2.11:5000/kubeovn/kube-ovn:v1.16.6",
		"--default-cidr=10.16.0.0/16",
		"--default-gateway=10.16.0.1",
		"--node-switch-cidr=172.19.0.0/16",
		"--service-cluster-ip-range=10.96.0.0/16",
		"--iface=ens34",
		"--default-interface-name=ens34",
		// every workload of the v1.16.6 material, all in kube-system
		"name: ovn-central", "name: ovs-ovn", "name: kube-ovn-controller",
		"name: kube-ovn-cni", "name: kube-ovn-monitor", "name: kube-ovn-pinger",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("rendered manifest missing %q", want)
		}
	}
	// No rendered image value may still point at the upstream registry (the
	// template header comment and the lookup keys legitimately mention the
	// originals), and the official SVC_CIDR default (10.96.0.0/12) must be
	// gone in favor of the site service_cidr. The gateway/join args must be
	// templated: a missing context key would render "<no value>" instead of a
	// silent literal.
	for _, stale := range []string{
		"image: docker.io/kubeovn", "--image=docker.io/kubeovn", "10.96.0.0/12", "KubeOVNMaterialsPending",
		"--iface=\n", "--default-interface-name=\n",
		"--default-gateway=<no value>", "--node-switch-cidr=<no value>",
	} {
		if strings.Contains(out, stale) {
			t.Fatalf("rendered manifest still contains stale value %q", stale)
		}
	}
	// R10/A05: the hardcoded literals must be gone from the template itself,
	// not just overridden at render time.
	source, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read kubeovn template source: %v", err)
	}
	for _, literal := range []string{
		"- --default-gateway=10.16.0.1",
		"- --node-switch-cidr=172.19.0.0/16",
	} {
		if strings.Contains(string(source), literal) {
			t.Fatalf("kubeovn template source still hardcodes %q; it must come from the site config", literal)
		}
	}
	if err := os.WriteFile(filepath.Join(t.TempDir(), "kubeovn-install.yaml"), []byte(out), 0o600); err != nil {
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
	// Every per-component verification script rides along inside the kk builtin
	// resources, so it needs the same LF guarantee.
	matches, err := filepath.Glob(filepath.Join(root, "builtin", "core", "roles", "ani", "*", "templates", "*.sh"))
	if err != nil {
		t.Fatalf("glob component scripts: %v", err)
	}
	if len(matches) == 0 {
		t.Fatal("no component verification scripts found")
	}
	for _, path := range matches {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read component script %s: %v", path, err)
		}
		if strings.Contains(string(data), "\r") {
			t.Fatalf("component script %s contains CRLF bytes", path)
		}
	}
}
