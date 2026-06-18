package auth

import (
	"fmt"
	"strings"
)

// Profile values control auth defaults and validation strictness.
const (
	ProfileDemo = "demo"
	ProfileDev  = "dev"
	ProfileProd = "prod"
)

type AuthConfig struct {
	WriteKeys []string
	ReadKeys  []string
	AgentKeys []string

	// WriteKeyEnv records which env var populated WriteKeys ("WAYLOG_WRITE_KEY"
	// or the legacy "WAYLOG_API_KEY") so a weak-key warning names the variable the
	// operator actually set. Empty when no write key is configured.
	WriteKeyEnv string

	Profile string // "demo", "dev", or "prod". Defaults to "dev" when unset.

	DashboardMode string // "off", "basic", "key"
	DashboardUser string // for basic mode
	DashboardPass string // for basic mode
	DashboardKey  string // for key mode
	SessionSecret []byte
}

func ParseConfig(env map[string]string) (AuthConfig, error) {
	var cfg AuthConfig

	profile := strings.ToLower(strings.TrimSpace(env["WAYLOG_PROFILE"]))
	switch profile {
	case "":
		cfg.Profile = ProfileDev
	case ProfileDemo, ProfileDev, ProfileProd:
		cfg.Profile = profile
	default:
		return cfg, fmt.Errorf("WAYLOG_PROFILE: must be one of demo, dev, prod; got %q", profile)
	}

	legacyKey := strings.TrimSpace(env["WAYLOG_API_KEY"])
	writeKey := strings.TrimSpace(env["WAYLOG_WRITE_KEY"])

	if legacyKey != "" && writeKey != "" {
		return cfg, fmt.Errorf("config conflict: both WAYLOG_API_KEY and WAYLOG_WRITE_KEY are set; use only WAYLOG_WRITE_KEY")
	}

	if writeKey != "" {
		cfg.WriteKeys = splitKeys(writeKey)
		cfg.WriteKeyEnv = "WAYLOG_WRITE_KEY"
	} else if legacyKey != "" {
		cfg.WriteKeys = []string{legacyKey}
		cfg.WriteKeyEnv = "WAYLOG_API_KEY"
	}

	cfg.ReadKeys = splitKeys(env["WAYLOG_READ_KEY"])
	cfg.AgentKeys = splitKeys(env["WAYLOG_AGENT_KEY"])

	dashAuth := strings.TrimSpace(env["DASHBOARD_AUTH"])
	if dashAuth == "" || dashAuth == "off" {
		cfg.DashboardMode = "off"
	} else if strings.HasPrefix(dashAuth, "basic:") {
		parts := strings.SplitN(dashAuth, ":", 3)
		if len(parts) != 3 || parts[1] == "" || parts[2] == "" {
			return cfg, fmt.Errorf("DASHBOARD_AUTH: basic mode requires format basic:<user>:<pass>")
		}
		cfg.DashboardMode = "basic"
		cfg.DashboardUser = parts[1]
		cfg.DashboardPass = parts[2]
	} else if strings.HasPrefix(dashAuth, "key:") {
		key := strings.TrimPrefix(dashAuth, "key:")
		if key == "" {
			return cfg, fmt.Errorf("DASHBOARD_AUTH: key mode requires format key:<secret>")
		}
		cfg.DashboardMode = "key"
		cfg.DashboardKey = key
	} else {
		return cfg, fmt.Errorf("DASHBOARD_AUTH: must be off, basic:<user>:<pass>, or key:<secret>; got %q", dashAuth)
	}

	sessionSecret := strings.TrimSpace(env["DASHBOARD_SESSION_SECRET"])
	if cfg.DashboardMode != "off" {
		if sessionSecret == "" && cfg.Profile == ProfileProd {
			return cfg, fmt.Errorf("DASHBOARD_SESSION_SECRET is required when DASHBOARD_AUTH is enabled in prod profile")
		}
		if sessionSecret != "" {
			cfg.SessionSecret = []byte(sessionSecret)
		} else {
			cfg.SessionSecret = DeriveSecret(dashAuth)
		}
	}

	if len(cfg.ReadKeys) > 0 && cfg.DashboardMode == "off" {
		return cfg, fmt.Errorf("WAYLOG_READ_KEY is set but DASHBOARD_AUTH is off; the dashboard cannot authenticate against read APIs without a session")
	}

	if cfg.Profile == ProfileProd {
		var missing []string
		if len(cfg.WriteKeys) == 0 {
			missing = append(missing, "WAYLOG_WRITE_KEY")
		}
		if len(cfg.ReadKeys) == 0 {
			missing = append(missing, "WAYLOG_READ_KEY")
		}
		if len(cfg.AgentKeys) == 0 {
			missing = append(missing, "WAYLOG_AGENT_KEY")
		}
		if len(missing) > 0 {
			return cfg, fmt.Errorf("WAYLOG_PROFILE=prod requires non-empty %s — refusing to boot with an open auth surface", strings.Join(missing, ", "))
		}
	}

	return cfg, nil
}

// weakKeyValues are common placeholder secrets that must never guard a real
// deployment. Matched case-insensitively; the deploy/prod.env "changeme-*"
// presets are caught by the prefix check in isWeakKey.
var weakKeyValues = map[string]bool{
	"demo": true, "changeme": true, "change-me": true, "password": true,
	"secret": true, "test": true, "example": true, "key": true, "token": true,
}

func isWeakKey(k string) bool {
	k = strings.ToLower(strings.TrimSpace(k))
	if k == "" {
		return false
	}
	return weakKeyValues[k] || strings.HasPrefix(k, "changeme") || strings.HasPrefix(k, "change-me")
}

// weakKeySuffix is appended to every weak-key warning. It is non-fatal and fires
// in all profiles — including demo — because a placeholder key is only ever safe
// on a local, unexposed server. The demo deliberately runs with a "demo" key, so
// `make demo` surfaces this warning by design; it is the same nudge an operator
// needs if they promote that config toward a real deployment.
const weakKeySuffix = " is a weak/placeholder value — fine for a local demo, but never expose this server with it"

// WeakKeyWarnings returns one human-readable warning per auth scope guarded by a
// placeholder/demo secret. Callers should log these at startup so an operator who
// ships with a default key is told before it becomes an incident.
func (c AuthConfig) WeakKeyWarnings() []string {
	var warns []string
	check := func(envName string, keys []string) {
		for _, k := range keys {
			if isWeakKey(k) {
				warns = append(warns, envName+weakKeySuffix)
				return
			}
		}
	}
	writeEnv := c.WriteKeyEnv
	if writeEnv == "" {
		writeEnv = "WAYLOG_WRITE_KEY"
	}
	check(writeEnv, c.WriteKeys)
	check("WAYLOG_READ_KEY", c.ReadKeys)
	check("WAYLOG_AGENT_KEY", c.AgentKeys)
	if c.DashboardMode == "basic" && isWeakKey(c.DashboardPass) {
		warns = append(warns, "DASHBOARD_AUTH basic password"+weakKeySuffix)
	}
	if c.DashboardMode == "key" && isWeakKey(c.DashboardKey) {
		warns = append(warns, "DASHBOARD_AUTH key"+weakKeySuffix)
	}
	return warns
}

func splitKeys(s string) []string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	keys := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			keys = append(keys, p)
		}
	}
	return keys
}
