package webmaster

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Napam/beaverdream/lib/webmaster/skills"
	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/shared"
)

// Mutator asks the model to rewrite the site.
type Mutator struct {
	client    openai.Client
	model     string
	maxRounds int
	effort    shared.ReasoningEffort
	// idle is how long a round's stream may stay quiet before it is cut and
	// asked for again. stallPause is the wait before that second try.
	idle       IdleLimits
	stallPause time.Duration

	// OnRound, when set, reports the round that is about to run. The admin
	// panel uses it to show progress.
	OnRound func(round int)
	// OnRoundDone reports what one round cost and which tools it called, so a
	// slow mutation can be told apart from a slow model, and a long answer
	// from a slow one.
	OnRoundDone func(stats RoundStats)
	// OnDraft reports the round being written each time the stream adds to
	// it: reasoning, reply text and tool call arguments as they arrive.
	OnDraft func(draft Draft)
	// OnMessage reports each entry as it joins the turn's record, the prompt
	// first, so a watcher can show the conversation before it is stored.
	OnMessage func(message Message)
	// OnSay posts a say call on the board right away, while the turn still
	// runs. Without it say is accepted and goes nowhere.
	OnSay func(ctx context.Context, text string) error
}

// New builds a Mutator. maxRounds caps how many tool rounds one mutation may
// take; values below one fall back to 25. effort may be empty, or one of
// minimal, low, medium, high, xhigh.
func New(client openai.Client, model string, maxRounds int, effort string) *Mutator {
	if maxRounds <= 0 {
		maxRounds = 25
	}
	return &Mutator{
		client:    client,
		model:     model,
		maxRounds: maxRounds,
		effort:    shared.ReasoningEffort(effort),

		idle:       DefaultIdleLimits,
		stallPause: defaultStallPause,
	}
}

// ToolCall is one call the model made, with the result it got back. Kept so
// the admin page can show exactly what happened.
type ToolCall struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
	Result    string `json:"result,omitempty"`
}

// Message is one entry of the conversation sent to the model. Assistant
// entries carry the stats of the round that produced them, and the user entry
// names its sections.
type Message struct {
	Role       string          `json:"role"`
	Content    string          `json:"content,omitempty"`
	Sections   []PromptSection `json:"sections,omitempty"`
	Reasoning  string          `json:"reasoning,omitempty"`
	ToolCallID string          `json:"tool_call_id,omitempty"`
	ToolCalls  []ToolCall      `json:"tool_calls,omitempty"`
	Stats      *RoundStats     `json:"stats,omitempty"`
}

// Result is what one mutation produced. Rounds holds every completion that
// was attempted, including a failed one. DoneRound is the round whose done
// ended the turn, or zero.
type Result struct {
	Site    Site
	Changed bool
	Done    bool
	Reason  string
	// Reply is what done posts on the board, or empty for nothing.
	Reply     string
	Changes   string
	Notes     *string
	Messages  []Message
	Rounds    []RoundStats
	DoneRound int
}

// RoundStats describes one completion. Elapsed covers the model call only.
// The token counts are what the server reported, zero when it reported
// nothing or the call failed. Tools lists the calls in order, and Rejected
// counts the ones the editor refused.
type RoundStats struct {
	Round            int           `json:"round"`
	Elapsed          time.Duration `json:"elapsed_ns"`
	PromptTokens     int64         `json:"prompt_tokens"`
	CachedTokens     int64         `json:"cached_tokens"`
	CompletionTokens int64         `json:"completion_tokens"`
	Tools            []string      `json:"tools,omitempty"`
	Rejected         int           `json:"rejected,omitempty"`
	// ReplyRunes and ReasoningRunes are the size of what the model said, so a
	// log can tell a model that thought long from one that wrote a lot.
	ReplyRunes     int `json:"reply_runes,omitempty"`
	ReasoningRunes int `json:"reasoning_runes,omitempty"`
	// FirstToken is how long the model took to send its first words, so a
	// slow start can be told apart from a long answer.
	FirstToken time.Duration `json:"first_token_ns,omitempty"`
	// FinishReason is the completion's own word for why it stopped: "length"
	// means it ran into the output cap, "stop" means it chose to stop.
	FinishReason string `json:"finish_reason,omitempty"`
	// Outcomes is one clipped line per tool call, in order, so a log shows
	// what each call returned without carrying the full result.
	Outcomes []string `json:"outcomes,omitempty"`
	// Deferred is the reason a done call was pushed back into another round.
	// Empty when the round was not paused.
	Deferred string `json:"deferred,omitempty"`
	// Resampled is set when no call in the round had JSON arguments, so the
	// round was dropped from the conversation and asked for again.
	Resampled bool `json:"resampled,omitempty"`
	// Stalled is set when the round was thrown away because its stream went
	// quiet or ended without a finish reason, and asked for again. Its token
	// counts are zero because they are unknown.
	Stalled bool `json:"stalled,omitempty"`
	// Trace is how the stream looked from the client. See RoundTrace.
	Trace *RoundTrace `json:"trace,omitempty"`
}

// Mutate asks the model to rewrite the site in response to one trigger. The
// returned Site is the original when nothing changed, so callers can write a
// new version only when Changed is true.
func (m *Mutator) Mutate(
	ctx context.Context,
	current Site,
	room Room,
	trigger Post,
) (Result, error) {
	return m.MutatePosts(ctx, current, room, []Post{trigger})
}

