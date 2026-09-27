/*
Copyright 2026 The KubeSphere Contributors.
Licensed under Apache License, Version 2.0.
*/

package connector

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"sync"
	"testing"

	"golang.org/x/crypto/ssh"

	_const "github.com/kubesphere/kubekey/v4/pkg/const"
	"k8s.io/utils/exec"
)

// ---------------------------------------------------------------------------
// C07: what actually crosses the execution boundary.
//
// The installer's run context has to reach the cluster-calling commands the same
// way on the installer node and on the two SSH nodes. That is only decidable by
// reading the two connectors that really run the commands, because they are not
// symmetric:
//
//   - localConnector.ExecuteCommand runs `sudo -SE <shell> -c <cmd>` and calls
//     SetEnv(append(os.Environ(), "SUDO_USER=...")) — the parent kk environment
//     DOES come along.
//   - sshConnector.ExecuteCommand sends one `exec` payload built by
//     buildSudoCommand, and nothing else: no `env` request is ever made, so a
//     variable that only exists in the parent's environment never arrives.
//
// A context that travels in the environment therefore works locally and silently
// fails (or works against a different cluster) over SSH — which is why the ANI
// roles carry the kubeconfig inside the rendered command text. These tests prove
// both halves of that statement on the production connectors.
// ---------------------------------------------------------------------------

// recordedCmd is one command handed to a fake exec.Interface, with the exact
// argv and environment the connector asked for.
type recordedCmd struct {
	path string
	args []string
	env  []string
}

func (r recordedCmd) lookup(name string) (string, bool) {
	for _, entry := range r.env {
		if value, ok := strings.CutPrefix(entry, name+"="); ok {
			return value, true
		}
	}
	return "", false
}

// fakeExec captures what the local connector built instead of running it, so
// this test never needs sudo and never executes anything.
type fakeExec struct {
	mu   sync.Mutex
	cmds []recordedCmd
	out  string
}

func (f *fakeExec) record(c recordedCmd) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cmds = append(f.cmds, c)
}

func (f *fakeExec) recorded() []recordedCmd {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]recordedCmd(nil), f.cmds...)
}

func (f *fakeExec) Command(cmd string, args ...string) exec.Cmd {
	return f.newCmd(cmd, args, nil)
}

func (f *fakeExec) CommandContext(_ context.Context, cmd string, args ...string) exec.Cmd {
	return f.newCmd(cmd, args, nil)
}

func (f *fakeExec) LookPath(file string) (string, error) { return file, nil }

func (f *fakeExec) newCmd(cmd string, args []string, _ error) exec.Cmd {
	return &fakeCmd{parent: f, rec: recordedCmd{path: cmd, args: append([]string(nil), args...)}}
}

type fakeCmd struct {
	parent *fakeExec
	rec    recordedCmd
	stdout io.Writer
	stderr io.Writer
}

func (c *fakeCmd) Run() error {
	c.parent.record(c.rec)
	return nil
}
func (c *fakeCmd) CombinedOutput() ([]byte, error) { return []byte(c.parent.out), nil }
func (c *fakeCmd) Output() ([]byte, error)         { return []byte(c.parent.out), nil }
func (c *fakeCmd) SetDir(string)                   {}
func (c *fakeCmd) SetStdin(io.Reader)              {}
func (c *fakeCmd) SetStdout(w io.Writer)           { c.stdout = w }
func (c *fakeCmd) SetStderr(w io.Writer)           { c.stderr = w }
func (c *fakeCmd) SetEnv(env []string)             { c.rec.env = append([]string(nil), env...) }
func (c *fakeCmd) StdoutPipe() (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader(c.parent.out)), nil
}
func (c *fakeCmd) StderrPipe() (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader("")), nil
}
func (c *fakeCmd) Start() error { return nil }
func (c *fakeCmd) Wait() error  { return nil }
func (c *fakeCmd) Stop()        {}

// c07SSHServer is a localhost SSH stand-in that records every channel request
// and the exact exec payload it received. It answers the Init() shell probe the
// same way the R12 harness does.
type c07SSHServer struct {
	t        *testing.T
	listener net.Listener
	config   *ssh.ServerConfig

	mu        sync.Mutex
	requests  []string
	execCount int
	execs     []string
}

