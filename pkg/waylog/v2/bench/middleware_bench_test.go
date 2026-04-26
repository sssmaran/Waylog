package bench

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	wayloghttp "github.com/sssmaran/WaylogCLI/pkg/waylog/http"
	waylogv2 "github.com/sssmaran/WaylogCLI/pkg/waylog/v2"
)

// BenchmarkMiddlewareNoOp measures the per-request overhead of the
// net/http middleware wrapping a handler that returns 200 with no body.
// §4.4.1 budget: ≤ 33000 ns/op, ≤ 20 allocs/op.
func BenchmarkMiddlewareNoOp(b *testing.B) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := waylogv2.Shutdown(ctx); err != nil {
		b.Fatalf("pre-bench drain: %v", err)
	}
	if err := waylogv2.Init(waylogv2.Config{
		Service: "bench",
		Env:     "test",
		Output:  io.Discard,
	}); err != nil {
		b.Fatalf("Init: %v", err)
	}

	handler := wayloghttp.HTTP(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/bench", nil)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
	}
}
