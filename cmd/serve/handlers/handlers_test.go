package handlers_test

import (
	"context"
	"database/sql"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Napam/beaverdream"
	"github.com/Napam/beaverdream/cmd/serve/handlers"
	"github.com/Napam/beaverdream/db"
	"github.com/Napam/beaverdream/db/generated/sqlc"
	"github.com/Napam/beaverdream/lib/mockllm"
	"github.com/Napam/beaverdream/lib/webmaster"
	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	_ "modernc.org/sqlite"
)

const testAdminToken = "test-token"

func setup(t *testing.T) (*handlers.Handlers, *sqlc.Queries) {
	t.Helper()
	return setupWithConfig(t, handlers.Config{AdminToken: testAdminToken})
}

func setupWithConfig(t *testing.T, cfg handlers.Config) (*handlers.Handlers, *sqlc.Queries) {
	t.Helper()

	dbConn, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = dbConn.Close() })

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if _, err := db.RunMigrations(
		context.Background(),
		beaverdream.MigrationsFS,
		dbConn,
		logger,
	); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	mockServer := httptest.NewServer(mockllm.Handler())
	t.Cleanup(mockServer.Close)

	client := openai.NewClient(
		option.WithBaseURL(mockServer.URL),
		option.WithAPIKey("test"),
	)
	queries := sqlc.New(dbConn)
	h := handlers.New(
		logger,
		dbConn,
		queries,
		webmaster.New(client, "mock-model", 3, ""),
		cfg,
	)
	return h, queries
}

func identityCookie(name string) *http.Cookie {
	return &http.Cookie{Name: handlers.IdentityCookieName, Value: url.QueryEscape(name)}
}

func postForm(
	t *testing.T,
	h http.HandlerFunc,
	path string,
	form url.Values,
) *httptest.ResponseRecorder {
	t.Helper()
	return postFormAs(t, h, path, form, "dave")
}

func postFormAs(
	t *testing.T,
	h http.HandlerFunc,
	path string,
	form url.Values,
	name string,
) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if name != "" {
		req.AddCookie(identityCookie(name))
	}
	rec := httptest.NewRecorder()
	h(rec, req)
	return rec
}

// waitFor polls until fn is true. Mutations run in the background now, so
// tests have to wait for the version or run to land.
func waitFor(t *testing.T, what string, fn func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestCreatePostStoresPostAndVersion(t *testing.T) {
	h, queries := setup(t)
	ctx := context.Background()

	before, err := queries.CurrentSite(ctx)
	if err != nil {
		t.Fatalf("current site: %v", err)
	}

	rec := postForm(t, h.CreatePost, "/posts", url.Values{
		"author": {"eve"},
		"body":   {"make it matrix"},
	})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", rec.Code)
	}

	posts, err := queries.ListPosts(ctx)
	if err != nil {
		t.Fatalf("list posts: %v", err)
	}
	if len(posts) != 5 {
		t.Fatalf("posts = %d, want 4 seed posts and the new one", len(posts))
	}
	latest := posts[len(posts)-1]
	if latest.Body != "make it matrix" {
		t.Fatalf("latest post = %+v", latest)
	}
	if latest.Author != "dave" {
		t.Errorf("author = %q, want the cookie identity", latest.Author)
	}

	waitFor(t, "a new version", func() bool {
		site, err := queries.CurrentSite(ctx)
		return err == nil && site.ID > before.ID
	})
	after, err := queries.CurrentSite(ctx)
	if err != nil {
		t.Fatalf("current site: %v", err)
	}
	if !strings.Contains(after.Css, "00ff6a") {
		t.Errorf("new version does not carry the theme: %q", after.Css)
	}
	if !strings.Contains(after.Board, webmaster.PostsPlaceholder) {
		t.Errorf("new board lost the placeholder: %q", after.Board)
	}

	runs, err := queries.ListMutationRuns(ctx, 10)
	if err != nil {
		t.Fatalf("list runs: %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("runs = %d, want 1", len(runs))
	}
	if runs[0].Status != "changed" {
		t.Errorf("status = %q, want changed", runs[0].Status)
	}
	if !runs[0].TriggerPostID.Valid {
		t.Error("run is not linked to the post that caused it")
	}
	if !strings.Contains(runs[0].Messages, "set_css") {
		t.Error("run does not keep the tool calls")
	}
}

func TestAdminMutateDoesNotCreatePost(t *testing.T) {
	h, queries := setup(t)
	ctx := context.Background()

	rec := postForm(t, h.AdminMutate, "/admin/mutate?token="+testAdminToken, url.Values{
		"instruction": {"make it pink"},
	})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", rec.Code)
	}

	posts, err := queries.ListPosts(ctx)
	if err != nil {
		t.Fatalf("list posts: %v", err)
	}
	if len(posts) != 4 {
		t.Fatalf("admin instruction changed the posts: %d", len(posts))
	}

	current, err := queries.CurrentSite(ctx)
	if err != nil {
		t.Fatalf("current site: %v", err)
	}
	if !strings.Contains(current.Css, "ff6fae") {
		t.Errorf("site was not restyled: %q", current.Css)
	}
}

