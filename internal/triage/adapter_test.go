package triage_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/sssmaran/WaylogCLI/internal/incidents"
	"github.com/sssmaran/WaylogCLI/internal/signals"
	"github.com/sssmaran/WaylogCLI/internal/triage"
	apiv2 "github.com/sssmaran/WaylogCLI/pkg/api/v2"
	pkgtriage "github.com/sssmaran/WaylogCLI/pkg/triage"
)

// ----- IncidentLookupAdapter -----

type fakeIncidentReader struct {
	inc incidents.Incident
	err error
}

func (f fakeIncidentReader) Get(_ context.Context, _ string) (incidents.Incident, error) {
	if f.err != nil {
		return incidents.Incident{}, f.err
	}
	return f.inc, nil
}

func TestIncidentLookupAdapter_MapsFamilyFields(t *testing.T) {
	started := time.Date(2026, 5, 6, 11, 0, 0, 0, time.UTC)
	updated := time.Date(2026, 5, 6, 11, 5, 0, 0, time.UTC)
	reader := fakeIncidentReader{inc: incidents.Incident{
		IncidentID: "inc_abc",
		Env:        "demo",
		StartedAt:  started,
		UpdatedAt:  updated,
		Service:    "payment",
		ErrorFamily: apiv2.ErrorFamily{
			Service:   "payment",
			Step:      "payment.charge",
			ErrorCode: "PMT_502",
		},
		Confidence: incidents.ConfidenceHigh,
		NextChecks: []string{"Verify payment-service health", "Check recent deploys"},
	}}
	a := triage.NewIncidentLookupAdapter(reader)
	got, err := a.GetIncident(context.Background(), "inc_abc")
	if err != nil {
		t.Fatalf("GetIncident: %v", err)
	}
	if got.ID != "inc_abc" {
		t.Fatalf("ID = %q, want inc_abc", got.ID)
	}
	if got.Env != "demo" {
		t.Fatalf("Env = %q, want demo", got.Env)
	}
	if !got.StartedAt.Equal(started) {
		t.Fatalf("StartedAt = %v, want %v", got.StartedAt, started)
	}
	if !got.UpdatedAt.Equal(updated) {
		t.Fatalf("UpdatedAt = %v, want %v", got.UpdatedAt, updated)
	}
	if got.Service != "payment" || got.Step != "payment.charge" || got.ErrorCode != "PMT_502" {
		t.Fatalf("family fields = %+v", got)
	}
	if got.Window != "15m" {
		t.Fatalf("Window default = %q, want 15m", got.Window)
	}
	if got.Confidence != pkgtriage.ConfidenceHigh {
		t.Fatalf("Confidence = %q, want high", got.Confidence)
	}
	wantChecks := []string{"Verify payment-service health", "Check recent deploys"}
	if len(got.NextChecks) != len(wantChecks) {
		t.Fatalf("NextChecks len = %d, want %d (%+v)", len(got.NextChecks), len(wantChecks), got.NextChecks)
	}
	for i := range wantChecks {
		if got.NextChecks[i] != wantChecks[i] {
			t.Fatalf("NextChecks[%d] = %q, want %q", i, got.NextChecks[i], wantChecks[i])
		}
	}
}

func TestIncidentLookupAdapter_ConfidenceMapping(t *testing.T) {
	cases := []struct {
		in   incidents.Confidence
		want pkgtriage.Confidence
	}{
		{incidents.ConfidenceHigh, pkgtriage.ConfidenceHigh},
		{incidents.ConfidenceMedium, pkgtriage.ConfidenceMedium},
		{incidents.ConfidenceLow, pkgtriage.ConfidenceLow},
		{incidents.Confidence("nonsense"), pkgtriage.ConfidenceMedium},
	}
	for _, tc := range cases {
		reader := fakeIncidentReader{inc: incidents.Incident{
			IncidentID: "inc_abc",
			Confidence: tc.in,
		}}
		a := triage.NewIncidentLookupAdapter(reader)
		got, err := a.GetIncident(context.Background(), "inc_abc")
		if err != nil {
			t.Fatalf("GetIncident(%q): %v", tc.in, err)
		}
		if got.Confidence != tc.want {
			t.Fatalf("Confidence(%q) = %q, want %q", tc.in, got.Confidence, tc.want)
		}
	}
}

