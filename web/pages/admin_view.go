package pages

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Napam/beaverdream/db/generated/sqlc"
	"github.com/Napam/beaverdream/lib/webmaster"
)

// runesPerToken is the estimate the server uses for compaction, so the panel
// estimates the same way.
const runesPerToken = 4

// TurnRow is one webmaster turn as the list and the charts read it: the stored
// row without its conversation, plus sums over its rounds.
type TurnRow struct {
	Run sqlc.ListWebmasterTurnsRow
	// Trigger is what the turn answered: the admin instruction, or the new
	// posts as the prompt showed them.
	Trigger          string
	Rounds           int
	PromptTokens     int64
	CachedTokens     int64
	CompletionTokens int64
	Rejected         int
}

// NewTurnRow sums a turn's rounds. The conversation is dropped from the row,
// so a list of rows stays small.
func NewTurnRow(run sqlc.ListWebmasterTurnsRow, messages []webmaster.Message) TurnRow {
	run.Messages = ""
	row := TurnRow{Run: run, Trigger: run.Body}
	for _, message := range messages {
		if message.Role == "user" && row.Trigger == "" {
			row.Trigger = newPostText(message)
		}
		if message.Stats == nil {
			continue
		}
		row.Rounds++
		row.PromptTokens += message.Stats.PromptTokens
		row.CachedTokens += message.Stats.CachedTokens
		row.CompletionTokens += message.Stats.CompletionTokens
		row.Rejected += message.Stats.Rejected
	}
	return row
}

// newPostText is the body of the prompt's new post section without its
// heading and closing line.
func newPostText(message webmaster.Message) string {
	for _, section := range message.Sections {
		if section.Name != "new post" || section.End > len(message.Content) {
			continue
		}
		lines := strings.Split(strings.TrimSpace(message.Content[section.Start:section.End]), "\n")
		if len(lines) > 2 {
			lines = lines[1 : len(lines)-1]
		}
		return strings.Join(lines, "\n")
	}
	return ""
}

// ContextPart is one piece of what a request carried: the system prompt, the
// tool definitions, or one section of the user prompt.
type ContextPart struct {
	Name  string
	Text  string
	Runes int
	Note  string
}

// RoundView is one completion: what the model returned and what each call did.
// Base and Carried estimate the request in runes: the system prompt, tools and
// user prompt every round sends, and the earlier replies and tool results the
// loop appended before this round.
type RoundView struct {
	Stats     webmaster.RoundStats
	Content   string
	Reasoning string
	Calls     []CallView
	Base      int
	Carried   int
}

// CallView is one tool call with its arguments split into fields, so long
// strings like CSS show with their line breaks.
type CallView struct {
	Name     string
	ID       string
	Args     []ArgView
	Result   string
	Rejected bool
	Deferred bool
}

// ArgView is one argument. Block marks a string worth its own box.
type ArgView struct {
	Key   string
	Value string
	Block bool
}

// TurnDetail is everything the inspector shows for one turn.
type TurnDetail struct {
	Row     TurnRow
	Context []ContextPart
	Rounds  []RoundView
	// Stored is false for a turn with no conversation: still running, or
	// closed before it finished.
	Stored bool
	// Sectioned is false for an older turn whose prompt carries no sections.
	Sectioned bool
}

// RunProgress is a turn still in flight. Its conversation is stored when the
// turn ends, so the inspector shows the rounds the server has finished from
// memory, and the stored version replaces them when it lands.
type RunProgress struct {
	TurnID  int64
	Round   int
	Since   time.Time
	Authors []string
	Rounds  []webmaster.RoundStats
}

// LiveRounds lays the finished rounds out as the inspector's cards. Only the
// round stats and their clipped outcomes exist before the turn is stored, so
// each call shows its name, result and rejection tone but no arguments.
func (r RunProgress) LiveRounds() []RoundView {
	rounds := make([]RoundView, 0, len(r.Rounds))
	for _, stats := range r.Rounds {
		round := RoundView{Stats: stats}
		for _, line := range stats.Outcomes {
			name, result, ok := strings.Cut(line, ": ")
			if !ok {
				name, result = "", line
			}
			round.Calls = append(round.Calls, CallView{
				Name:     name,
				Result:   result,
				Rejected: strings.HasPrefix(result, "rejected:"),
				Deferred: strings.HasPrefix(result, "not finished:"),
			})
		}
		rounds = append(rounds, round)
	}
	return rounds
}

