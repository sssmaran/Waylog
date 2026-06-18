package microdemo_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sssmaran/WaylogCLI/examples/microdemo"
	eventv2 "github.com/sssmaran/WaylogCLI/pkg/event/v2"
	wayloghttp "github.com/sssmaran/WaylogCLI/pkg/waylog/http"
	waylogv2 "github.com/sssmaran/WaylogCLI/pkg/waylog/v2"
)

func TestCheckoutPayment502EmitsNarrativeEvent(t *testing.T) {
	out := initSDK(t, "checkout")
	db := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true}`))
	}))
	defer db.Close()
	payment := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(`{"success":false}`))
	}))
	defer payment.Close()

	handler := wayloghttp.HTTP(microdemo.NewCheckoutHandler(payment.URL, db.URL))
	resp := postPurchase(t, handler, "/checkout", microdemo.ScenarioPayment502)
	if resp.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want %d", resp.Code, http.StatusBadGateway)
	}

	ev := oneEvent(t, out)
	if ev.SchemaVersion != eventv2.SchemaVersion2 {
		t.Fatalf("schema_version = %q, want %q", ev.SchemaVersion, eventv2.SchemaVersion2)
	}
	if ev.Service != "checkout" || ev.Status != eventv2.StatusError {
		t.Fatalf("service/status = %s/%s, want checkout/error", ev.Service, ev.Status)
	}
	if ev.Anchor == nil || ev.Anchor.Step != "payment.charge" || ev.Anchor.ErrorCode != "PMT_502" {
		t.Fatalf("anchor = %#v, want payment.charge/PMT_502", ev.Anchor)
	}
	requireStep(t, ev, "cart.validate", eventv2.StepStatusOK, "")
	requireStep(t, ev, "db.load_cart", eventv2.StepStatusOK, "db")
	requireStep(t, ev, "inventory.reserve", eventv2.StepStatusOK, "")
	requireStep(t, ev, "payment.charge", eventv2.StepStatusError, "payment")
	requireNoStep(t, ev, "order.commit")
	requireLog(t, ev, eventv2.LogLevelWarn, "retrying payment")
	requireLog(t, ev, eventv2.LogLevelError, "upstream gateway 5xx")
	requireField(t, ev, "user", "id", "demo-user")
	requireField(t, ev, "demo", "scenario", microdemo.ScenarioPayment502)
	requireField(t, ev, "http", "route", "/checkout")
}

func TestCheckoutDBMissEmitsCartNotFoundAnchor(t *testing.T) {
	out := initSDK(t, "checkout")
	db := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"success":false,"error":"cart record not found"}`))
	}))
	defer db.Close()
	payment := httptest.NewServer(okJSON())
	defer payment.Close()

	resp := postPurchase(t, wayloghttp.HTTP(microdemo.NewCheckoutHandler(payment.URL, db.URL)), "/checkout", microdemo.ScenarioDBMiss)
	if resp.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want %d", resp.Code, http.StatusBadGateway)
	}
	ev := oneEvent(t, out)
	if ev.Status != eventv2.StatusError {
		t.Fatalf("status = %s, want error", ev.Status)
	}
	if ev.Anchor == nil || ev.Anchor.Step != "db.load_cart" || ev.Anchor.ErrorCode != "CART_NOT_FOUND" {
		t.Fatalf("anchor = %#v, want db.load_cart/CART_NOT_FOUND", ev.Anchor)
	}
	requireStep(t, ev, "cart.validate", eventv2.StepStatusOK, "")
	requireStep(t, ev, "db.load_cart", eventv2.StepStatusError, "db")
	requireNoStep(t, ev, "inventory.reserve")
	requireNoStep(t, ev, "payment.charge")
	requireNoStep(t, ev, "order.commit")
}

func TestCheckoutInternalErrorEmitsCHK500WithoutDownstream(t *testing.T) {
	out := initSDK(t, "checkout")
	db := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("db should not be called for checkout_error scenario")
	}))
	defer db.Close()
	payment := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("payment should not be called for checkout_error scenario")
	}))
	defer payment.Close()

	resp := postPurchase(t, wayloghttp.HTTP(microdemo.NewCheckoutHandler(payment.URL, db.URL)), "/checkout", microdemo.ScenarioCheckoutError)
	if resp.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", resp.Code, http.StatusInternalServerError)
	}
	ev := oneEvent(t, out)
	if ev.Status != eventv2.StatusError {
		t.Fatalf("status = %s, want error", ev.Status)
	}
	if ev.Anchor == nil || ev.Anchor.Step != "cart.validate" || ev.Anchor.ErrorCode != "CHK_500" {
		t.Fatalf("anchor = %#v, want cart.validate/CHK_500", ev.Anchor)
	}
	requireStep(t, ev, "cart.validate", eventv2.StepStatusError, "")
	requireNoStep(t, ev, "db.load_cart")
	requireNoStep(t, ev, "inventory.reserve")
	requireNoStep(t, ev, "payment.charge")
	requireNoStep(t, ev, "order.commit")
}

