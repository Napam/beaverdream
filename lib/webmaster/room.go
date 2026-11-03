package webmaster

import (
	"fmt"
	"strings"
)

// Turn is one thing that happened in the room: a visitor post, a webmaster
// turn, or a server event. ID is the conversation order.
type Turn struct {
	ID        int64
	Kind      string
	Author    string
	Body      string
	Said      string
	Changes   string
	Status    string
	VersionID int64
}

// Turn kinds. Posts and webmaster turns alternate; the rest are events the
// server writes so the webmaster's memory matches the page.
const (
	TurnPost      = "post"
	TurnWebmaster = "webmaster"
	TurnHeal      = "heal"
	TurnUndo      = "undo"
	TurnRestore   = "restore"
)

// Room is the conversation the webmaster reads before it answers again.
// Summary is the compacted history: one message standing in for everything
// older than Log. Notes are its own private memory, carried forward
// independently.
type Room struct {
	Summary string
	Log     []Turn
	Notes   string
}

// roomLogBudget bounds the room log in runes. It matches the 128k-token
// compaction threshold at four runes per token. The oldest lines are dropped
// first: the newest conversation is what the next turn has to continue.
const roomLogBudget = 512_000

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

	case TurnWebmaster:
		return renderWebmasterTurn(turn)

	case TurnHeal, TurnUndo, TurnRestore:
		return fmt.Sprintf("#%d server: %s", turn.ID, turn.Body)
	}
	return ""
}

func renderWebmasterTurn(turn Turn) string {
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
		b.WriteString(" [said nothing]")
	default:
		b.WriteString(" [no visible change]")
	}
	return b.String()
}

// DescribeChanges names what a turn changed, for its line in the room log. It
// reports fields, never markup: the log explains the page, the page is the page.
func DescribeChanges(before, after Site) string {
	var parts []string
	if before.CSS != after.CSS {
		parts = append(parts, "stylesheet")
	}
	if before.Header != after.Header {
		parts = append(parts, "header")
	}
	if before.Board != after.Board {
		parts = append(parts, "board")
	}
	if before.Footer != after.Footer {
		parts = append(parts, "footer")
	}
	if normalizeSchema(before.StateSchema) != normalizeSchema(after.StateSchema) {
		parts = append(parts, "shared state")
	}

	if len(parts) == 0 {
		return ""
	}
	return clip(strings.Join(parts, "; "), 400)
}

// normalizeSchema keeps an empty schema equal to the stored default.
func normalizeSchema(schema string) string {
	if strings.TrimSpace(schema) == "" {
		return "{}"
	}
	return schema
}
