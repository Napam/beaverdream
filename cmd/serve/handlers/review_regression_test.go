//nolint:bodyclose // response bodies are closed in each test.
package handlers_test

import (
	"context"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Napam/beaverdream"
	"github.com/Napam/beaverdream/cmd/serve/handlers"
	"github.com/Napam/beaverdream/lib/registry"
	"github.com/Napam/beaverdream/lib/webmaster"
	"github.com/Napam/beaverdream/lib/worlds"
	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
)

func openAIForReview(in *instance) openai.Client {
	return openai.NewClient(option.WithBaseURL(in.mockURL), option.WithAPIKey("test"))
}

func TestReviewInvalidInviteRevokesSessionWithoutCSRF(t *testing.T) {
	in := newInstance(t, instanceOptions{})
	b := in.newBrowser(t)
	b.login(in, "admin", acceptanceAdminPassword)
	u, _ := url.Parse(in.appURL)
	var token string
	for _, c := range b.jar.Cookies(u) {
		if c.Name == handlers.AppCookieName {
			token = c.Value
		}
	}
	resp := b.post(
		in.appURLFor("/invite/not-a-code"),
		url.Values{"username": {"someone"}, "password": {"long-password"}},
	)
	_ = resp.Body.Close()
	_, _, err := in.reg.Authenticate(context.Background(), token, registry.ScopeApp)
	if err != nil {
		t.Errorf("invalid unauthenticated invite POST revoked existing app session: %v", err)
	}
}

