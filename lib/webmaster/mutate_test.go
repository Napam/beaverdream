package webmaster

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
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
		Room{},
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
	if len(res.Messages[2].ToolCalls) != 1 {
		t.Fatalf("first round called %d tools, want 1", len(res.Messages[2].ToolCalls))
	}
	for _, call := range res.Messages[2].ToolCalls {
		if call.Result != "ok" {
			t.Errorf("tool %s result = %q, want ok", call.Name, call.Result)
		}
	}
}

// editBytes sums the arguments of every call that changed the page, which is
// what the model had to write out.
func editBytes(res Result) int {
	total := 0
	for _, message := range res.Messages {
		for _, call := range message.ToolCalls {
			if call.Name != "done" {
				total += len(call.Arguments)
			}
		}
	}
	return total
}

// A small change is a small answer: the edit and done in one response, with
// arguments a fraction of a whole rewrite.
func TestMutateAppliesASmallEditInOneRound(t *testing.T) {
	m, closeServer := testMutator(t)
	defer closeServer()
	rounds := 0
	m.OnRoundDone = func(RoundStats) { rounds++ }

	res, err := m.Mutate(
		context.Background(),
		Site{Board: PostsPlaceholder},
		Room{},
		Post{Author: "dave", Body: "mockedit the heading red"},
	)
	if err != nil {
		t.Fatalf("Mutate: %v", err)
	}
	if !res.Changed || !res.Done || res.Changes != "stylesheet" {
		t.Fatalf("changed %v, done %v, changes %q", res.Changed, res.Done, res.Changes)
	}
	if res.Site.CSS != "h1{color:#c00}" {
		t.Errorf("css = %q", res.Site.CSS)
	}
	if rounds != 1 {
		t.Errorf("rounds = %d, want 1", rounds)
	}

	theme, err := m.Mutate(
		context.Background(),
		Site{Board: PostsPlaceholder},
		Room{},
		Post{Author: "dave", Body: "make it look like the matrix"},
	)
	if err != nil {
		t.Fatalf("Mutate theme: %v", err)
	}
	small, whole := editBytes(res), editBytes(theme)
	if small > 200 || small*4 > whole {
		t.Errorf("edit wrote %d bytes, a whole rewrite %d", small, whole)
	}
}

func TestMutatePatchesAValueInPlace(t *testing.T) {
	m, closeServer := testMutator(t)
	defer closeServer()

	first, err := m.Mutate(
		context.Background(),
		Site{Board: PostsPlaceholder},
		Room{},
		Post{Author: "dave", Body: "hello"},
	)
	if err != nil {
		t.Fatalf("Mutate default theme: %v", err)
	}
	res, err := m.Mutate(
		context.Background(),
		first.Site,
		Room{},
		Post{Author: "dave", Body: "mockpatch the accent"},
	)
	if err != nil {
		t.Fatalf("Mutate patch: %v", err)
	}
	if res.Changes != "stylesheet" {
		t.Errorf("changes = %q, want stylesheet", res.Changes)
	}
	if !strings.Contains(res.Site.CSS, mockllm.PatchedAccent) ||
		strings.Contains(res.Site.CSS, "--accent:#9db4ff") {
		t.Errorf("accent was not patched: %q", res.Site.CSS)
	}
	if res.Site.Board != first.Site.Board {
		t.Error("a stylesheet edit changed the board")
	}
}

// A done next to a rejected edit would end the turn before the model saw the
// rejection. With rounds left, the turn stays open for one fix.
func TestMutateDefersDoneAfterARejectedEdit(t *testing.T) {
	m, closeServer := testMutator(t)
	defer closeServer()
	var rounds []RoundStats
	m.OnRoundDone = func(stats RoundStats) { rounds = append(rounds, stats) }

	res, err := m.Mutate(
		context.Background(),
		Site{CSS: "body{margin:0}", Board: PostsPlaceholder},
		Room{},
		Post{Author: "dave", Body: "mockbadedit"},
	)
	if err != nil {
		t.Fatalf("Mutate: %v", err)
	}
	if len(rounds) != 2 {
		t.Fatalf("rounds = %d, want 2", len(rounds))
	}
	if rounds[0].Rejected != 1 || strings.Join(rounds[0].Tools, ",") != "edit,done" {
		t.Errorf("first round = %+v", rounds[0])
	}
	first := res.Messages[2].ToolCalls
	if !strings.HasPrefix(first[0].Result, "rejected:") {
		t.Errorf("edit result = %q, want a rejection", first[0].Result)
	}
	if first[1].Result != doneDeferred {
		t.Errorf("done result = %q, want it deferred", first[1].Result)
	}
	if !res.Done || res.Changed {
		t.Errorf("done %v, changed %v", res.Done, res.Changed)
	}
}