// NewTurnDetail lays out a turn's conversation for the inspector. toolsRunes
// is the size of the tool definitions this build sends; older turns may have
// sent different ones.
func NewTurnDetail(
	run sqlc.ListWebmasterTurnsRow,
	messages []webmaster.Message,
	toolsRunes int,
) TurnDetail {
	detail := TurnDetail{Row: NewTurnRow(run, messages), Stored: len(messages) > 0}
	base := 0
	carried := 0
	for _, message := range messages {
		switch message.Role {
		case "system":
			detail.Context = append(detail.Context,
				ContextPart{
					Name:  "system prompt",
					Text:  message.Content,
					Runes: utf8.RuneCountInString(message.Content),
				},
				ContextPart{
					Name:  "tool definitions",
					Runes: toolsRunes,
					Note:  "as this build sends them",
				},
			)
			base += utf8.RuneCountInString(message.Content) + toolsRunes
		case "user":
			parts, sectioned := userParts(message)
			detail.Context = append(detail.Context, parts...)
			detail.Sectioned = sectioned
			base += utf8.RuneCountInString(message.Content)
		case "assistant":
			round := RoundView{
				Content:   message.Content,
				Reasoning: message.Reasoning,
				Base:      base,
				Carried:   carried,
			}
			if message.Stats != nil {
				round.Stats = *message.Stats
			}
			// The next round carries this reply, its reasoning when the
			// provider returned one, and every result. A resampled round
			// never reached the next request at all.
			replayed := message.Stats == nil || !message.Stats.Resampled
			if replayed {
				carried += utf8.RuneCountInString(message.Content) +
					utf8.RuneCountInString(message.Reasoning)
			}
			for _, call := range message.ToolCalls {
				round.Calls = append(round.Calls, CallView{
					Name:     call.Name,
					ID:       call.ID,
					Args:     splitArgs(call.Arguments),
					Result:   call.Result,
					Rejected: strings.HasPrefix(call.Result, "rejected:"),
					Deferred: strings.HasPrefix(call.Result, "not finished:"),
				})
				if replayed {
					carried += utf8.RuneCountInString(call.Name) +
						utf8.RuneCountInString(call.Arguments) +
						utf8.RuneCountInString(call.Result)
				}
			}
			detail.Rounds = append(detail.Rounds, round)
		}
	}
	return detail
}

// userParts splits the user prompt by its recorded sections. A prompt without
// them, or with offsets that do not fit, is one part.
func userParts(message webmaster.Message) ([]ContextPart, bool) {
	content := message.Content
	at := 0
	parts := make([]ContextPart, 0, len(message.Sections))
	for _, section := range message.Sections {
		if section.Start != at || section.End < section.Start || section.End > len(content) {
			parts = nil
			break
		}
		text := content[section.Start:section.End]
		parts = append(parts, ContextPart{
			Name:  section.Name,
			Text:  text,
			Runes: utf8.RuneCountInString(text),
		})
		at = section.End
	}
	if len(parts) == 0 || at != len(content) {
		return []ContextPart{{
			Name:  "user prompt",
			Text:  content,
			Runes: utf8.RuneCountInString(content),
		}}, false
	}
	return parts, true
}

// splitArgs reads a tool call's JSON arguments in their written order. Strings
// are unquoted; anything else stays compact JSON. Arguments that are not a
// JSON object come back as one raw field.
func splitArgs(raw string) []ArgView {
	dec := json.NewDecoder(strings.NewReader(raw))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return []ArgView{{Key: "arguments", Value: raw, Block: true}}
	}
	var args []ArgView
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return []ArgView{{Key: "arguments", Value: raw, Block: true}}
		}
		key, _ := tok.(string)
		var value json.RawMessage
		if err := dec.Decode(&value); err != nil {
			return []ArgView{{Key: "arguments", Value: raw, Block: true}}
		}
		var text string
		if err := json.Unmarshal(value, &text); err == nil {
			args = append(args, ArgView{
				Key:   key,
				Value: text,
				Block: strings.Contains(text, "\n") || len(text) > 80,
			})
			continue
		}
		var pretty bytes.Buffer
		if err := json.Indent(&pretty, value, "", "  "); err != nil {
			pretty.Reset()
			pretty.Write(value)
		}
		args = append(args, ArgView{
			Key:   key,
			Value: pretty.String(),
			Block: pretty.Len() > 80 || bytes.Contains(pretty.Bytes(), []byte("\n")),
		})
	}
	return args
}

