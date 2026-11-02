package env

import (
	"os"
	"path/filepath"
	"testing"
)

// unsetEnv removes key for the duration of the test, then restores whatever
// was there before. t.Setenv cannot be used because it marks the key as set,
// which would stop LoadDotEnv from applying the file value.
func unsetEnv(t *testing.T, key string) {
	t.Helper()
	old, had := os.LookupEnv(key)
	if err := os.Unsetenv(key); err != nil {
		t.Fatalf("unset %s: %v", key, err)
	}
	t.Cleanup(func() {
		if had {
			_ = os.Setenv(key, old)
			return
		}
		_ = os.Unsetenv(key)
	})
}

func writeDotEnv(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write .env: %v", err)
	}
	return path
}

func TestLoadDotEnvAppliesValues(t *testing.T) {
	for _, key := range []string{
		"BEAVER_TEST_FRESH",
		"BEAVER_TEST_EXPORT",
		"BEAVER_TEST_DOUBLE",
		"BEAVER_TEST_SINGLE",
		"BEAVER_TEST_EMPTY",
		"BEAVER_TEST_CRLF",
	} {
		unsetEnv(t, key)
	}

	path := writeDotEnv(t, ""+
		"# a comment\n"+
		"\n"+
		"BEAVER_TEST_FRESH=fresh\n"+
		"export BEAVER_TEST_EXPORT=exported\n"+
		"BEAVER_TEST_DOUBLE=\"two words\"\n"+
		"BEAVER_TEST_SINGLE='three words'\n"+
		"BEAVER_TEST_EMPTY=\n"+
		"this line has no equals sign\n"+
		"BEAVER_TEST_CRLF=carriage\r\n")

	if err := LoadDotEnv(path); err != nil {
		t.Fatalf("load .env: %v", err)
	}

	want := map[string]string{
		"BEAVER_TEST_FRESH":  "fresh",
		"BEAVER_TEST_EXPORT": "exported",
		"BEAVER_TEST_DOUBLE": "two words",
		"BEAVER_TEST_SINGLE": "three words",
		"BEAVER_TEST_EMPTY":  "",
		"BEAVER_TEST_CRLF":   "carriage",
	}
	for key, value := range want {
		got, ok := os.LookupEnv(key)
		if !ok {
			t.Errorf("%s was not set", key)
			continue
		}
		if got != value {
			t.Errorf("%s = %q, want %q", key, got, value)
		}
	}
}

func TestLoadDotEnvKeepsExistingValues(t *testing.T) {
	t.Setenv("BEAVER_TEST_EXISTING", "from-shell")

	path := writeDotEnv(t, "BEAVER_TEST_EXISTING=from-file\n")

	if err := LoadDotEnv(path); err != nil {
		t.Fatalf("load .env: %v", err)
	}
	if got := os.Getenv("BEAVER_TEST_EXISTING"); got != "from-shell" {
		t.Fatalf("existing value = %q, want from-shell", got)
	}
}

func TestLoadDotEnvMissingFileIsFine(t *testing.T) {
	if err := LoadDotEnv(filepath.Join(t.TempDir(), "absent.env")); err != nil {
		t.Fatalf("missing file returned an error: %v", err)
	}
}
