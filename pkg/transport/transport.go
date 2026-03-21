package transport

import (
	"context"

	"github.com/sssmaran/WaylogCLI/pkg/event"
)

// Transport defines the event delivery interface.
//
// BREAKING CHANGE (SDK v2): Send now returns (int, error) instead of error.
// The int is the count of events successfully sent. Custom Transport
// implementations must update their signature. For an all-or-nothing
// transport, return (len(batch), nil) on success or (0, err) on failure.
type Transport interface {
	Send(ctx context.Context, batch []event.WideEvent) (int, error)
	Close(ctx context.Context) error
}
