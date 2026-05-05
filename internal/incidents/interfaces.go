package incidents

import (
	"context"
	"time"

	"github.com/sssmaran/WaylogCLI/internal/signals"
	apiv2 "github.com/sssmaran/WaylogCLI/pkg/api/v2"
	eventv2 "github.com/sssmaran/WaylogCLI/pkg/event/v2"
)

type Reader interface {
	Errors(f SearchFilter, limit int) ErrorsResult
	BlastRadius(f SearchFilter, key apiv2.BlastKey) apiv2.BlastRadiusResponse
	SearchEvents(f SearchFilter, limit int) []*eventv2.Event
}

type SearchFilter struct {
	Service   string
	Statuses  map[eventv2.Status]struct{}
	ErrorCode string
	Since     time.Time
	Until     time.Time
}

type ErrorsResult struct {
	Rows []apiv2.ErrorRow
}

type SignalStore interface {
	Query(ctx context.Context, f signals.Filter) ([]signals.Signal, error)
}

type DeploySource interface {
	DeploymentsInWindow(ctx context.Context, start, end time.Time, serviceFilter string) ([]Deployment, error)
}
