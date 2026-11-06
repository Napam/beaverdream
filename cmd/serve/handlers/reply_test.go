package handlers_test

import (
	"context"
	"net/http"
	"net/url"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/Napam/beaverdream/cmd/serve/handlers"
	"github.com/Napam/beaverdream/lib/webmaster"
)

// heldCall holds the first model call until stop is called, then lets it
// through to the mock; later calls pass straight through.
func heldCall(t *testing.T) (func(http.Handler) http.Handler, func()) {
	t.Helper()
	release := make(chan struct{})
	var once sync.Once
	stop := func() { once.Do(func() { close(release) }) }
	var calls atomic.Int32
	wrap := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if calls.Add(1) == 1 {
				<-release
			}
			next.ServeHTTP(w, r)
		})
	}
	return wrap, stop
}

// A reply lands on the board as the newest post, even when a post arrived
// while the turn ran and open pages already moved past the turn's id. It is
// never read back as a post that triggers a turn.
func TestReplyReachesTheBoardAfterPostsDuringTheRun(t *testing.T) {
	wrap, stop := heldCall(t)
	h, queries := setupWithModel(t, handlers.Config{}, wrap)
	t.Cleanup(stop)
	ctx := context.Background()

	live := openLive(t, h, cursor{Version: currentVersion(t, queries)})
	live.next(t, []string{`"shared":`})

	datastarPost(
		t,
		h.CreatePost,
		"/posts",
		url.Values{"body": {"why do beavers build dams? mockreply"}},
		"dave",
	)
	live.next(t, []string{"selector #bd-posts", "mockreply"})
	turnID := waitForRunningTurn(t, queries)

	datastarPost(t, h.CreatePost, "/posts", url.Values{"body": {"meanwhile mocknoop"}}, "erin")
	live.next(t, []string{"selector #bd-posts", "mode prepend", "meanwhile mocknoop"})

	stop()
	event := live.next(t, []string{"selector #bd-posts", "mode prepend", `data-kind="reply"`})
	for _, want := range []string{"post-webmaster", "beavers build dams", "&lt;deep&gt;"} {
		if !hasAll(event.data, []string{want}) {
			t.Errorf("reply patch is missing %q: %s", want, event.data)
		}
	}

	posts, err := queries.ListPosts(ctx)
	if err != nil {
		t.Fatalf("list posts: %v", err)
	}
	last := posts[len(posts)-1]
	if last.Kind != webmaster.TurnReply || last.ID <= turnID {
		t.Fatalf("newest row = %+v, want a reply after turn %d", last, turnID)
	}

	// The next turn answers the post that arrived mid-run. Its trigger range
	// covers the reply's id too, and the reply must not count as a trigger.
	waitFor(t, "the second turn", func() bool {
		turns, err := queries.ListWebmasterTurns(ctx, 2)
		return err == nil && len(turns) == 2 && turns[0].Status != "running"
	})
	turns, _ := queries.ListWebmasterTurns(ctx, 1)
	triggers, err := queries.ListPostsForTurn(ctx, turns[0].ID)
	if err != nil {
		t.Fatalf("posts for turn: %v", err)
	}
	if len(triggers) != 1 || triggers[0].Body != "meanwhile mocknoop" {
		t.Fatalf("triggers = %+v, want only the mid-run post", triggers)
	}
}

// A turn without a reply posts nothing.
func TestNoReplyPostsNothing(t *testing.T) {
	h, queries := setup(t)
	ctx := context.Background()
	postForm(t, h.CreatePost, "/posts", url.Values{"body": {"hello mocknoop"}})
	waitFor(t, "the turn", func() bool {
		turns, err := queries.ListWebmasterTurns(ctx, 1)
		return err == nil && len(turns) == 1 && turns[0].Status != "running"
	})
	posts, err := queries.ListPosts(ctx)
	if err != nil {
		t.Fatalf("list posts: %v", err)
	}
	if len(posts) != 1 {
		t.Fatalf("posts = %+v, want only the visitor's", posts)
	}
}
