package handlers_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/Napam/beaverdream/cmd/serve/handlers"
	"github.com/Napam/beaverdream/db/generated/sqlc"
)

func openAdminLive(t *testing.T, h *handlers.Handlers, token string) *liveClient {
	t.Helper()
	return openSSE(t, h.AdminLive, "/admin/live", func(req *http.Request) {
		if token != "" {
			req.Header.Set("X-Admin-Token", token)
		}
	})
}

// The panel opens one stream and polls nothing. The token rides in the stream's
// request headers, never in its URL.
func TestAdminPageOpensTheLiveStream(t *testing.T) {
	h, _ := setup(t)
	rec := getAdmin(t, h.Admin, "/admin")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{
		`id="admin-stream"`,
		`@get(&#39;/admin/live&#39;`,
		`X-Admin-Token`,
		`id="admin-body"`,
		`/static/datastar.js`,
		`data-on:submit="@post('/admin/heal'`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the admin page is missing %q", want)
		}
	}
	if strings.Contains(body, "setInterval") {
		t.Error("the admin page still polls")
	}
	if strings.Contains(body, "/admin/live?token=") {
		t.Error("the stream URL carries the token")
	}
}

// The first sync renders the card and the panels, and a state write reaches
// the feeds panel over the stream.
func TestAdminLivePushesThePanel(t *testing.T) {
	h, queries := setup(t)
	ctx := context.Background()
	seed, err := queries.CurrentSite(ctx)
	if err != nil {
		t.Fatalf("current site: %v", err)
	}
	if _, err := queries.CreateSiteVersion(ctx, sqlc.CreateSiteVersionParams{
		Css:         seed.Css,
		Note:        seed.Note,
		Header:      seed.Header,
		Board:       seed.Board,
		Footer:      seed.Footer,
		StateSchema: `{"likes":{"op":"counter"}}`,
		Reason:      "test declares state",
	}); err != nil {
		t.Fatalf("declare state: %v", err)
	}

	live := openAdminLive(t, h, testAdminToken)
	live.next(t, []string{`id="admin-live"`, `waiting for a post`})
	live.next(t, []string{`id="admin-version"`})
	live.next(t, []string{`id="admin-versions"`, `v1`, "current"})
	live.next(t, []string{`id="admin-memory"`, `nothing compacted yet`})
	live.next(t, []string{`id="admin-history"`, `nothing yet`})
	live.next(t, []string{`id="admin-feeds"`, `likes = 0`})

	rec := datastarPost(t, h.SetState, "/x/state/likes", nil, "dave")
	if rec.Code != http.StatusNoContent {
		t.Fatalf("state write: status = %d, want 204", rec.Code)
	}
	live.next(t, []string{`id="admin-feeds"`, `likes = 1`})
}

// The stream is admin-only and answers 204 so Datastar stops retrying a page
// the browser cannot fix.
func TestAdminLiveNeedsTheToken(t *testing.T) {
	h, _ := setup(t)
	rec := httptest.NewRecorder()
	h.AdminLive(rec, httptest.NewRequest(http.MethodGet, "/admin/live", nil))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rec.Code)
	}

	// A query token is not a credential, same as every other admin route.
	query := httptest.NewRecorder()
	h.AdminLive(
		query,
		httptest.NewRequest(http.MethodGet, "/admin/live?token="+testAdminToken, nil),
	)
	if query.Code != http.StatusNoContent {
		t.Fatalf("query token: status = %d, want 204", query.Code)
	}
}

// A visitor rewrite lands in the panel's history without a reload. The same
// stream patch carries the version and the turn that produced it.
func TestAdminLivePatchesHistoryAfterARewrite(t *testing.T) {
	h, _ := setup(t)
	live := openAdminLive(t, h, testAdminToken)
	live.next(t, []string{`id="admin-live"`})

	postForm(t, h.CreatePost, "/posts", url.Values{"body": {"mockedit please"}})
	live.next(t, []string{`id="admin-history"`, "mockedit please"})
}

// A run that keeps the page writes no version, and its turn still reaches the
// panel's history.
func TestAdminLivePatchesHistoryAfterAKeptTurn(t *testing.T) {
	h, _ := setup(t)
	live := openAdminLive(t, h, testAdminToken)
	live.next(t, []string{`id="admin-history"`, `nothing yet`})

	postForm(t, h.CreatePost, "/posts", url.Values{"body": {"mocknoop please"}})
	live.next(t, []string{`id="admin-history"`, "mocknoop please"})
}

// An admin instruction stored as a post reaches open boards, even when the
// run changes nothing.
func TestAdminPostReachesOpenBoards(t *testing.T) {
	h, queries := setup(t)
	live := openLive(t, h, cursor{Version: currentVersion(t, queries)})
	live.next(t, []string{`"shared":`})

	postAdmin(t, h.AdminMutate, "/admin/mutate", url.Values{
		"instruction": {"mocknoop from the admin"},
		"also_post":   {"on"},
	})
	live.next(t, []string{"selector #bd-posts", "mocknoop from the admin"})
}

// A Datastar action patches the panel in place, flash included, so no redirect
// has to carry the token.
func TestAdminActionPatchesThePanel(t *testing.T) {
	h, _ := setup(t)
	req := httptest.NewRequest(http.MethodPost, "/admin/heal", nil)
	req.Header.Set("X-Admin-Token", testAdminToken)
	req.Header.Set("Datastar-Request", "true")
	rec := httptest.NewRecorder()
	h.HealBoard(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("content type = %q, want an event stream", ct)
	}
	body := rec.Body.String()
	for _, want := range []string{
		"selector #admin-body",
		`id="admin-body"`,
		`id="admin-flash"`,
		"the board is already plain",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the patch is missing %q: %s", want, body)
		}
	}
	if strings.Contains(body, "text/html") && strings.Contains(body, "<html") {
		t.Error("the action answered with a whole page")
	}
}

// A plain action POST still works without Datastar: it sends the token in the
// form, renders the panel, and never redirects to a URL with the token.
func TestAdminActionWithoutDatastarRendersThePanel(t *testing.T) {
	h, _ := setup(t)
	rec := postForm(t, h.HealBoard, "/admin/heal", url.Values{
		"token": {testAdminToken},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := rec.Header().Get("Location"); got != "" {
		t.Errorf("Location = %q, want no redirect", got)
	}
	if !strings.Contains(rec.Body.String(), `id="admin-body"`) {
		t.Error("the response is missing the panel")
	}
}
