package notify

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestWebhookNotifier_Notify_SendsExpectedPayload(t *testing.T) {
	var gotBody []byte
	var gotHeader http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		gotHeader = r.Header.Clone()
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	n := &WebhookNotifier{URL: srv.URL}
	when := time.Date(2026, 7, 27, 10, 0, 0, 0, time.UTC)
	err := n.Notify(context.Background(), Event{
		Kind: "sync_failed", Message: "connection refused", AccountEmail: "user@example.com", Time: when,
	})
	if err != nil {
		t.Fatalf("Notify: %v", err)
	}

	if gotHeader.Get("Content-Type") != "application/json" {
		t.Errorf("Content-Type = %q", gotHeader.Get("Content-Type"))
	}
	if gotHeader.Get("X-Marchi-Signature") != "" {
		t.Error("X-Marchi-Signature set without a Secret configured")
	}

	var payload webhookPayload
	if err := json.Unmarshal(gotBody, &payload); err != nil {
		t.Fatalf("unmarshaling payload: %v", err)
	}
	if payload.Kind != "sync_failed" || payload.Message != "connection refused" || payload.AccountEmail != "user@example.com" {
		t.Errorf("payload = %+v", payload)
	}
	if !payload.Time.Equal(when) {
		t.Errorf("payload.Time = %v, want %v", payload.Time, when)
	}
}

func TestWebhookNotifier_Notify_SignsWithSecret(t *testing.T) {
	const secret = "shh-its-a-secret"
	var gotBody []byte
	var gotSig string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		gotSig = r.Header.Get("X-Marchi-Signature")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	n := &WebhookNotifier{URL: srv.URL, Secret: secret}
	if err := n.Notify(context.Background(), Event{Kind: "retention_failed", Message: "boom"}); err != nil {
		t.Fatalf("Notify: %v", err)
	}

	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(gotBody)
	want := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	if gotSig != want {
		t.Errorf("X-Marchi-Signature = %q, want %q", gotSig, want)
	}
}

func TestWebhookNotifier_Notify_NonOKStatusIsAnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	n := &WebhookNotifier{URL: srv.URL}
	if err := n.Notify(context.Background(), Event{Kind: "sync_failed"}); err == nil {
		t.Error("Notify against a 500 endpoint = nil error, want an error")
	}
}

func TestWebhookNotifier_Notify_UnreachableEndpointIsAnError(t *testing.T) {
	n := &WebhookNotifier{URL: "http://127.0.0.1:1"} // nothing listens on port 1
	if err := n.Notify(context.Background(), Event{Kind: "sync_failed"}); err == nil {
		t.Error("Notify against an unreachable endpoint = nil error, want an error")
	}
}
