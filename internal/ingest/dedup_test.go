package ingest

import (
	"context"
	"crypto/sha256"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestDedupCache_Miss(t *testing.T) {
	c := NewDedupCache()
	entry, conflict, canceled := c.AcquireOrWait(context.Background(), "POST", "/v1/tools/x", "p1", "key1", []byte(`{}`))
	if entry != nil || conflict || canceled {
		t.Fatal("expected miss (acquired ownership)")
	}
	// Clean up: we acquired ownership, so complete it
	c.Complete("POST", "/v1/tools/x", "p1", "key1", []byte(`{}`), 200, nil, nil, 0)
}

func TestDedupCache_HitAfterStore(t *testing.T) {
	c := NewDedupCache()
	body := []byte(`{"x":1}`)
	c.Store("POST", "/v1/tools/x", "p1", "key1", body, 200, map[string]int{"y": 2}, nil)

	entry, conflict, canceled := c.AcquireOrWait(context.Background(), "POST", "/v1/tools/x", "p1", "key1", body)
	if conflict || canceled {
		t.Fatal("unexpected conflict or cancel")
	}
	if entry == nil {
		t.Fatal("expected hit")
	}
	if entry.Status != 200 {
		t.Errorf("status = %d, want 200", entry.Status)
	}
}

func TestDedupCache_BodyConflict(t *testing.T) {
	c := NewDedupCache()
	c.Store("POST", "/v1/tools/x", "p1", "key1", []byte(`{"a":1}`), 200, nil, nil)

	_, conflict, _ := c.AcquireOrWait(context.Background(), "POST", "/v1/tools/x", "p1", "key1", []byte(`{"b":2}`))
	if !conflict {
		t.Fatal("expected conflict for different body")
	}
}

func TestDedupCache_CrossPrincipalIsolation(t *testing.T) {
	c := NewDedupCache()
	body := []byte(`{}`)
	c.Store("POST", "/v1/tools/x", "principal1", "key1", body, 200, "data1", nil)

	entry, _, _ := c.AcquireOrWait(context.Background(), "POST", "/v1/tools/x", "principal2", "key1", body)
	if entry != nil {
		t.Fatal("expected miss for different principal")
	}
	// Clean up: we acquired ownership for principal2's key
	c.Complete("POST", "/v1/tools/x", "principal2", "key1", body, 200, nil, nil, 0)
}

func TestDedupCache_Expiry(t *testing.T) {
	c := NewDedupCache()
	body := []byte(`{}`)
	c.Store("POST", "/v1/tools/x", "p1", "key1", body, 404, nil, nil) // 30s TTL

	// Directly expire the entry
	c.mu.Lock()
	for e := c.order.Front(); e != nil; e = e.Next() {
		ce := e.Value.(*dedupCacheEntry)
		ce.entry.ExpiresAt = time.Now().Add(-time.Second)
	}
	c.mu.Unlock()

	entry, _, _ := c.AcquireOrWait(context.Background(), "POST", "/v1/tools/x", "p1", "key1", body)
	if entry != nil {
		t.Fatal("expected miss after expiry")
	}
	// Clean up: we acquired ownership since cache was expired
	c.Complete("POST", "/v1/tools/x", "p1", "key1", body, 200, nil, nil, 0)
}

func TestDedupCache_NonCacheableStatus(t *testing.T) {
	c := NewDedupCache()
	body := []byte(`{}`)
	c.Store("POST", "/v1/tools/x", "p1", "key1", body, 500, nil, nil)

	entry, _, _ := c.AcquireOrWait(context.Background(), "POST", "/v1/tools/x", "p1", "key1", body)
	if entry != nil {
		t.Fatal("500 should not be cached")
	}
	// Clean up: we acquired ownership since 500 was not cached
	c.Complete("POST", "/v1/tools/x", "p1", "key1", body, 200, nil, nil, 0)
}

func TestDedupCache_LRUEviction(t *testing.T) {
	c := NewDedupCache()
	body := []byte(`{}`)
	// Fill to max + 1
	for i := 0; i <= dedupMaxEntries; i++ {
		c.Store("POST", "/v1/tools/x", "p1", string(rune(i)), body, 200, nil, nil)
	}
	if c.Size() > dedupMaxEntries {
		t.Errorf("size = %d, want <= %d", c.Size(), dedupMaxEntries)
	}
}

func TestDedupCache_DeepCopyIsolation(t *testing.T) {
	c := NewDedupCache()
	body := []byte(`{}`)
	data := map[string]int{"count": 1}
	c.Store("POST", "/v1/tools/x", "p1", "key1", body, 200, data, nil)

	entry1, _, _ := c.AcquireOrWait(context.Background(), "POST", "/v1/tools/x", "p1", "key1", body)
	entry2, _, _ := c.AcquireOrWait(context.Background(), "POST", "/v1/tools/x", "p1", "key1", body)

	// Mutate entry1's data — should not affect entry2 or cache
	if m, ok := entry1.Data.(map[string]any); ok {
		m["count"] = 999
	}
	if m, ok := entry2.Data.(map[string]any); ok {
		if m["count"] != float64(1) {
			t.Error("deep copy failed: mutation leaked")
		}
	}
}

func TestDedupCache_InflightDedup(t *testing.T) {
	c := NewDedupCache()
	body := []byte(`{}`)

	// First caller acquires
	entry, conflict, canceled := c.AcquireOrWait(context.Background(), "POST", "/v1/tools/x", "p1", "key1", body)
	if entry != nil || conflict || canceled {
		t.Fatal("first caller should acquire")
	}

	// Second caller waits in background
	var wg sync.WaitGroup
	var entry2 *dedupEntry
	var conflict2, canceled2 bool
	wg.Add(1)
	go func() {
		defer wg.Done()
		entry2, conflict2, canceled2 = c.AcquireOrWait(context.Background(), "POST", "/v1/tools/x", "p1", "key1", body)
	}()

	// First caller completes
	time.Sleep(10 * time.Millisecond) // let goroutine start waiting
	c.Complete("POST", "/v1/tools/x", "p1", "key1", body, 200, map[string]string{"ok": "yes"}, nil, 10)

	wg.Wait()
	if conflict2 || canceled2 {
		t.Fatal("second caller should get result, not conflict")
	}
	if entry2 == nil {
		t.Fatal("expected entry from inflight sharing")
	}
	if entry2.Status != 200 {
		t.Errorf("status = %d, want 200", entry2.Status)
	}
}

func TestDedupCache_InflightBodyConflict(t *testing.T) {
	c := NewDedupCache()

	// First caller acquires with body A
	c.AcquireOrWait(context.Background(), "POST", "/v1/tools/x", "p1", "key1", []byte(`{"a":1}`))

	// Second caller waits with body B
	var wg sync.WaitGroup
	var conflict2 bool
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, conflict2, _ = c.AcquireOrWait(context.Background(), "POST", "/v1/tools/x", "p1", "key1", []byte(`{"b":2}`))
	}()

	time.Sleep(10 * time.Millisecond)
	c.Complete("POST", "/v1/tools/x", "p1", "key1", []byte(`{"a":1}`), 200, nil, nil, 0)

	wg.Wait()
	if !conflict2 {
		t.Fatal("expected body hash conflict for different payload")
	}
}

