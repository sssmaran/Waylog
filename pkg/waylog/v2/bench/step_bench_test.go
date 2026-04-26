package bench

import (
	"context"
	"fmt"
	"io"
	"testing"
	"time"

	waylogv2 "github.com/sssmaran/WaylogCLI/pkg/waylog/v2"
)

const (
	benchRequestChunk   = 4096
	benchMaxBufferBytes = 1 << 30
)

func benchInit(b *testing.B) {
	b.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := waylogv2.Shutdown(ctx); err != nil {
		b.Fatalf("pre-bench drain: %v", err)
	}
	cfg := waylogv2.Config{
		Service:        "bench",
		Env:            "test",
		Output:         io.Discard,
		MaxSteps:       benchRequestChunk,
		MaxLogs:        benchRequestChunk,
		MaxBufferBytes: benchMaxBufferBytes,
	}
	if err := waylogv2.Init(cfg); err != nil {
		b.Fatalf("Init: %v", err)
	}
}

// BenchmarkStepEmptyBody measures the per-call overhead of opening and
// closing a Step with an empty body. §4.4.1 budget: ≤ 5500 ns/op,
// ≤ 4 allocs/op.
func BenchmarkStepEmptyBody(b *testing.B) {
	benchInit(b)
	ctx := waylogv2.Begin(context.Background(), waylogv2.BeginOptions{})
	noop := func(ctx context.Context) error { return nil }

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if i > 0 && i%benchRequestChunk == 0 {
			b.StopTimer()
			_, _ = waylogv2.Finalize(ctx)
			ctx = waylogv2.Begin(context.Background(), waylogv2.BeginOptions{})
			b.StartTimer()
		}
		_ = waylogv2.StepVoid(ctx, "bench.step", noop)
	}
	b.StopTimer()
	_, _ = waylogv2.Finalize(ctx)
}

// BenchmarkLoggerInfo measures the per-call overhead of From(ctx).Info
// with a single field. §4.4.1 budget: ≤ 3300 ns/op, ≤ 3 allocs/op.
func BenchmarkLoggerInfo(b *testing.B) {
	benchInit(b)
	ctx := waylogv2.Begin(context.Background(), waylogv2.BeginOptions{})
	logger := waylogv2.From(ctx)
	fields := waylogv2.F{"k": "v"}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if i > 0 && i%benchRequestChunk == 0 {
			b.StopTimer()
			_, _ = waylogv2.Finalize(ctx)
			ctx = waylogv2.Begin(context.Background(), waylogv2.BeginOptions{})
			logger = waylogv2.From(ctx)
			b.StartTimer()
		}
		logger.Info("hello", fields)
	}
	b.StopTimer()
	_, _ = waylogv2.Finalize(ctx)
}

// BenchmarkAssemble20Steps50Logs measures the cost of buffering a
// representative request — 20 closed steps and 50 logs — and emitting
// the final wide event. §4.4.1 budget: ≤ 110000 ns/op, ≤ 200 allocs/op.
func BenchmarkAssemble20Steps50Logs(b *testing.B) {
	benchInit(b)

	noop := func(ctx context.Context) error { return nil }
	logFields := waylogv2.F{"k": "v"}
	logMessages := make([]string, 50)
	for i := range logMessages {
		logMessages[i] = fmt.Sprintf("event_%d", i)
	}
	stepNames := make([]string, 20)
	for i := range stepNames {
		stepNames[i] = fmt.Sprintf("step_%d", i)
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ctx := waylogv2.Begin(context.Background(), waylogv2.BeginOptions{})
		logger := waylogv2.From(ctx)
		for _, msg := range logMessages {
			logger.Info(msg, logFields)
		}
		for _, name := range stepNames {
			_ = waylogv2.StepVoid(ctx, name, noop)
		}
		_, _ = waylogv2.Finalize(ctx)
	}
}
