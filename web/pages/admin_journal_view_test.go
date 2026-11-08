package pages

import (
	"bytes"
	"context"
	"html"
	"regexp"
	"strings"
	"testing"
)

// decodeJSString reads one single-quoted JavaScript string literal the way a
// browser does, so a test can prove a quote stayed data.
func decodeJSString(t *testing.T, lit string) string {
	t.Helper()
	if len(lit) < 2 || lit[0] != '\'' || lit[len(lit)-1] != '\'' {
		t.Fatalf("%q is not one single-quoted literal", lit)
	}
	body := lit[1 : len(lit)-1]
	var b strings.Builder
	for i := 0; i < len(body); i++ {
		switch c := body[i]; c {
		case '\'':
			t.Fatalf("%q has a quote that ends the literal early", lit)
		case '\\':
			i++
			if i >= len(body) {
				t.Fatalf("%q ends in a backslash", lit)
			}
			switch e := body[i]; e {
			case '\\', '\'':
				b.WriteByte(e)
			case 'n':
				b.WriteByte('\n')
			case 'r':
				b.WriteByte('\r')
			case 'u':
				if i+4 >= len(body) {
					t.Fatalf("%q has a short unicode escape", lit)
				}
				switch body[i+1 : i+5] {
				case "2028":
					b.WriteRune('\u2028')
				case "2029":
					b.WriteRune('\u2029')
				default:
					t.Fatalf("%q has an unknown unicode escape", lit)
				}
				i += 4
			default:
				t.Fatalf("%q has an unknown escape \\%c", lit, e)
			}
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

func TestJSStringKeepsHostileTextInTheLiteral(t *testing.T) {
	for _, in := range []string{"moves", "a'b", `a\b`, "two\nlines", "l\u2028m"} {
		lit := jsString(in)
		if got := decodeJSString(t, lit); got != in {
			t.Errorf("jsString(%q) = %q, which decodes to %q", in, lit, got)
		}
	}
}

func TestClearConfirmsKeepTheKeyInTheLiteral(t *testing.T) {
	key := "x'); alert(1); //"
	for name, script := range map[string]string{
		"journal":  clearConfirm(key),
		"holdings": holdingsClearConfirm(key),
	} {
		if !strings.HasPrefix(script, "confirm(") || !strings.HasSuffix(script, ")") {
			t.Fatalf("%s = %q, want one confirm call", name, script)
		}
		arg := script[len("confirm(") : len(script)-1]
		if got := decodeJSString(t, arg); !strings.Contains(got, key) {
			t.Errorf("%s decodes to %q, which loses the key", name, got)
		}
	}
}

// renderedClearScripts is the card's onsubmit and its clear button's click,
// HTML-unescaped the way a browser hands them to the JS parser.
func renderedClearScripts(t *testing.T, page string) (onsubmit, click string) {
	t.Helper()
	for _, m := range regexp.MustCompile(`onsubmit="([^"]*)"`).FindAllStringSubmatch(page, -1) {
		onsubmit = html.UnescapeString(m[1])
	}
	for _, m := range regexp.MustCompile(`data-on:click="([^"]*)"`).FindAllStringSubmatch(page, -1) {
		js := html.UnescapeString(m[1])
		if strings.HasPrefix(js, "evt.preventDefault(); if (confirm(") {
			click = js
		}
	}
	return onsubmit, click
}

func assertKeyIsText(t *testing.T, name, script, key string) {
	t.Helper()
	if script == "" {
		t.Fatalf("the card has no %s", name)
	}
	if strings.Contains(script, key) {
		t.Errorf("%s keeps the key as syntax: %s", name, script)
	}
	if escaped := strings.ReplaceAll(key, "'", `\'`); !strings.Contains(script, escaped) {
		t.Errorf("%s does not carry the key as escaped text: %s", name, script)
	}
}

func TestJournalCardKeepsAHostileKeyInsideItsScripts(t *testing.T) {
	key := "x'); alert(1); //"
	keys := []JournalKeyView{{
		Key: key, Declared: true, Max: 10, Rules: 1, Match: 1, Rows: 1,
	}}
	var buf bytes.Buffer
	if err := AdminJournal(
		nil, nil, nil, keys, nil, Panel{Page: PageJournal}, "csrf",
	).Render(context.Background(), &buf); err != nil {
		t.Fatalf("render: %v", err)
	}
	onsubmit, click := renderedClearScripts(t, buf.String())
	assertKeyIsText(t, "onsubmit", onsubmit, key)
	assertKeyIsText(t, "clear click", click, key)
}

func TestHoldingCardKeepsAHostileKeyInsideItsScripts(t *testing.T) {
	key := "x'); alert(1); //"
	holdings := []HoldingView{{
		Key: key, Total: 1, Holders: 1,
		Top: []HolderView{{Visitor: "v", Name: "dave", Amount: 1}},
	}}
	var buf bytes.Buffer
	if err := AdminJournal(
		nil, holdings, nil, nil, nil, Panel{Page: PageJournal}, "csrf",
	).Render(context.Background(), &buf); err != nil {
		t.Fatalf("render: %v", err)
	}
	onsubmit, click := renderedClearScripts(t, buf.String())
	assertKeyIsText(t, "onsubmit", onsubmit, key)
	assertKeyIsText(t, "clear click", click, key)
}

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
