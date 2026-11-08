package webmaster

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/Napam/beaverdream/lib/mockllm"
	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
)

func TestCleanJournalSchema(t *testing.T) {
	raw, err := cleanJournalSchema([]journalKeyInput{
		{Name: "moves", Max: 10},
		{Name: "chat"},
	})
	if err != nil {
		t.Fatalf("clean: %v", err)
	}
	schema := ParseJournalSchema(raw)
	if len(schema) != 2 {
		t.Fatalf("keys = %d, want 2", len(schema))
	}
	if schema["moves"].Max != 10 {
		t.Errorf("moves max = %d, want 10", schema["moves"].Max)
	}
	if schema["chat"].Max != defaultJournalMax {
		t.Errorf("chat max = %d, want default %d", schema["chat"].Max, defaultJournalMax)
	}
	if schema["chat"].Rules != 1 {
		t.Errorf("chat rules = %d, want 1", schema["chat"].Rules)
	}
}

// Stored schemas are normalized on read, so a hand-edited zero cannot make
// the server trim every row or stamp rules 0.
func TestParseJournalSchemaNormalizesStoredKeys(t *testing.T) {
	schema := ParseJournalSchema(`{"zero":{"max":0,"rules":0},"huge":{"max":5000,"rules":2}}`)

	if schema["zero"].Max != defaultJournalMax {
		t.Errorf("zero max = %d, want the default %d", schema["zero"].Max, defaultJournalMax)
	}
	if schema["zero"].Rules != 1 {
		t.Errorf("zero rules = %d, want 1", schema["zero"].Rules)
	}
	if schema["huge"].Max != maxJournalRows {
		t.Errorf("huge max = %d, want the cap %d", schema["huge"].Max, maxJournalRows)
	}
	if schema["huge"].Rules != 2 {
		t.Errorf("huge rules = %d, want 2", schema["huge"].Rules)
	}
}

// The system prompt is built with Sprintf from the server's constants.
func TestSystemPromptFormats(t *testing.T) {
	if strings.Contains(systemPrompt(), "%!") {
		t.Error("prompt has a formatting error")
	}
}

