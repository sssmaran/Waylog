package agentobs

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
)

type Session struct {
	client    *Client
	runID     string
	sessionID string
	startTime time.Time
	stepIdx   atomic.Int32
	endOnce   sync.Once
	hbCancel  context.CancelFunc
}

func (s *Session) startHeartbeat(interval time.Duration) {
	if interval <= 0 {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.hbCancel = cancel
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.client.emit(map[string]any{
					"event_id":       uuid.New().String(),
					"run_id":         s.runID,
					"session_id":     s.sessionID,
					"event_type":     "session.heartbeat",
					"timestamp":      time.Now().Format(time.RFC3339Nano),
					"schema_version": "1.0",
				})
			}
		}
	}()
}

func (s *Session) Step(name string) *Step {
	stepID := uuid.New().String()
	idx := int(s.stepIdx.Add(1) - 1)
	s.client.emit(map[string]any{
		"event_id":       uuid.New().String(),
		"run_id":         s.runID,
		"session_id":     s.sessionID,
		"step_id":        stepID,
		"step_index":     idx,
		"step_name":      name,
		"event_type":     "step.start",
		"timestamp":      time.Now().Format(time.RFC3339Nano),
		"schema_version": "1.0",
	})
	return &Step{
		client:    s.client,
		runID:     s.runID,
		sessionID: s.sessionID,
		stepID:    stepID,
		stepName:  name,
		stepIndex: idx,
		startTime: time.Now(),
	}
}

func (s *Session) Delegate(ctx context.Context, agentName string, triggerStep *Step, opts ...SessionOption) *Session {
	cfg := sessionConfig{}
	for _, o := range opts {
		o(&cfg)
	}
	childID := uuid.New().String()
	ev := map[string]any{
		"event_id":          uuid.New().String(),
		"run_id":            s.runID,
		"session_id":        childID,
		"parent_session_id": s.sessionID,
		"event_type":        "session.start",
		"timestamp":         time.Now().Format(time.RFC3339Nano),
		"schema_version":    "1.0",
		"agent_name":        agentName,
	}
	if triggerStep != nil {
		ev["trigger_step_id"] = triggerStep.stepID
	}
	if cfg.version != "" {
		ev["agent_version"] = cfg.version
	}
	s.client.emit(ev)
	child := &Session{client: s.client, runID: s.runID, sessionID: childID, startTime: time.Now()}
	s.client.trackSession(child)
	child.startHeartbeat(s.client.cfg.heartbeatInterval)
	return child
}

func (s *Session) End(ctx context.Context, success bool, errMsg string) error {
	s.endOnce.Do(func() {
		if s.hbCancel != nil {
			s.hbCancel()
		}
		ev := map[string]any{
			"event_id":       uuid.New().String(),
			"run_id":         s.runID,
			"session_id":     s.sessionID,
			"event_type":     "session.end",
			"timestamp":      time.Now().Format(time.RFC3339Nano),
			"schema_version": "1.0",
			"success":        success,
		}
		if errMsg != "" {
			ev["error_message"] = errMsg
		}
		s.client.emit(ev)
	})
	return nil
}
