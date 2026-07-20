// Package minio is a reusable test harness that runs a real minio/minio
// container for integration tests that need an actual S3-compatible
// server, mirroring internal/testutil/dovecot's pattern for IMAP.
package minio

import (
	"fmt"
	"net"
	"net/http"
	"os/exec"
	"testing"
	"time"
)

const (
	rootUser     = "marchi-test"
	rootPassword = "marchi-test-secret"
)

// Server is a running MinIO test container, reachable over plain HTTP.
type Server struct {
	ContainerName string
	Endpoint      string // http://host:port, suitable for s3store.Options.Endpoint
	AccessKeyID   string
	SecretKey     string
}

// RequireDocker skips the test if the docker CLI isn't available or the
// daemon isn't reachable — same opt-in convention as dovecot.RequireDocker.
func RequireDocker(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not found in PATH, skipping Docker-based integration test")
	}
	if err := exec.Command("docker", "info").Run(); err != nil {
		t.Skip("docker daemon not reachable, skipping Docker-based integration test")
	}
}

// Start launches a fresh minio/minio container and registers its teardown
// via t.Cleanup. The container is ready to accept S3 API requests by the
// time Start returns; the caller (typically s3store.Client.EnsureBucket)
// is responsible for creating any bucket it needs — MinIO doesn't
// provision one automatically.
func Start(t *testing.T) *Server {
	t.Helper()
	RequireDocker(t)

	port := freeTCPPort(t)
	name := fmt.Sprintf("marchi-test-minio-%d", time.Now().UnixNano())

	cmd := exec.Command("docker", "run", "-d", "--name", name,
		"-p", fmt.Sprintf("%d:9000", port),
		"-e", fmt.Sprintf("MINIO_ROOT_USER=%s", rootUser),
		"-e", fmt.Sprintf("MINIO_ROOT_PASSWORD=%s", rootPassword),
		"minio/minio:latest", "server", "/data")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("minio: starting container: %v\n%s", err, out)
	}
	t.Cleanup(func() {
		_ = exec.Command("docker", "rm", "-f", name).Run()
	})

	s := &Server{
		ContainerName: name,
		Endpoint:      fmt.Sprintf("http://127.0.0.1:%d", port),
		AccessKeyID:   rootUser,
		SecretKey:     rootPassword,
	}
	s.waitReady(t, 30*time.Second)
	return s
}

// waitReady polls MinIO's /minio/health/live endpoint, which returns 200
// only once the server is actually accepting S3 API requests (the
// container's listening socket can come up before that).
func (s *Server) waitReady(t *testing.T, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	client := &http.Client{Timeout: 2 * time.Second}
	for time.Now().Before(deadline) {
		resp, err := client.Get(s.Endpoint + "/minio/health/live")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(300 * time.Millisecond)
	}
	t.Fatalf("minio: container %s did not become ready on %s within %s", s.ContainerName, s.Endpoint, timeout)
}

// freeTCPPort asks the OS for a free port by briefly binding to :0.
func freeTCPPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("minio: finding a free port: %v", err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}
