package ingest

import (
	"encoding/json"
	"log"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/sssmaran/WaylogCLI/internal/graph/build"
	"github.com/sssmaran/WaylogCLI/internal/graph/store"
	"github.com/sssmaran/WaylogCLI/internal/sampler"
	"github.com/sssmaran/WaylogCLI/pkg/event"
)

// Server handles HTTP requests for the ingest service.
type Server struct {
	store    *store.Store
	builder  *build.Builder
	sampler  *sampler.Sampler
	accepted atomic.Uint64
}

// ServerConfig holds configuration for creating a new Server.
type ServerConfig struct {
	Store   *store.Store
	Sampler *sampler.Sampler
}

// NewServer creates a new ingest server with the given configuration.
func NewServer(cfg ServerConfig) *Server {
	s := &Server{
		store:   cfg.Store,
		builder: build.NewBuilder(),
		sampler: cfg.Sampler,
	}
	if s.sampler == nil {
		s.sampler = sampler.New(sampler.LoadConfigFromEnv())
	}
	return s
}

// Health handles health check requests.
func (s *Server) Health(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("ok"))
}

// Events handles event ingestion requests.
func (s *Server) Events(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var ev event.WideEvent
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()

	if err := dec.Decode(&ev); err != nil {
		log.Println("INGEST: json decode failed:", err) // logging the error
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}

	// Ensure server-side timestamp sanity
	if ev.Timestamp.After(time.Now().Add(5 * time.Minute)) {
		http.Error(w, "timestamp too far in future", http.StatusBadRequest)
		return
	}

	if err := ev.Validate(); err != nil {
		log.Println("INGEST: event validation failed:", err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if !s.sampler.ShouldKeep(ev) {
		// Dropped by design — still return 202 so producers never retry
		w.WriteHeader(http.StatusAccepted)
		return
	}

	log.Printf(
		"EVENT trace=%s status=%d success=%v error=%v",
		ev.Request.TraceID,
		ev.Outcome.StatusCode,
		ev.Outcome.Success,
		ev.Error,
	)

	// Build graph from event and merge into store
	g := s.builder.Build(ev)
	if s.store != nil {
		s.store.Merge(g)
	}

	s.accepted.Add(1)
	w.WriteHeader(http.StatusAccepted)
}

// Store returns the server's graph store.
func (s *Server) Store() *store.Store {
	return s.store
}

// AcceptedCount returns the number of accepted events.
func (s *Server) AcceptedCount() uint64 {
	return s.accepted.Load()
}
