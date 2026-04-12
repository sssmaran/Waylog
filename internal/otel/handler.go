// Package otel contains the HTTP handler that accepts OTLP/HTTP traces and
// feeds them through the shared ingest Pipeline. The protobuf decoding and
// span→WideEvent conversion live in internal/otel/convert; this package only
// deals with the HTTP transport contract.
package otel

import (
	"compress/gzip"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"strings"
	"time"

	"github.com/sssmaran/WaylogCLI/internal/ingest"
	"github.com/sssmaran/WaylogCLI/internal/metrics"
	"github.com/sssmaran/WaylogCLI/internal/otel/convert"
	coltracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	"google.golang.org/protobuf/proto"
)

// Handler serves POST /v1/otlp/v1/traces. It decodes the protobuf body,
// converts spans to WideEvents, and delegates ingestion to the shared
// Pipeline. The handler responds with an ExportTraceServiceResponse per
// the OTLP/HTTP spec — partial_success is set when any span was dropped
// by conversion or rejected by validation.
type Handler struct {
	pipeline     *ingest.Pipeline
	metrics      *metrics.Metrics
	maxBodyBytes int64
}

// NewHandler constructs an OTLP traces handler.
func NewHandler(pipeline *ingest.Pipeline, m *metrics.Metrics, maxBodyBytes int64) *Handler {
	return &Handler{
		pipeline:     pipeline,
		metrics:      m,
		maxBodyBytes: maxBodyBytes,
	}
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	if h.metrics != nil {
		defer func() {
			h.metrics.OTLPRequestDuration.Observe(time.Since(start).Seconds())
		}()
	}

	if r.Method != http.MethodPost {
		h.respondStatus(w, "4xx", http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/x-protobuf" {
		h.respondStatus(w, "4xx", http.StatusUnsupportedMediaType, "unsupported content type; expected application/x-protobuf")
		return
	}

	encoding := r.Header.Get("Content-Encoding")
	if encoding != "" && encoding != "gzip" {
		h.respondStatus(w, "4xx", http.StatusUnsupportedMediaType, "unsupported content encoding; only gzip is accepted")
		return
	}

	var reader io.Reader = r.Body
	if encoding == "gzip" {
		gr, err := gzip.NewReader(r.Body)
		if err != nil {
			h.respondStatus(w, "4xx", http.StatusBadRequest, "invalid gzip body")
			return
		}
		defer gr.Close()
		reader = gr
	}

	// Read up to maxBodyBytes+1 so we can detect overflow exactly.
	limited := io.LimitReader(reader, h.maxBodyBytes+1)
	body, err := io.ReadAll(limited)
	if err != nil {
		h.respondStatus(w, "4xx", http.StatusBadRequest, "failed to read body")
		return
	}
	if int64(len(body)) > h.maxBodyBytes {
		h.respondStatus(w, "4xx", http.StatusRequestEntityTooLarge, "request body too large")
		return
	}

	if h.metrics != nil {
		h.metrics.OTLPRequestSizeBytes.Observe(float64(len(body)))
	}

	var req coltracepb.ExportTraceServiceRequest
	if err := proto.Unmarshal(body, &req); err != nil {
		if h.metrics != nil {
			h.metrics.OTLPDecodeFailures.Inc()
		}
		h.respondStatus(w, "4xx", http.StatusBadRequest, "invalid protobuf")
		return
	}

	convResult := convert.SpansToEvents(&req)

	// Mirror the future-timestamp guard from Server.Events: drop any span
	// dated more than 5 minutes ahead of wall-clock so a skewed collector
	// can't poison recent traces, overview, or time-series buckets. Reported
	// in partial_success alongside conversion drops.
	futureCutoff := time.Now().Add(5 * time.Minute)
	kept := convResult.Events[:0]
	for _, ev := range convResult.Events {
		if ev.Timestamp.After(futureCutoff) {
			convResult.Dropped++
			convResult.Drops = append(convResult.Drops, convert.DropEntry{
				SpanName: ev.EventName,
				Reason:   convert.DropFutureTimestamp,
			})
			continue
		}
		kept = append(kept, ev)
	}
	convResult.Events = kept

	if h.metrics != nil {
		totalSpans := convResult.Dropped + len(convResult.Events)
		h.metrics.OTLPSpansReceived.Add(float64(totalSpans))
		h.metrics.OTLPSpansConverted.Add(float64(len(convResult.Events)))
		for _, d := range convResult.Drops {
			h.metrics.OTLPSpansDropped.WithLabelValues(string(d.Reason)).Inc()
		}
	}

	var batchResult ingest.BatchResult
	if len(convResult.Events) > 0 && h.pipeline != nil {
		batchResult, err = h.pipeline.ValidateAndIngestBatch(r.Context(), convResult.Events)
		if err != nil {
			slog.Error("otlp: pipeline infrastructure failure", "err", err)
			if h.metrics != nil {
				h.metrics.OTLPInfraFailures.Inc()
			}
			h.respondStatus(w, "5xx", http.StatusServiceUnavailable, "infrastructure error")
			return
		}
		if h.metrics != nil && batchResult.Rejected > 0 {
			h.metrics.OTLPValidationRejects.Add(float64(batchResult.Rejected))
		}
	}

	resp := &coltracepb.ExportTraceServiceResponse{}
	totalRejected := int64(convResult.Dropped + batchResult.Rejected)
	if totalRejected > 0 {
		resp.PartialSuccess = &coltracepb.ExportTracePartialSuccess{
			RejectedSpans: totalRejected,
			ErrorMessage:  buildPartialSuccessMessage(convResult.Drops, batchResult.Errors),
		}
	}

	respBytes, err := proto.Marshal(resp)
	if err != nil {
		h.respondStatus(w, "5xx", http.StatusInternalServerError, "failed to encode response")
		return
	}

	if h.metrics != nil {
		h.metrics.OTLPRequestsTotal.WithLabelValues("2xx").Inc()
	}

	w.Header().Set("Content-Type", "application/x-protobuf")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(respBytes)
}

func (h *Handler) respondStatus(w http.ResponseWriter, bucket string, code int, msg string) {
	if h.metrics != nil {
		h.metrics.OTLPRequestsTotal.WithLabelValues(bucket).Inc()
	}
	http.Error(w, msg, code)
}

// buildPartialSuccessMessage produces a short human-readable summary of the
// first few drops and validation rejects, capped to avoid runaway size.
func buildPartialSuccessMessage(drops []convert.DropEntry, errs []ingest.EventError) string {
	const limit = 5
	var parts []string
	for i, d := range drops {
		if i >= limit {
			break
		}
		msg := string(d.Reason)
		if d.SpanName != "" {
			msg += " (span: " + d.SpanName + ")"
		}
		parts = append(parts, msg)
	}
	for i, e := range errs {
		if i >= limit {
			break
		}
		parts = append(parts, e.Reason)
	}
	return strings.Join(parts, "; ")
}
