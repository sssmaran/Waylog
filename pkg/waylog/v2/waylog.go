// Package waylogv2 is the Waylog v2.0 SDK core (local mode).
//
// Public surface: Init, Shutdown, Stats, Logger, From, Step, StepVoid, Fail,
// NewError, Suppress, Explain. Internal-but-exported helpers for middleware
// and adapter authors: Begin, Finalize, FinalizePanic, FinalizeAborted,
// FinalizeTimeout, SetField, SetHTTPStatus, SetHTTPRoute, TraceID, SpanID,
// RecordOutgoingSpan.
package waylogv2

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	transporthttp "github.com/sssmaran/WaylogCLI/pkg/transport/http"
)

// F is the field bag used by Logger calls and request fields.
type F = map[string]any

// Config configures the SDK. Set once via Init.
type Config struct {
	Service string
	Env     string
	Version string

	// Output is the local writer for final wide events when IngestURL is empty.
	// Defaults to os.Stderr. One JSON event per line.
	Output io.Writer

	// IngestURL / APIKey enable HTTP delivery transport. When IngestURL is
	// set, final events are submitted to /v1/events instead of being written
	// to Output. Dev dual-emit remains local-only.
	IngestURL string
	APIKey    string

	DevMode bool

	MaxSteps         int
	MaxLogs          int
	MaxRequestAge    time.Duration
	MaxBufferBytes   int
	MaxInFlightBytes int64
	MaxEventsPerSec  int

	Redactor func(F) F
}

const (
	defaultMaxSteps       = 128
	defaultMaxLogs        = 256
	defaultMaxBufferBytes = 512 * 1024
)

// StatsSnapshot is a point-in-time view of SDK counters.
//
// Go disallows a type and a free function sharing a name at package scope, so
// the spec's `func Stats() Stats` is rendered here as `func Stats() StatsSnapshot`.
type StatsSnapshot struct {
	ActiveRequests          int64
	EventsEmitted           int64 // total final events successfully written (includes suppressed)
	EventsSuppressed        int64 // subset of EventsEmitted with status=suppressed
	StepsDropped            int64
	LogsDropped             int64
	BytesDroppedFromBuffer  int64
	BufferOverflows         int64 // requests degraded to header-only by MaxBufferBytes pressure
	ReservedCodeRejections  int64 // NewError/Fail/Step returns with WAYLOG_* codes
	SuppressedThenFailed    int64
	LateCompletionAfterEmit int64
	EventsDropped           int64
	DeliveryFailures        int64
}

// ErrAlreadyInitialized is returned by Init when a prior SDK is still alive
// with active requests. Call Shutdown first, or wait for requests to drain.
var ErrAlreadyInitialized = errors.New("waylog: SDK already initialized with active requests")

type sdk struct {
	cfg    Config
	out    io.Writer
	devOut io.Writer

	devEnabled bool
	delivery   *transporthttp.Client
	emitMu     sync.Mutex
	devMu      sync.Mutex
	rateMu     sync.Mutex
	rateSecond int64
	rateCount  int

	mu     sync.Mutex
	active map[*request]struct{}

	emitted          atomic.Int64
	suppressed       atomic.Int64
	stepsDropped     atomic.Int64
	logsDropped      atomic.Int64
	bytesDropped     atomic.Int64
	bufferOverflows  atomic.Int64
	reservedRejected atomic.Int64
	suppressFailed   atomic.Int64
	lateAfterEmit    atomic.Int64
	eventsDropped    atomic.Int64
}

var (
	stateMu sync.RWMutex
	state   *sdk
)

