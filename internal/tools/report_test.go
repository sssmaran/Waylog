package tools_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/sssmaran/WaylogCLI/internal/reports"
	"github.com/sssmaran/WaylogCLI/internal/tools"
)

func TestRenderTriageReportToolReturnsRenderedReport(t *testing.T) {
	reg := tools.NewRegistry()
	eng := newStubEngine(t)
	if err := tools.RegisterTriageReportTool(reg, eng); err != nil {
		t.Fatalf("register: %v", err)
	}
	out, err := reg.Call(context.Background(), "render_triage_report", json.RawMessage(`{"incident_id":"inc_abc","format":"markdown"}`))
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	rendered, ok := out.(reports.Rendered)
	if !ok {
		t.Fatalf("got %T, want reports.Rendered", out)
	}
	if rendered.Format != reports.FormatMarkdown || rendered.ContentType != "text/markdown" {
		t.Fatalf("unexpected rendered report: %+v", rendered)
	}
}
