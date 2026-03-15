package coldstore

import (
	"fmt"
	"strings"
	"time"
)

// SearchFilter defines the query parameters for searching cold-stored events.
type SearchFilter struct {
	TraceID   string
	UserID    string
	Service   string
	ErrorCode string
	Start     time.Time
	End       time.Time
	Limit     int
}

// SearchResult is a single row returned from a cold-store search.
type SearchResult struct {
	ID           int64     `json:"id"`
	TraceID      string    `json:"trace_id"`
	SpanID       string    `json:"span_id,omitempty"`
	EventName    string    `json:"event_name"`
	Service      string    `json:"service"`
	Env          string    `json:"env"`
	Version      string    `json:"version,omitempty"`
	DeploymentID string    `json:"deployment_id,omitempty"`
	UserID       string    `json:"user_id"`
	StatusCode   int       `json:"status_code"`
	Success      bool      `json:"success"`
	ErrorCode    string    `json:"error_code,omitempty"`
	ErrorMessage string    `json:"error_message,omitempty"`
	LatencyMs    int64     `json:"latency_ms"`
	Timestamp    time.Time `json:"timestamp"`
}

// SearchEvents queries the cold store for events matching the given filter.
// Results are ordered newest-first. Limit is clamped to [1, 200] (default 50).
func (s *Store) SearchEvents(f SearchFilter) ([]SearchResult, error) {
	if f.Limit <= 0 {
		f.Limit = 50
	}
	if f.Limit > 200 {
		f.Limit = 200
	}

	var conditions []string
	var args []any

	if f.TraceID != "" {
		conditions = append(conditions, "trace_id = ?")
		args = append(args, f.TraceID)
	}
	if f.UserID != "" {
		conditions = append(conditions, "user_id = ?")
		args = append(args, f.UserID)
	}
	if f.Service != "" {
		conditions = append(conditions, "service = ?")
		args = append(args, f.Service)
	}
	if f.ErrorCode != "" {
		conditions = append(conditions, "error_code = ?")
		args = append(args, f.ErrorCode)
	}
	if !f.Start.IsZero() {
		conditions = append(conditions, "timestamp >= ?")
		args = append(args, f.Start.UTC().Format(tsFormat))
	}
	if !f.End.IsZero() {
		conditions = append(conditions, "timestamp <= ?")
		args = append(args, f.End.UTC().Format(tsFormat))
	}

	where := ""
	if len(conditions) > 0 {
		where = "WHERE " + strings.Join(conditions, " AND ")
	}

	query := fmt.Sprintf(`SELECT id, trace_id, COALESCE(span_id,''), event_name,
		service, env, COALESCE(version,''), COALESCE(deployment_id,''),
		user_id, status_code, success,
		COALESCE(error_code,''), COALESCE(error_message,''),
		latency_ms, timestamp
		FROM events %s ORDER BY timestamp DESC LIMIT ?`, where)
	args = append(args, f.Limit)

	rows, err := s.reader.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("coldstore search: %w", err)
	}
	defer rows.Close()

	results := make([]SearchResult, 0)
	for rows.Next() {
		var r SearchResult
		var successInt int
		var tsStr string
		if err := rows.Scan(
			&r.ID, &r.TraceID, &r.SpanID, &r.EventName,
			&r.Service, &r.Env, &r.Version, &r.DeploymentID,
			&r.UserID, &r.StatusCode, &successInt,
			&r.ErrorCode, &r.ErrorMessage,
			&r.LatencyMs, &tsStr,
		); err != nil {
			return nil, fmt.Errorf("coldstore scan: %w", err)
		}
		r.Success = successInt != 0
		if t, err := time.Parse(tsFormat, tsStr); err != nil {
			return nil, fmt.Errorf("coldstore: bad timestamp %q in row %d: %w", tsStr, r.ID, err)
		} else {
			r.Timestamp = t
		}
		results = append(results, r)
	}

	return results, rows.Err()
}
