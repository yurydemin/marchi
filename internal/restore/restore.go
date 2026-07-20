// Package restore implements the Restore Engine (FR-RS-01..05): copying
// an archived email back into a live IMAP mailbox.
//
// FR-RS-02 describes two methods: primary IMAP APPEND (preserves the
// original INTERNALDATE and flags), and a secondary SMTP submission
// fallback (delivers via normal mail flow, but the message gets a fresh
// INTERNALDATE — an accepted degradation, not a bug). This package tries
// APPEND first and falls back to SMTP on ANY APPEND failure — including
// a server that rejects the message outright (FR-RS-05's "duplicate
// Message-ID" scenario is just one such rejection reason among others;
// there's nothing MailVault itself needs to detect specially, the
// target server's own response is what decides success or failure). Only
// if SMTP also fails is the attempt recorded as failed.
package restore

import (
	"context"
	"fmt"
	"os"

	"go.uber.org/zap"

	"github.com/yurydemin/marchi/internal/account"
	"github.com/yurydemin/marchi/internal/db/repo"
	"github.com/yurydemin/marchi/internal/domain"
	"github.com/yurydemin/marchi/internal/imapclient"
	"github.com/yurydemin/marchi/internal/s3store"
)

// Deps bundles everything RestoreOne needs.
type Deps struct {
	EmailsRepo      *repo.EmailsRepo
	AccountsRepo    *repo.AccountsRepo
	RestoreLogsRepo *repo.RestoreLogsRepo
	Manager         *account.Manager
	// LazyLoader, if nil, means an S3-resident email (storage_location
	// = 's3') can't be restored — FR-RS-03 requires being able to lazy
	// load it back, which needs S3 configured. A nil LazyLoader is only
	// a problem for S3-resident emails; local ones restore regardless.
	LazyLoader *s3store.LazyLoader
	// OAuth2Refresher refreshes an expired OAuth2 target account's token
	// before APPEND/SMTP — typically *oauth2config.Manager. nil means an
	// expired token is used as-is (account.Manager.ResolveIMAPAuth's own
	// documented fallback), which will simply fail authentication with a
	// clear error rather than refreshing first.
	OAuth2Refresher account.OAuth2TokenRefresher
	Logger          *zap.Logger
}

// Restorer executes restore operations against Deps.
type Restorer struct {
	deps Deps
}

func New(deps Deps) *Restorer {
	if deps.Logger == nil {
		deps.Logger = zap.NewNop()
	}
	return &Restorer{deps: deps}
}

// RestoreOne restores emailID into targetAccountID's targetFolder,
// recording the outcome in restore_logs (FR-RS-04) regardless of whether
// it succeeded. The returned error is non-nil only for a failure to even
// attempt the restore (e.g. the email or target account doesn't exist) or
// to record the log — a restore that was attempted but failed on both
// APPEND and SMTP still returns a nil error alongside a RestoreLog whose
// Status is "failed", since that's a successfully-recorded outcome, not
// a bug in RestoreOne itself.
func (r *Restorer) RestoreOne(ctx context.Context, emailID, targetAccountID int64, targetFolder string) (*domain.RestoreLog, error) {
	email, err := r.deps.EmailsRepo.GetByID(ctx, emailID)
	if err != nil {
		return nil, fmt.Errorf("restore: loading email %d: %w", emailID, err)
	}
	targetAccount, err := r.deps.AccountsRepo.GetByID(ctx, targetAccountID)
	if err != nil {
		return nil, fmt.Errorf("restore: loading target account %d: %w", targetAccountID, err)
	}

	content, loadErr := r.loadContent(ctx, email)
	if loadErr != nil {
		return r.record(ctx, email.ID, targetAccountID, targetFolder, domain.RestoreMethodIMAPAppend, loadErr)
	}

	auth, err := r.deps.Manager.ResolveIMAPAuth(ctx, targetAccount, r.deps.OAuth2Refresher)
	if err != nil {
		return r.record(ctx, email.ID, targetAccountID, targetFolder, domain.RestoreMethodIMAPAppend,
			fmt.Errorf("resolving target account credentials: %w", err))
	}

	appendErr := r.tryAppend(ctx, targetAccount, auth, targetFolder, email, content)
	if appendErr == nil {
		return r.record(ctx, email.ID, targetAccountID, targetFolder, domain.RestoreMethodIMAPAppend, nil)
	}
	r.deps.Logger.Warn("restore: IMAP APPEND failed, falling back to SMTP",
		zap.Int64("email_id", email.ID), zap.Error(appendErr))

	smtpErr := r.trySMTP(ctx, targetAccount, auth, content)
	if smtpErr == nil {
		return r.record(ctx, email.ID, targetAccountID, targetFolder, domain.RestoreMethodSMTP, nil)
	}

	return r.record(ctx, email.ID, targetAccountID, targetFolder, domain.RestoreMethodSMTP,
		fmt.Errorf("APPEND failed (%v); SMTP fallback also failed: %w", appendErr, smtpErr))
}

