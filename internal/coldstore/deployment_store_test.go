package coldstore

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func newTestStore(t *testing.T) *SQLiteStore {
	t.Helper()
	s, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s.(*SQLiteStore)
}

func mustTime(s string) time.Time {
	t, err := time.Parse(tsFormat, s)
	if err != nil {
		panic(err)
	}
	return t
}

var (
	t1 = mustTime("2024-01-01T10:00:00.000000000Z")
	t2 = mustTime("2024-01-01T11:00:00.000000000Z")
	t3 = mustTime("2024-01-01T12:00:00.000000000Z")
)

// TestUpsertAndByID verifies basic insert and round-trip retrieval.
func TestUpsertAndByID(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	d := Deployment{
		ID:        "dep-1",
		Service:   "svc-a",
		Version:   "v1.2.3",
		Env:       "prod",
		FirstSeen: t1,
		LastSeen:  t2,
		Metadata:  map[string]string{"region": "us-east-1"},
	}

	if err := s.UpsertDeployment(ctx, d); err != nil {
		t.Fatal(err)
	}

	got, err := s.DeploymentByID(ctx, "dep-1")
	if err != nil {
		t.Fatal(err)
	}
	if got == nil {
		t.Fatal("expected deployment, got nil")
	}
	if got.ID != d.ID || got.Service != d.Service || got.Version != d.Version || got.Env != d.Env {
		t.Errorf("field mismatch: got %+v", got)
	}
	if !got.FirstSeen.Equal(d.FirstSeen) {
		t.Errorf("FirstSeen: got %v, want %v", got.FirstSeen, d.FirstSeen)
	}
	if !got.LastSeen.Equal(d.LastSeen) {
		t.Errorf("LastSeen: got %v, want %v", got.LastSeen, d.LastSeen)
	}
	if got.Metadata["region"] != "us-east-1" {
		t.Errorf("metadata mismatch: %v", got.Metadata)
	}
}

// TestMinMaxSemantics verifies that first_seen takes MIN and last_seen takes MAX on upsert.
func TestMinMaxSemantics(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	// Initial insert.
	if err := s.UpsertDeployment(ctx, Deployment{
		ID: "dep-2", Service: "svc-b", Version: "v1", Env: "staging",
		FirstSeen: t2, LastSeen: t2,
	}); err != nil {
		t.Fatal(err)
	}

	// Earlier first_seen should update (MIN).
	if err := s.UpsertDeployment(ctx, Deployment{
		ID: "dep-2", Service: "svc-b", Version: "v1", Env: "staging",
		FirstSeen: t1, LastSeen: t1,
	}); err != nil {
		t.Fatal(err)
	}

	// Later last_seen should update (MAX).
	if err := s.UpsertDeployment(ctx, Deployment{
		ID: "dep-2", Service: "svc-b", Version: "v1", Env: "staging",
		FirstSeen: t3, LastSeen: t3,
	}); err != nil {
		t.Fatal(err)
	}

	got, err := s.DeploymentByID(ctx, "dep-2")
	if err != nil {
		t.Fatal(err)
	}
	if !got.FirstSeen.Equal(t1) {
		t.Errorf("FirstSeen: got %v, want %v (MIN)", got.FirstSeen, t1)
	}
	if !got.LastSeen.Equal(t3) {
		t.Errorf("LastSeen: got %v, want %v (MAX)", got.LastSeen, t3)
	}
}

// TestEnvConflict verifies that upsert returns ErrEnvConflict when env changes.
func TestEnvConflict(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	if err := s.UpsertDeployment(ctx, Deployment{
		ID: "dep-3", Service: "svc-c", Version: "v1", Env: "prod",
		FirstSeen: t1, LastSeen: t1,
	}); err != nil {
		t.Fatal(err)
	}

	err := s.UpsertDeployment(ctx, Deployment{
		ID: "dep-3", Service: "svc-c", Version: "v2", Env: "staging",
		FirstSeen: t2, LastSeen: t2,
	})
	if !errors.Is(err, ErrEnvConflict) {
		t.Errorf("expected ErrEnvConflict, got %v", err)
	}
}

