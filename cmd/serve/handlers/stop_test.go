package handlers_test

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/Napam/beaverdream/cmd/serve/handlers"
)

// holdModel lets the first free calls through, then answers nothing until
// release closes or the client goes away.
func holdModel(
	calls *atomic.Int32,
	release chan struct{},
	free int32,
) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if calls.Add(1) <= free {
				next.ServeHTTP(w, r)
				return
			}
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// Stop turn cancels the running turn. It ends as an error labeled "stopped by
// admin", the page stays as it was, and the world takes the next turn.
func TestStopTurnEndsTheRunningTurn(t *testing.T) {
	var calls atomic.Int32
	release := make(chan struct{})
	var once sync.Once
	h, queries := setupWithModel(
		t,
		handlers.Config{},
		holdModel(&calls, release, 1),
	)
	// After the server's cleanup, so a fatal cannot leave a held call
	// blocking the close.
	t.Cleanup(func() { once.Do(func() { close(release) }) })
	ctx := context.Background()
	before, err := queries.CurrentSite(ctx)
	if err != nil {
		t.Fatalf("current site: %v", err)
	}

	// One turn runs to the end, so its id is a real turn that is not running.
	postForm(t, h.CreatePost, "/posts", url.Values{"body": {"mockedit make it red"}})
	var finished int64
	waitFor(t, "the first turn to finish", func() bool {
		turns, err := queries.ListChangesets(ctx, 1)
		if err != nil || len(turns) != 1 || turns[0].Status == "running" {
			return false
		}
		finished = turns[0].ID
		return true
	})
	before, err = queries.CurrentSite(ctx)
	if err != nil {
		t.Fatalf("current site: %v", err)
	}

	// Another author, because one author's rewrites are rate limited.
	postFormAs(t, h.CreatePost, "/posts", url.Values{"body": {"mockedit make it blue"}}, "erin")
	var id int64
	waitFor(t, "the second turn to be running", func() bool {
		turns, err := queries.ListChangesets(ctx, 1)
		if err != nil || len(turns) != 1 || turns[0].Status != "running" || calls.Load() < 2 {
			return false
		}
		id = turns[0].ID
		return true
	})

	// A stop that names the finished turn is refused and cancels nothing.
	old := strconv.FormatInt(finished, 10)
	rec := postAdminAt(t, h.StopTurn, "/admin/stop/"+old, old)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "not running") {
		t.Fatalf("stale stop: status %d, body missing \"not running\"", rec.Code)
	}
	turns, err := queries.ListChangesets(ctx, 1)
	if err != nil || turns[0].Status != "running" {
		t.Fatalf("a stop with a stale id ended the turn: %+v, %v", turns, err)
	}

	sid := strconv.FormatInt(id, 10)
	if rec := postAdminAt(t, h.StopTurn, "/admin/stop/"+sid, sid); rec.Code != http.StatusOK {
		t.Fatalf("stop: status = %d", rec.Code)
	}
	waitFor(t, "the stopped turn to be stored", func() bool {
		turns, err := queries.ListChangesets(ctx, 1)
		return err == nil && len(turns) == 1 && turns[0].Status != "running"
	})
	turns, err = queries.ListChangesets(ctx, 1)
	if err != nil {
		t.Fatalf("list turns: %v", err)
	}
	if turns[0].Status != "error" || turns[0].ErrorText != "stopped by admin" {
		t.Errorf("status %q, error %q, want an error labeled stopped by admin",
			turns[0].Status, turns[0].ErrorText)
	}
	after, err := queries.CurrentSite(ctx)
	if err != nil || after.ID != before.ID {
		t.Errorf("version %d, err %v, want the page left at %d", after.ID, err, before.ID)
	}
}
