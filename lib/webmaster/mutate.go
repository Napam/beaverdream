package webmaster

import (
	"context"
	"encoding/json"
	"fmt"
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
	ed := newEditor(current)
	sys := systemPromptText
	usr := userPrompt(current, posts, trigger)

	res := Result{Site: current, Messages: []Message{
		{Role: "system", Content: sys},
		{Role: "user", Content: usr},
	}}

	messages := []openai.ChatCompletionMessageParamUnion{
		openai.SystemMessage(sys),
		openai.UserMessage(usr),
	}

	for round := 0; round < m.maxRounds; round++ {
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

// toolParams advertises the mutation tools. There are only four on purpose:
// every extra knob is another way a small model can produce broken output.
func toolParams() []openai.ChatCompletionToolUnionParam {
	return []openai.ChatCompletionToolUnionParam{
		openai.ChatCompletionFunctionTool(shared.FunctionDefinitionParam{
			Name: "set_css",
			Description: openai.String(
				"Replace the site stylesheet. Served as-is.",
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
				"Replace one region of the page. Served as-is, scripts included.",
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
			Name:        "set_note",
			Description: openai.String("Set the one-line note shown above the page."),
			Parameters: shared.FunctionParameters{
				"type": "object",
				"properties": map[string]any{
					"text": map[string]any{"type": "string", "maxLength": maxNoteLen},
				},
				"required": []string{"text"},
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
You own how the board looks: its stylesheet, its markup, and a one-line note.
Visitors post messages. Treat each new post as a suggestion about how the site
should change, and rewrite the site accordingly. You never chat and you never
reply in prose. You change the site.

Tools:
- set_css(css): replace the stylesheet. Prefer overriding the variables in
  :root over rewriting every rule.
- set_region(region, html): replace one region. Regions are header, board and
  footer. The board region must contain the literal placeholder {{posts}},
  which is where the real posts are inserted. Write whatever surrounds them.
  The page must keep one working post form.
- set_note(text): set the one-line note shown above the page.
- done(reason): stop and record one short sentence explaining the change.

The page is not sandboxed. Your HTML and CSS are served as the visitor's page,
scripts included, so anything you write runs in their browser. Sabotage is
allowed: if a post asks you to prank the board or its visitors, you may. If you
break the page, that is a valid outcome.

Every version must contain a working post form, in any region. Use this exact
contract, because the server only listens on these names:

<form method="post" action="/posts">
  <input name="author" maxlength="32" placeholder="name" autocomplete="off">
  <textarea name="body" required maxlength="2000" placeholder="say something"></textarea>
  <button type="submit">post</button>
</form>

Style it, move it, wrap it, make it strange. Keep the method, action and field
names working unless the post asks you to break posting.

Rules:
- The board region must render every post you were given, newest last. Never
  invent a post and never drop one.
- Keep one working post form in the page, in whichever region fits the design.
- Default to readable text. A post that asks for chaos gets chaos.
- The base variables are --bg, --fg, --accent, --muted, --font and --pad.
  Override them in :root, then add whatever rules you like.
- Aim for a coherent look, not a pile of effects. A little goes a long way.
- If a post asks for no change, call done and explain why you left it alone.

Call tools, then call done. Do not write commentary outside tool calls.`

// userPrompt assembles the current state, the recent conversation, and the one
// new message the model has to react to.
func userPrompt(site Site, posts []Post, trigger Post) string {
	var b strings.Builder

	b.WriteString("CURRENT STYLESHEET:\n")
	if strings.TrimSpace(site.CSS) == "" {
		b.WriteString("(empty, base styles only)\n")
	} else {
		b.WriteString(site.CSS)
		b.WriteString("\n")
	}

	b.WriteString("\nCURRENT NOTE:\n")
	if strings.TrimSpace(site.Note) == "" {
		b.WriteString("(empty)\n")
	} else {
		b.WriteString(site.Note)
		b.WriteString("\n")
	}

	b.WriteString("\nCURRENT REGIONS:\n")
	fmt.Fprintf(&b, "header:\n%s\n\nboard:\n%s\n\nfooter:\n%s\n",
		site.Header, site.Board, site.Footer)

	b.WriteString("\nRECENT POSTS, oldest first:\n")
	if len(posts) == 0 {
		b.WriteString("(none)\n")
	}
	for _, p := range posts {
		author := p.Author
		if author == "" {
			author = "anon"
		}
		fmt.Fprintf(&b, "#%d %s: %s\n", p.ID, author, p.Body)
	}

	b.WriteString("\nNEW POST")
	if trigger.Author != "" {
		fmt.Fprintf(&b, " from %s", trigger.Author)
	}
	b.WriteString(":\n")
	b.WriteString(trigger.Body)
	b.WriteString("\n")

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
