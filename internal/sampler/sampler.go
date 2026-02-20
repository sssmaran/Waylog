package sampler

import (
	"hash/fnv"
	"strings"

	"github.com/sssmaran/WaylogCLI/internal/config"
	"github.com/sssmaran/WaylogCLI/pkg/event"
)

type Config struct {
	// If latency >= SlowMs, always keep
	SlowMs int

	// Keep rate for "happy/fast/non-VIP" traffic: 1-5 (%) typically
	HappySampleRatePct int

	// Used to make sampling deterministic across identical events
	Salt string
}

type Sampler struct {
	cfg Config
}

func New(cfg Config) *Sampler {
	// log.Printf(
	// 	"SAMPLER CONFIG: slow_ms=%d happy_pct=%d",
	// 	cfg.SlowMs,
	// 	cfg.HappySampleRatePct,
	// )
	// sane defaults
	if cfg.SlowMs <= 0 {
		cfg.SlowMs = 400 // default "slow" threshold
	}
	if cfg.HappySampleRatePct <= 0 {
		cfg.HappySampleRatePct = 2 // default 2%
	}
	if cfg.HappySampleRatePct > 100 {
		cfg.HappySampleRatePct = 100
	}
	if strings.TrimSpace(cfg.Salt) == "" {
		cfg.Salt = "waylog"
	}
	return &Sampler{cfg: cfg}
}

// HappySampleRatePct returns the configured happy-path sample rate percentage.
func (s *Sampler) HappySampleRatePct() int { return s.cfg.HappySampleRatePct }

func (s *Sampler) ShouldKeep(ev event.WideEvent) bool {
	// 1) Always keep errors
	if !ev.Outcome.Success {
		return true
	}

	// 2) Always keep slow requests
	if ev.Metrics.LatencyMs >= int64(s.cfg.SlowMs) {
		return true
	}

	// 3) Always keep VIP users
	if ev.User.VIP {
		return true
	}

	// 4) Sample the rest (deterministic hash-based sampling)
	return s.keepByHash(ev) // stable across runs for same inputs
}

func (s *Sampler) keepByHash(ev event.WideEvent) bool {
	h := fnv.New32a()
	// Hash on trace_id so all spans in a request are kept or dropped together.
	_, _ = h.Write([]byte(s.cfg.Salt))
	_, _ = h.Write([]byte("|"))
	_, _ = h.Write([]byte(ev.Request.TraceID))

	// Convert hash to 0..99
	bucket := int(h.Sum32() % 100)
	return bucket < s.cfg.HappySampleRatePct
}

// LoadConfigFromEnv loads sampler configuration from environment variables.
func LoadConfigFromEnv() Config {
	return Config{
		SlowMs:             config.GetenvInt("SLOW_MS", 400),
		HappySampleRatePct: config.GetenvInt("HAPPY_SAMPLE_RATE_PCT", 2),
		Salt:               config.Getenv("SAMPLE_SALT", "waylog"),
	}
}