// On the last round there is nobody left to fix it, so done ends the turn.
func TestMutateAcceptsDoneOnTheLastRound(t *testing.T) {
	m, closeServer := testMutator(t)
	defer closeServer()
	m.maxRounds = 1

	res, err := m.Mutate(
		context.Background(),
		Site{CSS: "body{margin:0}", Board: PostsPlaceholder},
		Room{},
		Post{Author: "dave", Body: "mockbadedit"},
	)
	if err != nil {
		t.Fatalf("Mutate: %v", err)
	}
	if !res.Done || res.Reason != "a small edit" {
		t.Fatalf("done %v, reason %q", res.Done, res.Reason)
	}
	if got := res.Messages[2].ToolCalls[1].Result; got != "ok" {
		t.Errorf("done result = %q, want ok", got)
	}
}

// Each assistant message carries the stats of its round, so a stored turn says
// what every completion cost.
func TestMutateRecordsRoundStats(t *testing.T) {
	m, closeServer := testMutator(t)
	defer closeServer()

	res, err := m.Mutate(
		context.Background(),
		Site{CSS: "body{margin:0}", Board: PostsPlaceholder},
		Room{},
		Post{Author: "dave", Body: "mockbadedit"},
	)
	if err != nil {
		t.Fatalf("Mutate: %v", err)
	}
	if len(res.Rounds) != 2 || res.DoneRound != 2 {
		t.Fatalf("rounds %d, done round %d", len(res.Rounds), res.DoneRound)
	}
	first := res.Messages[2].Stats
	if first == nil || first.Round != 1 || first.Rejected != 1 ||
		strings.Join(first.Tools, ",") != "edit,done" {
		t.Fatalf("first round stats = %+v", first)
	}
	if first.Deferred != doneDeferred {
		t.Errorf("deferred = %q, want the done reason", first.Deferred)
	}
	if len(first.Outcomes) != 2 || !strings.HasPrefix(first.Outcomes[0], "edit: rejected:") ||
		!strings.HasPrefix(first.Outcomes[1], "done: not finished:") {
		t.Errorf("outcomes = %q", first.Outcomes)
	}
	if res.Messages[3].Stats == nil || res.Messages[3].Stats.Round != 2 {
		t.Fatalf("second round stats = %+v", res.Messages[3].Stats)
	}

	data, err := json.Marshal(res.Messages)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(data), `"stats":{"round":1,`) {
		t.Errorf("stored messages lack the stats: %s", data)
	}
}