// TestEmptyVersionPreservation verifies that an empty version does not overwrite an existing version.
func TestEmptyVersionPreservation(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	if err := s.UpsertDeployment(ctx, Deployment{
		ID: "dep-4", Service: "svc-d", Version: "v3.0", Env: "prod",
		FirstSeen: t1, LastSeen: t1,
	}); err != nil {
		t.Fatal(err)
	}

	// Upsert with empty version — should not overwrite "v3.0".
	if err := s.UpsertDeployment(ctx, Deployment{
		ID: "dep-4", Service: "svc-d", Version: "", Env: "prod",
		FirstSeen: t2, LastSeen: t2,
	}); err != nil {
		t.Fatal(err)
	}

	got, err := s.DeploymentByID(ctx, "dep-4")
	if err != nil {
		t.Fatal(err)
	}
	if got.Version != "v3.0" {
		t.Errorf("version overwritten: got %q, want %q", got.Version, "v3.0")
	}
}

// TestDeploymentsInWindow verifies window filtering and optional service filter.
func TestDeploymentsInWindow(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	deps := []Deployment{
		{ID: "w1", Service: "alpha", Version: "v1", Env: "prod", FirstSeen: t1, LastSeen: t1},
		{ID: "w2", Service: "beta", Version: "v1", Env: "prod", FirstSeen: t2, LastSeen: t2},
		{ID: "w3", Service: "alpha", Version: "v2", Env: "prod", FirstSeen: t3, LastSeen: t3},
	}
	for _, d := range deps {
		if err := s.UpsertDeployment(ctx, d); err != nil {
			t.Fatal(err)
		}
	}

	// All in window.
	all, err := s.DeploymentsInWindow(ctx, t1, t3, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 {
		t.Errorf("expected 3, got %d", len(all))
	}

	// Filter by service.
	alphas, err := s.DeploymentsInWindow(ctx, t1, t3, "alpha")
	if err != nil {
		t.Fatal(err)
	}
	if len(alphas) != 2 {
		t.Errorf("expected 2 alpha deployments, got %d", len(alphas))
	}

	// Narrow window excludes t1.
	narrow, err := s.DeploymentsInWindow(ctx, t2, t3, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(narrow) != 2 {
		t.Errorf("expected 2 in narrow window, got %d", len(narrow))
	}
}

// TestDeploymentByIDMiss verifies nil, nil for a missing ID.
func TestDeploymentByIDMiss(t *testing.T) {
	s := newTestStore(t)
	got, err := s.DeploymentByID(context.Background(), "no-such-id")
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Errorf("expected nil, got %+v", got)
	}
}

// TestConcurrentUpserts verifies that 10 goroutines upserting the same ID don't error or race.
func TestConcurrentUpserts(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	const n = 10
	var wg sync.WaitGroup
	errs := make(chan error, n)

	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			err := s.UpsertDeployment(ctx, Deployment{
				ID:        "concurrent-dep",
				Service:   "svc-e",
				Version:   "v1",
				Env:       "prod",
				FirstSeen: t1,
				LastSeen:  t2,
			})
			if err != nil {
				errs <- err
			}
		}(i)
	}
	wg.Wait()
	close(errs)

	for err := range errs {
		t.Errorf("concurrent upsert error: %v", err)
	}

	got, err := s.DeploymentByID(ctx, "concurrent-dep")
	if err != nil {
		t.Fatal(err)
	}
	if got == nil {
		t.Fatal("expected deployment after concurrent upserts")
	}
}

// TestMetadataRoundTrip verifies metadata is preserved through upsert and retrieval.
func TestMetadataRoundTrip(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	meta := map[string]string{
		"region":   "eu-west-1",
		"cluster":  "k8s-prod-7",
		"built_by": "ci-pipeline",
	}
	if err := s.UpsertDeployment(ctx, Deployment{
		ID: "dep-meta", Service: "svc-f", Version: "v1", Env: "prod",
		FirstSeen: t1, LastSeen: t1,
		Metadata: meta,
	}); err != nil {
		t.Fatal(err)
	}

	got, err := s.DeploymentByID(ctx, "dep-meta")
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range meta {
		if got.Metadata[k] != v {
			t.Errorf("metadata[%q]: got %q, want %q", k, got.Metadata[k], v)
		}
	}
	if len(got.Metadata) != len(meta) {
		t.Errorf("metadata len: got %d, want %d", len(got.Metadata), len(meta))
	}
}

