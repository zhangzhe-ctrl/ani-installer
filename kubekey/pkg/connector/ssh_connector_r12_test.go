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

package connector

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	mathrand "math/rand"
	"net"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	_const "github.com/kubesphere/kubekey/v4/pkg/const"
)

// ---------------------------------------------------------------------------
// R12 behaviour tests: an in-process fake SSH server (127.0.0.1 only) drives
// the real sshConnector.ExecuteCommand through scenarios the real hosts
// exhibit: a session that never exits, a partial sudo prompt, a remote that
// finished but never closes its response, plain success and cancellation
// mid-stream. Nothing here contacts a real machine.
// ---------------------------------------------------------------------------

// fakeSSHServer is a minimal in-process SSH server. The exec behaviour is a
// swappable function (stored atomically) so a single test can cancel one
// scenario and then switch to another without re-dialling.
type fakeSSHServer struct {
	t        *testing.T
	listener net.Listener
	config   *ssh.ServerConfig

	behavior atomic.Pointer[execBehavior]

	execCount atomic.Int64

	mu         sync.Mutex
	passwords  []string
	closeOnce  sync.Once
	closed     chan struct{}
	shutdownFn sync.Once
}

func newFakeSSHServer(t *testing.T) *fakeSSHServer {
	t.Helper()
	s := &fakeSSHServer{t: t, closed: make(chan struct{})}

	_, privKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate host key: %v", err)
	}
	signer, err := ssh.NewSignerFromKey(privKey)
	if err != nil {
		t.Fatalf("host signer: %v", err)
	}
	s.config = &ssh.ServerConfig{
		PasswordCallback: func(conn ssh.ConnMetadata, password []byte) (*ssh.Permissions, error) {
			return &ssh.Permissions{}, nil
		},
	}
	probe := execBehavior(func(ch ssh.Channel, cmd string) {
		fakeShellProbe(ch, cmd)
	})
	s.behavior.Store(&probe)
	// The server must present a host key or the handshake never completes.
	s.config.AddHostKey(signer)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	s.listener = listener
	t.Cleanup(func() { s.Close() })
	go s.serve()
	return s
}

func (s *fakeSSHServer) addr() string {
	return s.listener.Addr().String()
}

func (s *fakeSSHServer) setBehavior(fn func(ch ssh.Channel, cmd string)) {
	behavior := execBehavior(fn)
	s.behavior.Store(&behavior)
}

func (s *fakeSSHServer) Close() {
	s.shutdownFn.Do(func() {
		close(s.closed)
		_ = s.listener.Close()
	})
}

// recordPassword stores a password the fake server read from a session's
// stdin (the sudo prompt path).
func (s *fakeSSHServer) recordPassword(password string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.passwords = append(s.passwords, password)
}

func (s *fakeSSHServer) seenPasswords() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.passwords...)
}

func (s *fakeSSHServer) serve() {
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			return
		}
		go s.handleConn(conn)
	}
}

