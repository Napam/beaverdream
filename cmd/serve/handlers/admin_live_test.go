package handlers_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Napam/beaverdream/cmd/serve/handlers"
	"github.com/Napam/beaverdream/db/generated/sqlc"
)

// openAdminLive opens the stream of one admin page, named by its query.
func openAdminLive(t *testing.T, h *handlers.Handlers, query string, token string) *liveClient {
	t.Helper()
	return openSSE(t, h.AdminLive, "/admin/live?"+query, func(req *http.Request) {
		if token != "" {
			req.Header.Set("X-Admin-Token", token)
		}
	})
}

// Each page opens one stream for its own panels and polls nothing. The token
// rides in the stream's request headers, never in its URL.
func TestAdminPageOpensTheLiveStream(t *testing.T) {
	h, _ := setup(t)
	rec := getAdmin(t, h.AdminSite, "/admin/site")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{
		`id="admin-stream-site-0"`,
		`@get(&#39;/admin/live?page=site&#39;`,
		`requestCancellation: &#39;cleanup&#39;`,
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

	live := openAdminLive(t, h, "page=site", testAdminToken)
	live.next(t, []string{`id="admin-live"`, `waiting for a post`})
	live.next(t, []string{`id="admin-version"`})
	live.next(t, []string{`id="admin-versions"`, `v1`, "current"})
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
	h.AdminLive(rec, httptest.NewRequest(http.MethodGet, "/admin/live?page=site", nil))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rec.Code)
	}

	// A query token is not a credential, same as every other admin route.
	query := httptest.NewRecorder()
	h.AdminLive(
		query,
		httptest.NewRequest(http.MethodGet, "/admin/live?page=site&token="+testAdminToken, nil),
	)
	if query.Code != http.StatusNoContent {
		t.Fatalf("query token: status = %d, want 204", query.Code)
	}
}

// A visitor rewrite lands in the panel's history without a reload. The same
// stream patch carries the version and the turn that produced it.
func TestAdminLivePatchesHistoryAfterARewrite(t *testing.T) {
	h, _ := setup(t)
	live := openAdminLive(t, h, "page=turns", testAdminToken)
	live.next(t, []string{`id="admin-live"`})

	postForm(t, h.CreatePost, "/posts", url.Values{"body": {"mockedit please"}})
	live.next(t, []string{`id="admin-history"`, "mockedit please"})
}

// A run that keeps the page writes no version, and its turn still reaches the
// panel's history.
func TestAdminLivePatchesHistoryAfterAKeptTurn(t *testing.T) {
	h, _ := setup(t)
	live := openAdminLive(t, h, "page=turns", testAdminToken)
	live.next(t, []string{`id="admin-history"`, `nothing yet`})

	postForm(t, h.CreatePost, "/posts", url.Values{"body": {"mocknoop please"}})
	live.next(t, []string{`id="admin-history"`, "mocknoop please"})
}

// A finished run moves the overview's charts and budget, and the turns
// page's inspector, which shows the prompt section by section.
func TestAdminLivePatchesTheInspectorAndCharts(t *testing.T) {
	h, _ := setup(t)
	overview := openAdminLive(t, h, "page=overview", testAdminToken)
	overview.next(t, []string{`id="admin-metrics"`, "no finished turns yet"})
	turns := openAdminLive(t, h, "page=turns", testAdminToken)
	turns.next(t, []string{`id="admin-turn"`, "No turns yet"})

	postForm(t, h.CreatePost, "/posts", url.Values{"body": {"mockedit charts please"}})
	overview.next(t, []string{`id="admin-metrics"`, "tokens per turn", "last 1 finished turn"})
	overview.next(t, []string{`id="admin-budget"`, "turns since the summary"})
	turns.next(t, []string{
		`id="admin-turn"`,
		"following the newest turn",
		"what the model read",
		"room log",
		"stylesheet",
		"new post",
		"mockedit charts please",
		"round 1",
	})
}

