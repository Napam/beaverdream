package handlers_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Napam/beaverdream"
	"github.com/Napam/beaverdream/cmd/serve/handlers"
	"github.com/Napam/beaverdream/db"
	"github.com/Napam/beaverdream/db/generated/sqlc"
	"github.com/Napam/beaverdream/lib/mockllm"
	"github.com/Napam/beaverdream/lib/registry"
	"github.com/Napam/beaverdream/lib/webmaster"
	"github.com/Napam/beaverdream/web/pages"
	"github.com/go-chi/chi/v5"
	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	_ "modernc.org/sqlite"
)

const testAdminToken = "test-token"

func setup(t *testing.T) (*handlers.Handlers, *sqlc.Queries) {
	t.Helper()
	return setupWithConfig(t, handlers.Config{})
}

func setupWithConfig(t *testing.T, cfg handlers.Config) (*handlers.Handlers, *sqlc.Queries) {
	t.Helper()
	return setupWithModel(t, cfg, func(next http.Handler) http.Handler { return next })
}

// setupWithModel lets a test wrap the mock model, to fail or hold some calls.
func setupWithModel(
	t *testing.T,
	cfg handlers.Config,
	wrap func(http.Handler) http.Handler,
) (*handlers.Handlers, *sqlc.Queries) {
	t.Helper()

	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db") +
		"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)"
	dbConn, err := sql.Open("sqlite", dbPath)
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

	reg, err := registry.Open(
		context.Background(),
		filepath.Join(dir, "registry.db"),
		beaverdream.RegistryMigrationsFS,
		logger,
		registry.WithHasher(registry.NewArgon2id(1, 1024, 1)),
	)
	if err != nil {
		t.Fatalf("open registry: %v", err)
	}
	t.Cleanup(func() { _ = reg.Close() })

	mockServer := httptest.NewServer(wrap(mockllm.Handler()))
	t.Cleanup(mockServer.Close)

	client := openai.NewClient(
		option.WithBaseURL(mockServer.URL),
		option.WithAPIKey("test"),
	)
	queries := sqlc.New(dbConn)
	h := handlers.New(handlers.Deps{
		Logger:   logger,
		Registry: reg,
		World: registry.World{
			ID: "test-world", Slug: "test", Status: registry.WorldReady,
		},
		DB:      dbConn,
		Queries: queries,
		Runner:  webmaster.New(client, "mock-model", 3, ""),
		Config:  cfg,
	})
	t.Cleanup(h.Close)
	return h, queries
}

func setupCompaction(t *testing.T) (*handlers.Handlers, *sqlc.Queries) {
	t.Helper()
	return setupWithConfig(t, handlers.Config{
		CompactAfterTokens: 2500,
		CompactAfterTurns:  40,
	})
}

func withURLParam(req *http.Request, key, value string) *http.Request {
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add(key, value)
	return req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
}

// withIdentity makes a request as a signed-in play identity. The id is the
// name here because these unit tests do not seed a registry identity for every
// speaker; the world handler falls back to the id when it cannot resolve one.
func withIdentity(req *http.Request, name string) *http.Request {
	return handlers.WithTestPrincipal(req, name, name, false, true)
}

// withAdmin makes a request as the bootstrap admin on the app plane.
func withAdmin(req *http.Request) *http.Request {
	return handlers.WithTestAdmin(req, "admin", "admin")
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

// postAdmin sends an admin form with the token in the header, the way the
// panel does when Datastar drives it.
func postAdmin(
	t *testing.T,
	h http.HandlerFunc,
	path string,
	form url.Values,
) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req = withAdmin(req)
	rec := httptest.NewRecorder()
	h(rec, req)
	return rec
}

// postAdminAt is postAdmin for a route that carries an {id} parameter.
func postAdminAt(
	t *testing.T,
	h http.HandlerFunc,
	path string,
	id string,
) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, nil)
	req = withAdmin(req)
	rec := httptest.NewRecorder()
	h(rec, withURLParam(req, "id", id))
	return rec
}

// getAdmin is a GET with the token in the header.
func getAdmin(t *testing.T, h http.HandlerFunc, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req = withAdmin(req)
	rec := httptest.NewRecorder()
	h(rec, req)
	return rec
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
		req = withIdentity(req, name)
	}
	rec := httptest.NewRecorder()
	h(rec, req)
	return rec
}

func stateForm(
	t *testing.T,
	h http.HandlerFunc,
	key string,
	form url.Values,
	name string,
) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/state/"+key, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	if name != "" {
		req = withIdentity(req, name)
	}
	req = withURLParam(req, "key", key)
	rec := httptest.NewRecorder()
	h(rec, req)
	return rec
}

