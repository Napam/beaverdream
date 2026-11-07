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
	Trigger string
	// Batched is true when the turn started from several posts at once.
	Batched          bool
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
			row.Batched = newPostsBatched(message)
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

// WithPosts names the posts a turn answered when its conversation does not:
// a turn cut off before its prompt was saved has no new post section to read.
func (row TurnRow) WithPosts(posts []sqlc.Post) TurnRow {
	if row.Trigger != "" || len(posts) == 0 {
		return row
	}
	if len(posts) == 1 {
		row.Trigger = posts[0].Body
		return row
	}
	lines := make([]string, 0, len(posts))
	for _, post := range posts {
		lines = append(lines, post.Author+": "+post.Body)
	}
	row.Trigger = strings.Join(lines, "\n")
	row.Batched = true
	return row
}

// QueueView is what waits for a turn: the batch of posts, which the worker
// coalesces into one turn and the list shows as one queued row, and the admin
// instructions, which each get their own row.
type QueueView struct {
	Posts        int
	Authors      string
	Text         string
	Title        string
	Instructions []string
}

// Count is how many posts and instructions are waiting.
func (q QueueView) Count() int {
	return q.Posts + len(q.Instructions)
}

// queueTitleRunes caps the tooltip, since the visible row is one line and a
// burst can carry a lot of text.
const queueTitleRunes = 500

// NewQueue lays the pending posts out the way a turn row reads: who posted,
// what the batch says, and a count when more than one post is waiting.
func NewQueue(posts []sqlc.Post, instructions []string) QueueView {
	if len(posts) == 0 {
		return QueueView{Instructions: instructions}
	}
	view := QueueView{Posts: len(posts), Instructions: instructions}
	seen := make(map[string]bool, len(posts))
	authors := make([]string, 0, len(posts))
	title := make([]string, 0, len(posts))
	for _, post := range posts {
		// The worker names an empty author "anon" in the running row, so the
		// queued row says the same.
		author := post.Author
		if author == "" {
			author = "anon"
		}
		if !seen[author] {
			seen[author] = true
			authors = append(authors, author)
		}
		title = append(title, author+": "+post.Body)
	}
	view.Authors = strings.Join(authors, ", ")
	view.Text = posts[0].Body
	if len(posts) > 1 {
		view.Text += fmt.Sprintf(" (+%d more)", len(posts)-1)
	}
	view.Title = strings.Join(title, "\n\n")
	if runes := []rune(view.Title); len(runes) > queueTitleRunes {
		view.Title = string(runes[:queueTitleRunes]) + "…"
	}
	return view
}

