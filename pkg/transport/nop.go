package transport

import (
	"context"

	"github.com/sssmaran/WaylogCLI/pkg/event"
)

type NopTransport struct{}

func (t *NopTransport) Send(ctx context.Context, batch []event.WideEvent) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	return len(batch), nil
}

func (t *NopTransport) Close(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return nil
}
