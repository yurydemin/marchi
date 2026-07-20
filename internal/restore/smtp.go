package restore

import (
	"context"
	"fmt"
	"strings"
	"time"

	mail "github.com/wneessen/go-mail"

	"github.com/yurydemin/marchi/internal/account"
	"github.com/yurydemin/marchi/internal/domain"
)

// defaultSMTPPort is used since accounts has no dedicated SMTP connection
// fields yet — see deriveSMTPHost's doc comment. 587 is the standard
// submission port, expecting STARTTLS.
const defaultSMTPPort = 587

// smtpTimeout bounds the whole SMTP fallback attempt (connect through
// QUIT) — this only ever runs after IMAP APPEND has already failed, so it
// shouldn't be allowed to hang a restore indefinitely on top of that.
const smtpTimeout = 30 * time.Second

// deriveSMTPHost guesses the SMTP submission host from an account's IMAP
// host, since MailVault has no dedicated SMTP connection settings yet
// (accounts only has IMAP fields) — full SMTP account configuration,
// including OAuth2/XOAUTH2, is Phase 3 step 14's job. The common
// "imap.example.com" -> "smtp.example.com" convention covers most
// providers; anything else just reuses the IMAP host verbatim, which is
// also common for smaller/self-hosted mail servers that serve both
// protocols off the same hostname.
func deriveSMTPHost(imapHost string) string {
	if strings.HasPrefix(imapHost, "imap.") {
		return "smtp." + strings.TrimPrefix(imapHost, "imap.")
	}
	return imapHost
}

// trySMTP implements FR-RS-02's fallback: submit content (the raw .eml,
// byte-for-byte, so every original header survives) to targetAccount's
// own mailbox address via SMTP. INTERNALDATE isn't preserved this way —
// the message gets a fresh delivery date — which is the documented
// trade-off for this being the fallback, not the primary, method.
//
// go-mail's Client handles the connection/TLS/AUTH handshake — including
// XOAUTH2 for an OAuth2 targetAccount (auth.OAuth2AccessToken set), via
// its native SMTPAuthXOAUTH2 (whose wire encoding matches
// internal/oauth2.XOAUTH2InitialResponse byte-for-byte) — but the actual
// envelope and DATA are sent through the raw smtp.Client it hands back,
// not through go-mail's own Msg builder — that's what keeps every
// original header byte-identical rather than being re-serialized by a
// message-composition API designed for building new messages, not
// replaying existing ones untouched.
func (r *Restorer) trySMTP(ctx context.Context, targetAccount *domain.Account, auth account.IMAPAuth, content []byte) error {
	port := defaultSMTPPort
	if smtpPortOverrideForTests != 0 {
		port = smtpPortOverrideForTests
	}
	return sendSMTP(ctx, deriveSMTPHost(targetAccount.IMAPHost), port, targetAccount, auth, content)
}

// smtpPortOverrideForTests lets this package's own tests (same package,
// not _test) point the fallback at a fake SMTP server on a random local
// port instead of the real submission port 587 — always zero in
// production, never read anywhere outside trySMTP.
var smtpPortOverrideForTests int

// sendSMTP is trySMTP's implementation with host/port broken out as
// explicit parameters — trySMTP derives them from the account, tests
// point them directly at a fake SMTP server instead. auth picks the SMTP
// AUTH mechanism: OAuth2AccessToken set means XOAUTH2 (the token stands
// in for go-mail's "password" argument, per its smtp.XOAuth2Auth), a
// plain Password means AUTH PLAIN.
func sendSMTP(ctx context.Context, host string, port int, targetAccount *domain.Account, auth account.IMAPAuth, content []byte) error {
	ctx, cancel := context.WithTimeout(ctx, smtpTimeout)
	defer cancel()

	username := targetAccount.IMAPUsername
	if username == "" {
		username = targetAccount.Email
	}

	authType := mail.SMTPAuthPlain
	secret := auth.Password
	if auth.OAuth2AccessToken != "" {
		authType = mail.SMTPAuthXOAUTH2
		secret = auth.OAuth2AccessToken
	}

	client, err := mail.NewClient(host,
		mail.WithPort(port),
		mail.WithTLSPolicy(mail.TLSOpportunistic),
		mail.WithSMTPAuth(authType),
		mail.WithUsername(username),
		mail.WithPassword(secret),
		mail.WithTimeout(smtpTimeout),
	)
	if err != nil {
		return fmt.Errorf("building smtp client for %s: %w", host, err)
	}

	smtpClient, err := client.DialToSMTPClientWithContext(ctx)
	if err != nil {
		return fmt.Errorf("dialing smtp server %s: %w", host, err)
	}
	defer smtpClient.Close()

	addr := targetAccount.Email
	if err := smtpClient.Mail(addr); err != nil {
		return fmt.Errorf("MAIL FROM %s: %w", addr, err)
	}
	if err := smtpClient.Rcpt(addr); err != nil {
		return fmt.Errorf("RCPT TO %s: %w", addr, err)
	}
	w, err := smtpClient.Data()
	if err != nil {
		return fmt.Errorf("DATA: %w", err)
	}
	if _, err := w.Write(content); err != nil {
		_ = w.Close()
		return fmt.Errorf("writing message body: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("closing DATA: %w", err)
	}
	if err := smtpClient.Quit(); err != nil {
		return fmt.Errorf("QUIT: %w", err)
	}
	return nil
}
