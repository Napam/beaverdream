package webmaster

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// Turn is one thing that happened in the room: a visitor post, a webmaster
// turn, or a server event. ID is the conversation order.
type Turn struct {
	ID     int64
	Kind   string
	Author string
	Body   string
	// Said is the done reason, the webmaster's one log sentence. What it
	// posts in the room is a separate TurnReply row, with the text in Body.
	Said      string
	Changes   string
	Status    string
	VersionID int64
}

// Room is the conversation the webmaster reads before it answers again.
// Summary is the compacted history: one message standing in for everything
// older than Log. Notes are its own private memory, carried forward
// independently. Play is what visitors did on the current version.
type Room struct {
	Summary string
	Log     []Turn
	Notes   string
	Play    *PlayReport
}

// PlayCounter is one line of a version's play report: which kind of event,
// which key, how many times.
type PlayCounter struct {
	Kind string
	Key  string
	N    int64
}

// PlayReport is what happened to the version the model shipped. The model
// cannot see a browser console or a click, so the next prompt tells it how the
// page was used: loads, visitors, journal writes and refusals, state ops, and
// the alerts the page reported.
type PlayReport struct {
	Version  int64
	Age      time.Duration
	Visitors int64
	Counters []PlayCounter
}

// RenderLog writes the room as plain text. Nothing here is markup: the page
// stays in the CURRENT sections, and full tool calls stay in the admin view.
// triggerIDs are shown in the NEW POST section instead, so they are skipped.
func RenderLog(log []Turn, triggerIDs map[int64]bool) string {
	lines := logLines(log, triggerIDs)

	total := 0
	first := len(lines)
	for i := len(lines) - 1; i >= 0; i-- {
		total += len([]rune(lines[i])) + 1
		if total > roomLogBudget {
			break
		}
		first = i
	}
	return strings.Join(lines[first:], "\n")
}

// LogRunes counts what RenderLog would emit before its budget, so the caller
// can decide whether the uncompacted history is big enough to compact.
func LogRunes(log []Turn, triggerIDs map[int64]bool) int {
	total := 0
	for _, line := range logLines(log, triggerIDs) {
		total += len([]rune(line)) + 1
	}
	return total
}

func logLines(log []Turn, triggerIDs map[int64]bool) []string {
	lines := make([]string, 0, len(log))
	for _, turn := range log {
		if turn.Kind == TurnPost && triggerIDs[turn.ID] {
			continue
		}
		line := renderTurn(turn)
		if line == "" {
			continue
		}
		lines = append(lines, line)
	}
	return lines
}

func renderTurn(turn Turn) string {
	switch turn.Kind {
	case TurnPost:
		author := turn.Author
		if author == "" {
			author = "anon"
		}
		return fmt.Sprintf("#%d %s: %s", turn.ID, author, clip(turn.Body, historyPostLen))

	case TurnChangeset:
		return renderChangesetTurn(turn)

	case TurnReply:
		// Kept whole: a follow-up question needs the full answer it follows.
		return fmt.Sprintf("#%d you replied: %q", turn.ID, clip(turn.Body, maxReplyLen))

	case TurnHeal, TurnUndo, TurnRestore, TurnJournal, TurnState:
		return fmt.Sprintf("#%d server: %s", turn.ID, turn.Body)

	case TurnAlert:
		return fmt.Sprintf(
			"#%d browser (reported by a visitor's page): %s",
			turn.ID,
			clip(turn.Body, 200),
		)
	}
	return ""
}

