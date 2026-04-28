package ingest

import (
	"context"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/sssmaran/WaylogCLI/internal/eventlog"
	"github.com/sssmaran/WaylogCLI/internal/graph/build"
	"github.com/sssmaran/WaylogCLI/internal/graph/store"
	"github.com/sssmaran/WaylogCLI/internal/metrics"
	"github.com/sssmaran/WaylogCLI/internal/sampler"
	"github.com/sssmaran/WaylogCLI/pkg/event"
)

func validSDKEvent() *event.WideEvent {
	return &event.WideEvent{
		SchemaVersion: event.SchemaVersion,
		EventName:     "svc.request",
		Timestamp:     time.Now(),
		User:          event.UserContext{ID: "u1"},
		Request:       event.RequestContext{TraceID: "aaaabbbbccccddddeeeeffffaaaabbbb", SpanID: "aaaabbbbccccdddd"},
		System:        event.SystemContext{Service: "svc", Env: "prod"},
		Outcome:       event.OutcomeContext{Success: true, StatusCode: 200},
		Metrics:       event.MetricsContext{LatencyMs: 50},
	}
}

func validOTLPEvent() *event.WideEvent {
	ev := validSDKEvent()
	ev.User.ID = "" // OTLP has no user
	return ev
}

type countingNotifier struct{ calls int }

func (n *countingNotifier) MarkDirty(topics ...string) { n.calls++ }

func sdkPipeline(s *store.Store) *Pipeline {
	return NewPipeline(PipelineConfig{
		Store:     s,
		Builder:   build.NewBuilder(),
		Sampler:   sampler.New(sampler.Config{HappySampleRatePct: 100}),
		Validator: func(ev *event.WideEvent) error { return ev.Validate() },
	})
}

