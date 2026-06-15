package otel

import (
	"context"
	"testing"
	"time"

	"github.com/sssmaran/WaylogCLI/internal/coldstore"
	"github.com/sssmaran/WaylogCLI/internal/incidents"
	apiv2 "github.com/sssmaran/WaylogCLI/pkg/api/v2"
	eventv2 "github.com/sssmaran/WaylogCLI/pkg/event/v2"
	coltracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
)

type fakeDeployStore struct {
	upserts []coldstore.Deployment
}

func (f *fakeDeployStore) UpsertDeployment(_ context.Context, d coldstore.Deployment) error {
	f.upserts = append(f.upserts, d)
	return nil
}

// requestWithVersion builds a one-span OTLP request for test-svc/prod carrying
// the given service.version. seq makes trace/span IDs unique across calls so
// ingest dedup never drops the event.
func requestWithVersion(version string, seq byte) *coltracepb.ExportTraceServiceRequest {
	traceID := make([]byte, 16)
	spanID := make([]byte, 8)
	traceID[15] = seq
	traceID[0] = 0x0f
	spanID[7] = seq
	spanID[0] = 0x0f
	return &coltracepb.ExportTraceServiceRequest{
		ResourceSpans: []*tracepb.ResourceSpans{{
			Resource: &resourcepb.Resource{Attributes: []*commonpb.KeyValue{
				strAttr("service.name", "test-svc"),
				strAttr("service.version", version),
				strAttr("deployment.environment", "prod"),
			}},
			ScopeSpans: []*tracepb.ScopeSpans{{
				Spans: []*tracepb.Span{{
					TraceId:           traceID,
					SpanId:            spanID,
					Name:              "test-op",
					StartTimeUnixNano: 1000000000,
					EndTimeUnixNano:   1050000000,
					Attributes: []*commonpb.KeyValue{
						strAttr("http.request.method", "GET"),
						strAttr("http.route", "/test"),
						intAttr("http.response.status_code", 200),
					},
					Status: &tracepb.Status{Code: tracepb.Status_STATUS_CODE_OK},
				}},
			}},
		}},
	}
}

func TestExportRegistersDeploymentOnVersionChange(t *testing.T) {
	store := &fakeDeployStore{}
	h := NewHandler(testV2Ingest(t), nil, 1<<20, NewDeployTracker(store))
	ctx := context.Background()

	// First version seen for a (service, env) is tracked, not registered:
	// steady-state traffic after a restart must not fabricate a deploy
	// anchored at boot time.
	if _, err := h.Export(ctx, requestWithVersion("v1", 1)); err != nil {
		t.Fatalf("export v1: %v", err)
	}
	if len(store.upserts) != 0 {
		t.Fatalf("first-seen version must not register a deployment: %+v", store.upserts)
	}

	// A version change registers exactly one deployment.
	if _, err := h.Export(ctx, requestWithVersion("v2", 2)); err != nil {
		t.Fatalf("export v2: %v", err)
	}
	if len(store.upserts) != 1 {
		t.Fatalf("version change must register one deployment, got %d", len(store.upserts))
	}
	dep := store.upserts[0]
	if dep.Service != "test-svc" || dep.Env != "prod" || dep.Version != "v2" {
		t.Fatalf("deployment fields wrong: %+v", dep)
	}
	if dep.ID != "otlp:test-svc:prod:v2" {
		t.Fatalf("deployment ID must be deterministic, got %q", dep.ID)
	}
	if dep.FirstSeen.IsZero() || dep.LastSeen.IsZero() {
		t.Fatalf("first/last seen must be set: %+v", dep)
	}

	// Repeats of the same version are a no-op.
	if _, err := h.Export(ctx, requestWithVersion("v2", 3)); err != nil {
		t.Fatalf("export v2 again: %v", err)
	}
	if len(store.upserts) != 1 {
		t.Fatalf("unchanged version must not re-register, got %d upserts", len(store.upserts))
	}

	// Rolling back (or mixed replicas during rollout) re-registers the other
	// version; the store's MIN(first_seen) keeps the original anchor.
	if _, err := h.Export(ctx, requestWithVersion("v1", 4)); err != nil {
		t.Fatalf("export v1 rollback: %v", err)
	}
	if len(store.upserts) != 2 || store.upserts[1].Version != "v1" {
		t.Fatalf("rollback must register v1: %+v", store.upserts)
	}
}

