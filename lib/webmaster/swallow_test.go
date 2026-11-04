package webmaster

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Napam/beaverdream/lib/mockllm"
	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
)

// scriptedMutator answers each completion with the next list of tool calls,
// given as name and JSON arguments pairs.
func scriptedMutator(t *testing.T, script ...[][2]string) (*Mutator, *int) {
	t.Helper()
	calls := 0
	srv := httptest.NewServer(mockllm.Streamed(func(w http.ResponseWriter, _ *http.Request) {
		if calls >= len(script) {
			http.Error(w, "script ran out", http.StatusBadRequest)
			return
		}
		var toolCalls []map[string]any
		for i, call := range script[calls] {
			toolCalls = append(toolCalls, map[string]any{
				"id":       fmt.Sprintf("c%d-%d", calls, i),
				"type":     "function",
				"function": map[string]string{"name": call[0], "arguments": call[1]},
			})
		}
		calls++
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "x", "object": "chat.completion", "created": 0, "model": "m",
			"choices": []any{
				map[string]any{
					"index":         0,
					"finish_reason": "tool_calls",
					"message": map[string]any{
						"role":       "assistant",
						"content":    "",
						"tool_calls": toolCalls,
					},
				},
			},
			"usage": map[string]int{"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2},
		})
	}))
	t.Cleanup(srv.Close)
	client := openai.NewClient(option.WithBaseURL(srv.URL), option.WithAPIKey("test"))
	return New(client, "m", 3, ""), &calls
}

// An edit that leaves a comment open hides the posts. The model hears why
// once, and calling done again keeps the page: hiding posts can be the joke.
func TestMutatePausesOnceWhenAnEditHidesThePosts(t *testing.T) {
	m, calls := scriptedMutator(t,
		[][2]string{
			{"edit", `{"target":"html","old":"<main>","new":"<!-- oops<main>"}`},
			{"done", `{"reason":"a comment"}`},
		},
		[][2]string{{"done", `{"reason":"meant it"}`}},
	)
	res, err := m.Mutate(
		context.Background(),
		Site{HTML: "<h1>beaverdream</h1><main>" + PostsPlaceholder + "</main>"},
		Room{},
		Post{Author: "dave", Body: "hide everything"},
	)
	if err != nil {
		t.Fatalf("Mutate: %v", err)
	}
	if *calls != 2 || !res.Done || !res.Changed || res.Reason != "meant it" {
		t.Fatalf(
			"calls %d, done %v, changed %v, reason %q",
			*calls,
			res.Done,
			res.Changed,
			res.Reason,
		)
	}
	first := res.Messages[2].ToolCalls[1].Result
	if !strings.Contains(first, "an unclosed <!-- comment before {{posts}} hides every post") {
		t.Errorf("done result = %q", first)
	}
	if !strings.Contains(res.Site.HTML, "<!-- oops") {
		t.Errorf("the page was sanitized: %q", res.Site.HTML)
	}
}

// A page that already hid its posts before the turn is not this turn's
// accident, so done ends the turn at once.
func TestMutateDoesNotPauseForAnAlreadyHiddenBoard(t *testing.T) {
	m, calls := scriptedMutator(t, [][2]string{
		{"edit", `{"target":"css","old":"","new":"h1{color:red}"}`},
		{"done", `{"reason":"red"}`},
	})
	res, err := m.Mutate(
		context.Background(),
		Site{HTML: "<h1>beaverdream</h1><!--<main>" + PostsPlaceholder + "</main>"},
		Room{},
		Post{Author: "dave", Body: "red"},
	)
	if err != nil {
		t.Fatalf("Mutate: %v", err)
	}
	if *calls != 1 || !res.Done {
		t.Fatalf("calls %d, done %v", *calls, res.Done)
	}
}

func TestPostsSwallowedBy(t *testing.T) {
	board := `<main><form class="postbox" method="post" action="/posts">` +
		`<textarea name="body"></textarea></form>` +
		`<div class="thread">` + PostsPlaceholder + `</div></main>`

	cases := []struct {
		name, start, rest, want string
	}{
		{name: "seed page", start: "<h1>beaverdream</h1>", rest: board},
		{
			name:  "closed raw text",
			start: "<script>if (a < b) document.title = '<!--'</script><style>p{}</style>",
			rest:  board,
		},
		{
			name:  "a comment closed before the posts",
			start: "<h1>beaverdream</h1><!-- hidden",
			rest:  "--><main>" + PostsPlaceholder + "</main>",
		},
		{
			name:  "comment after the posts",
			start: "<h1>beaverdream</h1>",
			rest:  "<main>" + PostsPlaceholder + "</main><!-- trailing",
		},
		{
			name:  "unclosed comment",
			start: "<h1>beaverdream</h1><!-- todo",
			rest:  board,
			want:  "an unclosed <!-- comment",
		},
		{
			name:  "unclosed script",
			start: "<script>let x = 1",
			rest:  board,
			want:  "an unclosed <script>",
		},
		{
			name:  "unclosed style next to the posts",
			start: "<h1>beaverdream</h1>",
			rest:  "<style>p{color:red}<main>" + PostsPlaceholder + "</main>",
			want:  "an unclosed <style>",
		},
		{
			name:  "unclosed textarea",
			start: "<h1>beaverdream</h1>",
			rest:  "<textarea name=body><main>" + PostsPlaceholder + "</main>",
			want:  "an unclosed <textarea>",
		},
		{
			name:  "unclosed title",
			start: "<title>beaverdream",
			rest:  board,
			want:  "an unclosed <title>",
		},
		{
			name:  "unclosed attribute quote",
			start: `<h1 class="big>beaverdream</h1>`,
			rest:  "<main>" + PostsPlaceholder + "</main>",
			want:  "an unclosed tag or attribute quote",
		},
		{
			// A later quote closes the attribute, so the
			// browser still reaches the posts.
			name:  "attribute quote closed later",
			start: `<h1 class="big>beaverdream</h1>`,
			rest:  board,
		},
		{
			name:  "no placeholder",
			start: "<!--",
			rest:  "<main></main>",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := PostsSwallowedBy(Site{HTML: tc.start + tc.rest})
			if got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}