// ContextRunes is the size of everything round one sent.
func (d TurnDetail) ContextRunes() int {
	total := 0
	for _, part := range d.Context {
		total += part.Runes
	}
	return total
}

// MaxInput is the largest estimated request across the rounds, the scale for
// the growth bars.
func (d TurnDetail) MaxInput() int {
	largest := 0
	for _, round := range d.Rounds {
		largest = max(largest, round.Base+round.Carried)
	}
	return largest
}

// MetricsView is the recent turns, oldest first, with the scales and totals
// the charts need.
type MetricsView struct {
	Turns          []TurnRow
	MaxTokens      int64
	MaxDurationMs  int64
	PromptTokens   int64
	CachedTokens   int64
	OutputTokens   int64
	TotalMs        int64
	TotalRounds    int
	Finished       int
	StatusCounts   map[string]int
	RejectedCalls  int
	NewestTurnID   int64
	MaxRoundsCount int
}

// NewMetrics reads the newest-first list the history shows and turns it into
// chart order. A running turn has nothing to count yet.
func NewMetrics(newestFirst []TurnRow) MetricsView {
	m := MetricsView{StatusCounts: map[string]int{}}
	for i := len(newestFirst) - 1; i >= 0; i-- {
		row := newestFirst[i]
		if row.Run.Status == "running" {
			continue
		}
		m.Turns = append(m.Turns, row)
		m.MaxTokens = max(m.MaxTokens, row.PromptTokens+row.CompletionTokens)
		m.MaxDurationMs = max(m.MaxDurationMs, row.Run.DurationMs)
		m.PromptTokens += row.PromptTokens
		m.CachedTokens += row.CachedTokens
		m.OutputTokens += row.CompletionTokens
		m.TotalMs += row.Run.DurationMs
		m.TotalRounds += row.Rounds
		m.RejectedCalls += row.Rejected
		m.StatusCounts[row.Run.Status]++
		m.MaxRoundsCount = max(m.MaxRoundsCount, row.Rounds)
		m.Finished++
	}
	if len(newestFirst) > 0 {
		m.NewestTurnID = newestFirst[0].Run.ID
	}
	return m
}

// BudgetView is how close the room is to compaction, measured the way the
// server measures it: the uncompacted log plus the page, in estimated tokens,
// and the turn count backstop. The summary is shown but not counted, as in
// the server's check.
type BudgetView struct {
	LogRunes       int64
	PageRunes      int64
	SummaryRunes   int64
	LimitTokens    int
	Turns          int64
	LimitTurns     int
	SummaryThrough int64
}

// Tokens is the estimate compaction compares against its limit.
func (b BudgetView) Tokens() int64 {
	return (b.LogRunes + b.PageRunes) / runesPerToken
}

// percent is part of whole as a CSS percentage, clamped to 0..100.
func percent(part, whole int64) string {
	if whole <= 0 || part <= 0 {
		return "0%"
	}
	p := float64(part) * 100 / float64(whole)
	if p > 100 {
		p = 100
	}
	return strconv.FormatFloat(p, 'f', 2, 64) + "%"
}

// ratio is part of whole as a whole-number percentage for labels.
func ratio(part, whole int64) string {
	if whole <= 0 {
		return "–"
	}
	return strconv.FormatInt(part*100/whole, 10) + "%"
}

// short prints a count the way a glance reads it: 950, 12.3k, 1.2M.
func short(n int64) string {
	switch {
	case n >= 1_000_000:
		return strconv.FormatFloat(float64(n)/1_000_000, 'f', 1, 64) + "M"
	case n >= 10_000:
		return strconv.FormatInt(n/1000, 10) + "k"
	case n >= 1000:
		return strconv.FormatFloat(float64(n)/1000, 'f', 1, 64) + "k"
	}
	return strconv.FormatInt(n, 10)
}

// tokens is a rune count as estimated tokens.
func tokens(runes int) string {
	return "~" + short(int64(runes/runesPerToken))
}

// millis prints a duration in milliseconds at the precision that matters.
func millis(ms int64) string {
	d := time.Duration(ms) * time.Millisecond
	switch {
	case d < time.Second:
		return strconv.FormatInt(ms, 10) + "ms"
	case d < time.Minute:
		return strconv.FormatFloat(d.Seconds(), 'f', 1, 64) + "s"
	}
	return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
}

