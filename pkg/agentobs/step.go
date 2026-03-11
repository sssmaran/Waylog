package agentobs

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/google/uuid"
)

type Step struct {
	client     *Client
	runID      string
	sessionID  string
	stepID     string
	stepName   string
	stepIndex  int
	startTime  time.Time
	model      string
	tokensIn   int
	tokensOut  int
	toolName   string
	toolInput  string
	toolOutput string
	toolError  string
	endOnce    sync.Once
}

func (st *Step) SetModel(m string)  { st.model = m }
func (st *Step) SetTokensIn(n int)  { st.tokensIn = n }
func (st *Step) SetTokensOut(n int) { st.tokensOut = n }

func (st *Step) RecordToolCall(name string, input any, output any, err error) {
	st.toolName = name
	if input != nil {
		if b, e := json.Marshal(input); e == nil {
			st.toolInput = string(b)
		}
	}
	if output != nil {
		if b, e := json.Marshal(output); e == nil {
			st.toolOutput = string(b)
		}
	}
	if err != nil {
		st.toolError = err.Error()
	}
}

func (st *Step) End(ctx context.Context) error {
	st.endOnce.Do(func() {
		latency := time.Since(st.startTime).Milliseconds()
		ev := map[string]any{
			"event_id":       uuid.New().String(),
			"run_id":         st.runID,
			"session_id":     st.sessionID,
			"step_id":        st.stepID,
			"step_name":      st.stepName,
			"step_index":     st.stepIndex,
			"event_type":     "step.end",
			"timestamp":      time.Now().Format(time.RFC3339Nano),
			"schema_version": "1.0",
			"latency_ms":     latency,
			"model":          st.model,
			"tokens_in":      st.tokensIn,
			"tokens_out":     st.tokensOut,
			"tool_name":      st.toolName,
			"tool_input":     st.toolInput,
			"tool_output":    st.toolOutput,
			"tool_error":     st.toolError,
		}
		st.client.emit(ev)
	})
	return nil
}
