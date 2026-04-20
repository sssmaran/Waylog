// e2e-otlp constructs a minimal OTLP/HTTP ExportTraceServiceRequest (1 trace,
// 2 spans) and POSTs it to the ingest server's /v1/otlp/v1/traces endpoint.
// The trace_id is printed on stdout for use by scripts/e2e-mark2.sh.
package main

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	coltracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/protobuf/proto"
)

func main() {
	ingestURL := "http://localhost:8080"
	if v := os.Getenv("INGEST_URL"); v != "" {
		ingestURL = v
	}

	traceBytes := make([]byte, 16)
	parentBytes := make([]byte, 8)
	childBytes := make([]byte, 8)
	_, _ = rand.Read(traceBytes)
	_, _ = rand.Read(parentBytes)
	_, _ = rand.Read(childBytes)

	now := uint64(time.Now().UnixNano())
	req := &coltracepb.ExportTraceServiceRequest{
		ResourceSpans: []*tracepb.ResourceSpans{{
			Resource: &resourcepb.Resource{Attributes: []*commonpb.KeyValue{
				strAttr("service.name", "otlp-e2e"),
				strAttr("deployment.environment", "dev"),
			}},
			ScopeSpans: []*tracepb.ScopeSpans{{
				Spans: []*tracepb.Span{
					{
						TraceId:           traceBytes,
						SpanId:            parentBytes,
						Name:              "GET /api",
						StartTimeUnixNano: now,
						EndTimeUnixNano:   now + 50_000_000,
						Kind:              tracepb.Span_SPAN_KIND_SERVER,
						Status:            &tracepb.Status{Code: tracepb.Status_STATUS_CODE_OK},
					},
					{
						TraceId:           traceBytes,
						SpanId:            childBytes,
						ParentSpanId:      parentBytes,
						Name:              "db.query",
						StartTimeUnixNano: now + 5_000_000,
						EndTimeUnixNano:   now + 45_000_000,
						Kind:              tracepb.Span_SPAN_KIND_CLIENT,
						Status:            &tracepb.Status{Code: tracepb.Status_STATUS_CODE_OK},
					},
				},
			}},
		}},
	}

	body, err := proto.Marshal(req)
	if err != nil {
		fmt.Fprintln(os.Stderr, "marshal:", err)
		os.Exit(1)
	}

	resp, err := http.Post(ingestURL+"/v1/otlp/v1/traces", "application/x-protobuf", bytes.NewReader(body))
	if err != nil {
		fmt.Fprintln(os.Stderr, "post:", err)
		os.Exit(1)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		fmt.Fprintf(os.Stderr, "otlp post: status=%d body=%s\n", resp.StatusCode, string(b))
		os.Exit(1)
	}

	fmt.Println(hex.EncodeToString(traceBytes))
}

func strAttr(k, v string) *commonpb.KeyValue {
	return &commonpb.KeyValue{Key: k, Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: v}}}
}