// A turn has its own page. The panel fetches it with the token in a header and
// gets the body patched; without the token the path asks for the password and
// posts back to itself. Only a webmaster turn opens.
func TestAdminTurnPageShowsOneTurn(t *testing.T) {
	h, queries := setup(t)
	postAdmin(t, h.AdminMutate, "/admin/mutate", url.Values{"instruction": {"mockedit first"}})
	postAdmin(t, h.AdminMutate, "/admin/mutate", url.Values{"instruction": {"mocknoop second"}})
	turns, err := queries.ListWebmasterTurns(context.Background(), 10)
	if err != nil || len(turns) != 2 {
		t.Fatalf("turns = %d, err = %v", len(turns), err)
	}
	first := strconv.FormatInt(turns[1].ID, 10)

	open := func(id string, token string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/admin/turns/"+id, nil)
		req.Header.Set("Datastar-Request", "true")
		if token != "" {
			req.Header.Set("X-Admin-Token", token)
		}
		rec := httptest.NewRecorder()
		h.AdminTurn(rec, withURLParam(req, "id", id))
		return rec
	}

	rec := open(first, testAdminToken)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	for _, want := range []string{
		"selector #admin-body",
		"showing turn #" + first,
		"follow the newest",
		"mockedit first",
		"what the model read",
		"/admin/live?page=turns&amp;turn=" + first,
	} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Errorf("turn page is missing %q", want)
		}
	}

	login := open(first, "")
	if login.Code != http.StatusOK ||
		!strings.Contains(login.Body.String(), `action="/admin/turns/`+first+`"`) {
		t.Errorf("without the token: status = %d, want the password form posting here", login.Code)
	}
	if strings.Contains(login.Body.String(), "mockedit first") {
		t.Error("the password form carries the turn")
	}

	created, err := queries.CreatePost(context.Background(), sqlc.CreatePostParams{
		Author: "dave",
		Body:   "just a post",
	})
	if err != nil {
		t.Fatalf("create post: %v", err)
	}
	post := strconv.FormatInt(created.ID, 10)
	if rec := open(post, testAdminToken); rec.Code != http.StatusNotFound {
		t.Errorf("a non-webmaster turn: status = %d, want 404", rec.Code)
	}
}

// The password form posts back to the page it was asked for, so a reload or a
// new tab lands where it was.
func TestAdminLoginReturnsToThePage(t *testing.T) {
	h, _ := setupWithConfig(t, handlers.Config{AdminToken: "secret"})
	form := httptest.NewRecorder()
	h.AdminSite(form, httptest.NewRequest(http.MethodGet, "/admin/site", nil))
	if !strings.Contains(form.Body.String(), `action="/admin/site"`) {
		t.Fatal("the password form does not post back to /admin/site")
	}
	authed := postForm(t, h.AdminSite, "/admin/site", url.Values{"password": {"secret"}})
	if authed.Code != http.StatusOK ||
		!strings.Contains(authed.Body.String(), "journal and shared state") {
		t.Fatalf("status = %d, want the site page", authed.Code)
	}
}

// A picked turn stays put: its stream moves the list and never the inspector.
func TestAdminLiveLeavesAPickedTurnAlone(t *testing.T) {
	h, queries := setup(t)
	postAdmin(t, h.AdminMutate, "/admin/mutate", url.Values{"instruction": {"mocknoop first"}})
	turns, err := queries.ListWebmasterTurns(context.Background(), 1)
	if err != nil || len(turns) != 1 {
		t.Fatalf("turns = %d, err = %v", len(turns), err)
	}
	live := openAdminLive(
		t,
		h,
		"page=turns&turn="+strconv.FormatInt(turns[0].ID, 10),
		testAdminToken,
	)
	live.next(t, []string{`id="admin-history"`, "mocknoop first"})

	postAdmin(t, h.AdminMutate, "/admin/mutate", url.Values{"instruction": {"mocknoop second"}})
	live.next(t, []string{`id="admin-history"`, "mocknoop second"})
	live.quiet(t, `id="admin-turn"`, 200*time.Millisecond)
	for _, event := range live.seen {
		if strings.Contains(event.data, `id="admin-turn"`) {
			t.Fatal("the stream patched a picked inspector")
		}
	}
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
