package handlers_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/Napam/beaverdream/cmd/serve/handlers"
	"github.com/Napam/beaverdream/db/generated/sqlc"
	"github.com/Napam/beaverdream/lib/webmaster"
)

// A turn saves its prompt while it runs, so a restart that cuts it off still
// leaves the admin what it read.
func TestInterruptedTurnKeepsItsPrompt(t *testing.T) {
	var calls atomic.Int32
	release := make(chan struct{})
	var once sync.Once
	h, queries := setupWithModel(
		t,
		handlers.Config{AdminToken: testAdminToken},
		holdModel(&calls, release, 0),
	)
	t.Cleanup(func() { once.Do(func() { close(release) }) })
	ctx := context.Background()

	postForm(t, h.CreatePost, "/posts", url.Values{"body": {"mockedit make it blue"}})
	var id int64
	waitFor(t, "the turn to be running", func() bool {
		turns, err := queries.ListWebmasterTurns(ctx, 1)
		if err != nil || len(turns) != 1 || turns[0].Status != "running" || calls.Load() < 1 {
			return false
		}
		id = turns[0].ID
		return true
	})

	// What a restart does to the turn left running.
	if err := queries.FailRunningTurns(ctx); err != nil {
		t.Fatalf("fail running turns: %v", err)
	}
	turn, err := queries.GetWebmasterTurn(ctx, id)
	if err != nil {
		t.Fatalf("get turn: %v", err)
	}
	if turn.Status != "error" {
		t.Fatalf("status = %q, want error", turn.Status)
	}
	var messages []webmaster.Message
	if err := json.Unmarshal([]byte(turn.Messages), &messages); err != nil {
		t.Fatalf("decode messages: %v", err)
	}
	prompt := false
	for _, message := range messages {
		if message.Role == "user" && strings.Contains(message.Content, "mockedit make it blue") {
			prompt = true
		}
	}
	if !prompt {
		t.Errorf("stored %d messages, want the prompt with the post", len(messages))
	}

	sid := strconv.FormatInt(id, 10)
	postAdminAt(t, h.StopTurn, "/admin/stop/"+sid, sid)
}

// A failed turn that saved nothing still names the post it was answering.
func TestFailedTurnWithoutMessagesShowsItsPost(t *testing.T) {
	h, queries := setup(t)
	ctx := context.Background()

	if _, err := queries.CreatePost(ctx, sqlc.CreatePostParams{
		Author: "dave",
		Body:   "give the board a moon",
	}); err != nil {
		t.Fatalf("create post: %v", err)
	}
	turn, err := queries.StartTurn(ctx, sqlc.StartTurnParams{
		Kind:   webmaster.TurnWebmaster,
		Author: "dave",
	})
	if err != nil {
		t.Fatalf("start turn: %v", err)
	}
	if err := queries.FailRunningTurns(ctx); err != nil {
		t.Fatalf("fail running turns: %v", err)
	}

	rec := getAdmin(t, h.AdminTurns, "/admin/turns")
	if rec.Code != http.StatusOK {
		t.Fatalf("turns page: status = %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "give the board a moon") {
		t.Errorf("turn %d: the turns page does not show the post it answered", turn.ID)
	}
}
