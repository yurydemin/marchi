// Package imapclient wraps emersion/go-imap's client for MailVault's
// Account Manager and Sync Engine: connecting with a chosen TLS mode,
// logging in, and classifying failures into stages.
//
// FR-AM-04 wants a detailed reason for a failed Test Connection (wrong
// password vs unreachable server vs TLS problem), but go-imap's own Login
// error is just an untyped string built from the server's raw response —
// there's no error type to switch on. So the classification here comes
// from which stage of the connection sequence actually failed, not from
// inspecting the error's type or text.
package imapclient

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"strconv"
	"time"

	"github.com/emersion/go-imap/client"

	"github.com/yurydemin/marchi/internal/domain"
)

// Stage identifies which step of connecting failed.
type Stage string

const (
	StageDial  Stage = "dial"  // TCP connect, or reading the server's greeting
	StageTLS   Stage = "tls"   // TLS handshake (SSL mode) or STARTTLS negotiation
	StageLogin Stage = "login" // IMAP LOGIN rejected
)

// ConnectError wraps a failure with the Stage it occurred at.
type ConnectError struct {
	Stage Stage
	Err   error
}

func (e *ConnectError) Error() string {
	switch e.Stage {
	case StageDial:
		return fmt.Sprintf("could not reach IMAP server: %v", e.Err)
	case StageTLS:
		return fmt.Sprintf("TLS handshake failed: %v", e.Err)
	case StageLogin:
		return fmt.Sprintf("IMAP login rejected: %v", e.Err)
	default:
		return e.Err.Error()
	}
}

func (e *ConnectError) Unwrap() error { return e.Err }

// DefaultDialTimeout is used when ConnectOptions.DialTimeout is zero.
const DefaultDialTimeout = 30 * time.Second

// ConnectOptions describes how to reach and authenticate to an IMAP
// account. Exactly one of Password or OAuth2AccessToken should be set —
// Password logs in via plain LOGIN, OAuth2AccessToken authenticates via
// SASL XOAUTH2 (FR-AM-01's OAuth2 accounts, Phase 3 step 13). Refreshing
// an expired token before it gets here is the caller's job
// (account.Manager.ResolveIMAPAuth).
type ConnectOptions struct {
	Host              string
	Port              int
	TLS               domain.IMAPTLSMode
	Username          string
	Password          string
	OAuth2AccessToken string
	DialTimeout       time.Duration
}

// Connect dials opts.Host:opts.Port, negotiates TLS per opts.TLS, and logs
// in. The caller owns the returned client and must Logout() (or Close(), if
// Logout itself fails) it.
//
// TLS-mode connections deliberately don't reuse client.DialWithDialerTLS —
// that bundles the TCP dial and the TLS handshake into one call with one
// combined error, which would make StageDial and StageTLS indistinguishable.
// Dialing plain first and wrapping in tls.Client ourselves keeps each
// failure attributable to the right stage.
func Connect(ctx context.Context, opts ConnectOptions) (*client.Client, error) {
	addr := net.JoinHostPort(opts.Host, strconv.Itoa(opts.Port))
	timeout := opts.DialTimeout
	if timeout <= 0 {
		timeout = DefaultDialTimeout
	}

	dialer := &net.Dialer{Timeout: timeout}
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, &ConnectError{Stage: StageDial, Err: err}
	}

	var c *client.Client
	if opts.TLS == domain.IMAPTLSSSL {
		tlsConn := tls.Client(conn, tlsConfigFor(opts.Host))
		_ = tlsConn.SetDeadline(time.Now().Add(timeout))
		c, err = client.New(tlsConn)
		if err != nil {
			_ = tlsConn.Close()
			return nil, &ConnectError{Stage: StageTLS, Err: err}
		}
		_ = tlsConn.SetDeadline(time.Time{})
	} else {
		_ = conn.SetDeadline(time.Now().Add(timeout))
		c, err = client.New(conn)
		if err != nil {
			_ = conn.Close()
			return nil, &ConnectError{Stage: StageDial, Err: err}
		}
		_ = conn.SetDeadline(time.Time{})

		if opts.TLS == domain.IMAPTLSStartTLS {
			if err := c.StartTLS(tlsConfigFor(opts.Host)); err != nil {
				_ = c.Close()
				return nil, &ConnectError{Stage: StageTLS, Err: err}
			}
		}
	}

	if opts.OAuth2AccessToken != "" {
		if err := c.Authenticate(&xoauth2SASLClient{username: opts.Username, accessToken: opts.OAuth2AccessToken}); err != nil {
			_ = c.Logout()
			return nil, &ConnectError{Stage: StageLogin, Err: err}
		}
	} else {
		if err := c.Login(opts.Username, opts.Password); err != nil {
			_ = c.Logout()
			return nil, &ConnectError{Stage: StageLogin, Err: err}
		}
	}

	return c, nil
}

func tlsConfigFor(host string) *tls.Config {
	return &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}
}
