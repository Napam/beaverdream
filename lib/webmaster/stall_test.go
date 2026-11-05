package webmaster

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
)

// sseChunk writes one chat.completion.chunk event and flushes it.
func sseChunk(w http.ResponseWriter, choice map[string]any, usage map[string]any) {
	chunk := map[string]any{
		"id":                 "chatcmpl-test",
		"object":             "chat.completion.chunk",
		"model":              "backend-a",
		"system_fingerprint": "fp-1",
		"choices":            []any{},
	}
	if choice != nil {
		chunk["choices"] = []any{choice}
	}
	if usage != nil {
		chunk["usage"] = usage
	}
	data, _ := json.Marshal(chunk)
	_, _ = fmt.Fprintf(w, "data: %s\n\n", data)
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}

func sseHeaders(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
}

// writeEdit streams a call's name, then its arguments in one piece.
func writeEdit(w http.ResponseWriter, name, arguments string) {
	sseChunk(w, map[string]any{"index": 0, "delta": map[string]any{
		"tool_calls": []any{map[string]any{
			"index": 0, "id": "call_1", "type": "function",
			"function": map[string]any{"name": name, "arguments": ""},
		}},
	}}, nil)
	if arguments != "" {
		sseChunk(w, map[string]any{"index": 0, "delta": map[string]any{
			"tool_calls": []any{map[string]any{
				"index": 0, "function": map[string]any{"arguments": arguments},
			}},
		}}, nil)
	}
}

// stallAfterName answers like a gateway that sends a call's name and then
// goes quiet until the client gives up.
func stallAfterName(w http.ResponseWriter, r *http.Request) {
	sseHeaders(w)
	sseChunk(w, map[string]any{"index": 0, "delta": map[string]any{
		"reasoning_content": "thinking about it",
	}}, nil)
	writeEdit(w, "edit", "")
	<-r.Context().Done()
}

// scriptedModel serves request n with handlers[min(n, len-1)] and records
// every request body.
type scriptedModel struct {
	mu       sync.Mutex
	bodies   [][]byte
	handlers []http.HandlerFunc
}

func (s *scriptedModel) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	s.mu.Lock()
	n := len(s.bodies)
	s.bodies = append(s.bodies, body)
	s.mu.Unlock()
	h := s.handlers[min(n, len(s.handlers)-1)]
	h(w, r)
}

func (s *scriptedModel) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.bodies)
}

func stallMutator(srv *httptest.Server, maxRounds int) *Mutator {
	m := New(
		openai.NewClient(option.WithBaseURL(srv.URL), option.WithAPIKey("test")),
		"m", maxRounds, "",
	)
	m.idle = IdleLimits{
		First: 2 * time.Second,
		Think: 2 * time.Second,
		Call:  500 * time.Millisecond,
	}
	m.stallPause = 0
	return m
}

// A stream that goes quiet after the tool name is cut at the idle limit and
// the same request is sent again.
func TestMutateRetriesAStalledRound(t *testing.T) {
	model := &scriptedModel{handlers: []http.HandlerFunc{stallAfterName, realDone}}
	srv := httptest.NewServer(model)
	defer srv.Close()
	m := stallMutator(srv, 3)

	started := time.Now()
	res, err := m.Mutate(
		context.Background(),
		Site{HTML: PostsPlaceholder},
		Room{},
		Post{Body: "hi"},
	)
	if err != nil {
		t.Fatalf("Mutate: %v", err)
	}
	if time.Since(started) > 5*time.Second {
		t.Errorf("took %s, the watchdog did not cut the stall", time.Since(started))
	}
	if !res.Done || model.count() != 2 {
		t.Fatalf("done %v, requests %d, want a done on the second request", res.Done, model.count())
	}
	first := res.Rounds[0]
	if !first.Stalled || first.FinishReason != "stalled" {
		t.Errorf("first round = %+v, want it stalled", first)
	}
	if first.Trace == nil || !first.Trace.Stalled || first.Trace.StallPhase != "call" {
		t.Errorf("trace = %+v, want a stall in the call phase", first.Trace)
	}
	if first.Trace.Name == 0 || first.Trace.Model != "backend-a" ||
		first.Trace.Fingerprint != "fp-1" {
		t.Errorf("trace = %+v, want the tool-name time and the backend", first.Trace)
	}
	if string(model.bodies[0]) != string(model.bodies[1]) {
		t.Error("the retry is not the same request")
	}
	if strings.Contains(string(model.bodies[1]), "thinking about it") {
		t.Error("the stalled round's reasoning went back to the provider")
	}
}

