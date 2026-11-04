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
		`id="admin-body"`,
		`sign out`,
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
	if strings.Contains(body, testAdminToken) {
		t.Error("the page carries the admin token")
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
	if authed.Code != http.StatusSeeOther || authed.Header().Get("Location") != "/admin/site" {
		t.Fatalf("status = %d, want a redirect back to /admin/site", authed.Code)
	}
	req := httptest.NewRequest(http.MethodGet, "/admin/site", nil)
	req.AddCookie(authed.Result().Cookies()[0])
	page := httptest.NewRecorder()
	h.AdminSite(page, req)
	if !strings.Contains(page.Body.String(), "journal and shared state") {
		t.Fatal("the redirected page is missing the site panel")
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

// login posts the password. It answers with a redirect, so the next request
// carries the new cookie.
func login(t *testing.T, h *handlers.Handlers) *http.Cookie {
	t.Helper()
	req := httptest.NewRequest(
		http.MethodPost,
		"/admin",
		strings.NewReader("password="+testAdminToken),
	)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h.Admin(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("login status = %d, want a redirect", rec.Code)
	}
	cookies := rec.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("login set %d cookies, want one", len(cookies))
	}
	return cookies[0]
}

// panelWith fetches an admin page, asserting it is the panel and not the
// password form.
func panelWith(t *testing.T, h *handlers.Handlers, cookie *http.Cookie) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/admin", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.Admin(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `id="admin-body"`) {
		t.Fatalf("panel status = %d", rec.Code)
	}
	return rec.Body.String()
}

// csrfFrom reads the token the panel renders into its action forms.
func csrfFrom(t *testing.T, body string) string {
	t.Helper()
	const open = `name="csrf" value="`
	at := strings.Index(body, open)
	if at < 0 {
		t.Fatal("the panel carries no csrf token")
	}
	rest := body[at+len(open):]
	end := strings.Index(rest, `"`)
	if end < 0 {
		t.Fatal("the csrf token is not closed")
	}
	return rest[:end]
}

// A state change needs the token derived from the login. The board can send a
// form with the cookie attached, but it cannot read the admin page, so it
// cannot learn the token.
func TestAdminActionNeedsTheCSRFToken(t *testing.T) {
	h, queries := setup(t)
	cookie := login(t, h)
	csrf := csrfFrom(t, panelWith(t, h, cookie))

	send := func(fields url.Values) *httptest.ResponseRecorder {
		req := httptest.NewRequest(
			http.MethodPost,
			"/admin/undo",
			strings.NewReader(fields.Encode()),
		)
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.AddCookie(cookie)
		rec := httptest.NewRecorder()
		h.UndoLast(rec, req)
		return rec
	}

	// A second version gives undo something to copy forward.
	seed, err := queries.CurrentSite(context.Background())
	if err != nil {
		t.Fatalf("current site: %v", err)
	}
	if _, err := queries.CreateSiteVersion(context.Background(), sqlc.CreateSiteVersionParams{
		Css:    seed.Css,
		Note:   seed.Note,
		Header: seed.Header,
		Board:  seed.Board,
		Footer: seed.Footer,
		Reason: "test edit",
	}); err != nil {
		t.Fatalf("create version: %v", err)
	}
	countVersions := func() int {
		versions, err := queries.ListSiteVersions(context.Background(), 20)
		if err != nil {
			t.Fatalf("list versions: %v", err)
		}
		return len(versions)
	}
	before := countVersions()

	if rec := send(nil); rec.Code != http.StatusForbidden {
		t.Errorf("without the token: status = %d, want 403", rec.Code)
	}
	if rec := send(url.Values{"csrf": {"nope"}}); rec.Code != http.StatusForbidden {
		t.Errorf("a wrong token: status = %d, want 403", rec.Code)
	}
	if after := countVersions(); after != before {
		t.Errorf("a refused action changed the board: %d versions, was %d", after, before)
	}
	if rec := send(url.Values{"csrf": {csrf}}); rec.Code == http.StatusForbidden {
		t.Error("the right token is refused")
	}
	if after := countVersions(); after != before+1 {
		t.Errorf("the allowed action wrote %d versions, want one", after-before)
	}

	// A script with the admin token needs no token: the token is the secret.
	req := httptest.NewRequest(http.MethodPost, "/admin/undo", nil)
	req.Header.Set("X-Admin-Token", testAdminToken)
	rec := httptest.NewRecorder()
	h.UndoLast(rec, req)
	if rec.Code == http.StatusForbidden {
		t.Error("the admin token is refused without a csrf field")
	}
}