func TestDedupCache_WaiterCanceled(t *testing.T) {
	c := NewDedupCache()
	body := []byte(`{}`)

	c.AcquireOrWait(context.Background(), "POST", "/v1/tools/x", "p1", "key1", body)

	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	var canceled2 bool
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, _, canceled2 = c.AcquireOrWait(ctx, "POST", "/v1/tools/x", "p1", "key1", body)
	}()

	time.Sleep(10 * time.Millisecond)
	cancel()
	wg.Wait()

	if !canceled2 {
		t.Fatal("expected canceled")
	}

	// Cleanup: complete the inflight
	c.Complete("POST", "/v1/tools/x", "p1", "key1", body, 200, nil, nil, 0)
}

func TestDedupCachePolicy(t *testing.T) {
	tests := []struct {
		status    int
		cacheable bool
	}{
		{200, true},
		{400, true},
		{404, true},
		{422, true},
		{409, false},
		{429, false},
		{500, false},
		{503, false},
	}
	for _, tt := range tests {
		if got := isCacheable(tt.status); got != tt.cacheable {
			t.Errorf("isCacheable(%d) = %v, want %v", tt.status, got, tt.cacheable)
		}
	}
}

func TestDedupCache_AtomicAcquire_NoDoubleExecution(t *testing.T) {
	c := NewDedupCache()
	body := []byte(`{}`)

	executors := int32(0)
	var wg sync.WaitGroup
	const n = 50

	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			entry, conflict, canceled := c.AcquireOrWait(context.Background(), "POST", "/v1/ask", "p1", "key1", body)
			if conflict || canceled {
				return
			}
			if entry != nil {
				return // got cached/waited result
			}
			// We are the executor
			atomic.AddInt32(&executors, 1)
			time.Sleep(5 * time.Millisecond)
			c.Complete("POST", "/v1/ask", "p1", "key1", body, 200, "result", nil, 10)
		}()
	}
	wg.Wait()

	if got := atomic.LoadInt32(&executors); got != 1 {
		t.Errorf("executors = %d, want exactly 1", got)
	}
}

