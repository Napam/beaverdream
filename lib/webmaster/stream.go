package webmaster

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/openai/openai-go/v3"
)

// Draft is the round the model is writing, as far as it has arrived. Calls
// carry their name and the arguments so far, and no result yet.
type Draft struct {
	Round     int
	Reasoning string
	Content   string
	Calls     []ToolCall
}

// reply is one round's answer once its stream has ended.
type reply struct {
	Draft
	// HasReasoning is true when the provider sent reasoning_content at all,
	// even empty, because its thinking mode wants the field back.
	HasReasoning bool
	FinishReason string
	Usage        openai.CompletionUsage
	// Choices is false when no chunk carried a choice.
	Choices bool
	// FirstToken is how long the model took to send its first words.
	FirstToken time.Duration
}

// complete runs one round as a stream. OnDraft hears the answer as it grows,
// so the admin panel can follow a long round; the turn only acts on the whole
// reply.
func (m *Mutator) complete(
	ctx context.Context,
	params openai.ChatCompletionNewParams,
	round int,
) (reply, error) {
	start := time.Now()
	stream := m.client.Chat.Completions.NewStreaming(ctx, params)
	defer func() { _ = stream.Close() }()

	out := reply{Draft: Draft{Round: round}}
	var reasoning, content strings.Builder
	// A delta names its tool call by index. at maps that index to the call's
	// place in out.Calls, in the order the calls first appeared.
	at := make(map[int64]int)
	var args []*strings.Builder
	for stream.Next() {
		chunk := stream.Current()
		// Usage arrives on its own chunk at the end, and some providers
		// repeat a running total; the last one counts.
		if chunk.Usage.PromptTokens > 0 || chunk.Usage.CompletionTokens > 0 {
			out.Usage = chunk.Usage
		}
		grew := false
		for _, choice := range chunk.Choices {
			if choice.Index != 0 {
				continue
			}
			out.Choices = true
			if choice.FinishReason != "" {
				out.FinishReason = choice.FinishReason
			}
			delta := choice.Delta
			if text, ok := reasoningDelta(delta); ok {
				out.HasReasoning = true
				reasoning.WriteString(text)
				grew = grew || text != ""
			}
			if delta.Content != "" {
				content.WriteString(delta.Content)
				grew = true
			}
			for _, call := range delta.ToolCalls {
				i, seen := at[call.Index]
				if !seen {
					i = len(out.Calls)
					at[call.Index] = i
					out.Calls = append(out.Calls, ToolCall{})
					args = append(args, &strings.Builder{})
				}
				if call.ID != "" {
					out.Calls[i].ID = call.ID
				}
				out.Calls[i].Name += call.Function.Name
				args[i].WriteString(call.Function.Arguments)
				out.Calls[i].Arguments = args[i].String()
				grew = true
			}
		}
		if !grew {
			continue
		}
		if out.FirstToken == 0 {
			out.FirstToken = time.Since(start)
		}
		out.Reasoning = reasoning.String()
		out.Content = content.String()
		if m.OnDraft != nil {
			draft := out.Draft
			draft.Calls = append([]ToolCall(nil), out.Calls...)
			m.OnDraft(draft)
		}
	}
	out.Reasoning = reasoning.String()
	out.Content = content.String()
	// A provider that leaves out the id still needs one, because each tool
	// result names the call it answers.
	for i := range out.Calls {
		if out.Calls[i].ID == "" {
			out.Calls[i].ID = fmt.Sprintf("call_%d_%d", round, i)
		}
	}
	return out, stream.Err()
}

// reasoningDelta pulls "reasoning_content" out of one streamed delta. ok is
// true when the field is present, even as null: a provider that sends it
// expects it back.
func reasoningDelta(delta openai.ChatCompletionChunkChoiceDelta) (string, bool) {
	field, ok := delta.JSON.ExtraFields["reasoning_content"]
	if !ok {
		return "", false
	}
	var text string
	if err := json.Unmarshal([]byte(field.Raw()), &text); err != nil {
		return "", false
	}
	return text, true
}