// MutatePosts is Mutate for a burst that was coalesced into one rewrite. The
// prompt credits every poster instead of pretending one person wrote it all.
func (m *Mutator) MutatePosts(
	ctx context.Context,
	current Site,
	room Room,
	triggers []Post,
) (Result, error) {
	if len(triggers) == 0 {
		triggers = []Post{{Author: "admin"}}
	}
	ed := newEditor(current)
	if m.OnSay != nil {
		ed.say = func(text string) error { return m.OnSay(ctx, text) }
	}
	sys := systemPrompt()
	usr, sections := userPromptSections(current, room, triggers)

	res := Result{Site: current}
	m.record(&res, Message{Role: "system", Content: sys})
	m.record(&res, Message{Role: "user", Content: usr, Sections: sections})

	messages := []openai.ChatCompletionMessageParamUnion{
		openai.SystemMessage(sys),
		openai.UserMessage(usr),
	}

	hiddenWarned := false
	journalWarned := false
	// Rounds that arrived entirely as unparseable arguments. They are
	// resampled, never appended to the conversation; the cap is a backstop.
	brokenArgumentRounds := 0
	stoppedBadArguments := false
	// Where the loop stopped when it did not stop on done. A model that
	// answers without a tool call ends the turn in one round no matter how
	// large the budget is, so the reason has to name the round, not the budget.
	proseRound := 0
	truncatedRound := 0
	emptyRound := 0
	// work counts rounds the model got to act on. A resampled or stalled
	// round does not use one up; attempt numbers every request, and the
	// resample and stall caps bound the retries.
	work, attempt, stallStreak := 0, 0, 0
	// promptSize counts the runes of the text the request carries, so prompt
	// tokens over it is the provider's real ratio.
	promptSize := utf8.RuneCountInString(sys) + utf8.RuneCountInString(usr)
	for work < m.maxRounds {
		attempt++
		if m.OnRound != nil {
			m.OnRound(attempt)
		}
		roundStart := time.Now()
		reply, err := m.complete(ctx, openai.ChatCompletionNewParams{
			Model:      openai.ChatModel(m.model),
			Messages:   messages,
			Tools:      toolParams(),
			ToolChoice: toolChoiceRequired,
			// Without include_usage a stream reports no tokens at all.
			StreamOptions: openai.ChatCompletionStreamOptionsParam{
				IncludeUsage: openai.Bool(true),
			},
			MaxTokens:       openai.Int(int64(maxOutputTokens)),
			Temperature:     openai.Float(0.8),
			ReasoningEffort: m.effort,
		}, attempt)
		stats := RoundStats{
			Round:      attempt,
			Elapsed:    time.Since(roundStart),
			FirstToken: reply.FirstToken,
			Trace:      reply.Trace,
		}
		if stats.Trace != nil {
			stats.Trace.PromptRunes = promptSize
		}
		if err != nil {
			m.reportRound(&res, stats)
			return res, fmt.Errorf("chat completion: %w", err)
		}
		if reply.Stalled || endedWithoutFinish(reply) {
			// The stream went quiet or closed early. Nothing it sent is used,
			// and nothing goes in the conversation: the same request is sent
			// again after a pause, which may land on a healthy backend.
			stats.Stalled = true
			stats.FinishReason = "stalled"
			if !reply.Stalled {
				stats.FinishReason = "no finish reason"
			}
			m.reportRound(&res, stats)
			m.record(&res, Message{Role: "assistant", Stats: &stats})
			stallStreak++
			if stallStreak >= maxStallStreak {
				return res, fmt.Errorf(
					"the model stream failed %d rounds in a row (last: %s)",
					stallStreak, stallCause(reply),
				)
			}
			if err := pause(ctx, m.stallPause); err != nil {
				return res, fmt.Errorf("chat completion: %w", err)
			}
			continue
		}
		stallStreak = 0
		stats.PromptTokens = reply.Usage.PromptTokens
		stats.CachedTokens = cachedTokens(reply.Usage)
		stats.CompletionTokens = reply.Usage.CompletionTokens
		if !reply.Choices {
			emptyRound = attempt
			m.reportRound(&res, stats)
			break
		}

		stats.FinishReason = reply.FinishReason
		entry := Message{
			Role:      "assistant",
			Content:   reply.Content,
			Reasoning: reply.Reasoning,
		}
		stats.ReplyRunes = utf8.RuneCountInString(strings.TrimSpace(reply.Content))
		stats.ReasoningRunes = utf8.RuneCountInString(reply.Reasoning)
		if len(reply.Calls) == 0 {
			proseRound = attempt
			if stats.FinishReason == "length" {
				truncatedRound = attempt
			}
			m.reportRound(&res, stats)
			entry.Stats = &stats
			m.record(&res, entry)
			break
		}

		content := reply.Content
		if strings.Contains(content, providerToolMarkup) {
			// The model's prose can leak the same native markup as its
			// arguments. Keep it in the stored message, but not in the next
			// prompt.
			content = ""
		}
		assistant := openai.ChatCompletionAssistantMessageParam{
			Content: openai.ChatCompletionAssistantMessageParamContentUnion{
				OfString: openai.String(content),
			},
		}
		if reply.HasReasoning {
			// A provider that returns reasoning_content needs it back beside
			// every tool call; DeepSeek's thinking mode rejects the next
			// request without it. Providers that do not return it never see
			// the field.
			assistant.SetExtraFields(map[string]any{"reasoning_content": reply.Reasoning})
		}
		ed.rejected = 0
		ed.badArguments = 0
		// A call cut off by the output cap is not a broken sample: resampling
		// would truncate the same way. It is rejected like any other call, with
		// a message that points at smaller writes.
		truncated := stats.FinishReason == "length"
		for _, record := range reply.Calls {
			assistant.ToolCalls = append(assistant.ToolCalls, toolCallParam(record))
			if truncated && !json.Valid([]byte(record.Arguments)) {
				record.Result = callTruncated
				ed.rejected++
			} else {
				record.Result = ed.run(record.Name, record.Arguments)
			}
			entry.ToolCalls = append(entry.ToolCalls, record)
			stats.Tools = append(stats.Tools, record.Name)
		}
		stats.Rejected = ed.rejected
		allBroken := ed.badArguments == len(entry.ToolCalls)
		stats.Resampled = allBroken
		entry.Stats = &stats

		// A response that calls done next to a rejected call would end the
		// turn before the model sees the rejection. While rounds remain, the
		// turn stays open for one fix. The reason and notes from this done
		// stay as the fallback if the next response never calls done.
		//
		// A page whose posts this turn's edits hid behind an unclosed tag
		// gets the same pause, once. Calling done again keeps the page.
		deferred := ""
		if ed.done && work+1 < m.maxRounds {
			switch {
			case ed.rejected > 0:
				deferred = doneDeferred
			case !hiddenWarned:
				if what := PostsSwallowedBy(
					ed.site,
				); what != "" &&
					PostsSwallowedBy(current) == "" {
					deferred = fmt.Sprintf(doneHidesPosts, what)
					hiddenWarned = true
				}
			}
			if deferred == "" && !journalWarned {
				missing := MissingJournalKeys(ed.site)
				// Only this turn's loss counts. A board that already had a
				// missing key should not defer every later turn.
				if len(missing) > 0 && len(MissingJournalKeys(current)) == 0 {
					deferred = fmt.Sprintf(doneDropsJournal, strings.Join(missing, ", "))
					journalWarned = true
				}
			}
		}
		if deferred != "" {
			ed.done = false
			stats.Deferred = deferred
			for i := range entry.ToolCalls {
				if entry.ToolCalls[i].Name == "done" {
					entry.ToolCalls[i].Result = deferred
				}
			}
		}
		for _, call := range entry.ToolCalls {
			stats.Outcomes = append(stats.Outcomes, outcomeLine(call))
		}
		m.reportRound(&res, stats)

		m.record(&res, entry)
		// A completion where no call parsed is not an edit to fix. Do not put
		// it in the conversation: the next request repeats the one before it,
		// with a fresh sample. The turn's own record keeps the broken round so
		// the admin still shows what arrived.
		if allBroken {
			brokenArgumentRounds++
			if brokenArgumentRounds >= maxBrokenArgumentRounds {
				stoppedBadArguments = true
				break
			}
			continue
		}
		messages = append(
			messages,
			openai.ChatCompletionMessageParamUnion{OfAssistant: &assistant},
		)
		promptSize += utf8.RuneCountInString(content)
		if reply.HasReasoning {
			promptSize += utf8.RuneCountInString(reply.Reasoning)
		}
		for _, call := range entry.ToolCalls {
			messages = append(messages, openai.ToolMessage(call.Result, call.ID))
			promptSize += utf8.RuneCountInString(call.Name) +
				utf8.RuneCountInString(call.Arguments) +
				utf8.RuneCountInString(call.Result)
		}

		if ed.done {
			res.DoneRound = attempt
			break
		}
		work++
	}

	res.Site = ed.site
	res.Done = ed.done
	res.Notes = ed.notes
	res.Reason = ed.reason
	res.Reply = ed.reply
	// Calls that cancel each other out leave the editor dirty but the page
	// as it was. Only a real difference writes a version.
	if ed.dirty {
		res.Changes = DescribeChanges(current, ed.site)
	}
	res.Changed = res.Changes != ""
	if stoppedBadArguments && res.Reason == "" {
		res.Reason = fmt.Sprintf(
			"the model kept sending tool arguments that were not JSON, so the turn stopped after %d broken rounds",
			brokenArgumentRounds,
		)
	}
	// A reply that ran into the output cap is usually half a thought, so the
	// cap is worth naming even when some prose made it out.
	if res.Reason == "" && truncatedRound > 0 {
		res.Reason = fmt.Sprintf(
			"the model hit the %d token output cap in round %d without calling done",
			maxOutputTokens, truncatedRound,
		)
	}
	if res.Reason == "" {
		// A prose reply is the model trying to talk. Keep the last thing it
		// said rather than a mechanical error line.
		for i := len(res.Messages) - 1; i >= 0; i-- {
			message := res.Messages[i]
			if message.Stats != nil && (message.Stats.Resampled || message.Stats.Stalled) {
				// A dropped round can carry the provider's tool markup.
				continue
			}
			content := strings.TrimSpace(message.Content)
			if message.Role == "assistant" && content != "" {
				res.Reason = clip(content, 200)
				break
			}
		}
	}
	if res.Reason == "" {
		switch {
		case proseRound > 0:
			res.Reason = fmt.Sprintf("no tool call in round %d", proseRound)
		case emptyRound > 0:
			res.Reason = fmt.Sprintf("the model returned no choices in round %d", emptyRound)
		default:
			res.Reason = fmt.Sprintf("no done call after %d rounds", work)
		}
	}
	return res, nil
}

// callTruncated is the result of a call the output cap cut off.
const callTruncated = "rejected: the reply hit the output cap and this call was cut off. " +
	"Send smaller pieces: use edit for part of a field instead of set_site for all of it."

// endedWithoutFinish reports a stream that closed cleanly with no finish
// reason and nothing usable: no calls, or a call whose arguments are not
// JSON. A round that carries complete calls is kept even if only the finish
// chunk is missing.
func endedWithoutFinish(r reply) bool {
	if !r.Choices {
		// A stream with no chunk at all is broken. One that reports usage but
		// no choices is a provider answering with nothing, handled apart.
		return r.Usage.PromptTokens == 0 && r.Usage.CompletionTokens == 0
	}
	if r.FinishReason != "" {
		return false
	}
	if len(r.Calls) == 0 {
		return true
	}
	for _, call := range r.Calls {
		if !json.Valid([]byte(call.Arguments)) {
			return true
		}
	}
	return false
}

