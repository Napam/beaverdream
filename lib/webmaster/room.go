package webmaster

import (
	"fmt"
	"strings"
)

// Turn is one thing that happened in the room: a visitor post, a webmaster
// turn, or a server event. ID is the conversation order.
type Turn struct {
	ID     int64
	Kind   string
	Author string
	Body   string
	// Said is the done reason, the webmaster's one log sentence. What it
	// posts on the board is a separate TurnReply row, with the text in Body.
	Said      string
	Changes   string
	Status    string
	VersionID int64
}

// Room is the conversation the webmaster reads before it answers again.
// Summary is the compacted history: one message standing in for everything
// older than Log. Notes are its own private memory, carried forward
// independently.
type Room struct {
	Summary string
	Log     []Turn
	Notes   string
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

	case TurnWebmaster:
		return renderWebmasterTurn(turn)

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
		b.WriteString(" [ended without done]")
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
	if before.HTML != after.HTML {
		parts = append(parts, "html")
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

// normalizeSchema keeps an empty schema equal to the stored default.
func normalizeSchema(schema string) string {
	if strings.TrimSpace(schema) == "" {
		return "{}"
	}
	return schema
}