func renderChangesetTurn(turn Turn) string {
	var b strings.Builder
	fmt.Fprintf(&b, "#%d you", turn.ID)
	if turn.VersionID > 0 {
		fmt.Fprintf(&b, " (v%d)", turn.VersionID)
	}
	if body := strings.TrimSpace(turn.Body); body != "" {
		fmt.Fprintf(&b, ", answering admin: %q", clip(body, historyPostLen))
	}
	b.WriteString(": ")

	said := strings.TrimSpace(turn.Said)
	if said == "" {
		said = "(no words)"
	}
	fmt.Fprintf(&b, "%q", said)

	switch {
	case turn.Changes != "":
		fmt.Fprintf(&b, " [%s]", turn.Changes)
	case turn.Status == "kept":
		b.WriteString(" [kept the page]")
	case turn.Status == "silent":
		b.WriteString(" [ended without done]")
	default:
		b.WriteString(" [no visible change]")
	}
	return b.String()
}

// DescribeChanges names what a turn changed, for its line in the room log. It
// reports fields, never markup: the log explains the page, the page is the page.
// A changed field carries its byte delta, so a model that ballooned the html
// reads the size it wrote.
func DescribeChanges(before, after Site) string {
	var parts []string
	if before.CSS != after.CSS {
		parts = append(parts, fieldDelta("stylesheet", len(before.CSS), len(after.CSS)))
	}
	if before.HTML != after.HTML {
		parts = append(parts, fieldDelta("html", len(before.HTML), len(after.HTML)))
	}
	if normalizeSchema(before.StateSchema) != normalizeSchema(after.StateSchema) {
		parts = append(parts, "shared state")
	}
	if normalizeSchema(before.JournalSchema) != normalizeSchema(after.JournalSchema) {
		parts = append(parts, "journal")
	}

	if len(parts) == 0 {
		return ""
	}
	return clip(strings.Join(parts, "; "), 400)
}

// fieldDelta names a changed field and how many bytes it grew or shrank. An
// unchanged size keeps the bare name.
func fieldDelta(name string, before, after int) string {
	if before == after {
		return name
	}
	return fmt.Sprintf("%s %+d bytes", name, after-before)
}

// RenderPlay writes the play report as plain text, at most ten lines. It says
// what happened, never what it means: the model draws its own conclusion.
func RenderPlay(play *PlayReport) string {
	if play == nil {
		return ""
	}
	var lines []string
	lines = append(lines, fmt.Sprintf(
		"PLAY ON THIS VERSION (v%d, shipped %s)",
		play.Version,
		playAge(play.Age),
	))
	if loads := playKindTotal(play.Counters, statKindLoad); loads > 0 || play.Visitors > 0 {
		lines = append(lines, fmt.Sprintf(
			"%s, %s",
			countWord(loads, "load"),
			countWord(play.Visitors, "visitor"),
		))
	}
	if line := playKindLine(play.Counters, statKindJournal); line != "" {
		lines = append(lines, "journal "+line)
	}
	if line := playKindLine(play.Counters, statKindState); line != "" {
		lines = append(lines, "state "+line)
	}
	if line := playAlertLine(play.Counters); line != "" {
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

// The counter kinds the prompt reads. The handlers write these words into
// version_stats; a kind the prompt does not know is simply not shown.
const (
	statKindLoad    = "load"
	statKindJournal = "journal"
	statKindState   = "state"
	statKindAlert   = "alert"
)

// playBase groups one journal or state key: how many ops landed and why the
// rest were refused.
type playBase struct {
	key     string
	ok      int64
	refused map[string]int64
}

// playKindLine is one line for journal or state: the busiest keys with their
// refusals, keyed by the reason the handler already gave the page.
func playKindLine(counters []PlayCounter, kind string) string {
	bases := make(map[string]*playBase)
	base := func(key string) *playBase {
		if bases[key] == nil {
			bases[key] = &playBase{key: key, refused: make(map[string]int64)}
		}
		return bases[key]
	}
	for _, counter := range counters {
		switch counter.Kind {
		case kind:
			base(counter.Key).ok += counter.N
		case kind + "_refused":
			key, reason, ok := strings.Cut(counter.Key, ":")
			if !ok {
				key, reason = counter.Key, "refused"
			}
			base(key).refused[reason] += counter.N
		}
	}
	if len(bases) == 0 {
		return ""
	}

	list := make([]*playBase, 0, len(bases))
	for _, item := range bases {
		list = append(list, item)
	}
	sort.Slice(list, func(i, j int) bool {
		left, right := list[i], list[j]
		if left.total() != right.total() {
			return left.total() > right.total()
		}
		return left.key < right.key
	})
	if len(list) > 3 {
		list = list[:3]
	}

	segments := make([]string, 0, len(list))
	for _, item := range list {
		var parts []string
		if item.ok > 0 {
			parts = append(parts, fmt.Sprintf("%d ok", item.ok))
		}
		if refusals := item.refusedTotal(); refusals > 0 {
			parts = append(parts, fmt.Sprintf(
				"%d refused (%s)",
				refusals,
				topReasons(item.refused, 3),
			))
		}
		if len(parts) == 0 {
			continue
		}
		segments = append(segments, item.key+" "+strings.Join(parts, ", "))
	}
	return strings.Join(segments, "; ")
}

func (b *playBase) total() int64 { return b.ok + b.refusedTotal() }

func (b *playBase) refusedTotal() int64 {
	total := int64(0)
	for _, n := range b.refused {
		total += n
	}
	return total
}

// topReasons writes the busiest refusal reasons as "slow 12, cap 3".
func topReasons(reasons map[string]int64, limit int) string {
	type reasonCount struct {
		reason string
		n      int64
	}
	list := make([]reasonCount, 0, len(reasons))
	for reason, n := range reasons {
		list = append(list, reasonCount{reason, n})
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].n != list[j].n {
			return list[i].n > list[j].n
		}
		return list[i].reason < list[j].reason
	})
	if len(list) > limit {
		list = list[:limit]
	}
	parts := make([]string, 0, len(list))
	for _, item := range list {
		parts = append(parts, fmt.Sprintf("%s %d", item.reason, item.n))
	}
	return strings.Join(parts, ", ")
}

