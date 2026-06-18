package otel

import (
	"context"
	"net"
	"testing"

	coltracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
)

const bufSize = 1024 * 1024

func newBufconnClient(t *testing.T, keys []string) (coltracepb.TraceServiceClient, func()) {
	t.Helper()
	lis := bufconn.Listen(bufSize)
	srv := grpc.NewServer(grpc.UnaryInterceptor(AuthUnaryInterceptor(keys)))
	coltracepb.RegisterTraceServiceServer(srv, NewTraceServiceServer(testV2Ingest(t), nil, 1<<20, nil))
	go func() {
		_ = srv.Serve(lis)
	}()

	ctx := context.Background()
	conn, err := grpc.DialContext(ctx, "bufnet",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) {
			return lis.Dial()
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("dial bufconn: %v", err)
	}
	cleanup := func() {
		_ = conn.Close()
		srv.Stop()
		_ = lis.Close()
	}
	return coltracepb.NewTraceServiceClient(conn), cleanup
}

func TestGRPCExportHappyPath(t *testing.T) {
	client, cleanup := newBufconnClient(t, []string{"write-key"})
	defer cleanup()

	ctx := metadata.AppendToOutgoingContext(context.Background(), "authorization", "Bearer write-key")
	resp, err := client.Export(ctx, validOTLPRequest())
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	if resp.PartialSuccess != nil && resp.PartialSuccess.RejectedSpans != 0 {
		t.Fatalf("partial_success = %+v, want none", resp.PartialSuccess)
	}
}

func TestGRPCExportAuth(t *testing.T) {
	tests := []struct {
		name string
		auth string
		code codes.Code
	}{
		{name: "missing", code: codes.Unauthenticated},
		{name: "bad key", auth: "Bearer wrong", code: codes.Unauthenticated},
		{name: "read key", auth: "Bearer read-key", code: codes.Unauthenticated},
		{name: "agent key", auth: "Bearer agent-key", code: codes.Unauthenticated},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			client, cleanup := newBufconnClient(t, []string{"write-key"})
			defer cleanup()

			ctx := context.Background()
			if tc.auth != "" {
				ctx = metadata.AppendToOutgoingContext(ctx, "authorization", tc.auth)
			}
			_, err := client.Export(ctx, validOTLPRequest())
			if status.Code(err) != tc.code {
				t.Fatalf("code = %v, want %v (err=%v)", status.Code(err), tc.code, err)
			}
		})
	}
}

func TestGRPCExportDevModeNoKeys(t *testing.T) {
	client, cleanup := newBufconnClient(t, nil)
	defer cleanup()

	if _, err := client.Export(context.Background(), validOTLPRequest()); err != nil {
		t.Fatalf("export without auth in dev mode: %v", err)
	}
}
