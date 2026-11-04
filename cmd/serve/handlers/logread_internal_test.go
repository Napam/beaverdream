package handlers

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"
)

// A closed tab cancels its reads. That is the page working as intended, so it
// must not reach the error log; a deadline or a real query failure still does.
func TestLogReadIgnoresAClientThatLeft(t *testing.T) {
	var buf bytes.Buffer
	h := &Handlers{logger: slog.New(slog.NewTextHandler(&buf, nil))}

	h.logRead("Failed to read journal state", fmt.Errorf("query: %w", context.Canceled))
	if buf.Len() != 0 {
		t.Fatalf("logged a canceled read: %q", buf.String())
	}

	h.logRead("Failed to read journal state", context.DeadlineExceeded)
	if !strings.Contains(buf.String(), "deadline exceeded") {
		t.Fatalf("dropped a deadline: %q", buf.String())
	}

	buf.Reset()
	h.logRead("Failed to read journal state", errors.New("disk has feelings"))
	if !strings.Contains(buf.String(), "disk has feelings") {
		t.Fatalf("dropped a real failure: %q", buf.String())
	}
}