func TestCleanJournalSchemaRejects(t *testing.T) {
	cases := map[string][]journalKeyInput{
		"bad name":   {{Name: "Moves!"}},
		"too short":  {{Name: "ab"}},
		"duplicate":  {{Name: "aaa"}, {Name: "aaa"}},
		"too many":   make([]journalKeyInput, maxJournalKeys+1),
		"empty name": {{Name: ""}},
		"leading _":  {{Name: "_abc"}},
	}
	for name, keys := range cases {
		if _, err := cleanJournalSchema(keys); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
}

func TestCleanJournalSchemaAllowsEmpty(t *testing.T) {
	raw, err := cleanJournalSchema(nil)
	if err != nil {
		t.Fatalf("empty list should be allowed: %v", err)
	}
	if raw != "{}" {
		t.Errorf("schema = %s, want {}", raw)
	}
}

func TestMissingJournalKeys(t *testing.T) {
	site := Site{
		JournalSchema: `{"moves":{"max":10,"rules":1},"chat":{"rules":1}}`,
		HTML:          `<div id="game"></div><script>bd.journal("moves")</script>{{posts}}`,
	}
	missing := MissingJournalKeys(site)
	if len(missing) != 1 || missing[0] != "chat" {
		t.Fatalf("missing = %v, want [chat]", missing)
	}

	site.HTML = `<script>var c = bd.journal( 'chat' ); bd.journal("moves")</script>{{posts}}`
	if missing := MissingJournalKeys(site); len(missing) != 0 {
		t.Fatalf("missing = %v, want none", missing)
	}

	// The bare word is prose, not a feed.
	site.HTML = `<p>chat about your moves</p>{{posts}}`
	if missing := MissingJournalKeys(site); len(missing) != 2 {
		t.Fatalf("missing = %v, want both keys when only prose names them", missing)
	}

	site.JournalSchema = "{}"
	if missing := MissingJournalKeys(site); len(missing) != 0 {
		t.Fatalf("no keys should never be missing, got %v", missing)
	}
}

func TestCleanJournalPayload(t *testing.T) {
	raw, err := CleanJournalPayload(json.RawMessage(`{"cell": 4}`))
	if err != nil {
		t.Fatalf("clean: %v", err)
	}
	if raw != `{"cell":4}` {
		t.Errorf("payload = %s, want compact JSON", raw)
	}

	if _, err := CleanJournalPayload(json.RawMessage(`not json`)); err == nil {
		t.Error("invalid JSON should be rejected")
	}

	big := `"` + strings.Repeat("a", maxJournalPayload) + `"`
	if _, err := CleanJournalPayload(json.RawMessage(big)); err == nil {
		t.Error("oversized payload should be rejected")
	}
}

func TestJournalRuntimeIsOnEveryPage(t *testing.T) {
	doc := SiteDoc(Site{
		Version:       3,
		HTML:          `<main>{{posts}}</main>`,
		JournalSchema: `{"moves":{"max":10,"rules":1}}`,
		Journal:       map[string]JournalState{"moves": {Match: 2, Rules: 1, Rows: 4}},
		Visitor:       "0123456789abcdef",
	}, nil)
	if !strings.Contains(doc, `id="bd-journal"`) {
		t.Fatal("page is missing the journal config tag")
	}
	if !strings.Contains(doc, `window.bd`) {
		t.Fatal("page is missing the journal runtime")
	}
	if !strings.Contains(doc, `"visitor":"0123456789abcdef"`) {
		t.Fatal("journal config is missing the visitor id")
	}
	if !strings.Contains(doc, `"match":2`) {
		t.Fatal("journal config is missing the current match")
	}
}

type fakeToolCall struct {
	name string
	args string
}

// fakeModel answers each request with the next round of tool calls, so a test
// can script exactly what the model does across rounds.
func fakeModel(t *testing.T, rounds [][]fakeToolCall) *httptest.Server {
	t.Helper()
	var mu sync.Mutex
	next := 0
	server := httptest.NewServer(mockllm.Streamed(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		mu.Lock()
		index := next
		next++
		mu.Unlock()

		calls := make([]map[string]any, 0)
		if index < len(rounds) {
			for i, call := range rounds[index] {
				calls = append(calls, map[string]any{
					"id":   fmt.Sprintf("call_%d_%d", index, i),
					"type": "function",
					"function": map[string]any{
						"name":      call.name,
						"arguments": call.args,
					},
				})
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":      "chatcmpl-test",
			"object":  "chat.completion",
			"created": 0,
			"model":   "fake",
			"choices": []any{map[string]any{
				"index": 0,
				"message": map[string]any{
					"role":       "assistant",
					"content":    "",
					"tool_calls": calls,
				},
				"finish_reason": "tool_calls",
			}},
			"usage": map[string]any{"prompt_tokens": 1, "completion_tokens": 1},
		})
	}))
	t.Cleanup(server.Close)
	return server
}

// A restyle that drops the bd.journal call is caught even when the key still
// appears in prose: the done next to it is deferred, and the next round can
// fix it by dropping the key from the schema.
func TestDoneDefersWhenThePageDropsAJournalKey(t *testing.T) {
	server := fakeModel(t, [][]fakeToolCall{
		{
			{name: "set_site", args: `{"html":"<main><p>no more moves</p>{{posts}}</main>"}`},
			{name: "done", args: `{"reason":"made it plain"}`},
		},
		{
			{name: "set_journal_schema", args: `{"keys":[]}`},
			{name: "done", args: `{"reason":"dropped the game"}`},
		},
	})
	client := openai.NewClient(
		option.WithBaseURL(server.URL),
		option.WithAPIKey("test"),
	)
	m := New(client, "fake", 3, "")

	res, err := m.Run(
		context.Background(),
		Site{
			HTML:          journalPage,
			JournalSchema: `{"moves":{"rules":1}}`,
		},
		Room{},
		Post{Author: "dave", Body: "make it plain"},
	)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !res.Done {
		t.Fatal("the turn never finished")
	}
	if res.DoneRound != 2 {
		t.Fatalf("done round = %d, want 2", res.DoneRound)
	}

	if res.Site.JournalSchema != "{}" {
		t.Errorf("schema = %s, want the key removed in round 2", res.Site.JournalSchema)
	}
}

