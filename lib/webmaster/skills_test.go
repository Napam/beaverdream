package webmaster

import (
	"strings"
	"testing"

	"github.com/Napam/beaverdream/lib/webmaster/skills"
)

func TestLoadSkillReturnsTheBodyOnce(t *testing.T) {
	ed := newEditor(Site{HTML: "<main>{{posts}}</main>"})

	if got := ed.run("load_skill", `{"name":"controls"}`); got != body(t, "controls") {
		t.Fatalf("first load returned %.60q, want the controls body", got)
	}
	got := ed.run("load_skill", `{"name":"controls"}`)
	if !strings.HasPrefix(got, "ok: controls was already loaded") {
		t.Errorf("second load = %q, want a pointer to the first", got)
	}
	if ed.dirty || ed.rejected != 0 || ed.done {
		t.Errorf("a skill load changed the editor: dirty %v rejected %d done %v",
			ed.dirty, ed.rejected, ed.done)
	}
}

func TestLoadSkillRejectsAnUnknownName(t *testing.T) {
	ed := newEditor(Site{})
	ed.run("load_skill", `{"name":"sokoban"}`)
	if ed.rejected != 1 {
		t.Errorf("rejected = %d, want 1", ed.rejected)
	}
}

func TestLoadSkillBrokenArgumentsCountAsBroken(t *testing.T) {
	ed := newEditor(Site{})
	ed.run("load_skill", `{"name":`)
	if ed.badArguments != 1 {
		t.Errorf("badArguments = %d, want 1", ed.badArguments)
	}
}

// A round that only loads a skill is not the end of the turn. The skill's
// body is the tool result the next round builds from.
func TestRunLoadsASkillBeforeDone(t *testing.T) {
	res := runChangesetWith(t, Site{HTML: "<main>{{posts}}</main>"}, [][]fakeToolCall{
		{{name: "load_skill", args: `{"name":"controls"}`}},
		{{name: "done", args: `{"reason":"read the reference first"}`}},
	})
	if !res.Done || res.DoneRound != 2 {
		t.Fatalf("done = %v in round %d, want done in round 2", res.Done, res.DoneRound)
	}
	if len(res.Rounds) != 2 {
		t.Errorf("rounds = %d, want 2", len(res.Rounds))
	}
	first := res.Messages[2]
	if len(first.ToolCalls) != 1 || first.ToolCalls[0].Name != "load_skill" {
		t.Fatalf("round 1 calls = %+v, want load_skill", first.ToolCalls)
	}
	if first.ToolCalls[0].Result != body(t, "controls") {
		t.Error("the skill round did not return the controls body")
	}
	if res.Changed {
		t.Error("a skill load alone changed the page")
	}
}

func TestLoadSkillSchemaListsTheSkills(t *testing.T) {
	for _, tool := range toolParams() {
		if tool.OfFunction == nil || tool.OfFunction.Function.Name != "load_skill" {
			continue
		}
		props := tool.OfFunction.Function.Parameters["properties"].(map[string]any)
		name := props["name"].(map[string]any)
		if got := name["enum"].([]string); len(got) != len(skills.All) {
			t.Errorf("enum = %v, want the registry names", got)
		}
		return
	}
	t.Fatal("toolParams has no load_skill")
}

// Several skills load in one round, each with its own body.
func TestRunLoadsSeveralSkillsInOneRound(t *testing.T) {
	res := runChangesetWith(t, Site{HTML: "<main>{{posts}}</main>"}, [][]fakeToolCall{
		{
			{name: "load_skill", args: `{"name":"controls"}`},
			{name: "load_skill", args: `{"name":"feel"}`},
			{name: "load_skill", args: `{"name":"entities"}`},
		},
		{{name: "done", args: `{"reason":"read the references first"}`}},
	})
	if !res.Done || res.DoneRound != 2 {
		t.Fatalf("done = %v in round %d, want done in round 2", res.Done, res.DoneRound)
	}
	calls := res.Messages[2].ToolCalls
	want := []string{body(t, "controls"), body(t, "feel"), body(t, "entities")}
	if len(calls) != len(want) {
		t.Fatalf("round 1 calls = %d, want %d", len(calls), len(want))
	}
	for i, call := range calls {
		if call.Result != want[i] {
			t.Errorf("load %d returned %.40q", i, call.Result)
		}
	}
}

func body(t *testing.T, name string) string {
	t.Helper()
	b, ok := skills.Body(name)
	if !ok {
		t.Fatalf("no skill %q", name)
	}
	return b
}