func TestIncidentLookupAdapter_NextChecksDefensiveCopy(t *testing.T) {
	original := []string{"a", "b"}
	reader := fakeIncidentReader{inc: incidents.Incident{
		IncidentID: "inc_abc",
		NextChecks: original,
	}}
	a := triage.NewIncidentLookupAdapter(reader)
	got, err := a.GetIncident(context.Background(), "inc_abc")
	if err != nil {
		t.Fatalf("GetIncident: %v", err)
	}
	// Mutating the original slice must not affect the summary's copy.
	original[0] = "MUTATED"
	if got.NextChecks[0] != "a" {
		t.Fatalf("NextChecks copy must be defensive, got %q after mutation", got.NextChecks[0])
	}
}

func TestIncidentLookupAdapter_NotFoundIsErrUnknown(t *testing.T) {
	a := triage.NewIncidentLookupAdapter(fakeIncidentReader{err: incidents.ErrNotFound})
	if _, err := a.GetIncident(context.Background(), "missing"); !errors.Is(err, triage.ErrUnknownIncident) {
		t.Fatalf("err = %v, want ErrUnknownIncident", err)
	}
}

// ----- BlastQueryAdapter -----

type fakeBlastReader struct {
	br   apiv2.BlastRadiusResponse
	rows []apiv2.ErrorRow
}

func (f fakeBlastReader) BlastRadius(_ incidents.SearchFilter, _ apiv2.BlastKey) apiv2.BlastRadiusResponse {
	return f.br
}

func (f fakeBlastReader) Errors(_ incidents.SearchFilter, _ int) incidents.ErrorsResult {
	return incidents.ErrorsResult{Rows: f.rows}
}

func TestBlastQueryAdapter_MapsCountsAndTopFamilies(t *testing.T) {
	users := 8
	reader := fakeBlastReader{
		br: apiv2.BlastRadiusResponse{
			AffectedRequests: 12,
			AffectedUsers:    &users,
			AffectedServices: 4,
		},
		rows: []apiv2.ErrorRow{
			{ErrorFamily: apiv2.ErrorFamily{Service: "payment", Step: "payment.charge", ErrorCode: "PMT_502"}, Count: 11},
			{ErrorFamily: apiv2.ErrorFamily{Service: "payment", Step: "payment.charge", ErrorCode: "PMT_503"}, Count: 3},
		},
	}
	a := triage.NewBlastQueryAdapter(reader)
	inc := triage.IncidentSummary{
		ID: "inc_abc", Service: "payment", Step: "payment.charge", ErrorCode: "PMT_502",
		UpdatedAt: time.Date(2026, 5, 6, 11, 5, 0, 0, time.UTC),
	}
	opts, _ := triage.ParseBuildOptions("15m", true, time.Now())
	opts.Now = inc.UpdatedAt

	got, err := a.BlastSnapshot(context.Background(), inc, opts)
	if err != nil {
		t.Fatalf("BlastSnapshot: %v", err)
	}
	if got.Requests != 12 || got.Users != 8 || got.Services != 4 {
		t.Fatalf("counts = %+v", got)
	}
	if len(got.TopErrorFamilies) != 2 {
		t.Fatalf("top families = %d, want 2", len(got.TopErrorFamilies))
	}
	if got.TopErrorFamilies[0].ErrorCode != "PMT_502" || got.TopErrorFamilies[0].Count != 11 {
		t.Fatalf("first family = %+v", got.TopErrorFamilies[0])
	}
}

func TestBlastQueryAdapter_NilUsersBecomesZero(t *testing.T) {
	reader := fakeBlastReader{br: apiv2.BlastRadiusResponse{AffectedRequests: 1, AffectedUsers: nil}}
	a := triage.NewBlastQueryAdapter(reader)
	inc := triage.IncidentSummary{Service: "x", UpdatedAt: time.Now()}
	opts, _ := triage.ParseBuildOptions("15m", false, time.Now())
	got, err := a.BlastSnapshot(context.Background(), inc, opts)
	if err != nil {
		t.Fatalf("BlastSnapshot: %v", err)
	}
	if got.Users != 0 {
		t.Fatalf("Users = %d, want 0 when AffectedUsers is nil", got.Users)
	}
}

