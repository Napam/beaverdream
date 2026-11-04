package mockllm

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"time"
)

// streamPiece is how many runes one chunk carries, close to what a real
// provider sends, so a client sees a reply grow instead of arrive whole.
const streamPiece = 24

// Streamed answers a request that asks for "stream": true by cutting the
// whole completion next writes into chat.completion.chunk events: reasoning,
// then content, then each tool call with its arguments in pieces, the finish
// reason, and usage last. Anything else passes through, so one fake model
// serves the streaming turn and the plain summarizer alike.
func Streamed(next http.HandlerFunc) http.Handler {
	return SlowStreamed(next, 0)
}

// SlowStreamed is Streamed with a pause before each chunk, for watching a
// turn arrive in the admin panel.
func SlowStreamed(next http.HandlerFunc, delay time.Duration) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err != nil {
			http.Error(w, "read body", http.StatusBadRequest)
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		var req struct {
			Stream bool `json:"stream"`
		}
		_ = json.Unmarshal(body, &req)
		if !req.Stream {
			next(w, r)
			return
		}

		rec := httptest.NewRecorder()
		next(rec, r)
		if rec.Code != http.StatusOK {
			for k, v := range rec.Header() {
				w.Header()[k] = v
			}
			w.WriteHeader(rec.Code)
			_, _ = w.Write(rec.Body.Bytes())
			return
		}
		var whole wholeCompletion
		if err := json.Unmarshal(rec.Body.Bytes(), &whole); err != nil {
			http.Error(
				w,
				"the fake model did not answer with a completion",
				http.StatusInternalServerError,
			)
			return
		}
		writeChunks(w, whole, delay)
	})
}

type wholeCompletion struct {
	ID      string `json:"id"`
	Model   string `json:"model"`
	Created int64  `json:"created"`
	Choices []struct {
		Index        int    `json:"index"`
		FinishReason string `json:"finish_reason"`
		Message      struct {
			Content   string          `json:"content"`
			Reasoning json.RawMessage `json:"reasoning_content"`
			ToolCalls []toolCall      `json:"tool_calls"`
		} `json:"message"`
	} `json:"choices"`
	Usage json.RawMessage `json:"usage"`
}

func writeChunks(w http.ResponseWriter, whole wholeCompletion, delay time.Duration) {
	flusher, _ := w.(http.Flusher)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	send := func(choices []any, usage json.RawMessage) {
		if delay > 0 {
			time.Sleep(delay)
		}
		chunk := map[string]any{
			"id":      whole.ID,
			"object":  "chat.completion.chunk",
			"created": whole.Created,
			"model":   whole.Model,
			"choices": choices,
		}
		if usage != nil {
			chunk["usage"] = usage
		}
		data, _ := json.Marshal(chunk)
		_, _ = fmt.Fprintf(w, "data: %s\n\n", data)
		if flusher != nil {
			flusher.Flush()
		}
	}
	delta := func(index int, d map[string]any) {
		send([]any{map[string]any{"index": index, "delta": d}}, nil)
	}

	for _, choice := range whole.Choices {
		delta(choice.Index, map[string]any{"role": "assistant"})
		msg := choice.Message
		var reasoning string
		if json.Unmarshal(msg.Reasoning, &reasoning) == nil {
			for _, piece := range pieces(reasoning) {
				delta(choice.Index, map[string]any{"reasoning_content": piece})
			}
		}
		for _, piece := range pieces(msg.Content) {
			delta(choice.Index, map[string]any{"content": piece})
		}
		for i, call := range msg.ToolCalls {
			delta(choice.Index, map[string]any{"tool_calls": []any{map[string]any{
				"index": i,
				"id":    call.ID,
				"type":  "function",
				"function": map[string]string{
					"name":      call.Function.Name,
					"arguments": "",
				},
			}}})
			for _, piece := range pieces(call.Function.Arguments) {
				delta(choice.Index, map[string]any{"tool_calls": []any{map[string]any{
					"index":    i,
					"function": map[string]string{"arguments": piece},
				}}})
			}
		}
		send([]any{map[string]any{
			"index":         choice.Index,
			"delta":         map[string]any{},
			"finish_reason": choice.FinishReason,
		}}, nil)
	}
	if len(whole.Usage) > 0 && string(whole.Usage) != "null" {
		send([]any{}, whole.Usage)
	}
	_, _ = io.WriteString(w, "data: [DONE]\n\n")
	if flusher != nil {
		flusher.Flush()
	}
}

// pieces cuts text on rune boundaries, so every chunk is valid UTF-8.
func pieces(text string) []string {
	var out []string
	runes := []rune(text)
	for len(runes) > 0 {
		n := min(streamPiece, len(runes))
		out = append(out, string(runes[:n]))
		runes = runes[n:]
	}
	return out
}
