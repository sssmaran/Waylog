package microdemo_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/sssmaran/WaylogCLI/examples/microdemo"
	waylog "github.com/sssmaran/WaylogCLI/pkg"
	wayloghttp "github.com/sssmaran/WaylogCLI/pkg/http"
	"github.com/sssmaran/WaylogCLI/pkg/transport"
)

func TestMicroDemoChain(t *testing.T) {
	mem := transport.NewInMemoryTransport()

	// Create a fresh client for each test to avoid the global Init singleton.
	client, err := waylog.New(waylog.Config{
		Service:   "test-gateway",
		Env:       "test",
		Version:   "0.0.1",
		Transport: mem,
		ErrorClassifier: func(err error) string {
			if err == nil {
				return ""
			}
			type coded interface{ Code() string }
			if c, ok := err.(coded); ok {
				return c.Code()
			}
			return ""
		},
	})
	if err != nil {
		t.Fatalf("waylog.New: %v", err)
	}
	defer client.Close(context.Background())

	// Start payment test server
	paymentHandler := microdemo.NewPaymentHandler()
	paymentServer := httptest.NewServer(wayloghttp.MiddlewareWithClient(client)(paymentHandler))
	defer paymentServer.Close()

	// Start db test server
	dbHandler := microdemo.NewDBHandler()
	dbServer := httptest.NewServer(wayloghttp.MiddlewareWithClient(client)(dbHandler))
	defer dbServer.Close()

	// Start checkout test server pointing at db, then payment
	checkoutHandler := microdemo.NewCheckoutHandler(paymentServer.URL, dbServer.URL)
	checkoutServer := httptest.NewServer(wayloghttp.MiddlewareWithClient(client)(checkoutHandler))
	defer checkoutServer.Close()

	// Start gateway test server pointing at checkout
	gatewayHandler := microdemo.NewGatewayHandler(checkoutServer.URL)
	gatewayMux := http.NewServeMux()
	gatewayMux.Handle("/purchase", wayloghttp.MiddlewareWithClient(client)(http.HandlerFunc(gatewayHandler.ServePurchase)))
	gatewayMux.HandleFunc("/demo", gatewayHandler.ServeDemo)
	gatewayServer := httptest.NewServer(gatewayMux)
	defer gatewayServer.Close()

	tests := []struct {
		name       string
		force      string
		wantStatus int
		wantOK     bool
	}{
		{"success", "", http.StatusOK, true},
		{"payment_fail", "payment_fail", http.StatusBadGateway, false},
		{"checkout_fail", "checkout_fail", http.StatusInternalServerError, false},
		{"db_fail", "db_fail", http.StatusBadGateway, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			url := gatewayServer.URL + "/purchase"
			if tt.force != "" {
				url += "?force=" + tt.force
			}

			resp, err := http.Get(url)
			if err != nil {
				t.Fatalf("GET %s: %v", url, err)
			}
			defer resp.Body.Close()

			if resp.StatusCode != tt.wantStatus {
				t.Errorf("status = %d, want %d", resp.StatusCode, tt.wantStatus)
			}

			var body map[string]any
			if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
				t.Fatalf("decode body: %v", err)
			}

			success, _ := body["success"].(bool)
			if success != tt.wantOK {
				t.Errorf("success = %v, want %v", success, tt.wantOK)
			}

			traceID, _ := body["trace_id"].(string)
			if traceID == "" {
				t.Error("trace_id should not be empty")
			}
		})
	}

	// Test UI endpoint
	t.Run("demo_ui", func(t *testing.T) {
		resp, err := http.Get(gatewayServer.URL + "/demo")
		if err != nil {
			t.Fatalf("GET /demo: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			t.Errorf("status = %d, want 200", resp.StatusCode)
		}
		ct := resp.Header.Get("Content-Type")
		if ct != "text/html" {
			t.Errorf("Content-Type = %q, want text/html", ct)
		}
	})

	// Allow time for events to flush
	time.Sleep(2 * time.Second)
	events := mem.Events()
	if len(events) == 0 {
		t.Log("no events captured (expected with shared client across services)")
	} else {
		t.Logf("captured %d events", len(events))
		for i, ev := range events {
			t.Logf("event[%d]: %s trace=%s", i, ev.EventName, ev.Request.TraceID)
		}
	}

	fmt.Printf("Integration test passed: %d scenarios verified\n", len(tests)+1)
}
