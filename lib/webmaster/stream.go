package webmaster

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
)

// Draft is the round the model is writing, as far as it has arrived. Calls
// carry their name and the arguments so far, and no result yet.
type Draft struct {
	Round     int
	Reasoning string
	Content   string
	Calls     []ToolCall
	// Updated is when the stream last added anything, so the admin panel can
	// say how long a round has been quiet.
	Updated time.Time
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
	// Stalled is true when the watchdog cut the stream. Whatever arrived is
	// partial and must not be used.
	Stalled bool
	Trace   *RoundTrace
}

// complete runs one round as a stream. OnDraft hears the answer as it grows,
// so the admin panel can follow a long round; the turn only acts on the whole
// reply. A stream that adds nothing for longer than the idle limit is cut and
// comes back as Stalled with no error.
func (m *Mutator) complete(
	ctx context.Context,
	params openai.ChatCompletionNewParams,
	round int,
) (reply, error) {
	start := time.Now()
	roundCtx, cancelRound := context.WithCancelCause(ctx)
	defer cancelRound(nil)
	limits := m.idle
	dog := newWatchdog(func() { cancelRound(errStalled) })
	defer dog.stop()
	// armed is the limit the watchdog is running now, and armedPhase the phase
	// that chose it, so a stall reports what actually fired.
	armed, armedPhase := limits.First, "waiting"
	trace := &RoundTrace{}

	// The middleware sees every HTTP attempt, so retries and the time to the
	// response headers are on record. It runs on this goroutine.
	observe := func(req *http.Request, next option.MiddlewareNext) (*http.Response, error) {
		trace.Attempts++
		res, err := next(req)
		if err == nil && res != nil {
			trace.Headers = time.Since(start)
			trace.Status = res.StatusCode
			trace.RequestID = res.Header.Get("X-Request-Id")
		}
		return res, err
	}
	dog.reset(limits.First)
	stream := m.client.Chat.Completions.NewStreaming(
		roundCtx, params, option.WithMiddleware(observe),
	)
	defer func() { _ = stream.Close() }()

	out := reply{Draft: Draft{Round: round}, Trace: trace}
	var reasoning, content strings.Builder
	// A delta names its tool call by index. at maps that index to the call's
	// place in out.Calls, in the order the calls first appeared.
	at := make(map[int64]int)
	var args []*strings.Builder
	phase := "waiting"
	// prev is the phase the stream was in when the current gap began.
	prev := "waiting"
	lastGrow := time.Duration(0)
	chunks := 0
	unknown := make(map[string]bool)
	gap := func(now time.Duration, during string) {
		if g := now - lastGrow; g > trace.LongestGap {
			trace.LongestGap = g
			trace.GapPhase = during
		}
	}
	for stream.Next() {
		chunk := stream.Current()
		trace.keep(chunks, chunk.RawJSON())
		chunks++
		if chunk.Model != "" {
			trace.Model = chunk.Model
		}
		//nolint:staticcheck // the fingerprint is how a backend swap shows
		if chunk.SystemFingerprint != "" {
			trace.Fingerprint = chunk.SystemFingerprint //nolint:staticcheck // as above
		}
		if chunk.ID != "" {
			trace.ChunkID = chunk.ID
		}
		// Usage arrives on its own chunk at the end, and some providers
		// repeat a running total; the last one counts.
		hasUsage := chunk.Usage.PromptTokens > 0 || chunk.Usage.CompletionTokens > 0
		finishHere := false
		if hasUsage {
			out.Usage = chunk.Usage
			trace.ReasoningTokens = chunk.Usage.CompletionTokensDetails.ReasoningTokens
			if len(chunk.Choices) == 0 {
				trace.Events.UsageOnly++
			}
		}
		grew := false
		for _, choice := range chunk.Choices {
			if choice.Index != 0 {
				trace.OtherChoices++
				continue
			}
			out.Choices = true
			if choice.FinishReason != "" {
				out.FinishReason = choice.FinishReason
				trace.Finished = true
				finishHere = true
			}
			delta := choice.Delta
			//nolint:staticcheck // a provider that still sends function_call is what this counts
			if delta.FunctionCall.Name != "" || delta.FunctionCall.Arguments != "" {
				trace.FunctionCalls++
			}
			for name := range delta.JSON.ExtraFields {
				if name != "reasoning_content" {
					unknown[name] = true
				}
			}
			if text, ok := reasoningDelta(delta); ok {
				out.HasReasoning = true
				reasoning.WriteString(text)
				if text != "" {
					grew = true
					trace.Events.Reasoning++
					phase = "reasoning"
				}
			}
			if delta.Content != "" {
				content.WriteString(delta.Content)
				grew = true
				trace.Events.Content++
				phase = "content"
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
				if call.Function.Name != "" {
					trace.Events.ToolName++
					if trace.Name == 0 {
						trace.Name = time.Since(start)
					}
				}
				if call.Function.Arguments != "" {
					trace.Events.ToolArgs++
				}
				out.Calls[i].Name += call.Function.Name
				args[i].WriteString(call.Function.Arguments)
				out.Calls[i].Arguments = args[i].String()
				grew = true
				phase = "call"
			}
		}
		if !grew {
			if !finishHere && (!hasUsage || len(chunk.Choices) > 0) {
				trace.Events.Empty++
			}
			continue
		}
		now := time.Since(start)
		if out.FirstToken == 0 {
			out.FirstToken = now
			trace.FirstEvent = now
		}
		gap(now, prev)
		prev = phase
		lastGrow = now
		trace.LastEvent = now
		if len(out.Calls) > 0 {
			armed, armedPhase = limits.Call, "call"
		} else {
			armed, armedPhase = limits.Think, phase
		}
		dog.reset(armed)
		out.Reasoning = reasoning.String()
		out.Content = content.String()
		if m.OnDraft != nil {
			draft := out.Draft
			draft.Calls = append([]ToolCall(nil), out.Calls...)
			draft.Updated = time.Now()
			m.OnDraft(draft)
		}
	}
	// The stream is over. Stop the watchdog before reading the cause, so a
	// stall that fires as a clean stream ends cannot discard a finished round.
	dog.stop()
	trace.Total = time.Since(start)
	gap(trace.Total, prev)
	out.Reasoning = reasoning.String()
	out.Content = content.String()
	for name := range unknown {
		trace.UnknownFields = append(trace.UnknownFields, name)
	}
	sort.Strings(trace.UnknownFields)

	err := stream.Err()
	if err != nil && errors.Is(context.Cause(roundCtx), errStalled) && ctx.Err() == nil {
		trace.Stalled = true
		trace.StallPhase = armedPhase
		trace.StallLimit = armed
		out.Stalled = true
		err = nil
	}
	// A provider that leaves out the id still needs one, because each tool
	// result names the call it answers.
	for i := range out.Calls {
		if out.Calls[i].ID == "" {
			out.Calls[i].ID = fmt.Sprintf("call_%d_%d", round, i)
		}
	}
	return out, err
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
