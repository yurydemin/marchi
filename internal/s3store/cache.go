package s3store

import (
	"container/list"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
)

// Cache is FR-S3-07's Lazy Load Cache: a disk-backed LRU keyed by S3
// object key, evicted by total bytes stored rather than entry count
// (поправка #12 — hashicorp/golang-lru only evicts by item count, which
// doesn't fit a "10 GB of arbitrarily-sized decrypted .eml files" budget).
// Entries hold decrypted plaintext — never the S3 ciphertext — so a cache
// hit can be served directly with no further work.
type Cache struct {
	dir      string
	maxBytes int64

	mu       sync.Mutex
	entries  map[string]*list.Element // key -> element in order
	order    *list.List               // front = most recently used
	curBytes int64
}

type cacheItem struct {
	key  string
	size int64
}

// NewCache prepares dir as an empty cache directory (any leftover content
// from a previous process is wiped, the same startup-sweep convention
// internal/maildir uses for tmp/ — this cache's in-memory LRU index only
// ever exists for the current process, so stale files on disk would
// otherwise become permanently untracked and never evicted) and returns a
// Cache with the given byte budget.
func NewCache(dir string, maxBytes int64) (*Cache, error) {
	if err := os.RemoveAll(dir); err != nil {
		return nil, fmt.Errorf("s3store: clearing cache dir %q: %w", dir, err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("s3store: creating cache dir %q: %w", dir, err)
	}
	return &Cache{
		dir:      dir,
		maxBytes: maxBytes,
		entries:  make(map[string]*list.Element),
		order:    list.New(),
	}, nil
}

// filename maps an arbitrary S3 key (which contains '/') to a flat,
// filesystem-safe name — hashing avoids needing to recreate the S3 key's
// directory structure on disk.
func (c *Cache) filename(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])
}

func (c *Cache) path(key string) string {
	return filepath.Join(c.dir, c.filename(key))
}

// Get returns the cached plaintext for key, marking it most-recently-used.
// A miss (never cached, or evicted) returns ok=false.
func (c *Cache) Get(key string) (data []byte, ok bool) {
	c.mu.Lock()
	elem, found := c.entries[key]
	if !found {
		c.mu.Unlock()
		return nil, false
	}
	c.order.MoveToFront(elem)
	c.mu.Unlock()

	data, err := os.ReadFile(c.path(key))
	if err != nil {
		// The file disappeared out from under us (manual deletion, disk
		// issue) — drop the now-stale bookkeeping and report a miss
		// rather than returning a read error the caller has to special-case.
		c.mu.Lock()
		c.removeLocked(key)
		c.mu.Unlock()
		return nil, false
	}
	return data, true
}

// Put stores data under key, evicting the least-recently-used entries
// (by cumulative size, not count) until the cache fits within maxBytes.
// An entry larger than maxBytes on its own is written and then
// immediately evicted by the same loop — it's simply never retained,
// rather than being a special case.
func (c *Cache) Put(key string, data []byte) error {
	if err := os.WriteFile(c.path(key), data, 0o600); err != nil {
		return fmt.Errorf("s3store: writing cache entry: %w", err)
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if elem, ok := c.entries[key]; ok {
		item := elem.Value.(*cacheItem)
		c.curBytes += int64(len(data)) - item.size
		item.size = int64(len(data))
		c.order.MoveToFront(elem)
	} else {
		item := &cacheItem{key: key, size: int64(len(data))}
		c.entries[key] = c.order.PushFront(item)
		c.curBytes += item.size
	}

	c.evictLocked()
	return nil
}

// CurrentBytes returns the cache's current total size — mainly for tests
// and future Dashboard/status reporting.
func (c *Cache) CurrentBytes() int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.curBytes
}

func (c *Cache) evictLocked() {
	for c.curBytes > c.maxBytes && c.order.Len() > 0 {
		back := c.order.Back()
		item := back.Value.(*cacheItem)
		c.order.Remove(back)
		delete(c.entries, item.key)
		c.curBytes -= item.size
		_ = os.Remove(c.path(item.key)) // best-effort — a leaked file just wastes disk, doesn't corrupt state
	}
}

func (c *Cache) removeLocked(key string) {
	elem, ok := c.entries[key]
	if !ok {
		return
	}
	item := elem.Value.(*cacheItem)
	c.order.Remove(elem)
	delete(c.entries, key)
	c.curBytes -= item.size
}

// LazyLoader implements FR-S3-07's request flow: check cache, then
// download from S3, decrypt, populate the cache, and return the
// plaintext — a cache miss costs one S3 GET, a hit costs none.
type LazyLoader struct {
	Client    *Client
	Cache     *Cache
	MasterKey []byte
}

// Load returns the decrypted content of the object at s3Key. A failure to
// populate the cache after a successful download is not itself an error —
// the caller still gets its data; the next request just misses the cache
// again and re-downloads.
func (l *LazyLoader) Load(ctx context.Context, s3Key string) ([]byte, error) {
	if data, ok := l.Cache.Get(s3Key); ok {
		return data, nil
	}

	body, metadata, err := l.Client.Get(ctx, s3Key)
	if err != nil {
		return nil, err
	}
	defer body.Close()

	encrypted, err := io.ReadAll(body)
	if err != nil {
		return nil, fmt.Errorf("s3store: reading %q: %w", s3Key, err)
	}

	plaintext, err := DecryptObject(l.MasterKey, encrypted, metadata)
	if err != nil {
		return nil, err
	}

	_ = l.Cache.Put(s3Key, plaintext)
	return plaintext, nil
}