// historyCount is the turns table's subtitle: the stored turns, plus the
// queued rows waiting for a turn.
func historyCount(turns, queued int) string {
	count := plural(turns, "turn")
	if queued > 0 {
		count += " · " + itoa(queued) + " queued"
	}
	return count + ", newest first"
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

// newPostsBatched reports whether the prompt's new post section held a batch.
func newPostsBatched(message webmaster.Message) bool {
	for _, section := range message.Sections {
		if section.Name == "new post" && section.End <= len(message.Content) {
			return strings.HasPrefix(message.Content[section.Start:section.End], "NEW POSTS")
		}
	}
	return false
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
// turn ends, so the inspector shows the prompt and every finished round from
// memory, and Draft is the round the model is writing now. The stored turn
// replaces all of it when it lands.
type RunProgress struct {
	TurnID     int64
	Round      int
	Since      time.Time
	RoundSince time.Time
	Authors    []string
	Messages   []webmaster.Message
	Draft      *webmaster.Draft
	// RoundDone is true between a round's last chunk and the next round's
	// start, while its tools run.
	RoundDone bool
}

// DraftView is the round being written, laid out the way a finished round is,
// with arguments read as far as they have arrived.
type DraftView struct {
	Round int
	Phase string
	Since time.Time
	// Updated is when the stream last added anything, zero before the first
	// chunk.
	Updated   time.Time
	Reasoning string
	Content   string
	Calls     []CallView
}

// LiveDraft lays out the round in flight. Before its first chunk it is an
// empty draft for the current round, still waiting for the model.
func (r RunProgress) LiveDraft() DraftView {
	view := DraftView{
		Round: r.Round,
		Phase: RunPhase(r.Draft, r.RoundDone),
		Since: r.RoundSince,
	}
	if r.Draft == nil {
		return view
	}
	if r.Draft.Round > 0 {
		view.Round = r.Draft.Round
	}
	view.Updated = r.Draft.Updated
	view.Reasoning = r.Draft.Reasoning
	view.Content = r.Draft.Content
	for _, call := range r.Draft.Calls {
		view.Calls = append(view.Calls, CallView{
			Name: call.Name,
			ID:   call.ID,
			Args: streamingArgs(call.Arguments),
		})
	}
	return view
}

// RunPhase names what a round is doing from what it has sent so far: waiting,
// reasoning, a reply, a tool call, or the tools after it.
func RunPhase(draft *webmaster.Draft, roundDone bool) string {
	switch {
	case draft == nil && roundDone:
		return "running tools"
	case draft == nil:
		return "waiting for the model"
	case len(draft.Calls) > 0:
		return "writing " + draft.Calls[len(draft.Calls)-1].Name
	case draft.Content != "":
		return "replying"
	case draft.Reasoning != "":
		return "thinking"
	}
	return "waiting for the model"
}

// streamingArgs reads tool arguments that are still arriving: a JSON object
// cut off anywhere. Each string value shows unescaped as far as it got, so
// CSS being written reads like CSS. Anything else it cannot follow shows raw.
func streamingArgs(raw string) []ArgView {
	whole := []ArgView{{Key: "arguments", Value: raw, Block: true}}
	s := strings.TrimSpace(raw)
	if s == "" {
		return nil
	}
	if s[0] != '{' {
		return whole
	}
	var args []ArgView
	i := 1
	for {
		i = skipSpace(s, i)
		if i < len(s) && s[i] == ',' {
			i = skipSpace(s, i+1)
		}
		if i >= len(s) || s[i] == '}' {
			return args
		}
		if s[i] != '"' {
			return whole
		}
		key, next, closed := readPartialString(s, i)
		if !closed {
			return args
		}
		i = skipSpace(s, next)
		if i >= len(s) {
			return args
		}
		if s[i] != ':' {
			return whole
		}
		i = skipSpace(s, i+1)
		if i >= len(s) {
			return append(args, ArgView{Key: key})
		}
		if s[i] == '"' {
			value, next, closed := readPartialString(s, i)
			args = append(args, ArgView{
				Key:   key,
				Value: value,
				Block: strings.Contains(value, "\n") || len(value) > 80,
			})
			if !closed {
				return args
			}
			i = next
			continue
		}
		end := scanValue(s, i)
		value := s[i:end]
		args = append(args, ArgView{Key: key, Value: value, Block: len(value) > 80})
		i = end
	}
}

func skipSpace(s string, i int) int {
	for i < len(s) && strings.ContainsRune(" \t\r\n", rune(s[i])) {
		i++
	}
	return i
}

// readPartialString decodes the JSON string that opens at s[i]. closed is
// false when the string is cut off; the value is then what arrived, without
// a half-sent escape.
func readPartialString(s string, i int) (string, int, bool) {
	var value string
	for j := i + 1; j < len(s); j++ {
		switch s[j] {
		case '\\':
			j++
		case '"':
			if err := json.Unmarshal([]byte(s[i:j+1]), &value); err == nil {
				return value, j + 1, true
			}
			// A closed string with a broken escape is the sample an admin
			// wants to see, not an empty value.
			return s[i+1 : j], j + 1, true
		}
	}
	// The longest escape is six bytes, \uXXXX, so one of these cuts closes.
	for cut := len(s); cut > i && cut >= len(s)-6; cut-- {
		if json.Unmarshal([]byte(s[i:cut]+`"`), &value) == nil {
			return value, len(s), false
		}
	}
	return "", len(s), false
}

// scanValue finds the end of a value that is not a string: up to the comma
// or brace that closes it, or the end of what has arrived.
func scanValue(s string, i int) int {
	depth := 0
	for ; i < len(s); i++ {
		switch s[i] {
		case '"':
			_, next, _ := readPartialString(s, i)
			i = next - 1
		case '{', '[':
			depth++
		case '}', ']':
			if depth == 0 {
				return i
			}
			depth--
		case ',':
			if depth == 0 {
				return i
			}
		}
	}
	return i
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
	case "html":
		return "bg-emerald-600"
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
	case "queued":
		return "bg-amber-100 text-amber-800 dark:bg-amber-950 dark:text-amber-300"
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

// pickedRow highlights the turn the list shows: the picked one, or the newest
// when nothing is picked. The bar sits on the left edge so a row keeps its
// height wherever it appears.
func pickedRow(id, picked int64, newest bool) string {
	if id == picked || (picked == 0 && newest) {
		return "border-sky-500 bg-sky-50 dark:border-sky-500 dark:bg-sky-950/40"
	}
	return "border-transparent"
}

// TurnNeighbours are the turns one step older and newer than id in a list
// that is newest first, or zero at either end. id 0 means the newest turn.
func TurnNeighbours(turns []TurnRow, id int64) (older, newer int64) {
	at := 0
	if id > 0 {
		at = -1
		for i, row := range turns {
			if row.Run.ID == id {
				at = i
				break
			}
		}
	}
	if at < 0 {
		return 0, 0
	}
	if at+1 < len(turns) {
		older = turns[at+1].Run.ID
	}
	if at > 0 {
		newer = turns[at-1].Run.ID
	}
	return older, newer
}

// turnNeighbours are the turns one step older and newer than the one shown.
func (v AdminView) turnNeighbours() (older, newer int64) {
	return TurnNeighbours(v.Turns, v.Panel.Turn)
}

// turnAuthor is who the turn answered. Turns stored before the column held
// that carry the constant "webmaster", which names nobody.
func turnAuthor(row TurnRow) string {
	if row.Run.Author == "webmaster" {
		return ""
	}
	return row.Run.Author
}

// triggerLabel names what started the turn: a visitor's post, or an
// instruction typed in the admin panel.
func triggerLabel(row TurnRow) string {
	if row.Run.Author == "admin" {
		return "instruction"
	}
	if row.Batched {
		return "posts"
	}
	return "post"
}

// turnError is the reason a turn failed, or "" for any other turn.
func turnError(row TurnRow) string {
	if row.Run.Status != "error" {
		return ""
	}
	return row.Run.ErrorText
}

// turnText is the list row's text. A failed turn shows its error, since that
// is what the admin scans for; the trigger stays in the tooltip.
func turnText(row TurnRow) string {
	if failure := turnError(row); failure != "" {
		return failure
	}
	return row.Trigger
}

// turnTextTone colors the list row's text red when it is an error.
func turnTextTone(row TurnRow) string {
	if turnError(row) != "" {
		return "text-red-700 dark:text-red-400"
	}
	return "text-gray-900 dark:text-neutral-100"
}

// statusLabel is the status as the admin reads it. The stored value for a turn
// that never called done is "silent", which sounds chosen; it is unfinished.
func statusLabel(status string) string {
	if status == "silent" {
		return "unfinished"
	}
	return status
}

func turnTitle(row TurnRow) string {
	if row.Run.Status == "running" {
		return fmt.Sprintf("#%d %s · %s in (%s cached) · %s out · %d rounds",
			row.Run.ID, statusLabel(row.Run.Status),
			short(row.PromptTokens), short(row.CachedTokens), short(row.CompletionTokens),
			row.Rounds)
	}
	title := fmt.Sprintf("#%d %s · %s · %s in (%s cached) · %s out · %d rounds",
		row.Run.ID, statusLabel(row.Run.Status), millis(row.Run.DurationMs),
		short(row.PromptTokens), short(row.CachedTokens), short(row.CompletionTokens),
		row.Rounds)
	if reason := strings.TrimSpace(row.Run.Said); reason != "" {
		title += "\nreason: " + reason
	}
	if failure := turnError(row); failure != "" {
		if row.Trigger != "" {
			title += "\n" + triggerLabel(row) + ": " + row.Trigger
		}
		title += "\nerror: " + failure
	}
	return title
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

// stallNote explains a round that was thrown away and asked for again.
func stallNote(stats webmaster.RoundStats) string {
	t := stats.Trace
	if t != nil && t.Stalled {
		return fmt.Sprintf(
			"stalled: the stream added nothing for %s while %s, so the round was dropped",
			t.StallLimit.Round(time.Second),
			t.StallPhase,
		)
	}
	return "the stream ended with no finish reason and no usable call, so the round was dropped"
}

// traceSummary is the one line over a round's stream details.
func traceSummary(stats webmaster.RoundStats) string {
	t := stats.Trace
	line := "longest quiet " + millis(t.LongestGap.Milliseconds())
	if !t.Finished {
		line += ", no finish reason"
	}
	return line
}

func fingerprintSuffix(fingerprint string) string {
	if fingerprint == "" {
		return ""
	}
	return " · " + fingerprint
}

// charsPerToken is the measured size of the prompt per token the provider
// counted, which calibrates the section estimates.
func charsPerToken(runes int, tokens int64) string {
	return strconv.FormatFloat(float64(runes)/float64(tokens), 'f', 2, 64)
}