// End-to-end scenario: an OTel-only install (no SDK, no deploy webhook) sees a
// service.version change on spans; the auto-registered deployment must be
// queryable from the real cold store and make the incident classifier pick
// cause=deploy for a subsequent error burst.
func TestOTLPVersionChangeEnablesDeployClassification(t *testing.T) {
	db, err := coldstore.Open(":memory:")
	if err != nil {
		t.Fatalf("coldstore.Open: %v", err)
	}
	defer db.Close()
	store := db.(*coldstore.SQLiteStore)

	h := NewHandler(testV2Ingest(t), nil, 1<<20, NewDeployTracker(store))
	ctx := context.Background()
	if _, err := h.Export(ctx, requestWithVersion("v1", 21)); err != nil {
		t.Fatalf("export v1: %v", err)
	}
	if _, err := h.Export(ctx, requestWithVersion("v2", 22)); err != nil {
		t.Fatalf("export v2: %v", err)
	}

	now := time.Now().UTC()
	rows, err := store.DeploymentsInWindow(ctx, now.Add(-time.Minute), now.Add(time.Minute), "test-svc")
	if err != nil {
		t.Fatalf("DeploymentsInWindow: %v", err)
	}
	if len(rows) != 1 || rows[0].Version != "v2" {
		t.Fatalf("want one v2 deployment, got %+v", rows)
	}

	// Same conversion the engine's coldDeployAdapter performs.
	dep := incidents.Deployment{
		ID: rows[0].ID, Service: rows[0].Service, Version: rows[0].Version,
		Env: rows[0].Env, FirstSeen: rows[0].FirstSeen,
	}
	errEvent := &eventv2.Event{
		SchemaVersion: eventv2.SchemaVersion2,
		EventID:       "e-burst", TraceID: "trace-burst", SpanID: "span-burst",
		TsStart: now, TsEnd: now, Kind: "http",
		Service: "test-svc", Env: "prod", Version: "v2",
		Status: eventv2.StatusError,
		Anchor: &eventv2.Anchor{Step: "op", ErrorCode: "HTTP_500"},
		Steps: []eventv2.Step{{
			Name: "op", Status: eventv2.StepStatusError,
			Error: &eventv2.StepError{Code: "HTTP_500", Reason: "boom"},
		}},
	}
	got := incidents.Classify(incidents.ClassificationInput{
		Incident: incidents.Incident{
			Service: "test-svc", Env: "prod", StartedAt: now,
			ErrorFamily: apiv2.ErrorFamily{Service: "test-svc", Step: "op", ErrorCode: "HTTP_500"},
		},
		Events:      []*eventv2.Event{errEvent},
		Deployments: []incidents.Deployment{dep},
		Now:         now,
	})
	if got.Cause != incidents.CauseDeploy || got.Confidence != incidents.ConfidenceHigh {
		t.Fatalf("OTel-only deploy correlation failed: %+v", got)
	}
}

func TestExportWithoutTrackerOrVersionIsSafe(t *testing.T) {
	ctx := context.Background()

	// nil tracker: no panic.
	h := NewHandler(testV2Ingest(t), nil, 1<<20, nil)
	if _, err := h.Export(ctx, requestWithVersion("v1", 9)); err != nil {
		t.Fatalf("export with nil tracker: %v", err)
	}

	// events without service.version never register.
	store := &fakeDeployStore{}
	h2 := NewHandler(testV2Ingest(t), nil, 1<<20, NewDeployTracker(store))
	if _, err := h2.Export(ctx, validOTLPRequest()); err != nil {
		t.Fatalf("export without version: %v", err)
	}
	if _, err := h2.Export(ctx, validOTLPRequest()); err != nil {
		t.Fatalf("export without version again: %v", err)
	}
	if len(store.upserts) != 0 {
		t.Fatalf("versionless events must not register deployments: %+v", store.upserts)
	}
}
