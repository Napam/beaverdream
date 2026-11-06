package pages

import (
	"sort"
	"strings"
)

// JournalKeyView is one journal key as the journal page lists it: the stats
// of the rows it holds and, when the page declares it, its trim and rules.
// A key that is no longer declared still shows while it has rows.
type JournalKeyView struct {
	Key      string
	Declared bool
	// Max and Rules are the declared trim and rules number. Zero when the key
	// is not declared.
	Max   int
	Rules int
	Match int
	Rows  int

	FirstID, LastID int64
	FirstAt, LastAt string
	Visitors        int
	PayloadBytes    int64
	LargestPayload  int64
	// MinRules and MaxRules are the range of rules numbers the stored rows
	// carry, so rows left under older rules are visible.
	MinRules, MaxRules int
	OlderMatchRows     int
	RecentRows         int
}

// JournalMatchView is one match a key holds and how many rows it has.
type JournalMatchView struct {
	Match int64
	Rows  int
}

// JournalRowView is one stored row. The payload is opaque text written by the
// page, so it is only ever shown as text.
type JournalRowView struct {
	ID      int64
	Visitor string
	Match   int64
	Rules   int64
	At      string
	Payload string
}

// JournalBrowse is one page of rows of the picked key, newest first, under
// the picked filters. OlderBefore is the id to page back from, or 0 when this
// is the last page.
type JournalBrowse struct {
	Key         string
	Match       int64
	Visitor     string
	Before      int64
	Matches     []JournalMatchView
	Rows        []JournalRowView
	OlderBefore int64
}

// SortJournalKeys orders keys by name, so the cards do not move.
func SortJournalKeys(keys []JournalKeyView) {
	sort.Slice(keys, func(i, j int) bool { return keys[i].Key < keys[j].Key })
}

// journalPath is the journal page with a selection.
func journalPath(feed string, match int64, visitor string, before int64) string {
	return Panel{
		Page:    PageJournal,
		Feed:    feed,
		Match:   match,
		Visitor: visitor,
		Before:  before,
	}.Path()
}

// feedFill is how full a key is against its trim, as "812 / 1000". An
// undeclared key has no trim.
func feedFill(k JournalKeyView) string {
	if k.Max == 0 {
		return plural(k.Rows, "row")
	}
	return itoa(k.Rows) + " / " + itoa(k.Max) + " rows"
}

// feedFillPercent is the width of the fill bar, from 0 to 100.
func feedFillPercent(k JournalKeyView) int {
	if k.Max == 0 {
		return 0
	}
	return min(100, k.Rows*100/k.Max)
}

// rulesNote says when stored rows carry other rules than the page declares:
// they are ignored by the page's code and only a clear removes them early.
func rulesNote(k JournalKeyView) string {
	if k.Rows == 0 || !k.Declared || (k.MinRules == k.Rules && k.MaxRules == k.Rules) {
		return ""
	}
	if k.MinRules == k.MaxRules {
		return "rows carry rules " + itoa(k.MinRules) + ", the page is at " + itoa(k.Rules)
	}
	return "rows carry rules " + itoa(k.MinRules) + " to " + itoa(k.MaxRules) +
		", the page is at " + itoa(k.Rules)
}

// payloadSize is a byte count as the stats show it.
func payloadSize(n int64) string {
	return short(n) + " B"
}

// clearConfirm asks before one key is emptied.
func clearConfirm(key string) string {
	return "confirm('Delete every row of " + key +
		"? Open pages start it over. This cannot be undone.')"
}

// clearAction is where a key's clear form posts. The key is a lowercase name,
// so it is safe in a path and a script string.
func clearAction(key string, p Panel) string {
	return "/admin/journal/" + key + "/clear" + p.query()
}

// payloadText shows a payload on one block. Hostile text is safe: templ
// escapes it, and nothing here reaches innerHTML.
func payloadText(raw string) string {
	return strings.TrimSpace(raw)
}

// holderPath keeps the page's feed selection and picks a visitor, or drops
// the pick when visitor is empty.
func holderPath(p Panel, visitor string) string {
	p.Page = PageJournal
	p.Visitor = visitor
	p.Before = 0
	return p.Path()
}

func holdingsClearConfirm(key string) string {
	return "confirm('Delete what every visitor holds of " + key +
		"? Everyone starts at 0. This cannot be undone.')"
}

// holdingsClearAction is where a visitor counter's clear form posts. The key
// is a lowercase name, so it is safe in a path and a script string.
func holdingsClearAction(key string, p Panel) string {
	return "/admin/state/" + key + "/clear" + p.query()
}

func signed(n int64) string {
	if n > 0 {
		return "+" + i64(n)
	}
	return i64(n)
}

func deltaClass(n int64) string {
	if n < 0 {
		return "text-red-700 dark:text-red-400"
	}
	return "text-emerald-700 dark:text-emerald-400"
}
