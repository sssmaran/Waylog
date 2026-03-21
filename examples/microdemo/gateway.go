package microdemo

import (
	_ "embed"
	"encoding/json"
	"io"
	"net/http"

	waylog "github.com/sssmaran/WaylogCLI/pkg"
	wayloghttp "github.com/sssmaran/WaylogCLI/pkg/http"
	"github.com/sssmaran/WaylogCLI/pkg/trace"
)

//go:embed ui.html
var uiHTML []byte

type GatewayHandler struct {
	checkoutURL string
	client      *http.Client
	uiHTML      []byte
}

func NewGatewayHandler(checkoutURL string) *GatewayHandler {
	return &GatewayHandler{
		checkoutURL: checkoutURL,
		client: &http.Client{
			Transport: wayloghttp.WrapTransport(http.DefaultTransport, "checkout-demo"),
		},
		uiHTML: uiHTML,
	}
}

func (h *GatewayHandler) ServeDemo(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html")
	w.Write(h.uiHTML)
}

func (h *GatewayHandler) ServePurchase(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	force := r.URL.Query().Get("force")

	req, err := http.NewRequestWithContext(ctx, "GET", h.checkoutURL+"/checkout?force="+force, nil)
	if err != nil {
		http.Error(w, "failed to create request", http.StatusInternalServerError)
		return
	}

	resp, err := h.client.Do(req)
	if err != nil {
		ctx = waylog.WithUser(ctx, waylog.User{
			ID:     "demo-user",
			Tier:   "standard",
			Region: "us-east-1",
		})
		ctx = waylog.WithFlow(ctx, "purchase")
		waylog.Error(ctx, codedError{code: "GW_502", message: "checkout service unavailable"})

		traceID := ""
		if tc, ok := trace.FromContext(ctx); ok {
			traceID = tc.TraceID
		}

		w.WriteHeader(http.StatusBadGateway)
		json.NewEncoder(w).Encode(map[string]any{
			"success":  false,
			"trace_id": traceID,
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

	ctx = waylog.WithUser(ctx, waylog.User{
		ID:     "demo-user",
		Tier:   "standard",
		Region: "us-east-1",
	})
	ctx = waylog.WithFlow(ctx, "purchase")

	if resp.StatusCode >= http.StatusInternalServerError {
		waylog.Error(ctx, codedError{code: "GW_DOWNSTREAM", message: "downstream checkout failed"})
	}

	w.WriteHeader(resp.StatusCode)
	w.Write(body)
}
