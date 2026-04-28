package ingestv2

import (
	"encoding/json"
	"log/slog"
	"time"

	eventlogv2 "github.com/sssmaran/WaylogCLI/internal/eventlog/v2"
	"github.com/sssmaran/WaylogCLI/internal/metrics"
	eventv2 "github.com/sssmaran/WaylogCLI/pkg/event/v2"
)

const replayRotationSlack = 5 * time.Minute

type ReplayResult struct {
	DedupLoaded int
	Projected   int
	DecodeFails int
}

func ReplayWAL(dir string, dedup *Dedup, projector EventProjector, since time.Time, m *metrics.Metrics) (ReplayResult, error) {
	schema, err := eventv2.CompileEmbeddedSchema()
	if err != nil {
		return ReplayResult{}, err
	}
	fileSince := since
	if !fileSince.IsZero() {
		fileSince = fileSince.Add(-replayRotationSlack)
	}

	var result ReplayResult
	_, err = eventlogv2.Replay(dir, fileSince, func(rawLine []byte) error {
		var raw any
		if err := json.Unmarshal(rawLine, &raw); err != nil {
			recordReplaySkip(m, "malformed_json")
			slog.Warn("ingestv2: skipping malformed replay line", "err", err)
			return nil
		}
		if err := eventv2.ValidateAny(schema, raw); err != nil {
			recordReplaySkip(m, "schema_invalid")
			slog.Warn("ingestv2: skipping schema-invalid replay line", "err", err)
			return nil
		}
		var ev eventv2.Event
		if err := json.Unmarshal(rawLine, &ev); err != nil {
			result.DecodeFails++
			if m != nil {
				m.V2TypedDecodeFailed.Inc()
			}
			recordReplaySkip(m, "typed_decode")
			slog.Warn("ingestv2: skipping typed-decode replay line", "err", err)
			return nil
		}
		if !since.IsZero() && ev.TsEnd.Before(since) {
			recordReplaySkip(m, "stale")
			return nil
		}
		dedup.Add(ev.EventID)
		result.DedupLoaded++
		if projector != nil {
			projector.Project(&ev)
			result.Projected++
		}
		return nil
	})
	return result, err
}

func recordReplaySkip(m *metrics.Metrics, reason string) {
	if m != nil {
		m.V2ReplaySkipped.WithLabelValues(reason).Inc()
	}
}