// DeepSeek reports cache hits in its own usage field. It counts when the
// OpenAI field is missing.
func TestMutateReadsDeepSeekCacheHits(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"x","object":"chat.completion","created":0,"model":"m",`+
			`"choices":[{"index":0,"finish_reason":"tool_calls","message":{"role":"assistant",`+
			`"content":"","tool_calls":[{"id":"c1","type":"function","function":`+
			`{"name":"done","arguments":"{\"reason\":\"hi\"}"}}]}}],`+
			`"usage":{"prompt_tokens":1000,"completion_tokens":10,"total_tokens":1010,`+
			`"prompt_cache_hit_tokens":900,"prompt_cache_miss_tokens":100}}`)
	}))
	defer srv.Close()
	m := New(openai.NewClient(option.WithBaseURL(srv.URL), option.WithAPIKey("test")), "m", 3, "")

	res, err := m.Mutate(
		context.Background(),
		Site{Board: PostsPlaceholder},
		Room{},
		Post{Body: "hi"},
	)
	if err != nil {
		t.Fatalf("Mutate: %v", err)
	}
	if len(res.Rounds) != 1 || res.Rounds[0].CachedTokens != 900 {
		t.Fatalf("rounds = %+v, want 900 cached tokens", res.Rounds)
	}
}

// Every completion asks for a tool call, so the model cannot end a turn in
// prose.
func TestMutateRequiresAToolCall(t *testing.T) {
	var choices []any
	mock := mockllm.Handler()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req struct {
			ToolChoice any `json:"tool_choice"`
		}
		_ = json.Unmarshal(body, &req)
		choices = append(choices, req.ToolChoice)
		r.Body = io.NopCloser(strings.NewReader(string(body)))
		mock.ServeHTTP(w, r)
	}))
	defer srv.Close()
	m := New(openai.NewClient(option.WithBaseURL(srv.URL), option.WithAPIKey("test")), "m", 3, "")

	if _, err := m.Mutate(
		context.Background(),
		Site{CSS: "body{margin:0}", Board: PostsPlaceholder},
		Room{},
		Post{Author: "dave", Body: "mockbadedit"},
	); err != nil {
		t.Fatalf("Mutate: %v", err)
	}
	if len(choices) != 2 {
		t.Fatalf("completions = %d, want 2", len(choices))
	}
	for i, choice := range choices {
		if choice != "required" {
			t.Errorf("round %d tool_choice = %v, want required", i+1, choice)
		}
	}
}

// A model that answers in words with no tool call ends the turn in one round,
// whatever the budget says. The reason must name that round, not the budget.
func TestMutateStopsOnAProseReply(t *testing.T) {
	m, closeServer := testMutator(t)
	defer closeServer()
	m.maxRounds = 16384

	res, err := m.Mutate(
		context.Background(),
		Site{CSS: "body{margin:0}", Board: PostsPlaceholder},
		Room{},
		Post{Author: "dave", Body: "mockprose"},
	)
	if err != nil {
		t.Fatalf("Mutate: %v", err)
	}
	if len(res.Rounds) != 1 || res.Done {
		t.Fatalf("rounds %d, done %v", len(res.Rounds), res.Done)
	}
	if strings.Contains(res.Reason, "16384") {
		t.Errorf("reason names the budget, not the round: %q", res.Reason)
	}
	if res.Reason != "i would restyle this, but i have nothing in hand." {
		t.Errorf("reason = %q, want the prose", res.Reason)
	}
	if res.Rounds[0].FinishReason != "stop" {
		t.Errorf("finish reason = %q", res.Rounds[0].FinishReason)
	}
}

// A reply that stopped at the output cap never got far enough to call a tool.
// The reason says so instead of blaming the round budget.
func TestMutateNamesTheOutputCap(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"cap","object":"chat.completion","model":"m",` +
			`"choices":[{"index":0,"message":{"role":"assistant","content":""},` +
			`"finish_reason":"length"}],` +
			`"usage":{"prompt_tokens":10,"completion_tokens":8000,"total_tokens":8010}}`))
	}))
	defer srv.Close()
	m := New(openai.NewClient(option.WithBaseURL(srv.URL), option.WithAPIKey("test")), "m", 5, "")

	res, err := m.Mutate(
		context.Background(),
		Site{CSS: "body{margin:0}", Board: PostsPlaceholder},
		Room{},
		Post{Author: "dave", Body: "hello"},
	)
	if err != nil {
		t.Fatalf("Mutate: %v", err)
	}
	if len(res.Rounds) != 1 {
		t.Fatalf("rounds = %d, want 1", len(res.Rounds))
	}
	if !strings.Contains(res.Reason, "output cap in round 1") {
		t.Errorf("reason = %q, want the cap named", res.Reason)
	}
}