// TestMalformedMetadataByID verifies that DeploymentByID returns an error for malformed metadata.
func TestMalformedMetadataByID(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	// Insert a row directly with invalid JSON in metadata.
	_, err := s.writer.ExecContext(ctx, `
		INSERT INTO deployments (id, service, version, env, first_seen, last_seen, metadata)
		VALUES ('bad-meta', 'svc-g', 'v1', 'prod', ?, ?, 'not-valid-json')`,
		t1.UTC().Format(tsFormat), t1.UTC().Format(tsFormat),
	)
	if err != nil {
		t.Fatal(err)
	}

	_, err = s.DeploymentByID(ctx, "bad-meta")
	if err == nil {
		t.Error("expected error for malformed metadata, got nil")
	}
}

// TestMalformedMetadataInWindow verifies DeploymentsInWindow tolerates malformed metadata.
func TestMalformedMetadataInWindow(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	_, err := s.writer.ExecContext(ctx, `
		INSERT INTO deployments (id, service, version, env, first_seen, last_seen, metadata)
		VALUES ('bad-meta-win', 'svc-h', 'v1', 'prod', ?, ?, 'not-valid-json')`,
		t1.UTC().Format(tsFormat), t1.UTC().Format(tsFormat),
	)
	if err != nil {
		t.Fatal(err)
	}

	results, err := s.DeploymentsInWindow(ctx, t1, t2, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].Metadata == nil {
		t.Error("expected empty map, got nil")
	}
	if len(results[0].Metadata) != 0 {
		t.Errorf("expected empty map, got %v", results[0].Metadata)
	}
}

// TestDeployVCSRoundTrip verifies commit/PR provenance survives upsert and that
// empty provenance does not clobber an existing value (COALESCE/NULLIF idiom).
func TestDeployVCSRoundTrip(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	d := Deployment{
		ID: "dep-vcs", Service: "svc-v", Version: "v1", Env: "prod",
		FirstSeen: t1, LastSeen: t1,
		CommitSHA: "a1b2c3d", PRURL: "https://example/pr/1", CommitAuthor: "alice",
	}
	if err := s.UpsertDeployment(ctx, d); err != nil {
		t.Fatal(err)
	}

	got, err := s.DeploymentByID(ctx, "dep-vcs")
	if err != nil {
		t.Fatal(err)
	}
	if got.CommitSHA != "a1b2c3d" || got.PRURL != "https://example/pr/1" || got.CommitAuthor != "alice" {
		t.Fatalf("provenance round-trip mismatch: %+v", got)
	}

	// Re-upsert with empty provenance must not wipe the stored values.
	if err := s.UpsertDeployment(ctx, Deployment{
		ID: "dep-vcs", Service: "svc-v", Version: "v1", Env: "prod",
		FirstSeen: t2, LastSeen: t2,
	}); err != nil {
		t.Fatal(err)
	}
	got, err = s.DeploymentByID(ctx, "dep-vcs")
	if err != nil {
		t.Fatal(err)
	}
	if got.CommitSHA != "a1b2c3d" || got.PRURL != "https://example/pr/1" || got.CommitAuthor != "alice" {
		t.Fatalf("empty provenance clobbered stored values: %+v", got)
	}

	// And it round-trips through the window query too.
	win, err := s.DeploymentsInWindow(ctx, t1, t3, "svc-v")
	if err != nil {
		t.Fatal(err)
	}
	if len(win) != 1 || win[0].CommitSHA != "a1b2c3d" {
		t.Fatalf("window query missing provenance: %+v", win)
	}
}

