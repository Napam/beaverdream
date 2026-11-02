// Package mockllm is a deterministic OpenAI-compatible chat completions server
// used for tests and for developing the board without spending real tokens.
//
// It answers with the same mutation tools the real webmaster uses, chosen from
// keywords in the newest prompt, so a whole mutation can run offline.
package mockllm

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const model = "mock-model"

type chatMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

type chatRequest struct {
	Stream   *bool         `json:"stream"`
	Messages []chatMessage `json:"messages"`
}

type functionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type toolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"`
	Function functionCall `json:"function"`
}

type theme struct {
	css    string
	title  string
	note   string
	header string
}

// Handler serves chat completions and a model list.
func Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/models", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"object": "list",
			"data":   []map[string]string{{"id": model, "object": "model"}},
		})
	})
	// Unknown GET paths would otherwise get a 405 from the POST pattern.
	mux.HandleFunc("GET /", http.NotFound)
	mux.HandleFunc("POST /", completions)
	return mux
}

func completions(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		http.Error(w, "read body", http.StatusBadRequest)
		return
	}
	var req chatRequest
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, "decode request", http.StatusBadRequest)
		return
	}

	joined := strings.ToLower(newestPost(req.Messages))
	if strings.Contains(joined, "mockfail") {
		http.Error(w, "mock failure requested", http.StatusInternalServerError)
		return
	}

	calls := decide(joined, hasToolResult(req.Messages))
	if req.Stream != nil && *req.Stream {
		writeStream(w, calls)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id":      "chatcmpl-mock",
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   model,
		"choices": []any{map[string]any{
			"index": 0,
			"message": map[string]any{
				"role":       "assistant",
				"content":    "",
				"tool_calls": calls,
			},
			"finish_reason": "tool_calls",
		}},
		"usage": map[string]int{
			"prompt_tokens": 0, "completion_tokens": 0, "total_tokens": 0,
		},
	})
}

// decide returns the tool calls for one round. The first round edits the site;
// once a tool result is in the history, the second round closes with done, so
// tests exercise the multi-round loop rather than a single response.
func decide(prompt string, toolResultSeen bool) []toolCall {
	if strings.Contains(prompt, "mocknoop") || toolResultSeen {
		return []toolCall{doneCall("nothing needed changing")}
	}
	return pickTheme(prompt).calls()
}

func doneCall(reason string) toolCall {
	return toolCall{
		ID:   "call_done",
		Type: "function",
		Function: functionCall{
			Name:      "done",
			Arguments: mustJSON(map[string]string{"reason": reason}),
		},
	}
}

func pickTheme(prompt string) theme {
	switch {
	case strings.Contains(prompt, "matrix"):
		return theme{
			css: `:root{--bg:#000;--fg:#00ff6a;--accent:#00ff9c;--muted:#0b7a34;` +
				`--panel:#001507;--border:#0b5a28;` +
				`--font:ui-monospace,SFMono-Regular,Menlo,monospace}` +
				`h1{text-shadow:0 0 14px var(--accent);letter-spacing:.08em}` +
				`.post{border-color:#053d1a}.post-author{text-shadow:0 0 8px var(--accent)}`,
			title: "follow the white rabbit",
			note:  "everything that has a beginning has a glitch.",
		}
	case strings.Contains(prompt, "chaos") ||
		strings.Contains(prompt, "sabotage") ||
		strings.Contains(prompt, "prank"):
		return theme{
			css: `:root{--bg:#16000f;--fg:#ffe7fa;--accent:#ff2d95;--muted:#a05a85;` +
				`--panel:#250017;--border:#7a1a52}` +
				`body{animation:hue 9s linear infinite}` +
				`@keyframes hue{to{filter:hue-rotate(360deg)}}` +
				`h1{letter-spacing:.2em;text-transform:uppercase}`,
			title:  "chaos board",
			note:   "the board is not your friend.",
			header: `<h1>beaverdream</h1><script>document.title="chaos board"</script>`,
		}
	case strings.Contains(prompt, "pink") || strings.Contains(prompt, "cute"):
		return theme{
			css: `:root{--bg:#1b0d17;--fg:#ffd9ec;--accent:#ff6fae;--muted:#a56d8a;` +
				`--panel:#241019;--border:#5a2a44;` +
				`--font:Georgia,"Times New Roman",serif}` +
				`h1{letter-spacing:.02em}.post{border-color:#3a1b2c;border-radius:12px;padding-left:.9em}`,
			title: "soft board",
			note:  "be nice to each other, the walls are listening.",
		}
	case strings.Contains(prompt, "brutalist") || strings.Contains(prompt, "concrete"):
		return theme{
			css: `:root{--bg:#e8e4da;--fg:#111;--accent:#e0245e;--muted:#6b6459;` +
				`--panel:#fff;--border:#c9c2b4;` +
				`--font:Arial,Helvetica,sans-serif}` +
				`h1{text-transform:uppercase;letter-spacing:-.03em;font-size:2.2em}` +
				`.post{border-top:3px solid #111}`,
			title: "raw feed",
			note:  "no decoration. no excuses.",
		}
	default:
		return theme{
			css:   `:root{--bg:#0b0b0f;--fg:#e6e6e6;--accent:#9db4ff;--muted:#6d7280;--panel:#15151b;--border:#2a2a33}`,
			title: "the board",
			note:  "a quiet board tonight.",
		}
	}
}