func TestReviewAPITokenRevokeClosesItsStream(t *testing.T) {
	in := newInstance(t, instanceOptions{})
	_, token, err := in.createAdminAPIToken(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest("GET", in.appURLFor("/admin/worlds/heroes/live?page=overview"), nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	_, otherToken, err := in.createAdminAPIToken(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	otherReq, _ := http.NewRequest(
		"GET",
		in.appURLFor("/admin/worlds/heroes/live?page=overview"),
		nil,
	)
	otherReq.Header.Set("Authorization", "Bearer "+otherToken)
	other, err := http.DefaultClient.Do(otherReq)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = other.Body.Close() }()
	otherDone := make(chan struct{})
	go func() { _, _ = io.Copy(io.Discard, other.Body); close(otherDone) }()
	b := in.newBrowser(t)
	b.login(in, "admin", acceptanceAdminPassword)
	revoked := b.post(
		in.appURLFor("/account/tokens/revoke"),
		url.Values{"token": {token}, "csrf": {b.csrf(in, "/account")}},
	)
	_ = revoked.Body.Close()
	if _, _, err := in.reg.Authenticate(
		context.Background(),
		token,
		registry.ScopeAPI,
	); err == nil {
		t.Fatal("token not revoked")
	}
	done := make(chan struct{})
	go func() { _, _ = io.Copy(io.Discard, resp.Body); close(done) }()
	select {
	case <-done:
	case <-time.After(250 * time.Millisecond):
		t.Error("revoked API token's admin live stream remains open")
	}
	select {
	case <-otherDone:
		t.Error("revoking one API token closed another token's stream")
	case <-time.After(100 * time.Millisecond):
	}
}

type reviewBlockingFS struct {
	fs.FS
	entered, release chan struct{}
	once             sync.Once
}

func (b *reviewBlockingFS) Open(name string) (fs.File, error) {
	b.once.Do(func() { close(b.entered); <-b.release })
	return b.FS.Open(name)
}

func TestReviewDeleteDuringOpenDoesNotRecreateWorld(t *testing.T) {
	in := newInstance(t, instanceOptions{})
	block := &reviewBlockingFS{
		FS:      beaverdream.WorldMigrationsFS,
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	mgr := worlds.NewManager(in.reg, in.worlds.Dir(), block, logger)
	app := handlers.NewApp(
		logger,
		in.reg,
		mgr,
		func() *webmaster.ChangesetRunner { return webmaster.New(openAIForReview(in), "mock-model", 3, "") },
		handlers.AppConfig{},
	)
	defer app.Close()
	_, api, _ := in.createAdminAPIToken(context.Background())
	router := app.AppRouter()
	req := httptest.NewRequest("GET", "/admin/worlds/heroes/", nil)
	req.Header.Set("Authorization", "Bearer "+api)
	done := make(chan struct{})
	go func() { router.ServeHTTP(httptest.NewRecorder(), req); close(done) }()
	<-block.entered
	del := httptest.NewRequest("POST", "/admin/worlds/heroes/delete", nil)
	del.Header.Set("Authorization", "Bearer "+api)
	rec := httptest.NewRecorder()
	deleted := make(chan struct{})
	go func() { router.ServeHTTP(rec, del); close(deleted) }()
	select {
	case <-deleted:
		t.Fatal("delete did not wait for the world to open")
	case <-time.After(50 * time.Millisecond):
	}
	close(block.release)
	<-done
	<-deleted
	if rec.Code != 303 {
		t.Errorf("delete status %d: %s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(mgr.Path(in.heroesID)); err == nil {
		t.Error("opening request recreated deleted world file after successful delete")
	}
}

func TestReviewDisableWhileWorldLoadsClosesStream(t *testing.T) {
	in := newInstance(t, instanceOptions{})
	if _, err := in.reg.CreateIdentityWithPassword(
		context.Background(), "streamer", "streamer-password", "Streamer",
	); err != nil {
		t.Fatal(err)
	}
	id, sess, _, err := in.reg.Login(context.Background(), "streamer", "streamer-password")
	if err != nil {
		t.Fatal(err)
	}
	_, play, _ := in.reg.NewSession(context.Background(), id.ID, sess.LoginID, registry.ScopePlay)
	entered, release := make(chan struct{}), make(chan struct{})
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	app := handlers.NewApp(logger, in.reg, in.worlds, func() *webmaster.ChangesetRunner {
		close(entered)
		<-release
		return webmaster.New(openAIForReview(in), "mock-model", 3, "")
	}, handlers.AppConfig{})
	defer app.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req := httptest.NewRequest("GET", "/worlds/heroes/live", nil).WithContext(ctx)
	req.AddCookie(&http.Cookie{Name: handlers.PlayCookieName, Value: play})
	done := make(chan struct{})
	go func() { app.PlayRouter().ServeHTTP(httptest.NewRecorder(), req); close(done) }()
	<-entered
	_, api, _ := in.createAdminAPIToken(context.Background())
	disable := httptest.NewRequest(
		"POST",
		"/admin/identities/"+id.ID+"/disable",
		strings.NewReader(""),
	)
	disable.Header.Set("Authorization", "Bearer "+api)
	app.AppRouter().ServeHTTP(httptest.NewRecorder(), disable)
	close(release)
	select {
	case <-done:
	case <-time.After(250 * time.Millisecond):
		t.Error("stream authenticated before disable subscribes after cancellation and stays open")
	}
	cancel()
	<-done
}

func TestReviewWorldPanelSignoutFormWorks(t *testing.T) {
	in := newInstance(t, instanceOptions{})
	b := in.newBrowser(t)
	b.login(in, "admin", acceptanceAdminPassword)
	u, _ := url.Parse(in.appURL)
	var token string
	for _, c := range b.jar.Cookies(u) {
		if c.Name == handlers.AppCookieName {
			token = c.Value
		}
	}
	page := body(t, b.get(in.appURLFor("/admin/worlds/heroes/")))
	marker := `action="/logout"`
	if !strings.Contains(page, marker) {
		t.Fatal("expected rendered logout action missing")
	}
	resp := b.post(in.appURLFor("/logout"), url.Values{"csrf": {b.csrf(in, "/")}})
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 303 {
		t.Errorf("rendered sign-out form returns %d, want sign-out redirect", resp.StatusCode)
	}
	if _, _, err := in.reg.Authenticate(
		context.Background(),
		token,
		registry.ScopeApp,
	); err == nil {
		t.Error("sign-out did not revoke the app session")
	}
}

func TestReviewInviteRetryPreservesCode(t *testing.T) {
	in := newInstance(t, instanceOptions{})
	b := in.newBrowser(t)
	_, code, _ := in.reg.CreateInvite(
		context.Background(),
		in.adminID,
		registry.InviteParams{MaxUses: 1},
	)
	page := body(
		t,
		b.post(
			in.appURLFor("/invite/"+code),
			url.Values{"username": {"a"}, "password": {"long-password"}},
		),
	)
	if !strings.Contains(page, `action=""`) {
		t.Fatal("retry form does not post to the current URL")
	}
	resp := b.post(
		in.appURLFor("/invite/"+code),
		url.Values{"username": {"corrected"}, "password": {"long-password"}},
	)
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("retry status = %d", resp.StatusCode)
	}
}

func TestReviewMissingCreatingFileCleansSidecars(t *testing.T) {
	in := newInstance(t, instanceOptions{})
	id := "0123456789abcdef0123456789abcdef"
	_, err := in.reg.CreateWorldRecord(context.Background(), id, "unfinished", in.adminID)
	if err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		if err := os.WriteFile(in.worlds.Path(id)+suffix, []byte("unfinished"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	_, err = in.worlds.Recover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		if _, err := os.Stat(in.worlds.Path(id) + suffix); err == nil {
			t.Errorf("recovery removed creating row but left orphan %s", suffix)
		}
	}
}

type reviewPinnedBody struct {
	entered, release chan struct{}
	once             sync.Once
}

func (b *reviewPinnedBody) Read(p []byte) (int, error) {
	b.once.Do(func() { close(b.entered) })
	<-b.release
	return 0, io.EOF
}
func (b *reviewPinnedBody) Close() error { return nil }

func TestReviewCanceledDeleteCanBeRetried(t *testing.T) {
	in := newInstance(t, instanceOptions{})
	_, play, _ := in.reg.NewSession(
		context.Background(),
		in.adminID,
		in.adminLoginID,
		registry.ScopePlay,
	)
	_, api, _ := in.createAdminAPIToken(context.Background())
	pinned := &reviewPinnedBody{entered: make(chan struct{}), release: make(chan struct{})}
	post := httptest.NewRequest("POST", "/worlds/heroes/posts", pinned)
	post.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	post.AddCookie(&http.Cookie{Name: handlers.PlayCookieName, Value: play})
	posted := make(chan struct{})
	go func() { in.app.PlayRouter().ServeHTTP(httptest.NewRecorder(), post); close(posted) }()
	<-pinned.entered
	ctx, cancel := context.WithCancel(context.Background())
	del := httptest.NewRequest("POST", "/admin/worlds/heroes/delete", nil).WithContext(ctx)
	del.Header.Set("Authorization", "Bearer "+api)
	deleted := make(chan struct{})
	go func() { in.app.AppRouter().ServeHTTP(httptest.NewRecorder(), del); close(deleted) }()
	waitFor(t, "deleting", func() bool {
		world, err := in.reg.GetWorld(context.Background(), in.heroesID)
		return err == nil && world.Status == registry.WorldDeleting
	})
	cancel()
	<-deleted
	close(pinned.release)
	<-posted
	retryCtx, retryCancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer retryCancel()
	retry := httptest.NewRequest("POST", "/admin/worlds/heroes/delete", nil).
		WithContext(retryCtx)
	retry.Header.Set("Authorization", "Bearer "+api)
	rec := httptest.NewRecorder()
	in.app.AppRouter().ServeHTTP(rec, retry)
	if world, err := in.reg.GetWorld(context.Background(), in.heroesID); err == nil {
		t.Errorf(
			"retry cannot finish deletion after original request canceled: status=%s HTTP=%d",
			world.Status,
			rec.Code,
		)
	}
}

func TestReviewAdminRevokedWhileWorldLoads(t *testing.T) {
	in := newInstance(t, instanceOptions{})
	_, api, err := in.createAdminAPIToken(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	app := handlers.NewApp(logger, in.reg, in.worlds, func() *webmaster.ChangesetRunner {
		close(entered)
		<-release
		return webmaster.New(openAIForReview(in), "mock-model", 3, "")
	}, handlers.AppConfig{})
	defer app.Close()
	req := httptest.NewRequest("GET", "/admin/worlds/heroes/live?page=overview", nil)
	req.Header.Set("Authorization", "Bearer "+api)
	done := make(chan struct{})
	rec := httptest.NewRecorder()
	go func() { app.AppRouter().ServeHTTP(rec, req); close(done) }()
	<-entered
	if err := in.reg.RevokeAPIToken(context.Background(), api); err != nil {
		t.Fatal(err)
	}
	close(release)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("revoked admin stream stayed open")
	}
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d", rec.Code)
	}
}

func TestReviewCloseWaitsForLoadingWorld(t *testing.T) {
	in := newInstance(t, instanceOptions{})
	block := &reviewBlockingFS{
		FS:      beaverdream.WorldMigrationsFS,
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	mgr := worlds.NewManager(in.reg, in.worlds.Dir(), block, logger)
	app := handlers.NewApp(logger, in.reg, mgr, func() *webmaster.ChangesetRunner {
		return webmaster.New(openAIForReview(in), "mock-model", 3, "")
	}, handlers.AppConfig{})
	_, api, err := in.createAdminAPIToken(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("GET", "/admin/worlds/heroes/", nil)
	req.Header.Set("Authorization", "Bearer "+api)
	opened := make(chan struct{})
	go func() { app.AppRouter().ServeHTTP(httptest.NewRecorder(), req); close(opened) }()
	<-block.entered
	closed := make(chan struct{})
	go func() { app.Close(); close(closed) }()
	select {
	case <-closed:
		t.Fatal("close returned while the world was loading")
	case <-time.After(50 * time.Millisecond):
	}
	close(block.release)
	<-opened
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("close did not drain the load")
	}
	if app.LoadedForTest() != 0 {
		t.Fatal("close left a loaded world")
	}
}
