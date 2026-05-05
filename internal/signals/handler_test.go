package signals

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestHandlerSignals(t *testing.T) {
	now := time.Date(2026, 5, 2, 18, 0, 0, 0, time.UTC)
	store := &fakeStore{}
	h := NewHandler(store, nil)
	h.now = func() time.Time { return now }
	body := `{"type":"deploy","source":"github","service":"checkout","env":"prod","severity":"info","reason":"RolloutComplete","timestamp":"2026-05-02T17:59:00Z","custom_tag":"foo"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/signals", bytes.NewBufferString(body))
	rec := httptest.NewRecorder()
	h.Signals(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if len(store.inserted) != 1 {
		t.Fatalf("inserted=%d", len(store.inserted))
	}
	if store.inserted[0].SignalID == "" || store.inserted[0].ReceivedAt.IsZero() {
		t.Fatalf("server fields not set: %+v", store.inserted[0])
	}
	if store.inserted[0].Extra["custom_tag"] != "foo" {
		t.Fatalf("extra=%+v", store.inserted[0].Extra)
	}
	var resp struct {
		Signal Signal `json:"signal"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Signal.SignalID != store.inserted[0].SignalID {
		t.Fatalf("response id=%q inserted=%q", resp.Signal.SignalID, store.inserted[0].SignalID)
	}
}

func TestHandlerRejectsInvalidSignals(t *testing.T) {
	now := time.Date(2026, 5, 2, 18, 0, 0, 0, time.UTC)
	tests := []struct {
		name   string
		body   string
		status int
		code   string
	}{
		{name: "invalid json", body: `{`, status: 400, code: CodeInvalidJSON},
		{name: "missing service", body: `{"type":"deploy","source":"github","env":"prod","severity":"info","reason":"RolloutComplete","timestamp":"2026-05-02T17:59:00Z"}`, status: 400, code: CodeInvalidField},
		{name: "unknown type", body: `{"type":"wrong","source":"github","service":"checkout","env":"prod","severity":"info","reason":"RolloutComplete","timestamp":"2026-05-02T17:59:00Z"}`, status: 400, code: CodeUnknownType},
		{name: "unknown severity", body: `{"type":"deploy","source":"github","service":"checkout","env":"prod","severity":"huge","reason":"RolloutComplete","timestamp":"2026-05-02T17:59:00Z"}`, status: 400, code: CodeUnknownSeverity},
		{name: "future", body: `{"type":"deploy","source":"github","service":"checkout","env":"prod","severity":"info","reason":"RolloutComplete","timestamp":"2026-05-02T20:00:00Z"}`, status: 400, code: CodeTimestampTooFarInFuture},
		{name: "non object resource", body: `{"type":"deploy","source":"github","service":"checkout","env":"prod","severity":"info","reason":"RolloutComplete","timestamp":"2026-05-02T17:59:00Z","resource":"bad"}`, status: 400, code: CodeInvalidField},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := NewHandler(&fakeStore{}, nil)
			h.now = func() time.Time { return now }
			req := httptest.NewRequest(http.MethodPost, "/v1/signals", bytes.NewBufferString(tt.body))
			rec := httptest.NewRecorder()
			h.Signals(rec, req)
			assertError(t, rec, tt.status, tt.code)
		})
	}
}

func TestHandlerRejectsMethod(t *testing.T) {
	h := NewHandler(UnavailableStore{}, nil)
	req := httptest.NewRequest(http.MethodGet, "/v1/signals", nil)
	rec := httptest.NewRecorder()
	h.Signals(rec, req)
	assertError(t, rec, http.StatusMethodNotAllowed, CodeUnsupportedMethod)
}

func TestHandlerRejectsOversizeBody(t *testing.T) {
	h := NewHandler(UnavailableStore{}, nil)
	h.maxBodyBytes = 8
	req := httptest.NewRequest(http.MethodPost, "/v1/signals", bytes.NewBufferString(`{"too":"large"}`))
	rec := httptest.NewRecorder()
	h.Signals(rec, req)
	assertError(t, rec, http.StatusRequestEntityTooLarge, CodeBodyOversize)
}

func TestHandlerReportsStoreUnavailable(t *testing.T) {
	h := NewHandler(UnavailableStore{}, nil)
	h.now = func() time.Time { return time.Date(2026, 5, 2, 18, 0, 0, 0, time.UTC) }
	body := `{"type":"deploy","source":"github","service":"checkout","env":"prod","severity":"info","reason":"RolloutComplete","timestamp":"2026-05-02T17:59:00Z"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/signals", bytes.NewBufferString(body))
	rec := httptest.NewRecorder()
	h.Signals(rec, req)
	assertError(t, rec, http.StatusServiceUnavailable, CodeDurabilityUnavailable)
}

func TestHandlerReportsStoreError(t *testing.T) {
	h := NewHandler(&fakeStore{err: errors.New("boom")}, nil)
	h.now = func() time.Time { return time.Date(2026, 5, 2, 18, 0, 0, 0, time.UTC) }
	body := `{"type":"deploy","source":"github","service":"checkout","env":"prod","severity":"info","reason":"RolloutComplete","timestamp":"2026-05-02T17:59:00Z"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/signals", bytes.NewBufferString(body))
	rec := httptest.NewRecorder()
	h.Signals(rec, req)
	assertError(t, rec, http.StatusInternalServerError, CodeInternalError)
}

func assertError(t *testing.T, rec *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	if rec.Code != status {
		t.Fatalf("status=%d want %d body=%s", rec.Code, status, rec.Body.String())
	}
	var resp errorResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Error.Code != code {
		t.Fatalf("code=%q want %q body=%s", resp.Error.Code, code, rec.Body.String())
	}
}

type fakeStore struct {
	inserted []Signal
	err      error
}

func (s *fakeStore) Insert(_ context.Context, sig *Signal) error {
	if s.err != nil {
		return s.err
	}
	s.inserted = append(s.inserted, *sig)
	return nil
}

func (s *fakeStore) Query(context.Context, Filter) ([]Signal, error) {
	return nil, errors.New("unused")
}

func (s *fakeStore) PruneOlderThan(context.Context, time.Time) (int, error) {
	return 0, errors.New("unused")
}
