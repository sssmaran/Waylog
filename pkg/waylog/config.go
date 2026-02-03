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

	Kafka           KafkaConfig
	Transport       transport.Transport
	ErrorClassifier ErrorClassifier

	QueueSize       int
	BatchSize       int
	FlushInterval   time.Duration
	ShutdownTimeout time.Duration
}
