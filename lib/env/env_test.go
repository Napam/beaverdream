package env

import (
	"testing"
	"time"
)

func TestGetEnvInt(t *testing.T) {
	t.Setenv("BD_TEST_INT", "")
	if got := GetEnvInt("BD_TEST_INT", 7); got != 7 {
		t.Fatalf("empty = %d, want 7", got)
	}
	t.Setenv("BD_TEST_INT", "12")
	if got := GetEnvInt("BD_TEST_INT", 7); got != 12 {
		t.Fatalf("set = %d, want 12", got)
	}
	t.Setenv("BD_TEST_INT", "nope")
	if got := GetEnvInt("BD_TEST_INT", 7); got != 7 {
		t.Fatalf("invalid = %d, want 7", got)
	}
}

func TestLoadDefaults(t *testing.T) {
	clearConfig(t)
	got := Load()
	if got.Host != "localhost" || got.AppPort != "6930" || got.PlayPort != "6932" {
		t.Fatalf("bind = %s %s %s, want localhost 6930 6932", got.Host, got.AppPort, got.PlayPort)
	}
	if got.RegistryPath != "./data/registry.db" {
		t.Errorf("registry = %q, want ./data/registry.db", got.RegistryPath)
	}
	if got.WorldsDir != "./data/worlds" {
		t.Errorf("worlds = %q, want ./data/worlds", got.WorldsDir)
	}
	if got.AppSessionTTL != 168*time.Hour || got.PlaySessionTTL != 0 {
		t.Errorf("session ttls = %v %v, want 168h 0", got.AppSessionTTL, got.PlaySessionTTL)
	}
	if got.HandoffTTL != time.Minute || got.WorldIdleTimeout != 15*time.Minute {
		t.Errorf("handoff/idle = %v %v, want 1m 15m", got.HandoffTTL, got.WorldIdleTimeout)
	}
	if got.DailyTokensIdentity != 1_000_000 || got.DailyTokensGlobal != 5_000_000 {
		t.Errorf(
			"budgets = %d %d, want 1000000 5000000",
			got.DailyTokensIdentity,
			got.DailyTokensGlobal,
		)
	}
	if got.ModelConcurrency != 1 {
		t.Errorf("concurrency = %d, want 1", got.ModelConcurrency)
	}
}

func TestLoadOverrides(t *testing.T) {
	clearConfig(t)
	t.Setenv("HOST", "0.0.0.0")
	t.Setenv("APP_PORT", "7000")
	t.Setenv("PLAY_PORT", "7001")
	t.Setenv("APP_URL", "https://app.example.com/")
	t.Setenv("PLAY_URL", "https://play.example.com/")
	t.Setenv("REGISTRY_PATH", "/tmp/registry.db")
	t.Setenv("WORLDS_DIR", "/tmp/worlds")
	t.Setenv("APP_SESSION_TTL", "1h")
	t.Setenv("PLAY_SESSION_TTL", "0")
	t.Setenv("HANDOFF_TTL", "30s")
	t.Setenv("DAILY_TOKENS_IDENTITY", "0")
	t.Setenv("DAILY_TOKENS_GLOBAL", "123")
	t.Setenv("MODEL_CONCURRENCY", "3")
	t.Setenv("WORLD_IDLE_TIMEOUT", "0")

	got := Load()
	if got.Host != "0.0.0.0" || got.AppPort != "7000" || got.PlayPort != "7001" {
		t.Errorf("bind = %s %s %s", got.Host, got.AppPort, got.PlayPort)
	}
	if got.AppURL != "https://app.example.com" || got.PlayURL != "https://play.example.com" {
		t.Errorf("urls = %q %q", got.AppURL, got.PlayURL)
	}
	if got.RegistryPath != "/tmp/registry.db" || got.WorldsDir != "/tmp/worlds" {
		t.Errorf("paths = %q %q", got.RegistryPath, got.WorldsDir)
	}
	if got.AppSessionTTL != time.Hour || got.PlaySessionTTL != 0 ||
		got.HandoffTTL != 30*time.Second {
		t.Errorf("ttls = %v %v %v", got.AppSessionTTL, got.PlaySessionTTL, got.HandoffTTL)
	}
	if got.DailyTokensIdentity != 0 || got.DailyTokensGlobal != 123 {
		t.Errorf("budgets = %d %d", got.DailyTokensIdentity, got.DailyTokensGlobal)
	}
	if got.ModelConcurrency != 3 || got.WorldIdleTimeout != 0 {
		t.Errorf("concurrency/idle = %d %v", got.ModelConcurrency, got.WorldIdleTimeout)
	}
}

