package ingest

import (
	"container/list"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"
)

const (
	dedupMaxEntries    = 1000
	dedupEvictInterval = 30 * time.Second
	dedupCopyRetries   = 2
)

type dedupEntry struct {
	Status     int
	Data       any
	Err        *APIError
	DurationMs int64
	BodyHash   [32]byte
	ExpiresAt  time.Time
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

// AcquireOrWait atomically checks cache, checks/creates inflight, or waits.
// Returns:
//   - (entry, false, false): cache hit or waited and got result
//   - (nil, true, false): body hash conflict (cache or inflight)
//   - (nil, false, true): ctx was canceled/timed out while waiting
//   - (nil, false, false): acquired ownership, caller should execute
func (c *DedupCache) AcquireOrWait(ctx context.Context, method, path, principal, key string, body []byte) (*dedupEntry, bool, bool) {
	cacheKey := dedupCacheKey(method, path, principal, key)
	bodyHash := sha256.Sum256(body)

	for attempt := 0; ; attempt++ {
		c.mu.Lock()

		// 1. Check cache
		if elem, ok := c.entries[cacheKey]; ok {
			ce := elem.Value.(*dedupCacheEntry)
			if ce.entry.ExpiresAt.After(time.Now()) {
				if ce.entry.BodyHash != bodyHash {
					c.mu.Unlock()
					return nil, true, false // conflict
				}
				c.order.MoveToBack(elem)
				raw := ce.entry // capture immutable pointer under lock
				c.mu.Unlock()

				cp, err := deepCopyEntry(raw)
				if err != nil {
					slog.Warn("dedup_cache_copy_failed", "err", err)
					// Conditional delete: only if entry hasn't been replaced
					c.mu.Lock()
					if elem2, ok := c.entries[cacheKey]; ok {
						if elem2.Value.(*dedupCacheEntry).entry == raw {
							delete(c.entries, cacheKey)
							c.order.Remove(elem2)
						}
					}
					c.mu.Unlock()
					if attempt < dedupCopyRetries {
						continue // retry — will miss cache, fall to acquire/pending
					}
					// Exhausted retries — loop once more to acquire/pending under lock
					continue
				} else {
					return cp, false, false // cache hit
				}
			} else {
				// Expired — remove and fall through
				delete(c.entries, cacheKey)
				c.order.Remove(elem)
			}
		}

		// 2. Check/create pending
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

		// 3. Wait for executor
		select {
		case <-ifl.done:
			if ifl.bodyHash != bodyHash {
				return nil, true, false
			}
			if ifl.entry != nil {
				cp, err := deepCopyEntry(ifl.entry)
				if err != nil {
					slog.Warn("dedup_inflight_copy_failed", "err", err)
					return nil, false, false // treat as miss — caller re-executes
				}
				return cp, false, false
			}
			return nil, false, false
		case <-ctx.Done():
			return nil, false, true
		}
	}
}

// Complete finalizes an inflight request and stores in cache if cacheable.
func (c *DedupCache) Complete(method, path, principal, key string, body []byte, status int, data any, apiErr *APIError, durationMs int64) {
	cacheKey := dedupCacheKey(method, path, principal, key)
	bodyHash := sha256.Sum256(body)

	c.mu.Lock()
	ifl, exists := c.pending[cacheKey]
	if !exists {
		c.mu.Unlock()
		return
	}

	entry := &dedupEntry{
		Status:     status,
		Data:       data,
		DurationMs: durationMs,
		Err:        apiErr,
		BodyHash:   bodyHash,
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

func deepCopyEntry(e *dedupEntry) (*dedupEntry, error) {
	if e == nil {
		return nil, nil
	}
	cp := &dedupEntry{
		Status:     e.Status,
		DurationMs: e.DurationMs,
		BodyHash:   e.BodyHash,
		ExpiresAt:  e.ExpiresAt,
	}
	if e.Data != nil {
		b, err := json.Marshal(e.Data)
		if err != nil {
			return nil, fmt.Errorf("marshal dedup entry data: %w", err)
		}
		var d any
		if err := json.Unmarshal(b, &d); err != nil {
			return nil, fmt.Errorf("unmarshal dedup entry data: %w", err)
		}
		cp.Data = d
	}
	if e.Err != nil {
		errCp := *e.Err
		cp.Err = &errCp
	}
	return cp, nil
}

// Size returns the number of cached entries (for metrics).
func (c *DedupCache) Size() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.order.Len()
}