// TestDeployErrorRateDelta locks the single-source threshold: the change ratio is
// gated by minRequests (the constant lives only here), and rates reflect a real
// before/after delta once enough samples exist.
func TestDeployErrorRateDelta(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	deploy := t2

	insert := func(success int, ts time.Time) {
		t.Helper()
		_, err := s.writer.ExecContext(ctx, `
			INSERT INTO events (trace_id, event_name, service, env, user_id, status_code, success, latency_ms, timestamp)
			VALUES ('tr', 'svc-r.request', 'svc-r', 'prod', 'u', 200, ?, 10, ?)`,
			success, ts.UTC().Format(tsFormat))
		if err != nil {
			t.Fatal(err)
		}
	}

	// Below threshold: a few samples each side → no ratio yet (too little signal).
	for i := 0; i < 3; i++ {
		insert(1, deploy.Add(-time.Minute))
		insert(0, deploy.Add(time.Minute))
	}
	d, err := s.DeployErrorRateDelta(ctx, "svc-r", deploy)
	if err != nil {
		t.Fatal(err)
	}
	if d.Ratio != nil {
		t.Fatalf("expected nil ratio below minRequests, got %v", *d.Ratio)
	}

	// Above threshold with a real baseline: a few failures before, many after.
	for i := 0; i < 10; i++ {
		insert(1, deploy.Add(-time.Minute)) // before: mostly clean...
		insert(0, deploy.Add(time.Minute))  // after: failing
	}
	for i := 0; i < 3; i++ {
		insert(0, deploy.Add(-time.Minute)) // ...with a small baseline of failures
	}
	d, err = s.DeployErrorRateDelta(ctx, "svc-r", deploy)
	if err != nil {
		t.Fatal(err)
	}
	if d.BeforeRate == nil || d.AfterRate == nil {
		t.Fatalf("expected computed rates, got before=%v after=%v", d.BeforeRate, d.AfterRate)
	}
	if *d.AfterRate <= *d.BeforeRate {
		t.Fatalf("expected after rate > before rate, got before=%.3f after=%.3f", *d.BeforeRate, *d.AfterRate)
	}
	if d.Ratio == nil || *d.Ratio <= 1 {
		t.Fatalf("expected ratio > 1 above minRequests with baseline, got %v", d.Ratio)
	}
}

// TestServiceErrorRateInWindow verifies error rate calculation from events table.
func TestServiceErrorRateInWindow(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	insertEvent := func(service string, success int, ts time.Time) {
		t.Helper()
		_, err := s.writer.ExecContext(ctx, `
			INSERT INTO events (trace_id, event_name, service, env, user_id, status_code, success, latency_ms, timestamp)
			VALUES (?, ?, ?, 'prod', 'user1', 200, ?, 10, ?)`,
			"trace-x", service+".request", service, success, ts.UTC().Format(tsFormat),
		)
		if err != nil {
			t.Fatal(err)
		}
	}

	// 3 success, 2 failure for svc-i in window.
	insertEvent("svc-i", 1, t1)
	insertEvent("svc-i", 1, t1)
	insertEvent("svc-i", 1, t2)
	insertEvent("svc-i", 0, t2)
	insertEvent("svc-i", 0, t2)
	// 1 event outside window.
	insertEvent("svc-i", 0, t3.Add(24*time.Hour))
	// Different service — should not count.
	insertEvent("svc-j", 0, t1)

	rate, err := s.ServiceErrorRateInWindow(ctx, "svc-i", t1, t3)
	if err != nil {
		t.Fatal(err)
	}
	if rate.Total != 5 {
		t.Errorf("Total: got %d, want 5", rate.Total)
	}
	if rate.Failures != 2 {
		t.Errorf("Failures: got %d, want 2", rate.Failures)
	}
}

// TestServiceErrorRateEmpty verifies zero counts when no matching events exist.
func TestServiceErrorRateEmpty(t *testing.T) {
	s := newTestStore(t)
	rate, err := s.ServiceErrorRateInWindow(context.Background(), "no-svc", t1, t3)
	if err != nil {
		t.Fatal(err)
	}
	if rate.Total != 0 || rate.Failures != 0 {
		t.Errorf("expected zeros, got %+v", rate)
	}
}