func TestIndexServesTheModelPageInline(t *testing.T) {
	h, _ := setup(t)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(identityCookie("dave"))
	rec := httptest.NewRecorder()
	h.Index(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("content type = %q", ct)
	}
	body := rec.Body.String()
	for _, want := range []string{
		"<!doctype html>",
		"<h1>beaverdream</h1>",
		`class="postbox"`,
		`action="/posts"`,
		`data-version="1"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("page is missing %q", want)
		}
	}
	if strings.Contains(body, "rewrite") {
		t.Error("seed page gives away the gimmick")
	}
	if strings.Contains(body, "<iframe") || strings.Contains(body, "srcdoc") {
		t.Error("page is still sandboxed in an iframe")
	}
}

func TestIndexServesModelScripts(t *testing.T) {
	h, queries := setup(t)
	ctx := context.Background()

	if _, err := queries.CreateSiteVersion(ctx, sqlc.CreateSiteVersionParams{
		Header: `<script>document.title='owned'</script><h1>owned</h1>`,
		Board:  webmaster.PostsPlaceholder,
		Reason: "test",
	}); err != nil {
		t.Fatalf("create version: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(identityCookie("dave"))
	rec := httptest.NewRecorder()
	h.Index(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `<script>document.title='owned'</script>`) {
		t.Error("model script was stripped from the page")
	}
}

func TestIndexAsksForNameOnFirstVisit(t *testing.T) {
	h, _ := setup(t)

	rec := httptest.NewRecorder()
	h.Index(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", got)
	}
	body := rec.Body.String()
	for _, want := range []string{
		`action="/identity"`,
		`name="name"`,
		"shown next to your posts",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("identity page is missing %q", want)
		}
	}
	if strings.Contains(body, `action="/posts"`) {
		t.Error("first visit already shows the board form")
	}
}

func TestSetIdentityStoresCookie(t *testing.T) {
	h, _ := setup(t)

	rec := postFormAs(t, h.SetIdentity, "/identity", url.Values{
		"name": {"  Dave  "},
	}, "")
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", rec.Code)
	}
	if got := rec.Header().Get("Location"); got != "/" {
		t.Errorf("Location = %q, want /", got)
	}

	cookies := rec.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("cookies = %d, want 1", len(cookies))
	}
	cookie := cookies[0]
	if cookie.Name != handlers.IdentityCookieName {
		t.Errorf("cookie name = %q", cookie.Name)
	}
	if cookie.Value != "Dave" {
		t.Errorf("cookie value = %q, want Dave", cookie.Value)
	}
	if !cookie.HttpOnly {
		t.Error("identity cookie is not HttpOnly")
	}
	if cookie.SameSite != http.SameSiteLaxMode {
		t.Errorf("SameSite = %v, want Lax", cookie.SameSite)
	}
	if cookie.Path != "/" {
		t.Errorf("Path = %q, want /", cookie.Path)
	}
	if cookie.MaxAge <= 0 {
		t.Errorf("MaxAge = %d, want a persistent cookie", cookie.MaxAge)
	}

	board := httptest.NewRequest(http.MethodGet, "/", nil)
	board.AddCookie(cookie)
	page := httptest.NewRecorder()
	h.Index(page, board)
	if !strings.Contains(page.Body.String(), `action="/posts"`) {
		t.Error("identity cookie did not unlock the board")
	}
}

func TestSetIdentityDefaultsAnUsableName(t *testing.T) {
	h, _ := setup(t)

	for _, input := range []string{"", "   ", "<><><>", "admin", "ADMIN"} {
		rec := postFormAs(t, h.SetIdentity, "/identity", url.Values{
			"name": {input},
		}, "")
		if rec.Code != http.StatusSeeOther {
			t.Fatalf("input %q: status = %d, want 303", input, rec.Code)
		}
		cookies := rec.Result().Cookies()
		if len(cookies) != 1 {
			t.Fatalf("input %q: cookies = %d, want 1", input, len(cookies))
		}
		if cookies[0].Value != "Anonymous" {
			t.Errorf("input %q: value = %q, want Anonymous", input, cookies[0].Value)
		}
	}
}

func TestCreatePostWithoutIdentitySendsVisitorToTheGate(t *testing.T) {
	h, queries := setup(t)

	rec := postFormAs(t, h.CreatePost, "/posts", url.Values{
		"body": {"hello"},
	}, "")
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", rec.Code)
	}
	if got := rec.Header().Get("Location"); got != "/" {
		t.Errorf("Location = %q, want /", got)
	}

	posts, err := queries.ListPosts(context.Background())
	if err != nil {
		t.Fatalf("list posts: %v", err)
	}
	if len(posts) != 4 {
		t.Fatalf("post without identity was stored: %+v", posts)
	}
}

func TestAdminRequiresTokenWhenSet(t *testing.T) {
	h, _ := setupWithConfig(t, handlers.Config{AdminToken: "secret"})

	open := httptest.NewRecorder()
	h.Admin(open, httptest.NewRequest(http.MethodGet, "/admin", nil))
	if open.Code != http.StatusNotFound {
		t.Fatalf("without token: status = %d, want 404", open.Code)
	}

	wrong := httptest.NewRecorder()
	h.Admin(wrong, httptest.NewRequest(http.MethodGet, "/admin?token=nope", nil))
	if wrong.Code != http.StatusNotFound {
		t.Fatalf("wrong token: status = %d, want 404", wrong.Code)
	}

	authed := httptest.NewRecorder()
	h.Admin(authed, httptest.NewRequest(http.MethodGet, "/admin?token=secret", nil))
	if authed.Code != http.StatusOK {
		t.Fatalf("with token: status = %d, want 200", authed.Code)
	}
	if got := authed.Header().Get("Referrer-Policy"); got != "no-referrer" {
		t.Errorf("Referrer-Policy = %q", got)
	}
	if got := authed.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q", got)
	}
	if cookies := authed.Result().Cookies(); len(cookies) != 0 {
		t.Errorf("admin set %d cookies, want none", len(cookies))
	}
}

func TestResetBoardRestoresSeedPostsAndHistory(t *testing.T) {
	h, queries := setup(t)
	ctx := context.Background()

	rec := postForm(t, h.CreatePost, "/posts", url.Values{
		"author": {"dave"},
		"body":   {"make it matrix"},
	})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("post status = %d, want 303", rec.Code)
	}
	waitFor(t, "the rewrite", func() bool {
		site, err := queries.CurrentSite(ctx)
		return err == nil && site.ID > 1
	})

	reset := postForm(t, h.ResetBoard, "/admin/reset?token="+testAdminToken, url.Values{})
	if reset.Code != http.StatusSeeOther {
		t.Fatalf("reset status = %d, want 303", reset.Code)
	}

	posts, err := queries.ListPosts(ctx)
	if err != nil {
		t.Fatalf("list posts: %v", err)
	}
	if len(posts) != 4 {
		t.Fatalf("posts after reset = %d, want the 4 seed posts", len(posts))
	}
	if posts[0].Body != "first" || posts[1].Author != "kit" {
		t.Errorf("reset did not restore the seed posts: %+v", posts)
	}

	site, err := queries.CurrentSite(ctx)
	if err != nil {
		t.Fatalf("current site: %v", err)
	}
	if site.ID != 1 {
		t.Errorf("current version = %d, want the seed at 1", site.ID)
	}
	if site.Note != "welcome. be nice to each other." {
		t.Errorf("current version is not the seed: %q", site.Note)
	}

	runs, err := queries.ListMutationRuns(ctx, 10)
	if err != nil {
		t.Fatalf("list runs: %v", err)
	}
	if len(runs) != 0 {
		t.Errorf("mutation history survived the reset: %d", len(runs))
	}

	next, err := queries.CreateSiteVersion(ctx, sqlc.CreateSiteVersionParams{
		Board:  webmaster.PostsPlaceholder,
		Reason: "after reset",
	})
	if err != nil {
		t.Fatalf("create version: %v", err)
	}
	if next.ID != 2 {
		t.Errorf("next version id = %d, want 2", next.ID)
	}

	fresh, err := queries.CreatePost(ctx, sqlc.CreatePostParams{Body: "fresh"})
	if err != nil {
		t.Fatalf("create post: %v", err)
	}
	if fresh.ID != 5 {
		t.Errorf("next post id = %d, want 5", fresh.ID)
	}
}

func TestResetBoardRequiresToken(t *testing.T) {
	h, queries := setup(t)
	ctx := context.Background()

	if _, err := queries.CreatePost(ctx, sqlc.CreatePostParams{
		Author: "dave",
		Body:   "keep me",
	}); err != nil {
		t.Fatalf("create post: %v", err)
	}

	rec := postForm(t, h.ResetBoard, "/admin/reset", url.Values{})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}

	posts, err := queries.ListPosts(ctx)
	if err != nil {
		t.Fatalf("list posts: %v", err)
	}
	if len(posts) != 5 {
		t.Fatalf("unauthenticated reset changed the board: %d posts", len(posts))
	}
}

func TestModelFailureLeavesSiteAlone(t *testing.T) {
	h, queries := setup(t)
	ctx := context.Background()

	before, err := queries.CurrentSite(ctx)
	if err != nil {
		t.Fatalf("current site: %v", err)
	}

	rec := postForm(t, h.CreatePost, "/posts", url.Values{
		"author": {"dave"},
		"body":   {"mockfail"},
	})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", rec.Code)
	}

	posts, err := queries.ListPosts(ctx)
	if err != nil {
		t.Fatalf("list posts: %v", err)
	}
	if len(posts) != 5 {
		t.Fatalf("post was lost: %d posts", len(posts))
	}

	waitFor(t, "the failed run", func() bool {
		runs, err := queries.ListMutationRuns(ctx, 10)
		return err == nil && len(runs) == 1 && runs[0].Status == "error"
	})

	after, err := queries.CurrentSite(ctx)
	if err != nil {
		t.Fatalf("current site: %v", err)
	}
	if after.ID != before.ID {
		t.Fatalf("version advanced on a failed mutation: %d -> %d", before.ID, after.ID)
	}

	runs, err := queries.ListMutationRuns(ctx, 10)
	if err != nil {
		t.Fatalf("list runs: %v", err)
	}
	if runs[0].ErrorText == "" {
		t.Error("failed run has no error text")
	}
}
