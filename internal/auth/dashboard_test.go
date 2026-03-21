package auth

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestDashboardGate_Off_PassesThrough(t *testing.T) {
	gate := DashboardGate(AuthConfig{DashboardMode: "off"}, nil)
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	handler := gate(inner)
	req := httptest.NewRequest("GET", "/ui/", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d, want 200", rec.Code)
	}
}

func TestDashboardGate_Basic_NoSession_ShowsLogin(t *testing.T) {
	cfg := AuthConfig{
		DashboardMode: "basic",
		DashboardUser: "admin",
		DashboardPass: "pass",
		SessionSecret: []byte("secret"),
	}
	sm := NewSessionManager(cfg.SessionSecret, 24*time.Hour)
	gate := DashboardGate(cfg, sm)
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	handler := gate(inner)
	req := httptest.NewRequest("GET", "/ui/", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatal("expected login page")
	}
	if !strings.Contains(rec.Body.String(), "<form") {
		t.Fatal("expected login form in body")
	}
}

func TestDashboardGate_Basic_ValidLogin_SetsCookie(t *testing.T) {
	cfg := AuthConfig{
		DashboardMode: "basic",
		DashboardUser: "admin",
		DashboardPass: "pass",
		SessionSecret: []byte("secret"),
	}
	sm := NewSessionManager(cfg.SessionSecret, 24*time.Hour)
	gate := DashboardGate(cfg, sm)
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	handler := gate(inner)

	form := url.Values{"username": {"admin"}, "password": {"pass"}}
	req := httptest.NewRequest("POST", "/ui/", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("got %d, want 303 redirect", rec.Code)
	}
	cookies := rec.Result().Cookies()
	found := false
	for _, c := range cookies {
		if c.Name == cookieName {
			found = true
		}
	}
	if !found {
		t.Fatal("expected session cookie to be set")
	}
}

func TestDashboardGate_Key_ValidHeader(t *testing.T) {
	cfg := AuthConfig{
		DashboardMode: "key",
		DashboardKey:  "dashkey",
		SessionSecret: []byte("secret"),
	}
	sm := NewSessionManager(cfg.SessionSecret, 24*time.Hour)
	gate := DashboardGate(cfg, sm)
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	handler := gate(inner)

	req := httptest.NewRequest("GET", "/ui/", nil)
	req.Header.Set("Authorization", "Bearer dashkey")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d, want 200", rec.Code)
	}
}

func TestDashboardGate_Key_NoSession_ShowsKeyForm(t *testing.T) {
	cfg := AuthConfig{
		DashboardMode: "key",
		DashboardKey:  "dashkey",
		SessionSecret: []byte("secret"),
	}
	sm := NewSessionManager(cfg.SessionSecret, 24*time.Hour)
	gate := DashboardGate(cfg, sm)
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	handler := gate(inner)
	req := httptest.NewRequest("GET", "/ui/", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if !strings.Contains(rec.Body.String(), `name="key"`) {
		t.Fatal("expected key input field in form")
	}
	if strings.Contains(rec.Body.String(), `name="username"`) {
		t.Fatal("should not show username field in key mode")
	}
}

func TestDashboardGate_Key_FormPost_SetsCookie(t *testing.T) {
	cfg := AuthConfig{
		DashboardMode: "key",
		DashboardKey:  "dashkey",
		SessionSecret: []byte("secret"),
	}
	sm := NewSessionManager(cfg.SessionSecret, 24*time.Hour)
	gate := DashboardGate(cfg, sm)
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	handler := gate(inner)

	form := url.Values{"key": {"dashkey"}}
	req := httptest.NewRequest("POST", "/ui/", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("got %d, want 303 redirect", rec.Code)
	}
	found := false
	for _, c := range rec.Result().Cookies() {
		if c.Name == cookieName {
			found = true
		}
	}
	if !found {
		t.Fatal("expected session cookie to be set")
	}
}