func TestCheckoutInventory503EmitsINV503AfterDBLoad(t *testing.T) {
	out := initSDK(t, "checkout")
	db := httptest.NewServer(okJSON())
	defer db.Close()
	payment := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("payment should not be called for inventory_503 scenario")
	}))
	defer payment.Close()

	resp := postPurchase(t, wayloghttp.HTTP(microdemo.NewCheckoutHandler(payment.URL, db.URL)), "/checkout", microdemo.ScenarioInventory503)
	if resp.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want %d", resp.Code, http.StatusBadGateway)
	}
	ev := oneEvent(t, out)
	if ev.Status != eventv2.StatusError {
		t.Fatalf("status = %s, want error", ev.Status)
	}
	if ev.Anchor == nil || ev.Anchor.Step != "inventory.reserve" || ev.Anchor.ErrorCode != "INV_503" {
		t.Fatalf("anchor = %#v, want inventory.reserve/INV_503", ev.Anchor)
	}
	requireStep(t, ev, "cart.validate", eventv2.StepStatusOK, "")
	requireStep(t, ev, "db.load_cart", eventv2.StepStatusOK, "db")
	requireStep(t, ev, "inventory.reserve", eventv2.StepStatusError, "")
	requireNoStep(t, ev, "payment.charge")
	requireNoStep(t, ev, "order.commit")
}

func TestCheckoutHappyEmitsOKWithoutAnchor(t *testing.T) {
	out := initSDK(t, "checkout")
	db := httptest.NewServer(okJSON())
	defer db.Close()
	payment := httptest.NewServer(okJSON())
	defer payment.Close()

	resp := postPurchase(t, wayloghttp.HTTP(microdemo.NewCheckoutHandler(payment.URL, db.URL)), "/checkout", microdemo.ScenarioHappy)
	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", resp.Code, http.StatusOK)
	}
	ev := oneEvent(t, out)
	if ev.Status != eventv2.StatusOK {
		t.Fatalf("status = %s, want ok", ev.Status)
	}
	if ev.Anchor != nil {
		t.Fatalf("anchor = %#v, want nil", ev.Anchor)
	}
	requireStep(t, ev, "cart.validate", eventv2.StepStatusOK, "")
	requireStep(t, ev, "db.load_cart", eventv2.StepStatusOK, "db")
	requireStep(t, ev, "inventory.reserve", eventv2.StepStatusOK, "")
	requireStep(t, ev, "payment.charge", eventv2.StepStatusOK, "payment")
	requireStep(t, ev, "order.commit", eventv2.StepStatusOK, "")
	requireLog(t, ev, eventv2.LogLevelInfo, "cart validated")
	requireLog(t, ev, eventv2.LogLevelInfo, "cart loaded")
	requireLog(t, ev, eventv2.LogLevelInfo, "inventory reserved")
	requireLog(t, ev, eventv2.LogLevelInfo, "order committed")
	requireNoLog(t, ev, eventv2.LogLevelWarn, "retrying payment")
}

func TestPaymentSuppressed502IsHeaderOnly(t *testing.T) {
	out := initSDK(t, "payment")
	resp := postPurchase(t, wayloghttp.HTTP(microdemo.NewPaymentHandler()), "/pay", microdemo.ScenarioSuppressedPayment502)
	if resp.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want %d", resp.Code, http.StatusBadGateway)
	}
	ev := oneEvent(t, out)
	if ev.Status != eventv2.StatusSuppressed {
		t.Fatalf("status = %s, want suppressed", ev.Status)
	}
	if ev.Anchor != nil || len(ev.Steps) != 0 || len(ev.Logs) != 0 {
		t.Fatalf("suppressed event should be header-only: anchor=%#v steps=%d logs=%d", ev.Anchor, len(ev.Steps), len(ev.Logs))
	}
}

func TestPayment502DoesNotStealCheckoutNarrativeAnchor(t *testing.T) {
	out := initSDK(t, "payment")
	resp := postPurchase(t, wayloghttp.HTTP(microdemo.NewPaymentHandler()), "/pay", microdemo.ScenarioPayment502)
	if resp.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want %d", resp.Code, http.StatusBadGateway)
	}
	ev := oneEvent(t, out)
	if ev.Status != eventv2.StatusOK {
		t.Fatalf("status = %s, want ok so checkout remains the trace failure anchor", ev.Status)
	}
	if ev.Anchor != nil {
		t.Fatalf("anchor = %#v, want nil", ev.Anchor)
	}
	requireStep(t, ev, "acquirer.charge", eventv2.StepStatusOK, "")
}