// Declaring a second key must not reset the first one's max or rules, and the
// rules number can never go down: either would replay old rows through new
// code.
func TestJournalSchemaKeepsOmittedNumbers(t *testing.T) {
	site := Site{
		HTML:          `<main>moves {{posts}}</main>`,
		JournalSchema: `{"moves":{"max":400,"rules":2}}`,
	}
	ed := newEditor(site)

	result := ed.run("set_journal_schema", `{"keys":[{"name":"moves"},{"name":"chat"}]}`)
	if result != "ok" {
		t.Fatalf("result = %q, want ok", result)
	}
	schema := ParseJournalSchema(ed.site.JournalSchema)
	if schema["moves"].Rules != 2 || schema["moves"].Max != 400 {
		t.Errorf("moves = %+v, want max 400 rules 2", schema["moves"])
	}
	if schema["chat"].Rules != 1 || schema["chat"].Max != defaultJournalMax {
		t.Errorf("chat = %+v, want defaults", schema["chat"])
	}

	result = ed.run("set_journal_schema", `{"keys":[{"name":"moves","rules":1}]}`)
	if !strings.HasPrefix(result, "rejected:") {
		t.Fatalf("lowering rules = %q, want a rejection", result)
	}
	if ParseJournalSchema(ed.site.JournalSchema)["moves"].Rules != 2 {
		t.Error("a rejected schema write changed the stored rules")
	}
}

// journalPage is a page whose script opens the moves feed.
const journalPage = `<main><h2>moves</h2><div id="game"></div>` +
	`<script>var feed = bd.journal("moves")</script>{{posts}}</main>`

func runChangesetWith(t *testing.T, site Site, rounds [][]fakeToolCall) Result {
	t.Helper()
	server := fakeModel(t, rounds)
	client := openai.NewClient(
		option.WithBaseURL(server.URL),
		option.WithAPIKey("test"),
	)
	res, err := New(client, "fake", 3, "").Run(
		context.Background(),
		site,
		Room{},
		Post{Author: "dave", Body: "tidy up"},
	)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	return res
}

// Editing prose that happens to name the key is not dropping the feed: the
// call is still there, so done ends the turn at once.
func TestDoneKeepsAProseEditWhenTheCallStays(t *testing.T) {
	res := runChangesetWith(t, Site{
		HTML:          journalPage,
		JournalSchema: `{"moves":{"rules":1}}`,
	}, [][]fakeToolCall{{
		{name: "edit", args: `{"target":"html","old":"<h2>moves</h2>","new":"<h2>play</h2>"}`},
		{name: "done", args: `{"reason":"renamed the heading"}`},
	}})
	if !res.Done || res.DoneRound != 1 {
		t.Fatalf("done = %v in round %d, want done in round 1", res.Done, res.DoneRound)
	}
	if strings.Contains(res.Site.HTML, "<h2>moves</h2>") {
		t.Fatal("the edit did not land")
	}
}

