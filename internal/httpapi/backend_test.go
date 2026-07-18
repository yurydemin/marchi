package httpapi

import (
	"context"
	"crypto/rand"
	"database/sql"
	"io"
	"os"
	"path/filepath"
	"testing"

	"go.uber.org/zap"

	"github.com/yurydemin/marchi/internal/account"
	"github.com/yurydemin/marchi/internal/config"
	"github.com/yurydemin/marchi/internal/domain"
	"github.com/yurydemin/marchi/internal/search"
	"github.com/yurydemin/marchi/internal/security/crypto"
)

func newTestBackend(t *testing.T) *backend {
	t.Helper()
	dataDir := t.TempDir()
	cfg := config.Defaults(dataDir)
	if err := cfg.EnsureDirs(); err != nil {
		t.Fatalf("EnsureDirs: %v", err)
	}

	masterKey := make([]byte, crypto.KeySize)
	if _, err := io.ReadFull(rand.Reader, masterKey); err != nil {
		t.Fatal(err)
	}

	b, err := newBackend(cfg, zap.NewNop(), masterKey, newWSHub())
	if err != nil {
		t.Fatalf("newBackend: %v", err)
	}
	t.Cleanup(func() { b.close(zap.NewNop()) })
	return b
}

func TestBackend_CurrentIndex_ReturnsAWorkingIndex(t *testing.T) {
	b := newTestBackend(t)
	idx := b.currentIndex()
	if idx == nil {
		t.Fatal("currentIndex() = nil")
	}
	if err := idx.Index(search.Doc{EmailID: 1, Subject: "hello"}); err != nil {
		t.Errorf("indexing through currentIndex() failed: %v", err)
	}
}

// TestBackend_RunReindex_SwapsToAWorkingNewIndex confirms runReindex both
// replaces the index object (so a later currentIndex() sees the new one,
// not a stale reference) and leaves it in a genuinely usable state — not
// just a non-nil pointer.
func TestBackend_RunReindex_SwapsToAWorkingNewIndex(t *testing.T) {
	b := newTestBackend(t)
	ctx := context.Background()
	before := b.currentIndex()

	// Seed an account + one archived email so reindex has something real
	// to rebuild from.
	acct, err := b.manager.AddAccount(ctx, account.AddAccountParams{
		Email: "seed@example.com", IMAPHost: "127.0.0.1", IMAPTLS: domain.IMAPTLSNone,
		IMAPPassword: "hunter2hunter2",
	})
	if err != nil {
		t.Fatalf("AddAccount: %v", err)
	}
	folder, err := b.foldersRepo.UpsertFolder(ctx, acct.ID, "INBOX", 1)
	if err != nil {
		t.Fatalf("UpsertFolder: %v", err)
	}

	emlPath := filepath.Join(t.TempDir(), "msg.eml")
	if err := os.WriteFile(emlPath, []byte("Subject: seeded\r\nFrom: a@example.com\r\n\r\nBody.\r\n"), 0o644); err != nil {
		t.Fatalf("writing seed .eml: %v", err)
	}
	err = b.w.Do(ctx, func(tx *sql.Tx) error {
		_, err := b.emailsRepo.Insert(ctx, tx, &domain.Email{
			MessageID: "seed@example.com", AccountID: acct.ID, FolderID: folder.ID, UID: 1,
			StorageLocation: "local", LocalPath: emlPath,
		})
		return err
	})
	if err != nil {
		t.Fatalf("inserting seed email: %v", err)
	}

	stats, err := b.runReindex(ctx, nil)
	if err != nil {
		t.Fatalf("runReindex: %v", err)
	}
	if stats.Total != 1 || stats.Indexed != 1 {
		t.Errorf("stats = %+v, want Total:1 Indexed:1", stats)
	}

	after := b.currentIndex()
	if after == before {
		t.Error("currentIndex() still returns the pre-reindex *search.Index — the swap didn't take")
	}

	if err := after.Index(search.Doc{EmailID: 999, Subject: "post-reindex write"}); err != nil {
		t.Errorf("indexing through the post-reindex index failed: %v", err)
	}
}
