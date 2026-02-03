package transport

import (
	"context"

	"github.com/sssmaran/WaylogCLI/pkg/event"
)

type Transport interface {
	Send(ctx context.Context, batch []event.WideEvent) error
	Close(ctx context.Context) error
}

type KafkaConfig struct {
	Brokers      []string
	Topic        string
	BatchBytes   int64
	RequiredAcks int
	Compression  string
}
