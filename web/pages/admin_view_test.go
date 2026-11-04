package pages

import (
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/Napam/beaverdream/db/generated/sqlc"
	"github.com/Napam/beaverdream/lib/webmaster"
)

// Turns made before the author column named who they answered carry the
// constant "webmaster", which the panel must not show as a person.
func TestTurnAuthorHidesTheOldConstant(t *testing.T) {
	for author, want := range map[string]string{
		"dave, kit": "dave, kit",
		"admin":     "admin",
		"webmaster": "",
		"":          "",
	} {
		row := TurnRow{Run: sqlc.ListWebmasterTurnsRow{Author: author}}
		if got := turnAuthor(row); got != want {
			t.Errorf("turnAuthor(%q) = %q, want %q", author, got, want)
		}
	}
}

func storedTurn() (sqlc.ListWebmasterTurnsRow, []webmaster.Message) {
	user := "COMPACTED HISTORY:\n(nothing)\n\nNEW POST #4 from dave:\nmake it pink\nThe poster is looking at this post.\n"
	split := strings.Index(user, "NEW POST")
	return sqlc.ListWebmasterTurnsRow{
		ID:        5,
		Kind:      "webmaster",
		Status:    "changed",
		Messages:  "[big]",
		VersionID: sql.NullInt64{Int64: 3, Valid: true},
	}, []webmaster.Message{
		{Role: "system", Content: "rules"},
		{Role: "user", Content: user, Sections: []webmaster.PromptSection{
			{Name: "compacted history", Start: 0, End: split},
			{Name: "new post", Start: split, End: len(user)},
		}},
		{
			Role:      "assistant",
			Reasoning: "thinking that goes back with the call",
			ToolCalls: []webmaster.ToolCall{
				{
					Name:      "edit",
					Arguments: `{"target":"css","old":"a{}","new":"a{color:pink}\nb{}"}`,
					Result:    "rejected: old text not found",
				},
				{Name: "done", Arguments: `{"reason":"pink"}`, Result: "not finished: fix it"},
			},
			Stats: &webmaster.RoundStats{
				Round:            1,
				PromptTokens:     100,
				CachedTokens:     80,
				CompletionTokens: 10,
				Rejected:         1,
			},
		},
		{
			Role: "assistant",
			ToolCalls: []webmaster.ToolCall{
				{Name: "done", Arguments: `{"reason":"pink"}`, Result: "ok"},
			},
			Stats: &webmaster.RoundStats{
				Round:            2,
				PromptTokens:     120,
				CachedTokens:     100,
				CompletionTokens: 5,
			},
		},
	}
}

// A row sums the rounds and names what the turn answered, without carrying
// the conversation.
func TestNewTurnRowSumsRounds(t *testing.T) {
	run, messages := storedTurn()
	row := NewTurnRow(run, messages)

	if row.Run.Messages != "" {
		t.Error("the row kept the stored conversation")
	}
	if row.Rounds != 2 || row.PromptTokens != 220 || row.CachedTokens != 180 ||
		row.CompletionTokens != 15 || row.Rejected != 1 {
		t.Errorf("sums = %+v", row)
	}
	if row.Trigger != "make it pink" {
		t.Errorf("trigger = %q, want the new post body", row.Trigger)
	}
}

