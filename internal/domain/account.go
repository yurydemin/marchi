// Package domain holds plain data structs shared across packages (Account,
// and more as later steps add Folder/Email/Attachment/Rule/...). No
// behavior lives here — that's what keeps internal/db/repo, internal/account,
// internal/sync etc. from having to import each other just to pass data
// around.
package domain

import (
	"fmt"
	"strings"
	"time"
)

// IMAPTLSMode mirrors the accounts.imap_tls column (FR-ST-03): 0=none,
// 1=ssl, 2=starttls.
type IMAPTLSMode int

const (
	IMAPTLSNone     IMAPTLSMode = 0
	IMAPTLSSSL      IMAPTLSMode = 1
	IMAPTLSStartTLS IMAPTLSMode = 2
)

func (m IMAPTLSMode) String() string {
	switch m {
	case IMAPTLSNone:
		return "none"
	case IMAPTLSSSL:
		return "ssl"
	case IMAPTLSStartTLS:
		return "starttls"
	default:
		return "unknown"
	}
}

// ParseIMAPTLSMode is String's inverse — shared by the CLI's --tls flag
// and the Accounts REST API's JSON "tls" field so the two never drift
// apart on which strings are accepted. An empty string defaults to ssl,
// matching the CLI's own --tls default.
func ParseIMAPTLSMode(s string) (IMAPTLSMode, error) {
	switch strings.ToLower(s) {
	case "none":
		return IMAPTLSNone, nil
	case "ssl", "tls", "":
		return IMAPTLSSSL, nil
	case "starttls":
		return IMAPTLSStartTLS, nil
	default:
		return 0, fmt.Errorf("invalid tls mode %q (want none, ssl, or starttls)", s)
	}
}

// Account is an IMAP account (FR-AM-01). IMAPPasswordEncrypted and
// OAuth2TokenEncrypted are AES-256-GCM ciphertext (FR-AM-02) — never
// plaintext past the moment internal/account encrypts it.
type Account struct {
	ID                    int64
	Email                 string
	DisplayName           string
	IMAPHost              string
	IMAPPort              int
	IMAPTLS               IMAPTLSMode
	IMAPUsername          string
	IMAPPasswordEncrypted []byte
	OAuth2Provider        string // "google", "microsoft", or "" for none
	OAuth2TokenEncrypted  []byte
	IsActive              bool
	SyncCron              string // FR-SE-06: cron expression; "" means "use sync.default_schedule"
	CreatedAt             time.Time
	UpdatedAt             time.Time
}
