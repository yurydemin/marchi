// Package retention implements FR-RE-04's cron-driven archive lifecycle:
// a three-stage model per email —
//
//	Stage A (local + S3 mirror, if configured)
//	  -> Stage B (S3-only, local copy evicted)
//	  -> Stage C (deleted entirely)
//
// Unlike Phase 1's Rule Engine, retention thresholds are NOT a per-rule
// setting. They're a single archive-wide default (RetentionSettingsRepo)
// with an optional per-account override (domain.Account's own
// Retention*Days fields) — the same default+override shape
// accounts.sync_cron already uses for sync.default_schedule. Both are
// resolved fresh on every Run, so an edit takes effect immediately for
// every email, old and new alike.
//
// This shape was chosen over "retention per archive rule" (an earlier
// design considered and dropped — see migration 000006's comment) because
// re-evaluating a rule's conditions against an already-archived email
// isn't reliable: several condition types (from_contains/to_contains)
// need the full RFC 5322 From/To header, which the emails table never
// stores — only the bare address survives archival.
//
// Stage A -> B fires on retention_move_to_s3_days when S3 mirroring is
// enabled (gated on emails.s3_etag being confirmed non-NULL — brief.md
// §4.6's safety invariant: never evict the only copy before its mirror
// is confirmed). When S3 is NOT enabled, retention_local_days instead
// triggers direct deletion — there's no mirror to fall back to. Stage
// B -> C fires on retention_s3_days, counted from s3_only_since (when
// Stage B started), independent of which A -> B path was taken.
package retention

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"time"

	"go.uber.org/zap"

	"github.com/yurydemin/marchi/internal/db/repo"
	"github.com/yurydemin/marchi/internal/db/writer"
	"github.com/yurydemin/marchi/internal/domain"
	"github.com/yurydemin/marchi/internal/s3config"
	"github.com/yurydemin/marchi/internal/s3store"
	"github.com/yurydemin/marchi/internal/search"
)

// Deps bundles everything a Run needs. S3ConfigRepo/S3ConfigManager, if
// either is nil, are treated as "S3 not configured" — Stage A always
// takes the direct-delete path in that case, and Stage B/C are skipped
// entirely (nothing can be in Stage B without S3 having been enabled at
// some point to put it there).
type Deps struct {
	AccountsRepo          *repo.AccountsRepo
	EmailsRepo            *repo.EmailsRepo
	RetentionSettingsRepo *repo.RetentionSettingsRepo
	S3ConfigRepo          *repo.S3ConfigRepo
	S3ConfigManager       *s3config.Manager
	Writer                writer.Writer
	// IndexFunc, if non-nil, is called fresh on every Run (the same
	// resolved-per-run convention scheduler.Deps.IndexFunc uses) and the
	// result gets a Delete call for every email actually removed (Stage A
	// direct-delete, Stage C) — best-effort, same philosophy as
	// archiveOne's own indexing (internal/sync/fetch.go): a failed index
	// delete never blocks the archive-correctness-critical part of
	// retention (the file/S3 object/SQLite row are the source of truth).
	// Resolving it fresh (rather than capturing a *search.Index once)
	// means a live reindex's swap is picked up on the very next Run.
	IndexFunc func() *search.Index
	// Now, if nil, defaults to time.Now — injectable for tests that need
	// to simulate "it's been N days" without actually waiting.
	Now    func() time.Time
	Logger *zap.Logger
}

// Stats summarizes one Run.
type Stats struct {
	MovedToS3Only int // Stage A -> B
	DeletedDirect int // Stage A -> gone (no S3 backup existed)
	DeletedFromS3 int // Stage B -> C
	Errors        int
}

// Runner executes retention passes against Deps.
type Runner struct {
	deps Deps
}

func New(deps Deps) *Runner {
	if deps.Now == nil {
		deps.Now = time.Now
	}
	if deps.Logger == nil {
		deps.Logger = zap.NewNop()
	}
	return &Runner{deps: deps}
}

