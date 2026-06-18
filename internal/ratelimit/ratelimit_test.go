package ratelimit

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestAllowPerKeyIsolation(t *testing.T) {
	l := New(2) // 2 rps, burst 2
	now := time.Now()
	if !l.Allow("a", now) || !l.Allow("a", now) {
		t.Fatal("requests within burst must be admitted")
	}
	if l.Allow("a", now) {
		t.Fatal("request over budget must be denied")
	}
	if !l.Allow("b", now) {
		t.Fatal("an exhausted key must not affect other keys")
	}
}

func TestAllowRefill(t *testing.T) {
	l := New(1)
	now := time.Now()
	if !l.Allow("k", now) {
		t.Fatal("first request must pass")
	}
	if l.Allow("k", now) {
		t.Fatal("second immediate request must be denied at 1 rps")
	}
	if !l.Allow("k", now.Add(1100*time.Millisecond)) {
		t.Fatal("token must refill after ~1s")
	}
}

func TestDisabledLimiterAlwaysAllows(t *testing.T) {
	for _, l := range []*Limiter{New(0), New(-1), nil} {
		for i := 0; i < 50; i++ {
			if !l.Allow("k", time.Now()) {
				t.Fatal("disabled limiter must always allow")
			}
		}
	}
}

func TestBucketCountIsBounded(t *testing.T) {
	l := New(1)
	now := time.Now()
	for i := 0; i < maxKeys+10; i++ {
		l.Allow(fmt.Sprintf("k%d", i), now)
	}
	l.mu.Lock()
	n := len(l.buckets)
	l.mu.Unlock()
	if n > maxKeys {
		t.Fatalf("bucket map must stay bounded: %d > %d", n, maxKeys)
	}
}

func TestEvictionKeepsRecentlyUsedKeyThrottled(t *testing.T) {
	l := New(1) // 1 rps, burst 1
	now := time.Now()

	// Exhaust a legitimate hot key.
	if !l.Allow("real", now) {
		t.Fatal("first request for hot key should pass")
	}
	if l.Allow("real", now) {
		t.Fatal("hot key should be throttled after consuming its single token")
	}

	// An attacker churns far more than maxKeys distinct fake credentials while
	// the legitimate key keeps receiving traffic. LRU eviction must drop the
	// cold attacker keys, never the hot one — so the hot key stays throttled.
	for i := 0; i < maxKeys*2; i++ {
		l.Allow(fmt.Sprintf("fake-%d", i), now)
		if i%10 == 0 {
			if l.Allow("real", now) {
				t.Fatalf("hot key's bucket was reset by eviction churn at i=%d", i)
			}
		}
	}
}

func TestMiddlewareReturns429WithRetryAfter(t *testing.T) {
	l := New(1)
	var hits int
	h := Middleware(l, "write", nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.WriteHeader(http.StatusAccepted)
	}))

	send := func(key string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/v1/events", nil)
		if key != "" {
			req.Header.Set("Authorization", "Bearer "+key)
		}
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		return rr
	}

	if rr := send("key1"); rr.Code != http.StatusAccepted {
		t.Fatalf("first request: %d", rr.Code)
	}
	rr := send("key1")
	if rr.Code != http.StatusTooManyRequests {
		t.Fatalf("second request must be throttled, got %d", rr.Code)
	}
	if rr.Header().Get("Retry-After") != "1" {
		t.Fatalf("429 must carry Retry-After: 1, got %q", rr.Header().Get("Retry-After"))
	}
	if rr := send("key2"); rr.Code != http.StatusAccepted {
		t.Fatalf("other key must not be throttled: %d", rr.Code)
	}
	if hits != 2 {
		t.Fatalf("handler hits = %d, want 2", hits)
	}
}

func TestMiddlewareNilLimiterPassesThrough(t *testing.T) {
	h := Middleware(nil, "read", nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	for i := 0; i < 20; i++ {
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/", nil))
		if rr.Code != http.StatusOK {
			t.Fatalf("nil limiter must pass through, got %d", rr.Code)
		}
	}
}

func TestKeyFromRequestFallsBackToClientIP(t *testing.T) {
	bearer := httptest.NewRequest(http.MethodGet, "/", nil)
	bearer.Header.Set("Authorization", "Bearer tok123")
	if got := keyFromRequest(bearer); got != "tok123" {
		t.Fatalf("bearer key = %q", got)
	}

	apiKey := httptest.NewRequest(http.MethodGet, "/", nil)
	apiKey.Header.Set("X-API-Key", "xk1")
	if got := keyFromRequest(apiKey); got != "xk1" {
		t.Fatalf("x-api-key = %q", got)
	}

	anon := httptest.NewRequest(http.MethodGet, "/", nil)
	anon.RemoteAddr = "10.1.2.3:5544"
	if got := keyFromRequest(anon); got != "10.1.2.3" {
		t.Fatalf("ip fallback = %q", got)
	}
}