func newC07SSHServer(t *testing.T) *c07SSHServer {
	t.Helper()
	_, privKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("host key: %v", err)
	}
	signer, err := ssh.NewSignerFromKey(privKey)
	if err != nil {
		t.Fatalf("host signer: %v", err)
	}
	s := &c07SSHServer{t: t}
	s.config = &ssh.ServerConfig{
		PasswordCallback: func(ssh.ConnMetadata, []byte) (*ssh.Permissions, error) {
			return &ssh.Permissions{}, nil
		},
	}
	s.config.AddHostKey(signer)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	s.listener = listener
	t.Cleanup(func() { _ = listener.Close() })
	go s.serve()
	return s
}

func (s *c07SSHServer) note(kind string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.requests = append(s.requests, kind)
}

func (s *c07SSHServer) seen(kind string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, r := range s.requests {
		if r == kind {
			n++
		}
	}
	return n
}

func (s *c07SSHServer) executed() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.execs...)
}

func (s *c07SSHServer) serve() {
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			return
		}
		go s.handleConn(conn)
	}
}

func (s *c07SSHServer) handleConn(conn net.Conn) {
	serverConn, channels, requests, err := ssh.NewServerConn(conn, s.config)
	if err != nil {
		return
	}
	defer serverConn.Close()
	go func() {
		for req := range requests {
			s.note("global-" + req.Type)
		}
	}()
	for newChannel := range channels {
		if newChannel.ChannelType() != "session" {
			_ = newChannel.Reject(ssh.UnknownChannelType, "unsupported")
			continue
		}
		channel, reqs, err := newChannel.Accept()
		if err != nil {
			continue
		}
		go s.handleSession(channel, reqs)
	}
}

func (s *c07SSHServer) handleSession(ch ssh.Channel, requests <-chan *ssh.Request) {
	defer ch.Close()
	for req := range requests {
		s.note(req.Type)
		switch req.Type {
		case "exec":
			payload := ""
			if len(req.Payload) > 4 {
				payload = string(req.Payload[4:])
			}
			s.mu.Lock()
			s.execs = append(s.execs, payload)
			s.execCount++
			s.mu.Unlock()
			_ = req.Reply(true, nil)
			if strings.Contains(payload, "echo $SHELL") {
				_, _ = ch.Write([]byte("/bin/bash\n"))
			} else {
				// Echo back the received command so the assertion below can also
				// check the round trip really went through the SSH channel.
				_, _ = ch.Write([]byte(payload + "\n"))
			}
			_, _ = ch.SendRequest("exit-status", false,
				ssh.Marshal(struct{ Code uint32 }{0}))
			return
		default:
			if req.WantReply {
				_ = req.Reply(true, nil)
			}
		}
	}
}

func newC07SSHConnector(t *testing.T, s *c07SSHServer) *sshConnector {
	t.Helper()
	host, portStr, err := net.SplitHostPort(s.listener.Addr().String())
	if err != nil {
		t.Fatalf("split addr: %v", err)
	}
	var port int
	if _, err := fmt.Sscanf(portStr, "%d", &port); err != nil {
		t.Fatalf("parse port: %v", err)
	}
	c := newSSHConnector(t.TempDir(), host, map[string]any{
		_const.VariableConnector: map[string]any{
			_const.VariableConnectorPort:     port,
			_const.VariableConnectorUser:     "ubuntu",
			_const.VariableConnectorPassword: "stand-in-password",
		},
	})
	if err := c.Init(context.Background()); err != nil {
		t.Fatalf("ssh connector init: %v", err)
	}
	t.Cleanup(func() { _ = c.Close(context.Background()) })
	return c
}

// The command text one ANI role renders to: the run's kubeconfig is inside the
// command, because that is the only form both connectors carry unchanged.
const c07RenderedCommand = `set -euo pipefail
KUBECONFIG="/run-scope/cluster-A.conf" kubectl -n ani-platform get svc nats -o jsonpath='{.spec.clusterIP}'`