func TestGatewayDemoUIAndCheckoutPropagationStep(t *testing.T) {
	out := initSDK(t, "api-gateway")
	checkout := httptest.NewServer(okJSON())
	defer checkout.Close()
	gateway := microdemo.NewGatewayHandler(checkout.URL)

	mux := http.NewServeMux()
	mux.Handle("/purchase", wayloghttp.HTTP(http.HandlerFunc(gateway.ServePurchase)))
	mux.HandleFunc("/demo", gateway.ServeDemo)

	uiReq := httptest.NewRequest(http.MethodGet, "/demo", nil)
	uiResp := httptest.NewRecorder()
	mux.ServeHTTP(uiResp, uiReq)
	if uiResp.Code != http.StatusOK || !strings.Contains(uiResp.Body.String(), "Run payment outage") {
		t.Fatalf("demo UI status/body = %d/%q", uiResp.Code, uiResp.Body.String())
	}

	resp := postPurchase(t, mux, "/purchase", microdemo.ScenarioHappy)
	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", resp.Code, http.StatusOK)
	}
	ev := oneEvent(t, out)
	requireStep(t, ev, "checkout.purchase", eventv2.StepStatusOK, "checkout")
}

func initSDK(t *testing.T, service string) *bytes.Buffer {
	t.Helper()
	shutdownSDK(t)
	var out bytes.Buffer
	if err := waylogv2.Init(waylogv2.Config{
		Service: service,
		Env:     "test",
		Version: "test",
		Output:  &out,
	}); err != nil {
		t.Fatalf("waylog init: %v", err)
	}
	t.Cleanup(func() {
		shutdownSDK(t)
	})
	return &out
}

func shutdownSDK(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := waylogv2.Shutdown(ctx); err != nil {
		t.Fatalf("waylog shutdown: %v", err)
	}
}

func postPurchase(t *testing.T, handler http.Handler, path, scenario string) *httptest.ResponseRecorder {
	t.Helper()
	body := strings.NewReader(`{"sku":"X1","scenario":"` + scenario + `"}`)
	req := httptest.NewRequest(http.MethodPost, path, body)
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	handler.ServeHTTP(resp, req)
	return resp
}

func oneEvent(t *testing.T, out *bytes.Buffer) eventv2.Event {
	t.Helper()
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 1 || strings.TrimSpace(lines[0]) == "" {
		t.Fatalf("captured %d events, want 1: %q", len(lines), out.String())
	}
	var ev eventv2.Event
	if err := json.Unmarshal([]byte(lines[0]), &ev); err != nil {
		t.Fatalf("unmarshal event: %v\n%s", err, lines[0])
	}
	return ev
}

func requireStep(t *testing.T, ev eventv2.Event, name, status, downstream string) {
	t.Helper()
	for _, step := range ev.Steps {
		if step.Name != name {
			continue
		}
		if step.Status != status {
			t.Fatalf("step %s status = %s, want %s", name, step.Status, status)
		}
		if downstream != "" {
			if step.Downstream == nil || step.Downstream.Service != downstream {
				t.Fatalf("step %s downstream = %#v, want service %q", name, step.Downstream, downstream)
			}
			if step.SpanID == "" {
				t.Fatalf("step %s span_id is empty; outbound transport did not record linkage", name)
			}
		}
		return
	}
	t.Fatalf("missing step %q in %#v", name, ev.Steps)
}

func hasStep(ev eventv2.Event, name string) bool {
	for _, step := range ev.Steps {
		if step.Name == name {
			return true
		}
	}
	return false
}

func requireNoStep(t *testing.T, ev eventv2.Event, name string) {
	t.Helper()
	if hasStep(ev, name) {
		t.Fatalf("unexpected step %q in %#v", name, ev.Steps)
	}
}

func requireLog(t *testing.T, ev eventv2.Event, level, msg string) {
	t.Helper()
	for _, log := range ev.Logs {
		if log.Level == level && log.Msg == msg {
			return
		}
	}
	t.Fatalf("missing log %s/%q in %#v", level, msg, ev.Logs)
}

func requireNoLog(t *testing.T, ev eventv2.Event, level, msg string) {
	t.Helper()
	for _, log := range ev.Logs {
		if log.Level == level && log.Msg == msg {
			t.Fatalf("unexpected log %s/%q in %#v", level, msg, ev.Logs)
		}
	}
}

func requireField(t *testing.T, ev eventv2.Event, top, key string, want any) {
	t.Helper()
	fields, _ := ev.Fields[top].(map[string]any)
	if fields == nil {
		t.Fatalf("missing fields.%s in %#v", top, ev.Fields)
	}
	if got := fields[key]; got != want {
		t.Fatalf("fields.%s.%s = %#v, want %#v", top, key, got, want)
	}
}

func okJSON() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true}`))
	}
}
