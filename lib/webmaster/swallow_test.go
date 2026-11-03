package webmaster

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
)

// scriptedMutator answers each completion with the next list of tool calls,
// given as name and JSON arguments pairs.
func scriptedMutator(t *testing.T, script ...[][2]string) (*Mutator, *int) {
	t.Helper()
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
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
			{"edit", `{"target":"header","old":"","new":"<!-- oops"}`},
			{"done", `{"reason":"a comment"}`},
		},
		[][2]string{{"done", `{"reason":"meant it"}`}},
	)
	res, err := m.Mutate(
		context.Background(),
		Site{Header: "<h1>beaverdream</h1>", Board: "<main>" + PostsPlaceholder + "</main>"},
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
	if !strings.Contains(res.Site.Header, "<!-- oops") {
		t.Errorf("the page was sanitized: %q", res.Site.Header)
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
		Site{Header: "<h1>beaverdream</h1><!--", Board: "<main>" + PostsPlaceholder + "</main>"},
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
		name, header, board, want string
	}{
		{name: "seed page", header: "<h1>beaverdream</h1>", board: board},
		{
			name:   "closed raw text",
			header: "<script>if (a < b) document.title = '<!--'</script><style>p{}</style>",
			board:  board,
		},
		{
			name:   "header opens what the board closes",
			header: "<h1>beaverdream</h1><!-- hidden",
			board:  "--><main>" + PostsPlaceholder + "</main>",
		},
		{
			name:   "comment after the posts",
			header: "<h1>beaverdream</h1>",
			board:  "<main>" + PostsPlaceholder + "</main><!-- trailing",
		},
		{
			name:   "unclosed comment",
			header: "<h1>beaverdream</h1><!-- todo",
			board:  board,
			want:   "an unclosed <!-- comment",
		},
		{
			name:   "unclosed script",
			header: "<script>let x = 1",
			board:  board,
			want:   "an unclosed <script>",
		},
		{
			name:   "unclosed style in the board",
			header: "<h1>beaverdream</h1>",
			board:  "<style>p{color:red}<main>" + PostsPlaceholder + "</main>",
			want:   "an unclosed <style>",
		},
		{
			name:   "unclosed textarea",
			header: "<h1>beaverdream</h1>",
			board:  "<textarea name=body><main>" + PostsPlaceholder + "</main>",
			want:   "an unclosed <textarea>",
		},
		{
			name:   "unclosed title",
			header: "<title>beaverdream",
			board:  board,
			want:   "an unclosed <title>",
		},
		{
			name:   "unclosed attribute quote",
			header: `<h1 class="big>beaverdream</h1>`,
			board:  "<main>" + PostsPlaceholder + "</main>",
			want:   "an unclosed tag or attribute quote",
		},
		{
			// The next quote in the board closes the attribute, so the
			// browser still reaches the posts.
			name:   "attribute quote closed by the board",
			header: `<h1 class="big>beaverdream</h1>`,
			board:  board,
		},
		{
			name:   "board without the placeholder",
			header: "<!--",
			board:  "<main></main>",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := PostsSwallowedBy(Site{Header: tc.header, Board: tc.board})
			if got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}
