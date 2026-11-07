package pages

import "testing"

func TestFeedFillAndRulesNote(t *testing.T) {
	declared := JournalKeyView{
		Key:      "moves",
		Declared: true,
		Max:      1000,
		Rules:    3,
		Rows:     812,
		MinRules: 3,
		MaxRules: 3,
	}
	if got := feedFill(declared); got != "812 / 1000 rows" {
		t.Errorf("fill = %q", got)
	}
	if got := feedFillPercent(declared); got != 81 {
		t.Errorf("percent = %d, want 81", got)
	}
	if got := rulesNote(declared); got != "" {
		t.Errorf("a feed on current rules has the note %q", got)
	}

	declared.MinRules = 1
	if got := rulesNote(declared); got != "rows carry rules 1 to 3, the page is at 3" {
		t.Errorf("note = %q", got)
	}

	gone := JournalKeyView{Key: "old", Rows: 2}
	if got := feedFill(gone); got != "2 rows" {
		t.Errorf("an undeclared key fill = %q", got)
	}
	if got := feedFillPercent(gone); got != 0 {
		t.Errorf("an undeclared key percent = %d", got)
	}
}

// The selection is in the URL, so a link keeps it and the stream renders it.
func TestJournalPanelPathCarriesTheSelection(t *testing.T) {
	p := Panel{Page: PageJournal, Key: "moves", Match: 2, Visitor: "aaaaaaaaaaaaaaaa", Before: 40}
	if got, want := p.Path(), "/admin/journal?before=40&key=moves&match=2&visitor=aaaaaaaaaaaaaaaa"; got != want {
		t.Errorf("path = %q, want %q", got, want)
	}
	if got, want := p.LivePath(), "/admin/live?page=journal&before=40&key=moves&match=2&visitor=aaaaaaaaaaaaaaaa"; got != want {
		t.Errorf("live path = %q, want %q", got, want)
	}
	if got := (Panel{Page: PageJournal}).Path(); got != "/admin/journal" {
		t.Errorf("bare path = %q", got)
	}
}