// T-C07-1: the two execution chains must hand the remote side the same command
// text, and the SSH chain must not drag the parent process's environment along.
func TestC07_LocalAndSSHChainsCarryTheSameCommandAndNoInheritedTarget(t *testing.T) {
	// A dev-machine environment that would silently repoint a cluster call if
	// any layer resolved its context from the environment instead of the run.
	t.Setenv("KUBECONFIG", "/parent-process/development.conf")
	t.Setenv("ANI_VERIFY_KUBECONFIG", "/parent-process/development.conf")
	t.Setenv("HOME", "/parent-process/home")

	s := newC07SSHServer(t)
	sshConn := newC07SSHConnector(t, s)
	stdout, _, err := sshConn.ExecuteCommand(context.Background(), c07RenderedCommand)
	if err != nil {
		t.Fatalf("ssh execute: %v", err)
	}
	execs := s.executed()
	if len(execs) != 2 {
		t.Fatalf("expected the shell probe plus one command, got %d exec payloads: %v", len(execs), execs)
	}
	remotePayload := execs[1]

	// Nothing but the wrapped sudo line is sent: an `env` request is how a
	// client asks the server to export a variable, and this connector makes none.
	if n := s.seen("env"); n != 0 {
		t.Fatalf("the ssh connector forwarded %d environment variables to the remote host", n)
	}
	for _, leaked := range []string{"/parent-process/development.conf", "/parent-process/home"} {
		if strings.Contains(remotePayload, leaked) {
			t.Fatalf("the parent process's %s crossed the ssh boundary inside the exec payload: %s", leaked, remotePayload)
		}
	}
	if !strings.Contains(remotePayload, `KUBECONFIG="/run-scope/cluster-A.conf"`) {
		t.Fatalf("the run's own kubeconfig pin did not survive into the ssh exec payload: %s", remotePayload)
	}
	if !strings.Contains(string(stdout), `KUBECONFIG="/run-scope/cluster-A.conf"`) {
		t.Fatalf("the command did not round-trip through the ssh channel: %q", string(stdout))
	}

	// The local connector is the one that DOES inherit the parent environment;
	// that asymmetry is precisely why a run context carried only in the
	// environment would work here and not over ssh.
	local := newLocalConnector(t.TempDir(), map[string]any{
		_const.VariableConnector: map[string]any{_const.VariableConnectorUser: "ubuntu"},
	})
	fake := &fakeExec{}
	local.Cmd = fake
	if err := local.Init(context.Background()); err != nil {
		t.Fatalf("local connector init: %v", err)
	}
	if _, _, err := local.ExecuteCommand(context.Background(), c07RenderedCommand); err != nil {
		t.Fatalf("local execute: %v", err)
	}
	ran := fake.recorded()
	if len(ran) != 1 {
		t.Fatalf("expected one local command, got %d", len(ran))
	}
	if ran[0].path != "sudo" {
		t.Fatalf("the local chain does not run through sudo like the ssh chain does: %q", ran[0].path)
	}
	localScript := ""
	for i, arg := range ran[0].args {
		if arg == "-c" && i+1 < len(ran[0].args) {
			localScript = ran[0].args[i+1]
		}
	}
	if localScript == "" {
		t.Fatalf("no -c script was handed to the local shell: %#v", ran[0].args)
	}
	if localScript != c07RenderedCommand {
		t.Fatalf("the local chain did not run the command text unchanged:\n got: %q\nwant: %q", localScript, c07RenderedCommand)
	}
	// The ssh wrapper carries the same script verbatim inside its -c quoting, so
	// both chains execute byte-identical text.
	sshScript := extractQuotedScript(t, remotePayload)
	if sshScript != localScript {
		t.Fatalf("the two execution chains ran different command text:\nlocal: %q\n ssh:  %q", localScript, sshScript)
	}
	// The proof of the asymmetry: the local environment really does carry the
	// parent's stale pin, and it is the command text, not the environment, that
	// decides the target.
	inherited, ok := ran[0].lookup("KUBECONFIG")
	if !ok || inherited != "/parent-process/development.conf" {
		t.Fatalf("the local connector was expected to pass the parent KUBECONFIG through, got %q (present=%v)", inherited, ok)
	}
	if _, ok := ran[0].lookup("SUDO_USER"); !ok {
		t.Fatal("the local connector did not set SUDO_USER")
	}
}

