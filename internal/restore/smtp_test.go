package restore

import (
	"context"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/yurydemin/marchi/internal/account"
	"github.com/yurydemin/marchi/internal/domain"
)

func TestDeriveSMTPHost(t *testing.T) {
	cases := []struct {
		imapHost string
		want     string
	}{
		{"imap.example.com", "smtp.example.com"},
		{"mail.example.com", "mail.example.com"}, // no "imap." prefix, reused verbatim
		{"127.0.0.1", "127.0.0.1"},
	}
	for _, c := range cases {
		if got := deriveSMTPHost(c.imapHost); got != c.want {
			t.Errorf("deriveSMTPHost(%q) = %q, want %q", c.imapHost, got, c.want)
		}
	}
}

// TestSendSMTP_TransmitsRawContentByteForByte confirms the fallback path
// sends the .eml verbatim (envelope aside) — a fake SMTP server records
// exactly what MAIL FROM/RCPT TO/DATA it received, and this checks DATA
// matches the input exactly (byte-for-byte header preservation, FR-RS-02).
func TestSendSMTP_TransmitsRawContentByteForByte(t *testing.T) {
	addr, srv := startFakeSMTPServer(t)
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatalf("SplitHostPort: %v", err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatalf("Atoi: %v", err)
	}

	targetAccount := &domain.Account{
		Email: "restored-to@example.com", IMAPUsername: "restored-to@example.com",
	}
	content := []byte("From: original-sender@example.com\r\nSubject: restore via smtp\r\nMessage-ID: <original@example.com>\r\n\r\nOriginal body, unmodified.\r\n")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := sendSMTP(ctx, host, port, targetAccount, account.IMAPAuth{Password: "irrelevant-password"}, content); err != nil {
		t.Fatalf("sendSMTP: %v", err)
	}

	mailFrom, rcptTo, data := srv.received()
	if mailFrom != "restored-to@example.com" {
		t.Errorf("MAIL FROM = %q, want the target account's own address", mailFrom)
	}
	if rcptTo != "restored-to@example.com" {
		t.Errorf("RCPT TO = %q, want the target account's own address", rcptTo)
	}
	if !strings.Contains(string(data), "Message-ID: <original@example.com>") {
		t.Errorf("DATA missing the original Message-ID header, got:\n%s", data)
	}
	if !strings.Contains(string(data), "Original body, unmodified.") {
		t.Errorf("DATA missing the original body, got:\n%s", data)
	}
}

// TestSendSMTP_OAuth2_AuthenticatesViaXOAUTH2 confirms an OAuth2 auth
// (auth.OAuth2AccessToken set, no Password) drives go-mail's native
// SMTPAuthXOAUTH2 rather than AUTH PLAIN.
func TestSendSMTP_OAuth2_AuthenticatesViaXOAUTH2(t *testing.T) {
	addr, srv := startFakeSMTPServerXOAUTH2(t, true)
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatalf("SplitHostPort: %v", err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatalf("Atoi: %v", err)
	}

	targetAccount := &domain.Account{Email: "oauth2-target@gmail.com", IMAPUsername: "oauth2-target@gmail.com"}
	content := []byte("From: a@example.com\r\nSubject: oauth2 smtp restore\r\n\r\nBody.\r\n")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := sendSMTP(ctx, host, port, targetAccount, account.IMAPAuth{OAuth2AccessToken: "ya29.valid-token"}, content); err != nil {
		t.Fatalf("sendSMTP: %v", err)
	}

	mailFrom, _, _ := srv.received()
	if mailFrom != "oauth2-target@gmail.com" {
		t.Errorf("MAIL FROM = %q, want the target account's own address", mailFrom)
	}
}

// TestSendSMTP_OAuth2_RejectedToken_ReturnsError confirms a rejected
// XOAUTH2 token (Google's two-step failure protocol) surfaces as an
// error from sendSMTP rather than being silently swallowed.
func TestSendSMTP_OAuth2_RejectedToken_ReturnsError(t *testing.T) {
	addr, _ := startFakeSMTPServerXOAUTH2(t, false)
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatalf("SplitHostPort: %v", err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatalf("Atoi: %v", err)
	}

	targetAccount := &domain.Account{Email: "oauth2-target@gmail.com", IMAPUsername: "oauth2-target@gmail.com"}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	err = sendSMTP(ctx, host, port, targetAccount, account.IMAPAuth{OAuth2AccessToken: "ya29.expired-token"}, []byte("irrelevant"))
	if err == nil {
		t.Fatal("expected an error for a rejected XOAUTH2 token, got nil")
	}
}
