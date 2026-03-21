package kafka

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"

	"github.com/segmentio/kafka-go"
	"github.com/sssmaran/WaylogCLI/pkg/event"
)

var errTransportClosed = errors.New("transport closed")

type Transport struct {
	writer *kafka.Writer
	closed atomic.Bool
}

func New(cfg Config) (*Transport, error) {
	if len(cfg.Brokers) == 0 {
		return nil, errors.New("kafka brokers are required")
	}
	topic := cfg.Topic
	if topic == "" {
		topic = "wide_events"
	}

	writer := &kafka.Writer{
		Addr:         kafka.TCP(cfg.Brokers...),
		Topic:        topic,
		BatchBytes:   cfg.BatchBytes,
		RequiredAcks: kafka.RequiredAcks(cfg.RequiredAcks),
		Compression:  parseCompression(cfg.Compression),
	}

	return &Transport{writer: writer}, nil
}

func (t *Transport) Send(ctx context.Context, batch []event.WideEvent) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if t.closed.Load() {
		return 0, errTransportClosed
	}
	if len(batch) == 0 {
		return 0, nil
	}

	msgs := make([]kafka.Message, 0, len(batch))
	for _, ev := range batch {
		payload, err := json.Marshal(ev)
		if err != nil {
			return 0, err
		}
		msgs = append(msgs, kafka.Message{Value: payload})
	}

	if err := t.writer.WriteMessages(ctx, msgs...); err != nil {
		return 0, err
	}
	return len(batch), nil
}

func (t *Transport) Close(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !t.closed.CompareAndSwap(false, true) {
		return nil
	}
	return t.writer.Close()
}

func parseCompression(value string) kafka.Compression {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "gzip":
		return kafka.Gzip
	case "snappy":
		return kafka.Snappy
	case "lz4":
		return kafka.Lz4
	case "zstd":
		return kafka.Zstd
	default:
		return kafka.Compression(0)
	}
}
