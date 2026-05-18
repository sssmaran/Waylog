package auth

import (
	"strings"
	"testing"
)

func TestParseConfig_Defaults(t *testing.T) {
	cfg, err := ParseConfig(map[string]string{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cfg.WriteKeys) != 0 || len(cfg.ReadKeys) != 0 || len(cfg.AgentKeys) != 0 {
		t.Fatal("expected all keys empty by default")
	}
	if cfg.DashboardMode != "off" {
		t.Fatalf("dashboard mode = %q, want off", cfg.DashboardMode)
	}
}

func TestParseConfig_WriteKeyFromLegacy(t *testing.T) {
	cfg, err := ParseConfig(map[string]string{"WAYLOG_API_KEY": "legacy"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cfg.WriteKeys) != 1 || cfg.WriteKeys[0] != "legacy" {
		t.Fatalf("write keys = %v, want [legacy]", cfg.WriteKeys)
	}
}

func TestParseConfig_ConflictingWriteKeys(t *testing.T) {
	_, err := ParseConfig(map[string]string{
		"WAYLOG_API_KEY":   "old",
		"WAYLOG_WRITE_KEY": "new",
	})
	if err == nil || !strings.Contains(err.Error(), "conflict") {
		t.Fatalf("expected conflict error, got %v", err)
	}
}

func TestParseConfig_CommaKeys(t *testing.T) {
	cfg, err := ParseConfig(map[string]string{"WAYLOG_WRITE_KEY": "key1,,key2,"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cfg.WriteKeys) != 2 || cfg.WriteKeys[0] != "key1" || cfg.WriteKeys[1] != "key2" {
		t.Fatalf("write keys = %v, want [key1 key2]", cfg.WriteKeys)
	}
}

func TestParseConfig_DashboardBasic(t *testing.T) {
	cfg, err := ParseConfig(map[string]string{
		"DASHBOARD_AUTH":           "basic:admin:pass123",
		"DASHBOARD_SESSION_SECRET": "somesecret",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.DashboardMode != "basic" {
		t.Fatalf("mode = %q, want basic", cfg.DashboardMode)
	}
	if cfg.DashboardUser != "admin" || cfg.DashboardPass != "pass123" {
		t.Fatalf("creds = %q/%q, want admin/pass123", cfg.DashboardUser, cfg.DashboardPass)
	}
}

func TestParseConfig_DashboardKey(t *testing.T) {
	cfg, err := ParseConfig(map[string]string{
		"DASHBOARD_AUTH":           "key:mysecretkey",
		"DASHBOARD_SESSION_SECRET": "somesecret",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.DashboardMode != "key" {
		t.Fatalf("mode = %q, want key", cfg.DashboardMode)
	}
	if cfg.DashboardKey != "mysecretkey" {
		t.Fatalf("key = %q, want mysecretkey", cfg.DashboardKey)
	}
}

func TestParseConfig_DashboardMalformed(t *testing.T) {
	_, err := ParseConfig(map[string]string{"DASHBOARD_AUTH": "invalid"})
	if err == nil || !strings.Contains(err.Error(), "DASHBOARD_AUTH") {
		t.Fatalf("expected DASHBOARD_AUTH error, got %v", err)
	}
}

func TestParseConfig_ReadKeyWithoutDashboardAuth(t *testing.T) {
	_, err := ParseConfig(map[string]string{"WAYLOG_READ_KEY": "rk"})
	if err == nil || !strings.Contains(err.Error(), "DASHBOARD_AUTH") {
		t.Fatalf("expected error about DASHBOARD_AUTH needed, got %v", err)
	}
}

func TestParseConfig_DashboardAuthWithoutSessionSecret_Prod(t *testing.T) {
	_, err := ParseConfig(map[string]string{
		"DASHBOARD_AUTH": "basic:admin:pass",
		"WAYLOG_PROFILE": "prod",
	})
	if err == nil || !strings.Contains(err.Error(), "DASHBOARD_SESSION_SECRET") {
		t.Fatalf("expected DASHBOARD_SESSION_SECRET error, got %v", err)
	}
}

func TestParseConfig_DashboardAuthWithoutSessionSecret_Dev(t *testing.T) {
	cfg, err := ParseConfig(map[string]string{
		"DASHBOARD_AUTH": "basic:admin:pass",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cfg.SessionSecret) == 0 {
		t.Fatal("expected derived session secret in dev mode")
	}
}

func TestParseConfig_ProfileDefaultsToDev(t *testing.T) {
	cfg, err := ParseConfig(map[string]string{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Profile != ProfileDev {
		t.Fatalf("profile = %q, want %q", cfg.Profile, ProfileDev)
	}
}

func TestParseConfig_ProfileRejectsUnknown(t *testing.T) {
	_, err := ParseConfig(map[string]string{"WAYLOG_PROFILE": "staging"})
	if err == nil || !strings.Contains(err.Error(), "WAYLOG_PROFILE") {
		t.Fatalf("expected WAYLOG_PROFILE validation error, got %v", err)
	}
}

func TestParseConfig_ProfileProdRequiresAllKeys(t *testing.T) {
	_, err := ParseConfig(map[string]string{"WAYLOG_PROFILE": "prod"})
	if err == nil {
		t.Fatal("expected error for prod profile with no keys")
	}
	for _, want := range []string{"WAYLOG_WRITE_KEY", "WAYLOG_READ_KEY", "WAYLOG_AGENT_KEY", "refusing"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q missing %q", err.Error(), want)
		}
	}
}

func TestParseConfig_ProfileProdBootsWithAllKeys(t *testing.T) {
	cfg, err := ParseConfig(map[string]string{
		"WAYLOG_PROFILE":           "prod",
		"WAYLOG_WRITE_KEY":         "w",
		"WAYLOG_READ_KEY":          "r",
		"WAYLOG_AGENT_KEY":         "a",
		"DASHBOARD_AUTH":           "key:dash",
		"DASHBOARD_SESSION_SECRET": "secret",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Profile != ProfileProd {
		t.Fatalf("profile = %q, want %q", cfg.Profile, ProfileProd)
	}
}

func TestParseConfig_ProfileDemoAllowsOpen(t *testing.T) {
	cfg, err := ParseConfig(map[string]string{"WAYLOG_PROFILE": "demo"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Profile != ProfileDemo {
		t.Fatalf("profile = %q, want %q", cfg.Profile, ProfileDemo)
	}
}
