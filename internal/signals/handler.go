package signals

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/sssmaran/WaylogCLI/internal/metrics"
)

const defaultMaxBodyBytes int64 = 1 << 20

type Handler struct {
	store        Store
	metrics      *metrics.Metrics
	now          func() time.Time
	futureSkew   time.Duration
	maxBodyBytes int64
}

func NewHandler(store Store, m *metrics.Metrics) *Handler {
	if store == nil {
		store = UnavailableStore{}
	}
	return &Handler{
		store:        store,
		metrics:      m,
		now:          time.Now,
		futureSkew:   5 * time.Minute,
		maxBodyBytes: defaultMaxBodyBytes,
	}
}

func (h *Handler) Signals(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		h.reject(w, http.StatusMethodNotAllowed, CodeUnsupportedMethod, "method not allowed", "")
		return
	}

	body, ok := h.readBody(w, r)
	if !ok {
		return
	}
	var signal Signal
	if err := json.Unmarshal(body, &signal); err != nil {
		var validation *ValidationError
		if errors.As(err, &validation) {
			h.reject(w, http.StatusBadRequest, validation.Code, "invalid signal", validation.Error())
			return
		}
		h.reject(w, http.StatusBadRequest, CodeInvalidJSON, "invalid json", err.Error())
		return
	}
	now := h.now().UTC()
	if err := Validate(&signal, now, h.futureSkew); err != nil {
		var validation *ValidationError
		if errors.As(err, &validation) {
			h.reject(w, http.StatusBadRequest, validation.Code, "invalid signal", validation.Error())
			return
		}
		h.reject(w, http.StatusBadRequest, CodeInvalidField, "invalid signal", err.Error())
		return
	}
	signal.SignalID = NewSignalID()
	signal.ReceivedAt = now
	if err := h.store.Insert(r.Context(), &signal); err != nil {
		if errors.Is(err, ErrUnavailable) {
			h.reject(w, http.StatusServiceUnavailable, CodeDurabilityUnavailable, "signals unavailable", "set SQLITE_PATH to enable signals")
			return
		}
		h.reject(w, http.StatusInternalServerError, CodeInternalError, "internal error", "")
		return
	}
	if h.metrics != nil {
		h.metrics.SignalsAccepted.Inc()
	}
	writeJSON(w, http.StatusCreated, map[string]Signal{"signal": signal})
}

func (h *Handler) readBody(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	r.Body = http.MaxBytesReader(w, r.Body, h.maxBodyBytes)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			h.reject(w, http.StatusRequestEntityTooLarge, CodeBodyOversize, "body too large", "request body exceeds 1 MB")
			return nil, false
		}
		h.reject(w, http.StatusBadRequest, CodeInvalidBody, "invalid body", err.Error())
		return nil, false
	}
	return body, true
}

func (h *Handler) reject(w http.ResponseWriter, status int, code, message, detail string) {
	if h.metrics != nil {
		h.metrics.SignalsRejected.WithLabelValues(code).Inc()
	}
	writeJSON(w, status, errorResponse{Error: readError{Code: code, Message: message, Detail: detail}})
}

type errorResponse struct {
	Error readError `json:"error"`
}

type readError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Detail  string `json:"detail,omitempty"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
