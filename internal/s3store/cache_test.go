package s3store

import (
	"bytes"
	"context"
	"path/filepath"
	"testing"
)

func newTestCache(t *testing.T, maxBytes int64) *Cache {
	t.Helper()
	c, err := NewCache(filepath.Join(t.TempDir(), "cache"), maxBytes)
	if err != nil {
		t.Fatalf("NewCache: %v", err)
	}
	return c
}

// TestCache_EvictsByTotalBytesNotEntryCount is this step's demo criterion:
// a byte-budget cache must keep however many small entries fit the
// budget, not cap out at some fixed entry count the way an unwrapped
// hashicorp/golang-lru would.
func TestCache_EvictsByTotalBytesNotEntryCount(t *testing.T) {
	c := newTestCache(t, 100)

	// Five 10-byte entries comfortably fit under a 100-byte budget — a
	// count-based cache with a small fixed capacity would have evicted
	// some of these already.
	for i := 0; i < 5; i++ {
		key := string(rune('a' + i))
		if err := c.Put(key, bytes.Repeat([]byte{byte(i)}, 10)); err != nil {
			t.Fatalf("Put(%q): %v", key, err)
		}
	}
	if got := c.CurrentBytes(); got != 50 {
		t.Fatalf("CurrentBytes = %d, want 50 (5 x 10-byte entries, well under the 100-byte budget)", got)
	}
	for i := 0; i < 5; i++ {
		key := string(rune('a' + i))
		if _, ok := c.Get(key); !ok {
			t.Errorf("Get(%q) missed, want a hit — nothing should have been evicted yet", key)
		}
	}

	// A single 60-byte entry pushes total past 100 and must evict by
	// size (oldest-by-recency first) until it fits — not simply refuse
	// the 6th distinct key the way a count-capped-at-5 cache would.
	if err := c.Put("big", bytes.Repeat([]byte{9}, 60)); err != nil {
		t.Fatalf("Put(big): %v", err)
	}
	if got := c.CurrentBytes(); got > 100 {
		t.Fatalf("CurrentBytes = %d, want <= 100 after eviction", got)
	}
	if _, ok := c.Get("big"); !ok {
		t.Error("Get(big) missed — the entry that triggered eviction should itself survive")
	}
	if _, ok := c.Get("a"); ok {
		t.Error("Get(a) hit — the least-recently-used entry should have been evicted to make room")
	}
}

func TestCache_GetRefreshesRecency(t *testing.T) {
	c := newTestCache(t, 30)

	mustPut := func(key string, size int) {
		t.Helper()
		if err := c.Put(key, bytes.Repeat([]byte{1}, size)); err != nil {
			t.Fatalf("Put(%q): %v", key, err)
		}
	}
	mustPut("a", 10)
	mustPut("b", 10)
	mustPut("c", 10) // cache now exactly full: a, b, c

	// Touch "a" so "b" becomes the least-recently-used entry instead.
	if _, ok := c.Get("a"); !ok {
		t.Fatal("Get(a) missed unexpectedly")
	}

	mustPut("d", 10) // must evict "b", not "a", now that "a" was refreshed

	if _, ok := c.Get("a"); !ok {
		t.Error("Get(a) missed — recently-touched entry should have survived eviction")
	}
	if _, ok := c.Get("b"); ok {
		t.Error("Get(b) hit — least-recently-used entry should have been evicted")
	}
	if _, ok := c.Get("c"); !ok {
		t.Error("Get(c) missed, want a hit")
	}
	if _, ok := c.Get("d"); !ok {
		t.Error("Get(d) missed, want a hit")
	}
}

func TestCache_Get_MissForUnknownKey(t *testing.T) {
	c := newTestCache(t, 100)
	if _, ok := c.Get("never-put"); ok {
		t.Error("Get on an unknown key hit, want a miss")
	}
}

func TestCache_EntryLargerThanBudget_IsNeverRetained(t *testing.T) {
	c := newTestCache(t, 50)
	if err := c.Put("huge", bytes.Repeat([]byte{1}, 200)); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if got := c.CurrentBytes(); got != 0 {
		t.Errorf("CurrentBytes = %d, want 0 (an entry larger than the whole budget can't be retained)", got)
	}
	if _, ok := c.Get("huge"); ok {
		t.Error("Get(huge) hit, want a miss — it should have been evicted immediately")
	}
}

// TestLazyLoader_CacheMissThenHit_AgainstRealMinIO is FR-S3-07's request
// flow end to end: a miss downloads from S3 and decrypts, a subsequent
// request for the same key is served from the cache without another S3
// GET.
func TestLazyLoader_CacheMissThenHit_AgainstRealMinIO(t *testing.T) {
	c := newTestClient(t)
	ctx := context.Background()
	masterKey := randMasterKey(t)

	plaintext := []byte("From: a@example.com\r\nSubject: lazy load test\r\n\r\nbody")
	key := "mailvault/v1/accounts/1/emails/2026/07/19/ca/cafebabe.eml"

	body, meta, err := EncryptObject(masterKey, plaintext)
	if err != nil {
		t.Fatalf("EncryptObject: %v", err)
	}
	if _, err := c.Put(ctx, key, bytes.NewReader(body), meta); err != nil {
		t.Fatalf("Put: %v", err)
	}

	cache := newTestCache(t, 10*1024*1024)
	loader := &LazyLoader{Client: c, Cache: cache, MasterKey: masterKey}

	got, err := loader.Load(ctx, key)
	if err != nil {
		t.Fatalf("Load (miss): %v", err)
	}
	if !bytes.Equal(got, plaintext) {
		t.Errorf("Load (miss) = %q, want %q", got, plaintext)
	}
	if cache.CurrentBytes() != int64(len(plaintext)) {
		t.Errorf("CurrentBytes after Load = %d, want %d", cache.CurrentBytes(), len(plaintext))
	}

	// Delete the object from S3 — a second Load must still succeed
	// because it's now served from the cache, never touching S3 again.
	if err := c.Delete(ctx, key); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	got, err = loader.Load(ctx, key)
	if err != nil {
		t.Fatalf("Load (hit, after S3 deletion): %v", err)
	}
	if !bytes.Equal(got, plaintext) {
		t.Errorf("Load (hit) = %q, want %q", got, plaintext)
	}
}
