package waylogecho

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/labstack/echo/v4"

	eventv2 "github.com/sssmaran/WaylogCLI/pkg/event/v2"
	waylogv2 "github.com/sssmaran/WaylogCLI/pkg/waylog/v2"
)

const schemaPath = "../../../docs/schema/v2.0.json"

func newHarness(t *testing.T, cfg waylogv2.Config) *bytes.Buffer {
	t.Helper()
	deadline, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	_ = waylogv2.Shutdown(deadline)

	buf := &bytes.Buffer{}
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

func lastEvent(t *testing.T, buf *bytes.Buffer) *eventv2.Event {
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

	e := echo.New()
	e.Use(Middleware())
	e.GET("/orders/:id", func(c echo.Context) error {
		return c.NoContent(http.StatusNoContent)
	})

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/orders/42", nil)
	e.ServeHTTP(rr, req)

	ev := lastEvent(t, buf)
	httpFields, _ := ev.Fields["http"].(map[string]any)
	if got := httpFields["route"]; got != "/orders/:id" {
		t.Fatalf("route=%v want /orders/:id", got)
	}
}

func TestMiddlewareKeepsSuppressedStatus(t *testing.T) {
	buf := newHarness(t, waylogv2.Config{})

	e := echo.New()
	e.Use(Middleware())
	e.GET("/quiet/:id", func(c echo.Context) error {
		waylogv2.Suppress(c.Request().Context())
		return c.NoContent(http.StatusNoContent)
	})

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/quiet/42", nil)
	e.ServeHTTP(rr, req)

	ev := lastEvent(t, buf)
	if ev.Status != eventv2.StatusSuppressed {
		t.Fatalf("status=%s want suppressed", ev.Status)
	}
}
