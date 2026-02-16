package tools

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/sssmaran/WaylogCLI/internal/graph/build"
	graphstore "github.com/sssmaran/WaylogCLI/internal/graph/store"
	"github.com/sssmaran/WaylogCLI/internal/testutil"
)

func makeStoreWithSingleSpanFailure() *graphstore.Store {
	s := graphstore.NewStore()
	b := build.NewBuilder()

	ev := testutil.MakeEvent(
		testutil.WithTraceID("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"),
		testutil.WithSpanID("1111111111111111"),
		testutil.WithService("checkout"),
		testutil.WithStatusCode(502),
		testutil.WithError("PMT_502", "payment failed"),
	)
	s.Merge(b.Build(ev))
	return s
}

func TestHandleInsightsDeduplicatesRequestAndSpanFailures(t *testing.T) {
	store := makeStoreWithSingleSpanFailure()

	outAny, err := handleInsights(context.Background(), store, json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("handleInsights returned error: %v", err)
	}
	out, ok := outAny.(insightsOutput)
	if !ok {
		t.Fatalf("unexpected output type %T", outAny)
	}

	if out.TotalFailures != 1 {
		t.Fatalf("total_failures = %d, want 1", out.TotalFailures)
	}
	if len(out.TopErrors) != 1 {
		t.Fatalf("top_errors len = %d, want 1", len(out.TopErrors))
	}
	if out.TopErrors[0].ErrorCode != "PMT_502" || out.TopErrors[0].Count != 1 {
		t.Fatalf("top error = %+v, want PMT_502 count=1", out.TopErrors[0])
	}
	if len(out.TopServices) != 1 {
		t.Fatalf("top_services len = %d, want 1", len(out.TopServices))
	}
	if out.TopServices[0].Service != "checkout" || out.TopServices[0].Count != 1 {
		t.Fatalf("top service = %+v, want checkout count=1", out.TopServices[0])
	}
}

func TestHandleBlastRadiusDeduplicatesRequestAndSpanFailures(t *testing.T) {
	store := makeStoreWithSingleSpanFailure()

	params := json.RawMessage(`{"error_code":"PMT_502","include_services":true,"by_tier":true,"top_users":5}`)
	outAny, err := handleBlastRadius(context.Background(), store, params)
	if err != nil {
		t.Fatalf("handleBlastRadius returned error: %v", err)
	}
	out, ok := outAny.(blastOutput)
	if !ok {
		t.Fatalf("unexpected output type %T", outAny)
	}

	if out.AffectedRequests != 1 {
		t.Fatalf("affected_requests = %d, want 1", out.AffectedRequests)
	}
	if out.AffectedUsers != 1 {
		t.Fatalf("affected_users = %d, want 1", out.AffectedUsers)
	}
	if len(out.Services) != 1 || out.Services[0].Count != 1 {
		t.Fatalf("services = %+v, want one entry with count=1", out.Services)
	}
	if len(out.Tiers) != 1 || out.Tiers[0].Count != 1 {
		t.Fatalf("tiers = %+v, want one entry with count=1", out.Tiers)
	}
	if len(out.TopUsers) != 1 || out.TopUsers[0].Count != 1 {
		t.Fatalf("top_users = %+v, want one entry with count=1", out.TopUsers)
	}
}