// extractQuotedScript pulls the command text back out of buildSudoCommand's
// `sudo -E <shell> -c "$(cat <<'KUBEKEY_EOF' ... KUBEKEY_EOF)"` wrapper.
func extractQuotedScript(t *testing.T, payload string) string {
	t.Helper()
	const (
		open  = "<< 'KUBEKEY_EOF'\n"
		close = "\nKUBEKEY_EOF\n"
	)
	start := strings.Index(payload, open)
	if start < 0 {
		t.Fatalf("no heredoc wrapper found in: %s", payload)
	}
	rest := payload[start+len(open):]
	end := strings.Index(rest, close)
	if end < 0 {
		t.Fatalf("unterminated heredoc wrapper in: %s", payload)
	}
	return rest[:end]
}

// T-C07-2: the sudo wrapper must not ask the remote shell to resolve anything
// from a login environment that the installer does not control.
func TestC07_SSHPayloadCarriesNoFallbackCredentialLookup(t *testing.T) {
	s := newC07SSHServer(t)
	c := newC07SSHConnector(t, s)
	if _, _, err := c.ExecuteCommand(context.Background(), c07RenderedCommand); err != nil {
		t.Fatalf("ssh execute: %v", err)
	}
	payload := strings.Join(s.executed(), "\n")
	if strings.Contains(payload, ".kube/config") {
		t.Fatalf("the ssh payload reaches a default kubeconfig location: %s", payload)
	}
	if !strings.Contains(payload, "sudo -E") {
		t.Fatalf("expected the production sudo wrapper, got: %s", payload)
	}
	// `sudo -E` preserves the SESSION environment, and nothing was sent into it,
	// so an unset target cannot quietly resolve on the remote either.
	if n := s.seen("env"); n != 0 {
		t.Fatalf("%d env requests were sent into the ssh session", n)
	}
}

// T-C07-3: a remote that is simply missing the pinned file must not be papered
// over — this is the connector-level half of "fail before the first write".
func TestC07_RemoteHostWithoutThePinnedKubeconfigIsStillGivenThePinnedPath(t *testing.T) {
	s := newC07SSHServer(t)
	c := newC07SSHConnector(t, s)
	missing := "/no/such/directory/on-the-remote.conf"
	command := fmt.Sprintf("test -f %q && echo present || echo absent", missing)
	if _, _, err := c.ExecuteCommand(context.Background(), command); err != nil {
		t.Fatalf("ssh execute: %v", err)
	}
	payload := s.executed()
	last := payload[len(payload)-1]
	if !strings.Contains(last, missing) {
		t.Fatalf("the connector substituted a different kubeconfig path: %s", last)
	}
	if strings.Contains(last, "/etc/kubernetes/admin.conf") {
		t.Fatalf("the connector reached for the installer's admin.conf on the remote: %s", last)
	}
}

// Guard: the fake must be honest about the environment it reports. If os.Environ
// ever stops containing the value t.Setenv wrote, the assertion above would pass
// for the wrong reason.
func TestC07_FakeExecReportsTheEnvironmentItWasGiven(t *testing.T) {
	const marker = "ANI_C07_ENV_GUARD"
	value := fmt.Sprintf("probe-%d", os.Getpid())
	t.Setenv(marker, value)
	local := newLocalConnector(t.TempDir(), map[string]any{})
	fake := &fakeExec{}
	local.Cmd = fake
	if err := local.Init(context.Background()); err != nil {
		t.Fatalf("init: %v", err)
	}
	if _, _, err := local.ExecuteCommand(context.Background(), "true"); err != nil {
		t.Fatalf("execute: %v", err)
	}
	got, ok := fake.recorded()[0].lookup(marker)
	if !ok || got != value {
		t.Fatalf("the recorded environment does not contain what the parent process has (%q=%q, present=%v); this file's asymmetry claim is unproven", marker, got, ok)
	}
	if len(fake.recorded()[0].env) < 2 {
		t.Fatalf("the local connector set an empty environment: %#v", fake.recorded()[0].env)
	}
}
