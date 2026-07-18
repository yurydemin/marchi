package s3store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"os"
	"sync"
	"time"

	"go.uber.org/zap"

	"github.com/yurydemin/marchi/internal/db/repo"
	"github.com/yurydemin/marchi/internal/domain"
)

// Upload queue tuning (FR-S3-06): base-2 exponential backoff, capped at
// maxBackoff, giving up permanently after maxAttempts failed tries.
const (
	DefaultWorkers    = 4
	maxAttempts       = 5
	backoffBase       = 2 * time.Second
	maxBackoff        = time.Hour
	defaultPollPeriod = 5 * time.Second
	defaultBatchSize  = 20
)

// UploaderDeps bundles what the upload queue worker pool needs: a
// ready-to-use S3 Client (already pointed at the configured bucket) and
// the Master Key to derive each object's client-side encryption subkey
// from (FR-S3-05).
type UploaderDeps struct {
	Client     *Client
	QueueRepo  *repo.S3UploadQueueRepo
	MasterKey  []byte
	Logger     *zap.Logger
	Workers    int           // 0 defaults to DefaultWorkers
	PollPeriod time.Duration // 0 defaults to defaultPollPeriod
	BatchSize  int           // 0 defaults to defaultBatchSize
	// OnError, if set, is called for every failed upload attempt — the
	// hook internal/httpapi uses to broadcast a WebSocket notification
	// (FR-S3-06: "WebSocket-уведомления об ошибках загрузки").
	OnError func(item *domain.S3UploadQueueItem, err error)
}

// Uploader is the S3 mirror upload queue's worker pool: one dispatcher
// goroutine polls s3_upload_queue for claimable rows (FR-S3-06's async
// queue) and fans them out to Workers goroutines that do the actual
// encrypt-then-PUT, entirely outside of any DB transaction — only the
// claim and the post-upload result recording touch the Single Writer.
type Uploader struct {
	deps UploaderDeps

	stop   chan struct{}
	done   chan struct{}
	wg     sync.WaitGroup
	logger *zap.Logger
}

// NewUploader builds an Uploader. It does not start polling until Start
// is called.
func NewUploader(deps UploaderDeps) *Uploader {
	if deps.Workers < 1 {
		deps.Workers = DefaultWorkers
	}
	if deps.PollPeriod <= 0 {
		deps.PollPeriod = defaultPollPeriod
	}
	if deps.BatchSize < 1 {
		deps.BatchSize = defaultBatchSize
	}
	logger := deps.Logger
	if logger == nil {
		logger = zap.NewNop()
	}
	return &Uploader{deps: deps, stop: make(chan struct{}), done: make(chan struct{}), logger: logger}
}

// Start launches the dispatcher and worker goroutines. It returns
// immediately; call Stop to shut them down.
func (u *Uploader) Start(ctx context.Context) {
	items := make(chan *domain.S3UploadQueueItem)

	u.wg.Add(1)
	go func() {
		defer u.wg.Done()
		defer close(items)
		u.dispatch(ctx, items)
	}()

	for i := 0; i < u.deps.Workers; i++ {
		u.wg.Add(1)
		go func() {
			defer u.wg.Done()
			for item := range items {
				u.process(ctx, item)
			}
		}()
	}

	go func() {
		u.wg.Wait()
		close(u.done)
	}()
}

// Stop signals the dispatcher to stop claiming new work and waits for any
// upload already in flight to finish.
func (u *Uploader) Stop() {
	close(u.stop)
	<-u.done
}

// dispatch polls QueueRepo.ClaimBatch on PollPeriod and feeds claimed
// items to the worker goroutines via items. It stops on ctx cancellation
// or Stop being called.
func (u *Uploader) dispatch(ctx context.Context, items chan<- *domain.S3UploadQueueItem) {
	ticker := time.NewTicker(u.deps.PollPeriod)
	defer ticker.Stop()

	poll := func() {
		claimed, err := u.deps.QueueRepo.ClaimBatch(ctx, u.deps.BatchSize)
		if err != nil {
			u.logger.Warn("s3store: claiming upload queue batch failed", zap.Error(err))
			return
		}
		for _, item := range claimed {
			select {
			case items <- item:
			case <-ctx.Done():
				return
			case <-u.stop:
				return
			}
		}
	}

	poll() // don't wait a full PollPeriod before the first claim
	for {
		select {
		case <-ctx.Done():
			return
		case <-u.stop:
			return
		case <-ticker.C:
			poll()
		}
	}
}

// process uploads a single queue item: read the local .eml, encrypt it
// (FR-S3-05), PUT it, and record the result. A failure computes the next
// exponential-backoff attempt time and reports it via MarkFailed/OnError
// rather than propagating — one item's failure must never stop the
// dispatcher or other workers.
func (u *Uploader) process(ctx context.Context, item *domain.S3UploadQueueItem) {
	err := u.upload(ctx, item)
	if err == nil {
		return
	}

	retryCount := item.RetryCount + 1
	delay := backoff(retryCount)
	if markErr := u.deps.QueueRepo.MarkFailed(ctx, item.ID, retryCount, maxAttempts, err.Error(), time.Now().Add(delay)); markErr != nil {
		u.logger.Error("s3store: recording failed upload attempt", zap.Int64("queue_id", item.ID), zap.Error(markErr))
	}
	u.logger.Warn("s3store: upload attempt failed", zap.Int64("queue_id", item.ID), zap.Int("retry_count", retryCount), zap.Error(err))
	if u.deps.OnError != nil {
		u.deps.OnError(item, err)
	}
}

func (u *Uploader) upload(ctx context.Context, item *domain.S3UploadQueueItem) error {
	plaintext, err := os.ReadFile(item.LocalPath)
	if err != nil {
		return fmt.Errorf("s3store: reading %q: %w", item.LocalPath, err)
	}

	body, metadata, err := EncryptObject(u.deps.MasterKey, plaintext)
	if err != nil {
		return err
	}

	etag, err := u.deps.Client.Put(ctx, item.S3Key, bytes.NewReader(body), metadata)
	if err != nil {
		return err
	}

	sum := sha256.Sum256(plaintext)
	if err := u.deps.QueueRepo.MarkDone(ctx, item, etag, hex.EncodeToString(sum[:])); err != nil {
		return fmt.Errorf("s3store: recording completed upload: %w", err)
	}
	return nil
}

// backoff returns min(backoffBase * 2^(retryCount-1), maxBackoff) — the
// first retry waits backoffBase, doubling each attempt after that
// (FR-S3-06: "экспоненциальным backoff, base 2, max delay 1 час").
func backoff(retryCount int) time.Duration {
	if retryCount < 1 {
		retryCount = 1
	}
	d := float64(backoffBase) * math.Pow(2, float64(retryCount-1))
	if d > float64(maxBackoff) {
		return maxBackoff
	}
	return time.Duration(d)
}