func TestMutateNoopKeepsSite(t *testing.T) {
	m, closeServer := testMutator(t)
	defer closeServer()

	original := Site{CSS: "body{color:red}", Board: PostsPlaceholder}
	res, err := m.Mutate(
		context.Background(),
		original,
		Room{},
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

func TestUserPromptCapsHistoryAndNamesTheTrigger(t *testing.T) {
	site := Site{Version: 8, Board: PostsPlaceholder, ChangesSinceHeal: 7}
	room := Room{Log: []Turn{{
		ID:     1,
		Kind:   TurnPost,
		Author: "long",
		Body:   strings.Repeat("x", 1000),
	}}}
	trigger := Post{ID: 9, Author: "dave", Body: "hello"}

	got := userPrompt(site, room, []Post{trigger})
	if strings.Contains(got, strings.Repeat("x", historyPostLen+1)) {
		t.Error("old post was not shortened")
	}
	if !strings.Contains(got, "BOARD IS PLAIN: no") {
		t.Error("prompt does not say whether the board is plain")
	}
	if !strings.Contains(got, "CHANGES SINCE THE LAST HEAL: 7") {
		t.Error("prompt does not carry the change count")
	}
	if !strings.Contains(got, "NEW POST #9 from dave") {
		t.Error("prompt does not name the trigger post")
	}
	if !strings.Contains(got, "The poster is looking at this post.") {
		t.Error("prompt does not anchor the change to the poster")
	}
}

func TestUserPromptPutsTheStableHistoryBeforeThePage(t *testing.T) {
	room := Room{Summary: "old news", Notes: "keep an eye on dave", Log: []Turn{{
		ID: 1, Kind: TurnPost, Author: "kit", Body: "hi",
	}}}
	trigger := Post{ID: 9, Author: "dave", Body: "yo"}
	got := userPrompt(Site{CSS: "body{}", Plain: true}, room, []Post{trigger})

	order := []string{
		"COMPACTED HISTORY:",
		"ROOM LOG AFTER THAT",
		"YOUR NOTES:",
		"CURRENT STYLESHEET:",
		"CURRENT REGIONS:",
		"CURRENT SHARED STATE:",
		"BOARD IS PLAIN:",
		"NEW POST #9",
	}
	last := -1
	for _, section := range order {
		at := strings.Index(got, section)
		if at < 0 || at < last {
			t.Fatalf("section %q is missing or out of order in:\n%s", section, got)
		}
		last = at
	}
	if strings.LastIndex(got, "NEW POST") < strings.Index(got, "BOARD IS PLAIN:") {
		t.Error("NEW POST is not the final section")
	}
}

// The sections tile the prompt with no gap or overlap, and each one starts at
// its own heading, so the admin panel shows exactly what the model read.
func TestUserPromptSectionsTileThePrompt(t *testing.T) {
	room := Room{Summary: "old news", Notes: "keep an eye on dave", Log: []Turn{{
		ID: 1, Kind: TurnPost, Author: "kit", Body: "hi",
	}}}
	site := Site{CSS: "body{}", Header: "<h1>hi</h1>", Board: "{{posts}}", Footer: "<p>bye</p>"}
	prompt, sections := userPromptSections(site, room, []Post{{ID: 9, Author: "dave", Body: "yo"}})

	if prompt != userPrompt(site, room, []Post{{ID: 9, Author: "dave", Body: "yo"}}) {
		t.Fatal("the sectioned prompt differs from the prompt")
	}
	starts := map[string]string{
		"compacted history": "COMPACTED HISTORY:",
		"room log":          "ROOM LOG AFTER THAT",
		"notes":             "YOUR NOTES:",
		"stylesheet":        "CURRENT STYLESHEET:",
		"header":            "CURRENT REGIONS:\nheader:",
		"board":             "board:\n{{posts}}",
		"footer":            "footer:\n<p>bye</p>",
		"shared state":      "CURRENT SHARED STATE:",
		"journal":           "CURRENT JOURNAL:",
		"flags":             "BOARD IS PLAIN:",
		"new post":          "NEW POST #9 from dave",
	}
	if len(sections) != len(starts) {
		t.Fatalf("got %d sections, want %d: %+v", len(sections), len(starts), sections)
	}
	at := 0
	for _, s := range sections {
		if s.Start != at || s.End < s.Start {
			t.Fatalf("section %q spans %d..%d, want it to start at %d", s.Name, s.Start, s.End, at)
		}
		if !strings.HasPrefix(prompt[s.Start:s.End], starts[s.Name]) {
			t.Errorf("section %q starts with %q", s.Name, prompt[s.Start:min(s.End, s.Start+30)])
		}
		at = s.End
	}
	if at != len(prompt) {
		t.Errorf("sections end at %d, prompt is %d bytes", at, len(prompt))
	}
}

// A notes rewrite must not touch the cacheable prefix: everything through the
// end of the room log stays byte for byte the same.
func TestUserPromptKeepsTheLogPrefixWhenNotesChange(t *testing.T) {
	room := Room{Summary: "old news", Log: []Turn{
		{ID: 1, Kind: TurnPost, Author: "kit", Body: "hi"},
		{ID: 2, Kind: TurnWebmaster, Said: "hello kit", Status: "kept"},
	}}
	trigger := []Post{{ID: 3, Author: "dave", Body: "yo"}}

	room.Notes = "first notes"
	before := userPrompt(Site{}, room, trigger)
	room.Notes = "completely different notes"
	after := userPrompt(Site{}, room, trigger)

	cut := strings.Index(before, "\nYOUR NOTES:")
	if cut < 0 || !strings.HasPrefix(after, before[:cut]) {
		t.Fatalf("notes changed the prefix before them:\n%s\n---\n%s", before, after)
	}
	if !strings.Contains(before[:cut], `#2 you: "hello kit"`) {
		t.Errorf("the room log is not inside the stable prefix:\n%s", before[:cut])
	}
}

func TestPageRunesCountsThePageSections(t *testing.T) {
	small := PageRunes(Site{CSS: "x"})
	big := PageRunes(Site{CSS: strings.Repeat("x", 4001)})
	if big-small != 4000 {
		t.Errorf("page runes grew by %d, want 4000", big-small)
	}
}

func TestUserPromptCreditsABurstToEveryone(t *testing.T) {
	got := userPrompt(Site{Version: 9}, Room{}, []Post{
		{ID: 8, Author: "kit", Body: "one"},
		{ID: 9, Author: "dave", Body: "two"},
	})
	if !strings.Contains(got, "NEW POSTS since the last change") {
		t.Errorf("burst is not labeled as several posts: %s", got)
	}
	if !strings.Contains(got, "#8 kit: one") || !strings.Contains(got, "#9 dave: two") {
		t.Errorf("burst is missing a poster: %s", got)
	}
	if !strings.Contains(got, "The posters are looking at their own posts.") {
		t.Errorf("burst uses the singular poster line: %s", got)
	}
}

func TestMutateReportsModelFailure(t *testing.T) {
	m, closeServer := testMutator(t)
	defer closeServer()

	if _, err := m.Mutate(
		context.Background(),
		Site{},
		Room{},
		Post{Body: "mockfail"},
	); err == nil {
		t.Fatal("expected an error")
	}
}

// The prompt points the model at the browser for per-visitor jokes.
func TestTheSystemPromptTellsTheModelHowToPersonalize(t *testing.T) {
	if !strings.Contains(systemPrompt(), "dataset.name") {
		t.Error("the prompt does not tell the model how to personalize in the browser")
	}
	if strings.Contains(systemPrompt(), "set_user_override") {
		t.Error("the prompt still describes a per-visitor tool")
	}
}

// The model styles posts it never writes. Every class the post renderer emits
// must be named in the static prompt, with the base rules it overrides.
func TestTheSystemPromptShowsThePageAnatomy(t *testing.T) {
	rendered := RenderPosts([]Post{{ID: 3, Author: "a", When: "now", Body: "x\n>q\n>>2"}}) +
		RenderPosts(nil)
	classes := regexp.MustCompile(`class="([^"]+)"`).FindAllStringSubmatch(rendered, -1)
	if len(classes) < 8 {
		t.Fatalf("found only %d classes in %s", len(classes), rendered)
	}

	prompt := systemPrompt()
	if prompt != systemPrompt() {
		t.Fatal("the system prompt is not a stable string")
	}
	for _, match := range classes {
		for _, class := range strings.Fields(match[1]) {
			if !strings.Contains(prompt, "."+class) &&
				!strings.Contains(prompt, `class="`+class+`"`) {
				t.Errorf("class %q is not named in the prompt", class)
			}
		}
	}
	for _, want := range []string{
		strings.TrimSpace(BaseCSS),
		"white-space:pre-wrap",
		"span.quote",
		"a.post-ref",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt lacks %q", want)
		}
	}
	if !strings.HasSuffix(prompt, "Do not write commentary outside tool calls.") {
		t.Error("the closing instruction is not last")
	}
}

// testCall is one tool call a scripted completion makes.
type testCall struct {
	name      string
	arguments string
}

// writeCompletion answers one completion with these calls and, when set, the
// reasoning the provider reports alongside them.
func writeCompletion(w http.ResponseWriter, reasoning string, calls ...testCall) {
	toolCalls := make([]any, len(calls))
	for i, call := range calls {
		toolCalls[i] = map[string]any{
			"id":   fmt.Sprintf("call_%d", i+1),
			"type": "function",
			"function": map[string]any{
				"name":      call.name,
				"arguments": call.arguments,
			},
		}
	}
	message := map[string]any{
		"role":       "assistant",
		"content":    "",
		"tool_calls": toolCalls,
	}
	if reasoning != "" {
		message["reasoning_content"] = reasoning
	}
	reply, _ := json.Marshal(map[string]any{
		"id":      "chatcmpl-test",
		"object":  "chat.completion",
		"created": 0,
		"model":   "m",
		"choices": []any{map[string]any{
			"index":         0,
			"message":       message,
			"finish_reason": "tool_calls",
		}},
		"usage": map[string]any{
			"prompt_tokens":     1,
			"completion_tokens": 1,
			"total_tokens":      2,
		},
	})
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(reply)
}

func writeToolCall(w http.ResponseWriter, name, arguments string) {
	writeCompletion(w, "", testCall{name: name, arguments: arguments})
}

// brokenArguments is the shape a provider returns when it leaks its native
// tool markup into the arguments instead of JSON.
const brokenArguments = `{"target">footer</｜DSML｜: "`

// A round whose calls all failed to parse is sampled again, not replayed: the
// next request looks exactly like the one before it, so the provider's broken
// markup never enters the conversation.
func TestMutateResamplesARoundOfBrokenArguments(t *testing.T) {
	var requests [][]byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		requests = append(requests, body)
		if len(requests) == 1 {
			writeToolCall(w, "edit", brokenArguments)
			return
		}
		writeToolCall(w, "done", `{"reason":"left it alone"}`)
	}))
	defer srv.Close()
	m := New(openai.NewClient(option.WithBaseURL(srv.URL), option.WithAPIKey("test")), "m", 3, "")

	res, err := m.Mutate(
		context.Background(),
		Site{Board: PostsPlaceholder},
		Room{},
		Post{Body: "hi"},
	)
	if err != nil {
		t.Fatalf("Mutate: %v", err)
	}
	if len(requests) != 2 {
		t.Fatalf("completions = %d, want 2", len(requests))
	}
	var second struct {
		Messages []json.RawMessage `json:"messages"`
	}
	if err := json.Unmarshal(requests[1], &second); err != nil {
		t.Fatalf("decode request: %v", err)
	}
	if len(second.Messages) != 2 {
		t.Errorf(
			"the resampled request has %d messages, want system and user",
			len(second.Messages),
		)
	}
	if strings.Contains(string(requests[1]), "DSML") {
		t.Error("the next prompt carries the provider's tool markup")
	}
	if !res.Done {
		t.Error("done after the broken round was not accepted")
	}
	// The turn still records what arrived, for the admin, and marks the round
	// as one the model never saw.
	if len(res.Messages) != 4 {
		t.Fatalf("stored messages = %d, want the broken round and the done", len(res.Messages))
	}
	if !res.Rounds[0].Resampled || res.Messages[2].Stats.Resampled != true {
		t.Error("the broken round is not marked as resampled")
	}
	stored := res.Messages[2].ToolCalls[0]
	if stored.Arguments != brokenArguments {
		t.Errorf("stored arguments = %q, want what arrived", stored.Arguments)
	}
	if !strings.Contains(stored.Result, "tool markup") {
		t.Errorf("result = %q, want a markup rejection", stored.Result)
	}
}