func TestValidateAndIngestBatch_AllValid(t *testing.T) {
	s := store.NewStore()
	p := sdkPipeline(s)
	evs := []*event.WideEvent{validSDKEvent(), validSDKEvent()}
	res, err := p.ValidateAndIngestBatch(context.Background(), evs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Accepted != 2 {
		t.Errorf("expected 2 accepted, got %d", res.Accepted)
	}
	if res.Rejected != 0 {
		t.Errorf("expected 0 rejected, got %d", res.Rejected)
	}
}

func TestValidateAndIngestBatch_MixedValidInvalid(t *testing.T) {
	s := store.NewStore()
	p := sdkPipeline(s)
	invalid := &event.WideEvent{} // empty, fails validation
	evs := []*event.WideEvent{validSDKEvent(), invalid}
	res, err := p.ValidateAndIngestBatch(context.Background(), evs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Accepted != 1 {
		t.Errorf("expected 1 accepted, got %d", res.Accepted)
	}
	if res.Rejected != 1 {
		t.Errorf("expected 1 rejected, got %d", res.Rejected)
	}
	if len(res.Errors) != 1 {
		t.Fatalf("expected 1 error entry, got %d", len(res.Errors))
	}
	if res.Errors[0].Index != 1 {
		t.Errorf("expected error at index 1, got %d", res.Errors[0].Index)
	}
}

func TestValidateAndIngestBatch_OTLPValidator_EmptyUser(t *testing.T) {
	s := store.NewStore()
	p := NewPipeline(PipelineConfig{
		Store:     s,
		Builder:   build.NewBuilder(),
		Sampler:   sampler.New(sampler.Config{HappySampleRatePct: 100}),
		Validator: OTLPValidator,
	})
	evs := []*event.WideEvent{validOTLPEvent()}
	res, err := p.ValidateAndIngestBatch(context.Background(), evs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Accepted != 1 {
		t.Errorf("expected 1 accepted, got %d", res.Accepted)
	}
	if res.Rejected != 0 {
		t.Errorf("expected 0 rejected, got %d", res.Rejected)
	}
}

func TestValidateAndIngestBatch_OTLPValidator_OtherErrors(t *testing.T) {
	s := store.NewStore()
	p := NewPipeline(PipelineConfig{
		Store:     s,
		Builder:   build.NewBuilder(),
		Sampler:   sampler.New(sampler.Config{HappySampleRatePct: 100}),
		Validator: OTLPValidator,
	})
	ev := validOTLPEvent()
	ev.System.Service = "" // missing service in addition to empty user
	evs := []*event.WideEvent{ev}
	res, err := p.ValidateAndIngestBatch(context.Background(), evs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Rejected != 1 {
		t.Errorf("expected 1 rejected, got %d", res.Rejected)
	}
}

func TestIngestBatch_NotifierCalledOnce(t *testing.T) {
	s := store.NewStore()
	n := &countingNotifier{}
	p := NewPipeline(PipelineConfig{
		Store:     s,
		Builder:   build.NewBuilder(),
		Sampler:   sampler.New(sampler.Config{HappySampleRatePct: 100}),
		Validator: func(ev *event.WideEvent) error { return ev.Validate() },
		Notifier:  n,
	})
	evs := []*event.WideEvent{validSDKEvent(), validSDKEvent(), validSDKEvent()}
	if _, err := p.ValidateAndIngestBatch(context.Background(), evs); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if n.calls != 1 {
		t.Errorf("expected notifier called once, got %d", n.calls)
	}
}

func TestIngestBatch_SamplingAccounting(t *testing.T) {
	s := store.NewStore()
	p := NewPipeline(PipelineConfig{
		Store: s,
		// No builder means graph merge is skipped, but sampler still runs.
		// HappySampleRatePct=1 + a low-probability trace id won't reliably
		// drop. Instead use a real builder and a deterministic drop-all config
		// by setting the slow_ms threshold absurdly high and rate pct to 1
		// then picking a trace id whose hash bucket != 0.
		Builder:   build.NewBuilder(),
		Sampler:   sampler.New(sampler.Config{HappySampleRatePct: 1, SlowMs: 10000, Salt: "deterministic"}),
		Validator: func(ev *event.WideEvent) error { return ev.Validate() },
	})
	// Drive enough events with distinct trace ids so at least one lands
	// outside bucket 0 and gets sampled out.
	sampledOut := 0
	for i := 0; i < 20; i++ {
		ev := validSDKEvent()
		ev.Request.TraceID = traceIDForIndex(i)
		res, err := p.ValidateAndIngestBatch(context.Background(), []*event.WideEvent{ev})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if res.Accepted != 1 {
			t.Errorf("expected 1 accepted, got %d", res.Accepted)
		}
		sampledOut += res.SampledOut
	}
	if sampledOut == 0 {
		t.Error("expected at least one sampled-out event across 20 distinct trace ids")
	}
}

func TestIngestBatch_AcceptedMetricCountsDurableSampledOutEvents(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := metrics.New(reg)
	el, err := eventlog.NewWithConfig(t.TempDir(), eventlog.WriterConfig{})
	if err != nil {
		t.Fatal(err)
	}
	defer el.Close()

	p := NewPipeline(PipelineConfig{
		Store:    store.NewStore(),
		Builder:  build.NewBuilder(),
		Sampler:  sampler.New(sampler.Config{HappySampleRatePct: 1, SlowMs: 10000, Salt: "deterministic"}),
		EventLog: el,
		Metrics:  m,
		Validator: func(ev *event.WideEvent) error {
			return ev.Validate()
		},
	})

	accepted, sampledOut := 0, 0
	for i := 0; i < 20; i++ {
		ev := validSDKEvent()
		ev.Request.TraceID = traceIDForIndex(i)
		res, err := p.ValidateAndIngestBatch(context.Background(), []*event.WideEvent{ev})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		accepted += res.Accepted
		sampledOut += res.SampledOut
	}
	if sampledOut == 0 {
		t.Fatal("test did not exercise sampled-out durable events")
	}
	if got := counterMetric(t, reg, "waylog_events_accepted_total"); got != float64(accepted) {
		t.Fatalf("events_accepted=%v want %d", got, accepted)
	}
}

func counterMetric(t *testing.T, reg *prometheus.Registry, name string) float64 {
	t.Helper()
	families, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, mf := range families {
		if mf.GetName() != name {
			continue
		}
		var total float64
		for _, metric := range mf.GetMetric() {
			if counter := metric.GetCounter(); counter != nil {
				total += counter.GetValue()
			}
		}
		return total
	}
	return 0
}

// traceIDForIndex generates a distinct 32-hex trace id for each index.
func traceIDForIndex(i int) string {
	hex := "0123456789abcdef"
	out := make([]byte, 32)
	for j := 0; j < 32; j++ {
		out[j] = hex[(i+j)%16]
	}
	return string(out)
}
