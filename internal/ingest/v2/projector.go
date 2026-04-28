package ingestv2

import (
	"log/slog"

	eventv2 "github.com/sssmaran/WaylogCLI/pkg/event/v2"
)

type EventProjector interface {
	Project(*eventv2.Event)
}

type Projector struct {
	index *RecentIndex
}

func NewProjector(index *RecentIndex) *Projector {
	return &Projector{index: index}
}

func (p *Projector) Project(ev *eventv2.Event) {
	if p == nil || p.index == nil {
		return
	}
	if !p.index.Insert(ev) && ev != nil {
		slog.Warn("ingestv2: projector saw duplicate event_id post-dedup", "event_id", ev.EventID)
	}
}