// A round with one good call and one broken one keeps the good edit and
// replays the broken call as an empty object, so the model reads the
// rejection without the markup.
func TestMutateDropsBrokenArgumentsFromAMixedRound(t *testing.T) {
	var requests [][]byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		requests = append(requests, body)
		if len(requests) == 1 {
			writeCompletion(
				w,
				"",
				testCall{
					name:      "edit",
					arguments: `{"target":"css","old":"","new":"h1{color:red}"}`,
				},
				testCall{name: "edit", arguments: brokenArguments},
			)
			return
		}
		writeToolCall(w, "done", `{"reason":"touched the css"}`)
	}))
	defer srv.Close()
	m := New(openai.NewClient(option.WithBaseURL(srv.URL), option.WithAPIKey("test")), "m", 3, "")

	res, err := m.Mutate(
		context.Background(),
		Site{Board: PostsPlaceholder},
		Room{},
		Post{Body: "hi"},
	)
	if err != nil {
		t.Fatalf("Mutate: %v", err)
	}
	if len(requests) != 2 {
		t.Fatalf("completions = %d, want 2", len(requests))
	}
	if strings.Contains(string(requests[1]), "DSML") {
		t.Error("the next prompt carries the provider's tool markup")
	}
	if !strings.Contains(string(requests[1]), `"arguments":"{}"`) {
		t.Error("the broken call was not replayed as an empty object")
	}
	if !res.Changed || !strings.Contains(res.Site.CSS, "color:red") {
		t.Errorf("the good edit was dropped: changed %v, css %q", res.Changed, res.Site.CSS)
	}
	calls := res.Messages[2].ToolCalls
	if len(calls) != 2 || calls[0].Result != "ok" ||
		!strings.Contains(calls[1].Result, "tool markup") {
		t.Errorf("stored calls = %+v", calls)
	}
}

