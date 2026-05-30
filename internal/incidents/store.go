package incidents

import (
	"context"
	"errors"
	"sort"
	"sync"
	"time"
)

var ErrNotFound = errors.New("incidents: not found")

type Store interface {
	Upsert(ctx context.Context, inc Incident) error
	ReplaceNonResolved(ctx context.Context, rows []Incident) error
	Get(ctx context.Context, id string) (Incident, error)
	ListActive(ctx context.Context) ([]Incident, error)
	PruneResolvedOlderThan(ctx context.Context, cutoff time.Time) (int, error)
}

type MemoryStore struct {
	mu   sync.Mutex
	rows map[string]Incident
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{rows: map[string]Incident{}}
}

func (s *MemoryStore) Upsert(_ context.Context, inc Incident) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rows[inc.IncidentID] = cloneIncident(inc)
	return nil
}

func (s *MemoryStore) ReplaceNonResolved(_ context.Context, rows []Incident) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, inc := range s.rows {
		if inc.Status != StatusResolved {
			delete(s.rows, id)
		}
	}
	for _, inc := range rows {
		s.rows[inc.IncidentID] = cloneIncident(inc)
	}
	return nil
}

func (s *MemoryStore) Get(_ context.Context, id string) (Incident, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	inc, ok := s.rows[id]
	if !ok {
		return Incident{}, ErrNotFound
	}
	return cloneIncident(inc), nil
}

func (s *MemoryStore) ListActive(_ context.Context) ([]Incident, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Incident, 0, len(s.rows))
	for _, inc := range s.rows {
		if inc.Status != StatusResolved {
			out = append(out, cloneIncident(inc))
		}
	}
	sortIncidents(out)
	return out, nil
}

func (s *MemoryStore) PruneResolvedOlderThan(_ context.Context, cutoff time.Time) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	deleted := 0
	for id, inc := range s.rows {
		if inc.Status == StatusResolved && inc.ResolvedAt != nil && inc.ResolvedAt.Before(cutoff) {
			delete(s.rows, id)
			deleted++
		}
	}
	return deleted, nil
}

func sortIncidents(rows []Incident) {
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].Severity != rows[j].Severity {
			return rows[i].Severity > rows[j].Severity
		}
		if !rows[i].StartedAt.Equal(rows[j].StartedAt) {
			return rows[i].StartedAt.After(rows[j].StartedAt)
		}
		return rows[i].IncidentID < rows[j].IncidentID
	})
}

func cloneIncident(in Incident) Incident {
	out := in
	out.TopServices = append([]string(nil), in.TopServices...)
	out.SampleTraces = append([]string(nil), in.SampleTraces...)
	out.Evidence = append([]Evidence(nil), in.Evidence...)
	out.NextChecks = append([]string(nil), in.NextChecks...)
	out.InstrumentationWarnings = append([]string(nil), in.InstrumentationWarnings...)
	if in.AffectedUsers != nil {
		v := *in.AffectedUsers
		out.AffectedUsers = &v
	}
	if in.RecoveringAt != nil {
		v := *in.RecoveringAt
		out.RecoveringAt = &v
	}
	if in.ResolvedAt != nil {
		v := *in.ResolvedAt
		out.ResolvedAt = &v
	}
	out.Propagation = clonePropagationSnapshot(in.Propagation)
	out.Blast = cloneBlastSnapshot(in.Blast)
	out.Alerts = cloneAlertSnapshot(in.Alerts)
	out.Runtime = cloneRuntimeSnapshot(in.Runtime)
	return out
}

func clonePropagationEvidence(p *PropagationEvidence) *PropagationEvidence {
	if p == nil {
		return nil
	}
	out := *p
	if p.Path != nil {
		out.Path = append([]PropagationStep(nil), p.Path...)
	}
	if p.FirstSeenAt != nil {
		t := *p.FirstSeenAt
		out.FirstSeenAt = &t
	}
	return &out
}

func clonePropagationSnapshot(s *PropagationSnapshot) *PropagationSnapshot {
	if s == nil {
		return nil
	}
	return &PropagationSnapshot{
		Opening: clonePropagationEvidence(s.Opening),
		Latest:  clonePropagationEvidence(s.Latest),
	}
}

func cloneBlastEvidence(b *BlastEvidence) *BlastEvidence {
	if b == nil {
		return nil
	}
	out := *b
	if b.AffectedUsers != nil {
		u := *b.AffectedUsers
		out.AffectedUsers = &u
	}
	if b.TopServices != nil {
		out.TopServices = append([]string(nil), b.TopServices...)
	}
	if b.SampledTraces != nil {
		out.SampledTraces = append([]string(nil), b.SampledTraces...)
	}
	return &out
}

func cloneBlastSnapshot(s *BlastSnapshot) *BlastSnapshot {
	if s == nil {
		return nil
	}
	return &BlastSnapshot{
		Opening: cloneBlastEvidence(s.Opening),
		Latest:  cloneBlastEvidence(s.Latest),
	}
}

func cloneAlertEvidence(a *AlertEvidence) *AlertEvidence {
	if a == nil {
		return nil
	}
	out := *a
	if a.Matches != nil {
		out.Matches = append([]MatchedAlert(nil), a.Matches...)
		for i := range out.Matches {
			out.Matches[i].EvidenceIDs = append([]string(nil), a.Matches[i].EvidenceIDs...)
		}
	}
	return &out
}

func cloneAlertSnapshot(s *AlertSnapshot) *AlertSnapshot {
	if s == nil {
		return nil
	}
	return &AlertSnapshot{
		Opening: cloneAlertEvidence(s.Opening),
		Latest:  cloneAlertEvidence(s.Latest),
	}
}

func cloneRuntimeEvidence(r *RuntimeEvidence) *RuntimeEvidence {
	if r == nil {
		return nil
	}
	out := *r
	if r.Metadata != nil {
		out.Metadata = make(map[string]any, len(r.Metadata))
		for k, v := range r.Metadata {
			out.Metadata[k] = v
		}
	}
	return &out
}

func cloneRuntimeSnapshot(s *RuntimeSnapshot) *RuntimeSnapshot {
	if s == nil {
		return nil
	}
	out := &RuntimeSnapshot{
		Opening: cloneRuntimeEvidence(s.Opening),
		Latest:  cloneRuntimeEvidence(s.Latest),
	}
	if s.Matches != nil {
		out.Matches = make([]RuntimeEvidence, len(s.Matches))
		for i := range s.Matches {
			out.Matches[i] = *cloneRuntimeEvidence(&s.Matches[i])
		}
	}
	return out
}
