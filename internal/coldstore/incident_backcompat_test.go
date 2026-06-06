package coldstore

import (
	"context"
	"testing"
	"time"

	"github.com/sssmaran/WaylogCLI/internal/incidents"
	apiv2 "github.com/sssmaran/WaylogCLI/pkg/api/v2"
)

// TestIncidentStoreScansLegacyNullEvidenceColumns is a characterization test for
// data-correctness invariant (f): a row whose evidence columns are NULL — the
// shape of an incident migrated in from before evidence-snapshot capture — must
// scan cleanly, with nil snapshots and no panic.
//
// It does NOT rely on today's writer choosing to write nil snapshots as NULL.
// Instead it writes a fully-populated incident, then drives the four evidence
// columns to NULL with raw SQL (simulating the migrated legacy row), and
// verifies the READ path (scanIncident -> parseJSONText) turns those NULL
// columns back into nil pointers.
func TestIncidentStoreScansLegacyNullEvidenceColumns(t *testing.T) {
	managed, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer managed.Close()
	sqlStore := managed.(*SQLiteStore)
	store := NewIncidentStore(sqlStore)

	now := time.Date(2026, 5, 6, 12, 0, 0, 0, time.UTC)
	inc := incidents.Incident{
		IncidentID:  "inc_legacy",
		Env:         "prod",
		Service:     "checkout",
		ErrorFamily: apiv2.ErrorFamily{Service: "checkout", Step: "charge", ErrorCode: "PMT_502"},
		Status:      incidents.StatusActive,
		Cause:       incidents.CauseUnknown,
		Confidence:  incidents.ConfidenceLow,
		StartedAt:   now, UpdatedAt: now, LastSeenAt: now,
		// Populate every evidence snapshot, so the subsequent NULL-ing is a real
		// state change and the read-path NULL handling is what produces nil.
		Propagation: &incidents.PropagationSnapshot{Latest: &incidents.PropagationEvidence{
			SampleTraceID: "trace-a", CaptureStatus: incidents.CaptureOK, CapturedAt: now,
		}},
		Blast: &incidents.BlastSnapshot{Latest: &incidents.BlastEvidence{
			AffectedRequests: 1, CaptureStatus: incidents.CaptureOK, CapturedAt: now,
		}},
		Alerts: &incidents.AlertSnapshot{Latest: &incidents.AlertEvidence{
			CaptureStatus: incidents.CaptureOK, CapturedAt: now,
		}},
		Runtime: &incidents.RuntimeSnapshot{Matches: []incidents.RuntimeEvidence{{
			SignalID: "sig_1", Subtype: "oom_killed", Service: "checkout",
			Severity: "critical", Reason: "OOMKilled", OccurredAt: now, CapturedAt: now,
		}}},
	}
	ctx := context.Background()
	if err := store.Upsert(ctx, inc); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	// Drive the evidence columns to NULL the way a migration would for a row that
	// predates evidence capture.
	if _, err := sqlStore.writer.ExecContext(ctx,
		`UPDATE incidents SET propagation_json = NULL, blast_json = NULL, alert_json = NULL, runtime_json = NULL WHERE incident_id = ?`,
		"inc_legacy",
	); err != nil {
		t.Fatalf("null-out evidence columns: %v", err)
	}

	got, err := store.Get(ctx, "inc_legacy")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Propagation != nil || got.Blast != nil || got.Alerts != nil || got.Runtime != nil {
		t.Fatalf("NULL evidence columns must scan as nil, got prop=%v blast=%v alerts=%v runtime=%v",
			got.Propagation, got.Blast, got.Alerts, got.Runtime)
	}
	// Sanity: the rest of the row is intact (we only nulled evidence).
	if got.IncidentID != "inc_legacy" || got.Service != "checkout" {
		t.Fatalf("non-evidence fields corrupted: %+v", got)
	}
}