// loadContent returns email's raw .eml bytes, lazily loading and
// decrypting from S3 (FR-RS-03) if it's no longer stored locally.
func (r *Restorer) loadContent(ctx context.Context, email *domain.Email) ([]byte, error) {
	if email.StorageLocation == "local" {
		content, err := os.ReadFile(email.LocalPath)
		if err != nil {
			return nil, fmt.Errorf("reading local .eml: %w", err)
		}
		return content, nil
	}

	if r.deps.LazyLoader == nil {
		return nil, fmt.Errorf("email is stored in S3 (%s) but S3 is not configured, can't lazy-load it", email.S3Key)
	}
	// Load returns the fully decrypted plaintext in memory — the caller
	// holds onto that slice for the rest of RestoreOne, which is what
	// keeps this content available for the duration of the restore
	// without needing any cache-level pinning: nothing can evict a byte
	// slice Go code already holds a reference to, only the on-disk cache
	// file backing future requests, which is a separate concern.
	content, err := r.deps.LazyLoader.Load(ctx, email.S3Key)
	if err != nil {
		return nil, fmt.Errorf("lazy-loading from s3: %w", err)
	}
	return content, nil
}

func (r *Restorer) tryAppend(ctx context.Context, targetAccount *domain.Account, auth account.IMAPAuth, targetFolder string, email *domain.Email, content []byte) error {
	conn, err := imapclient.Connect(ctx, imapclient.ConnectOptions{
		Host: targetAccount.IMAPHost, Port: targetAccount.IMAPPort, TLS: targetAccount.IMAPTLS,
		Username: targetAccount.IMAPUsername, Password: auth.Password, OAuth2AccessToken: auth.OAuth2AccessToken,
	})
	if err != nil {
		return fmt.Errorf("connecting to target account: %w", err)
	}
	defer conn.Logout()

	if err := imapclient.Append(conn, targetFolder, email.Flags, email.Date, content); err != nil {
		return err
	}
	return nil
}

// record writes the outcome to restore_logs and returns it. attemptErr
// nil means success; non-nil means failure, with its message stored.
func (r *Restorer) record(ctx context.Context, emailID, targetAccountID int64, targetFolder, method string, attemptErr error) (*domain.RestoreLog, error) {
	log := &domain.RestoreLog{
		EmailID: emailID, TargetAccountID: targetAccountID, TargetFolder: targetFolder,
		Method: method, Status: domain.RestoreStatusCompleted,
	}
	if attemptErr != nil {
		log.Status = domain.RestoreStatusFailed
		log.ErrorMsg = attemptErr.Error()
	}

	id, err := r.deps.RestoreLogsRepo.Create(ctx, log)
	if err != nil {
		return nil, fmt.Errorf("restore: recording restore log: %w", err)
	}
	log.ID = id
	return log, nil
}
