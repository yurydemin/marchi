// Package dovecot is a reusable test harness that runs a real
// dovecot/dovecot:latest container for integration tests that need an
// actual IMAP server rather than the in-process fake servers
// internal/imapclient and internal/sync's own unit tests use for
// deterministic, Docker-free coverage of protocol edge cases.
//
// This step's crash-recovery test is the first user; Phase 3's Restore
// Engine tests are expected to reuse it too (per the implementation plan).
package dovecot

import (
	"bytes"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/emersion/go-imap/client"
)

// Server is a running Dovecot test container, reachable over plaintext IMAP.
type Server struct {
	ContainerName string
	Host          string
	Port          int
}

// RequireDocker skips the test if the docker CLI isn't available or the
// daemon isn't reachable, rather than failing — this harness is an opt-in
// integration aid, not something every `go test ./...` run needs to have
// Docker for.
func RequireDocker(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not found in PATH, skipping Docker-based integration test")
	}
	if err := exec.Command("docker", "info").Run(); err != nil {
		t.Skip("docker daemon not reachable, skipping Docker-based integration test")
	}
}

// Start launches a fresh dovecot/dovecot:latest container with cleartext
// auth allowed and a single test user, and registers its teardown via
// t.Cleanup. The container is ready to accept IMAP connections by the time
// Start returns.
//
// auth_allow_cleartext is Dovecot 2.4's renamed disable_plaintext_auth
// (inverted logic) — the image's default security posture rejects LOGIN
// over a plaintext connection, which this harness deliberately relaxes
// since it's a local, throwaway test container, not a real mailbox.
func Start(t *testing.T, user, password string) *Server {
	t.Helper()
	RequireDocker(t)

	port := freeTCPPort(t)
	name := fmt.Sprintf("marchi-test-dovecot-%d", time.Now().UnixNano())

	confDir := t.TempDir()
	confPath := filepath.Join(confDir, "99-test.conf")
	if err := os.WriteFile(confPath, []byte("auth_allow_cleartext = yes\n"), 0o644); err != nil {
		t.Fatalf("dovecot: writing test config: %v", err)
	}

	cmd := exec.Command("docker", "run", "-d", "--name", name,
		"-p", fmt.Sprintf("%d:31143", port),
		"-e", fmt.Sprintf("USER_PASSWORD={PLAIN}%s", password),
		"-v", confPath+":/etc/dovecot/conf.d/99-test.conf:ro",
		"dovecot/dovecot:latest")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("dovecot: starting container: %v\n%s", err, out)
	}
	t.Cleanup(func() {
		_ = exec.Command("docker", "rm", "-f", name).Run()
	})

	s := &Server{ContainerName: name, Host: "127.0.0.1", Port: port}
	s.waitReady(t, 30*time.Second)
	return s
}

// waitReady polls with a real IMAP client.Dial (not just a raw TCP
// connect) until the server actually completes the protocol greeting —
// Dovecot's listening socket comes up before the IMAP service inside it is
// fully ready to answer, so a bare TCP-connect check can report "ready"
// too early and the first real client.Dial gets an EOF.
func (s *Server) waitReady(t *testing.T, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	addr := net.JoinHostPort(s.Host, fmt.Sprint(s.Port))
	for time.Now().Before(deadline) {
		c, err := client.Dial(addr)
		if err == nil {
			_ = c.Logout()
			return
		}
		time.Sleep(300 * time.Millisecond)
	}
	t.Fatalf("dovecot: container %s did not become ready on %s within %s", s.ContainerName, addr, timeout)
}

// AppendMessage connects, logs in, and IMAP-APPENDs raw into mailbox. Test
// fixture setup only — Marchi's own Restore Engine (Phase 3) is what
// eventually exercises APPEND as a product feature; this is just how tests
// seed a mailbox with known content.
func (s *Server) AppendMessage(t *testing.T, user, password, mailbox string, raw []byte) {
	t.Helper()
	c, err := client.Dial(net.JoinHostPort(s.Host, fmt.Sprint(s.Port)))
	if err != nil {
		t.Fatalf("dovecot: dialing: %v", err)
	}
	defer c.Logout()

	if err := c.Login(user, password); err != nil {
		t.Fatalf("dovecot: login: %v", err)
	}
	if err := c.Append(mailbox, nil, time.Time{}, bytes.NewReader(raw)); err != nil {
		t.Fatalf("dovecot: append: %v", err)
	}
}

// freeTCPPort asks the OS for a free port by briefly binding to :0. Not
// airtight against another process grabbing the same port before Docker
// binds it, but that race is vanishingly rare in practice for tests.
func freeTCPPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("dovecot: finding a free port: %v", err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}
