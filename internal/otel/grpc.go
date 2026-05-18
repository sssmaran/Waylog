package otel

import (
	"context"
	"crypto/subtle"
	"net/http"
	"strings"
	"time"

	ingestv2 "github.com/sssmaran/WaylogCLI/internal/ingest/v2"
	"github.com/sssmaran/WaylogCLI/internal/metrics"
	coltracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

// TraceServiceServer implements OTLP/gRPC TraceService over the same export
// processor used by the OTLP/HTTP endpoint.
type TraceServiceServer struct {
	coltracepb.UnimplementedTraceServiceServer
	handler *Handler
	metrics *metrics.Metrics
}

func NewTraceServiceServer(v2Ingest *ingestv2.Handler, m *metrics.Metrics, maxBodyBytes int64) *TraceServiceServer {
	return &TraceServiceServer{
		handler: NewHandler(v2Ingest, m, maxBodyBytes),
		metrics: m,
	}
}

func (s *TraceServiceServer) Export(ctx context.Context, req *coltracepb.ExportTraceServiceRequest) (*coltracepb.ExportTraceServiceResponse, error) {
	start := time.Now()
	if s.metrics != nil {
		defer func() {
			s.metrics.OTLPRequestDuration.Observe(time.Since(start).Seconds())
		}()
		if req != nil {
			s.metrics.OTLPRequestSizeBytes.Observe(float64(proto.Size(req)))
		}
	}

	resp, err := s.handler.Export(ctx, req)
	if err != nil {
		code := codes.Internal
		msg := "infrastructure error"
		if exportErr, ok := err.(*ExportError); ok {
			if s.metrics != nil {
				s.metrics.OTLPRequestsTotal.WithLabelValues(exportErr.Bucket).Inc()
			}
			code = grpcCode(exportErr.StatusCode)
			msg = exportErr.Message
		}
		return nil, status.Error(code, msg)
	}
	return resp, nil
}

func AuthUnaryInterceptor(writeKeys []string) grpc.UnaryServerInterceptor {
	keyBytes := make([][]byte, 0, len(writeKeys))
	for _, key := range writeKeys {
		key = strings.TrimSpace(key)
		if key != "" {
			keyBytes = append(keyBytes, []byte(key))
		}
	}
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		if len(keyBytes) == 0 {
			return handler(ctx, req)
		}
		if token := bearerToken(ctx); token != "" && matchesAnyToken([]byte(token), keyBytes) {
			return handler(ctx, req)
		}
		return nil, status.Error(codes.Unauthenticated, "unauthorized")
	}
}

func bearerToken(ctx context.Context) string {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return ""
	}
	for _, auth := range md.Get("authorization") {
		if idx := strings.IndexByte(auth, ' '); idx > 0 && strings.EqualFold(auth[:idx], "bearer") {
			return strings.TrimSpace(auth[idx+1:])
		}
	}
	return ""
}

func matchesAnyToken(token []byte, keys [][]byte) bool {
	match := 0
	for _, key := range keys {
		match |= subtle.ConstantTimeCompare(token, key)
	}
	return match == 1
}

func grpcCode(statusCode int) codes.Code {
	switch statusCode {
	case http.StatusBadRequest, http.StatusRequestEntityTooLarge, http.StatusUnsupportedMediaType:
		return codes.InvalidArgument
	case http.StatusUnauthorized:
		return codes.Unauthenticated
	case http.StatusForbidden:
		return codes.PermissionDenied
	case http.StatusServiceUnavailable:
		return codes.Unavailable
	default:
		if statusCode >= 500 {
			return codes.Internal
		}
		return codes.Unknown
	}
}
