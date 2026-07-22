package imapclient

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/yurydemin/marchi/internal/domain"
)

func TestConnect_Success(t *testing.T) {
	addr := startFakePlaintextIMAPServer(t, fakeServerBehavior{loginOK: true})
	host, port := splitHostPort(t, addr)

	c, err := Connect(context.Background(), ConnectOptions{
		Host: host, Port: port, TLS: domain.IMAPTLSNone,
		Username: "user", Password: "pass", DialTimeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer c.Logout()
}

func TestConnect_LoginRejected_IsStageLogin(t *testing.T) {
	addr := startFakePlaintextIMAPServer(t, fakeServerBehavior{loginOK: false})
	host, port := splitHostPort(t, addr)

	_, err := Connect(context.Background(), ConnectOptions{
		Host: host, Port: port, TLS: domain.IMAPTLSNone,
		Username: "user", Password: "wrong", DialTimeout: 5 * time.Second,
	})
	assertStage(t, err, StageLogin)
}

func TestConnect_UnreachableServer_IsStageDial(t *testing.T) {
	// Bind then immediately close, so the port is (almost certainly) free
	// and nothing answers — connection refused.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	host, port := splitHostPort(t, ln.Addr().String())
	ln.Close()

	_, err = Connect(context.Background(), ConnectOptions{
		Host: host, Port: port, TLS: domain.IMAPTLSNone,
		Username: "user", Password: "pass", DialTimeout: 3 * time.Second,
	})
	assertStage(t, err, StageDial)
}

func TestConnect_CertificateMismatch_IsStageTLS(t *testing.T) {
	// Cert is for a hostname that doesn't match what we ask Connect to
	// verify — a deterministic, real crypto/tls handshake failure.
	cert := generateSelfSignedCert(t, "imap.wrong-hostname.example")
	addr := startFakeTLSIMAPServer(t, cert, fakeServerBehavior{loginOK: true})
	host, port := splitHostPort(t, addr) // host is 127.0.0.1, cert doesn't cover it

	_, err := Connect(context.Background(), ConnectOptions{
		Host: host, Port: port, TLS: domain.IMAPTLSSSL,
		Username: "user", Password: "pass", DialTimeout: 5 * time.Second,
	})
	assertStage(t, err, StageTLS)
}

func TestConnect_ContextCancelled_FailsDial(t *testing.T) {
	addr := startFakePlaintextIMAPServer(t, fakeServerBehavior{loginOK: true})
	host, port := splitHostPort(t, addr)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := Connect(ctx, ConnectOptions{
		Host: host, Port: port, TLS: domain.IMAPTLSNone,
		Username: "user", Password: "pass",
	})
	if err == nil {
		t.Fatal("expected an error for an already-cancelled context")
	}
}

func TestConnect_OAuth2_Success_AuthenticatesViaXOAUTH2(t *testing.T) {
	addr := startFakePlaintextIMAPServer(t, fakeServerBehavior{xoauth2: &xoauth2Behavior{ok: true}})
	host, port := splitHostPort(t, addr)

	c, err := Connect(context.Background(), ConnectOptions{
		Host: host, Port: port, TLS: domain.IMAPTLSNone,
		Username: "user@gmail.com", OAuth2AccessToken: "ya29.valid-token", DialTimeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer c.Logout()
}

func TestConnect_OAuth2_RejectedToken_IsStageLogin(t *testing.T) {
	addr := startFakePlaintextIMAPServer(t, fakeServerBehavior{xoauth2: &xoauth2Behavior{ok: false}})
	host, port := splitHostPort(t, addr)

	_, err := Connect(context.Background(), ConnectOptions{
		Host: host, Port: port, TLS: domain.IMAPTLSNone,
		Username: "user@gmail.com", OAuth2AccessToken: "ya29.expired-token", DialTimeout: 5 * time.Second,
	})
	assertStage(t, err, StageLogin)
}

func TestListFolders(t *testing.T) {
	addr := startFakePlaintextIMAPServer(t, fakeServerBehavior{
		loginOK: true,
		listResponses: []string{
			`* LIST (\HasNoChildren) "/" "INBOX"`,
			`* LIST (\HasNoChildren) "/" "Sent"`,
			// "Заметки" (Russian for "Notes") mod-UTF-7 encoded (generated
			// via utf7.Encoding.NewEncoder(), not hand-computed), to prove
			// decoding actually happens rather than just passing through ASCII.
			`* LIST (\HasNoChildren) "/" "&BBcEMAQ8BDUEQgQ6BDg-"`,
		},
	})
	host, port := splitHostPort(t, addr)

	c, err := Connect(context.Background(), ConnectOptions{
		Host: host, Port: port, TLS: domain.IMAPTLSNone,
		Username: "user", Password: "pass", DialTimeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer c.Logout()

	folders, err := ListFolders(c)
	if err != nil {
		t.Fatalf("ListFolders: %v", err)
	}
	if len(folders) != 3 {
		t.Fatalf("got %d folders, want 3: %+v", len(folders), folders)
	}

	names := map[string]bool{}
	for _, f := range folders {
		names[f.Name] = true
	}
	if !names["INBOX"] || !names["Sent"] {
		t.Errorf("missing expected folders in %v", names)
	}
	if !names["Заметки"] {
		t.Errorf("expected UTF-7 decoded 'Заметки', got names: %v", names)
	}
}

// TestConnectError_Error_FormatsByStage covers ConnectError.Error()
// directly: assertStage's own %v-formatted failure messages (its
// t.Fatalf/t.Errorf calls above) only evaluate when a test fails, so
// none of the passing Connect_* tests above ever actually call Error()
// or Unwrap() — hence this dedicated test.
func TestConnectError_Error_FormatsByStage(t *testing.T) {
	inner := errors.New("boom")
	cases := []struct {
		stage Stage
		want  string
	}{
		{StageDial, "could not reach IMAP server: boom"},
		{StageTLS, "TLS handshake failed: boom"},
		{StageLogin, "IMAP login rejected: boom"},
	}
	for _, tc := range cases {
		err := &ConnectError{Stage: tc.stage, Err: inner}
		if got := err.Error(); got != tc.want {
			t.Errorf("Stage=%v: Error() = %q, want %q", tc.stage, got, tc.want)
		}
	}
}

func TestConnectError_Unwrap(t *testing.T) {
	inner := errors.New("boom")
	err := &ConnectError{Stage: StageDial, Err: inner}
	if got := errors.Unwrap(err); got != inner {
		t.Errorf("Unwrap() = %v, want %v", got, inner)
	}
	if !errors.Is(err, inner) {
		t.Error("errors.Is(err, inner) = false, want true (relies on Unwrap)")
	}
}

func assertStage(t *testing.T, err error, want Stage) {
	t.Helper()
	var connErr *ConnectError
	if !errors.As(err, &connErr) {
		t.Fatalf("error = %v (%T), want *ConnectError", err, err)
	}
	if connErr.Stage != want {
		t.Errorf("Stage = %v, want %v (err: %v)", connErr.Stage, want, connErr)
	}
}