// realDone answers one round with a finished done call.
func realDone(w http.ResponseWriter, _ *http.Request) {
	sseHeaders(w)
	writeEdit(w, "done", `{"reason":"kept it"}`)
	sseChunk(w, map[string]any{
		"index": 0, "delta": map[string]any{}, "finish_reason": "tool_calls",
	}, nil)
	sseChunk(w, nil, map[string]any{
		"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15,
		"completion_tokens_details": map[string]any{"reasoning_tokens": 2},
	})
	_, _ = io.WriteString(w, "data: [DONE]\n\n")
}

// A model that stalls round after round ends the turn with an error and no
// change to the page.
func TestMutateStallStreakEndsTheTurn(t *testing.T) {
	model := &scriptedModel{handlers: []http.HandlerFunc{stallAfterName}}
	srv := httptest.NewServer(model)
	defer srv.Close()
	m := stallMutator(srv, 10)

	res, err := m.Mutate(
		context.Background(),
		Site{HTML: PostsPlaceholder},
		Room{},
		Post{Body: "hi"},
	)
	if err == nil || !strings.Contains(err.Error(), "failed 3 rounds") {
		t.Fatalf("err = %v, want the stall streak named", err)
	}
	if model.count() != maxStallStreak {
		t.Errorf("requests = %d, want %d", model.count(), maxStallStreak)
	}
	if res.Done || res.Changed {
		t.Errorf("done %v, changed %v, want the page untouched", res.Done, res.Changed)
	}
}

// A stream that closes cleanly with a call but no arguments and no finish
// reason is a broken stream, so it is asked for again.
func TestMutateRetriesAStreamWithoutAFinishReason(t *testing.T) {
	early := func(w http.ResponseWriter, _ *http.Request) {
		sseHeaders(w)
		writeEdit(w, "edit", "")
		sseChunk(w, nil, map[string]any{
			"prompt_tokens": 10, "completion_tokens": 1, "total_tokens": 11,
		})
	}
	model := &scriptedModel{handlers: []http.HandlerFunc{early, realDone}}
	srv := httptest.NewServer(model)
	defer srv.Close()
	m := stallMutator(srv, 3)

	res, err := m.Mutate(
		context.Background(),
		Site{HTML: PostsPlaceholder},
		Room{},
		Post{Body: "hi"},
	)
	if err != nil {
		t.Fatalf("Mutate: %v", err)
	}
	if !res.Done || len(res.Rounds) != 2 {
		t.Fatalf("done %v, rounds %d, want a retry that finished", res.Done, len(res.Rounds))
	}
	if got := res.Rounds[0]; !got.Stalled || got.FinishReason != "no finish reason" {
		t.Errorf("first round = %+v", got)
	}
	if res.Rounds[1].Trace.ReasoningTokens != 2 {
		t.Errorf("reasoning tokens = %d, want 2", res.Rounds[1].Trace.ReasoningTokens)
	}
}

// A complete call whose only fault is a missing finish chunk is kept.
func TestMutateKeepsACompleteRoundWithoutAFinishReason(t *testing.T) {
	model := &scriptedModel{
		handlers: []http.HandlerFunc{func(w http.ResponseWriter, _ *http.Request) {
			sseHeaders(w)
			writeEdit(w, "done", `{"reason":"kept it"}`)
		}},
	}
	srv := httptest.NewServer(model)
	defer srv.Close()
	m := stallMutator(srv, 3)

	res, err := m.Mutate(
		context.Background(),
		Site{HTML: PostsPlaceholder},
		Room{},
		Post{Body: "hi"},
	)
	if err != nil || !res.Done || model.count() != 1 {
		t.Fatalf("err %v, done %v, requests %d, want one kept round", err, res.Done, model.count())
	}
}

// A stream that keeps moving, however slowly, is not a stall.
func TestMutateDoesNotCutASlowStream(t *testing.T) {
	slow := func(w http.ResponseWriter, _ *http.Request) {
		sseHeaders(w)
		writeEdit(w, "done", "")
		for _, piece := range []string{
			`{"reas`, `on":"s`, `low `, `but `, `alive`, `"}`, ` `, ` `, ` `, ` `, ` `,
		} {
			time.Sleep(50 * time.Millisecond)
			sseChunk(w, map[string]any{"index": 0, "delta": map[string]any{
				"tool_calls": []any{map[string]any{
					"index": 0, "function": map[string]any{"arguments": piece},
				}},
			}}, nil)
		}
		sseChunk(w, map[string]any{
			"index": 0, "delta": map[string]any{}, "finish_reason": "tool_calls",
		}, nil)
	}
	srv := httptest.NewServer(&scriptedModel{handlers: []http.HandlerFunc{slow}})
	defer srv.Close()
	m := stallMutator(srv, 3)

	res, err := m.Mutate(
		context.Background(),
		Site{HTML: PostsPlaceholder},
		Room{},
		Post{Body: "hi"},
	)
	if err != nil || !res.Done {
		t.Fatalf("err %v, done %v, want the slow round to finish", err, res.Done)
	}
	if res.Rounds[0].Stalled {
		t.Error("a moving stream was marked stalled")
	}
}