// waitFor polls until fn is true. Changesets run in the background now, so
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
	if got := rec.Header().Get("Location"); got != "/worlds/test/" {
		t.Errorf("Location = %q, want /worlds/test/ so the page stays at the top", got)
	}

	posts, err := queries.ListPosts(ctx)
	if err != nil {
		t.Fatalf("list posts: %v", err)
	}
	if len(posts) != 1 {
		t.Fatalf("posts = %d, want only the new one", len(posts))
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
	if !strings.Contains(after.Html, webmaster.PostsPlaceholder) {
		t.Errorf("new html lost the placeholder: %q", after.Html)
	}

	runs, err := queries.ListChangesets(ctx, 10)
	if err != nil {
		t.Fatalf("list runs: %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("runs = %d, want 1", len(runs))
	}
	if runs[0].Status != "changed" {
		t.Errorf("status = %q, want changed", runs[0].Status)
	}
	if runs[0].ID <= latest.ID {
		t.Error("the changeset did not come after the post it answered")
	}
	if !runs[0].VersionID.Valid {
		t.Error("the turn is not linked to the version it produced")
	}
	if !strings.Contains(runs[0].Messages, "set_site") {
		t.Error("run does not keep the tool calls")
	}
}

func TestSecondPostInTheCooldownIsStoredWithoutARewrite(t *testing.T) {
	h, queries := setup(t)
	ctx := context.Background()

	first := postForm(t, h.CreatePost, "/posts", url.Values{"body": {"one"}})
	if first.Code != http.StatusSeeOther {
		t.Fatalf("first status = %d", first.Code)
	}
	waitFor(t, "the first rewrite", func() bool {
		runs, err := queries.ListChangesets(ctx, 10)
		return err == nil && len(runs) == 1
	})

	second := postForm(t, h.CreatePost, "/posts", url.Values{"body": {"two"}})
	if second.Code != http.StatusSeeOther {
		t.Fatalf("second status = %d", second.Code)
	}
	posts, err := queries.ListPosts(ctx)
	if err != nil {
		t.Fatalf("list posts: %v", err)
	}
	if len(posts) != 2 || posts[len(posts)-1].Body != "two" {
		t.Fatalf("throttled post was lost: %+v", posts)
	}

	time.Sleep(200 * time.Millisecond)
	runs, err := queries.ListChangesets(ctx, 10)
	if err != nil {
		t.Fatalf("list runs: %v", err)
	}
	if len(runs) != 1 {
		t.Errorf("throttled post started %d runs, want 1", len(runs))
	}
}

func TestAdminChangesetDoesNotCreatePost(t *testing.T) {
	h, queries := setup(t)
	ctx := context.Background()

	rec := postAdmin(t, h.AdminChangeset, "/admin/changeset", url.Values{
		"instruction": {"make it pink"},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}

	posts, err := queries.ListPosts(ctx)
	if err != nil {
		t.Fatalf("list posts: %v", err)
	}
	if len(posts) != 0 {
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
	req = withIdentity(req, "dave")
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
		`action="posts"`,
		`posting as dave`,
		`/account`,
		`account`,
		`data-version="1"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("page is missing %q", want)
		}
	}
	if !strings.Contains(body, "no posts yet") {
		t.Error("empty board does not say it has no posts")
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
		Html:   `<script>document.title='owned'</script><h1>owned</h1>` + webmaster.PostsPlaceholder,
		Reason: "test",
	}); err != nil {
		t.Fatalf("create version: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req = withIdentity(req, "dave")
	rec := httptest.NewRecorder()
	h.Index(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `<script>document.title='owned'</script>`) {
		t.Error("model script was stripped from the page")
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
	if got := rec.Header().Get("Location"); got != "/worlds/test/" {
		t.Errorf("Location = %q, want /worlds/test/", got)
	}

	posts, err := queries.ListPosts(context.Background())
	if err != nil {
		t.Fatalf("list posts: %v", err)
	}
	if len(posts) != 0 {
		t.Fatalf("post without identity was stored: %+v", posts)
	}
}

type versionResponse struct {
	Version int64 `json:"version"`
	Post    int64 `json:"post"`
}

func getVersion(t *testing.T, h *handlers.Handlers, target string) versionResponse {
	t.Helper()
	rec := httptest.NewRecorder()
	h.Version(rec, httptest.NewRequest(http.MethodGet, target, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("%s: status = %d", target, rec.Code)
	}
	var resp versionResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("%s: decode %q: %v", target, rec.Body.String(), err)
	}
	return resp
}

func TestVersionReportsTheNewestPost(t *testing.T) {
	h, queries := setup(t)
	ctx := context.Background()

	before := getVersion(t, h, "/version")
	if before.Post != 0 {
		t.Errorf("post = %d on an empty board, want 0", before.Post)
	}

	first, err := queries.CreatePost(ctx, sqlc.CreatePostParams{Author: "dave", Body: "one"})
	if err != nil {
		t.Fatalf("create post: %v", err)
	}
	mid := getVersion(t, h, "/version")
	if mid.Post != first.ID {
		t.Errorf("post = %d, want %d", mid.Post, first.ID)
	}

	second, err := queries.CreatePost(ctx, sqlc.CreatePostParams{Author: "erin", Body: "two"})
	if err != nil {
		t.Fatalf("create post: %v", err)
	}
	after := getVersion(t, h, "/version")
	if after.Post != second.ID {
		t.Errorf("post = %d, want %d", after.Post, second.ID)
	}
	if after.Version != mid.Version {
		t.Errorf("version moved on a post: %d -> %d", mid.Version, after.Version)
	}
}

func TestIndexCarriesTheNewestPostID(t *testing.T) {
	h, queries := setup(t)
	ctx := context.Background()

	var newest int64
	for _, body := range []string{"one", "two"} {
		post, err := queries.CreatePost(ctx, sqlc.CreatePostParams{Author: "dave", Body: body})
		if err != nil {
			t.Fatalf("create post: %v", err)
		}
		newest = post.ID
	}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req = withIdentity(req, "dave")
	rec := httptest.NewRecorder()
	h.Index(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	want := `data-post="` + strconv.FormatInt(newest, 10) + `"`
	if !strings.Contains(rec.Body.String(), want) {
		t.Errorf("page is missing %s", want)
	}
	if got := getVersion(t, h, "/version").Post; got != newest {
		t.Errorf("/version post = %d, page says %d", got, newest)
	}
}

func TestAdminPanelNeedsAnAdminPrincipal(t *testing.T) {
	h, _ := setup(t)

	anonymous := httptest.NewRecorder()
	h.Admin(anonymous, httptest.NewRequest(http.MethodGet, "/admin", nil))
	if anonymous.Code != http.StatusNotFound {
		t.Fatalf("without principal: status = %d, want 404", anonymous.Code)
	}
	if strings.Contains(anonymous.Body.String(), "talk to the webmaster") {
		t.Fatal("an anonymous request carries admin data")
	}

	member := httptest.NewRecorder()
	req := handlers.WithTestPrincipal(
		httptest.NewRequest(http.MethodGet, "/admin", nil), "m", "member", false, true)
	h.Admin(member, req)
	if member.Code != http.StatusNotFound {
		t.Fatalf("member: status = %d, want 404", member.Code)
	}

	admin := httptest.NewRecorder()
	req = withAdmin(httptest.NewRequest(http.MethodGet, "/admin", nil))
	h.Admin(admin, req)
	if admin.Code != http.StatusOK ||
		!strings.Contains(admin.Body.String(), "talk to the webmaster") {
		t.Fatalf("admin: status = %d, want the panel", admin.Code)
	}
	if got := admin.Header().Get("Referrer-Policy"); got != "no-referrer" {
		t.Errorf("Referrer-Policy = %q", got)
	}
	if got := admin.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q", got)
	}
}

func TestStateReadsAndWritesDeclaredKeys(t *testing.T) {
	h, queries := setup(t)
	ctx := context.Background()

	seed, err := queries.CurrentSite(ctx)
	if err != nil {
		t.Fatalf("current site: %v", err)
	}
	if _, err := queries.CreateSiteVersion(ctx, sqlc.CreateSiteVersionParams{
		Css:         seed.Css,
		Html:        seed.Html,
		StateSchema: `{"votes":{"op":"counter"},"vibes":{"op":"list","max":2},"weather":{"op":"choice","options":["rain","sun"]}}`,
		Reason:      "state test",
	}); err != nil {
		t.Fatalf("create version: %v", err)
	}

	page := httptest.NewRecorder()
	pageReq := httptest.NewRequest(http.MethodGet, "/", nil)
	pageReq = withIdentity(pageReq, "dave")
	h.Index(page, pageReq)
	if !strings.Contains(page.Body.String(), `id="bd-state"`) {
		t.Error("board page does not carry the shared-state tag")
	}

	read := httptest.NewRecorder()
	h.State(read, httptest.NewRequest(http.MethodGet, "/state", nil))
	if read.Code != http.StatusOK {
		t.Fatalf("state status = %d", read.Code)
	}
	for _, want := range []string{`"votes":{"op":"counter"}`, `"votes":0`, `"vibes":[]`, `"weather":{}`} {
		if !strings.Contains(read.Body.String(), want) {
			t.Errorf("state response is missing %q: %s", want, read.Body.String())
		}
	}

	for i, name := range []string{"dave", "kit"} {
		want := i + 1
		rec := stateForm(t, h.SetState, "votes", url.Values{}, name)
		if rec.Code != http.StatusOK {
			t.Fatalf("vote status = %d", rec.Code)
		}
		if !strings.Contains(rec.Body.String(), `"value":`+strconv.Itoa(want)) {
			t.Errorf("vote response = %s", rec.Body.String())
		}
	}

	vibe := stateForm(t, h.SetState, "vibes", url.Values{"value": {"quiet"}}, "kit")
	if vibe.Code != http.StatusOK {
		t.Fatalf("list status = %d", vibe.Code)
	}
	if !strings.Contains(vibe.Body.String(), `"author":"kit"`) {
		t.Errorf("list response = %s", vibe.Body.String())
	}

	choice := stateForm(t, h.SetState, "weather", url.Values{"value": {"rain"}}, "dave")
	if choice.Code != http.StatusOK {
		t.Fatalf("choice status = %d", choice.Code)
	}
	if !strings.Contains(choice.Body.String(), `"rain":1`) {
		t.Errorf("choice response = %s", choice.Body.String())
	}

	bad := stateForm(t, h.SetState, "nope", url.Values{}, "dave")
	if bad.Code != http.StatusNotFound {
		t.Errorf("unknown key status = %d, want 404", bad.Code)
	}

	anonymous := stateForm(t, h.SetState, "votes", url.Values{}, "")
	if anonymous.Code != http.StatusUnauthorized {
		t.Errorf("anonymous status = %d, want 401", anonymous.Code)
	}
}

func TestResetClearsSharedState(t *testing.T) {
	h, queries := setup(t)
	ctx := context.Background()

	seed, err := queries.CurrentSite(ctx)
	if err != nil {
		t.Fatalf("current site: %v", err)
	}
	if _, err := queries.CreateSiteVersion(ctx, sqlc.CreateSiteVersionParams{
		Css:         seed.Css,
		Html:        seed.Html,
		StateSchema: `{"votes":{"op":"counter"}}`,
		Reason:      "state test",
	}); err != nil {
		t.Fatalf("create version: %v", err)
	}
	if rec := stateForm(t, h.SetState, "votes", url.Values{}, "dave"); rec.Code != http.StatusOK {
		t.Fatalf("vote status = %d", rec.Code)
	}

	reset := postAdmin(t, h.ResetWorld, "/admin/reset", url.Values{})
	if reset.Code != http.StatusOK {
		t.Fatalf("reset status = %d", reset.Code)
	}
	rows, err := queries.ListSharedState(ctx)
	if err != nil {
		t.Fatalf("list state: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("state survived the reset: %+v", rows)
	}
}

func TestUndoLastRestoresThePreviousVersion(t *testing.T) {
	h, queries := setup(t)
	ctx := context.Background()

	seed, err := queries.CurrentSite(ctx)
	if err != nil {
		t.Fatalf("current site: %v", err)
	}

	rec := postAdmin(t, h.AdminChangeset, "/admin/changeset", url.Values{
		"instruction": {"make it pink"},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("runChangeset status = %d", rec.Code)
	}
	changed, err := queries.CurrentSite(ctx)
	if err != nil {
		t.Fatalf("current site: %v", err)
	}
	if changed.ID <= seed.ID {
		t.Fatalf("changeset did not create a version: %d", changed.ID)
	}

	undo := postAdmin(t, h.UndoLast, "/admin/undo", url.Values{})
	if undo.Code != http.StatusOK {
		t.Fatalf("undo status = %d", undo.Code)
	}
	restored, err := queries.CurrentSite(ctx)
	if err != nil {
		t.Fatalf("current site: %v", err)
	}
	if restored.ID <= changed.ID {
		t.Fatalf("undo did not append a version: %d", restored.ID)
	}
	if restored.Css != seed.Css || restored.Html != seed.Html {
		t.Errorf("undo did not restore the previous version: %+v", restored)
	}
}

func TestHealSiteReturnsToTheSeed(t *testing.T) {
	h, queries := setup(t)
	ctx := context.Background()

	seed, err := queries.CurrentSite(ctx)
	if err != nil {
		t.Fatalf("current site: %v", err)
	}
	rec := postAdmin(t, h.AdminChangeset, "/admin/changeset", url.Values{
		"instruction": {"make it pink"},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("runChangeset status = %d", rec.Code)
	}
	changed, err := queries.CurrentSite(ctx)
	if err != nil {
		t.Fatalf("current site: %v", err)
	}
	if changed.Css == seed.Css {
		t.Fatal("the changeset did not change the site")
	}

	if err := queries.UpsertSharedState(ctx, sqlc.UpsertSharedStateParams{
		Key:   "votes",
		Value: "7",
	}); err != nil {
		t.Fatalf("upsert state: %v", err)
	}

	heal := postAdmin(t, h.HealSite, "/admin/heal", url.Values{})
	if heal.Code != http.StatusOK {
		t.Fatalf("heal status = %d", heal.Code)
	}
	healed, err := queries.CurrentSite(ctx)
	if err != nil {
		t.Fatalf("current site: %v", err)
	}
	if healed.ID <= changed.ID {
		t.Fatalf("heal did not append a version: %d", healed.ID)
	}
	if healed.Css != seed.Css || healed.Html != seed.Html {
		t.Errorf("heal did not restore the seed: %+v", healed)
	}
	posts, err := queries.ListPosts(ctx)
	if err != nil {
		t.Fatalf("list posts: %v", err)
	}
	if len(posts) != 0 {
		t.Errorf("heal changed the posts: %d", len(posts))
	}
	state, err := queries.ListSharedState(ctx)
	if err != nil {
		t.Fatalf("list state: %v", err)
	}
	if len(state) != 0 {
		t.Errorf("heal kept stale shared state: %+v", state)
	}

	again := postAdmin(t, h.HealSite, "/admin/heal", url.Values{})
	if again.Code != http.StatusOK {
		t.Fatalf("second heal status = %d", again.Code)
	}
	after, err := queries.CurrentSite(ctx)
	if err != nil {
		t.Fatalf("current site: %v", err)
	}
	if after.ID != healed.ID {
		t.Errorf("healing a plain board appended version %d", after.ID)
	}
}

func TestUndoLastAtTheSeedDoesNothing(t *testing.T) {
	h, queries := setup(t)
	ctx := context.Background()

	before, err := queries.CurrentSite(ctx)
	if err != nil {
		t.Fatalf("current site: %v", err)
	}
	rec := postAdmin(t, h.UndoLast, "/admin/undo", url.Values{})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	after, err := queries.CurrentSite(ctx)
	if err != nil {
		t.Fatalf("current site: %v", err)
	}
	if after.ID != before.ID {
		t.Errorf("undo at the seed created version %d", after.ID)
	}
}

func TestResetWorldClearsPostsAndHistory(t *testing.T) {
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

	reset := postAdmin(t, h.ResetWorld, "/admin/reset", url.Values{})
	if reset.Code != http.StatusOK {
		t.Fatalf("reset status = %d, want 200", reset.Code)
	}

	posts, err := queries.ListPosts(ctx)
	if err != nil {
		t.Fatalf("list posts: %v", err)
	}
	if len(posts) != 0 {
		t.Fatalf("posts after reset = %d, want an empty board", len(posts))
	}

	site, err := queries.CurrentSite(ctx)
	if err != nil {
		t.Fatalf("current site: %v", err)
	}
	if site.ID != 1 {
		t.Errorf("current version = %d, want the seed at 1", site.ID)
	}
	if site.TurnID.Valid {
		t.Errorf("current version belongs to turn %d, want the seed", site.TurnID.Int64)
	}

	runs, err := queries.ListChangesets(ctx, 10)
	if err != nil {
		t.Fatalf("list runs: %v", err)
	}
	if len(runs) != 0 {
		t.Errorf("changeset history survived the reset: %d", len(runs))
	}

	next, err := queries.CreateSiteVersion(ctx, sqlc.CreateSiteVersionParams{
		Html:   webmaster.PostsPlaceholder,
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
	if fresh.ID != 1 {
		t.Errorf("next post id = %d, want 1", fresh.ID)
	}
}

func TestResetWorldRequiresToken(t *testing.T) {
	h, queries := setup(t)
	ctx := context.Background()

	if _, err := queries.CreatePost(ctx, sqlc.CreatePostParams{
		Author: "dave",
		Body:   "keep me",
	}); err != nil {
		t.Fatalf("create post: %v", err)
	}

	rec := postForm(t, h.ResetWorld, "/admin/reset", url.Values{})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}

	posts, err := queries.ListPosts(ctx)
	if err != nil {
		t.Fatalf("list posts: %v", err)
	}
	if len(posts) != 1 {
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
	if len(posts) != 1 {
		t.Fatalf("post was lost: %d posts", len(posts))
	}

	waitFor(t, "the failed run", func() bool {
		runs, err := queries.ListChangesets(ctx, 10)
		return err == nil && len(runs) == 1 && runs[0].Status == "error"
	})

	after, err := queries.CurrentSite(ctx)
	if err != nil {
		t.Fatalf("current site: %v", err)
	}
	if after.ID != before.ID {
		t.Fatalf("version advanced on a failed changeset: %d -> %d", before.ID, after.ID)
	}

	runs, err := queries.ListChangesets(ctx, 10)
	if err != nil {
		t.Fatalf("list runs: %v", err)
	}
	if runs[0].ErrorText == "" {
		t.Error("failed run has no error text")
	}
}

// The whole point of the room log: the second turn can read what the first
// turn said and did, instead of starting from a bare snapshot.
func TestSecondTurnReadsTheFirst(t *testing.T) {
	h, queries := setup(t)
	ctx := context.Background()

	first := postFormAs(t, h.CreatePost, "/posts", url.Values{
		"body": {"make it matrix"},
	}, "dave")
	if first.Code != http.StatusSeeOther {
		t.Fatalf("first post status = %d", first.Code)
	}
	waitFor(t, "the first rewrite", func() bool {
		site, err := queries.CurrentSite(ctx)
		return err == nil && site.ID > 1
	})

	turns, err := queries.ListChangesets(ctx, 10)
	if err != nil {
		t.Fatalf("list turns: %v", err)
	}
	if len(turns) != 1 || turns[0].Status != "changed" {
		t.Fatalf("first turn = %+v", turns)
	}
	if turns[0].Said == "" || turns[0].Changes == "" {
		t.Fatalf("first turn did not record its words or change: %+v", turns[0])
	}

	second := postFormAs(t, h.CreatePost, "/posts", url.Values{
		"body": {"make it pink"},
	}, "kit")
	if second.Code != http.StatusSeeOther {
		t.Fatalf("second post status = %d", second.Code)
	}
	waitFor(t, "the second rewrite", func() bool {
		turns, err := queries.ListChangesets(ctx, 10)
		return err == nil && len(turns) == 2 && turns[0].Status == "changed"
	})

	turns, err = queries.ListChangesets(ctx, 10)
	if err != nil {
		t.Fatalf("list turns: %v", err)
	}
	var messages []webmaster.Message
	if err := json.Unmarshal([]byte(turns[0].Messages), &messages); err != nil {
		t.Fatalf("decode messages: %v", err)
	}
	if len(messages) < 2 || messages[1].Role != "user" {
		t.Fatalf("second turn has no user prompt: %+v", messages)
	}
	prompt := messages[1].Content

	if !strings.Contains(prompt, "ROOM LOG AFTER THAT, oldest first:") {
		t.Errorf("second prompt has no room log:\n%s", prompt)
	}
	if strings.Index(prompt, "ROOM LOG AFTER THAT") > strings.Index(prompt, "CURRENT STYLESHEET:") {
		t.Errorf("the room log comes after the page:\n%s", prompt)
	}
	if !strings.Contains(prompt, "#1 dave: make it matrix") {
		t.Errorf("second prompt forgot the first post:\n%s", prompt)
	}
	want := `you (v2): "` + turns[1].Said + `"`
	if !strings.Contains(prompt, want) {
		t.Errorf("second prompt does not show the first turn %q:\n%s", want, prompt)
	}
	if !strings.Contains(prompt, "["+turns[1].Changes+"]") {
		t.Errorf("second prompt does not show what the first turn changed:\n%s", prompt)
	}
}

func TestHealRecordsATurn(t *testing.T) {
	h, queries := setup(t)
	ctx := context.Background()

	postAdmin(t, h.AdminChangeset, "/admin/changeset", url.Values{
		"instruction": {"make it pink"},
	})
	rec := postAdmin(t, h.HealSite, "/admin/heal", url.Values{})
	if rec.Code != http.StatusOK {
		t.Fatalf("heal status = %d", rec.Code)
	}

	rows, err := queries.ListRoomLog(ctx, sqlc.ListRoomLogParams{
		ID:    0,
		ID_2:  1 << 60,
		Limit: 100,
	})
	if err != nil {
		t.Fatalf("list room log: %v", err)
	}
	found := false
	for _, row := range rows {
		if row.Kind == webmaster.TurnHeal {
			found = true
			if row.Body == "" {
				t.Error("heal turn has no explanation for the webmaster")
			}
		}
	}
	if !found {
		t.Fatalf("heal left no turn: %+v", rows)
	}
}

func TestNoopTurnIsKeptAndLeavesThePage(t *testing.T) {
	h, queries := setup(t)
	ctx := context.Background()

	before, err := queries.CurrentSite(ctx)
	if err != nil {
		t.Fatalf("current site: %v", err)
	}
	postForm(t, h.CreatePost, "/posts", url.Values{"body": {"mocknoop please"}})

	waitFor(t, "the kept turn", func() bool {
		turns, err := queries.ListChangesets(ctx, 10)
		return err == nil && len(turns) == 1 && turns[0].Status == "kept"
	})
	after, err := queries.CurrentSite(ctx)
	if err != nil {
		t.Fatalf("current site: %v", err)
	}
	if after.ID != before.ID {
		t.Errorf("a kept turn wrote version %d", after.ID)
	}
}

// Mirrors the handler's message threshold so these tests do not depend on an
// unexported constant across packages.
const compactionTurns = 40

// Compaction is the opencode shape: once the history gets big, replace it
// with one summary, then build a new log after that point. It runs after the
// turn lands, so the next turn is the first to read the summary, and the turn
// that triggered it stays in that log.
func TestCompactionReplacesTheHistoryWithOneMessage(t *testing.T) {
	h, queries := setupCompaction(t)
	ctx := context.Background()

	const posts = compactionTurns + 5
	for i := 1; i <= posts; i++ {
		if _, err := queries.CreatePost(ctx, sqlc.CreatePostParams{
			Author: "dave",
			Body:   "message " + strconv.Itoa(i) + " with enough words to look real",
		}); err != nil {
			t.Fatalf("create post %d: %v", i, err)
		}
	}

	rec := postAdmin(t, h.AdminChangeset, "/admin/changeset", url.Values{
		"instruction": {"make it pink"},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("admin runChangeset status = %d", rec.Code)
	}

	summary, err := queries.GetLatestSummary(ctx)
	if err != nil {
		t.Fatalf("no summary was written: %v", err)
	}
	if summary.ThroughTurnID != posts {
		t.Errorf("summary covers through turn %d, want %d", summary.ThroughTurnID, posts)
	}
	if summary.Text == "" {
		t.Error("summary is empty")
	}

	postAdmin(t, h.AdminChangeset, "/admin/changeset", url.Values{
		"instruction": {"mocknoop"},
	})
	turns, err := queries.ListChangesets(ctx, 10)
	if err != nil || len(turns) != 2 {
		t.Fatalf("list turns: %v %+v", err, turns)
	}
	var messages []webmaster.Message
	if err := json.Unmarshal([]byte(turns[0].Messages), &messages); err != nil {
		t.Fatalf("decode messages: %v", err)
	}
	prompt := messages[1].Content

	if !strings.Contains(prompt, "COMPACTED HISTORY:\n"+summary.Text) {
		t.Errorf("prompt does not carry the summary text:\n%s", prompt)
	}
	if strings.Contains(prompt, "#1 dave:") || strings.Contains(prompt, "#45 dave:") {
		t.Errorf("compacted post is still in the room log:\n%s", prompt)
	}
	if !strings.Contains(prompt, "#"+strconv.FormatInt(turns[1].ID, 10)+" you (v2)") {
		t.Errorf("the turn that compacted is missing from the next log:\n%s", prompt)
	}
}

// A post still waiting in the queue is answered by a later turn, so the
// summary must stop before it.
func TestCompactionLeavesQueuedPostsOut(t *testing.T) {
	h, queries := setupCompaction(t)
	ctx := context.Background()

	for i := 1; i <= compactionTurns+5; i++ {
		if _, err := queries.CreatePost(ctx, sqlc.CreatePostParams{
			Author: "dave",
			Body:   "message " + strconv.Itoa(i),
		}); err != nil {
			t.Fatalf("create post %d: %v", i, err)
		}
	}
	queued, err := queries.CreatePost(ctx, sqlc.CreatePostParams{
		Author: "kit",
		Body:   "waiting my turn",
	})
	if err != nil {
		t.Fatalf("create queued post: %v", err)
	}
	h.HoldPost(createdPostRow(queued))

	postAdmin(t, h.AdminChangeset, "/admin/changeset", url.Values{
		"instruction": {"make it pink"},
	})
	summary, err := queries.GetLatestSummary(ctx)
	if err != nil {
		t.Fatalf("no summary was written: %v", err)
	}
	if summary.ThroughTurnID != queued.ID-1 {
		t.Errorf("summary covers through %d, want %d", summary.ThroughTurnID, queued.ID-1)
	}
}

func createdPostRow(row sqlc.CreatePostRow) sqlc.Post { return sqlc.Post(row) }

// setupWithSummarizer serves the mock model, but sends every summarizer call
// to summary instead.
func setupWithSummarizer(
	t *testing.T,
	cfg handlers.Config,
	summary http.HandlerFunc,
) (*handlers.Handlers, *sqlc.Queries) {
	t.Helper()
	return setupWithModel(t, cfg, func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			body, _ := io.ReadAll(r.Body)
			r.Body = io.NopCloser(strings.NewReader(string(body)))
			if strings.Contains(string(body), "Summarize this stretch of the room") {
				summary(w, r)
				return
			}
			next.ServeHTTP(w, r)
		})
	})
}

// A summary that fails must not cost the turn its page.
func TestFailedSummaryStillLandsTheTurn(t *testing.T) {
	var calls atomic.Int32
	h, queries := setupWithSummarizer(t, handlers.Config{
		CompactAfterTokens: 2500,
		CompactAfterTurns:  compactionTurns,
	}, func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		http.Error(w, "summarizer down", http.StatusBadRequest)
	})
	ctx := context.Background()

	for i := 1; i <= compactionTurns+5; i++ {
		if _, err := queries.CreatePost(ctx, sqlc.CreatePostParams{
			Author: "dave",
			Body:   "message " + strconv.Itoa(i),
		}); err != nil {
			t.Fatalf("create post %d: %v", i, err)
		}
	}
	postAdmin(t, h.AdminChangeset, "/admin/changeset", url.Values{
		"instruction": {"make it pink"},
	})

	turns, err := queries.ListChangesets(ctx, 10)
	if err != nil || len(turns) != 1 || turns[0].Status != "changed" {
		t.Fatalf("turn did not land: %v %+v", err, turns)
	}
	if site, err := queries.CurrentSite(ctx); err != nil || site.ID != 2 {
		t.Fatalf("version did not land: %v %+v", err, site)
	}
	if summary, err := queries.GetLatestSummary(ctx); err == nil {
		t.Errorf("a failed summary was stored: %+v", summary)
	}
	if calls.Load() == 0 {
		t.Error("the summarizer was never asked")
	}
}

// The page is visible before a slow summary finishes.
func TestSlowSummaryDoesNotDelayTheVersion(t *testing.T) {
	release := make(chan struct{})
	h, queries := setupWithSummarizer(t, handlers.Config{
		CompactAfterTokens: 2500,
		CompactAfterTurns:  compactionTurns,
	}, func(w http.ResponseWriter, _ *http.Request) {
		<-release
		http.Error(w, "too late", http.StatusBadRequest)
	})
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
	})
	ctx := context.Background()

	for i := 1; i <= compactionTurns+5; i++ {
		if _, err := queries.CreatePost(ctx, sqlc.CreatePostParams{
			Author: "dave",
			Body:   "message " + strconv.Itoa(i),
		}); err != nil {
			t.Fatalf("create post %d: %v", i, err)
		}
	}
	postForm(t, h.CreatePost, "/posts", url.Values{"body": {"make it pink"}})

	waitFor(t, "the version while the summary is still running", func() bool {
		site, err := queries.CurrentSite(ctx)
		return err == nil && site.ID == 2
	})
	turns, err := queries.ListChangesets(ctx, 10)
	if err != nil || len(turns) != 1 || turns[0].Status != "changed" {
		t.Fatalf("turn is not finished while the summary runs: %v %+v", err, turns)
	}
	close(release)
}

// Under the message threshold but over the token estimate: long posts still
// force a compaction.
func TestCompactionTriggersOnTokenEstimate(t *testing.T) {
	h, queries := setupCompaction(t)
	ctx := context.Background()

	const posts = compactionTurns - 5
	for i := 1; i <= posts; i++ {
		if _, err := queries.CreatePost(ctx, sqlc.CreatePostParams{
			Author: "dave",
			Body:   strings.Repeat("x", 1000),
		}); err != nil {
			t.Fatalf("create post %d: %v", i, err)
		}
	}

	rec := postAdmin(t, h.AdminChangeset, "/admin/changeset", url.Values{
		"instruction": {"make it pink"},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("admin runChangeset status = %d", rec.Code)
	}
	if _, err := queries.GetLatestSummary(ctx); err != nil {
		t.Fatalf("token estimate did not trigger compaction: %v", err)
	}
}

func TestResetClearsCompactedMemory(t *testing.T) {
	h, queries := setupCompaction(t)
	ctx := context.Background()

	for i := 1; i <= compactionTurns+5; i++ {
		if _, err := queries.CreatePost(ctx, sqlc.CreatePostParams{
			Author: "dave",
			Body:   "message " + strconv.Itoa(i),
		}); err != nil {
			t.Fatalf("create post %d: %v", i, err)
		}
	}
	postAdmin(t, h.AdminChangeset, "/admin/changeset", url.Values{
		"instruction": {"make it pink"},
	})
	if _, err := queries.GetLatestSummary(ctx); err != nil {
		t.Fatalf("no summary before reset: %v", err)
	}

	reset := postAdmin(t, h.ResetWorld, "/admin/reset", url.Values{})
	if reset.Code != http.StatusOK {
		t.Fatalf("reset status = %d", reset.Code)
	}
	if summary, err := queries.GetLatestSummary(ctx); err == nil {
		t.Errorf("summary survived the reset: %+v", summary)
	}
}

// The default thresholds must actually fire. This failed when the trigger was
// measured on the capped summarizer chunk instead of the whole history.
func TestCompactionFiresAtDefaultThresholds(t *testing.T) {
	h, queries := setup(t)
	ctx := context.Background()

	// About 296 runes per turn after the 280-rune clip. The default prompt
	// budget is 200k tokens, so 3000 posts clear it.
	const posts = 3000
	for i := 1; i <= posts; i++ {
		if _, err := queries.CreatePost(ctx, sqlc.CreatePostParams{
			Author: "dave",
			Body:   strings.Repeat("x", 400),
		}); err != nil {
			t.Fatalf("create post %d: %v", i, err)
		}
	}

	rec := postAdmin(t, h.AdminChangeset, "/admin/changeset", url.Values{
		"instruction": {"make it pink"},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("admin runChangeset status = %d", rec.Code)
	}

	summary, err := queries.GetLatestSummary(ctx)
	if err != nil {
		t.Fatalf("default thresholds never compacted: %v", err)
	}
	if summary.ThroughTurnID == 0 {
		t.Errorf("summary covers no turns: %+v", summary)
	}
}

// Below the turn backstop the log keeps every turn since the last summary. A
// fixed 2000-turn cap would drop the oldest line on every turn instead.
func TestRoomLogKeepsEveryTurnBelowTheBackstop(t *testing.T) {
	h, queries := setupWithConfig(t, handlers.Config{
		CompactAfterTokens: 10_000_000,
		CompactAfterTurns:  3000,
	})
	ctx := context.Background()

	const posts = 2500
	for i := 1; i <= posts; i++ {
		if _, err := queries.CreatePost(ctx, sqlc.CreatePostParams{
			Author: "dave",
			Body:   "message " + strconv.Itoa(i),
		}); err != nil {
			t.Fatalf("create post %d: %v", i, err)
		}
	}
	postAdmin(t, h.AdminChangeset, "/admin/changeset", url.Values{
		"instruction": {"mocknoop"},
	})

	turns, err := queries.ListChangesets(ctx, 1)
	if err != nil || len(turns) != 1 {
		t.Fatalf("list turns: %v %+v", err, turns)
	}
	var messages []webmaster.Message
	if err := json.Unmarshal([]byte(turns[0].Messages), &messages); err != nil {
		t.Fatalf("decode messages: %v", err)
	}
	prompt := messages[1].Content
	if !strings.Contains(prompt, "#1 dave: message 1\n") {
		t.Error("the oldest uncompacted post fell out of the log")
	}
	if !strings.Contains(prompt, "#2500 dave: message 2500\n") {
		t.Error("the newest post is missing from the log")
	}
}

// Notes are the webmaster's memory, not part of the page. A failed turn must
// carry them forward, including the turn row that starts empty.
func TestFailedTurnKeepsThePreviousNotes(t *testing.T) {
	h, queries := setup(t)
	ctx := context.Background()

	if _, err := queries.CreateTurn(ctx, sqlc.CreateTurnParams{
		Kind:   webmaster.TurnChangeset,
		Author: "webmaster",
		Body:   "seed turn",
	}); err != nil {
		t.Fatalf("create turn: %v", err)
	}
	turns, err := queries.ListChangesets(ctx, 10)
	if err != nil || len(turns) != 1 {
		t.Fatalf("list turns: %v %+v", err, turns)
	}
	if err := queries.FinishTurn(ctx, sqlc.FinishTurnParams{
		Said:     "a good turn",
		Status:   "changed",
		Notes:    "dave promised to come back. kit owes me one.",
		Messages: "[]",
		ID:       turns[0].ID,
	}); err != nil {
		t.Fatalf("finish turn: %v", err)
	}

	postForm(t, h.CreatePost, "/posts", url.Values{"body": {"mockfail"}})
	waitFor(t, "the failed turn", func() bool {
		turns, err := queries.ListChangesets(ctx, 10)
		return err == nil && len(turns) == 2 && turns[0].Status == "error"
	})

	notes, err := queries.LatestNotes(ctx)
	if err != nil {
		t.Fatalf("latest notes: %v", err)
	}
	if !strings.Contains(notes, "kit owes me one") {
		t.Errorf("failed turn wiped the notes: %q", notes)
	}
}

// A reset between storing a post and running it deletes the post and rewinds
// the ids. The worker must not answer the deleted post, even when a new post
// has taken its id.
func TestResetDropsAQueuedPostThatReusedItsID(t *testing.T) {
	h, queries := setup(t)
	ctx := context.Background()

	stale, err := queries.CreatePost(ctx, sqlc.CreatePostParams{
		Author: "dave",
		Body:   "make it pink",
	})
	if err != nil {
		t.Fatalf("create post: %v", err)
	}

	reset := postAdmin(t, h.ResetWorld, "/admin/reset", url.Values{})
	if reset.Code != http.StatusOK {
		t.Fatalf("reset status = %d", reset.Code)
	}

	fresh, err := queries.CreatePost(ctx, sqlc.CreatePostParams{
		Author: "dave",
		Body:   "a different post",
	})
	if err != nil {
		t.Fatalf("create fresh post: %v", err)
	}
	if fresh.ID != stale.ID {
		t.Fatalf("fresh post id = %d, want the reused id %d", fresh.ID, stale.ID)
	}

	h.QueuePost(sqlc.Post(stale))
	h.RunPending()

	turns, err := queries.ListChangesets(ctx, 10)
	if err != nil {
		t.Fatalf("list turns: %v", err)
	}
	if len(turns) != 0 {
		t.Errorf("the worker answered a deleted post: %+v", turns)
	}
}

// A queued post that still matches its stored row is answered.
func TestRunPendingAnswersAStoredPost(t *testing.T) {
	h, queries := setup(t)
	ctx := context.Background()

	post, err := queries.CreatePost(ctx, sqlc.CreatePostParams{
		Author: "dave",
		Body:   "make it pink",
	})
	if err != nil {
		t.Fatalf("create post: %v", err)
	}
	h.QueuePost(sqlc.Post(post))
	h.RunPending()

	waitFor(t, "the rewrite", func() bool {
		turns, err := queries.ListChangesets(ctx, 10)
		return err == nil && len(turns) == 1
	})
}

// When the page alone is over the budget, summarizing the log cannot help, so
// nothing is compacted.
func TestCompactionSkipsWhenThePageAloneIsOverBudget(t *testing.T) {
	h, queries := setupWithConfig(t, handlers.Config{
		CompactAfterTokens: 10,
		CompactAfterTurns:  5,
	})
	ctx := context.Background()

	for i := 1; i <= 20; i++ {
		if _, err := queries.CreatePost(ctx, sqlc.CreatePostParams{
			Author: "dave",
			Body:   "message " + strconv.Itoa(i),
		}); err != nil {
			t.Fatalf("create post %d: %v", i, err)
		}
	}
	rec := postAdmin(t, h.AdminChangeset, "/admin/changeset", url.Values{
		"instruction": {"make it pink"},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("admin runChangeset status = %d", rec.Code)
	}
	if summary, err := queries.GetLatestSummary(ctx); err == nil {
		t.Errorf("compacted although the page fills the budget: %+v", summary)
	}
}

// The admin panel reads the same journal and shared state the page and the
// prompt do, so a stuck feed is visible without opening sqlite.
func TestAdminShowsTheJournalAndSharedState(t *testing.T) {
	h, queries := setup(t)
	ctx := context.Background()

	site, err := queries.CurrentSite(ctx)
	if err != nil {
		t.Fatalf("current site: %v", err)
	}
	if _, err := queries.CreateSiteVersion(ctx, sqlc.CreateSiteVersionParams{
		Css:           site.Css,
		Html:          site.Html,
		StateSchema:   `{"taps":{"op":"counter"}}`,
		JournalSchema: `{"moves":{"max":50,"rules":1}}`,
		Reason:        "test declares a feed",
	}); err != nil {
		t.Fatalf("create version: %v", err)
	}
	appendJournal(t, h.JournalAppend, "moves", `{"cell":4}`, 0, visitorA)
	if err := queries.UpsertSharedState(ctx, sqlc.UpsertSharedStateParams{
		Key:   "taps",
		Value: "7",
	}); err != nil {
		t.Fatalf("upsert state: %v", err)
	}

	site2 := getAdmin(t, h.AdminSite, "/admin/site")
	if site2.Code != http.StatusOK || !strings.Contains(site2.Body.String(), "journal schema") {
		t.Fatalf("site page = %d, want the journal schema", site2.Code)
	}
	if strings.Contains(site2.Body.String(), "taps = 7") {
		t.Error("the site page still shows shared state")
	}

	rec := getAdmin(t, h.AdminJournal, "/admin/journal")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{
		"shared state",
		"taps = 7",
		"1 / 50 rows",
		publicID(visitorA),
	} {
		if !strings.Contains(body, want) {
			t.Errorf("data page is missing %q", want)
		}
	}
}

// A reset must not wait behind a model call. It cancels the turn in flight and
// wipes the board, so the admin request answers now instead of minutes later.
func TestResetCancelsTheRunningTurn(t *testing.T) {
	var hold atomic.Bool
	hold.Store(true)
	inFlight := make(chan struct{})
	h, queries := setupWithModel(t, handlers.Config{},
		func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				// The server only notices a dropped client once the body is
				// read, so read it (and put it back) before blocking.
				body, _ := io.ReadAll(r.Body)
				r.Body = io.NopCloser(bytes.NewReader(body))
				if !hold.Load() {
					next.ServeHTTP(w, r)
					return
				}
				select {
				case <-inFlight:
				default:
					close(inFlight)
				}
				<-r.Context().Done()
			})
		})

	postForm(t, h.CreatePost, "/posts", url.Values{"body": {"hold that turn"}})
	select {
	case <-inFlight:
	case <-time.After(5 * time.Second):
		t.Fatal("the model call never started")
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		postAdmin(t, h.ResetWorld, "/admin/reset", url.Values{})
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("the reset waited for the model call")
	}

	ctx := context.Background()
	site, err := queries.CurrentSite(ctx)
	if err != nil {
		t.Fatalf("current site: %v", err)
	}
	if site.ID != 1 {
		t.Errorf("current version = %d, want the seed", site.ID)
	}
	posts, err := queries.ListPosts(ctx)
	if err != nil {
		t.Fatalf("list posts: %v", err)
	}
	if len(posts) != 0 {
		t.Errorf("reset left %d posts", len(posts))
	}

	// The worker has to start again: one post after the reset is enough.
	hold.Store(false)
	postFormAs(t, h.CreatePost, "/posts", url.Values{"body": {"after the reset"}}, "eve")
	waitFor(t, "the version after the reset", func() bool {
		current, err := queries.CurrentSite(ctx)
		return err == nil && current.ID == 2
	})
}

// An errored admin turn can be run again. The retry is a new turn with the
// same instruction; the failed turn stays in history untouched.
func TestRetryRunsAnErroredAdminTurn(t *testing.T) {
	h, queries := setup(t)
	ctx := context.Background()

	turn, err := queries.StartTurn(ctx, sqlc.StartTurnParams{
		Kind:   webmaster.TurnChangeset,
		Author: "admin",
		Body:   "mockedit please",
	})
	if err != nil {
		t.Fatalf("start turn: %v", err)
	}
	if err := queries.FailTurn(ctx, sqlc.FailTurnParams{
		ErrorText: "the model fell over",
		ID:        turn.ID,
	}); err != nil {
		t.Fatalf("fail turn: %v", err)
	}

	before, err := queries.CurrentSite(ctx)
	if err != nil {
		t.Fatalf("current site: %v", err)
	}

	id := strconv.FormatInt(turn.ID, 10)
	rec := postAdminAt(t, h.RetryTurn, "/admin/retry/"+id, id)
	if rec.Code != http.StatusOK {
		t.Fatalf("retry: status = %d", rec.Code)
	}

	after, err := queries.CurrentSite(ctx)
	if err != nil {
		t.Fatalf("current site after the retry: %v", err)
	}
	if after.ID <= before.ID {
		t.Fatalf("version = %d, want it past %d", after.ID, before.ID)
	}

	runs, err := queries.ListChangesets(ctx, 10)
	if err != nil || len(runs) != 2 {
		t.Fatalf("turns = %d, err = %v, want the failed turn and its retry", len(runs), err)
	}
	if runs[0].ID == turn.ID || runs[0].Status != "changed" || runs[0].Body != "mockedit please" {
		t.Errorf("retry = %+v, want a changed turn answering the same instruction", runs[0])
	}
	if runs[1].ID != turn.ID || runs[1].Status != "error" {
		t.Errorf("the failed turn changed: %+v", runs[1])
	}

	// Only an errored turn offers the button.
	page := func(id int64) string {
		req := httptest.NewRequest(http.MethodGet, "/admin/turns/"+strconv.FormatInt(id, 10), nil)
		req = withAdmin(req)
		rec := httptest.NewRecorder()
		h.AdminTurn(rec, withURLParam(req, "id", strconv.FormatInt(id, 10)))
		return rec.Body.String()
	}
	if !strings.Contains(page(turn.ID), "/admin/worlds/test/retry/"+id) {
		t.Error("the errored turn has no retry button")
	}
	if strings.Contains(page(runs[0].ID), "/admin/worlds/test/retry/") {
		t.Error("a turn that did not error offers a retry")
	}
}

// A post-triggered turn answers exactly the posts it had, not the ones that
// arrived while it ran. The board as it is now is what the retry rewrites.
func TestRetryAnswersThePostsItsTurnHad(t *testing.T) {
	h, queries := setup(t)
	ctx := context.Background()

	if _, err := queries.CreatePost(ctx, sqlc.CreatePostParams{
		Author: "dave",
		Body:   "mockedit make it pink",
	}); err != nil {
		t.Fatalf("create post: %v", err)
	}
	turn, err := queries.StartTurn(ctx, sqlc.StartTurnParams{
		Kind:   webmaster.TurnChangeset,
		Author: "dave",
	})
	if err != nil {
		t.Fatalf("start turn: %v", err)
	}
	if _, err := queries.CreatePost(ctx, sqlc.CreatePostParams{
		Author: "kit",
		Body:   "mockpatch later",
	}); err != nil {
		t.Fatalf("create later post: %v", err)
	}
	if err := queries.FailTurn(ctx, sqlc.FailTurnParams{
		ErrorText: "the model fell over",
		ID:        turn.ID,
	}); err != nil {
		t.Fatalf("fail turn: %v", err)
	}

	before, err := queries.CurrentSite(ctx)
	if err != nil {
		t.Fatalf("current site: %v", err)
	}
	id := strconv.FormatInt(turn.ID, 10)
	rec := postAdminAt(t, h.RetryTurn, "/admin/retry/"+id, id)
	if rec.Code != http.StatusOK {
		t.Fatalf("retry: status = %d", rec.Code)
	}
	after, err := queries.CurrentSite(ctx)
	if err != nil {
		t.Fatalf("current site after the retry: %v", err)
	}
	if after.ID <= before.ID {
		t.Fatalf("version = %d, want it past %d", after.ID, before.ID)
	}

	runs, err := queries.ListChangesets(ctx, 10)
	if err != nil || len(runs) != 2 {
		t.Fatalf("turns = %d, err = %v", len(runs), err)
	}
	var messages []webmaster.Message
	if err := json.Unmarshal([]byte(runs[0].Messages), &messages); err != nil {
		t.Fatalf("decode the retry's conversation: %v", err)
	}
	trigger := pages.NewTurnRow(runs[0], messages).Trigger
	if !strings.Contains(trigger, "mockedit make it pink") {
		t.Errorf("trigger = %q, want the post the turn had", trigger)
	}
	if strings.Contains(trigger, "mockpatch later") {
		t.Errorf("trigger = %q, answered a post that arrived later", trigger)
	}
}

// A retry that fails can be retried again. It has no posts of its own, so the
// handler walks back to the turn it copied instead of answering nothing.
func TestRetryOfARetryAnswersTheSamePosts(t *testing.T) {
	h, queries := setup(t)
	ctx := context.Background()

	post, err := queries.CreatePost(ctx, sqlc.CreatePostParams{
		Author: "dave",
		Body:   "mockedit make it pink",
	})
	if err != nil {
		t.Fatalf("create post: %v", err)
	}
	turn, err := queries.StartTurn(ctx, sqlc.StartTurnParams{
		Kind:   webmaster.TurnChangeset,
		Author: "dave",
	})
	if err != nil {
		t.Fatalf("start turn: %v", err)
	}
	if err := queries.FailTurn(
		ctx,
		sqlc.FailTurnParams{ErrorText: "boom", ID: turn.ID},
	); err != nil {
		t.Fatalf("fail turn: %v", err)
	}

	firstID := strconv.FormatInt(turn.ID, 10)
	if rec := postAdminAt(
		t,
		h.RetryTurn,
		"/admin/retry/"+firstID,
		firstID,
	); rec.Code != http.StatusOK {
		t.Fatalf("first retry: status = %d", rec.Code)
	}
	runs, err := queries.ListChangesets(ctx, 10)
	if err != nil || len(runs) != 2 {
		t.Fatalf("turns = %d, err = %v", len(runs), err)
	}
	retry := runs[0]
	if retry.Status != "changed" {
		t.Fatalf("first retry status = %q, want changed", retry.Status)
	}

	// Fail the retry itself, then retry it again.
	if err := queries.FailTurn(ctx, sqlc.FailTurnParams{
		ErrorText: "the retry fell over too",
		ID:        retry.ID,
	}); err != nil {
		t.Fatalf("fail the retry: %v", err)
	}
	id := strconv.FormatInt(retry.ID, 10)
	if rec := postAdminAt(t, h.RetryTurn, "/admin/retry/"+id, id); rec.Code != http.StatusOK {
		t.Fatalf("second retry: status = %d", rec.Code)
	}

	runs, err = queries.ListChangesets(ctx, 10)
	if err != nil || len(runs) != 3 {
		t.Fatalf("turns = %d, err = %v, want the failed retry and its retry", len(runs), err)
	}
	if runs[0].Status != "changed" {
		t.Fatalf("second retry status = %q, want changed", runs[0].Status)
	}
	var messages []webmaster.Message
	if err := json.Unmarshal([]byte(runs[0].Messages), &messages); err != nil {
		t.Fatalf("decode the second retry: %v", err)
	}
	trigger := pages.NewTurnRow(runs[0], messages).Trigger
	if !strings.Contains(trigger, post.Body) {
		t.Errorf("trigger = %q, want the post the first turn had", trigger)
	}
}

func TestBoardsRenderWithAMissingIdentity(t *testing.T) {
	h, queries := setup(t)
	if _, err := queries.CreatePost(context.Background(), sqlc.CreatePostParams{
		Author: "ghost-identity", Body: "still readable",
	}); err != nil {
		t.Fatalf("create post: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req = withIdentity(req, "dave")
	rec := httptest.NewRecorder()
	h.Index(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	page := rec.Body.String()
	if !strings.Contains(page, "ghost-identity") || !strings.Contains(page, "still readable") {
		t.Error("a post whose identity is missing did not render with its fallback id")
	}
}
