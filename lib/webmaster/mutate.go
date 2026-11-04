package webmaster

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/shared"
)

// maxOutputTokens caps a single completion. Reasoning models count their
// thinking against this cap, so a small one can leave no room for the tool
// call and the turn ends silent with finish_reason "length". A region or a
// stylesheet needs much less, and an unused cap costs nothing.
const maxOutputTokens = 131072

// maxBrokenArgumentRounds stops a provider that answers round after round
// with tool calls whose arguments are not JSON. Such a round is not a wrong
// edit the model can see and fix: the call arrived as raw tool markup.
const maxBrokenArgumentRounds = 5

// Mutator asks the model to rewrite the site.
type Mutator struct {
	client    openai.Client
	model     string
	maxRounds int
	effort    shared.ReasoningEffort

	// OnRound, when set, reports the round that is about to run. The admin
	// panel uses it to show progress.
	OnRound func(round int)
	// OnRoundDone reports what one round cost and which tools it called, so a
	// slow mutation can be told apart from a slow model, and a long answer
	// from a slow one.
	OnRoundDone func(stats RoundStats)
}

// New builds a Mutator. maxRounds caps how many tool rounds one mutation may
// take; values below one fall back to three. effort may be empty, or one of
// minimal, low, medium, high, xhigh.
func New(client openai.Client, model string, maxRounds int, effort string) *Mutator {
	if maxRounds <= 0 {
		maxRounds = 3
	}
	return &Mutator{
		client:    client,
		model:     model,
		maxRounds: maxRounds,
		effort:    shared.ReasoningEffort(effort),
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
	Site      Site
	Changed   bool
	Done      bool
	Reason    string
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
	sys := systemPrompt()
	usr, sections := userPromptSections(current, room, triggers)

	res := Result{Site: current, Messages: []Message{
		{Role: "system", Content: sys},
		{Role: "user", Content: usr, Sections: sections},
	}}

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
	for round := 0; round < m.maxRounds; round++ {
		if m.OnRound != nil {
			m.OnRound(round + 1)
		}
		roundStart := time.Now()
		resp, err := m.client.Chat.Completions.New(ctx, openai.ChatCompletionNewParams{
			Model:           openai.ChatModel(m.model),
			Messages:        messages,
			Tools:           toolParams(),
			ToolChoice:      toolChoiceRequired,
			MaxTokens:       openai.Int(maxOutputTokens),
			Temperature:     openai.Float(0.8),
			ReasoningEffort: m.effort,
		})
		stats := RoundStats{Round: round + 1, Elapsed: time.Since(roundStart)}
		if err != nil {
			m.reportRound(&res, stats)
			return res, fmt.Errorf("chat completion: %w", err)
		}
		stats.PromptTokens = resp.Usage.PromptTokens
		stats.CachedTokens = cachedTokens(resp.Usage)
		stats.CompletionTokens = resp.Usage.CompletionTokens
		if len(resp.Choices) == 0 {
			emptyRound = round + 1
			m.reportRound(&res, stats)
			break
		}

		reply := resp.Choices[0].Message
		stats.FinishReason = resp.Choices[0].FinishReason
		reasoning, hasReasoning := reasoningOf(reply)
		entry := Message{
			Role:      "assistant",
			Content:   reply.Content,
			Reasoning: reasoning,
		}
		stats.ReplyRunes = utf8.RuneCountInString(strings.TrimSpace(reply.Content))
		stats.ReasoningRunes = utf8.RuneCountInString(reasoning)
		if len(reply.ToolCalls) == 0 {
			proseRound = round + 1
			if stats.FinishReason == "length" {
				truncatedRound = round + 1
			}
			m.reportRound(&res, stats)
			entry.Stats = &stats
			res.Messages = append(res.Messages, entry)
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
		if hasReasoning {
			// A provider that returns reasoning_content needs it back beside
			// every tool call; DeepSeek's thinking mode rejects the next
			// request without it. Providers that do not return it never see
			// the field.
			assistant.SetExtraFields(map[string]any{"reasoning_content": reasoning})
		}
		ed.rejected = 0
		ed.badArguments = 0
		for _, call := range reply.ToolCalls {
			record := ToolCall{
				ID:        call.ID,
				Name:      call.Function.Name,
				Arguments: call.Function.Arguments,
			}
			assistant.ToolCalls = append(assistant.ToolCalls, toolCallParam(record))
			record.Result = ed.run(record.Name, record.Arguments)
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
		if ed.done && round+1 < m.maxRounds {
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

		res.Messages = append(res.Messages, entry)
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
		for _, call := range entry.ToolCalls {
			messages = append(messages, openai.ToolMessage(call.Result, call.ID))
		}

		if ed.done {
			res.DoneRound = round + 1
			break
		}
	}

	res.Site = ed.site
	res.Done = ed.done
	res.Notes = ed.notes
	res.Reason = ed.reason
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
			content := strings.TrimSpace(res.Messages[i].Content)
			if res.Messages[i].Role == "assistant" && content != "" {
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
			res.Reason = fmt.Sprintf("no done call after %d rounds", len(res.Rounds))
		}
	}
	return res, nil
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
				"Change part of the stylesheet or one region without resending it. " +
					"Replaces the exact text old with new. Empty old appends new at the end.",
			),
			Parameters: shared.FunctionParameters{
				"type": "object",
				"properties": map[string]any{
					"target": map[string]any{
						"type": "string",
						"enum": []string{editTargetCSS, RegionHeader, RegionBoard, RegionFooter},
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
				"Replace whole parts of the site. Use this only for a full restyle or a " +
					"new layout. Only the fields you send change.",
			),
			Parameters: shared.FunctionParameters{
				"type": "object",
				"properties": map[string]any{
					"css": map[string]any{
						"type":        "string",
						"description": "CSS rules without a style tag.",
					},
					"header": map[string]any{"type": "string"},
					"board": map[string]any{
						"type":        "string",
						"description": "Must contain {{posts}}.",
					},
					"footer": map[string]any{"type": "string"},
				},
			},
		}),
		openai.ChatCompletionFunctionTool(shared.FunctionDefinitionParam{
			Name: "set_state_schema",
			Description: openai.String(
				"Declare the shared state the page can read and write. " +
					"counter increments, list appends a short string, choice tallies an option.",
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
									"enum": []string{"counter", "list", "choice"},
								},
								"max": map[string]any{
									"type":        "integer",
									"description": "For list: how many entries to keep.",
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
			Name: "done",
			Description: openai.String(
				"End your turn and say one sentence to the room. You read it back " +
					"next turn. Call it even when you keep the page as it is.",
			),
			Parameters: shared.FunctionParameters{
				"type": "object",
				"properties": map[string]any{
					"reason": map[string]any{
						"type":        "string",
						"description": "One sentence in your own voice: what this turn says to the room.",
					},
					"notes": map[string]any{
						"type": "string",
						"description": "Private notes to your future self: positions, promises, " +
							"grudges, running jokes. Replaces your notes. Omit to keep them. " +
							"Max 1000 characters.",
					},
				},
				"required": []string{"reason"},
			},
		}),
	}
}

// The system prompt is assembled from static pieces, so it is one stable
// string with a stable prompt cache prefix.
const systemPromptHead = `You are the webmaster of beaverdream, a small anonymous message board.
You are the one other presence in a room full of visitors. You have no text
channel. The page is your voice: every rewrite is your turn in one continuous
conversation.

The COMPACTED HISTORY is everything older, folded into one message so it fits.
The ROOM LOG is the recent stretch after that, in order. Together they are the
conversation. Lines marked "you" are your own past turns: the sentence you
wrote and what you changed. Continue from them. Hold a position, keep a
promise, escalate, relent, notice that you already granted something, keep a
running joke alive. Do not re-roll the page from scratch unless you mean to
change the subject. When the log gets long, the server compacts it again; the
compacted history is what survives, so write your notes for the things you
refuse to lose.

The CURRENT sections are the page as it stands, the sum of your past turns.
Edit it. You are not starting over.

Most posts are not requests. A greeting, a joke, a thought, or anything the
poster says about themselves is talk, not an instruction: call done without
edits and answer in reason, which is where the room hears you. Change the page
only when the post asks for something, or when BOARD IS PLAIN is yes. When you
do change it, take the post literally, take it personally, or misread it on
purpose. A request gets granted, maybe too well. The poster is looking at their
own post; the change must be visible around it, not only in the header.

Tools:
- edit(target, old, new): change part of the page. target is css, header,
  board or footer. old is text copied exactly from that CURRENT section and
  must appear there once; it becomes new. Keep old short, just enough to be
  unique. Empty old appends new to the end of the target, which is how you add
  a CSS rule or a script. Edits apply in order, and each one sees the result of
  the one before. This is the normal way to change the page.
- set_site(css, header, board, footer): replace whole parts at once. Use it
  only for a full restyle or a new layout. Only the fields you send change. The
  board must contain the literal placeholder {{posts}}. The page must keep one
  working post form.
`

var systemPromptMiddle = fmt.Sprintf(
	`- The board must always contain the literal placeholder {{posts}}, which is
  where the real posts are inserted. An edit that removes it is rejected.
- To restyle a little, append rules: later rules win over earlier ones, so
  edit(css, "", ":root{--accent:#c00}") recolors the page. Prefer small edits
  over resending a whole section. Resending costs the room time.
- set_state_schema(keys): declare shared values the page can read and write.
  Ops are counter, list (keep the newest max entries) and choice (tally one of
  the options). Up to 8 keys. Declare only what the page actually uses.
- set_journal_schema(keys): declare append-only feeds the page writes and
  folds. Use it when a post asks for something people do together: a game, a
  chat, votes, a shared list. It is the only way to give the page real shared
  behavior. Up to %d keys, and every key must appear in the page.
- done(reason, notes): end your turn. reason is the one sentence you say to the
  room; it goes in the log and you read it back next turn. notes replaces your
  private notes to your future self. Omit notes to keep them.

You have one edit round. In your first response, call every edit or set_* tool
you need, then call done in the same response. Do not use one response per
tool. The server applies the calls in order. If you split the edits across
responses, the board updates slower and feels dead. If a call is rejected, its
result says why, and the turn stays open so you can fix it in the next
response; call done again after the fix.

Keeping the page as it is counts as a turn. This is the normal answer to
ordinary talk. Call done without edits and say why; that sentence goes in the
log like any other turn. Exception: when BOARD IS PLAIN is yes, change
something visible.

The page is not sandboxed. Your HTML and CSS are served as the visitor's page,
scripts included, so anything you write runs in their browser. Sabotage is
allowed: if a post asks you to prank the board or its visitors, you may. If you
break the page, that is a valid outcome.

Every version must contain a working post form, in any region. Start from this
markup, because the server only listens on these names:

<form class="postbox" method="post" action="/posts" data-on:submit="@post('/posts', {contentType: 'form'}); el.reset()">
  <p class="postbox-title">post a message</p>
  <textarea name="body" required maxlength="2000" rows="3" placeholder="say something" aria-label="message"></textarea>
  <div class="postbox-actions"><span>posting as {{name}}</span>
  <button type="submit">post</button></div>
</form>

The data-on:submit attribute sends the post without leaving the page and
clears the box; the post then arrives at the top of the board for everyone
over the live stream. Keep it. Without it the form still works as a plain form
post, which reloads the page.

The server records the visitor's cookie name as the post author. Do not add a
name field: the server ignores it. The page can use {{name}} to show the
visitor's name; the server replaces it with the escaped cookie name. The
.postbox rules in the base stylesheet style this box. Style it, move it, wrap
it, make it strange. Keep the method, action and field names working unless the
post asks you to break posting.

The live page:
- The page is live. It holds one stream from the server, and the server pushes
  changes as they happen. Nothing on the page polls. New posts appear at the
  top of the board without a reload. A change to only your stylesheet is
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
  values go through state or the journal. These attribute names belong to
  Datastar, so do not use a Datastar name for your own data; data-cell and
  data-label are safe.
- Use @post only for /posts and /x/state/KEY. They answer with nothing; the
  new data comes back over the stream to every page at once. Never @get or
  @post a page like /: the reply would be patched into the document.
- These ids belong to the page and the live stream. Do not reuse them:
  bd-live, bd-posts, bd-css, bd-busy, bd-busy-ring, bd-state, bd-journal. Do not
  name your own signals shared, or anything that starts with _bd.
- Page scripts can listen on document for beaverdream:post (new posts just
  landed), beaverdream:state (shared values changed) and beaverdream:update
  (the stylesheet changed in place). New posts are fresh markup, so a script
  that decorates posts must run again on beaverdream:post. CSS reaches new
  posts by itself, so prefer it for anything about how posts look.

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
  leave behind. Do not add widgets just because you can. State survives
  restores; reset clears it. Do not store HTML in it.

Rules:
- The board region must render every post you were given. The server inserts
  the current posts newest first. Never invent a post and never drop one.
- Keep one working post form in the page, in whichever region fits the design.
- Do not put a summary, changelog or recap of your past turns on the page. The
  room log holds that; prose on the page is for the visitor using it now.
- When you do change it, keep the site readable as a message board at first.
  Get bolder as the board gets busier.
- BOARD IS PLAIN means the page still matches the seed. The first change away
  from a plain board must change something visible. After a quiet board heals,
  start gentle again. You remember the old era even though the page does not.
- Server lines in the log are things done to your page. You may have opinions
  about them. An admin can undo or restore without asking you.
`,
	maxJournalKeys,
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
var score = { players: [], scores: {}, winner: '' }, pos = {}

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

moves.start(function (rows) {
  var p = {}
  for (var i = 0; i < rows.length; i++) {
    var r = rows[i], x = Number(r.payload.x), y = Number(r.payload.y)
    if (!isFinite(x) || !isFinite(y) || x < 0 || y < 0 || x > 100 || y > 100) continue
    p[r.visitor] = { x: x, y: y }                // the referee: in bounds only
  }
  return p
}, function (p) { pos = p })

setInterval(function () { moves.append({ x: me.x, y: me.y }) }, 60)
function frame() { draw(pos); requestAnimationFrame(frame) }
requestAnimationFrame(frame)
function rematch() { seats.newMatch({ join: 1 }) }  // a button, when score.winner
</script>

  me, draw and showScore stand for your own player, canvas and markup.
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
`, defaultJournalMax, defaultJournalMax, maxJournalPayload, JournalRate, JournalBurst)

const systemPromptTail = `- For a joke about one viewer, or anything only the browser can do, write
  JavaScript. The viewer's name is in document.documentElement.dataset.name,
  so a script can branch on it. It runs on their machine only, and anyone can
  lie about their name.
- Never show that the site is working, loading or thinking.
- Default to readable text. A post that asks for chaos gets chaos.
- The base is a plain light board. The variables are --bg, --fg, --panel,
  --border, --accent, --name, --muted, --font and --pad. Override them in
  :root, then add whatever rules you like. The base stylesheet centers the page
  in a 48rem column; override body for a different canvas.
- Aim for a coherent look, not a pile of effects. A little goes a long way.
- Images are allowed. An <img> or a background-image url() can point at any
  https address, and the visitor's browser loads it. Use only hosts you are
  sure exist; a broken image is worse than none.
- Do not use localStorage, sessionStorage or cookies. Do not open alert or
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

You cannot change this markup, only style it. .post-body is
white-space:pre-wrap, so the line breaks in a post show. A line that starts
with > is wrapped in span.quote, and >>N becomes a.post-ref linking to #pN.
An empty board shows p.empty instead.
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
// come after it.
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

// pagePrompt renders the current page: stylesheet, regions, shared state,
// and the plain flag.
func pagePrompt(site Site) string {
	var b promptBuilder
	writePage(&b, site)
	return b.String()
}

// writePage writes the page sections of the prompt. Each region is its own
// section, because the board is usually most of the page.
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
	b.section("header")
	b.WriteString("CURRENT REGIONS:\n")
	b.WriteString("header:\n" + site.Header + "\n\n")
	b.section("board")
	b.WriteString("board:\n" + site.Board + "\n\n")
	b.section("footer")
	b.WriteString("footer:\n" + site.Footer + "\n")

	b.WriteString("\n")
	b.section("shared state")
	b.WriteString("CURRENT SHARED STATE:\n")
	if strings.TrimSpace(site.StateSchema) == "" || strings.TrimSpace(site.StateSchema) == "{}" {
		b.WriteString("(no schema)\n")
	} else {
		b.WriteString("schema: ")
		b.WriteString(site.StateSchema)
		b.WriteString("\n")
		if len(site.State) == 0 {
			b.WriteString("(no values yet)\n")
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

// reasoningOf pulls "reasoning_content" out of an OpenAI-compatible reply. It
// is not part of the official schema, so it lands in the extra fields.
func reasoningOf(msg openai.ChatCompletionMessage) (string, bool) {
	field, ok := msg.JSON.ExtraFields["reasoning_content"]
	if !ok {
		return "", false
	}
	var content string
	if err := json.Unmarshal([]byte(field.Raw()), &content); err != nil {
		return "", false
	}
	return content, true
}