// A page on the board port can plant a cookie with the admin's name ahead of
// the real login. Any cookie that verifies is accepted, so that cannot lock
// the admin out.
func TestAdminIgnoresAPlantedCookie(t *testing.T) {
	h, _ := setup(t)
	cookie := login(t, h)

	req := httptest.NewRequest(http.MethodGet, "/admin", nil)
	req.AddCookie(&http.Cookie{Name: handlers.AdminCookieName, Value: "junk", Path: "/admin"})
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.Admin(rec, req)
	if !strings.Contains(rec.Body.String(), `id="admin-body"`) {
		t.Fatal("a planted cookie locked the admin out")
	}
}

// The remembered login is enough for the stream, which sends no header or
// token.
func TestAdminLiveAcceptsTheRememberedLogin(t *testing.T) {
	h, _ := setup(t)
	cookie := login(t, h)
	live := openSSE(t, h.AdminLive, "/admin/live?page=site", func(req *http.Request) {
		req.AddCookie(cookie)
	})
	live.next(t, []string{`id="admin-live"`, `waiting for a post`})
	// The stream's own fragments keep the action forms working.
	live.next(t, []string{`id="admin-version"`, `name="csrf"`})
}

// Signing out clears the cookie and shows the password form again.
func TestAdminLogoutForgetsTheLogin(t *testing.T) {
	h, _ := setup(t)
	cookie := login(t, h)
	csrf := csrfFrom(t, panelWith(t, h, cookie))

	req := httptest.NewRequest(http.MethodPost, "/admin/logout", strings.NewReader("csrf="+csrf))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.AdminLogout(rec, req)

	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `name="password"`) {
		t.Fatalf("status = %d, want the password form", rec.Code)
	}
	cleared := rec.Result().Cookies()
	if len(cleared) != 1 || cleared[0].Name != handlers.AdminCookieName ||
		cleared[0].MaxAge >= 0 {
		t.Errorf("logout cookies = %+v, want the login cleared", cleared)
	}
}

// The board's port sends admin paths to the admin port, keeping the host and
// path, so the link works from localhost and from a LAN address alike. The
// admin port serves no board.
func TestAdminRedirectsToItsOwnPort(t *testing.T) {
	h, _ := setupWithConfig(t, handlers.Config{
		AdminToken: testAdminToken,
		Port:       "6930",
		AdminPort:  "6932",
	})
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	board := h.RedirectAdmin(next)

	call := func(host string, target string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, target, nil)
		req.Host = host
		rec := httptest.NewRecorder()
		board.ServeHTTP(rec, req)
		return rec
	}

	redirect := call("172.29.101.116:6930", "/admin/turns/8?pick=1")
	if redirect.Code != http.StatusFound {
		t.Fatalf("status = %d, want a redirect", redirect.Code)
	}
	if got := redirect.Header().
		Get("Location"); got != "http://172.29.101.116:6932/admin/turns/8?pick=1" {
		t.Errorf("Location = %q", got)
	}
	if rec := call("localhost:6930", "/posts"); rec.Code != http.StatusNoContent {
		t.Errorf("a board path: status = %d, want it passed through", rec.Code)
	}

	// The header link back to the board crosses ports in the other direction.
	req := httptest.NewRequest(http.MethodGet, "/admin", nil)
	req.Host = "172.29.101.116:6932"
	if got := h.BoardURL(req); got != "http://172.29.101.116:6930/" {
		t.Errorf("board URL = %q", got)
	}
}
