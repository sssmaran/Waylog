package handler

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"time"
)

func parseWindow(r *http.Request) time.Duration {
	w := r.URL.Query().Get("window")
	if w == "" {
		return time.Hour
	}
	d, err := time.ParseDuration(w)
	if err != nil || d <= 0 {
		return time.Hour
	}
	if d > 24*time.Hour {
		return 24 * time.Hour
	}
	return d
}

func parseLimit(r *http.Request) int {
	l := r.URL.Query().Get("limit")
	if l == "" {
		return 50
	}
	n, err := strconv.Atoi(l)
	if err != nil || n <= 0 {
		return 50
	}
	if n > 100 {
		return 100
	}
	return n
}

// GET /v1/agent/runs
func (h *Handler) ListRuns(w http.ResponseWriter, r *http.Request) {
	limit := parseLimit(r)
	status := r.URL.Query().Get("status")

	var before time.Time
	if cursor := r.URL.Query().Get("before"); cursor != "" {
		decoded, err := base64.URLEncoding.DecodeString(cursor)
		if err != nil {
			http.Error(w, "invalid cursor", http.StatusBadRequest)
			return
		}
		before, err = time.Parse(time.RFC3339Nano, string(decoded))
		if err != nil {
			http.Error(w, "invalid cursor", http.StatusBadRequest)
			return
		}
	}

	runs := h.store.ListRuns(limit, status, before)

	resp := map[string]any{"runs": runs}
	if len(runs) == limit {
		last := runs[len(runs)-1]
		cursor := base64.URLEncoding.EncodeToString([]byte(last.StartTime.Format(time.RFC3339Nano)))
		resp["next_cursor"] = cursor
	}

	writeJSON(w, resp)
}

// GET /v1/agent/runs/{id}
func (h *Handler) GetRun(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	run, ok := h.store.GetRun(id)
	if !ok {
		http.Error(w, "run not found", http.StatusNotFound)
		return
	}
	sessions := h.store.GetRunSessions(id)
	writeJSON(w, map[string]any{"run": run, "sessions": sessions})
}

// GET /v1/agent/sessions/{id}
func (h *Handler) GetSession(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	sess, ok := h.store.GetSession(id)
	if !ok {
		http.Error(w, "session not found", http.StatusNotFound)
		return
	}
	steps := h.store.GetSteps(id)
	if h.config.RedactPayloads {
		sess.Prompt = ""
		for i := range steps {
			steps[i].ToolInput = ""
			steps[i].ToolOutput = ""
		}
	}
	writeJSON(w, map[string]any{"session": sess, "steps": steps})
}

// WaterfallEntry represents a single step in the waterfall view.
type WaterfallEntry struct {
	RunID           string `json:"run_id"`
	SessionID       string `json:"session_id"`
	ParentSessionID string `json:"parent_session_id,omitempty"`
	StepID          string `json:"step_id"`
	Lane            int    `json:"lane"`
	OffsetMs        int64  `json:"offset_ms"`
	DurationMs      int64  `json:"duration_ms"`
	Kind            string `json:"kind"`
	Model           string `json:"model,omitempty"`
	ToolName        string `json:"tool_name,omitempty"`
	Status          string `json:"status"`
	StepIndex       int    `json:"step_index"`
}

// GET /v1/agent/sessions/{id}/waterfall
func (h *Handler) GetWaterfall(w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("id")
	sess, ok := h.store.GetSession(sessionID)
	if !ok {
		http.Error(w, "session not found", http.StatusNotFound)
		return
	}

	// Get all sessions in the run to assign lanes
	runSessions := h.store.GetRunSessions(sess.RunID)
	laneMap := make(map[string]int)
	for i, s := range runSessions {
		laneMap[s.SessionID] = i
	}

	var entries []WaterfallEntry
	for _, rs := range runSessions {
		steps := h.store.GetSteps(rs.SessionID)
		for _, step := range steps {
			status := "ok"
			if step.ToolError != "" {
				status = "error"
			}
			entries = append(entries, WaterfallEntry{
				RunID:           sess.RunID,
				SessionID:       rs.SessionID,
				ParentSessionID: rs.ParentSessionID,
				StepID:          step.StepID,
				Lane:            laneMap[rs.SessionID],
				OffsetMs:        step.OffsetMs,
				DurationMs:      step.LatencyMs,
				Kind:            "step",
				Model:           step.Model,
				ToolName:        step.ToolName,
				Status:          status,
				StepIndex:       step.StepIndex,
			})
		}
	}

	writeJSON(w, map[string]any{"waterfall": entries})
}

// GET /v1/agent/stats
func (h *Handler) GetStats(w http.ResponseWriter, r *http.Request) {
	window := parseWindow(r)
	stats := h.store.AggregateStats(window)
	writeJSON(w, stats)
}

// GET /v1/agent/cost
func (h *Handler) GetCost(w http.ResponseWriter, r *http.Request) {
	window := parseWindow(r)
	tokens := h.store.TokensByModel(window)

	type ModelCost struct {
		Model     string  `json:"model"`
		TokensIn  int     `json:"tokens_in"`
		TokensOut int     `json:"tokens_out"`
		CostIn    float64 `json:"cost_in"`
		CostOut   float64 `json:"cost_out"`
		Total     float64 `json:"total"`
	}

	var models []ModelCost
	var grandTotal float64
	for model, tc := range tokens {
		mc := ModelCost{
			Model:     model,
			TokensIn:  tc.TokensIn,
			TokensOut: tc.TokensOut,
		}
		if rate, ok := h.config.CostRates[model]; ok {
			mc.CostIn = float64(tc.TokensIn) / 1000 * rate.InputPer1K
			mc.CostOut = float64(tc.TokensOut) / 1000 * rate.OutputPer1K
			mc.Total = mc.CostIn + mc.CostOut
			grandTotal += mc.Total
		}
		models = append(models, mc)
	}
	sort.Slice(models, func(i, j int) bool { return models[i].Model < models[j].Model })

	writeJSON(w, map[string]any{
		"models":     models,
		"total_cost": grandTotal,
		"rates":      h.config.CostRates,
	})
}

// GET /v1/agent/tools
func (h *Handler) GetToolAnalytics(w http.ResponseWriter, r *http.Request) {
	window := parseWindow(r)
	tools := h.store.ToolAnalytics(window)
	writeJSON(w, map[string]any{"tools": tools})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}
