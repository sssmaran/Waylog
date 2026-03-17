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
	Cursor    int64
}

// SearchPage is the paginated result of a cold-store search.
type SearchPage struct {
	Results    []SearchResult `json:"results"`
	NextCursor int64          `json:"next_cursor,omitempty"`
	TotalCount int            `json:"total_count"`
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
// Results are ordered newest-first by rowid. Limit is clamped to [1, 200] (default 50).
// When Cursor > 0, only rows with id < Cursor are returned (keyset pagination).
func (s *Store) SearchEvents(f SearchFilter) (SearchPage, error) {
	if f.Limit <= 0 {
		f.Limit = 50
	}
	if f.Limit > 200 {
		f.Limit = 200
	}

	// Build shared filter conditions (everything except cursor).
	var baseConds []string
	var baseArgs []any

	if f.TraceID != "" {
		baseConds = append(baseConds, "trace_id = ?")
		baseArgs = append(baseArgs, f.TraceID)
	}
	if f.UserID != "" {
		baseConds = append(baseConds, "user_id = ?")
		baseArgs = append(baseArgs, f.UserID)
	}
	if f.Service != "" {
		baseConds = append(baseConds, "service = ?")
		baseArgs = append(baseArgs, f.Service)
	}
	if f.ErrorCode != "" {
		baseConds = append(baseConds, "error_code = ?")
		baseArgs = append(baseArgs, f.ErrorCode)
	}
	if !f.Start.IsZero() {
		baseConds = append(baseConds, "timestamp >= ?")
		baseArgs = append(baseArgs, f.Start.UTC().Format(tsFormat))
	}
	if !f.End.IsZero() {
		baseConds = append(baseConds, "timestamp <= ?")
		baseArgs = append(baseArgs, f.End.UTC().Format(tsFormat))
	}

	baseWhere := ""
	if len(baseConds) > 0 {
		baseWhere = "WHERE " + strings.Join(baseConds, " AND ")
	}

	// COUNT total matching rows (without cursor).
	countQuery := fmt.Sprintf("SELECT COUNT(*) FROM events %s", baseWhere)
	var totalCount int
	if err := s.reader.QueryRow(countQuery, baseArgs...).Scan(&totalCount); err != nil {
		return SearchPage{}, fmt.Errorf("coldstore count: %w", err)
	}

	// Build SELECT conditions: base filters + optional cursor.
	selectConds := append([]string{}, baseConds...)
	selectArgs := append([]any{}, baseArgs...)
	if f.Cursor > 0 {
		selectConds = append(selectConds, "id < ?")
		selectArgs = append(selectArgs, f.Cursor)
	}

	selectWhere := ""
	if len(selectConds) > 0 {
		selectWhere = "WHERE " + strings.Join(selectConds, " AND ")
	}

	query := fmt.Sprintf(`SELECT id, trace_id, COALESCE(span_id,''), event_name,
		service, env, COALESCE(version,''), COALESCE(deployment_id,''),
		user_id, status_code, success,
		COALESCE(error_code,''), COALESCE(error_message,''),
		latency_ms, timestamp
		FROM events %s ORDER BY id DESC LIMIT ?`, selectWhere)
	selectArgs = append(selectArgs, f.Limit+1) // fetch one extra to detect next page

	rows, err := s.reader.Query(query, selectArgs...)
	if err != nil {
		return SearchPage{}, fmt.Errorf("coldstore search: %w", err)
	}
	defer rows.Close()

	results := make([]SearchResult, 0, f.Limit)
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
			return SearchPage{}, fmt.Errorf("coldstore scan: %w", err)
		}
		r.Success = successInt != 0
		if t, err := time.Parse(tsFormat, tsStr); err != nil {
			return SearchPage{}, fmt.Errorf("coldstore: bad timestamp %q in row %d: %w", tsStr, r.ID, err)
		} else {
			r.Timestamp = t
		}
		results = append(results, r)
	}
	if err := rows.Err(); err != nil {
		return SearchPage{}, err
	}

	var page SearchPage
	page.TotalCount = totalCount

	if len(results) > f.Limit {
		results = results[:f.Limit]
		page.NextCursor = results[f.Limit-1].ID
	}
	page.Results = results

	return page, nil
}
