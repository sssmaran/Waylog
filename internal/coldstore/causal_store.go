package coldstore

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/sssmaran/WaylogCLI/internal/graph/causal"
)

func (s *SQLiteStore) SaveClaims(ctx context.Context, claims []causal.Claim) error {
	if len(claims) == 0 {
		return nil
	}

	tx, err := s.writer.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("causal: begin tx: %w", err)
	}
	defer tx.Rollback()

	now := time.Now().UTC().Format(tsFormat)

	for _, c := range claims {
		_, err := tx.ExecContext(ctx,
			`UPDATE causal_claims SET superseded_at = ? WHERE claim_type = ? AND subject = ? AND service = ? AND superseded_at IS NULL`,
			now, string(c.ClaimType), c.Subject, c.Service,
		)
		if err != nil {
			return fmt.Errorf("causal: supersede: %w", err)
		}

		evJSON, err := json.Marshal(c.Evidence)
		if err != nil {
			return fmt.Errorf("causal: marshal evidence: %w", err)
		}

		shadowInt := 0
		if c.ShadowMode {
			shadowInt = 1
		}

		_, err = tx.ExecContext(ctx,
			`INSERT INTO causal_claims (claim_type, subject, target, service, confidence, tier, evidence, window_start, window_end, shadow_mode)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			string(c.ClaimType), c.Subject, c.Target, c.Service,
			c.Confidence, string(c.Tier), string(evJSON),
			c.WindowStart.UTC().Format(tsFormat),
			c.WindowEnd.UTC().Format(tsFormat),
			shadowInt,
		)
		if err != nil {
			return fmt.Errorf("causal: insert claim: %w", err)
		}
	}

	return tx.Commit()
}

func (s *SQLiteStore) ActiveClaims(ctx context.Context, q causal.ClaimQuery) ([]causal.Claim, error) {
	rows, err := s.reader.QueryContext(ctx,
		`SELECT claim_type, subject, target, service, confidence, tier, evidence, window_start, window_end, shadow_mode
		 FROM causal_claims
		 WHERE claim_type = ? AND superseded_at IS NULL
		 ORDER BY confidence DESC`,
		string(q.ClaimType),
	)
	if err != nil {
		return nil, fmt.Errorf("causal: query active: %w", err)
	}
	defer rows.Close()

	var out []causal.Claim
	for rows.Next() {
		var (
			c         causal.Claim
			ct, tier  string
			evJSON    string
			ws, we    string
			shadowInt int
		)
		if err := rows.Scan(&ct, &c.Subject, &c.Target, &c.Service, &c.Confidence, &tier, &evJSON, &ws, &we, &shadowInt); err != nil {
			return nil, fmt.Errorf("causal: scan row: %w", err)
		}
		c.ClaimType = causal.ClaimType(ct)
		c.Tier = causal.ConfidenceTier(tier)
		c.ShadowMode = shadowInt == 1
		if err := json.Unmarshal([]byte(evJSON), &c.Evidence); err != nil {
			return nil, fmt.Errorf("causal: unmarshal evidence: %w", err)
		}
		c.WindowStart, _ = time.Parse(tsFormat, ws)
		c.WindowEnd, _ = time.Parse(tsFormat, we)
		out = append(out, c)
	}
	return out, rows.Err()
}