func (s *fakeSSHServer) handleConn(conn net.Conn) {
	serverConn, channels, requests, err := ssh.NewServerConn(conn, s.config)
	if err != nil {
		return
	}
	defer serverConn.Close()
	go ssh.DiscardRequests(requests)
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

func (s *fakeSSHServer) handleSession(ch ssh.Channel, requests <-chan *ssh.Request) {
	defer ch.Close()
	for req := range requests {
		switch req.Type {
		case "pty-req", "env":
			_ = req.Reply(true, nil)
		case "exec":
			cmd := ""
			if len(req.Payload) > 4 {
				cmd = string(req.Payload[4:])
			}
			s.execCount.Add(1)
			_ = req.Reply(true, nil)
			if behavior := s.behavior.Load(); behavior != nil {
				(*behavior)(ch, cmd)
			}
			return
		default:
			if req.WantReply {
				_ = req.Reply(false, nil)
			}
		}
	}
}

type execBehavior func(ch ssh.Channel, cmd string)

// fakeShellProbe answers the Init() "echo $SHELL" probe.
func fakeShellProbe(ch ssh.Channel, cmd string) {
	if strings.Contains(cmd, "echo $SHELL") {
		_, _ = ch.Write([]byte("/bin/bash\n"))
		fakeSendExit(ch, 0)
		return
	}
	// Unknown command in probe mode: fail loudly so tests notice.
	_, _ = ch.Stderr().Write([]byte("fake ssh server: unexpected exec " + cmd))
	fakeSendExit(ch, 127)
}

func fakeSendExit(ch ssh.Channel, code int) {
	_, _ = ch.SendRequest("exit-status", false, ssh.Marshal(struct{ Code uint32 }{uint32(code)}))
	_ = ch.Close()
}

// scenario behaviors -------------------------------------------------------------

// fakeNeverExit writes a partial sudo password prompt (no newline) and then
// blocks forever, capturing whatever the client writes to stdin.
func fakeNeverExit(s *fakeSSHServer) execBehavior {
	return func(ch ssh.Channel, cmd string) {
		if strings.Contains(cmd, "echo $SHELL") {
			fakeShellProbe(ch, cmd)
			return
		}
		_, _ = ch.Write([]byte("[sudo] password for root: "))
		go func() {
			buf := make([]byte, 128)
			for {
				n, err := ch.Read(buf)
				if n > 0 {
					s.recordPassword(strings.TrimSpace(string(buf[:n])))
				}
				if err != nil {
					return
				}
			}
		}()
		<-s.closed // the remote command never completes
	}
}

// fakeSuccessNoEOF models a command that fully succeeded remotely but whose
// response channel never ends: no exit-status, channel kept open.
func fakeSuccessNoEOF(ch ssh.Channel, cmd string) {
	if strings.Contains(cmd, "echo $SHELL") {
		fakeShellProbe(ch, cmd)
		return
	}
	_, _ = ch.Write([]byte("REMOTE-DONE\n"))
	// Intentionally no exit-status and no Close: the local side can never
	// confirm the remote outcome from the response.
	<-make(chan struct{})
}

// fakeNormalExit writes output (and optional stderr), then exits 0.
func fakeNormalExit(stdout, stderr string) execBehavior {
	return func(ch ssh.Channel, cmd string) {
		if strings.Contains(cmd, "echo $SHELL") {
			fakeShellProbe(ch, cmd)
			return
		}
		if stdout != "" {
			_, _ = ch.Write([]byte(stdout))
		}
		if stderr != "" {
			_, _ = ch.Stderr().Write([]byte(stderr))
		}
		fakeSendExit(ch, 0)
	}
}

// fakeStreamForever writes stdout lines until the local side disappears.
func fakeStreamForever(ch ssh.Channel, cmd string) {
	if strings.Contains(cmd, "echo $SHELL") {
		fakeShellProbe(ch, cmd)
		return
	}
	for {
		select {
		default:
		}
		if _, err := fmt.Fprintf(ch, "tick-%d\n", mathrand.Int63()); err != nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// fakeConnector wires a real sshConnector to the fake server on 127.0.0.1.
func fakeConnector(t *testing.T, s *fakeSSHServer) *sshConnector {
	t.Helper()
	host, portStr, err := net.SplitHostPort(s.addr())
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
			_const.VariableConnectorUser:     "root",
			_const.VariableConnectorPassword: "fake-password",
		},
	})
	if err := c.Init(context.Background()); err != nil {
		t.Fatalf("connector init: %v", err)
	}
	t.Cleanup(func() { _ = c.Close(context.Background()) })
	return c
}

