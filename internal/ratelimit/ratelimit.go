// Package ratelimit provides a per-key token-bucket limiter for the HTTP
// surface. Requests are keyed by the presented credential (Bearer or
// X-API-Key) so one leaked or misbehaving key cannot starve others; requests
// without a credential are keyed by client IP. Throttling on the *presented*
// credential — valid or not — also slows down key brute-forcing.
package ratelimit

import (
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/sssmaran/WaylogCLI/internal/metrics"
	"golang.org/x/time/rate"
)

// maxKeys bounds the bucket map. When exceeded, the map is reset: a brief
// over-admit window after reset is preferable to unbounded memory growth
// from attacker-generated keys.
const maxKeys = 10000

// Limiter is a per-key token bucket at rps requests/second with burst = rps.
// A nil Limiter or rps <= 0 disables limiting (Allow always true).
type Limiter struct {
	rps     int
	mu      sync.Mutex
	buckets map[string]*rate.Limiter
}

func New(rps int) *Limiter {
	if rps <= 0 {
		return nil
	}
	return &Limiter{rps: rps, buckets: map[string]*rate.Limiter{}}
}

func (l *Limiter) Allow(key string, now time.Time) bool {
	if l == nil {
		return true
	}
	l.mu.Lock()
	b, ok := l.buckets[key]
	if !ok {
		if len(l.buckets) >= maxKeys {
			l.buckets = map[string]*rate.Limiter{}
		}
		b = rate.NewLimiter(rate.Limit(l.rps), l.rps)
		l.buckets[key] = b
	}
	l.mu.Unlock()
	return b.AllowN(now, 1)
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
