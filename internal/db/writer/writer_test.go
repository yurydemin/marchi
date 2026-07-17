package writer

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("opening in-memory db: %v", err)
	}
	// SQLite :memory: databases are per-connection; without this, a pooled
	// second connection would see an empty database of its own.
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })

	if _, err := db.Exec(`CREATE TABLE counter (id INTEGER PRIMARY KEY, value INTEGER NOT NULL)`); err != nil {
		t.Fatalf("creating counter table: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO counter (id, value) VALUES (1, 0)`); err != nil {
		t.Fatalf("seeding counter: %v", err)
	}
	return db
}

// TestSingleWriter_NoLostUpdates is this step's demo: N goroutines
// concurrently increment a shared counter via read-then-write across two
// separate statements (deliberately not a single atomic UPDATE), which
// would race under naive concurrent access. If Do() correctly serializes
// every call, the final count is exactly N regardless of how many
// goroutines raced to get there.
func TestSingleWriter_NoLostUpdates(t *testing.T) {
	db := openTestDB(t)
	w := New(db)
	defer w.Close()

	const n = 200
	var wg sync.WaitGroup
	errCh := make(chan error, n)

	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := w.Do(context.Background(), func(tx *sql.Tx) error {
				var value int
				if err := tx.QueryRow(`SELECT value FROM counter WHERE id = 1`).Scan(&value); err != nil {
					return err
				}
				_, err := tx.Exec(`UPDATE counter SET value = ? WHERE id = 1`, value+1)
				return err
			})
			if err != nil {
				errCh <- err
			}
		}()
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Errorf("Do returned error: %v", err)
	}

	var final int
	if err := db.QueryRow(`SELECT value FROM counter WHERE id = 1`).Scan(&final); err != nil {
		t.Fatalf("reading final value: %v", err)
	}
	if final != n {
		t.Errorf("final counter = %d, want %d (lost updates under concurrent access)", final, n)
	}
}

// TestSingleWriter_SerializesConcurrentCalls proves the serialization
// guarantee directly, independent of SQLite: no two fn invocations ever
// overlap in time, no matter how many goroutines call Do concurrently.
func TestSingleWriter_SerializesConcurrentCalls(t *testing.T) {
	db := openTestDB(t)
	w := New(db)
	defer w.Close()

	var running int32
	var overlapDetected int32
	const n = 100
	var wg sync.WaitGroup

	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = w.Do(context.Background(), func(tx *sql.Tx) error {
				if atomic.AddInt32(&running, 1) != 1 {
					atomic.StoreInt32(&overlapDetected, 1)
				}
				time.Sleep(time.Millisecond)
				atomic.AddInt32(&running, -1)
				return nil
			})
		}()
	}
	wg.Wait()

	if atomic.LoadInt32(&overlapDetected) != 0 {
		t.Error("detected overlapping fn execution — writer did not serialize calls")
	}
}

func TestSingleWriter_CommitsOnSuccess(t *testing.T) {
	db := openTestDB(t)
	w := New(db)
	defer w.Close()

	err := w.Do(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE counter SET value = 42 WHERE id = 1`)
		return err
	})
	if err != nil {
		t.Fatalf("Do: %v", err)
	}

	var value int
	if err := db.QueryRow(`SELECT value FROM counter WHERE id = 1`).Scan(&value); err != nil {
		t.Fatal(err)
	}
	if value != 42 {
		t.Errorf("value = %d, want 42 (commit didn't take)", value)
	}
}

func TestSingleWriter_RollsBackOnError(t *testing.T) {
	db := openTestDB(t)
	w := New(db)
	defer w.Close()

	sentinel := errors.New("boom")
	err := w.Do(context.Background(), func(tx *sql.Tx) error {
		if _, err := tx.Exec(`UPDATE counter SET value = 999 WHERE id = 1`); err != nil {
			return err
		}
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Errorf("Do error = %v, want sentinel", err)
	}

	var value int
	if err := db.QueryRow(`SELECT value FROM counter WHERE id = 1`).Scan(&value); err != nil {
		t.Fatal(err)
	}
	if value != 0 {
		t.Errorf("value = %d, want 0 (failed fn should have rolled back)", value)
	}
}

func TestSingleWriter_CloseRejectsNewWork(t *testing.T) {
	db := openTestDB(t)
	w := New(db)

	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	err := w.Do(context.Background(), func(tx *sql.Tx) error { return nil })
	if !errors.Is(err, ErrClosed) {
		t.Errorf("Do after Close = %v, want ErrClosed", err)
	}
}

func TestSingleWriter_CloseIsIdempotent(t *testing.T) {
	db := openTestDB(t)
	w := New(db)

	if err := w.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}

func TestSingleWriter_ContextCancellation(t *testing.T) {
	db := openTestDB(t)
	w := New(db)
	defer w.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := w.Do(ctx, func(tx *sql.Tx) error {
		t.Error("fn should not run for an already-cancelled context")
		return nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("Do with cancelled ctx = %v, want context.Canceled", err)
	}
}

func TestSingleWriter_CloseWaitsForInFlightWork(t *testing.T) {
	db := openTestDB(t)
	w := New(db)

	started := make(chan struct{})
	finished := make(chan struct{})
	go func() {
		_ = w.Do(context.Background(), func(tx *sql.Tx) error {
			close(started)
			time.Sleep(50 * time.Millisecond)
			close(finished)
			return nil
		})
	}()

	<-started
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	select {
	case <-finished:
	default:
		t.Error("Close returned before in-flight transaction finished")
	}
}
