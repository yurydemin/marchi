package notify

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// webhookTimeout bounds one delivery attempt — this runs inline in the
// scheduler/uploader goroutine that detected the failure, so it must not
// be allowed to hang indefinitely waiting on an unreachable endpoint.
const webhookTimeout = 10 * time.Second

// WebhookNotifier POSTs a JSON payload to a single configured URL — the
// generic, integrate-with-anything channel (n8n, a Telegram bot's own
// webhook proxy, a Slack incoming webhook, a custom relay). Secret, if
// non-empty, signs the raw request body with HMAC-SHA256 in the
// X-Marchi-Signature header (the same "sha256=<hex>" convention GitHub's
// own webhooks use), so a receiver can verify the request actually came
// from this Marchi instance rather than trusting the URL alone.
type WebhookNotifier struct {
	URL    string
	Secret string
	Client *http.Client // nil uses a default client with webhookTimeout
}

type webhookPayload struct {
	Kind         string         `json:"kind"`
	Message      string         `json:"message"`
	AccountEmail string         `json:"account_email,omitempty"`
	Time         time.Time      `json:"time"`
	Meta         map[string]any `json:"meta,omitempty"`
}

func (w *WebhookNotifier) Notify(ctx context.Context, e Event) error {
	body, err := json.Marshal(webhookPayload{
		Kind: e.Kind, Message: e.Message, AccountEmail: e.AccountEmail, Time: e.Time, Meta: e.Meta,
	})
	if err != nil {
		return fmt.Errorf("notify: encoding webhook payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.URL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("notify: building webhook request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if w.Secret != "" {
		mac := hmac.New(sha256.New, []byte(w.Secret))
		mac.Write(body)
		req.Header.Set("X-Marchi-Signature", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	}

	client := w.Client
	if client == nil {
		client = &http.Client{Timeout: webhookTimeout}
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("notify: delivering webhook: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		return fmt.Errorf("notify: webhook endpoint returned %d", resp.StatusCode)
	}
	return nil
}
