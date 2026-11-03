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
	// The summarizer asks for prose, not tools. A fixed line keeps the
	// compaction path deterministic without modelling a second mind.
	if strings.Contains(joined, "summarize this stretch of the room") {
		writeTextCompletion(w, "the room changed look twice. dave and kit asked for it.")
		return
	}
	// mockprose answers in words with no tool call, the shape a model takes
	// when it explains instead of acting. The turn ends there.
	if strings.Contains(joined, "mockprose") {
		writeTextCompletion(w, "i would restyle this, but i have nothing in hand.")
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

// decide returns the tool calls for one round. A theme keyword rewrites the
// site in the first round and closes with done in the second, so tests
// exercise the multi-round loop. An edit keyword sends the edit and done in
// one response.
func decide(prompt string, toolResultSeen bool) []toolCall {
	if strings.Contains(prompt, "mocknoop") || toolResultSeen {
		return []toolCall{doneCall("nothing needed changing")}
	}
	if strings.Contains(prompt, "mockgame") {
		return gameCalls()
	}
	if edit, ok := pickEdit(prompt); ok {
		return []toolCall{editCall(edit), doneCall("a small edit")}
	}
	return pickTheme(prompt).calls()
}

// gameCalls declares a feed and ships a whole two-player applet in one turn.
// The applet is the reference shape the prompt asks for: a reducer that is the
// referee, a render that draws whatever the reducer returns, and no state of
// its own.
func gameCalls() []toolCall {
	schema := map[string]any{"keys": []map[string]any{{
		"name":  "moves",
		"max":   200,
		"rules": 1,
	}}}
	board := `<h2 class="board-title">tic tac toe</h2>` +
		`<div id="game" class="ttt" data-journal="moves"></div>` +
		GameScript + postForm + `{{posts}}`
	site := map[string]string{
		"css":   `:root{--bg:#0d1117;--fg:#e6edf3;--accent:#f78166;--muted:#8b949e;--panel:#161b22;--border:#30363d}`,
		"board": board,
	}
	return []toolCall{
		{
			ID:       "call_journal",
			Type:     "function",
			Function: functionCall{Name: "set_journal_schema", Arguments: mustJSON(schema)},
		},
		{
			ID:       "call_site",
			Type:     "function",
			Function: functionCall{Name: "set_site", Arguments: mustJSON(site)},
		},
		doneCall("two-player tic tac toe. click join and take a seat."),
	}
}

// GameScript is the applet the mockgame keyword ships. It is exported so a
// test can run its reducer against a row list.
const GameScript = `<style>
.ttt-grid{display:grid;grid-template-columns:repeat(3,64px);gap:6px;margin:.6em 0}
.ttt-cell{height:64px;font-size:1.6em;font-weight:700;background:var(--panel);color:var(--fg);border:1px solid var(--border);border-radius:4px;cursor:pointer}
.ttt-status{font-weight:700;margin:.4em 0}
.ttt-join{background:var(--accent);color:#fff;border:0;border-radius:4px;padding:.4em .9em;font-weight:700;cursor:pointer}
</style>
<script>
(function () {
  var feed = bd.journal("moves")
  var LINES = [[0,1,2],[3,4,5],[6,7,8],[0,3,6],[1,4,7],[2,5,8],[0,4,8],[2,4,6]]
  function winnerOf(board) {
    for (var l = 0; l < LINES.length; l++) {
      var a = board[LINES[l][0]], b = board[LINES[l][1]], c = board[LINES[l][2]]
      if (a && a === b && b === c) return a
    }
    return ''
  }
  function reduce(rows) {
    var players = []
    var board = ['', '', '', '', '', '', '', '', '']
    var played = 0
    var winner = ''
    for (var i = 0; i < rows.length; i++) {
      var p = rows[i].payload || {}
      var who = rows[i].visitor
      if (p.join) {
        if (players.length < 2 && players.indexOf(who) < 0) players.push(who)
        continue
      }
      if (typeof p.cell !== 'number' || winner) continue
      if (players.length < 2) continue
      var seat = players.indexOf(who)
      if (seat < 0 || p.cell < 0 || p.cell > 8 || board[p.cell]) continue
      if (played % 2 !== seat) continue
      board[p.cell] = seat === 0 ? 'X' : 'O'
      played++
      winner = winnerOf(board)
    }
    if (!winner && played === 9) winner = 'draw'
    var turn = players.length === 2 ? players[played % 2] : ''
    return { board: board, players: players, winner: winner, turn: turn }
  }
  function render(s) {
    var box = document.getElementById('game')
    if (!box) return
    var me = s.players.indexOf(bd.visitor)
    var status
    if (s.winner === 'draw') status = 'draw.'
    else if (s.winner) status = s.winner + ' wins.'
    else if (!s.players.length) status = 'press join.'
    else if (s.turn === bd.visitor) status = 'your turn, ' + (me === 0 ? 'X' : 'O')
    else status = 'waiting for the other player.'
    var html = '<div class="ttt-status">' + status + '</div><div class="ttt-grid">'
    for (var i = 0; i < 9; i++) {
      html += '<button class="ttt-cell" data-cell="' + i + '">' + (s.board[i] || '') + '</button>'
    }
    html += '</div>'
    if (me < 0 && s.players.length < 2 && !s.winner) {
      html += '<button class="ttt-join" id="ttt-join">join</button>'
    }
    box.innerHTML = html
    var join = document.getElementById('ttt-join')
    if (join) join.onclick = function () { feed.append({ join: 1 }) }
    var cells = box.querySelectorAll('.ttt-cell')
    for (var c = 0; c < cells.length; c++) {
      cells[c].onclick = function () {
        if (s.turn !== bd.visitor || s.board[+this.dataset.cell]) return
        feed.append({ cell: +this.dataset.cell })
      }
    }
  }
  feed.start(reduce, render)
})()
</script>`

// PatchedAccent is the accent color the mockpatch keyword writes over the
// default theme's accent.
const PatchedAccent = "--accent:#c00"

// pickEdit returns the targeted edit for the edit keywords. Each one answers
// with the edit and done in the same response, the way the prompt asks:
//   - mockedit appends one CSS rule.
//   - mockpatch replaces the default theme's accent, so it needs a page that
//     already carries that theme.
//   - mockbadedit names text that is not on the page, so the edit is
//     rejected and the done next to it is deferred.
func pickEdit(prompt string) (map[string]string, bool) {
	switch {
	case strings.Contains(prompt, "mockbadedit"):
		return map[string]string{"target": "css", "old": "no such text", "new": "x"}, true
	case strings.Contains(prompt, "mockedit"):
		return map[string]string{"target": "css", "old": "", "new": "h1{color:#c00}"}, true
	case strings.Contains(prompt, "mockpatch"):
		return map[string]string{
			"target": "css",
			"old":    "--accent:#9db4ff",
			"new":    PatchedAccent,
		}, true
	}
	return nil, false
}

func editCall(edit map[string]string) toolCall {
	return toolCall{
		ID:       "call_edit",
		Type:     "function",
		Function: functionCall{Name: "edit", Arguments: mustJSON(edit)},
	}
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
			header: `<h1>beaverdream</h1><script>document.title="chaos board"</script>`,
		}
	case strings.Contains(prompt, "pink") || strings.Contains(prompt, "cute"):
		return theme{
			css: `:root{--bg:#1b0d17;--fg:#ffd9ec;--accent:#ff6fae;--muted:#a56d8a;` +
				`--panel:#241019;--border:#5a2a44;` +
				`--font:Georgia,"Times New Roman",serif}` +
				`h1{letter-spacing:.02em}.post{border-color:#3a1b2c;border-radius:12px;padding-left:.9em}`,
			title: "soft board",
		}
	case strings.Contains(prompt, "brutalist") || strings.Contains(prompt, "concrete"):
		return theme{
			css: `:root{--bg:#e8e4da;--fg:#111;--accent:#e0245e;--muted:#6b6459;` +
				`--panel:#fff;--border:#c9c2b4;` +
				`--font:Arial,Helvetica,sans-serif}` +
				`h1{text-transform:uppercase;letter-spacing:-.03em;font-size:2.2em}` +
				`.post{border-top:3px solid #111}`,
			title: "raw feed",
		}
	default:
		return theme{
			css:   `:root{--bg:#0b0b0f;--fg:#e6e6e6;--accent:#9db4ff;--muted:#6d7280;--panel:#15151b;--border:#2a2a33}`,
			title: "the board",
		}
	}
}

