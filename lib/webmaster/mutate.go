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

// maxOutputTokens caps a single completion. High enough that a region or a
// stylesheet can fill its byte limit in one tool call. A runaway stylesheet is
// still the most expensive failure this toy has.
const maxOutputTokens = 8000

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
// entries carry the stats of the round that produced them.
type Message struct {
	Role       string      `json:"role"`
	Content    string      `json:"content,omitempty"`
	Reasoning  string      `json:"reasoning,omitempty"`
	ToolCallID string      `json:"tool_call_id,omitempty"`
	ToolCalls  []ToolCall  `json:"tool_calls,omitempty"`
	Stats      *RoundStats `json:"stats,omitempty"`
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
	usr := userPrompt(current, room, triggers)

	res := Result{Site: current, Messages: []Message{
		{Role: "system", Content: sys},
		{Role: "user", Content: usr},
	}}

	messages := []openai.ChatCompletionMessageParamUnion{
		openai.SystemMessage(sys),
		openai.UserMessage(usr),
	}

	hiddenWarned := false
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
			m.reportRound(&res, stats)
			break
		}

		reply := resp.Choices[0].Message
		entry := Message{
			Role:      "assistant",
			Content:   reply.Content,
			Reasoning: reasoningOf(reply),
		}
		if len(reply.ToolCalls) == 0 {
			m.reportRound(&res, stats)
			entry.Stats = &stats
			res.Messages = append(res.Messages, entry)
			break
		}

		assistant := openai.ChatCompletionAssistantMessageParam{
			Content: openai.ChatCompletionAssistantMessageParamContentUnion{
				OfString: openai.String(reply.Content),
			},
		}
		ed.rejected = 0
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
		m.reportRound(&res, stats)
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
		}
		if deferred != "" {
			ed.done = false
			for i := range entry.ToolCalls {
				if entry.ToolCalls[i].Name == "done" {
					entry.ToolCalls[i].Result = deferred
				}
			}
		}

		res.Messages = append(res.Messages, entry)
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
		res.Reason = fmt.Sprintf("no done call after %d rounds", m.maxRounds)
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