func TestDedupCache_DurationPreserved(t *testing.T) {
	c := NewDedupCache()
	body := []byte(`{}`)

	// Acquire and complete with duration
	c.AcquireOrWait(context.Background(), "POST", "/v1/ask", "p1", "dur-key", body)
	c.Complete("POST", "/v1/ask", "p1", "dur-key", body, 200, "ok", nil, 42)

	// Fetch from cache
	entry, conflict, canceled := c.AcquireOrWait(context.Background(), "POST", "/v1/ask", "p1", "dur-key", body)
	if conflict || canceled || entry == nil {
		t.Fatal("expected cache hit")
	}
	if entry.DurationMs != 42 {
		t.Errorf("DurationMs = %d, want 42", entry.DurationMs)
	}
}

func TestDeepCopyEntry_ErrorOnNonSerializable(t *testing.T) {
	entry := &dedupEntry{
		Status:     200,
		DurationMs: 10,
		Data:       func() {}, // json.Marshal will fail
	}
	_, err := deepCopyEntry(entry)
	if err == nil {
		t.Fatal("expected error for non-serializable Data")
	}
}

func TestDeepCopyEntry_NilAndValid(t *testing.T) {
	// nil entry
	cp, err := deepCopyEntry(nil)
	if err != nil || cp != nil {
		t.Fatalf("nil entry: cp=%v, err=%v", cp, err)
	}

	// Valid entry
	entry := &dedupEntry{
		Status:     200,
		DurationMs: 42,
		Data:       map[string]string{"k": "v"},
		Err:        &APIError{Code: "TEST", Message: "msg"},
	}
	cp, err = deepCopyEntry(entry)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cp.Status != 200 || cp.DurationMs != 42 {
		t.Error("scalar fields not copied")
	}
	if cp.Err == entry.Err {
		t.Error("Err should be a distinct copy")
	}
}

func TestDedupCache_CopyFailureRecovery(t *testing.T) {
	c := NewDedupCache()
	body := []byte(`{}`)

	// Store an entry with non-serializable Data (func value)
	c.mu.Lock()
	cacheKey := dedupCacheKey("POST", "/v1/ask", "p1", "corrupt-key")
	bodyHash := sha256.Sum256(body)
	badEntry := &dedupEntry{
		Status:    200,
		Data:      func() {}, // json.Marshal will fail
		BodyHash:  bodyHash,
		ExpiresAt: time.Now().Add(5 * time.Minute),
	}
	elem := c.order.PushBack(&dedupCacheEntry{key: cacheKey, entry: badEntry})
	c.entries[cacheKey] = elem
	c.mu.Unlock()

	// AcquireOrWait should: find corrupt entry, fail copy, delete it,
	// retry (miss), and acquire executor ownership
	entry, conflict, canceled := c.AcquireOrWait(context.Background(), "POST", "/v1/ask", "p1", "corrupt-key", body)
	if entry != nil {
		t.Fatal("expected nil entry (acquired ownership), got cached result")
	}
	if conflict || canceled {
		t.Fatal("expected neither conflict nor cancel")
	}

	// Corrupt entry should be removed from cache
	if c.Size() != 0 {
		t.Errorf("cache size = %d, want 0 (corrupt entry should be deleted)", c.Size())
	}

	// Complete to prove no stuck pending
	c.Complete("POST", "/v1/ask", "p1", "corrupt-key", body, 200, "recovered", nil, 5)

	// Next call should get the completed result from cache
	entry2, conflict2, canceled2 := c.AcquireOrWait(context.Background(), "POST", "/v1/ask", "p1", "corrupt-key", body)
	if conflict2 || canceled2 {
		t.Fatal("unexpected conflict or cancel on second call")
	}
	if entry2 == nil {
		t.Fatal("expected cache hit after recovery+complete")
	}
	if entry2.Status != 200 {
		t.Errorf("status = %d, want 200", entry2.Status)
	}
}