// clearConfig removes every setting Load reads from the ambient shell.
func clearConfig(t *testing.T) {
	t.Helper()
	for _, name := range []string{
		"HOST", "APP_PORT", "PLAY_PORT", "APP_URL", "PLAY_URL",
		"TLS_CERT_FILE", "TLS_KEY_FILE", "REGISTRY_PATH", "WORLDS_DIR",
		"ADMIN_PASSWORD", "APP_SESSION_TTL", "PLAY_SESSION_TTL", "HANDOFF_TTL",
		"DAILY_TOKENS_IDENTITY", "DAILY_TOKENS_GLOBAL", "MODEL_CONCURRENCY",
		"WORLD_IDLE_TIMEOUT", "OPENAI_MODEL", "OPENAI_BASE_URL", "OPENAI_API_KEY",
		"OPENCODE_SESSION_ID", "OPENAI_REASONING_EFFORT", "TURN_MAX_ROUNDS",
		"LIVE_RELOAD_PROXY_HOST", "LIVE_RELOAD_PROXY_PORT",
	} {
		t.Setenv(name, "")
	}
}

func TestGetEnvNonNegativeInt64(t *testing.T) {
	t.Setenv("BD_TEST_INT64", "0")
	if got := GetEnvNonNegativeInt64("BD_TEST_INT64", 5); got != 0 {
		t.Fatalf("zero = %d, want 0", got)
	}
	t.Setenv("BD_TEST_INT64", "9000000000")
	if got := GetEnvNonNegativeInt64("BD_TEST_INT64", 5); got != 9_000_000_000 {
		t.Fatalf("set = %d, want 9000000000", got)
	}
	for _, value := range []string{"-1", "nope", ""} {
		t.Setenv("BD_TEST_INT64", value)
		if got := GetEnvNonNegativeInt64("BD_TEST_INT64", 5); got != 5 {
			t.Errorf("%q = %d, want fallback 5", value, got)
		}
	}
}

func TestGetEnvNonNegativeDuration(t *testing.T) {
	t.Setenv("BD_TEST_NONNEG", "0")
	if got := GetEnvNonNegativeDuration("BD_TEST_NONNEG", time.Second); got != 0 {
		t.Fatalf("zero = %v, want 0", got)
	}
	for _, value := range []string{"-1s", "nope", ""} {
		t.Setenv("BD_TEST_NONNEG", value)
		if got := GetEnvNonNegativeDuration("BD_TEST_NONNEG", time.Second); got != time.Second {
			t.Errorf("%q = %v, want fallback", value, got)
		}
	}
}

func TestGetEnvPositiveInt(t *testing.T) {
	t.Setenv("BD_TEST_POSITIVE", "12")
	if got := GetEnvPositiveInt("BD_TEST_POSITIVE", 7); got != 12 {
		t.Fatalf("set = %d, want 12", got)
	}
	for _, value := range []string{"0", "-3", "nope", ""} {
		t.Setenv("BD_TEST_POSITIVE", value)
		if got := GetEnvPositiveInt("BD_TEST_POSITIVE", 7); got != 7 {
			t.Errorf("%q = %d, want fallback 7", value, got)
		}
	}
}

func TestGetEnvDuration(t *testing.T) {
	t.Setenv("BD_TEST_DURATION", "1h30m")
	if got := GetEnvDuration("BD_TEST_DURATION", time.Second); got != 90*time.Minute {
		t.Fatalf("set = %v, want 1h30m", got)
	}
	t.Setenv("BD_TEST_DURATION", "0")
	if got := GetEnvDuration("BD_TEST_DURATION", time.Second); got != 0 {
		t.Fatalf("zero = %v, want 0", got)
	}
	t.Setenv("BD_TEST_DURATION", "nope")
	if got := GetEnvDuration("BD_TEST_DURATION", time.Second); got != time.Second {
		t.Fatalf("invalid = %v, want fallback", got)
	}
}

func TestGetEnvPositiveDuration(t *testing.T) {
	t.Setenv("BD_TEST_POSITIVE_DURATION", "250ms")
	if got := GetEnvPositiveDuration(
		"BD_TEST_POSITIVE_DURATION",
		time.Second,
	); got != 250*time.Millisecond {
		t.Fatalf("set = %v, want 250ms", got)
	}
	for _, value := range []string{"0", "-1s", "nope", ""} {
		t.Setenv("BD_TEST_POSITIVE_DURATION", value)
		if got := GetEnvPositiveDuration(
			"BD_TEST_POSITIVE_DURATION",
			time.Second,
		); got != time.Second {
			t.Errorf("%q = %v, want fallback", value, got)
		}
	}
}
