package notify

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestEmailNotifier_Notify_DeliversViaSMTP(t *testing.T) {
	host, port, srv := startFakeSMTPServer(t)

	n := &EmailNotifier{
		Host: host, Port: port, Username: "relay-user", Password: "relay-pass",
		From: "marchi@example.com", To: "admin@example.com",
	}
	err := n.Notify(context.Background(), Event{
		Kind: "retention_failed", Message: "listing candidates failed", Time: time.Date(2026, 7, 27, 10, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("Notify: %v", err)
	}

	mailFrom, rcptTo, data, authSeen := srv.received()
	if mailFrom != "marchi@example.com" {
		t.Errorf("MAIL FROM = %q", mailFrom)
	}
	if len(rcptTo) != 1 || rcptTo[0] != "admin@example.com" {
		t.Errorf("RCPT TO = %v", rcptTo)
	}
	if !authSeen {
		t.Error("expected AUTH PLAIN with a Username configured, saw none")
	}
	body := string(data)
	if !strings.Contains(body, "Marchi: retention_failed") {
		t.Errorf("body missing subject line, got: %q", body)
	}
	if !strings.Contains(body, "listing candidates failed") {
		t.Errorf("body missing event message, got: %q", body)
	}
}

func TestEmailNotifier_Notify_MultipleRecipients(t *testing.T) {
	host, port, srv := startFakeSMTPServer(t)

	n := &EmailNotifier{
		Host: host, Port: port,
		From: "marchi@example.com", To: "a@example.com, b@example.com",
	}
	if err := n.Notify(context.Background(), Event{Kind: "sync_failed", Message: "x"}); err != nil {
		t.Fatalf("Notify: %v", err)
	}

	_, rcptTo, _, authSeen := srv.received()
	if len(rcptTo) != 2 || rcptTo[0] != "a@example.com" || rcptTo[1] != "b@example.com" {
		t.Errorf("RCPT TO = %v, want [a@example.com b@example.com]", rcptTo)
	}
	if authSeen {
		t.Error("AUTH seen with no Username configured, want unauthenticated delivery")
	}
}

func TestEmailNotifier_Notify_NoRecipient_IsAnError(t *testing.T) {
	n := &EmailNotifier{Host: "127.0.0.1", Port: 1, From: "marchi@example.com", To: ""}
	if err := n.Notify(context.Background(), Event{Kind: "sync_failed"}); err == nil {
		t.Error("Notify with no recipient configured = nil error, want an error")
	}
}
