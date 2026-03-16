package ingest

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync/atomic"
	"time"
)

// SSEStream handles GET /v1/stream/dashboard.
func (s *Server) SSEStream(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	id, ch, err := s.sseHub.Subscribe()
	if err != nil {
		http.Error(w, "too many connections", http.StatusServiceUnavailable)
		return
	}
	defer s.sseHub.Unsubscribe(id)

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	var eventID atomic.Uint64
	writeEvent := func(topic string, data []byte) {
		eid := eventID.Add(1)
		fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", eid, topic, data)
		flusher.Flush()
	}

	// Initial snapshot in stable order
	s.sendInitialSnapshot(writeEvent)

	heartbeatInterval := s.sseHeartbeatInterval
	if heartbeatInterval == 0 {
		heartbeatInterval = 15 * time.Second
	}
	heartbeat := time.NewTicker(heartbeatInterval)
	defer heartbeat.Stop()

	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ch:
			events := s.sseHub.Latest(id)
			for topic, data := range events {
				writeEvent(topic, data)
			}
		case <-heartbeat.C:
			fmt.Fprint(w, ": heartbeat\n\n")
			flusher.Flush()
		}
	}
}

func (s *Server) sendInitialSnapshot(writeEvent func(string, []byte)) {
	topics := []string{TopicOverview, TopicTimeseries, TopicDeployments, TopicRoutes}
	for _, topic := range topics {
		data := s.ComputeSSETopic(topic)
		if data != nil {
			writeEvent(topic, data)
		}
	}
}

// ComputeSSETopic generates JSON bytes for a given SSE topic by computing
// the same data as the corresponding read endpoints.
func (s *Server) ComputeSSETopic(topic string) []byte {
	switch topic {
	case TopicOverview:
		return s.computeOverviewJSON()
	case TopicTimeseries:
		return s.computeTimeseriesJSON()
	case TopicRoutes:
		return s.computeRoutesJSON()
	case TopicDeployments:
		return s.computeDeploymentsJSON()
	default:
		return nil
	}
}

func (s *Server) computeOverviewJSON() []byte {
	if s.store == nil {
		return nil
	}
	payload := s.overviewPayload(time.Hour, 20)
	data, err := json.Marshal(payload)
	if err != nil {
		return nil
	}
	return data
}

func (s *Server) computeTimeseriesJSON() []byte {
	if s.store == nil {
		return nil
	}
	payload := s.timeseriesPayload(time.Hour, 5*time.Minute)
	data, err := json.Marshal(payload)
	if err != nil {
		return nil
	}
	return data
}

func (s *Server) computeRoutesJSON() []byte {
	if s.store == nil {
		return nil
	}
	payload := s.routesPayload(time.Hour, 20, false)
	data, err := json.Marshal(payload)
	if err != nil {
		return nil
	}
	return data
}

func (s *Server) computeDeploymentsJSON() []byte {
	if s.coldStore == nil {
		return []byte(`{"deployments":[]}`)
	}
	now := time.Now().UTC()
	start := now.Add(-time.Hour)
	out, err := s.deploymentsPayload(context.Background(), start, now, "")
	if err != nil {
		return []byte(`{"deployments":[]}`)
	}
	data, err := json.Marshal(map[string]any{"deployments": out})
	if err != nil {
		return []byte(`{"deployments":[]}`)
	}
	return data
}
