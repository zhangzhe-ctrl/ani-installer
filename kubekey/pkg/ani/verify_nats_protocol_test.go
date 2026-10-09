package ani

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	batchv1 "k8s.io/api/batch/v1"
	"sigs.k8s.io/yaml"
)

// Execute the shell program that actually leaves protocolWrite/ReadBack in
// the typed Job. Only kubectl and the external NATS CLI/server are doubled.
// In particular, PubAck is on stderr, as in the locked CLI; no success marker
// is supplied by the Kubernetes double.
func TestNATSAcceptanceExecutesNoninteractiveProtocol(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("ANI_NATS_TEST_DIR", dir)
	t.Setenv("ANI_NATS_KUBECTL_HELPER", "1")
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("ANI_NATS_TEST_EXECUTABLE", executable)
	for name, contents := range map[string]string{
		"kubectl": "#!/bin/sh\nexec \"$ANI_NATS_TEST_EXECUTABLE\" -test.run=^TestNATSAcceptanceKubectlHelper$ -- \"$@\"\n",
		"nats": `#!/usr/bin/env python3
import json, os, pathlib, sys
args = sys.argv[1:]
assert args[:2] == ['-s', 'nats://nats.ani-platform.svc:4222'], args
assert os.environ['NATS_TOKEN'] == 'external boundary credential'
args = args[2:]
root = pathlib.Path(os.environ['ANI_NATS_TEST_DIR'])
state = root / 'messages.json'
messages = json.loads(state.read_text()) if state.exists() else {}
mode = os.environ.get('ANI_NATS_TEST_FAILURE', '')
if args[:2] == ['stream', 'add']:
    # stream add --force is invalid in the locked CLI 0.4.0. JSON avoids
    # both that flag and interactive defaults.
    assert args[2] == '--config' and pathlib.Path(args[3]).name == 'stream.json' and len(args) == 4, args
    conf = json.loads(pathlib.Path(args[3]).read_text())
    assert conf['name'].startswith('ANI_ACCEPT_') and conf['storage'] == 'file'
    token = conf['name'].removeprefix('ANI_ACCEPT_')
    assert conf['subjects'] == ['ani.accept.' + token]
    assert conf['num_replicas'] == 1 and conf['retention'] == 'limits'
    assert conf['name'] not in messages, 'another attempt reused a stream'
    messages[conf['name']] = None
    state.write_text(json.dumps(messages))
elif args[:2] == ['pub', '-J']:
    stream = 'ANI_ACCEPT_' + args[2].removeprefix('ani.accept.')
    assert stream in messages and args[3] == stream.removeprefix('ANI_ACCEPT_')
    messages[stream] = args[3]
    state.write_text(json.dumps(messages))
    ack_stream = 'FOREIGN_STREAM' if mode == 'foreign-ack' else stream
    print('Stored in Stream: ' + ack_stream + ' Sequence: 1', file=sys.stderr)
elif args[:2] == ['consumer', 'add']:
    assert args[2] in messages and args[3] == '--config' and pathlib.Path(args[4]).name == 'consumer.json' and len(args) == 5
    if mode == 'consumer-rejected':
        sys.exit(3)
    conf = json.loads(pathlib.Path(args[4]).read_text())
    token = args[2].removeprefix('ANI_ACCEPT_')
    assert conf['durable_name'] == 'ani-verify-' + token
    assert conf['filter_subject'] == 'ani.accept.' + token
    assert conf['ack_policy'] == 'explicit' and conf['deliver_policy'] == 'all'
elif args[:2] == ['consumer', 'next']:
    assert args[2] in messages and args[4:] == ['--raw', '--ack', '--count', '1']
    token = args[2].removeprefix('ANI_ACCEPT_')
    assert args[3] == 'ani-verify-' + token
    print('foreign-value' if mode == 'wrong-message' else messages[args[2]])
else:
    raise AssertionError('unexpected CLI operation: ' + repr(args))
`,
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(contents), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	runner := kubectlRunner{bin: filepath.Join(dir, "kubectl"), kubeconfig: "external-boundary"}
	target := acceptanceTarget{Protocol: acceptanceNATSJetStream, Namespace: "ani-platform"}
	for _, token := range []string{"aabbccdd", "11223344"} {
		if detail, ok := protocolWrite(context.Background(), runner, target, "registry", token); !ok {
			t.Fatalf("write %s: %s", token, detail)
		}
		if detail, ok := protocolReadBack(context.Background(), runner, target, "registry", token); !ok {
			t.Fatalf("read %s: %s", token, detail)
		}
	}
	t.Setenv("ANI_NATS_TEST_FAILURE", "foreign-ack")
	if detail, ok := protocolWrite(context.Background(), runner, target, "registry", "55667788"); ok {
		t.Fatalf("foreign stream PubAck passed: %s", detail)
	}
	for _, failure := range []string{"consumer-rejected", "wrong-message"} {
		t.Setenv("ANI_NATS_TEST_FAILURE", failure)
		if detail, ok := protocolReadBack(context.Background(), runner, target, "registry", "aabbccdd"); ok {
			t.Fatalf("%s passed: %s", failure, detail)
		}
	}
}

func TestNATSAcceptanceKubectlHelper(t *testing.T) {
	if os.Getenv("ANI_NATS_KUBECTL_HELPER") != "1" {
		return
	}
	separator := 0
	for i, arg := range os.Args {
		if arg == "--" {
			separator = i + 1
			break
		}
	}
	if separator == 0 {
		return // The parent test process is not a kubectl invocation.
	}
	args := os.Args[separator:]
	args = args[3:] // kubeconfig and request timeout are supplied by the runner.
	logPath := filepath.Join(os.Getenv("ANI_NATS_TEST_DIR"), "job.log")
	switch args[0] {
	case "apply":
		data, err := os.ReadFile(args[len(args)-1])
		if err != nil {
			panic(err)
		}
		var job batchv1.Job
		if err := yaml.Unmarshal(data, &job); err != nil {
			panic(err)
		}
		c := job.Spec.Template.Spec.Containers[0]
		if len(c.Env) != 1 || c.Env[0].Name != "NATS_TOKEN" || c.Env[0].Value != "" ||
			c.Env[0].ValueFrom.SecretKeyRef.Name != "ani-nats-auth" || c.Env[0].ValueFrom.SecretKeyRef.Key != "token" {
			panic("credential lost its Secret reference")
		}
		// Each real Job has its own container /tmp. Map that filesystem boundary
		// into this test's private directory when running its program locally.
		command := append([]string(nil), c.Command...)
		command[2] = strings.ReplaceAll(command[2], "/tmp/", os.Getenv("ANI_NATS_TEST_DIR")+"/")
		cmd := exec.Command(command[0], command[1:]...)
		cmd.Env = append(os.Environ(), "NATS_TOKEN=external boundary credential")
		output, err := cmd.CombinedOutput()
		if err := os.WriteFile(logPath, output, 0o600); err != nil {
			panic(err)
		}
		if err != nil {
			_, _ = os.Stdout.Write(output)
			os.Exit(1)
		}
	case "logs":
		output, err := os.ReadFile(logPath)
		if err != nil {
			panic(err)
		}
		_, _ = os.Stdout.Write(output)
	case "wait", "delete":
	default:
		panic(strings.Join(args, " "))
	}
	os.Exit(0)
}
