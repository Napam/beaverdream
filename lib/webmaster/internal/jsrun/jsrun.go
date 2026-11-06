// Package jsrun runs JavaScript from Go tests in node or bun.
package jsrun

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Run runs harness against the script block inside src, passing the script
// path and scenario as arguments, and returns what it printed. It uses node
// or bun, whichever is installed, and skips the test when neither is.
func Run(t *testing.T, src, harness, scenario string) []byte {
	t.Helper()
	bin, err := runtime()
	if err != nil {
		t.Skip("neither node nor bun is installed")
	}

	start := strings.Index(src, "<script>")
	end := strings.LastIndex(src, "</script>")
	if start < 0 || end < start {
		t.Fatal("the script has no script block")
	}
	script := src[start+len("<script>") : end]

	dir := t.TempDir()
	scriptPath := filepath.Join(dir, "script.js")
	harnessPath := filepath.Join(dir, "harness.js")
	if err := os.WriteFile(scriptPath, []byte(script), 0o600); err != nil {
		t.Fatalf("write script: %v", err)
	}
	if err := os.WriteFile(harnessPath, []byte(harness), 0o600); err != nil {
		t.Fatalf("write harness: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin, harnessPath, scriptPath, scenario).
		CombinedOutput()
	if err != nil {
		t.Fatalf("%s: %v\n%s", filepath.Base(bin), err, out)
	}
	return out
}

func runtime() (string, error) {
	var lastErr error
	for _, name := range []string{"node", "bun"} {
		path, err := exec.LookPath(name)
		if err == nil {
			return path, nil
		}
		lastErr = err
	}
	return "", lastErr
}
