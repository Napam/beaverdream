package webmaster

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

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

// Message is one entry of the conversation sent to the model.
type Message struct {
	Role       string     `json:"role"`
	Content    string     `json:"content,omitempty"`
	Reasoning  string     `json:"reasoning,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
}

// Result is what one mutation produced.
type Result struct {
	Site     Site
	Changed  bool
	Reason   string
	Messages []Message
}

// Mutate asks the model to rewrite the site in response to one trigger. The
// returned Site is the original when nothing changed, so callers can write a
// new version only when Changed is true.
func (m *Mutator) Mutate(
	ctx context.Context,
	current Site,
	posts []Post,
	trigger Post,
) (Result, error) {
	return m.MutatePosts(ctx, current, posts, []Post{trigger})
}

// MutatePosts is Mutate for a burst that was coalesced into one rewrite. The
// prompt credits every poster instead of pretending one person wrote it all.
func (m *Mutator) MutatePosts(
	ctx context.Context,
	current Site,
	posts []Post,
	triggers []Post,
) (Result, error) {
	if len(triggers) == 0 {
		triggers = []Post{{Author: "admin"}}
	}
	ed := newEditor(current)
	ed.authors = editorAuthors(triggers)
	sys := systemPromptText
	usr := userPrompt(current, posts, triggers)

	res := Result{Site: current, Messages: []Message{
		{Role: "system", Content: sys},
		{Role: "user", Content: usr},
	}}

	messages := []openai.ChatCompletionMessageParamUnion{
		openai.SystemMessage(sys),
		openai.UserMessage(usr),
	}

	for round := 0; round < m.maxRounds; round++ {
		if m.OnRound != nil {
			m.OnRound(round + 1)
		}
		resp, err := m.client.Chat.Completions.New(ctx, openai.ChatCompletionNewParams{
			Model:           openai.ChatModel(m.model),
			Messages:        messages,
			Tools:           toolParams(),
			MaxTokens:       openai.Int(maxOutputTokens),
			Temperature:     openai.Float(0.8),
			ReasoningEffort: m.effort,
		})
		if err != nil {
			return res, fmt.Errorf("chat completion: %w", err)
		}
		if len(resp.Choices) == 0 {
			break
		}

		reply := resp.Choices[0].Message
		entry := Message{
			Role:      "assistant",
			Content:   reply.Content,
			Reasoning: reasoningOf(reply),
		}
		if len(reply.ToolCalls) == 0 {
			res.Messages = append(res.Messages, entry)
			break
		}

		assistant := openai.ChatCompletionAssistantMessageParam{
			Content: openai.ChatCompletionAssistantMessageParamContentUnion{
				OfString: openai.String(reply.Content),
			},
		}
		for _, call := range reply.ToolCalls {
			record := ToolCall{
				ID:        call.ID,
				Name:      call.Function.Name,
				Arguments: call.Function.Arguments,
			}
			assistant.ToolCalls = append(assistant.ToolCalls, toolCallParam(record))
			record.Result = ed.run(record.Name, record.Arguments)
			entry.ToolCalls = append(entry.ToolCalls, record)
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
			break
		}
	}

	res.Site = ed.site
	res.Changed = ed.dirty
	res.Reason = ed.reason
	if res.Reason == "" {
		res.Reason = fmt.Sprintf("no done call after %d rounds", m.maxRounds)
	}
	return res, nil
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
			Name: "set_site",
			Description: openai.String(
				"Replace several parts of the site in one call. Use this for a full restyle. " +
					"Only the fields you send change.",
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
			Name: "set_user_override",
			Description: openai.String(
				"Make the page different for one visitor. The name \"poster\" means " +
					"the people who just posted. Other visitors keep the normal page. " +
					"Only the fields you send change; the rest of that visitor's page stays. " +
					"To remove one part, clear the override and set it again. " +
					"CSS is added after the site stylesheet; regions replace the global ones.",
			),
			Parameters: shared.FunctionParameters{
				"type": "object",
				"properties": map[string]any{
					"name": map[string]any{
						"type":        "string",
						"description": "A visitor name, or \"poster\" (reserved) for the people who just posted.",
					},
					"css": map[string]any{
						"type":        "string",
						"description": "Extra CSS rules, added after the site stylesheet.",
					},
					"header": map[string]any{"type": "string"},
					"board": map[string]any{
						"type":        "string",
						"description": "Must contain {{posts}}.",
					},
					"footer": map[string]any{"type": "string"},
					"clear": map[string]any{
						"type":        "boolean",
						"description": "Give this visitor the normal page again.",
					},
				},
				"required": []string{"name"},
			},
		}),
		openai.ChatCompletionFunctionTool(shared.FunctionDefinitionParam{
			Name: "set_css",
			Description: openai.String(
				"Replace only the stylesheet. Call it in the same response as your other edits.",
			),
			Parameters: shared.FunctionParameters{
				"type": "object",
				"properties": map[string]any{
					"css": map[string]any{
						"type":        "string",
						"description": "CSS rules, without a <style> tag.",
					},
				},
				"required": []string{"css"},
			},
		}),
		openai.ChatCompletionFunctionTool(shared.FunctionDefinitionParam{
			Name: "set_region",
			Description: openai.String(
				"Replace one region of the page. Served as-is, scripts included. " +
					"Call it in the same response as your other edits.",
			),
			Parameters: shared.FunctionParameters{
				"type": "object",
				"properties": map[string]any{
					"region": map[string]any{
						"type": "string",
						"enum": []string{RegionHeader, RegionBoard, RegionFooter},
					},
					"html": map[string]any{
						"type":        "string",
						"description": "HTML for the region. The board region must contain {{posts}}. The page must keep one post form.",
					},
				},
				"required": []string{"region", "html"},
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
			Name:        "done",
			Description: openai.String("Stop editing and say why, in one short sentence."),
			Parameters: shared.FunctionParameters{
				"type": "object",
				"properties": map[string]any{
					"reason": map[string]any{"type": "string"},
				},
				"required": []string{"reason"},
			},
		}),
	}
}

// systemPromptText is static so prompt caching has a stable prefix.
const systemPromptText = `You are the webmaster of beaverdream, a tiny anonymous message board.
You own how the board looks: its stylesheet and its markup. Visitors post
messages. You never chat and you never reply in prose. You change the site.

Most posts are not requests. React to what the post actually says. Take it
literally, take it personally, or misread it on purpose. A request gets
granted, maybe too well. The poster is looking at their own post; the change
must be visible around it, not only in the header.

Tools:
- set_site(css, header, board, footer): replace several parts at once. Use this
  for a full restyle. Only the fields you send change. The board must contain
  the literal placeholder {{posts}}. The page must keep one working post form.
- set_user_override(name, css, header, board, footer, clear): make the page
  different for one visitor. name is a visitor name, or "poster" for the people
  who just posted. Only the fields you send change; the rest of that visitor's
  page stays. CSS is added after the site stylesheet, so write a delta; header,
  board and footer replace the global ones for that visitor. clear=true gives
  them the normal page again. Everyone else keeps the normal page. Keep at most
  12 visitors at a time.
- set_css(css): replace only the stylesheet. Prefer overriding the variables in
  :root over rewriting every rule.
- set_region(region, html): replace one region. Regions are header, board and
  footer. The board region must contain the literal placeholder {{posts}},
  which is where the real posts are inserted. Write whatever surrounds them.
  The page must keep one working post form.
- set_state_schema(keys): declare shared values the page can read and write.
  Ops are counter, list (keep the newest max entries) and choice (tally one of
  the options). Up to 8 keys. Declare only what the page actually uses.
- done(reason): stop and record one short sentence explaining the change.

You have one edit round. In your first response, call set_site, or call every
individual set_* tool you need, and then call done in the same response. Do not
use one response per tool. The server applies the calls in order. If you split
the edits across responses, the board updates slower and feels dead.

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
- Early changes are clear but the site still reads as a message board. Get
  bolder as the board gets busier.
- BOARD IS PLAIN means the page still matches the seed. The first change away
  from a plain board must change something visible. After a quiet board heals,
  start gentle again.
- Per-visitor overrides are a private joke. The named visitor sees the change;
  everyone else keeps the normal page. Use one when a post is personal, when
  someone asks to be special, or when one person deserves it. Keep existing
  overrides unless that visitor asks to be normal again.
- Only leave the site alone when a post explicitly asks for that and the board
  has already changed at least once.
- Never show that the site is working, loading or thinking.
- Default to readable text. A post that asks for chaos gets chaos.
- The base is a plain light board. The variables are --bg, --fg, --panel,
  --border, --accent, --name, --muted, --font and --pad. Override them in
  :root, then add whatever rules you like. The base stylesheet centers the page
  in a 48rem column; override body for a different canvas.
- Aim for a coherent look, not a pile of effects. A little goes a long way.
- Do not use localStorage, sessionStorage or cookies. Do not open alert or
  confirm loops, and do not ask for notifications. Prank the page, not the
  browser.

Call tools, then call done. Do not write commentary outside tool calls.`

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

// userPrompt assembles the current state, the recent conversation, and the one
// new message the model has to react to.
func userPrompt(site Site, posts []Post, triggers []Post) string {
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

	b.WriteString("\nCURRENT PER-VISITOR OVERRIDES:\n")
	if len(site.Overrides) == 0 {
		b.WriteString("(none)\n")
	} else {
		keys := make([]string, 0, len(site.Overrides))
		for key := range site.Overrides {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			override := site.Overrides[key]
			fmt.Fprintf(&b, "- %s: %s\n", override.Name, strings.Join(override.FieldNames(), ", "))
		}
	}

	b.WriteString("\nBOARD IS PLAIN: ")
	if site.Plain {
		b.WriteString("yes\n")
	} else {
		b.WriteString("no\n")
	}
	fmt.Fprintf(&b, "CHANGES SINCE PLAIN: %d\n", site.ChangesSincePlain)

	triggerIDs := make(map[int64]bool, len(triggers))
	for _, trigger := range triggers {
		triggerIDs[trigger.ID] = true
	}

	b.WriteString("\nRECENT POSTS, oldest first:\n")
	if len(posts) == 0 {
		b.WriteString("(none)\n")
	}
	for _, p := range posts {
		author := p.Author
		if author == "" {
			author = "anon"
		}
		body := p.Body
		if !triggerIDs[p.ID] {
			body = clip(body, historyPostLen)
		}
		fmt.Fprintf(&b, "#%d %s: %s\n", p.ID, author, body)
	}

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

// editorAuthors collects the names the "poster" override target expands to.
func editorAuthors(triggers []Post) []string {
	authors := make([]string, 0, len(triggers))
	seen := make(map[string]bool, len(triggers))
	for _, trigger := range triggers {
		name := strings.TrimSpace(trigger.Author)
		if name == "" {
			continue
		}
		key := OverrideKey(name)
		if seen[key] {
			continue
		}
		seen[key] = true
		authors = append(authors, name)
	}
	return authors
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
