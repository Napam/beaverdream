package webmaster

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Napam/beaverdream/lib/mockllm"
	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
)

func testMutator(t *testing.T) (*Mutator, func()) {
	t.Helper()
	srv := httptest.NewServer(mockllm.Handler())
	client := openai.NewClient(
		option.WithBaseURL(srv.URL),
		option.WithAPIKey("test"),
	)
	return New(client, "mock-model", 3, ""), srv.Close
}

func TestMutateAppliesMockTheme(t *testing.T) {
	m, closeServer := testMutator(t)
	defer closeServer()

	res, err := m.Mutate(
		context.Background(),
		Site{Board: PostsPlaceholder},
		nil,
		Post{Author: "dave", Body: "make it look like the matrix"},
	)
	if err != nil {
		t.Fatalf("Mutate: %v", err)
	}
	if !res.Changed {
		t.Fatal("expected a change")
	}
	if !strings.Contains(res.Site.CSS, "00ff6a") {
		t.Errorf("css does not carry the matrix palette: %q", res.Site.CSS)
	}
	if !strings.Contains(res.Site.Board, PostsPlaceholder) {
		t.Errorf("board lost the posts placeholder: %q", res.Site.Board)
	}
	if res.Site.Note == "" {
		t.Error("note was not set")
	}
	if res.Reason == "" {
		t.Error("done reason was not recorded")
	}

	// system, user, assistant(edits), assistant(done)
	if len(res.Messages) != 4 {
		t.Fatalf("messages = %d, want 4", len(res.Messages))
	}
	if res.Messages[0].Role != "system" || res.Messages[1].Role != "user" {
		t.Fatalf("unexpected prefix: %q, %q", res.Messages[0].Role, res.Messages[1].Role)
	}
	if len(res.Messages[2].ToolCalls) != 3 {
		t.Fatalf("first round called %d tools, want 3", len(res.Messages[2].ToolCalls))
	}
	for _, call := range res.Messages[2].ToolCalls {
		if call.Result != "ok" {
			t.Errorf("tool %s result = %q, want ok", call.Name, call.Result)
		}
	}
}

func TestMutateNoopKeepsSite(t *testing.T) {
	m, closeServer := testMutator(t)
	defer closeServer()

	original := Site{CSS: "body{color:red}", Board: PostsPlaceholder}
	res, err := m.Mutate(
		context.Background(),
		original,
		nil,
		Post{Author: "dave", Body: "mocknoop please"},
	)
	if err != nil {
		t.Fatalf("Mutate: %v", err)
	}
	if res.Changed {
		t.Fatal("expected no change")
	}
	if res.Site.CSS != original.CSS {
		t.Fatalf("site changed: %q", res.Site.CSS)
	}
	if res.Reason == "" {
		t.Fatal("expected a reason for doing nothing")
	}
}

func TestMutateReportsModelFailure(t *testing.T) {
	m, closeServer := testMutator(t)
	defer closeServer()

	if _, err := m.Mutate(
		context.Background(),
		Site{},
		nil,
		Post{Body: "mockfail"},
	); err == nil {
		t.Fatal("expected an error")
	}
}
