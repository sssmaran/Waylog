// OTLP deploy auto-registration: spans carry service.version, and a version
// change for a (service, env) pair is the strongest deploy evidence an
// OTel-only install can produce. Registering it as a deployment makes the
// incident classifier's deploy correlation work without the deploy webhook.
package otel

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/sssmaran/WaylogCLI/internal/coldstore"
	eventv2 "github.com/sssmaran/WaylogCLI/pkg/event/v2"
)

// DeploymentUpserter is the slice of the cold store the tracker needs.
type DeploymentUpserter interface {
	UpsertDeployment(ctx context.Context, d coldstore.Deployment) error
}

// DeployTracker registers a deployment when a (service, env) tuple changes
// version within the process lifetime. The first version seen per tuple is
// tracked but never registered: after a restart, steady-state traffic must
// not fabricate a deploy anchored at boot time and poison deploy correlation.
type DeployTracker struct {
	store DeploymentUpserter

	mu          sync.Mutex
	lastVersion map[string]string // service + "\x00" + env → version
}

func NewDeployTracker(store DeploymentUpserter) *DeployTracker {
	return &DeployTracker{store: store, lastVersion: map[string]string{}}
}

// Observe scans successfully ingested events and upserts a deployment for
// every version change. Upsert failures are logged, not propagated: deploy
// registration must never fail span ingestion.
func (t *DeployTracker) Observe(ctx context.Context, events []*eventv2.Event) {
	if t == nil {
		return
	}
	type change struct{ service, env, version string }
	var changes []change

	t.mu.Lock()
	for _, ev := range events {
		if ev == nil || ev.Service == "" || ev.Version == "" {
			continue
		}
		key := ev.Service + "\x00" + ev.Env
		last, seen := t.lastVersion[key]
		if last == ev.Version {
			continue
		}
		t.lastVersion[key] = ev.Version
		if seen {
			changes = append(changes, change{ev.Service, ev.Env, ev.Version})
		}
	}
	t.mu.Unlock()

	now := time.Now().UTC()
	for _, c := range changes {
		dep := coldstore.Deployment{
			ID:        "otlp:" + c.service + ":" + c.env + ":" + c.version,
			Service:   c.service,
			Version:   c.version,
			Env:       c.env,
			FirstSeen: now,
			LastSeen:  now,
			Metadata:  map[string]string{"source": "otlp"},
		}
		if err := t.store.UpsertDeployment(ctx, dep); err != nil {
			slog.Warn("otlp: deploy auto-registration failed", "service", c.service, "version", c.version, "err", err)
		}
	}
}
