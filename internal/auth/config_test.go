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

func TestWeakKeyWarnings(t *testing.T) {
	t.Run("flags placeholder keys across scopes in dev", func(t *testing.T) {
		cfg := AuthConfig{
			Profile:       ProfileDev,
			WriteKeys:     []string{"changeme-write"},
			ReadKeys:      []string{"demo"},
			AgentKeys:     []string{"a-real-strong-agent-key"},
			DashboardMode: "basic",
			DashboardPass: "changeme",
		}
		warns := cfg.WeakKeyWarnings()
		joined := strings.Join(warns, "\n")
		for _, want := range []string{"WAYLOG_WRITE_KEY", "WAYLOG_READ_KEY", "DASHBOARD_AUTH basic"} {
			if !strings.Contains(joined, want) {
				t.Fatalf("warnings %q missing mention of %q", joined, want)
			}
		}
		if strings.Contains(joined, "WAYLOG_AGENT_KEY") {
			t.Fatalf("strong agent key should not be flagged: %q", joined)
		}
	})

	t.Run("still warns in demo profile (make demo runs with a demo key)", func(t *testing.T) {
		cfg := AuthConfig{Profile: ProfileDemo, WriteKeys: []string{"demo"}, ReadKeys: []string{"demo"}}
		warns := cfg.WeakKeyWarnings()
		if len(warns) == 0 {
			t.Fatal("demo profile with demo keys should warn (plan G5a: start with demo key -> warning log)")
		}
		for _, w := range warns {
			if !strings.Contains(w, "local demo") {
				t.Fatalf("demo warning should read as non-fatal/expected for local demo, got %q", w)
			}
		}
	})

	t.Run("silent when keys are strong", func(t *testing.T) {
		cfg := AuthConfig{
			Profile:   ProfileProd,
			WriteKeys: []string{"K7f2-write-9aZ"},
			ReadKeys:  []string{"K7f2-read-9aZ"},
			AgentKeys: []string{"K7f2-agent-9aZ"},
		}
		if warns := cfg.WeakKeyWarnings(); len(warns) != 0 {
			t.Fatalf("strong keys should not warn, got %v", warns)
		}
	})

	t.Run("names WAYLOG_API_KEY when the weak write key came from the legacy var", func(t *testing.T) {
		cfg, err := ParseConfig(map[string]string{"WAYLOG_API_KEY": "demo"})
		if err != nil {
			t.Fatalf("ParseConfig: %v", err)
		}
		joined := strings.Join(cfg.WeakKeyWarnings(), "\n")
		if !strings.Contains(joined, "WAYLOG_API_KEY") {
			t.Fatalf("legacy weak write key should name WAYLOG_API_KEY, got %q", joined)
		}
		if strings.Contains(joined, "WAYLOG_WRITE_KEY") {
			t.Fatalf("must not name WAYLOG_WRITE_KEY when the source was the legacy var, got %q", joined)
		}
	})
}