// Valid JSON of the wrong shape is a normal model mistake, not provider
// corruption, so it is named as a schema error and does not count toward the
// broken-arguments cap.
func TestMutateNamesASchemaMismatch(t *testing.T) {
	rounds := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rounds++
		if rounds == 1 {
			writeToolCall(w, "edit", `{"target":5}`)
			return
		}
		writeToolCall(w, "done", `{"reason":"nothing to do"}`)
	}))
	defer srv.Close()
	m := New(openai.NewClient(option.WithBaseURL(srv.URL), option.WithAPIKey("test")), "m", 3, "")

	res, err := m.Mutate(
		context.Background(),
		Site{Board: PostsPlaceholder},
		Room{},
		Post{Body: "hi"},
	)
	if err != nil {
		t.Fatalf("Mutate: %v", err)
	}
	if got := res.Messages[2].ToolCalls[0].Result; !strings.HasPrefix(
		got,
		"rejected: arguments do not match the schema",
	) {
		t.Errorf("result = %q, want a schema mismatch", got)
	}
	if !res.Done || len(res.Rounds) != 2 {
		t.Errorf("done %v after %d rounds, want done in round 2", res.Done, len(res.Rounds))
	}
}

// Tool markup inside otherwise valid JSON is still provider corruption. The
// call is dropped, never applied to the page, and a mixed round must not
// replay it either: the markup would go straight back into the next prompt.
func TestMutateDropsToolMarkupFromAMixedRound(t *testing.T) {
	arguments := `{"target":"css","old":"","new":"h1{color:red}` + providerToolMarkup + `"}`
	var requests [][]byte
	rounds := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		requests = append(requests, body)
		rounds++
		if rounds == 1 {
			writeCompletion(
				w,
				"",
				testCall{
					name:      "edit",
					arguments: `{"target":"css","old":"","new":"h1{color:blue}"}`,
				},
				testCall{name: "edit", arguments: arguments},
			)
			return
		}
		writeToolCall(w, "done", `{"reason":"dropped the markup"}`)
	}))
	defer srv.Close()
	m := New(openai.NewClient(option.WithBaseURL(srv.URL), option.WithAPIKey("test")), "m", 3, "")

	res, err := m.Mutate(
		context.Background(),
		Site{Board: PostsPlaceholder},
		Room{},
		Post{Body: "hi"},
	)
	if err != nil {
		t.Fatalf("Mutate: %v", err)
	}
	if !strings.Contains(res.Site.CSS, "color:blue") ||
		strings.Contains(res.Site.CSS, providerToolMarkup) {
		t.Errorf("css = %q, want the good edit and no markup", res.Site.CSS)
	}
	if strings.Contains(string(requests[1]), "DSML") {
		t.Error("the next prompt carries the provider's tool markup")
	}
	if !strings.Contains(string(requests[1]), `"arguments":"{}"`) {
		t.Error("the markup call was not replayed as an empty object")
	}
	if got := res.Messages[2].ToolCalls[1].Result; !strings.Contains(got, "tool markup") {
		t.Errorf("result = %q, want a markup rejection", got)
	}
}

