package coldstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/sssmaran/WaylogCLI/internal/signals"
)

type SignalStore struct {
	db *SQLiteStore
}

func NewSignalStore(db *SQLiteStore) *SignalStore {
	return &SignalStore{db: db}
}

func (s *SignalStore) Insert(ctx context.Context, sig *signals.Signal) error {
	resource, err := marshalMap(sig.Resource)
	if err != nil {
		return fmt.Errorf("coldstore signals marshal resource: %w", err)
	}
	metadata, err := marshalMap(sig.Metadata)
	if err != nil {
		return fmt.Errorf("coldstore signals marshal metadata: %w", err)
	}
	extra, err := marshalMap(sig.Extra)
	if err != nil {
		return fmt.Errorf("coldstore signals marshal extra: %w", err)
	}
	_, err = s.db.writer.ExecContext(ctx, `
		INSERT INTO signals (
			signal_id, type, source, service, env, severity, reason, message,
			resource, metadata, extra, timestamp, received_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		sig.SignalID, string(sig.Type), sig.Source, sig.Service, sig.Env, string(sig.Severity), sig.Reason, sig.Message,
		resource, metadata, extra, sig.Timestamp.UTC().Format(tsFormat), sig.ReceivedAt.UTC().Format(tsFormat),
	)
	if err != nil {
		return fmt.Errorf("coldstore insert signal: %w", err)
	}
	return nil
}

func (s *SignalStore) Query(ctx context.Context, f signals.Filter) ([]signals.Signal, error) {
	if f.Limit <= 0 {
		f.Limit = 200
	}
	if f.Limit > 200 {
		f.Limit = 200
	}
	conds := []string{}
	args := []any{}
	if f.Service != "" {
		conds = append(conds, "service = ?")
		args = append(args, f.Service)
	}
	if f.Env != "" {
		conds = append(conds, "env = ?")
		args = append(args, f.Env)
	}
	if f.Source != "" {
		conds = append(conds, "source = ?")
		args = append(args, f.Source)
	}
	if f.Reason != "" {
		conds = append(conds, "reason = ?")
		args = append(args, f.Reason)
	}
	if len(f.Types) > 0 {
		placeholders := make([]string, 0, len(f.Types))
		for _, typ := range f.Types {
			placeholders = append(placeholders, "?")
			args = append(args, string(typ))
		}
		conds = append(conds, "type IN ("+strings.Join(placeholders, ", ")+")")
	}
	if !f.Since.IsZero() {
		conds = append(conds, "timestamp >= ?")
		args = append(args, f.Since.UTC().Format(tsFormat))
	}
	if !f.Until.IsZero() {
		conds = append(conds, "timestamp <= ?")
		args = append(args, f.Until.UTC().Format(tsFormat))
	}
	where := ""
	if len(conds) > 0 {
		where = "WHERE " + strings.Join(conds, " AND ")
	}
	query := fmt.Sprintf(`SELECT signal_id, type, source, service, env, severity, reason,
		COALESCE(message, ''), COALESCE(resource, ''), COALESCE(metadata, ''), COALESCE(extra, ''),
		timestamp, received_at
		FROM signals %s ORDER BY timestamp DESC, signal_id DESC LIMIT ?`, where)
	args = append(args, f.Limit)
	rows, err := s.db.reader.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("coldstore query signals: %w", err)
	}
	defer rows.Close()
	out := make([]signals.Signal, 0, f.Limit)
	for rows.Next() {
		sig, err := scanSignal(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, sig)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *SignalStore) PruneOlderThan(ctx context.Context, cutoff time.Time) (int, error) {
	res, err := s.db.writer.ExecContext(ctx, `DELETE FROM signals WHERE timestamp < ?`, cutoff.UTC().Format(tsFormat))
	if err != nil {
		return 0, fmt.Errorf("coldstore prune signals: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("coldstore prune signals rows affected: %w", err)
	}
	return int(n), nil
}

func scanSignal(rows interface {
	Scan(dest ...any) error
}) (signals.Signal, error) {
	var sig signals.Signal
	var typ, severity, timestamp, receivedAt string
	var resource, metadata, extra string
	if err := rows.Scan(
		&sig.SignalID, &typ, &sig.Source, &sig.Service, &sig.Env, &severity, &sig.Reason,
		&sig.Message, &resource, &metadata, &extra, &timestamp, &receivedAt,
	); err != nil {
		return signals.Signal{}, fmt.Errorf("coldstore scan signal: %w", err)
	}
	sig.Type = signals.Type(typ)
	sig.Severity = signals.Severity(severity)
	ts, err := time.Parse(tsFormat, timestamp)
	if err != nil {
		return signals.Signal{}, fmt.Errorf("coldstore signal timestamp: %w", err)
	}
	sig.Timestamp = ts
	recv, err := time.Parse(tsFormat, receivedAt)
	if err != nil {
		return signals.Signal{}, fmt.Errorf("coldstore signal received_at: %w", err)
	}
	sig.ReceivedAt = recv
	if sig.Resource, err = unmarshalMap(resource); err != nil {
		return signals.Signal{}, fmt.Errorf("coldstore signal resource: %w", err)
	}
	if sig.Metadata, err = unmarshalMap(metadata); err != nil {
		return signals.Signal{}, fmt.Errorf("coldstore signal metadata: %w", err)
	}
	if sig.Extra, err = unmarshalMap(extra); err != nil {
		return signals.Signal{}, fmt.Errorf("coldstore signal extra: %w", err)
	}
	return sig, nil
}

func marshalMap(m map[string]any) (sql.NullString, error) {
	if len(m) == 0 {
		return sql.NullString{}, nil
	}
	b, err := json.Marshal(m)
	if err != nil {
		return sql.NullString{}, err
	}
	return sql.NullString{String: string(b), Valid: true}, nil
}

func unmarshalMap(raw string) (map[string]any, error) {
	if raw == "" {
		return nil, nil
	}
	out := map[string]any{}
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil, err
	}
	return out, nil
}

var _ signals.Store = (*SignalStore)(nil)