// Init configures the SDK. Returns ErrAlreadyInitialized if a prior SDK is
// still alive with active requests; in that case, call Shutdown first.
func Init(cfg Config) error {
	if cfg.Service == "" {
		return errors.New("waylog: Config.Service is required")
	}
	if cfg.Env == "" {
		return errors.New("waylog: Config.Env is required")
	}

	if cfg.MaxSteps <= 0 {
		cfg.MaxSteps = defaultMaxSteps
	}
	if cfg.MaxLogs <= 0 {
		cfg.MaxLogs = defaultMaxLogs
	}
	if cfg.MaxBufferBytes <= 0 {
		cfg.MaxBufferBytes = defaultMaxBufferBytes
	}

	localOut := cfg.Output
	if localOut == nil {
		localOut = os.Stderr
	}
	devEnabled := cfg.DevMode || strings.EqualFold(cfg.Env, "dev")

	s := &sdk{
		cfg:        cfg,
		out:        localOut,
		devOut:     os.Stderr,
		devEnabled: devEnabled,
		active:     make(map[*request]struct{}),
	}
	if cfg.IngestURL != "" {
		delivery, err := transporthttp.New(transporthttp.Config{
			IngestURL:   cfg.IngestURL,
			APIKey:      cfg.APIKey,
			Timeout:     5 * time.Second,
			BatchMode:   true,
			InFlightCap: cfg.MaxInFlightBytes,
		})
		if err != nil {
			return err
		}
		s.delivery = delivery
	}

	stateMu.Lock()
	defer stateMu.Unlock()
	if state != nil {
		state.mu.Lock()
		n := len(state.active)
		state.mu.Unlock()
		if n > 0 {
			return fmt.Errorf("%w: %d in flight", ErrAlreadyInitialized, n)
		}
	}
	state = s
	return nil
}

// Shutdown waits up to ctx's deadline for in-flight requests to finalize. It
// does not force-finalize requests; middleware (or test code) is responsible
// for ending requests it started.
func Shutdown(ctx context.Context) error {
	s := getState()
	if s == nil {
		return nil
	}

	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()
	for {
		s.mu.Lock()
		n := len(s.active)
		s.mu.Unlock()
		if n == 0 {
			if s.delivery != nil {
				s.delivery.Shutdown(remainingTimeout(ctx))
			}
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("waylog: shutdown timeout with %d active requests: %w", n, ctx.Err())
		case <-tick.C:
		}
	}
}

// Stats returns a snapshot of runtime counters.
func Stats() StatsSnapshot {
	s := getState()
	if s == nil {
		return StatsSnapshot{}
	}
	s.mu.Lock()
	active := int64(len(s.active))
	s.mu.Unlock()
	return StatsSnapshot{
		ActiveRequests:          active,
		EventsEmitted:           s.emitted.Load(),
		EventsSuppressed:        s.suppressed.Load(),
		StepsDropped:            s.stepsDropped.Load(),
		LogsDropped:             s.logsDropped.Load(),
		BytesDroppedFromBuffer:  s.bytesDropped.Load(),
		BufferOverflows:         s.bufferOverflows.Load(),
		ReservedCodeRejections:  s.reservedRejected.Load(),
		SuppressedThenFailed:    s.suppressFailed.Load(),
		LateCompletionAfterEmit: s.lateAfterEmit.Load(),
		EventsDropped:           s.eventsDropped.Load() + deliveryDropped(s),
		DeliveryFailures:        deliveryFailures(s),
	}
}

// MaxRequestAge returns the configured watchdog duration. Zero disables the
// HTTP middleware watchdog path.
func MaxRequestAge() time.Duration {
	s := getState()
	if s == nil {
		return 0
	}
	return s.cfg.MaxRequestAge
}

func getState() *sdk {
	stateMu.RLock()
	defer stateMu.RUnlock()
	return state
}

func remainingTimeout(ctx context.Context) time.Duration {
	if deadline, ok := ctx.Deadline(); ok {
		d := time.Until(deadline)
		if d < 0 {
			return 0
		}
		return d
	}
	return 0
}

func deliveryDropped(s *sdk) int64 {
	if s == nil || s.delivery == nil {
		return 0
	}
	return s.delivery.Dropped()
}

func deliveryFailures(s *sdk) int64 {
	if s == nil || s.delivery == nil {
		return 0
	}
	return s.delivery.Failures()
}

func resetForTest() {
	stateMu.Lock()
	state = nil
	stateMu.Unlock()
}
