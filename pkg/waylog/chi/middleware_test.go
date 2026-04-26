package waylogchi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	eventv2 "github.com/sssmaran/WaylogCLI/pkg/event/v2"
	waylogv2 "github.com/sssmaran/WaylogCLI/pkg/waylog/v2"
)

const schemaPath = "../../../docs/schema/v2.0.json"

type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) Bytes() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]byte(nil), b.buf.Bytes()...)
}

func newHarness(t *testing.T, cfg waylogv2.Config) *lockedBuffer {
	t.Helper()
	deadline, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	_ = waylogv2.Shutdown(deadline)

	buf := &lockedBuffer{}
	if cfg.Service == "" {
		cfg.Service = "checkout"
	}
	if cfg.Env == "" {
		cfg.Env = "test"
	}
	cfg.Output = buf
	if err := waylogv2.Init(cfg); err != nil {
		t.Fatalf("Init: %v", err)
	}
	return buf
}

func lastEvent(t *testing.T, buf *lockedBuffer) *eventv2.Event {
	t.Helper()
	var ev eventv2.Event
	if err := json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &ev); err != nil {
		t.Fatalf("unmarshal event: %v", err)
	}
	if err := eventv2.Validate(schemaPath, &ev); err != nil {
		t.Fatalf("schema validate: %v", err)
	}
	return &ev
}

func TestMiddlewareCapturesRouteTemplate(t *testing.T) {
	buf := newHarness(t, waylogv2.Config{})

	r := chi.NewRouter()
	r.Use(Middleware)
	r.Get("/orders/{id}", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/orders/42", nil)
	r.ServeHTTP(rr, req)

	ev := lastEvent(t, buf)
	httpFields, _ := ev.Fields["http"].(map[string]any)
	if got := httpFields["route"]; got != "/orders/{id}" {
		t.Fatalf("route=%v want /orders/{id}", got)
	}
}

func TestMiddlewareTimeoutUsesSharedLifecycle(t *testing.T) {
	buf := newHarness(t, waylogv2.Config{MaxRequestAge: 10 * time.Millisecond})

	r := chi.NewRouter()
	r.Use(Middleware)
	r.Get("/slow/{id}", func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(30 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	})

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/slow/42", nil)
	r.ServeHTTP(rr, req)

	ev := lastEvent(t, buf)
	if ev.Status != eventv2.StatusTimeout {
		t.Fatalf("status=%s want timeout", ev.Status)
	}
	if ev.Anchor == nil || ev.Anchor.ErrorCode != eventv2.CodeTimeout {
		t.Fatalf("timeout anchor wrong: %+v", ev.Anchor)
	}
}

func TestMiddlewarePanicKeepsRouteTemplate(t *testing.T) {
	buf := newHarness(t, waylogv2.Config{})

	r := chi.NewRouter()
	r.Use(Middleware)
	r.Get("/panic/{id}", func(w http.ResponseWriter, r *http.Request) {
		panic("boom")
	})

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/panic/42", nil)
	r.ServeHTTP(rr, req)

	ev := lastEvent(t, buf)
	if ev.Status != eventv2.StatusError {
		t.Fatalf("status=%s want error", ev.Status)
	}
	httpFields, _ := ev.Fields["http"].(map[string]any)
	if got := httpFields["route"]; got != "/panic/{id}" {
		t.Fatalf("route=%v want /panic/{id}", got)
	}
}