// clock is the time of day of a stored timestamp, in UTC like the database.
func clock(stamp string) string {
	t, err := time.Parse(time.RFC3339Nano, stamp)
	if err != nil {
		return stamp
	}
	return t.Format("Jan 2 15:04:05")
}

// partColor gives each context part a fixed swatch, so a section keeps its
// color from turn to turn and in every chart.
func partColor(name string) string {
	switch name {
	case "system prompt":
		return "bg-slate-500"
	case "tool definitions":
		return "bg-slate-300 dark:bg-slate-600"
	case "compacted history":
		return "bg-violet-500"
	case "room log":
		return "bg-sky-500"
	case "notes":
		return "bg-amber-400"
	case "stylesheet":
		return "bg-pink-500"
	case "header":
		return "bg-emerald-400"
	case "board":
		return "bg-emerald-600"
	case "footer":
		return "bg-emerald-300"
	case "shared state":
		return "bg-orange-400"
	case "journal":
		return "bg-yellow-500"
	case "flags":
		return "bg-gray-400"
	case "new post":
		return "bg-rose-500"
	}
	return "bg-gray-500"
}

// statusTone is the badge color of a turn status.
func statusTone(status string) string {
	switch status {
	case "changed":
		return "bg-emerald-100 text-emerald-800 dark:bg-emerald-950 dark:text-emerald-300"
	case "kept":
		return "bg-sky-100 text-sky-800 dark:bg-sky-950 dark:text-sky-300"
	case "silent":
		return "bg-amber-100 text-amber-800 dark:bg-amber-950 dark:text-amber-300"
	case "error":
		return "bg-red-100 text-red-800 dark:bg-red-950 dark:text-red-300"
	case "running":
		return "bg-indigo-100 text-indigo-800 dark:bg-indigo-950 dark:text-indigo-300"
	}
	return "bg-gray-100 text-gray-700 dark:bg-neutral-800 dark:text-neutral-300"
}

// statusBar is the solid color of a turn status in the charts.
func statusBar(status string) string {
	switch status {
	case "changed":
		return "bg-emerald-500"
	case "kept":
		return "bg-sky-500"
	case "silent":
		return "bg-amber-400"
	case "error":
		return "bg-red-500"
	}
	return "bg-gray-400"
}

// turnPath is a turn's page. The newest turn links to the page that follows
// the newest, so it keeps up as turns land.
func turnPath(id int64, newest bool) string {
	if newest {
		return "/admin/turns"
	}
	return "/admin/turns/" + strconv.FormatInt(id, 10)
}

// pickedRow highlights the turn the list shows: the picked one, or the newest
// when nothing is picked. The bar sits on the left edge so a row keeps its
// height wherever it appears.
func pickedRow(id, picked int64, newest bool) string {
	if id == picked || (picked == 0 && newest) {
		return "border-sky-500 bg-sky-50 dark:border-sky-500 dark:bg-sky-950/40"
	}
	return "border-transparent"
}

// turnNeighbours are the turns one step older and newer than the one shown,
// or zero at either end. The list is newest first.
func (v AdminView) turnNeighbours() (older, newer int64) {
	at := 0
	if v.Panel.Turn > 0 {
		at = -1
		for i, row := range v.Turns {
			if row.Run.ID == v.Panel.Turn {
				at = i
				break
			}
		}
	}
	if at < 0 {
		return 0, 0
	}
	if at+1 < len(v.Turns) {
		older = v.Turns[at+1].Run.ID
	}
	if at > 0 {
		newer = v.Turns[at-1].Run.ID
	}
	return older, newer
}

func turnTitle(row TurnRow) string {
	if row.Run.Status == "running" {
		return fmt.Sprintf("#%d %s · %s in (%s cached) · %s out · %d rounds",
			row.Run.ID, row.Run.Status,
			short(row.PromptTokens), short(row.CachedTokens), short(row.CompletionTokens),
			row.Rounds)
	}
	return fmt.Sprintf("#%d %s · %s · %s in (%s cached) · %s out · %d rounds",
		row.Run.ID, row.Run.Status, millis(row.Run.DurationMs),
		short(row.PromptTokens), short(row.CachedTokens), short(row.CompletionTokens),
		row.Rounds)
}

func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func itoa(n int) string { return strconv.Itoa(n) }

func i64(n int64) string { return strconv.FormatInt(n, 10) }

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return strconv.Itoa(n) + " " + word + "s"
}
