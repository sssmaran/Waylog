package ingest

import (
	"container/list"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"sync"
	"time"
)

const (
	dedupMaxEntries    = 1000
	dedupEvictInterval = 30 * time.Second
)

type dedupEntry struct {
	Status    int
	Data      any
	Err       *APIError
	BodyHash  [32]byte
	ExpiresAt time.Time
}

type dedupCacheEntry struct {
	key   string
	entry *dedupEntry
}

type inflight struct {
	done     chan struct{}
	bodyHash [32]byte
	entry    *dedupEntry
}

// DedupCache provides idempotency and inflight deduplication.
type DedupCache struct {
	mu      sync.Mutex
	entries map[string]*list.Element // cache key -> list element
	order   *list.List               // LRU order
	pending map[string]*inflight     // in-flight requests
}

// NewDedupCache creates a new idempotency cache.
func NewDedupCache() *DedupCache {
	return &DedupCache{
		entries: make(map[string]*list.Element),
		order:   list.New(),
		pending: make(map[string]*inflight),
	}
}

// StartEviction runs periodic cache eviction.
func (c *DedupCache) StartEviction(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(dedupEvictInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				c.evictExpired()
			}
		}
	}()
}

func (c *DedupCache) evictExpired() {
	now := time.Now()
	c.mu.Lock()
	defer c.mu.Unlock()
	for e := c.order.Front(); e != nil; {
		next := e.Next()
		ce := e.Value.(*dedupCacheEntry)
		if ce.entry.ExpiresAt.Before(now) {
			delete(c.entries, ce.key)
			c.order.Remove(e)
		}
		e = next
	}
}

func dedupCacheKey(method, path, principal, idempotencyKey string) string {
	h := sha256.New()
	h.Write([]byte(method))
	h.Write([]byte{0})
	h.Write([]byte(path))
	h.Write([]byte{0})
	h.Write([]byte(principal))
	h.Write([]byte{0})
	h.Write([]byte(idempotencyKey))
	return hex.EncodeToString(h.Sum(nil))
}

func dedupTTL(status int) time.Duration {
	switch {
	case status == http.StatusOK:
		return 5 * time.Minute
	case status == http.StatusBadRequest:
		return 5 * time.Minute
	case status == http.StatusNotFound:
		return 30 * time.Second
	case status == http.StatusUnprocessableEntity:
		return 5 * time.Minute
	default:
		return 0 // don't cache 409/429/500+
	}
}

func isCacheable(status int) bool {
	return dedupTTL(status) > 0
}

// Check looks up the cache for an existing entry.
// Returns (entry, false) on hit+match, (nil, true) on body conflict, (nil, false) on miss.
func (c *DedupCache) Check(method, path, principal, key string, body []byte) (*dedupEntry, bool) {
	cacheKey := dedupCacheKey(method, path, principal, key)
	bodyHash := sha256.Sum256(body)

	c.mu.Lock()
	defer c.mu.Unlock()

	elem, ok := c.entries[cacheKey]
	if !ok {
		return nil, false
	}
	ce := elem.Value.(*dedupCacheEntry)
	if ce.entry.ExpiresAt.Before(time.Now()) {
		delete(c.entries, cacheKey)
		c.order.Remove(elem)
		return nil, false
	}
	if ce.entry.BodyHash != bodyHash {
		return nil, true // conflict
	}
	c.order.MoveToBack(elem)
	return deepCopyEntry(ce.entry), false
}

// Store caches a result for future replay.
func (c *DedupCache) Store(method, path, principal, key string, body []byte, status int, data any, apiErr *APIError) {
	ttl := dedupTTL(status)
	if ttl == 0 {
		return
	}
	cacheKey := dedupCacheKey(method, path, principal, key)
	bodyHash := sha256.Sum256(body)

	c.mu.Lock()
	defer c.mu.Unlock()

	entry := &dedupEntry{
		Status:    status,
		Data:      data,
		Err:       apiErr,
		BodyHash:  bodyHash,
		ExpiresAt: time.Now().Add(ttl),
	}

	if elem, ok := c.entries[cacheKey]; ok {
		elem.Value.(*dedupCacheEntry).entry = entry
		c.order.MoveToBack(elem)
		return
	}

	// Enforce LRU max size
	for c.order.Len() >= dedupMaxEntries {
		front := c.order.Front()
		if front == nil {
			break
		}
		ce := front.Value.(*dedupCacheEntry)
		delete(c.entries, ce.key)
		c.order.Remove(front)
	}

	elem := c.order.PushBack(&dedupCacheEntry{key: cacheKey, entry: entry})
	c.entries[cacheKey] = elem
}

