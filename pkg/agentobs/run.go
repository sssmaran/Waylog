package agentobs

import (
	"context"
	"sync"
	"time"

	"github.com/google/uuid"
)

type Run struct {
	client  *Client
	runID   string
	endOnce sync.Once
}

func (r *Run) StartSession(ctx context.Context, agentName string, opts ...SessionOption) *Session {
	cfg := sessionConfig{}
	for _, o := range opts {
		o(&cfg)
	}
	sessionID := uuid.New().String()
	ev := map[string]any{
		"event_id":       uuid.New().String(),
		"run_id":         r.runID,
		"session_id":     sessionID,
		"event_type":     "session.start",
		"timestamp":      time.Now().Format(time.RFC3339Nano),
		"schema_version": "1.0",
		"agent_name":     agentName,
	}
	if cfg.version != "" {
		ev["agent_version"] = cfg.version
	}
	if cfg.prompt != "" {
		ev["prompt"] = cfg.prompt
	}
	r.client.emit(ev)
	sess := &Session{client: r.client, runID: r.runID, sessionID: sessionID, startTime: time.Now()}
	r.client.trackSession(sess)
	sess.startHeartbeat(r.client.cfg.heartbeatInterval)
	return sess
}

func (r *Run) End(ctx context.Context, success bool, errMsg string) error {
	r.endOnce.Do(func() {
		ev := map[string]any{
			"event_id":       uuid.New().String(),
			"run_id":         r.runID,
			"event_type":     "run.end",
			"timestamp":      time.Now().Format(time.RFC3339Nano),
			"schema_version": "1.0",
			"success":        success,
		}
		if errMsg != "" {
			ev["error_message"] = errMsg
		}
		r.client.emit(ev)
	})
	return nil
}
