package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const cookieName = "waylog_session"

type SessionManager struct {
	secret []byte
	maxAge time.Duration
	Secure bool
}

func NewSessionManager(secret []byte, maxAge time.Duration) *SessionManager {
	return &SessionManager{secret: secret, maxAge: maxAge}
}

func (sm *SessionManager) SetSession(w http.ResponseWriter) {
	ts := time.Now().Unix()
	payload := fmt.Sprintf("%d", ts)
	sig := sm.sign(payload)
	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Value:    payload + "." + sig,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(sm.maxAge.Seconds()),
		Secure:   sm.Secure,
	})
}

func (sm *SessionManager) ValidSession(r *http.Request) bool {
	c, err := r.Cookie(cookieName)
	if err != nil {
		return false
	}
	parts := strings.SplitN(c.Value, ".", 2)
	if len(parts) != 2 {
		return false
	}
	payload, sig := parts[0], parts[1]

	if !hmac.Equal([]byte(sm.sign(payload)), []byte(sig)) {
		return false
	}

	ts, err := strconv.ParseInt(payload, 10, 64)
	if err != nil {
		return false
	}
	return time.Since(time.Unix(ts, 0)) < sm.maxAge
}

func (sm *SessionManager) ClearSession(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		MaxAge:   -1,
	})
}

func (sm *SessionManager) sign(payload string) string {
	mac := hmac.New(sha256.New, sm.secret)
	mac.Write([]byte(payload))
	return hex.EncodeToString(mac.Sum(nil))
}

// DeriveSecret produces a deterministic secret from auth credentials (dev-only fallback).
func DeriveSecret(credentials string) []byte {
	h := sha256.Sum256([]byte("waylog-session:" + credentials))
	return h[:]
}
