package microdemo

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"io"
	"net/http"

	wayloghttp "github.com/sssmaran/WaylogCLI/pkg/waylog/http"
	waylogv2 "github.com/sssmaran/WaylogCLI/pkg/waylog/v2"
)

const (
	ScenarioHappy                = "happy"
	ScenarioPayment502           = "payment_502"
	ScenarioSuppressedPayment502 = "suppressed_payment_502"

	demoUserID = "demo-user"
)

//go:embed ui.html
var uiHTML []byte

type GatewayHandler struct {
	checkoutURL string
	client      *http.Client
}

type PurchaseRequest struct {
	SKU      string `json:"sku"`
	Scenario string `json:"scenario"`
}

func NewGatewayHandler(checkoutURL string) *GatewayHandler {
	return &GatewayHandler{
		checkoutURL: checkoutURL,
		client: &http.Client{
			Transport: wayloghttp.NewTransport(http.DefaultTransport, "checkout"),
		},
	}
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
	if force := r.URL.Query().Get("force"); force != "" {
		req.Scenario = legacyForceScenario(force)
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
