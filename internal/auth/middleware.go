package auth

import (
	"crypto/subtle"
	"log/slog"
	"net/http"
	"strings"
)

// SessionChecker validates dashboard session cookies as an alternative auth path.
type SessionChecker func(r *http.Request) bool

// Middleware returns HTTP middleware that validates API keys for the given scope.
// If keys is empty, all requests pass through (dev mode).
// If sessionCheck is non-nil, a valid session cookie is accepted as alternative auth.
func Middleware(scope string, keys []string, sessionCheck SessionChecker) func(http.Handler) http.Handler {
	keyBytes := make([][]byte, len(keys))
	for i, k := range keys {
		keyBytes[i] = []byte(k)
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if len(keyBytes) == 0 {
				next.ServeHTTP(w, r)
				return
			}

			if token := extractToken(r); token != "" {
				if matchesAny([]byte(token), keyBytes) {
					slog.Debug("auth ok", "scope", scope, "method", "key")
					next.ServeHTTP(w, r)
					return
				}
			}

			if sessionCheck != nil && sessionCheck(r) {
				slog.Debug("auth ok", "scope", scope, "method", "session")
				next.ServeHTTP(w, r)
				return
			}

			slog.Warn("auth failed",
				"scope", scope,
				"path", r.URL.Path,
				"source_ip", sourceIP(r),
				"reason", "no valid credential",
			)
			w.Header().Set("WWW-Authenticate", `Bearer realm="waylog", scope="`+scope+`"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
		})
	}
}

func extractToken(r *http.Request) string {
	if auth := r.Header.Get("Authorization"); auth != "" {
		if idx := strings.IndexByte(auth, ' '); idx > 0 {
			scheme, token := auth[:idx], auth[idx+1:]
			if strings.EqualFold(scheme, "bearer") {
				return token
			}
		}
	}
	return r.Header.Get("X-API-Key")
}

// matchesAny performs constant-time comparison against all keys.
// Uses |= (not early return) so every key is always compared, preventing timing leaks on key position.
func matchesAny(token []byte, keys [][]byte) bool {
	match := 0
	for _, k := range keys {
		match |= subtle.ConstantTimeCompare(token, k)
	}
	return match == 1
}

func sourceIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		if parts := strings.SplitN(xff, ",", 2); len(parts) > 0 {
			return strings.TrimSpace(parts[0])
		}
	}
	return r.RemoteAddr
}
