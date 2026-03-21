package auth

import (
	"net/http/httptest"
	"testing"
	"time"
)

func TestSession_CreateAndValidate(t *testing.T) {
	sm := NewSessionManager([]byte("test-secret"), 24*time.Hour)
	rec := httptest.NewRecorder()
	sm.SetSession(rec)

	cookie := rec.Result().Cookies()[0]
	if cookie.Name != "waylog_session" {
		t.Fatalf("cookie name = %q, want waylog_session", cookie.Name)
	}

	req := httptest.NewRequest("GET", "/", nil)
	req.AddCookie(cookie)
	if !sm.ValidSession(req) {
		t.Fatal("expected valid session")
	}
}

func TestSession_Expired(t *testing.T) {
	sm := NewSessionManager([]byte("test-secret"), 1*time.Millisecond)
	rec := httptest.NewRecorder()
	sm.SetSession(rec)

	time.Sleep(5 * time.Millisecond)

	cookie := rec.Result().Cookies()[0]
	req := httptest.NewRequest("GET", "/", nil)
	req.AddCookie(cookie)
	if sm.ValidSession(req) {
		t.Fatal("expected expired session to be invalid")
	}
}

func TestSession_TamperedCookie(t *testing.T) {
	sm := NewSessionManager([]byte("test-secret"), 24*time.Hour)
	rec := httptest.NewRecorder()
	sm.SetSession(rec)

	cookie := rec.Result().Cookies()[0]
	cookie.Value = "tampered" + cookie.Value
	req := httptest.NewRequest("GET", "/", nil)
	req.AddCookie(cookie)
	if sm.ValidSession(req) {
		t.Fatal("expected tampered cookie to be invalid")
	}
}

func TestSession_WrongSecret(t *testing.T) {
	sm1 := NewSessionManager([]byte("secret-1"), 24*time.Hour)
	sm2 := NewSessionManager([]byte("secret-2"), 24*time.Hour)
	rec := httptest.NewRecorder()
	sm1.SetSession(rec)

	cookie := rec.Result().Cookies()[0]
	req := httptest.NewRequest("GET", "/", nil)
	req.AddCookie(cookie)
	if sm2.ValidSession(req) {
		t.Fatal("expected wrong-secret cookie to be invalid")
	}
}

func TestSession_NoCookie(t *testing.T) {
	sm := NewSessionManager([]byte("test-secret"), 24*time.Hour)
	req := httptest.NewRequest("GET", "/", nil)
	if sm.ValidSession(req) {
		t.Fatal("expected no-cookie request to be invalid")
	}
}