// A rules number already stamped on stored rows belongs to the reducer that
// wrote them. A bump, a new key and a key declared again all skip past it;
// keeping the declared number keeps the old rows.
func TestJournalSchemaSkipsStoredRules(t *testing.T) {
	site := Site{
		HTML:             journalPage,
		JournalSchema:    `{"moves":{"max":300,"rules":1}}`,
		JournalRulesUsed: map[string]int{"moves": 3, "chat": 2},
	}

	ed := newEditor(site)
	result := ed.run("set_journal_schema", `{"keys":[{"name":"moves"}]}`)
	if result != "ok" {
		t.Fatalf("keeping rules = %q, want ok", result)
	}
	if got := ParseJournalSchema(ed.site.JournalSchema)["moves"].Rules; got != 1 {
		t.Fatalf("kept rules = %d, want 1 (a restored game keeps its rows)", got)
	}

	ed = newEditor(site)
	result = ed.run("set_journal_schema", `{"keys":[{"name":"moves","rules":2}]}`)
	if !strings.HasPrefix(result, "ok (") || !strings.Contains(result, "raised to 4") {
		t.Fatalf("bump = %q, want ok with the raise named", result)
	}
	if got := ParseJournalSchema(ed.site.JournalSchema)["moves"].Rules; got != 4 {
		t.Fatalf("bumped rules = %d, want 4, past the stored 3", got)
	}

	ed = newEditor(site)
	result = ed.run("set_journal_schema", `{"keys":[{"name":"moves","rules":7}]}`)
	if result != "ok" {
		t.Fatalf("bump past the floor = %q, want ok", result)
	}
	if got := ParseJournalSchema(ed.site.JournalSchema)["moves"].Rules; got != 7 {
		t.Fatalf("rules = %d, want 7 as asked", got)
	}

	// chat was dropped from the schema, but its rows are still stored.
	ed = newEditor(site)
	result = ed.run("set_journal_schema", `{"keys":[{"name":"moves"},{"name":"chat"}]}`)
	if !strings.HasPrefix(result, "ok") {
		t.Fatalf("redeclare = %q, want ok", result)
	}
	if got := ParseJournalSchema(ed.site.JournalSchema)["chat"].Rules; got != 3 {
		t.Fatalf("redeclared chat rules = %d, want 3, past the stored 2", got)
	}

	ed = newEditor(site)
	result = ed.run("set_journal_schema", `{"keys":[{"name":"fresh"}]}`)
	if result != "ok" || ParseJournalSchema(ed.site.JournalSchema)["fresh"].Rules != 1 {
		t.Fatalf("a key with no rows = %q, want ok at rules 1", result)
	}
}

// The page config carries only what the runtime reads: match, rules and the
// row window, and the visitor's own holdings and the top lists.
func TestJournalConfigLeavesOutPromptData(t *testing.T) {
	data := string(journalConfig(Site{
		Visitor:  "0123456789abcdef",
		Holdings: []HoldingTotal{{Key: "coins", Total: 99, Holders: 3}},
		Mine:     map[string]int64{"coins": 4},
		Journal: map[string]JournalState{"moves": {
			Match: 2,
			Rules: 3,
			Max:   300,
			Rows:  9,
			Last:  []JournalTailRow{{ID: 1, Payload: json.RawMessage(`{"secret":1}`)}},
		}},
	}))
	want := `{"visitor":"0123456789abcdef","keys":{"moves":{"match":2,"rules":3,"max":300}},` +
		`"mine":{"coins":4},"tops":{},"base":"","store":"bd:"}`
	if data != want {
		t.Fatalf("config = %s, want %s", data, want)
	}
}

// The prompt shows each recent row with who wrote it and which match it
// belongs to.
func TestPagePromptShowsJournalRows(t *testing.T) {
	prompt := pagePrompt(Site{
		JournalSchema: `{"moves":{"rules":1}}`,
		Journal: map[string]JournalState{"moves": {
			Match: 2,
			Rules: 1,
			Rows:  5,
			Last: []JournalTailRow{
				{ID: 4, Visitor: "aaaa", Match: 2, Payload: json.RawMessage(`{"cell":4}`)},
			},
		}},
	})
	want := `moves: match 2, rules 1, 5 rows, newest rows of this match: ` +
		`{"id":4,"visitor":"aaaa","match":2,"payload":{"cell":4}}`
	if !strings.Contains(prompt, want) {
		t.Fatalf("prompt journal line missing, want %q in:\n%s", want, prompt)
	}
}

// The runtime recognizes a closed match by the server's exact refusal text.
func TestJournalRuntimeKnowsTheClosedMatchText(t *testing.T) {
	if !strings.Contains(JournalRuntime, `'`+JournalMatchClosed+`'`) {
		t.Fatal("the runtime does not match the server's closed-match text")
	}
}