// A cancelled turn is a cancel, not a stall: no retry, an error comes back.
func TestMutateDoesNotMistakeACancelForAStall(t *testing.T) {
	model := &scriptedModel{handlers: []http.HandlerFunc{stallAfterName}}
	srv := httptest.NewServer(model)
	defer srv.Close()
	m := stallMutator(srv, 3)
	m.idle = IdleLimits{First: time.Minute, Think: time.Minute, Call: time.Minute}

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()
	_, err := m.Mutate(ctx, Site{HTML: PostsPlaceholder}, Room{}, Post{Body: "hi"})
	if err == nil || !strings.Contains(err.Error(), "canceled") {
		t.Fatalf("err = %v, want a cancel", err)
	}
	if model.count() != 1 {
		t.Errorf("requests = %d, a cancel must not be retried", model.count())
	}
}

// A stalled or resampled round does not use up a work round.
func TestMutateStallsDoNotUseWorkRounds(t *testing.T) {
	model := &scriptedModel{handlers: []http.HandlerFunc{stallAfterName, stallAfterName, realDone}}
	srv := httptest.NewServer(model)
	defer srv.Close()
	m := stallMutator(srv, 1)

	res, err := m.Mutate(
		context.Background(),
		Site{HTML: PostsPlaceholder},
		Room{},
		Post{Body: "hi"},
	)
	if err != nil || !res.Done {
		t.Fatalf("err %v, done %v, want done after two stalls with one work round", err, res.Done)
	}
	if res.DoneRound != 3 {
		t.Errorf("done round = %d, want 3", res.DoneRound)
	}
}

// A call cut off by the output cap is rejected with a message, not resampled.
func TestMutateRejectsATruncatedCall(t *testing.T) {
	cut := func(w http.ResponseWriter, _ *http.Request) {
		sseHeaders(w)
		writeEdit(w, "set_site", `{"html":"<main>{{posts}}`)
		sseChunk(w, map[string]any{
			"index": 0, "delta": map[string]any{}, "finish_reason": "length",
		}, nil)
	}
	model := &scriptedModel{handlers: []http.HandlerFunc{cut, realDone}}
	srv := httptest.NewServer(model)
	defer srv.Close()
	m := stallMutator(srv, 3)

	res, err := m.Mutate(
		context.Background(),
		Site{HTML: PostsPlaceholder},
		Room{},
		Post{Body: "hi"},
	)
	if err != nil || !res.Done {
		t.Fatalf("err %v, done %v", err, res.Done)
	}
	if res.Rounds[0].Resampled {
		t.Error("a truncated call was resampled")
	}
	if !strings.Contains(string(model.bodies[1]), "output cap") {
		t.Error("the next request does not tell the model its call was cut off")
	}
}

// A dropped round's markup never becomes the turn's reason.
func TestMutateFallbackReasonSkipsResampledMarkup(t *testing.T) {
	broken := func(w http.ResponseWriter, _ *http.Request) {
		sseHeaders(w)
		sseChunk(w, map[string]any{"index": 0, "delta": map[string]any{
			"content": "<｜DSML｜ leaked",
		}}, nil)
		writeEdit(w, "edit", `{"target":`)
		sseChunk(w, map[string]any{
			"index": 0, "delta": map[string]any{}, "finish_reason": "tool_calls",
		}, nil)
	}
	prose := func(w http.ResponseWriter, _ *http.Request) {
		sseHeaders(w)
		sseChunk(w, map[string]any{
			"index": 0, "delta": map[string]any{}, "finish_reason": "stop",
		}, nil)
	}
	srv := httptest.NewServer(&scriptedModel{handlers: []http.HandlerFunc{broken, prose}})
	defer srv.Close()
	m := stallMutator(srv, 3)

	res, err := m.Mutate(
		context.Background(),
		Site{HTML: PostsPlaceholder},
		Room{},
		Post{Body: "hi"},
	)
	if err != nil {
		t.Fatalf("Mutate: %v", err)
	}
	if strings.Contains(res.Reason, "DSML") {
		t.Errorf("reason = %q carries the dropped round's markup", res.Reason)
	}
}

