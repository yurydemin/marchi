package notify

import (
	"context"
	"fmt"
	"strings"
	"time"

	mail "github.com/wneessen/go-mail"
)

// smtpTimeout mirrors internal/restore's own SMTP fallback timeout — one
// notification attempt shouldn't be allowed to hang the goroutine that
// detected the failure any longer than a restore attempt would.
const smtpTimeout = 30 * time.Second

// EmailNotifier sends a plain-text email through a dedicated outbound
// SMTP relay — deliberately not the same code path as
// internal/restore's SMTP fallback, which authenticates as one of the
// user's own IMAP accounts to relay a specific already-archived message.
// This is a standalone "where do failure alerts go" relay configuration,
// independent of any IMAP account.
type EmailNotifier struct {
	Host     string
	Port     int
	Username string // "" sends unauthenticated (a local relay/catcher, e.g. for testing)
	Password string
	From     string
	To       string // comma-separated recipient list
}

func (n *EmailNotifier) Notify(ctx context.Context, e Event) error {
	msg := mail.NewMsg()
	if err := msg.From(n.From); err != nil {
		return fmt.Errorf("notify: invalid From address %q: %w", n.From, err)
	}
	recipients := splitAddressList(n.To)
	if len(recipients) == 0 {
		return fmt.Errorf("notify: no recipient configured")
	}
	if err := msg.To(recipients...); err != nil {
		return fmt.Errorf("notify: invalid To address(es) %q: %w", n.To, err)
	}
	msg.Subject(fmt.Sprintf("Marchi: %s", e.Kind))
	msg.SetBodyString(mail.TypeTextPlain, fmt.Sprintf("%s\n\nTime: %s\n", e.Message, e.Time.Format(time.RFC3339)))

	opts := []mail.Option{
		mail.WithPort(n.Port),
		mail.WithTLSPolicy(mail.TLSOpportunistic),
		mail.WithTimeout(smtpTimeout),
	}
	if n.Username != "" {
		opts = append(opts, mail.WithSMTPAuth(mail.SMTPAuthPlain), mail.WithUsername(n.Username), mail.WithPassword(n.Password))
	}

	client, err := mail.NewClient(n.Host, opts...)
	if err != nil {
		return fmt.Errorf("notify: building smtp client for %s: %w", n.Host, err)
	}

	if err := client.DialAndSendWithContext(ctx, msg); err != nil {
		return fmt.Errorf("notify: sending email via %s: %w", n.Host, err)
	}
	return nil
}

func splitAddressList(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}