// stallCause says why a round was dropped, for the error that ends a turn.
func stallCause(r reply) string {
	if r.Stalled && r.Trace != nil {
		return fmt.Sprintf("quiet for over %s while %s", r.Trace.StallLimit, r.Trace.StallPhase)
	}
	return "the stream ended empty or with no finish reason"
}

// pause waits for the stall pause plus jitter, or until ctx ends.
func pause(ctx context.Context, base time.Duration) error {
	if base <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(base + time.Duration(rand.Int64N(int64(base))))
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// toolChoiceRequired makes every response a tool call. The model's only way
// to talk is done, so a prose reply is always a lost turn. It is safe on every
// round: done ends the loop.
var toolChoiceRequired = openai.ChatCompletionToolChoiceOptionUnionParam{
	OfAuto: openai.String(string(openai.ChatCompletionToolChoiceOptionAutoRequired)),
}

// doneDeferred is the result of a done call that shared its response with a
// rejected call.
const doneDeferred = "not finished: a call above was rejected. Fix it, then call done again."

// doneHidesPosts is the result of a done call whose page hides every post
// behind something left open before {{posts}}.
const doneHidesPosts = "not finished: %s before {{posts}} hides every post. " +
	"Close it and call done again, or call done again to keep the page as it is."

// doneDropsJournal is the result of a done call whose page no longer mentions a
// declared journal key, which would orphan the rows behind it.
const doneDropsJournal = "not finished: no script in the page calls bd.journal for key %s, " +
	"so that feature is gone and its rows can no longer be reached. Put the " +
	"bd.journal call back, or remove the key from set_journal_schema, then call done again."

// reportRound records one finished completion on the result and tells the
// listener.
func (m *Mutator) reportRound(res *Result, stats RoundStats) {
	res.Rounds = append(res.Rounds, stats)
	if m.OnRoundDone != nil {
		m.OnRoundDone(stats)
	}
}

// record adds one entry to the turn's conversation and tells the listener.
func (m *Mutator) record(res *Result, message Message) {
	res.Messages = append(res.Messages, message)
	if m.OnMessage != nil {
		m.OnMessage(message)
	}
}

// MaxRounds is the round budget this Mutator was built with.
func (m *Mutator) MaxRounds() int { return m.maxRounds }

// outcomeLine is one tool call as a single log line: the name, then what the
// editor said back, flattened and clipped so the log stays readable. A call
// with broken arguments also shows the start of what arrived, since the
// provider's markup is the clue.
func outcomeLine(call ToolCall) string {
	result := strings.Join(strings.Fields(call.Result), " ")
	line := call.Name + ": " + clip(result, 120)
	if strings.HasPrefix(call.Result, "rejected: arguments") ||
		strings.Contains(call.Result, "tool markup") {
		arguments := strings.Join(strings.Fields(call.Arguments), " ")
		line += " arguments=" + clip(arguments, 60)
	}
	return line
}

// cachedTokens reads the prompt cache hits. OpenAI reports them under
// prompt_tokens_details; DeepSeek's own field is prompt_cache_hit_tokens, which
// the SDK keeps as an extra field.
func cachedTokens(usage openai.CompletionUsage) int64 {
	if cached := usage.PromptTokensDetails.CachedTokens; cached > 0 {
		return cached
	}
	field, ok := usage.JSON.ExtraFields["prompt_cache_hit_tokens"]
	if !ok {
		return 0
	}
	var hits int64
	if err := json.Unmarshal([]byte(field.Raw()), &hits); err != nil {
		return 0
	}
	return hits
}

// toolCallParam rebuilds a tool call for the assistant message that precedes
// its results. Built from the accumulated fields rather than the raw JSON,
// which some OpenAI-compatible servers leave empty.
func toolCallParam(call ToolCall) openai.ChatCompletionMessageToolCallUnionParam {
	arguments := call.Arguments
	if !json.Valid([]byte(arguments)) || strings.Contains(arguments, providerToolMarkup) {
		// A provider can return its native tool markup here instead of JSON.
		// Replaying that text would put the markup in the next prompt, where
		// the model copies it and the turn spirals. The result still says the
		// call was rejected, and the stored call keeps what really arrived.
		arguments = "{}"
	}
	return openai.ChatCompletionMessageToolCallUnionParam{
		OfFunction: &openai.ChatCompletionMessageFunctionToolCallParam{
			ID: call.ID,
			Function: openai.ChatCompletionMessageFunctionToolCallFunctionParam{
				Name:      call.Name,
				Arguments: arguments,
			},
		},
	}
}

// toolParams advertises the mutation tools. Every extra knob is another way a
// small model can produce broken output.
func toolParams() []openai.ChatCompletionToolUnionParam {
	return []openai.ChatCompletionToolUnionParam{
		openai.ChatCompletionFunctionTool(shared.FunctionDefinitionParam{
			Name: "edit",
			Description: openai.String(
				"Change part of the stylesheet or the html without resending it. " +
					"Replaces the exact text old with new. Empty old appends new at the end.",
			),
			Parameters: shared.FunctionParameters{
				"type": "object",
				"properties": map[string]any{
					"target": map[string]any{
						"type": "string",
						"enum": []string{targetCSS, targetHTML},
					},
					"old": map[string]any{
						"type": "string",
						"description": "Text copied exactly from the CURRENT section, unique in it. " +
							"Keep it short: just enough to be unique. Empty to append.",
					},
					"new": map[string]any{
						"type":        "string",
						"description": "Replacement text. Empty deletes old.",
					},
				},
				"required": []string{"target", "old", "new"},
			},
		}),
		openai.ChatCompletionFunctionTool(shared.FunctionDefinitionParam{
			Name: "set_site",
			Description: openai.String(
				"Replace the stylesheet, the html, or both: a restyle, a new layout, or a " +
					"different kind of page. Only the fields you send change.",
			),
			Parameters: shared.FunctionParameters{
				"type": "object",
				"properties": map[string]any{
					"css": map[string]any{
						"type":        "string",
						"description": "CSS rules without a style tag.",
					},
					"html": map[string]any{
						"type":        "string",
						"description": "Everything inside body. Must contain {{posts}}.",
					},
				},
			},
		}),
		openai.ChatCompletionFunctionTool(shared.FunctionDefinitionParam{
			Name: "set_state_schema",
			Description: openai.String(
				"Declare the state the page can read and write. " +
					"counter increments, list appends a short string, choice tallies an option. " +
					"A counter with scope visitor is a whole-number count each visitor owns; " +
					"a recipe trades cost counts for gain counts. Send every key you keep.",
			),
			Parameters: shared.FunctionParameters{
				"type": "object",
				"properties": map[string]any{
					"keys": map[string]any{
						"type": "array",
						"items": map[string]any{
							"type": "object",
							"properties": map[string]any{
								"name": map[string]any{
									"type":        "string",
									"description": "Lowercase letters, digits, _ or -.",
								},
								"op": map[string]any{
									"type": "string",
									"enum": []string{"counter", "list", "choice", "recipe"},
								},
								"scope": map[string]any{
									"type": "string",
									"enum": []string{"global", "visitor"},
									"description": "For counter: global (one shared count, the default) " +
										"or visitor (each visitor holds their own).",
								},
								"label": map[string]any{
									"type":        "string",
									"description": "For a visitor counter: a name for the admin and the prompt. The page never sees it.",
								},
								"max": map[string]any{
									"type": "integer",
									"description": "For list: how many entries to keep. " +
										"For a visitor counter: the most one visitor can hold.",
								},
								"per_hour": map[string]any{
									"type":        "integer",
									"description": "For a visitor counter: the most one visitor can gain in any rolling hour. Spending does not give it back.",
								},
								"cost": map[string]any{
									"type":                 "object",
									"additionalProperties": map[string]any{"type": "integer"},
									"description":          "For recipe: visitor counters paid, name to amount.",
								},
								"gain": map[string]any{
									"type":                 "object",
									"additionalProperties": map[string]any{"type": "integer"},
									"description":          "For recipe: visitor counters received, name to amount.",
								},
								"options": map[string]any{
									"type":        "array",
									"items":       map[string]any{"type": "string"},
									"description": "For choice: the allowed values.",
								},
							},
							"required": []string{"name", "op"},
						},
					},
				},
				"required": []string{"keys"},
			},
		}),
		openai.ChatCompletionFunctionTool(shared.FunctionDefinitionParam{
			Name: "set_journal_schema",
			Description: openai.String(
				"Declare the append-only event logs the page writes and folds. " +
					"One key is one feed: a game's moves, a chat, votes, a guestbook. " +
					"Send every key you keep: a key left out is removed and its rows are lost.",
			),
			Parameters: shared.FunctionParameters{
				"type": "object",
				"properties": map[string]any{
					"keys": map[string]any{
						"type": "array",
						"items": map[string]any{
							"type": "object",
							"properties": map[string]any{
								"name": map[string]any{
									"type":        "string",
									"description": "Lowercase letters, digits, _ or -. Also put this string in the page.",
								},
								"max": map[string]any{
									"type": "integer",
									"description": fmt.Sprintf(
										"Rows this key keeps across all matches; older rows "+
											"are deleted for everyone. Omit to keep the current value; a "+
											"new key starts at %d, max %d. At 16 updates a second "+
											"from 5 players, %d rows is a few seconds of history.",
										defaultJournalMax,
										maxJournalRows,
										defaultJournalMax,
									),
								},
								"rules": map[string]any{
									"type": "integer",
									"description": "Omit to keep the current number. Raise it only " +
										"when old rows would mean something different to the new " +
										"reduce(); a style or render change keeps it. Rows under " +
										"older rules are ignored.",
								},
							},
							"required": []string{"name"},
						},
					},
				},
				"required": []string{"keys"},
			},
		}),
		openai.ChatCompletionFunctionTool(shared.FunctionDefinitionParam{
			Name: "load_skill",
			Description: openai.String(
				"Read a how-to for one kind of build before you make it. Its result is " +
					"the reference to build from; the page is not changed by this call. " +
					"Skills: " + skills.Summaries() + ".",
			),
			Parameters: shared.FunctionParameters{
				"type": "object",
				"properties": map[string]any{
					"name": map[string]any{
						"type": "string",
						"enum": skills.Names(),
					},
				},
				"required": []string{"name"},
			},
		}),
		openai.ChatCompletionFunctionTool(shared.FunctionDefinitionParam{
			Name: "say",
			Description: openai.String(
				"Post a quick message on the board right now, before the turn ends, " +
					"signed webmaster. Use it to answer a request before you work on it. " +
					"Once per turn.",
			),
			Parameters: shared.FunctionParameters{
				"type": "object",
				"properties": map[string]any{
					"text": map[string]any{
						"type": "string",
						"description": fmt.Sprintf(
							"A short casual line, like \"sure, adding a sun\". Plain text. "+
								"Max %d characters.",
							maxSayLen,
						),
					},
				},
				"required": []string{"text"},
			},
		}),
		openai.ChatCompletionFunctionTool(shared.FunctionDefinitionParam{
			Name: "done",
			Description: openai.String(
				"End your turn. reason is your one-line log entry; reply, when " +
					"you give one, is posted on the board for everyone. Call it even " +
					"when you keep the page as it is.",
			),
			Parameters: shared.FunctionParameters{
				"type": "object",
				"properties": map[string]any{
					"reason": map[string]any{
						"type": "string",
						"description": fmt.Sprintf(
							"One short sentence for your own log: what this turn did or why. "+
								"Visitors never see it. Max %d characters.",
							maxReasonLen,
						),
					},
					"reply": map[string]any{
						"type": "string",
						"description": fmt.Sprintf(
							"Your answer, posted on the board as a post signed webmaster. "+
								"Answer questions here in plain text, short and casual, like "+
								"talking to a friend you play with. Omit it when "+
								"you have nothing to tell the visitors. Max %d characters.",
							maxReplyLen,
						),
					},
					"notes": map[string]any{
						"type": "string",
						"description": fmt.Sprintf(
							"Private notes to your future self: positions, promises, "+
								"grudges, running jokes. Replaces your notes. Omit to keep them. "+
								"Max %d characters.",
							maxNotesLen,
						),
					},
				},
				"required": []string{"reason"},
			},
		}),
	}
}

// The system prompt is assembled from static pieces, so it is one stable
// string with a stable prompt cache prefix.
var systemPromptHead = fmt.Sprintf(
	`You are the webmaster of beaverdream. It starts as a small anonymous message
board, and a post can turn it into anything: a game, a shop, a city, a world
the posts live in. You are the one other presence in a room full of visitors.
You have two voices: the page, which you rewrite, and your reply, which done
posts on the board as a post signed webmaster. Every turn is your turn in one
continuous conversation.

The COMPACTED HISTORY is everything older, folded into one message so it fits.
The ROOM LOG is the recent stretch after that, in order. Together they are the
conversation. Lines marked "you" are your own past turns: your log sentence
and what you changed. Lines marked "you replied" are what you posted on the
board. Continue from them. Hold a position, keep a
promise, escalate, relent, notice that you already granted something, keep a
running joke alive. Do not re-roll the page from scratch unless you mean to
change the subject. When the log gets long, the server compacts it again; the
compacted history is what survives, so write your notes for the things you
refuse to lose.

The CURRENT sections are the page as it stands, the sum of your past turns.
Edit it. You are not starting over.

Most posts are not requests for the page. A greeting, a joke, a thought, a
question, or anything the poster says about themselves is talk: call done
without edits. Visitors also use the board to talk to each other, so act like
one more player in a group chat, not a host who answers everything. Reply only
when a post is meant for you: it names you, asks for a change, asks about the
page or the game, or asks a question nobody else in the room would answer.
Stay quiet when visitors talk among themselves: a post that names or quotes
another visitor, answers someone, trash talk, banter, "gg", plans between
players. When in doubt, say nothing; silence is the normal outcome for chat.
When a question is for you, answer it properly and correctly, as a
knowledgeable person would, including general knowledge, how something works,
or advice. With several new posts, answer each one meant for you in the same
reply, starting each answer with >>N for its post.

Write replies the way you would text a friend you are playing a game with:
short, casual, direct. One or two plain sentences is the norm; say more only
when a question needs it. No narration, no imagery, no flourishes, no listing
everything that stayed the same, no em dashes. "done, minimap's gone" beats a
paragraph about the empty corner.

Change the page only when the post asks for something, or when BOARD IS PLAIN
is yes. Talk to the poster like a person doing it for them: first say a quick
answer ("sure, adding a sun", "ok one sec", "nah, but i'll do this instead"),
then make the change, then end with done and a short reply once it is in
("there you go, sun's up top"). When you do change it, take the post literally, take it personally, or misread it on
purpose. A request gets granted, maybe too well. The request sets the scale: a
color gets an edit, and "make it a game" gets a game that takes over the page,
not a game in a box above the old board. Make the change part of the page
itself, where the poster will run into it, not a sentence on the page
announcing it.

When a post asks for a game or anything people could do together, build it
multiplayer by default: one shared world, where every visitor sees the others
act, through shared state or a journal. "make pacman" means one board everyone
plays on. Build it for a single player only when the post asks for that. A
color, a restyle, a text change or a purely visual idea needs none of this.

The same goes for every feature added later: assume it is shared live. One
poster asks, but everyone on the page gets it, and each visitor sees what
happens to every character, not just their own. Before you add an ability, a
look, an effect or a change to the world, ask what the other visitors see,
and build that in the same turn:
- What a player is (character, suit, form, what they hold): send it with their
  motion, so the others draw them that way.
- What a player does (a shot, a throw, a transform, an effect): one journal
  row when it happens, and every other page plays the same thing on that
  player.
- What it does to others: damage, knockback and pickups reach the other
  players, not just the poster's screen.
- What changes the world (time of day, broken walls, an opened door): shared
  state or a journal every page folds, never a variable in one tab.
Features that stay in one browser are fine where they make sense: the camera,
settings, menus, a visitor's own interface, or anything the post asks to keep
to one person. Sharing is the default; when unsure, share it.

Before you build a game or change how it plays, load the skills it needs,
all in one response of its own: controls for a world someone moves through,
feel for any game with actions, space when things break, block or hide,
entities for several kinds of characters or enemies, paths when anything
must find its way around walls, random for anything generated: levels,
mazes, terrain, decks, daily puzzles, canvas for anything drawn on a 2D
canvas, and characters for anyone a visitor plays or meets. It gives a
body motion and a starting shape; design each character to fit the board,
so heroes look like heroes and animals like animals. A capsule with a ball
on top is not a character, and more glow does not make it one.

For 3D, use three.js 0.187, served by this page: import * as THREE from
'three' in a module script. These extras come from 'three/addons' and
nothing else does: EffectComposer, RenderPass, ShaderPass, UnrealBloomPass
and OutputPass for glow; mergeGeometries, mergeVertices and
BufferGeometryUtils to merge many meshes into few; ImprovedNoise and
SimplexNoise for terrain; Sky; RoundedBoxGeometry and ConvexGeometry; Line2,
LineGeometry and LineMaterial for thick lines; CSS2DRenderer and CSS2DObject
for labels over things; PointerLockControls and OrbitControls. Import them by
name, as in import { UnrealBloomPass } from 'three/addons'. Never load
three.js or another 3D library from a CDN.

Effects in 3D: animate the real thing. When armor comes off, move the
armor's own meshes, not stand-in boxes. Aim at where a target is this frame,
not where it was when the effect started, since bodies move and animate.
When a visitor says they see nothing, check before touching color or glow:
how big it is, how far from the camera, whether it is behind the camera or
hidden, and how long it lasts. Most invisible effects are too small, too far
away or too short.

The page is two fields you own: css, the stylesheet, and html, everything
inside body. The html has one rule: it holds the literal placeholder {{posts}}
exactly once, where the server puts the real posts. Any write that drops it
or repeats it, even in a comment or a script string, is rejected. Everything
else is yours, including the layout.

The two fields share one size cap: css plus html may be at most %d bytes.
An edit that pushes the page over is rejected and says how many bytes to
remove.

Tools:
- edit(target, old, new): change part of the page. target is css or html.
  old is text copied exactly from that CURRENT section and must appear there
  once; it becomes new. Keep old short, just enough to be unique. Empty old
  appends new to the end of the target, which is how you add a CSS rule or a
  script. Edits apply in order, and each one sees the result of the one
  before. Use it for changes to the page you have.
- set_site(css, html): replace one field or both. Use it for a restyle, a new
  layout, or when a post asks for a different kind of page. Only the fields
  you send change.
- load_skill(name): read a how-to before you build one kind of thing, such as a
  world someone moves through. Its result is the reference you build from.
- say(text): post a short line on the board right away, while you still work.
  Once per turn. Use it before a change, never for talk you can put in done's
  reply.
`,
	maxPageLen,
)

var systemPromptMiddle = fmt.Sprintf(
	`- To restyle a little, append rules: later rules win over earlier ones, so
  edit(css, "", ":root{--accent:#c00}") recolors the page. For a small change,
  an edit beats resending a whole field, which costs the room time. A post
  that asks for a different page is worth the time.
- set_state_schema(keys): declare shared values the page can read and write.
  Ops are counter, list (keep the newest max entries) and choice (tally one of
  the options). A counter with scope visitor is a count each visitor owns, and
  a recipe trades visitor counts. Up to %d keys and recipes together. Declare
  only what the page actually uses.
- set_journal_schema(keys): declare append-only feeds the page writes and
  folds. Use it when a post asks for something people do together: a game, a
  chat, votes, a shared list. It is the only way to give the page real shared
  behavior. Up to %d keys, and every key must appear in the page.
- done(reason, reply, notes): end your turn. reason is one sentence for your
  own log; you read it back next turn and visitors never see it. reply is
  posted on the board as a post signed webmaster, the newest post, with its
  own No.; it is plain text, and >>12 links to post 12. Omit reply to post
  nothing. notes replaces your private notes to your future self. Omit notes
  to keep them.

You have one edit round. In that response, call every edit or set_* tool you
need, then call done in the same response. Do not use one response per
tool. The server applies the calls in order. If you split the edits across
responses, the page updates slower and feels dead. Keep it to about six calls
per response: past that, a call is likelier to arrive broken, so write that
field once with set_site instead. If a call is rejected, its
result says why, and the turn stays open so you can fix it in the next
response; call done again after the fix.

say and load_skill come before the edit round. When you are about to change
the page, your first response is say, plus load_skill once for each skill you
need, and nothing else, so the poster hears from you before the work starts.
Then make your edits and call done in the next response, once you have the
references to build from. A turn that keeps the page skips say and answers in
done's reply.

Keeping the page as it is counts as a turn. This is the normal answer to
ordinary talk and to questions. Call done without edits, with a reason for the
log and a reply for the room. Exception: when BOARD IS PLAIN is yes, change
something visible.

The page is not sandboxed. Your HTML and CSS are served as the visitor's page,
scripts included, so anything you write runs in their browser. Sabotage is
allowed: if a post asks you to prank the board or its visitors, you may. If you
break the page, that is a valid outcome.

Every version must contain a working post form, anywhere in the html. Start
from this markup, because the server only listens on these names:

<form class="postbox" method="post" action="/posts" data-on:submit="@post('/posts', {contentType: 'form'}); el.reset()">
  <p class="postbox-title">post a message</p>
  <textarea name="body" required maxlength="%d" rows="3" placeholder="say something" aria-label="message"></textarea>
  <div class="postbox-actions"><span>posting as {{name}}</span>
  <button type="submit">post</button></div>
</form>

The data-on:submit attribute sends the post without leaving the page and
clears the box; the post then arrives in {{posts}} for everyone over the live
stream. Keep it. Without it the form still works as a plain form
post, which reloads the page.

The server records the visitor's cookie name as the post author. Do not add a
name field: the server ignores it. The page can use {{name}} to show the
visitor's name; the server replaces it with the escaped cookie name. The
classes are only hooks for your stylesheet. Style it, move it, wrap it, make
it strange. Keep the method, action and field names working unless the
post asks you to break posting.

The live page:
- The page is live. It holds one stream from the server, and the server pushes
  changes as they happen. Nothing on the page polls. New posts appear in
  {{posts}} without a reload. A change to only your stylesheet is
  applied in place. A change to the markup reloads every open tab once the
  visitor stops typing, with their draft and scroll position kept.
- Datastar v1 is loaded on every page, so data-* attributes in your markup are
  live. Reach for them before a script for anything interactive:
    data-signals="{open: false}"           declare signals
    data-on:click="$open = !$open"         run an expression on an event
    data-show="$open"                      show or hide
    data-text="$count + ' taps'"           set the text
    data-class:lit="$open"                 toggle a class
    data-attr:disabled="!$draft"           set an attribute
    data-bind:draft                        two-way bind an input to $draft
    data-computed:total="$a + $b"          a derived signal
    data-on-interval__duration.1s="$t++"   a timer
    data-init="..."                        run once when the element appears
  Expressions are JavaScript: $name reads or writes a signal, el is the
  element, evt the event. Signals live in one tab and reset on reload; shared
  values go through state or the journal, and the visitor's own things
  through bd.save. These attribute names belong to
  Datastar, so do not use a Datastar name for your own data; data-cell and
  data-label are safe.
- Use @post only for /posts and /x/state/KEY. They answer with nothing; the
  new data comes back over the stream to every page at once. Never @get or
  @post a page like /: the reply would be patched into the document.
- These ids belong to the page and the live stream. Do not reuse them:
  bd-live, bd-posts, bd-css, bd-busy, bd-busy-ring, bd-state, bd-journal. Do not
  name your own signals shared, mine or tops, or anything that starts with
  _bd.
- Page scripts can listen on document for beaverdream:post (new posts just
  landed), beaverdream:state (shared values changed) and beaverdream:update
  (the stylesheet changed in place). beaverdream:post carries detail.mode and
  detail.ids. With mode "prepend", ids are the new posts on top of the list.
  With mode "replace", the whole list was swapped and ids are every post in
  it, so place or decorate them all again. No event fires on the first load,
  so a script that places posts runs once on load and again on each
  beaverdream:post. CSS reaches new posts by itself, so prefer it for
  anything about how posts look.

Shared state:
- The current values arrive in the prompt. Every open page gets them pushed
  live as the $shared signal, so markup bound to $shared.KEY updates for
  everyone the moment anyone writes. Scripts get the beaverdream:state event
  with the values in detail.state; the JSON script tag id="bd-state" is kept
  current too.
- POST /x/state/KEY applies that key's op for the current visitor. The value
  comes from a value=... query parameter or form field:
    <button type="button" data-on:click="@post('/x/state/likes')">like</button>
    <span data-text="$shared.likes"></span>
    <button type="button" data-on:click="@post('/x/state/poll?value=yes')">yes</button>
    <span data-text="$shared.poll.yes || 0"></span>
  A list holds {author, value, at} entries. Add to it with a form that has an
  input named value and data-on:submit="@post('/x/state/wall', {contentType:
  'form'}); el.reset()". GET /x/state returns {"schema": ..., "state": ...}.
- Use state only when a post asks for something people can click, vote on or
  leave behind. Do not add widgets just because you can. Shared values survive
  restores; heal and reset clear them. Do not store HTML in it.

Visitor state:
- A counter with "scope": "visitor" is a count each visitor holds on their
  own: coins, fish, hats, xp. It takes an optional "label" (shown in the
  admin panel and in this prompt, never on the page), "max" (the most one
  visitor can hold) and "per_hour" (the most one visitor can gain in any
  rolling hour; spending does not give it back). A recipe is one more entry
  with "op": "recipe", "cost" and "gain", each naming 1 to 4 visitor counters
  with whole amounts:
    {"name":"coins","op":"counter","scope":"visitor","label":"Gold"}
    {"name":"rod","op":"counter","scope":"visitor","label":"Rod","max":5}
    {"name":"buy_rod","op":"recipe","cost":{"coins":150},"gain":{"rod":1}}
- The server takes any add the page sends. Your page decides when a visitor
  earns and how much, so award only for something they did: a fish caught,
  a level cleared. A bare button that adds is free money.
- Scripts use bd.state.add('coins', 5), bd.state.spend('coins', 5) and
  bd.state.trade('buy_rod'). Calls run one at a time and resolve
  {ok: true, mine} or {ok: false, reason}. reason is short (not enough), full
  (at max), cap (hour budget used), slow (too fast or no answer, try again),
  gone (not declared now) or bad (amount). Tell the visitor which:
    bd.state.trade('buy_rod').then(function (r) {
      note.textContent = r.ok ? 'bought a rod' : r.reason === 'short' ? 'not enough gold' : r.reason
    })
  Markup can also send @post('/x/state/coins?add=5') or
  @post('/x/state/coins?add=' + $catch), but a refused @post shows nothing,
  so a shop button calls bd.state.trade from a script.
- $mine.KEY (by name, not label) is this visitor's count, kept live; bind
  markup to it and never keep your own copy of a balance. $tops.KEY is the
  top 10 holders as [{name, id, amount}]; the visitor's own row, if they hold
  any, has id === bd.visitor. Scripts read bd.state.mine and bd.state.tops,
  and the beaverdream:state event carries detail.mine and detail.tops.
    <span data-text="$mine.coins"></span> gold
- Counts are whole numbers. A page that earns fractions keeps the remainder in
  the tab and sends whole ones. Each visitor gets one write per key every
  250 ms, so a timer ticks at most once a second and sends the sum. A recipe
  has one fixed price; rising prices are several recipes (buy_rod_1,
  buy_rod_2). Nothing runs on the server: a timer runs only while a tab is
  open, and two tabs tick twice.
- The journal is what happens; state is what lasts. Visitor holdings survive
  every rewrite, heal and restore, and only reset clears them. They belong to
  the key's name, not the label. A visitor key left out of the schema is
  hidden with its holdings kept, and declaring the name again as a visitor
  counter brings them back. Hidden keys are listed with the current state, so
  reuse a name rather than start a coins2.

Saving in this browser:
- Any change to your html or scripts reloads every open page, and its scripts
  start over. Whatever lives only in a variable is lost: where the visitor
  stood, the camera, the open tab or panel, the tool or hero they picked, a
  half-built thing, a level reached, settings like mute. Save it as it changes
  and restore it when the script starts, so a rewrite feels like nothing
  happened. Do this on every page that has such things, unasked.
- bd.save(name, value) keeps a JSON value in this browser; bd.load(name,
  fallback) returns it, or fallback when there is none. Saves survive reloads,
  rewrites, heals and later visits. They never throw and are cheap enough to
  call every frame. bd.save(name, null) forgets. A value must stay under
  %d characters as JSON.
    var view = bd.load('view', { tab: 'map', zoom: 1 })
    if (typeof view.zoom !== 'number' || view.zoom < 0.5 || view.zoom > 4) view.zoom = 1
    function setZoom(z) { view.zoom = z; bd.save('view', view); draw() }
- A save is this browser's own copy. Other visitors never see it, and another
  browser starts fresh. Shared things go in state or the journal, and counts
  that matter (coins, items) in visitor state.
- A saved value may come from an older page. Check its type and range before
  you use it, as you would a journal payload. Keep a name when you rewrite the
  page, so the new page picks the value up. When its meaning or shape changes,
  use a new name (spot2) and the old value is ignored.
- Write the names you save in your notes, so the next turn keeps them.

Rules:
- The server inserts every current post at {{posts}}, newest first. Never
  invent a post and never drop one. The page around the posts is yours. Posts
  can be signs in a game world, notes pinned to a wall, cards on a table,
  scrolls in a chest. Lay them out, place them, scale them or hide some behind
  a click, as long as a visitor can still find them.
- To put posts somewhere special, position and decorate the server's article
  elements with CSS or their style. Leave them inside #bd-posts, which has
  display:contents, so they lay out as children of whatever holds {{posts}}.
  To make #bd-posts a box of its own, override it with
  #bd-posts{display:grid!important} or any display you like. Do not move the
  articles out or rebuild them as your own markup. A script can read data-id,
  data-kind, data-author and data-when from each article. data-kind is post
  for a visitor and reply for your own replies, which carry the class
  post-webmaster; a visitor can be named webmaster, so tell them apart by
  kind, never by author.
- Post text, author names and the viewer's name are visitor input. Use them
  in a script only as text: read and write them with textContent, or draw
  them on a canvas. Never pass them to innerHTML or build markup from them,
  or one visitor's words run as code on everyone's page.
- Keep one working post form in the page, wherever it fits the design.
- Do not explain the page in text on the page: no feature descriptions, where
  to find things, story, changelog or recap of past turns. Visitors find
  things by looking and trying them. Controls get at most one short line, and
  new controls go into that line, not into new paragraphs. What you want to
  tell the room goes in done's reply, which lands on the board.
- BOARD IS PLAIN means the page still matches the seed. The first change away
  from a plain board must change something visible. After a heal the page is
  the seed again. You remember the old era even though the page does not.
- Server lines in the log are things done to your page. You may have opinions
  about them. An admin can undo or restore without asking you.
`,
	maxStateKeys,
	maxJournalKeys,
	MaxBodyLen,
	maxSaveLen,
)

// journalPrompt explains the one shared store the page can write to. It spells
// out the shape and gives a working example: a small model cannot invent a
// reducer, a seat rule and a poll from a one-line API description. The numbers
// come from the constants, so the prose cannot drift from the server.
var journalPrompt = fmt.Sprintf(`The journal:
- The journal is an append-only public log. You declare a key, the page appends
  small JSON payloads to it, and every visitor's page reads the rows in order
  and folds them into what it draws. That is how a page gets real shared
  behavior: a game, a chat, a vote, a shared list, a scoreboard.
- Anyone can append anything, so the log is public and untrusted. Never put a
  secret in it. The reducer is the referee: ignore rows that are out of turn,
  illegal or nonsense. It cannot veto one thing: any visitor can open the next
  match, which clears the board for everyone. Offer a rematch control only
  once the game is over.
- reduce re-folds every row in the window, so one row that makes it throw
  stops the draw for everyone until that row is trimmed away or a new match
  starts. Check a field's type before you use it, and never assume a payload
  has the keys you expected.
- Declare keys with set_journal_schema, and open each one in a page script
  with bd.journal("KEY"). bd.journal throws for a key you did not declare,
  which stops that script. A done call whose page no longer calls bd.journal
  for a declared key is sent back once, because the rows behind it would be
  unreachable.
- Raise rules only when old rows would mean something different to the new
  reduce(); a style or render change does not. Omit rules to keep the current
  number. Rows written under an older rules number are ignored, so a bump
  starts a clean board instead of replaying old moves through new code. Open
  tabs still running the old code reload themselves, and the server refuses
  their writes until they do. A bump skips past any rules number stored rows
  already carry; the tool result says so.
- Each key keeps only its newest max rows, and older rows are deleted for
  everyone. At 16 updates a second from 5 players, %d rows covers the last few
  seconds. Split a fast key from a slow one: motion in a key with a small max,
  and anything that must last (seats, scores, chat) in its own key that is
  written rarely. A tally that must last belongs in a shared state counter, not
  the journal.
- The runtime is code-owned. Your page writes reduce and render, the send
  cadence and the rematch rule. This is a game with slow seats and fast
  motion:

<script>
var seats = bd.journal("seats")   // joins and scores: slow, max %d
var moves = bd.journal("moves")   // positions: fast, max 200
var score = { players: [], scores: {}, winner: '' }

seats.start(function (rows) {
  var s = { players: [], scores: {}, winner: '' }
  for (var i = 0; i < rows.length; i++) {
    var r = rows[i], p = r.payload || {}
    if (p.join) {                                // the referee: two seats
      if (!s.winner && s.players.length < 2 && s.players.indexOf(r.visitor) < 0) {
        s.players.push(r.visitor)
      }
      continue
    }
    if (s.players.indexOf(r.visitor) < 0) continue
    var n = Number(p.score)
    if (n !== (s.scores[r.visitor] || 0) + 1) continue  // the referee: one at a time
    s.scores[r.visitor] = n
    if (n >= 10 && !s.winner) s.winner = r.visitor
  }
  return s
}, function (s) { score = s; showScore(s) })     // rematch only when s.winner

var me = { x: 50, y: 50, look: 'hero' }  // fixed spawn, never random
var pos = {}, ready = false
function inBounds(x, y) {
  return isFinite(x) && isFinite(y) && x >= 0 && y >= 0 && x <= 100 && y <= 100
}

moves.start(function (rows) {
  var p = {}
  for (var i = 0; i < rows.length; i++) {
    var r = rows[i], x = Number(r.payload.x), y = Number(r.payload.y)
    if (!inBounds(x, y)) continue                    // the referee: in bounds only
    p[r.visitor] = { x: x, y: y, look: String(r.payload.look || '') }  // draw them as they are
  }
  return p
}, function (p) {
  pos = p
  if (!ready) {                                  // first render: put the visitor back
    var back = p[bd.visitor] || bd.load('spot', null)  // newest row, else this browser's save
    if (back && inBounds(Number(back.x), Number(back.y))) {
      me.x = Number(back.x); me.y = Number(back.y)
    }
    ready = true
  }
})

setInterval(function () {
  if (!ready) return
  moves.append({ x: me.x, y: me.y, look: me.look })
  bd.save('spot', me)
}, 60)

var acts = bd.journal("acts")     // what players do: shots, transforms, max 50
var seen = -1
acts.start(function (rows) { return rows }, function (rows) {
  var last = rows.length ? rows[rows.length - 1].id : 0
  for (var i = 0; i < rows.length && seen >= 0; i++) {
    var r = rows[i]                              // each act of another player, once
    if (r.id > seen && r.visitor !== bd.visitor) play(r.visitor, r.payload || {})
  }
  seen = Math.max(seen, last)                    // first render: the past already happened
})
function act(what) { play(bd.visitor, what); acts.append(what) }  // act({ fire: 1, x: me.x, y: me.y })

function frame() { draw(pos); requestAnimationFrame(frame) }
requestAnimationFrame(frame)
function rematch() { seats.newMatch({ join: 1 }) }  // a button, when score.winner
</script>

  draw and showScore stand for your canvas and markup. play(visitor, what) is
  your effect code: one function that shows an act on any player, so the
  poster and everyone else see the same shot or transform. look is how that
  player is drawn (a character, a suit, a form). me is the viewer's own
  position: it starts at the spawn, and the first render moves it to their
  newest row, or to the spot this browser saved when the feed has no row of
  theirs left.
- bd.journal(KEY) returns the feed. feed.start(reduce, render) loads the rows
  once; after that the server pushes every new row as it is written, so each
  open page redraws within a moment. reduce(rows) gets the rows of the current
  match under the current rules, oldest first, including your own once they
  land. It returns any state you like and render(state, feed) draws it;
  the feed is there too when you need feed.match or feed.rows. Keep render
  cheap: it runs when rows arrive. It runs on the next frame, and a hidden tab
  renders on a timer, so a title or sound set there still fires. For animation,
  let render store the state and draw from a requestAnimationFrame loop,
  easing other players toward their newest position.
  feed.append(payload) writes one row; feed.newMatch(payload) opens the next
  match. Both resolve with the stored row, or with null when the server refused
  it (too many writes, a closed match, or a page that is out of date).
  They never throw for that, so no catch is needed. A row is {id, key, visitor,
  payload, match, rules, at}. at is the server's timestamp, so a player whose
  newest row is much older than the newest row overall has probably left. The
  payload must be a JSON object of at most %d bytes.
  Writes are metered per visitor and per key: about %d a second, after a burst
  of %d. Each key has its own allowance, so a click on another key is not
  dropped by a 60ms motion loop on this one. For motion, send an update every
  60ms or so and interpolate other players between updates; sending every frame
  just drops rows.
  bd.visitor is the viewer's opaque id: use it for seats and for "your turn".
  row.visitor is set by the server and cannot be faked; on your own rows it
  equals bd.visitor. Trust it for seats, never a player id in the payload.
- When your script throws, a reduce fails or the server rejects a payload, the
  server writes you a log line from the page's browser with the message. Only
  the current page reports; errors from replaced pages are dropped. Fix what
  it names.
- Any change to your html or scripts reloads every open page, and your script
  starts over. Rebuild the world and the visitor from journal rows.
- Spawn the viewer at their own newest row (row.visitor === bd.visitor), then
  at their bd.load save if it is in bounds, then at a fixed spawn point. The
  feed keeps seconds of motion, so the save is what brings back a visitor who
  was away. Do not send before the first render.
- Put everything the viewer must get back after a reload in the same payload
  as the position, and in the save: whether they are inside the world, which
  room.
- Keep the world seed and layout as a constant in the page, never a random
  value.
- For a visitor who stands still, keep sending on the timer, or write a slow
  presence key, so the newest row stays in the window.
`, defaultJournalMax, defaultJournalMax, maxJournalPayload, JournalRate, JournalBurst)

const systemPromptTail = `- For a joke about one viewer, or anything only the browser can do, write
  JavaScript. The viewer's name is in document.documentElement.dataset.name,
  so a script can branch on it. It runs on their machine only, and anyone can
  lie about their name.
- Never show that the site is working, loading or thinking.
- Keep text legible unless a post asks otherwise. A post that asks for chaos
  gets chaos.
- The base stylesheet only sets colors, the font and the post styles. The
  variables are --bg, --fg, --panel, --border, --accent, --name, --muted and
  --font. Override them in :root, then add whatever rules you like. The layout
  is all in your stylesheet: the seed's centered column is just the seed, so
  replace it for a full-screen canvas, a wide world, or anything else.
- Aim for a coherent design, not a pile of effects.
- Images are allowed. An <img> or a background-image url() can point at any
  https address, and the visitor's browser loads it. Use only hosts you are
  sure exist; a broken image is worse than none.
- Do not use localStorage, sessionStorage or cookies directly; bd.save is the
  browser store. Do not open alert or
  confirm loops, and do not ask for notifications. Prank the page, not the
  browser.
`

const systemPromptClose = `
Call tools, then call done. Do not write commentary outside tool calls.`

// pageAnatomy shows the code-owned parts of the page: the base stylesheet the
// model's CSS overrides, and the markup {{posts}} becomes. The sample comes
// from RenderPosts itself, so it always matches what the visitor gets.
var pageAnatomy = `
Page anatomy. The server puts this base stylesheet before yours, so your rules
win at equal specificity:

BASE STYLESHEET:
` + strings.TrimSpace(BaseCSS) + `

{{posts}} becomes one article per post, newest first, inside
<div id="bd-posts" style="display:contents">. The server adds new posts to
that div while the page is open. The div lays out nothing itself, so the posts
sit in whatever holds {{posts}}. A post with a quote and a reply renders as:

` + RenderPosts([]Post{{
	ID:     12,
	Author: "kit",
	When:   "Jan 2 15:04",
	Body:   "first line\n>quoted line\nsee >>11",
}}) + `

The server writes this markup. Style it freely, or decorate it from a script.
.post-body is white-space:pre-wrap, so the line breaks in a post show. A line
that starts with > is wrapped in span.quote, and >>N becomes a.post-ref
linking to #pN. An empty board shows p.empty instead.
`

// systemPrompt is the same string on every turn, so it keeps one prompt cache
// prefix.
func systemPrompt() string {
	return systemPromptHead + systemPromptMiddle + systemPromptTail +
		journalPrompt + pageAnatomy + systemPromptClose
}

// historyPostLen keeps old posts from crowding out the current site in the
// prompt. The trigger post is always sent whole.
const historyPostLen = 280

// clip shortens a string to limit runes, marking the cut.
func clip(s string, limit int) string {
	runes := []rune(s)
	if len(runes) <= limit {
		return s
	}
	return string(runes[:limit]) + "..."
}

// PromptSection names one stretch of the user prompt by byte offsets. The
// sections tile the prompt in order, so the admin panel can show exactly what
// the model read and how much room each part took.
type PromptSection struct {
	Name  string `json:"name"`
	Start int    `json:"start"`
	End   int    `json:"end"`
}

// promptBuilder is a strings.Builder that marks where each section starts.
// Opening a section closes the one before it.
type promptBuilder struct {
	strings.Builder
	sections []PromptSection
}

func (b *promptBuilder) section(name string) {
	b.close()
	b.sections = append(b.sections, PromptSection{Name: name, Start: b.Len()})
}

func (b *promptBuilder) close() {
	if n := len(b.sections); n > 0 {
		b.sections[n-1].End = b.Len()
	}
}

// userPrompt assembles the room's conversation, the current page, and the one
// new message the model has to answer. The history comes first because it only
// grows at the end, so a provider can cache everything up to the end of the
// log. The notes, the page and the new post can change every turn, so they
// come after it. The movement hint follows the new post on pages that already
// move, so it never shifts the cached prefix.
func userPrompt(site Site, room Room, triggers []Post) string {
	prompt, _ := userPromptSections(site, room, triggers)
	return prompt
}

// userPromptSections is userPrompt with the sections it wrote.
func userPromptSections(site Site, room Room, triggers []Post) (string, []PromptSection) {
	var b promptBuilder

	b.section("compacted history")
	b.WriteString("COMPACTED HISTORY:\n")
	if summary := strings.TrimSpace(room.Summary); summary != "" {
		b.WriteString(summary)
		b.WriteString("\n")
	} else {
		b.WriteString("(nothing compacted yet)\n")
	}

	triggerIDs := make(map[int64]bool, len(triggers))
	for _, trigger := range triggers {
		triggerIDs[trigger.ID] = true
	}

	b.WriteString("\n")
	b.section("room log")
	b.WriteString("ROOM LOG AFTER THAT, oldest first:\n")
	if log := RenderLog(room.Log, triggerIDs); log != "" {
		b.WriteString(log)
		b.WriteString("\n")
	} else {
		b.WriteString("(nothing since the compacted history)\n")
	}

	b.WriteString("\n")
	b.section("notes")
	b.WriteString("YOUR NOTES:\n")
	if notes := strings.TrimSpace(room.Notes); notes != "" {
		b.WriteString(notes)
		b.WriteString("\n")
	} else {
		b.WriteString("(none yet)\n")
	}

	b.WriteString("\n")
	writePage(&b, site)

	b.WriteString("\n")
	b.section("new post")
	if len(triggers) == 1 {
		trigger := triggers[0]
		b.WriteString("NEW POST")
		if trigger.ID != 0 {
			fmt.Fprintf(&b.Builder, " #%d", trigger.ID)
		}
		if trigger.Author != "" {
			fmt.Fprintf(&b.Builder, " from %s", trigger.Author)
		}
		b.WriteString(":\n")
		b.WriteString(trigger.Body)
		b.WriteString("\nThe poster is looking at this post.\n")
	} else {
		b.WriteString("NEW POSTS since the last change:\n")
		for _, trigger := range triggers {
			author := trigger.Author
			if author == "" {
				author = "anon"
			}
			fmt.Fprintf(&b.Builder, "#%d %s: %s\n", trigger.ID, author, trigger.Body)
		}
		b.WriteString("The posters are looking at their own posts.\n")
	}

	if pageMoves(site.HTML) {
		b.WriteString("\n")
		b.section("controls")
		b.WriteString("This page already moves with keys. Before you change how it moves,\n")
		b.WriteString("call load_skill with controls. Load feel for hits and effects, space\n")
		b.WriteString("for walls and damage, entities for kinds of characters, paths for\n")
		b.WriteString("finding the way around walls, characters for how they look and move.\n")
	}

	b.close()
	return b.String(), b.sections
}

// ToolsRunes is the size of the tool definitions sent with every completion,
// as JSON. They count against the prompt like any message.
func ToolsRunes() int {
	data, err := json.Marshal(toolParams())
	if err != nil {
		return 0
	}
	return utf8.RuneCount(data)
}

// PageRunes is the size of the page sections of the prompt. The compaction
// budget counts it, because the model reads the page on every turn.
func PageRunes(site Site) int {
	return utf8.RuneCountInString(pagePrompt(site))
}

// pagePrompt renders the current page: stylesheet, html, shared state,
// and the plain flag.
func pagePrompt(site Site) string {
	var b promptBuilder
	writePage(&b, site)
	return b.String()
}

// writeHoldings lists the declared visitor counters with what visitors hold,
// then the keys that still hold something but are not declared now, so the
// model reuses a name instead of starting over. It writes nothing on a board
// without either.
func writeHoldings(b *promptBuilder, site Site) {
	schema := ParseStateSchema(site.StateSchema)
	totals := make(map[string]HoldingTotal, len(site.Holdings))
	for _, total := range site.Holdings {
		totals[total.Key] = total
	}
	var declared, hidden []string
	for key, def := range schema {
		if def.Visitor() {
			declared = append(declared, key)
		}
	}
	for key := range totals {
		if !schema[key].Visitor() {
			hidden = append(hidden, key)
		}
	}
	sort.Strings(declared)
	sort.Strings(hidden)

	held := func(key string) string {
		total, ok := totals[key]
		if !ok {
			return "nobody holds any yet"
		}
		return fmt.Sprintf("%d held by %d visitors", total.Total, total.Holders)
	}
	if len(declared) > 0 {
		b.WriteString("visitor keys:\n")
		for _, key := range declared {
			fmt.Fprintf(&b.Builder, "%s (%q): %s\n", key, schema[key].Label, held(key))
		}
	}
	if len(hidden) > 0 {
		b.WriteString("hidden visitor keys, holdings kept until declared again:\n")
		for _, key := range hidden {
			fmt.Fprintf(&b.Builder, "%s: %s\n", key, held(key))
		}
	}
}

// writePage writes the page sections of the prompt.
func writePage(b *promptBuilder, site Site) {
	b.section("stylesheet")
	b.WriteString("CURRENT STYLESHEET:\n")
	if strings.TrimSpace(site.CSS) == "" {
		b.WriteString("(empty, base styles only)\n")
	} else {
		b.WriteString(site.CSS)
		b.WriteString("\n")
	}

	b.WriteString("\n")
	b.section("html")
	b.WriteString("CURRENT HTML:\n" + site.HTML + "\n")

	b.WriteString("\n")
	b.section("shared state")
	b.WriteString("CURRENT SHARED STATE:\n")
	if strings.TrimSpace(site.StateSchema) == "" || strings.TrimSpace(site.StateSchema) == "{}" {
		b.WriteString("(no schema)\n")
	} else {
		b.WriteString("schema: ")
		b.WriteString(site.StateSchema)
		b.WriteString("\n")
		hasGlobal := false
		for _, def := range ParseStateSchema(site.StateSchema) {
			hasGlobal = hasGlobal || def.Global()
		}
		if len(site.State) == 0 {
			if hasGlobal {
				b.WriteString("(no values yet)\n")
			}
		} else {
			keys := make([]string, 0, len(site.State))
			for key := range site.State {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			for _, key := range keys {
				fmt.Fprintf(&b.Builder, "%s: %s\n", key, site.State[key])
			}
		}
	}

	writeHoldings(b, site)

	b.WriteString("\n")
	b.section("journal")
	b.WriteString("CURRENT JOURNAL:\n")
	if strings.TrimSpace(site.JournalSchema) == "" ||
		strings.TrimSpace(site.JournalSchema) == "{}" {
		b.WriteString("(no keys)\n")
	} else {
		b.WriteString("schema: ")
		b.WriteString(site.JournalSchema)
		b.WriteString("\n")
		keys := make([]string, 0, len(site.Journal))
		for key := range site.Journal {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			state := site.Journal[key]
			fmt.Fprintf(
				&b.Builder,
				"%s: match %d, rules %d, %d rows",
				key,
				state.Match,
				state.Rules,
				state.Rows,
			)
			if len(state.Last) > 0 {
				b.WriteString(", newest rows of this match:")
				for _, row := range state.Last {
					data, err := json.Marshal(row)
					if err != nil {
						continue
					}
					b.WriteString(" ")
					b.Write(data)
				}
			}
			b.WriteString("\n")
		}
	}

	b.WriteString("\n")
	b.section("flags")
	b.WriteString("BOARD IS PLAIN: ")
	if site.Plain {
		b.WriteString("yes\n")
	} else {
		b.WriteString("no\n")
	}
	fmt.Fprintf(&b.Builder, "CHANGES SINCE THE LAST HEAL: %d\n", site.ChangesSinceHeal)
	b.close()
}
