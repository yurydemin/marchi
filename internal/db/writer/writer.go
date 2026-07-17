// Package writer implements the Single Writer Pattern (FR-ST-02): every
// INSERT/UPDATE/DELETE against SQLite goes through one goroutine and one
// channel, so concurrent Sync Engine/S3 uploader/Rule Engine components
// never contend on WAL writer locks or deadlock against each other.
// Reads bypass this entirely — WAL mode allows concurrent readers, so
// repositories query the pooled *sql.DB directly for SELECTs.
package writer

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// ErrClosed is returned by Do once the Writer has been closed.
var ErrClosed = errors.New("writer: closed")

// Writer serializes SQLite mutations through a single background goroutine.
type Writer interface {
	// Do runs fn inside a transaction on the writer goroutine, committing
	// on success and rolling back if fn returns an error, then returns
	// whatever error occurred (fn's, or a begin/commit/rollback failure).
	// Callers get real transactional grouping — e.g. "insert an email row
	// and bump folders.last_uid" as one atomic write — while every write
	// across the whole process still goes through the same goroutine.
	Do(ctx context.Context, fn func(*sql.Tx) error) error
	// Close stops accepting new work and waits for any in-flight
	// transaction to finish. Safe to call once.
	Close() error
}

type request struct {
	ctx  context.Context
	fn   func(*sql.Tx) error
	done chan error
}

type singleWriter struct {
	db      *sql.DB
	reqs    chan request
	closed  chan struct{}
	stopped chan struct{}
}

// New starts the writer goroutine against db. Callers should route every
// mutation through the returned Writer instead of issuing write
// transactions against db directly — bypassing it defeats the whole point.
func New(db *sql.DB) Writer {
	w := &singleWriter{
		db:      db,
		reqs:    make(chan request),
		closed:  make(chan struct{}),
		stopped: make(chan struct{}),
	}
	go w.run()
	return w
}

func (w *singleWriter) Do(ctx context.Context, fn func(*sql.Tx) error) error {
	done := make(chan error, 1)
	req := request{ctx: ctx, fn: fn, done: done}

	select {
	case w.reqs <- req:
	case <-w.closed:
		return ErrClosed
	case <-ctx.Done():
		return ctx.Err()
	}

	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (w *singleWriter) run() {
	defer close(w.stopped)
	for {
		select {
		case req := <-w.reqs:
			req.done <- w.exec(req)
		case <-w.closed:
			return
		}
	}
}

func (w *singleWriter) exec(req request) error {
	tx, err := w.db.BeginTx(req.ctx, nil)
	if err != nil {
		return fmt.Errorf("writer: beginning transaction: %w", err)
	}

	if err := req.fn(tx); err != nil {
		if rbErr := tx.Rollback(); rbErr != nil {
			return fmt.Errorf("writer: %w (rollback also failed: %w)", err, rbErr)
		}
		return err
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("writer: committing transaction: %w", err)
	}
	return nil
}

func (w *singleWriter) Close() error {
	select {
	case <-w.closed:
		// already closed
	default:
		close(w.closed)
	}
	<-w.stopped
	return nil
}