// ----- StoryBuilderAdapter -----

type fakeIncForStory struct{ inc incidents.Incident }

func (f fakeIncForStory) Get(_ context.Context, _ string) (incidents.Incident, error) {
	return f.inc, nil
}

func TestStoryBuilderAdapter_UsesFirstSampleTrace(t *testing.T) {
	traceID := "abc123"
	wantStory := apiv2.StoryResponse{
		TraceID: traceID,
		Service: "payment",
		Anchor:  &apiv2.StoryAnchor{Step: "payment.charge", ErrorCode: "PMT_502"},
		Linkage: "trace_id",
	}

	called := false
	build := func(tid string) (apiv2.StoryResponse, bool) {
		called = true
		if tid != traceID {
			t.Fatalf("build called with %q, want %q", tid, traceID)
		}
		return wantStory, true
	}

	incReader := fakeIncForStory{inc: incidents.Incident{
		IncidentID:   "inc_abc",
		SampleTraces: []string{traceID, "other"},
		Service:      "payment",
		ErrorFamily:  apiv2.ErrorFamily{Service: "payment", Step: "payment.charge", ErrorCode: "PMT_502"},
	}}
	a := triage.NewStoryBuilderAdapter(incReader, build)
	inc := triage.IncidentSummary{ID: "inc_abc"}
	opts, _ := triage.ParseBuildOptions("15m", false, time.Now())

	got, err := a.FirstFailureStory(context.Background(), inc, opts)
	if err != nil {
		t.Fatalf("FirstFailureStory: %v", err)
	}
	if !called {
		t.Fatalf("build func was not called")
	}
	if len(got.SampleTraces) != 1 || got.SampleTraces[0].TraceID != traceID {
		t.Fatalf("sample traces = %+v", got.SampleTraces)
	}
	// Payload should be a non-empty JSON object that decodes to the public
	// StoryResponse shape.
	if len(got.Payload) == 0 || got.Payload[0] != '{' {
		t.Fatalf("payload not JSON object: %s", string(got.Payload))
	}
	var decoded map[string]any
	if err := json.Unmarshal(got.Payload, &decoded); err != nil {
		t.Fatalf("payload unmarshal: %v", err)
	}
	if decoded["trace_id"] != traceID {
		t.Fatalf("payload.trace_id = %v, want %q", decoded["trace_id"], traceID)
	}
}

func TestStoryBuilderAdapter_NoSampleTraceReturnsEmptyResult(t *testing.T) {
	build := func(string) (apiv2.StoryResponse, bool) {
		t.Fatalf("build should not be called when no sample trace")
		return apiv2.StoryResponse{}, false
	}
	incReader := fakeIncForStory{inc: incidents.Incident{IncidentID: "inc_abc"}}
	a := triage.NewStoryBuilderAdapter(incReader, build)
	got, err := a.FirstFailureStory(context.Background(), triage.IncidentSummary{ID: "inc_abc"}, triage.BuildOptions{})
	if err != nil {
		t.Fatalf("FirstFailureStory: %v", err)
	}
	if len(got.SampleTraces) != 0 {
		t.Fatalf("expected no sample traces, got %+v", got.SampleTraces)
	}
}

func TestStoryBuilderAdapter_StoryNotFoundReturnsEmpty(t *testing.T) {
	// When TraceStoryByTraceID returns ok=false (no matching trace), the
	// adapter must produce an empty result without erroring.
	build := func(string) (apiv2.StoryResponse, bool) {
		return apiv2.StoryResponse{}, false
	}
	incReader := fakeIncForStory{inc: incidents.Incident{
		IncidentID:   "inc_abc",
		SampleTraces: []string{"missing"},
	}}
	a := triage.NewStoryBuilderAdapter(incReader, build)
	got, err := a.FirstFailureStory(context.Background(), triage.IncidentSummary{ID: "inc_abc"}, triage.BuildOptions{})
	if err != nil {
		t.Fatalf("FirstFailureStory: %v", err)
	}
	if len(got.Payload) != 0 || len(got.SampleTraces) != 0 {
		t.Fatalf("expected empty result for not-found story, got %+v", got)
	}
}

