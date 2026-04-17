package detect

import (
	"time"

	"github.com/sssmaran/WaylogCLI/internal/config"
)

type Config struct {
	Enabled        bool
	Interval       time.Duration
	CurrentWindow  time.Duration
	BaselineWindow time.Duration
	MinLift        float64
	MinCount       int
}

func ParseConfig() Config {
	return Config{
		Enabled:        config.GetenvBool("DETECT_ENABLED", true),
		Interval:       config.GetenvDuration("DETECT_INTERVAL", 10*time.Second),
		CurrentWindow:  config.GetenvDuration("DETECT_CURRENT_WINDOW", 1*time.Minute),
		BaselineWindow: config.GetenvDuration("DETECT_BASELINE_WINDOW", 5*time.Minute),
		MinLift:        config.GetenvFloat("DETECT_MIN_LIFT", 3.0),
		MinCount:       config.GetenvInt("DETECT_MIN_COUNT", 3),
	}
}