// Run evaluates every account's effective retention policy against its
// emails and applies whichever stage transitions are due. One account's
// failure (a bad S3 client build, a single email's delete erroring) is
// logged and counted in Stats.Errors, not fatal to the rest of the run —
// tomorrow's cron tick will simply retry whatever didn't complete today.
func (r *Runner) Run(ctx context.Context) (Stats, error) {
	var stats Stats
	now := r.deps.Now()

	accounts, err := r.deps.AccountsRepo.List(ctx)
	if err != nil {
		return stats, fmt.Errorf("retention: listing accounts: %w", err)
	}

	globalSettings, err := r.deps.RetentionSettingsRepo.Get(ctx)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return stats, fmt.Errorf("retention: loading global retention settings: %w", err)
	}

	s3Enabled, s3Client, err := r.buildS3Client(ctx)
	if err != nil {
		r.deps.Logger.Warn("retention: s3 client unavailable, Stage A will use direct-delete for every account", zap.Error(err))
	}

	for _, a := range accounts {
		policy := resolveEffectivePolicy(a, globalSettings)

		if s3Enabled && policy.MoveToS3Days != nil {
			r.evictToS3Only(ctx, a.ID, now.AddDate(0, 0, -*policy.MoveToS3Days), now, &stats)
		} else if !s3Enabled && policy.LocalDays != nil {
			r.deleteDirectNoBackup(ctx, a.ID, now.AddDate(0, 0, -*policy.LocalDays), &stats)
		}

		if s3Enabled && policy.S3Days != nil {
			r.deleteFromS3(ctx, a.ID, now.AddDate(0, 0, -*policy.S3Days), s3Client, &stats)
		}
	}

	return stats, nil
}

func (r *Runner) index() *search.Index {
	if r.deps.IndexFunc == nil {
		return nil
	}
	return r.deps.IndexFunc()
}

// effectivePolicy is the resolved (account override, else global default)
// three-threshold policy for one account.
type effectivePolicy struct {
	LocalDays    *int
	MoveToS3Days *int
	S3Days       *int
}

func resolveEffectivePolicy(a *domain.Account, global *domain.RetentionSettings) effectivePolicy {
	p := effectivePolicy{
		LocalDays:    a.RetentionLocalDays,
		MoveToS3Days: a.RetentionMoveToS3Days,
		S3Days:       a.RetentionS3Days,
	}
	if global == nil {
		return p
	}
	if p.LocalDays == nil {
		p.LocalDays = global.DefaultLocalDays
	}
	if p.MoveToS3Days == nil {
		p.MoveToS3Days = global.DefaultMoveToS3Days
	}
	if p.S3Days == nil {
		p.S3Days = global.DefaultS3Days
	}
	return p
}

// buildS3Client reports whether S3 mirroring is enabled and, if so,
// returns a ready client. "Not enabled" (never configured, or configured
// but Enabled=false) is a normal state, not an error — only a genuine
// failure to build the client (bad credentials, decryption failure) is
// returned as err, and even that is treated as "not enabled" by the
// caller rather than aborting the whole run.
func (r *Runner) buildS3Client(ctx context.Context) (enabled bool, client *s3store.Client, err error) {
	if r.deps.S3ConfigRepo == nil || r.deps.S3ConfigManager == nil {
		return false, nil, nil
	}
	cfg, err := r.deps.S3ConfigRepo.Get(ctx)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil, nil
		}
		return false, nil, fmt.Errorf("loading s3 config: %w", err)
	}
	if !cfg.Enabled {
		return false, nil, nil
	}
	accessKey, secretKey, err := r.deps.S3ConfigManager.DecryptCredentials(cfg)
	if err != nil {
		return false, nil, fmt.Errorf("decrypting s3 credentials: %w", err)
	}
	c, err := s3store.NewClient(s3store.Options{
		Endpoint: cfg.Endpoint, Region: cfg.Region, Bucket: cfg.Bucket,
		AccessKeyID: accessKey, SecretAccessKey: secretKey,
		PathStyle: cfg.PathStyle, TLSSkipVerify: cfg.TLSSkipVerify,
	})
	if err != nil {
		return false, nil, fmt.Errorf("building s3 client: %w", err)
	}
	return true, c, nil
}

