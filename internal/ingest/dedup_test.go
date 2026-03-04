package ingest

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestDedupCache_Miss(t *testing.T) {
	c := NewDedupCache()
	entry, conflict := c.Check("POST", "/v1/tools/x", "p1", "key1", []byte(`{}`))
	if entry != nil || conflict {
		t.Fatal("expected miss")
	}
}

func TestDedupCache_HitAfterStore(t *testing.T) {
	c := NewDedupCache()
	body := []byte(`{"x":1}`)
	c.Store("POST", "/v1/tools/x", "p1", "key1", body, 200, map[string]int{"y": 2}, nil)

	entry, conflict := c.Check("POST", "/v1/tools/x", "p1", "key1", body)
	if conflict {
		t.Fatal("unexpected conflict")
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

	_, conflict := c.Check("POST", "/v1/tools/x", "p1", "key1", []byte(`{"b":2}`))
	if !conflict {
		t.Fatal("expected conflict for different body")
	}
}

func TestDedupCache_CrossPrincipalIsolation(t *testing.T) {
	c := NewDedupCache()
	body := []byte(`{}`)
	c.Store("POST", "/v1/tools/x", "principal1", "key1", body, 200, "data1", nil)

	entry, _ := c.Check("POST", "/v1/tools/x", "principal2", "key1", body)
	if entry != nil {
		t.Fatal("expected miss for different principal")
	}
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

	entry, _ := c.Check("POST", "/v1/tools/x", "p1", "key1", body)
	if entry != nil {
		t.Fatal("expected miss after expiry")
	}
}

func TestDedupCache_NonCacheableStatus(t *testing.T) {
	c := NewDedupCache()
	body := []byte(`{}`)
	c.Store("POST", "/v1/tools/x", "p1", "key1", body, 500, nil, nil)

	entry, _ := c.Check("POST", "/v1/tools/x", "p1", "key1", body)
	if entry != nil {
		t.Fatal("500 should not be cached")
	}
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

	entry1, _ := c.Check("POST", "/v1/tools/x", "p1", "key1", body)
	entry2, _ := c.Check("POST", "/v1/tools/x", "p1", "key1", body)

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
	c.Complete("POST", "/v1/tools/x", "p1", "key1", body, 200, map[string]string{"ok": "yes"}, nil)

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
	c.Complete("POST", "/v1/tools/x", "p1", "key1", []byte(`{"a":1}`), 200, nil, nil)

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
	c.Complete("POST", "/v1/tools/x", "p1", "key1", body, 200, nil, nil)
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
