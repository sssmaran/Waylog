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
	return out
}