// The inspector shows the prompt by its recorded sections, and each round
// carries what earlier rounds appended, reasoning included.
func TestNewTurnDetailLaysOutTheConversation(t *testing.T) {
	run, messages := storedTurn()
	d := NewTurnDetail(run, messages, 50)

	var names []string
	for _, part := range d.Context {
		names = append(names, part.Name)
	}
	if got := strings.Join(
		names,
		",",
	); got != "system prompt,tool definitions,compacted history,new post" {
		t.Errorf("context parts = %s", got)
	}
	if !d.Sectioned || !d.Stored {
		t.Errorf("sectioned = %v, stored = %v", d.Sectioned, d.Stored)
	}
	if len(d.Rounds) != 2 {
		t.Fatalf("rounds = %d", len(d.Rounds))
	}
	base := len("rules") + 50 + len(messages[1].Content)
	if d.Rounds[0].Base != base || d.Rounds[0].Carried != 0 {
		t.Errorf(
			"round 1 = base %d carried %d, want %d and 0",
			d.Rounds[0].Base,
			d.Rounds[0].Carried,
			base,
		)
	}
	carried := len(messages[2].Reasoning)
	for _, call := range messages[2].ToolCalls {
		carried += len(call.Name) + len(call.Arguments) + len(call.Result)
	}
	if d.Rounds[1].Carried != carried {
		t.Errorf("round 2 carried %d, want %d", d.Rounds[1].Carried, carried)
	}

	calls := d.Rounds[0].Calls
	if !calls[0].Rejected || calls[0].Deferred || calls[1].Rejected || !calls[1].Deferred {
		t.Errorf("call flags = %+v", calls)
	}
	var keys []string
	for _, arg := range calls[0].Args {
		keys = append(keys, arg.Key)
	}
	if got := strings.Join(keys, ","); got != "target,old,new" {
		t.Errorf("argument order = %s, want the written order", got)
	}
	if arg := calls[0].Args[2]; arg.Value != "a{color:pink}\nb{}" || !arg.Block {
		t.Errorf("new = %+v, want the unquoted string in a block", arg)
	}
}

// A prompt stored without sections, or with offsets that do not fit, is one
// block instead of a wrong split.
func TestNewTurnDetailFallsBackToOneBlock(t *testing.T) {
	run, messages := storedTurn()
	messages[1].Sections[1].End++
	d := NewTurnDetail(run, messages, 0)
	if d.Sectioned || len(d.Context) != 3 || d.Context[2].Name != "user prompt" {
		t.Errorf("context = %+v", d.Context)
	}
}

func TestSplitArgsKeepsRawTextThatIsNotAnObject(t *testing.T) {
	args := splitArgs(`not json`)
	if len(args) != 1 || args[0].Value != "not json" {
		t.Errorf("args = %+v", args)
	}
}

// The charts read oldest first and skip a run that has nothing to count yet.
func TestNewMetricsSkipsRunningTurns(t *testing.T) {
	m := NewMetrics([]TurnRow{
		{Run: sqlc.ListWebmasterTurnsRow{ID: 9, Status: "running"}},
		{
			Run:          sqlc.ListWebmasterTurnsRow{ID: 7, Status: "kept", DurationMs: 300},
			Rounds:       1,
			PromptTokens: 40,
		},
		{
			Run:              sqlc.ListWebmasterTurnsRow{ID: 5, Status: "changed", DurationMs: 900},
			Rounds:           3,
			PromptTokens:     60,
			CompletionTokens: 20,
		},
	})
	if m.Finished != 2 || m.Turns[0].Run.ID != 5 || m.Turns[1].Run.ID != 7 {
		t.Fatalf("turns = %+v", m.Turns)
	}
	if m.MaxTokens != 80 || m.MaxDurationMs != 900 || m.TotalRounds != 4 || m.NewestTurnID != 9 {
		t.Errorf("metrics = %+v", m)
	}
}

// Stepping walks the list by id and stops at both ends. Following the newest
// is the top of the list, so it has no newer turn.
func TestTurnNeighboursStopsAtTheEnds(t *testing.T) {
	view := AdminView{Turns: []TurnRow{
		{Run: sqlc.ListWebmasterTurnsRow{ID: 9}},
		{Run: sqlc.ListWebmasterTurnsRow{ID: 7}},
		{Run: sqlc.ListWebmasterTurnsRow{ID: 5}},
	}}
	if older, newer := view.turnNeighbours(); older != 7 || newer != 0 {
		t.Errorf("newest: older = %d, newer = %d", older, newer)
	}
	view.Panel = Panel{Page: PageTurns, Turn: 7}
	if older, newer := view.turnNeighbours(); older != 5 || newer != 9 {
		t.Errorf("middle: older = %d, newer = %d", older, newer)
	}
	view.Panel.Turn = 5
	if older, newer := view.turnNeighbours(); older != 0 || newer != 7 {
		t.Errorf("oldest: older = %d, newer = %d", older, newer)
	}
	view.Panel.Turn = 3
	if older, newer := view.turnNeighbours(); older != 0 || newer != 0 {
		t.Errorf("a turn off the list: older = %d, newer = %d", older, newer)
	}
}