// AcquireOrWait either acquires inflight ownership or waits for the first caller.
// Returns:
//   - (nil, false, false): acquired ownership, caller should execute
//   - (entry, false, false): waited and got result (matching body)
//   - (nil, true, false): waited but body hash conflict
//   - (nil, false, true): ctx was canceled/timed out while waiting
func (c *DedupCache) AcquireOrWait(ctx context.Context, method, path, principal, key string, body []byte) (*dedupEntry, bool, bool) {
	cacheKey := dedupCacheKey(method, path, principal, key)
	bodyHash := sha256.Sum256(body)

	c.mu.Lock()
	ifl, exists := c.pending[cacheKey]
	if !exists {
		ifl = &inflight{
			done:     make(chan struct{}),
			bodyHash: bodyHash,
		}
		c.pending[cacheKey] = ifl
		c.mu.Unlock()
		return nil, false, false // caller is the executor
	}
	c.mu.Unlock()

	// Wait for first caller to finish
	select {
	case <-ifl.done:
		if ifl.bodyHash != bodyHash {
			return nil, true, false
		}
		if ifl.entry != nil {
			return deepCopyEntry(ifl.entry), false, false
		}
		// entry nil means Complete was never called or set no entry — treat as miss
		return nil, false, false
	case <-ctx.Done():
		return nil, false, true
	}
}

// Complete finalizes an inflight request and stores in cache if cacheable.
func (c *DedupCache) Complete(method, path, principal, key string, body []byte, status int, data any, apiErr *APIError) {
	cacheKey := dedupCacheKey(method, path, principal, key)
	bodyHash := sha256.Sum256(body)

	c.mu.Lock()
	ifl, exists := c.pending[cacheKey]
	if !exists {
		c.mu.Unlock()
		return
	}

	entry := &dedupEntry{
		Status:   status,
		Data:     data,
		Err:      apiErr,
		BodyHash: bodyHash,
	}
	if isCacheable(status) {
		entry.ExpiresAt = time.Now().Add(dedupTTL(status))
	}
	ifl.entry = entry

	// Store in LRU cache if cacheable
	if isCacheable(status) {
		c.storeLocked(cacheKey, entry)
	}

	close(ifl.done)
	delete(c.pending, cacheKey)
	c.mu.Unlock()
}

func (c *DedupCache) storeLocked(cacheKey string, entry *dedupEntry) {
	if elem, ok := c.entries[cacheKey]; ok {
		elem.Value.(*dedupCacheEntry).entry = entry
		c.order.MoveToBack(elem)
		return
	}
	for c.order.Len() >= dedupMaxEntries {
		front := c.order.Front()
		if front == nil {
			break
		}
		ce := front.Value.(*dedupCacheEntry)
		delete(c.entries, ce.key)
		c.order.Remove(front)
	}
	elem := c.order.PushBack(&dedupCacheEntry{key: cacheKey, entry: entry})
	c.entries[cacheKey] = elem
}

func deepCopyEntry(e *dedupEntry) *dedupEntry {
	if e == nil {
		return nil
	}
	cp := &dedupEntry{
		Status:    e.Status,
		BodyHash:  e.BodyHash,
		ExpiresAt: e.ExpiresAt,
	}
	if e.Data != nil {
		b, _ := json.Marshal(e.Data)
		var d any
		json.Unmarshal(b, &d)
		cp.Data = d
	}
	if e.Err != nil {
		errCp := *e.Err
		cp.Err = &errCp
	}
	return cp
}

// Size returns the number of cached entries (for metrics).
func (c *DedupCache) Size() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.order.Len()
}
