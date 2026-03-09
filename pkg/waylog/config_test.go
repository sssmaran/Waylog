package waylog

import (
	"errors"
	"testing"

	"github.com/sssmaran/WaylogCLI/pkg/waylog/transport"
)

func TestValidate_MissingService(t *testing.T) {
	cfg := Config{Env: "test"}
	if err := cfg.Validate(); !errors.Is(err, ErrServiceRequired) {
		t.Fatalf("got %v, want ErrServiceRequired", err)
	}
}

func TestValidate_MissingEnv(t *testing.T) {
	cfg := Config{Service: "svc"}
	if err := cfg.Validate(); !errors.Is(err, ErrEnvRequired) {
		t.Fatalf("got %v, want ErrEnvRequired", err)
	}
}

func TestValidate_AmbiguousTransport(t *testing.T) {
	cfg := Config{
		Service:   "svc",
		Env:       "test",
		IngestURL: "http://localhost:8080",
		Kafka:     transport.KafkaConfig{Brokers: []string{"localhost:9092"}},
	}
	if err := cfg.Validate(); !errors.Is(err, ErrTransportAmbiguous) {
		t.Fatalf("got %v, want ErrTransportAmbiguous", err)
	}
}

func TestValidate_HappyPath(t *testing.T) {
	cfg := Config{
		Service:   "svc",
		Env:       "test",
		IngestURL: "http://localhost:8080",
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidate_NoTransport(t *testing.T) {
	cfg := Config{Service: "svc", Env: "test"}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}