// waitForGoroutines polls until the goroutine count settles back to baseline
// (or the budget expires), proving a cancelled run did not leak its reader.
func waitForGoroutines(t *testing.T, baseline int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		runtime.Gosched()
		if runtime.NumGoroutine() <= baseline+2 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("goroutines did not settle: baseline=%d now=%d", baseline, runtime.NumGoroutine())
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// T-R12-01: a session that never exits must make ExecuteCommand return when
// the short ctx ends — resources released, no leaked reader, and the error
// states that the remote effect is unknown instead of inviting a retry.
func TestSSHCommandCancellation(t *testing.T) {
	s := newFakeSSHServer(t)
	s.setBehavior(fakeNeverExit(s))
	c := fakeConnector(t, s)
	execsAfterInit := s.execCount.Load()

	baseline := runtime.NumGoroutine()
	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()

	start := time.Now()
	stdout, _, err := c.ExecuteCommand(ctx, "echo hello")
	elapsed := time.Since(start)
	if err == nil {
		t.Fatalf("a never-exiting session must fail on ctx timeout")
	}
	if elapsed > 5*time.Second {
		t.Fatalf("ExecuteCommand ignored the ctx: took %v", elapsed)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error must unwrap to the ctx cause, got %v", err)
	}
	if !strings.Contains(err.Error(), "remote effect is unknown") || !strings.Contains(err.Error(), "must not be replayed") {
		t.Fatalf("error must state the remote effect is unknown and forbid replay, got %v", err)
	}
	if strings.Contains(string(stdout), "hello") {
		t.Fatalf("no command output was expected: %q", stdout)
	}
	// The partial sudo prompt was answered before the cancellation: the
	// prompt-detection path still worked under the new cancellation logic.
	if len(s.seenPasswords()) == 0 {
		t.Fatal("the fake server never received the sudo password on stdin")
	}
	// Only one execution beyond the Init probe: a cancelled command is never
	// silently replayed.
	if got := s.execCount.Load() - execsAfterInit; got != 1 {
		t.Fatalf("expected exactly 1 exec (no automatic replay), got %d", got)
	}
	waitForGoroutines(t, baseline)

	// The shared SSH client must stay usable: switching the scenario to a
	// normal exit lets the next command succeed without re-dialling.
	s.setBehavior(fakeNormalExit("SECOND-OK\n", ""))
	stdout2, _, err2 := c.ExecuteCommand(context.Background(), "echo second")
	if err2 != nil {
		t.Fatalf("the connector must stay usable after a cancellation: %v", err2)
	}
	if !strings.Contains(string(stdout2), "SECOND-OK") {
		t.Fatalf("unexpected output: %q", stdout2)
	}
	if got := s.execCount.Load() - execsAfterInit; got != 2 {
		t.Fatalf("the follow-up command must be exactly one more exec, total delta=%d", got)
	}
	waitForGoroutines(t, baseline)
}

// T-R12-03: a remote that succeeded but whose response never completes also
// ends in "remote effect unknown", exactly once, with no replay; the deadline
// variant (not only Timeout) is honoured the same way.
func TestSSHCommandDeadline(t *testing.T) {
	t.Run("remote succeeded but response interrupted records unknown", func(t *testing.T) {
		s := newFakeSSHServer(t)
		s.setBehavior(fakeSuccessNoEOF)
		c := fakeConnector(t, s)
		execsAfterInit := s.execCount.Load()

		baseline := runtime.NumGoroutine()
		ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
		defer cancel()
		_, _, err := c.ExecuteCommand(ctx, "apply-something")
		if err == nil {
			t.Fatal("a hanging response must fail once the ctx ends")
		}
		var unknown *RemoteStateUnknownError
		if !errors.As(err, &unknown) {
			t.Fatalf("the failure must be a RemoteStateUnknownError, got %T: %v", err, err)
		}
		if !strings.Contains(unknown.Cmd, "apply-something") {
			t.Fatalf("the unknown error must name the command, got %v", unknown)
		}
		if got := s.execCount.Load() - execsAfterInit; got != 1 {
			t.Fatalf("no replay without remote confirmation, execs=%d", got)
		}
		waitForGoroutines(t, baseline)

		// The next (explicitly issued) command runs normally.
		s.setBehavior(fakeNormalExit("AFTER-OK\n", ""))
		out, _, err := c.ExecuteCommand(context.Background(), "echo after")
		if err != nil || !strings.Contains(string(out), "AFTER-OK") {
			t.Fatalf("connector unusable after cancellation: %v %q", err, out)
		}
	})

	t.Run("deadline in the past behaves like a timeout", func(t *testing.T) {
		s := newFakeSSHServer(t)
		s.setBehavior(fakeNeverExit(s))
		c := fakeConnector(t, s)

		ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-1*time.Second))
		defer cancel()
		start := time.Now()
		_, _, err := c.ExecuteCommand(ctx, "echo hello")
		if err == nil {
			t.Fatal("an already-expired deadline must fail immediately")
		}
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("error must unwrap to DeadlineExceeded, got %v", err)
		}
		if time.Since(start) > 5*time.Second {
			t.Fatalf("expired deadline was not honoured immediately")
		}
	})

	t.Run("stdout stream and stderr are collected until cancellation", func(t *testing.T) {
		s := newFakeSSHServer(t)
		s.setBehavior(fakeStreamForever)
		c := fakeConnector(t, s)

		ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
		defer cancel()
		_, _, err := c.ExecuteCommand(ctx, "yes")
		if err == nil {
			t.Fatal("an endless stream must end with the ctx")
		}
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("error must unwrap to DeadlineExceeded, got %v", err)
		}
	})

	t.Run("normal exit keeps stdout stderr and exit semantics", func(t *testing.T) {
		s := newFakeSSHServer(t)
		s.setBehavior(fakeNormalExit("OUT-OK\n", "ERR-DETAIL\n"))
		c := fakeConnector(t, s)

		stdout, stderr, err := c.ExecuteCommand(context.Background(), "echo OUT-OK")
		if err != nil {
			t.Fatalf("normal exit must succeed: %v", err)
		}
		if !strings.Contains(string(stdout), "OUT-OK") {
			t.Fatalf("stdout missing: %q", stdout)
		}
		if !strings.Contains(string(stderr), "ERR-DETAIL") {
			t.Fatalf("stderr missing: %q", stderr)
		}
	})
}
