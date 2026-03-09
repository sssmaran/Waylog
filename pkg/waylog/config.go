package waylog

import (
	"time"

	"github.com/sssmaran/WaylogCLI/pkg/waylog/transport"
)

type KafkaConfig = transport.KafkaConfig

type ErrorClassifier func(error) string

type Config struct {
	Service      string
	Env          string
	Version      string
	DeploymentID string
	IngestURL    string // HTTP ingest endpoint (e.g. "http://localhost:8080")

	Kafka           KafkaConfig
	Transport       transport.Transport
	ErrorClassifier ErrorClassifier

	QueueSize       int
	BatchSize       int
	FlushInterval   time.Duration
	ShutdownTimeout time.Duration
}

// Validate checks required fields and ensures at most one transport source is set.
func (c Config) Validate() error {
	if c.Service == "" {
		return ErrServiceRequired
	}
	if c.Env == "" {
		return ErrEnvRequired
	}
	n := 0
	if c.IngestURL != "" {
		n++
	}
	if len(c.Kafka.Brokers) > 0 {
		n++
	}
	if c.Transport != nil {
		n++
	}
	if n > 1 {
		return ErrTransportAmbiguous
	}
	return nil
}
