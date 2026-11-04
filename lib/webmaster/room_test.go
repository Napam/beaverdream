package webmaster

import (
	"fmt"
	"strings"
	"testing"
)

func TestRenderLogShowsTheWebmastersOwnTurns(t *testing.T) {
	log := []Turn{
		{ID: 1, Kind: TurnPost, Author: "dave", Body: "make it pink"},
		{
			ID: 2, Kind: TurnWebmaster, Said: "pink, as asked.",
			Changes: "stylesheet; html", VersionID: 2,
		},
		{ID: 3, Kind: TurnPost, Author: "kit", Body: "ugly"},
		{ID: 4, Kind: TurnWebmaster, Said: "kit is wrong.", Status: "kept"},
	}

	got := RenderLog(log, nil)
	for _, want := range []string{
		`#1 dave: make it pink`,
		`#2 you (v2): "pink, as asked." [stylesheet; html]`,
		`#3 kit: ugly`,
		`#4 you: "kit is wrong." [kept the page]`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("room log is missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "<") {
		t.Errorf("the room log carries markup:\n%s", got)
	}
}

func TestRenderLogSkipsTheTriggerPosts(t *testing.T) {
	log := []Turn{
		{ID: 1, Kind: TurnPost, Author: "dave", Body: "old"},
		{ID: 2, Kind: TurnPost, Author: "kit", Body: "the new one"},
	}

	got := RenderLog(log, map[int64]bool{2: true})
	if strings.Contains(got, "the new one") {
		t.Errorf("trigger post is repeated in the room log:\n%s", got)
	}
	if !strings.Contains(got, "#1 dave: old") {
		t.Errorf("older post disappeared:\n%s", got)
	}
}

func TestRenderLogBudgetDropsOldestFirst(t *testing.T) {
	count := roomLogBudget/200 + 50
	log := make([]Turn, 0, count)
	for i := 1; i <= count; i++ {
		log = append(log, Turn{
			ID:     int64(i),
			Kind:   TurnPost,
			Author: "dave",
			Body:   strings.Repeat("x", 400),
		})
	}

	got := RenderLog(log, nil)
	if len([]rune(got)) > roomLogBudget+500 {
		t.Fatalf("room log is over budget: %d runes", len([]rune(got)))
	}
	if strings.Contains(got, "#1 dave") {
		t.Error("the oldest line survived the budget")
	}
	if !strings.Contains(got, fmt.Sprintf("#%d dave", count)) {
		t.Error("the newest line was dropped")
	}
}

func TestDescribeChangesNamesFieldsNotMarkup(t *testing.T) {
	before := Site{
		CSS:  "old",
		HTML: "<h1>old</h1>" + PostsPlaceholder,
	}
	after := Site{
		CSS:  "new",
		HTML: "<h1>old</h1>" + PostsPlaceholder,
	}

	got := DescribeChanges(before, after)
	if got != "stylesheet" {
		t.Errorf("summary = %q, want stylesheet", got)
	}
	if strings.Contains(got, "<") {
		t.Errorf("summary carries markup: %s", got)
	}
}

func TestRenderLogLabelsBrowserErrors(t *testing.T) {
	got := RenderLog([]Turn{{
		ID:   3,
		Kind: TurnAlert,
		Body: "reduce is not a function",
	}}, nil)
	want := "#3 browser (reported by a visitor's page): reduce is not a function"
	if !strings.Contains(got, want) {
		t.Fatalf("log = %q, want a line containing %q", got, want)
	}
}