// calls renders a theme as the three edits the webmaster tools expect. The
// board keeps the {{posts}} placeholder, so the real posts stay in place.
func (t theme) calls() []toolCall {
	board := `<h2 class="board-title">` + t.title + `</h2>{{posts}}`
	calls := []toolCall{
		{
			ID:   "call_css",
			Type: "function",
			Function: functionCall{
				Name:      "set_css",
				Arguments: mustJSON(map[string]string{"css": t.css}),
			},
		},
		{
			ID:   "call_board",
			Type: "function",
			Function: functionCall{
				Name: "set_region",
				Arguments: mustJSON(map[string]string{
					"region": "board",
					"html":   board,
				}),
			},
		},
		{
			ID:   "call_note",
			Type: "function",
			Function: functionCall{
				Name:      "set_note",
				Arguments: mustJSON(map[string]string{"text": t.note}),
			},
		},
	}
	if t.header != "" {
		calls = append(calls, toolCall{
			ID:   "call_header",
			Type: "function",
			Function: functionCall{
				Name: "set_region",
				Arguments: mustJSON(map[string]string{
					"region": "header",
					"html":   t.header,
				}),
			},
		})
	}
	return calls
}

// writeStream emits the same calls as data-only SSE chunks, so a client that
// asks for streaming still works.
func writeStream(w http.ResponseWriter, calls []toolCall) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")

	for i, call := range calls {
		chunk := map[string]any{
			"id":      "chatcmpl-mock",
			"object":  "chat.completion.chunk",
			"created": time.Now().Unix(),
			"model":   model,
			"choices": []any{map[string]any{
				"index": 0,
				"delta": map[string]any{
					"tool_calls": []any{map[string]any{
						"index":    i,
						"id":       call.ID,
						"type":     call.Type,
						"function": call.Function,
					}},
				},
				"finish_reason": nil,
			}},
		}
		data, _ := json.Marshal(chunk)
		_, _ = fmt.Fprintf(w, "data: %s\n\n", data)
		flusher.Flush()
	}

	final, _ := json.Marshal(map[string]any{
		"id":      "chatcmpl-mock",
		"object":  "chat.completion.chunk",
		"created": time.Now().Unix(),
		"model":   model,
		"choices": []any{map[string]any{
			"index": 0, "delta": map[string]any{}, "finish_reason": "tool_calls",
		}},
	})
	_, _ = fmt.Fprintf(w, "data: %s\n\n", final)
	_, _ = io.WriteString(w, "data: [DONE]\n\n")
	flusher.Flush()
}

func hasToolResult(messages []chatMessage) bool {
	for _, m := range messages {
		if m.Role == "tool" {
			return true
		}
	}
	return false
}

// newestPost returns the part of the last user prompt that describes the new
// post. Matching keywords in the whole prompt would pick up the system prompt
// and the current site, so "sabotage" in the rules would always win.
func newestPost(messages []chatMessage) string {
	var text string
	for _, m := range messages {
		if m.Role != "user" {
			continue
		}
		var s string
		if err := json.Unmarshal(m.Content, &s); err == nil {
			text = s
		}
	}
	if idx := strings.LastIndex(text, "NEW POST"); idx >= 0 {
		text = text[idx:]
	}
	return text
}

func mustJSON(v any) string {
	data, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(data)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
