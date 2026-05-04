package microdemo

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	wayloghttp "github.com/sssmaran/WaylogCLI/pkg/waylog/http"
	waylogv2 "github.com/sssmaran/WaylogCLI/pkg/waylog/v2"
)

const (
	ScenarioHappy                = "happy"
	ScenarioPayment502           = "payment_502"
	ScenarioSuppressedPayment502 = "suppressed_payment_502"
	ScenarioDBMiss               = "db_miss"
	ScenarioCheckoutError        = "checkout_error"

	demoUserID = "demo-user"
)

//go:embed ui.html
var uiHTML []byte

type GatewayHandler struct {
	checkoutURL string
	client      *http.Client
	purchase    http.Handler
}

type PurchaseRequest struct {
	SKU      string `json:"sku"`
	Scenario string `json:"scenario"`
}

func NewGatewayHandler(checkoutURL string) *GatewayHandler {
	h := &GatewayHandler{
		checkoutURL: checkoutURL,
		client: &http.Client{
			Transport: wayloghttp.NewTransport(demoHTTPTransport(), "checkout"),
		},
	}
	// Pre-wrap so the live /purchase route and /demo/burst dispatch share a
	// single instance — and so callers can't forget to wire it up.
	h.purchase = wayloghttp.HTTP(http.HandlerFunc(h.ServePurchase))
	return h
}

// PurchaseHandler returns the wayloghttp-wrapped /purchase handler. Use this
// to register the route so /demo/burst dispatches through the same chain.
func (h *GatewayHandler) PurchaseHandler() http.Handler {
	return h.purchase
}

// SetPurchaseHandler overrides the handler used by /demo/burst. Test seam.
func (h *GatewayHandler) SetPurchaseHandler(handler http.Handler) {
	h.purchase = handler
}

func (h *GatewayHandler) ServeDemo(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html")
	_, _ = w.Write(uiHTML)
}

func (h *GatewayHandler) ServePurchase(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	reqBody, err := parsePurchaseRequest(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	setDemoFields(ctx, "api-gateway", reqBody)

	raw, _ := json.Marshal(reqBody)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.checkoutURL+"/checkout", bytes.NewReader(raw))
	if err != nil {
		http.Error(w, "failed to create checkout request", http.StatusInternalServerError)
		return
	}
	req.Header.Set("Content-Type", "application/json")

	var resp *http.Response
	err = waylogv2.StepVoid(ctx, "checkout.purchase", func(ctx context.Context) error {
		var doErr error
		resp, doErr = h.client.Do(req.WithContext(ctx))
		return doErr
	})
	if err != nil {
		w.WriteHeader(http.StatusBadGateway)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"success":  false,
			"trace_id": waylogv2.TraceID(ctx),
			"scenario": reqBody.Scenario,
			"error":    "checkout service unavailable",
		})
		return
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		http.Error(w, "failed to read checkout response", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(body)
}

func (h *GatewayHandler) ServeBurst(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if ct := r.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		http.Error(w, "content-type must be application/json", http.StatusUnsupportedMediaType)
		return
	}
	if h.purchase == nil {
		http.Error(w, "purchase handler unavailable", http.StatusServiceUnavailable)
		return
	}

	var req BurstRequest
	if r.Body != nil {
		defer r.Body.Close()
		dec := json.NewDecoder(r.Body)
		dec.DisallowUnknownFields()
		if err := dec.Decode(&req); err != nil && err != io.EOF {
			http.Error(w, "invalid json: "+err.Error(), http.StatusBadRequest)
			return
		}
	}

	summary := runBurst(r.Context(), h.purchase, req)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(summary)
}

func parsePurchaseRequest(r *http.Request) (PurchaseRequest, error) {
	req := PurchaseRequest{SKU: "X1", Scenario: ScenarioHappy}
	if r.Method == http.MethodPost && r.Body != nil {
		defer r.Body.Close()
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
			return PurchaseRequest{}, err
		}
	}
	if scenario := r.URL.Query().Get("scenario"); scenario != "" {
		req.Scenario = scenario
	}
	if req.SKU == "" {
		req.SKU = "X1"
	}
	req.Scenario = normalizeScenario(req.Scenario)
	if req.Scenario == "" {
		return PurchaseRequest{}, errUnknownScenario
	}
	return req, nil
}