// evictToS3Only implements Stage A -> B for one account: delete the local
// file and flip storage_location, only for emails whose S3 upload is
// already confirmed (ListLocalDueForS3Eviction enforces that). now
// stamps s3_only_since — the same r.deps.Now() value Run resolved once
// for this whole pass, not a fresh wall-clock read, so a test driving an
// injected clock sees Stage B start exactly when the test says "now" is.
func (r *Runner) evictToS3Only(ctx context.Context, accountID int64, cutoff, now time.Time, stats *Stats) {
	due, err := r.deps.EmailsRepo.ListLocalDueForS3Eviction(ctx, accountID, cutoff)
	if err != nil {
		r.deps.Logger.Error("retention: listing Stage A->B candidates failed", zap.Int64("account_id", accountID), zap.Error(err))
		stats.Errors++
		return
	}
	for _, e := range due {
		if err := r.deps.Writer.Do(ctx, func(tx *sql.Tx) error {
			return r.deps.EmailsRepo.MarkMovedToS3Only(ctx, tx, e.ID, now)
		}); err != nil {
			r.deps.Logger.Error("retention: marking email moved to s3-only failed", zap.Int64("email_id", e.ID), zap.Error(err))
			stats.Errors++
			continue
		}
		if e.LocalPath != "" {
			if err := os.Remove(e.LocalPath); err != nil && !os.IsNotExist(err) {
				r.deps.Logger.Warn("retention: removing local file after Stage A->B eviction failed", zap.Int64("email_id", e.ID), zap.String("path", e.LocalPath), zap.Error(err))
			}
		}
		stats.MovedToS3Only++
	}
}

// deleteDirectNoBackup implements the S3-not-configured Stage A path:
// there's no mirror to fall back to, so retention_local_days triggers
// outright deletion.
func (r *Runner) deleteDirectNoBackup(ctx context.Context, accountID int64, cutoff time.Time, stats *Stats) {
	due, err := r.deps.EmailsRepo.ListLocalDueForDirectDelete(ctx, accountID, cutoff)
	if err != nil {
		r.deps.Logger.Error("retention: listing direct-delete candidates failed", zap.Int64("account_id", accountID), zap.Error(err))
		stats.Errors++
		return
	}
	for _, e := range due {
		if err := r.deps.Writer.Do(ctx, func(tx *sql.Tx) error {
			return r.deps.EmailsRepo.DeleteCompletely(ctx, tx, e.ID)
		}); err != nil {
			r.deps.Logger.Error("retention: deleting email failed", zap.Int64("email_id", e.ID), zap.Error(err))
			stats.Errors++
			continue
		}
		if e.LocalPath != "" {
			if err := os.Remove(e.LocalPath); err != nil && !os.IsNotExist(err) {
				r.deps.Logger.Warn("retention: removing local file after direct delete failed", zap.Int64("email_id", e.ID), zap.String("path", e.LocalPath), zap.Error(err))
			}
		}
		if idx := r.index(); idx != nil {
			if err := idx.Delete(e.ID); err != nil {
				r.deps.Logger.Warn("retention: removing search index entry failed", zap.Int64("email_id", e.ID), zap.Error(err))
			}
		}
		stats.DeletedDirect++
	}
}

// deleteFromS3 implements Stage B -> C: remove the S3 object, then the
// row. Deleting from S3 is idempotent (s3store.Client.Delete treats an
// already-absent key as success), so a partial failure on a previous run
// — S3 delete succeeded but the row survived, or vice versa — safely
// resolves itself on the next run rather than needing special recovery.
func (r *Runner) deleteFromS3(ctx context.Context, accountID int64, cutoff time.Time, client *s3store.Client, stats *Stats) {
	due, err := r.deps.EmailsRepo.ListS3OnlyDueForDeletion(ctx, accountID, cutoff)
	if err != nil {
		r.deps.Logger.Error("retention: listing Stage B->C candidates failed", zap.Int64("account_id", accountID), zap.Error(err))
		stats.Errors++
		return
	}
	for _, e := range due {
		if e.S3Key != "" && client != nil {
			if err := client.Delete(ctx, e.S3Key); err != nil {
				r.deps.Logger.Error("retention: deleting s3 object failed, skipping this email for now", zap.Int64("email_id", e.ID), zap.String("s3_key", e.S3Key), zap.Error(err))
				stats.Errors++
				continue
			}
		}
		if err := r.deps.Writer.Do(ctx, func(tx *sql.Tx) error {
			return r.deps.EmailsRepo.DeleteCompletely(ctx, tx, e.ID)
		}); err != nil {
			r.deps.Logger.Error("retention: deleting email row failed", zap.Int64("email_id", e.ID), zap.Error(err))
			stats.Errors++
			continue
		}
		if idx := r.index(); idx != nil {
			if err := idx.Delete(e.ID); err != nil {
				r.deps.Logger.Warn("retention: removing search index entry failed", zap.Int64("email_id", e.ID), zap.Error(err))
			}
		}
		stats.DeletedFromS3++
	}
}