// TestStoryBuilderAdapterPayloadHasReadAPIFields verifies the FirstFailure
// payload uses the public StoryResponse shape — keys consumers see at
// /v1/traces/story.
func TestStoryBuilderAdapterPayloadHasReadAPIFields(t *testing.T) {
	traceID := "trace_demo"
	resp := apiv2.StoryResponse{
		TraceID: traceID,
		Anchor:  &apiv2.StoryAnchor{Step: "payment.charge", ErrorCode: "PMT_502"},
		Path:    []apiv2.StoryStep{{Name: "payment.charge", StartMS: 0, DurationMS: 12}},
		Logs:    []apiv2.StoryLog{{TsOffsetMS: 5, Msg: "boom"}},
		Downstream: []apiv2.StoryDownstream{
			{Step: "payment.charge", Service: "payment", Endpoint: "/charge"},
		},
		Linkage: "trace_id",
	}
	build := func(string) (apiv2.StoryResponse, bool) { return resp, true }
	incReader := fakeIncForStory{inc: incidents.Incident{
		IncidentID:   "inc_abc",
		SampleTraces: []string{traceID},
	}}
	a := triage.NewStoryBuilderAdapter(incReader, build)

	got, err := a.FirstFailureStory(context.Background(), triage.IncidentSummary{ID: "inc_abc"}, triage.BuildOptions{})
	if err != nil {
		t.Fatalf("FirstFailureStory: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(got.Payload, &decoded); err != nil {
		t.Fatalf("payload unmarshal: %v", err)
	}
	for _, key := range []string{"trace_id", "anchor", "path", "logs", "downstream", "linkage"} {
		if _, ok := decoded[key]; !ok {
			t.Fatalf("payload missing read-API key %q: %v", key, decoded)
		}
	}
}

// ----- SignalQueryAdapter -----

type fakeSignalStore struct {
	out []signals.Signal
	err error
	got signals.Filter
}

func (f *fakeSignalStore) Query(_ context.Context, filter signals.Filter) ([]signals.Signal, error) {
	f.got = filter
	if f.err != nil {
		return nil, f.err
	}
	return f.out, nil
}

func TestSignalQueryAdapter_QueriesBroadByEnvWindowNotService(t *testing.T) {
	// Adapter must mirror incidents.Engine.querySignals: filter by Env + window
	// only. Service is intentionally NOT set so cross-service dependency
	// signals (e.g. a payment-service signal evidencing a checkout incident)
	// are surfaced.
	store := &fakeSignalStore{
		out: []signals.Signal{
			{SignalID: "sig_1", Type: signals.TypeDeploy, Service: "payment"},
			{SignalID: "sig_2", Type: signals.TypeDependency, Service: "payment"},
		},
	}
	a := triage.NewSignalQueryAdapter(store)
	now := time.Date(2026, 5, 6, 12, 0, 0, 0, time.UTC)
	inc := triage.IncidentSummary{
		Service:   "checkout",
		Env:       "demo",
		UpdatedAt: now,
	}
	opts, _ := triage.ParseBuildOptions("15m", false, now)

	got, err := a.SignalsFor(context.Background(), inc, opts)
	if err != nil {
		t.Fatalf("SignalsFor: %v", err)
	}
	if store.got.Service != "" {
		t.Fatalf("filter.Service = %q, want empty (broad query)", store.got.Service)
	}
	if store.got.Env != "demo" {
		t.Fatalf("filter.Env = %q, want demo", store.got.Env)
	}
	wantSince := now.Add(-15 * time.Minute)
	if !store.got.Since.Equal(wantSince) {
		t.Fatalf("filter.Since = %v, want %v", store.got.Since, wantSince)
	}
	if !store.got.Until.Equal(now) {
		t.Fatalf("filter.Until = %v, want %v", store.got.Until, now)
	}
	if store.got.Limit != 200 {
		t.Fatalf("filter.Limit = %d, want 200", store.got.Limit)
	}
	if len(got) != 2 {
		t.Fatalf("got %d signals, want 2 (cross-service signals must be returned)", len(got))
	}
	if got[0].ID != "sig_1" || got[0].Type != "deploy" {
		t.Fatalf("first signal = %+v", got[0])
	}
	// Critical assertion for Fix 1: the payment-service dependency signal must
	// be in the result even though inc.Service = checkout.
	foundPaymentDep := false
	for _, s := range got {
		if s.ID == "sig_2" && s.Type == "dependency" {
			foundPaymentDep = true
		}
	}
	if !foundPaymentDep {
		t.Fatalf("payment-service dependency signal dropped: got %+v", got)
	}
}

func TestSignalQueryAdapter_UnavailableReturnsEmpty(t *testing.T) {
	a := triage.NewSignalQueryAdapter(&fakeSignalStore{err: signals.ErrUnavailable})
	got, err := a.SignalsFor(context.Background(), triage.IncidentSummary{UpdatedAt: time.Now()}, triage.BuildOptions{Window: time.Minute})
	if err != nil {
		t.Fatalf("SignalsFor: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("want empty when unavailable, got %+v", got)
	}
}

func TestAlertQueryAdapter_UsesIncidentWindowPlusMatchWindow(t *testing.T) {
	now := time.Date(2026, 5, 10, 12, 0, 0, 0, time.UTC)
	started := now.Add(-2 * time.Hour)
	store := &fakeSignalStore{out: []signals.Signal{{
		SignalID:  "sig_alert",
		Type:      signals.TypeAlert,
		Source:    "grafana",
		Service:   "checkout",
		Env:       "demo",
		Severity:  signals.SeverityCritical,
		Reason:    "PMT_502 spike",
		Timestamp: started.Add(-20 * time.Minute),
		Metadata:  map[string]any{"alert_id": "alert_1"},
	}}}
	a := triage.NewAlertQueryAdapter(store, 30*time.Minute)
	got, err := a.AlertsFor(context.Background(), triage.IncidentSummary{
		Service:   "checkout",
		Env:       "demo",
		StartedAt: started,
		UpdatedAt: now,
	}, triage.BuildOptions{Window: 15 * time.Minute, Now: now})
	if err != nil {
		t.Fatalf("AlertsFor: %v", err)
	}
	wantSince := started.Add(-30 * time.Minute)
	if !store.got.Since.Equal(wantSince) {
		t.Fatalf("filter.Since = %v, want %v", store.got.Since, wantSince)
	}
	wantUntil := now.Add(30 * time.Minute)
	if !store.got.Until.Equal(wantUntil) {
		t.Fatalf("filter.Until = %v, want %v", store.got.Until, wantUntil)
	}
	if len(got) != 1 || got[0].AlertID != "alert_1" {
		t.Fatalf("alert refs wrong: %+v", got)
	}
}

// ----- NextChecksAdapter -----

func TestNextChecksAdapter_ConsumesIncidentNextChecks(t *testing.T) {
	a := triage.NewNextChecksAdapter()
	got, err := a.NextChecks(context.Background(), triage.IncidentSummary{
		Service:    "checkout",
		ErrorCode:  "PMT_502",
		NextChecks: []string{"Verify payment-service health", "Check recent deploys"},
	})
	if err != nil {
		t.Fatalf("NextChecks: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d checks, want 2: %+v", len(got), got)
	}
	if got[0].ID != "check_0" || got[0].Prompt != "Verify payment-service health" {
		t.Fatalf("got[0] = %+v, want {check_0, Verify payment-service health}", got[0])
	}
	if got[1].ID != "check_1" || got[1].Prompt != "Check recent deploys" {
		t.Fatalf("got[1] = %+v, want {check_1, Check recent deploys}", got[1])
	}
}

func TestNextChecksAdapter_EmptyIncidentReturnsEmpty(t *testing.T) {
	a := triage.NewNextChecksAdapter()
	got, err := a.NextChecks(context.Background(), triage.IncidentSummary{
		Service: "anything", ErrorCode: "XYZ_123",
	})
	if err != nil {
		t.Fatalf("NextChecks: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected no checks for empty NextChecks, got %+v", got)
	}
}
