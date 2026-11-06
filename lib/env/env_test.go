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
