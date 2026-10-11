package ani

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func fluentQueryHelpers(t *testing.T, suffix string, env ...string) ([]byte, error) {
	t.Helper()
	script := g6RenderVerify(t, "fluent-bit")
	dir := t.TempDir()
	pin := filepath.Join(dir, "kubeconfig")
	if err := os.WriteFile(pin, []byte("apiVersion: v1\nkind: Config\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("bash", "-c", "source \"$1\"; BACKEND=opensearch; CLIENT_POD=owned-client; "+suffix, "query-test", script)
	cmd.Env = append(os.Environ(), "ANI_VERIFY_LIB_ONLY=1", "ANI_VERIFY_KUBECONFIG="+pin, "KUBECONFIG="+pin, "ANI_VERIFY_OUTPUT_DIR="+filepath.Join(dir, "out"))
	cmd.Env = append(cmd.Env, env...)
	return cmd.CombinedOutput()
}

func TestFluentQuerySecretProjectionKeepsCredentialsOutOfArguments(t *testing.T) {
	out, err := fluentQueryHelpers(t, "opensearch_client_overrides")
	if err != nil {
		t.Fatalf("rendered client projection: %v: %s", err, out)
	}
	var pod struct {
		Spec struct {
			Automount bool `json:"automountServiceAccountToken"`
			Volumes   []struct {
				Secret struct {
					Name  string `json:"secretName"`
					Mode  int    `json:"defaultMode"`
					Items []struct {
						Key  string `json:"key"`
						Path string `json:"path"`
					} `json:"items"`
				} `json:"secret"`
			} `json:"volumes"`
			Containers []struct {
				Name    string   `json:"name"`
				Command []string `json:"command"`
				Mounts  []struct {
					Path     string `json:"mountPath"`
					ReadOnly bool   `json:"readOnly"`
				} `json:"volumeMounts"`
			} `json:"containers"`
		} `json:"spec"`
	}
	if err := json.Unmarshal(out, &pod); err != nil {
		t.Fatal(err)
	}
	if pod.Spec.Automount || len(pod.Spec.Volumes) != 3 || len(pod.Spec.Containers) != 1 {
		t.Fatalf("unexpected client scope: %s", out)
	}
	want := map[string]string{"ani-opensearch-node-tls": "ca.crt", "ani-opensearch-fluent-bit": "username,password", "ani-opensearch-admin": "username,password"}
	for _, v := range pod.Spec.Volumes {
		var keys []string
		for _, item := range v.Secret.Items {
			keys = append(keys, item.Key)
			if item.Key != item.Path {
				t.Fatal("Secret key path differs")
			}
		}
		if want[v.Secret.Name] != strings.Join(keys, ",") || v.Secret.Mode != 0400 {
			t.Fatalf("unexpected projected keys: %s", out)
		}
		delete(want, v.Secret.Name)
	}
	if len(want) != 0 || strings.Contains(string(out), "tls.key") {
		t.Fatal("client projection exposes unintended material")
	}
	container := pod.Spec.Containers[0]
	if container.Name != "owned-client" || len(container.Mounts) != 3 {
		t.Fatalf("invalid generated client: %s", out)
	}
	for _, mount := range container.Mounts {
		if !mount.ReadOnly || !strings.HasPrefix(mount.Path, "/run/ani-log-query/") {
			t.Fatalf("unprotected mount: %s", out)
		}
	}
}

func TestFluentQueryRejectsEmptyCertificateBeforePolling(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"ca", "collector", "admin", "bin"} {
		if err := os.Mkdir(filepath.Join(root, name), 0700); err != nil {
			t.Fatal(err)
		}
	}
	for _, directory := range []string{"collector", "admin"} {
		for _, key := range []string{"username", "password"} {
			if err := os.WriteFile(filepath.Join(root, directory, key), []byte("fixture-private-value"), 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
	ca := filepath.Join(root, "ca", "ca.crt")
	cert := exec.Command("openssl", "req", "-x509", "-newkey", "ec", "-pkeyopt", "ec_paramgen_curve:prime256v1", "-nodes", "-keyout", filepath.Join(root, "test.key"), "-out", ca, "-subj", "/CN=ANI-query-input-test", "-days", "1")
	if out, err := cert.CombinedOutput(); err != nil {
		t.Fatalf("certificate fixture: %v: %s", err, out)
	}
	valid, err := os.ReadFile(ca)
	if err != nil {
		t.Fatal(err)
	}
	// The double replaces only kubectl exec and the Pod filesystem mapping.
	// The production Python certificate parser and input checks execute unchanged.
	fake := "#!/usr/bin/env python3\nimport os,subprocess,sys\na=sys.argv[1:];a=a[a.index('--')+1:];assert a[:2]==['python3','-c'];a[2]=a[2].replace('/run/ani-log-query',os.environ['QUERY_FIXTURE_ROOT']);sys.exit(subprocess.run(a).returncode)\n"
	if err := os.WriteFile(filepath.Join(root, "bin", "kubectl"), []byte(fake), 0700); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		data []byte
		pass bool
	}{{"valid", valid, true}, {"empty", nil, false}, {"malformed", []byte("not a certificate"), false}} {
		t.Run(tc.name, func(t *testing.T) {
			if err := os.WriteFile(ca, tc.data, 0600); err != nil {
				t.Fatal(err)
			}
			out, err := fluentQueryHelpers(t, "validate_opensearch_client_inputs", "PATH="+filepath.Join(root, "bin")+":"+os.Getenv("PATH"), "QUERY_FIXTURE_ROOT="+root)
			if (err == nil) != tc.pass {
				t.Fatalf("actual TLS input verdict: %v: %s", err, out)
			}
			if strings.Contains(string(out), "fixture-private-value") {
				t.Fatalf("credential bytes leaked: %s", out)
			}
			if tc.pass && !strings.Contains(string(out), "OpenSearch query inputs verified") {
				t.Fatalf("validation did not run: %s", out)
			}
		})
	}
}