// playAlertLine is the browser errors the current page reported, busiest
// first. The total counts every alert, not just the three shown.
func playAlertLine(counters []PlayCounter) string {
	total := int64(0)
	alerts := make([]PlayCounter, 0)
	for _, counter := range counters {
		if counter.Kind != statKindAlert {
			continue
		}
		total += counter.N
		alerts = append(alerts, counter)
	}
	if total == 0 {
		return ""
	}
	sort.Slice(alerts, func(i, j int) bool {
		if alerts[i].N != alerts[j].N {
			return alerts[i].N > alerts[j].N
		}
		return alerts[i].Key < alerts[j].Key
	})
	if len(alerts) > 3 {
		alerts = alerts[:3]
	}
	parts := make([]string, 0, len(alerts))
	for _, alert := range alerts {
		parts = append(parts, fmt.Sprintf("%s (%d)", alert.Key, alert.N))
	}
	word := "alerts"
	if total == 1 {
		word = "alert"
	}
	return fmt.Sprintf("%s %d: %s", word, total, strings.Join(parts, ", "))
}

func playKindTotal(counters []PlayCounter, kind string) int64 {
	total := int64(0)
	for _, counter := range counters {
		if counter.Kind == kind {
			total += counter.N
		}
	}
	return total
}

// countWord writes one event or many: "1 load", "42 loads".
func countWord(n int64, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return fmt.Sprintf("%d %ss", n, word)
}

// playAge keeps the report's timestamp short and relative.
func playAge(age time.Duration) string {
	switch {
	case age < time.Minute:
		return "just now"
	case age < time.Hour:
		return fmt.Sprintf("%dm ago", int(age.Minutes()))
	case age < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(age.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(age.Hours()/24))
	}
}

// normalizeSchema keeps an empty schema equal to the stored default.
func normalizeSchema(schema string) string {
	if strings.TrimSpace(schema) == "" {
		return "{}"
	}
	return schema
}