// reportRound records one finished completion on the result and tells the
// listener.
func (m *Mutator) reportRound(res *Result, stats RoundStats) {
	res.Rounds = append(res.Rounds, stats)
	if m.OnRoundDone != nil {
		m.OnRoundDone(stats)
	}
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
	return openai.ChatCompletionMessageToolCallUnionParam{
		OfFunction: &openai.ChatCompletionMessageFunctionToolCallParam{
			ID: call.ID,
			Function: openai.ChatCompletionMessageFunctionToolCallFunctionParam{
				Name:      call.Name,
				Arguments: call.Arguments,
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

const systemPromptMiddle = `- The board must always contain the literal placeholder {{posts}}, which is
  where the real posts are inserted. An edit that removes it is rejected.
- To restyle a little, append rules: later rules win over earlier ones, so
  edit(css, "", ":root{--accent:#c00}") recolors the page. Prefer small edits
  over resending a whole section. Resending costs the room time.
- set_state_schema(keys): declare shared values the page can read and write.
  Ops are counter, list (keep the newest max entries) and choice (tally one of
  the options). Up to 8 keys. Declare only what the page actually uses.
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

<form class="postbox" method="post" action="/posts">
  <p class="postbox-title">post a message</p>
  <textarea name="body" required maxlength="2000" rows="3" placeholder="say something"></textarea>
  <div class="postbox-actions"><span>posting as {{name}}</span>
  <button type="submit">post</button></div>
</form>

The server records the visitor's cookie name as the post author. Do not add a
name field: the server ignores it. The page can use {{name}} to show the
visitor's name; the server replaces it with the escaped cookie name. The
.postbox rules in the base stylesheet style this box. Style it, move it, wrap
it, make it strange. Keep the method, action and field names working unless the
post asks you to break posting.

Shared state:
- The current values arrive in the prompt, and the page gets them in a JSON
  script tag with id="bd-state".
- GET /x/state returns {"schema": ..., "state": ...}. POST /x/state/KEY with a
  value=... form field applies that key's op for the current visitor.
- Use state only when a post asks for something people can click, vote on or
  leave behind. Do not add widgets just because you can. State survives
  restores; reset clears it. Do not store HTML in it.

Rules:
- The board region must render every post you were given. The server inserts
  the current posts newest first. Never invent a post and never drop one.
- Keep one working post form in the page, in whichever region fits the design.
- When you do change it, keep the site readable as a message board at first.
  Get bolder as the board gets busier.
- BOARD IS PLAIN means the page still matches the seed. The first change away
  from a plain board must change something visible. After a quiet board heals,
  start gentle again. You remember the old era even though the page does not.
- Server lines in the log are things done to your page. You may have opinions
  about them. An admin can undo or restore without asking you.
`

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

{{posts}} becomes one article per post, newest first. A post with a quote and
a reply renders as:

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
		pageAnatomy + systemPromptClose
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

// userPrompt assembles the room's conversation, the current page, and the one
// new message the model has to answer. The history comes first because it only
// grows at the end, so a provider can cache everything up to the end of the
// log. The notes, the page and the new post can change every turn, so they
// come after it.
func userPrompt(site Site, room Room, triggers []Post) string {
	var b strings.Builder

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

	b.WriteString("\nROOM LOG AFTER THAT, oldest first:\n")
	if log := RenderLog(room.Log, triggerIDs); log != "" {
		b.WriteString(log)
		b.WriteString("\n")
	} else {
		b.WriteString("(nothing since the compacted history)\n")
	}

	b.WriteString("\nYOUR NOTES:\n")
	if notes := strings.TrimSpace(room.Notes); notes != "" {
		b.WriteString(notes)
		b.WriteString("\n")
	} else {
		b.WriteString("(none yet)\n")
	}

	b.WriteString("\n")
	b.WriteString(pagePrompt(site))

	if len(triggers) == 1 {
		trigger := triggers[0]
		b.WriteString("\nNEW POST")
		if trigger.ID != 0 {
			fmt.Fprintf(&b, " #%d", trigger.ID)
		}
		if trigger.Author != "" {
			fmt.Fprintf(&b, " from %s", trigger.Author)
		}
		b.WriteString(":\n")
		b.WriteString(trigger.Body)
		b.WriteString("\nThe poster is looking at this post.\n")
	} else {
		b.WriteString("\nNEW POSTS since the last change:\n")
		for _, trigger := range triggers {
			author := trigger.Author
			if author == "" {
				author = "anon"
			}
			fmt.Fprintf(&b, "#%d %s: %s\n", trigger.ID, author, trigger.Body)
		}
		b.WriteString("The posters are looking at their own posts.\n")
	}

	return b.String()
}

// PageRunes is the size of the page sections of the prompt. The compaction
// budget counts it, because the model reads the page on every turn.
func PageRunes(site Site) int {
	return utf8.RuneCountInString(pagePrompt(site))
}

// pagePrompt renders the current page: stylesheet, regions, shared state,
// and the plain flag.
func pagePrompt(site Site) string {
	var b strings.Builder

	b.WriteString("CURRENT STYLESHEET:\n")
	if strings.TrimSpace(site.CSS) == "" {
		b.WriteString("(empty, base styles only)\n")
	} else {
		b.WriteString(site.CSS)
		b.WriteString("\n")
	}

	b.WriteString("\nCURRENT REGIONS:\n")
	fmt.Fprintf(&b, "header:\n%s\n\nboard:\n%s\n\nfooter:\n%s\n",
		site.Header, site.Board, site.Footer)

	b.WriteString("\nCURRENT SHARED STATE:\n")
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
				fmt.Fprintf(&b, "%s: %s\n", key, site.State[key])
			}
		}
	}

	b.WriteString("\nBOARD IS PLAIN: ")
	if site.Plain {
		b.WriteString("yes\n")
	} else {
		b.WriteString("no\n")
	}
	fmt.Fprintf(&b, "CHANGES SINCE THE LAST HEAL: %d\n", site.ChangesSinceHeal)
	return b.String()
}

// reasoningOf pulls "reasoning_content" out of an OpenAI-compatible reply. It
// is not part of the official schema, so it lands in the extra fields.
func reasoningOf(msg openai.ChatCompletionMessage) string {
	field, ok := msg.JSON.ExtraFields["reasoning_content"]
	if !ok {
		return ""
	}
	var content string
	if err := json.Unmarshal([]byte(field.Raw()), &content); err != nil {
		return ""
	}
	return content
}