// calls renders a theme as the edits the webmaster tools expect. The board
// keeps the {{posts}} placeholder, so the real posts stay in place.
const postForm = `<form class="postbox" method="post" action="/posts" ` +
	`data-on:submit="@post('/posts', {contentType: 'form'}); el.reset()">` +
	`<p class="postbox-title">post a message</p>` +
	`<textarea name="body" required maxlength="2000" rows="3" placeholder="say something" aria-label="message"></textarea>` +
	`<div class="postbox-actions"><span>posting as {{name}}</span>` +
	`<button type="submit">post</button></div></form>`

func (t theme) calls() []toolCall {
	site := map[string]string{
		"css":   t.css,
		"board": `<h2 class="board-title">` + t.title + `</h2>` + postForm + `{{posts}}`,
	}
	if t.header != "" {
		site["header"] = t.header
	}
	calls := []toolCall{{
		ID:       "call_site",
		Type:     "function",
		Function: functionCall{Name: "set_site", Arguments: mustJSON(site)},
	}}
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

func writeTextCompletion(w http.ResponseWriter, content string) {
	writeJSON(w, http.StatusOK, map[string]any{
		"id":      "chatcmpl-mock",
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   model,
		"choices": []any{map[string]any{
			"index": 0,
			"message": map[string]any{
				"role":    "assistant",
				"content": content,
			},
			"finish_reason": "stop",
		}},
		"usage": map[string]int{
			"prompt_tokens": 0, "completion_tokens": 0, "total_tokens": 0,
		},
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