// The trace holds raw chunks and counts, off the message text.
func TestMutateTraceStaysOutOfTheMessages(t *testing.T) {
	srv := httptest.NewServer(&scriptedModel{handlers: []http.HandlerFunc{realDone}})
	defer srv.Close()
	m := stallMutator(srv, 3)

	res, err := m.Mutate(
		context.Background(),
		Site{HTML: PostsPlaceholder},
		Room{},
		Post{Body: "hi"},
	)
	if err != nil {
		t.Fatalf("Mutate: %v", err)
	}
	trace := res.Rounds[0].Trace
	if trace == nil || len(trace.Head) != 2 || len(trace.Tail) == 0 || !trace.Finished {
		t.Fatalf("trace = %+v, want raw chunks and a finish", trace)
	}
	if trace.Attempts != 1 || trace.Status != 200 || trace.PromptRunes == 0 {
		t.Errorf("trace = %+v, want attempts, status and prompt size", trace)
	}
	if trace.Events.ToolName != 1 || trace.Events.ToolArgs != 1 || trace.Events.UsageOnly != 1 {
		t.Errorf("events = %+v", trace.Events)
	}
	for _, message := range res.Messages {
		if strings.Contains(message.Content, "chatcmpl-test") ||
			strings.Contains(message.Reasoning, "chatcmpl-test") {
			t.Error("raw chunk text reached a message body")
		}
	}
}

// A stream that closes with no chunk at all is broken and asked for again.
func TestMutateRetriesAnEmptyStream(t *testing.T) {
	empty := func(w http.ResponseWriter, _ *http.Request) { sseHeaders(w) }
	model := &scriptedModel{handlers: []http.HandlerFunc{empty, realDone}}
	srv := httptest.NewServer(model)
	defer srv.Close()
	m := stallMutator(srv, 3)

	res, err := m.Mutate(
		context.Background(),
		Site{HTML: PostsPlaceholder},
		Room{},
		Post{Body: "hi"},
	)
	if err != nil || !res.Done || len(res.Rounds) != 2 || !res.Rounds[0].Stalled {
		t.Fatalf(
			"err %v, done %v, rounds %+v, want a retry after the empty stream",
			err,
			res.Done,
			res.Rounds,
		)
	}
}

// A stall streak counts rounds in a row: a real round in between clears it, so
// two stalls, a real round and two more stalls still finish.
func TestMutateStallStreakResetsAfterARealRound(t *testing.T) {
	edit := func(w http.ResponseWriter, _ *http.Request) {
		sseHeaders(w)
		writeEdit(w, "edit", `{"target":"css","old":"","new":"h1{color:red}"}`)
		sseChunk(w, map[string]any{
			"index": 0, "delta": map[string]any{}, "finish_reason": "tool_calls",
		}, nil)
	}
	model := &scriptedModel{handlers: []http.HandlerFunc{
		stallAfterName, stallAfterName, edit, stallAfterName, stallAfterName, realDone,
	}}
	srv := httptest.NewServer(model)
	defer srv.Close()
	m := stallMutator(srv, 5)

	res, err := m.Mutate(
		context.Background(),
		Site{HTML: PostsPlaceholder},
		Room{},
		Post{Body: "hi"},
	)
	if err != nil || !res.Done {
		t.Fatalf("err %v, done %v, want the turn to finish", err, res.Done)
	}
	if !strings.Contains(res.Site.CSS, "h1{color:red}") {
		t.Errorf("css = %q, want the real round's edit", res.Site.CSS)
	}
}

// A cancel that lands during the pause before a retry ends the turn at once.
func TestMutateCancelDuringTheRetryPause(t *testing.T) {
	model := &scriptedModel{handlers: []http.HandlerFunc{stallAfterName}}
	srv := httptest.NewServer(model)
	defer srv.Close()
	m := stallMutator(srv, 3)
	m.stallPause = 30 * time.Second

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(1200 * time.Millisecond)
		cancel()
	}()
	started := time.Now()
	_, err := m.Mutate(ctx, Site{HTML: PostsPlaceholder}, Room{}, Post{Body: "hi"})
	if err == nil || !strings.Contains(err.Error(), "canceled") {
		t.Fatalf("err = %v, want a cancel", err)
	}
	if time.Since(started) > 5*time.Second || model.count() != 1 {
		t.Errorf(
			"took %s with %d requests, want the pause cut short",
			time.Since(started),
			model.count(),
		)
	}
}

// A zero limit turns a phase's check off, even after an earlier phase armed it.
func TestWatchdogZeroLimitDisarms(t *testing.T) {
	fired := make(chan struct{}, 1)
	dog := newWatchdog(func() { fired <- struct{}{} })
	defer dog.stop()
	dog.reset(50 * time.Millisecond)
	dog.reset(0)
	select {
	case <-fired:
		t.Fatal("the watchdog fired after a zero limit")
	case <-time.After(200 * time.Millisecond):
	}
}
