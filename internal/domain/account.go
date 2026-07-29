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

// ConnectorType distinguishes how Marchi talks to a mailbox. ConnectorIMAP
// is the default and everything Phase 1-4 ever did; ConnectorGmailAPI
// syncs a Gmail account through Google's native REST API instead —
// labels instead of folders, History API delta sync instead of UID
// FETCH — see internal/gmailapi and internal/sync.SyncAccountGmailAPI.
type ConnectorType string

const (
	ConnectorIMAP     ConnectorType = "imap"
	ConnectorGmailAPI ConnectorType = "gmail_api"
)

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
	// IsImported marks an account created by `marchi import` — it has no
	// real IMAP credentials to connect with (see the migration that added
	// this column for why it's distinct from IsActive).
	IsImported bool
	SyncCron   string // FR-SE-06: cron expression; "" means "use sync.default_schedule"
	// ConnectorType selects which sync engine Scheduler dispatches this
	// account to (internal/scheduler). Empty behaves as ConnectorIMAP —
	// every account created before this field existed is an IMAP account.
	ConnectorType ConnectorType
	// GmailHistoryID is ConnectorGmailAPI's incremental-sync cursor
	// (Gmail History API's startHistoryId for the next sync). Empty means
	// no full sync has completed yet — the next sync must list every
	// message rather than just the delta since a cursor.
	GmailHistoryID string
	// RetentionLocalDays/RetentionMoveToS3Days/RetentionS3Days override
	// this account's retention policy (FR-RE-04); nil means "inherit the
	// global default" (repo.RetentionSettingsRepo), the same nil-means-
	// inherit convention SyncCron uses for sync.default_schedule. See
	// internal/retention's package doc for the three-stage model these
	// three thresholds drive.
	RetentionLocalDays    *int
	RetentionMoveToS3Days *int
	RetentionS3Days       *int
	CreatedAt             time.Time
	UpdatedAt             time.Time
}