// A provider that answers with broken arguments round after round gets a few
// resamples, not the whole round budget.
func TestMutateStopsWhenBrokenArgumentsKeepComing(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeToolCall(w, "edit", `{"target":`)
	}))
	defer srv.Close()
	m := New(openai.NewClient(option.WithBaseURL(srv.URL), option.WithAPIKey("test")), "m", 10, "")

	res, err := m.Mutate(
		context.Background(),
		Site{Board: PostsPlaceholder},
		Room{},
		Post{Body: "hi"},
	)
	if err != nil {
		t.Fatalf("Mutate: %v", err)
	}
	if len(res.Rounds) != maxBrokenArgumentRounds {
		t.Errorf("rounds = %d, want %d", len(res.Rounds), maxBrokenArgumentRounds)
	}
	if !strings.Contains(res.Reason, "not JSON") {
		t.Errorf("reason = %q, want it to name the broken arguments", res.Reason)
	}
	if res.Done || res.Changed {
		t.Errorf("done %v, changed %v, want the page untouched", res.Done, res.Changed)
	}
}

// DeepSeek's thinking mode needs the reasoning behind a tool call sent back
// with it; without it the model loses the thread of its own tool format.
func TestMutateReplaysDeepSeekReasoning(t *testing.T) {
	var requests [][]byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		requests = append(requests, body)
		if len(requests) == 1 {
			writeCompletion(
				w,
				"the css needs a heading color",
				testCall{
					name:      "edit",
					arguments: `{"target":"css","old":"","new":"h1{color:red}"}`,
				},
			)
			return
		}
		writeCompletion(
			w,
			"done",
			testCall{name: "done", arguments: `{"reason":"colored the heading"}`},
		)
	}))
	defer srv.Close()
	m := New(
		openai.NewClient(option.WithBaseURL(srv.URL), option.WithAPIKey("test")),
		"deepseek-v4.1-flash",
		3,
		"",
	)

	if _, err := m.Mutate(
		context.Background(),
		Site{Board: PostsPlaceholder},
		Room{},
		Post{Body: "hi"},
	); err != nil {
		t.Fatalf("Mutate: %v", err)
	}
	if len(requests) != 2 {
		t.Fatalf("completions = %d, want 2", len(requests))
	}
	var second struct {
		Messages []struct {
			Role             string            `json:"role"`
			ReasoningContent string            `json:"reasoning_content"`
			ToolCalls        []json.RawMessage `json:"tool_calls"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(requests[1], &second); err != nil {
		t.Fatalf("decode request: %v", err)
	}
	for _, msg := range second.Messages {
		if msg.Role == "assistant" && len(msg.ToolCalls) > 0 {
			if msg.ReasoningContent != "the css needs a heading color" {
				t.Errorf("reasoning_content = %q, want the round's reasoning", msg.ReasoningContent)
			}
			return
		}
	}
	t.Error("the next request has no assistant tool call")
}