// A resampled round never reached the next request, so it adds nothing to
// what the next round carried.
func TestNewTurnDetailSkipsResampledRounds(t *testing.T) {
	run, messages := storedTurn()
	messages[2].Stats.Resampled = true
	d := NewTurnDetail(run, messages, 50)
	if d.Rounds[1].Carried != 0 {
		t.Errorf("round 2 carried %d, want 0 after a resampled round", d.Rounds[1].Carried)
	}
}

// The round in flight shows what it is doing and its calls as far as they
// have arrived, with string arguments unescaped.
func TestRunProgressLaysOutTheDraft(t *testing.T) {
	run := RunProgress{Round: 2, RoundSince: time.Now()}
	if got := run.LiveDraft(); got.Phase != "waiting for the model" || got.Round != 2 {
		t.Fatalf("draft before the first chunk = %+v", got)
	}

	run.Draft = &webmaster.Draft{Round: 2, Reasoning: "make it red"}
	if got := run.LiveDraft().Phase; got != "thinking" {
		t.Errorf("phase while reasoning = %q", got)
	}
	run.Draft = nil
	run.RoundDone = true
	if got := run.LiveDraft().Phase; got != "running tools" {
		t.Errorf("phase between rounds = %q", got)
	}
	run.RoundDone = false
	run.Draft = &webmaster.Draft{Round: 2, Reasoning: "make it red"}
	run.Draft.Calls = []webmaster.ToolCall{{
		ID:        "c1",
		Name:      "edit",
		Arguments: `{"target":"css","old":"","new":"h1{\n  color: re`,
	}}
	draft := run.LiveDraft()
	if draft.Phase != "writing edit" {
		t.Errorf("phase while calling = %q", draft.Phase)
	}
	if len(draft.Calls) != 1 {
		t.Fatalf("calls = %+v", draft.Calls)
	}
	args := draft.Calls[0].Args
	if len(args) != 3 || args[0].Value != "css" || args[2].Value != "h1{\n  color: re" ||
		!args[2].Block {
		t.Errorf("args = %+v, want the new text unescaped as far as it came", args)
	}
}

// Arguments can be cut anywhere. Every cut reads as the fields so far, never
// as raw JSON, and a half-sent escape is held back.
func TestStreamingArgsReadsEveryCut(t *testing.T) {
	whole := `{"target": "board", "old": "a\"b", "keys": [{"name":"x"}], "new": "line\none \u00e9"}`
	for cut := 0; cut <= len(whole); cut++ {
		for _, arg := range streamingArgs(whole[:cut]) {
			if arg.Key == "arguments" {
				t.Fatalf("cut %d read as raw: %q", cut, whole[:cut])
			}
			if strings.HasSuffix(arg.Value, "\\") {
				t.Fatalf("cut %d kept a half escape: %q", cut, arg.Value)
			}
		}
	}
	got := streamingArgs(whole)
	want := []string{"board", `a"b`, `[{"name":"x"}]`, "line\none é"}
	if len(got) != len(want) {
		t.Fatalf("args = %+v", got)
	}
	for i, value := range want {
		if got[i].Value != value {
			t.Errorf("arg %s = %q, want %q", got[i].Key, got[i].Value, value)
		}
	}
	if raw := streamingArgs(`[1,2`); len(raw) != 1 || raw[0].Key != "arguments" {
		t.Errorf("non-object arguments = %+v, want them raw", raw)
	}
	if bad := streamingArgs(`{"new":"a\qb"}`); len(bad) != 1 || bad[0].Value != `a\qb` {
		t.Errorf("broken escape = %+v, want the raw text", bad)
	}
}
