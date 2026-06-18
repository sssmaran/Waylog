// Package ratelimit provides a per-key token-bucket limiter for the HTTP
// surface. Requests are keyed by the presented credential (Bearer or
// X-API-Key) so one leaked or misbehaving key cannot starve others; requests
// without a credential are keyed by client IP. Throttling on the *presented*
// credential — valid or not — also slows down key brute-forcing.
package ratelimit

import (
	"container/list"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/sssmaran/WaylogCLI/internal/metrics"
	"golang.org/x/time/rate"
)

// maxKeys bounds the bucket map. When it is full, the least-recently-used key
// is evicted to make room — so a flood of attacker-generated keys drops the
// cold attacker entries rather than wiping the rate state of hot legitimate
// keys.
const maxKeys = 10000

type bucket struct {
	key string
	lim *rate.Limiter
}

// Limiter is a per-key token bucket at rps requests/second with burst = rps,
// backed by a bounded LRU. A nil Limiter or rps <= 0 disables limiting (Allow
// always true).
type Limiter struct {
	rps     int
	mu      sync.Mutex
	lru     *list.List               // *bucket, most-recently-used at front
	buckets map[string]*list.Element // key -> its element in lru
}

func New(rps int) *Limiter {
	if rps <= 0 {
		return nil
	}
	return &Limiter{rps: rps, lru: list.New(), buckets: map[string]*list.Element{}}
}

func (l *Limiter) Allow(key string, now time.Time) bool {
	if l == nil {
		return true
	}
	l.mu.Lock()
	var lim *rate.Limiter
	if el, ok := l.buckets[key]; ok {
		l.lru.MoveToFront(el)
		lim = el.Value.(*bucket).lim
	} else {
		if l.lru.Len() >= maxKeys {
			if oldest := l.lru.Back(); oldest != nil {
				l.lru.Remove(oldest)
				delete(l.buckets, oldest.Value.(*bucket).key)
			}
		}
		lim = rate.NewLimiter(rate.Limit(l.rps), l.rps)
		l.buckets[key] = l.lru.PushFront(&bucket{key: key, lim: lim})
	}
	l.mu.Unlock()
	// lim stays valid even if another goroutine evicts it before AllowN runs.
	return lim.AllowN(now, 1)
}

// Middleware throttles requests through l. On rejection it responds
// 429 + Retry-After: 1 (plain text, matching the auth middleware style)
// and increments the rate-limited counter for the scope.
func Middleware(l *Limiter, scope string, m *metrics.Metrics) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		if l == nil {
			return next
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !l.Allow(keyFromRequest(r), time.Now()) {
				if m != nil {
					m.RateLimited.WithLabelValues(scope).Inc()
				}
				slog.Debug("rate limited", "scope", scope, "path", r.URL.Path)
				w.Header().Set("Retry-After", "1")
				http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func keyFromRequest(r *http.Request) string {
	if auth := r.Header.Get("Authorization"); auth != "" {
		if idx := strings.IndexByte(auth, ' '); idx > 0 && strings.EqualFold(auth[:idx], "bearer") {
			return auth[idx+1:]
		}
	}
	if k := r.Header.Get("X-API-Key"); k != "" {
		return k
	}
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}
