//nolint:bodyclose // bodies are closed by body() or intentionally streamed.
package handlers_test

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Napam/beaverdream"
	"github.com/Napam/beaverdream/cmd/serve/handlers"
	"github.com/Napam/beaverdream/lib/mockllm"
	"github.com/Napam/beaverdream/lib/registry"
	"github.com/Napam/beaverdream/lib/webmaster"
	"github.com/Napam/beaverdream/lib/worlds"
	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
)

const acceptanceAdminPassword = "acceptance-password"

// instance is a whole app: registry, worlds, both real routers and the mock
// model. Servers are on separate random ports, both on 127.0.0.1.
type instance struct {
	app          *handlers.App
	reg          *registry.Registry
	worlds       *worlds.Manager
	appURL       string
	playURL      string
	mockURL      string
	adminID      string
	adminLoginID string
	heroesID     string
	atlasID      string
}

type instanceOptions struct {
	budget   registry.Budget
	clock    func() time.Time
	idleTime time.Duration
	model    func(http.Handler) http.Handler
}

func newInstance(t *testing.T, opts instanceOptions) *instance {
	t.Helper()
	dir := t.TempDir()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	regOpts := []registry.Option{
		registry.WithHasher(registry.NewArgon2id(1, 1024, 1)),
		registry.WithSessionTTLs(7*24*time.Hour, 0),
		registry.WithHandoffTTL(time.Minute),
	}
	if opts.clock != nil {
		regOpts = append(regOpts, registry.WithClock(opts.clock))
	}
	reg, err := registry.Open(
		context.Background(),
		filepath.Join(dir, "registry.db"),
		beaverdream.RegistryMigrationsFS,
		logger,
		regOpts...,
	)
	if err != nil {
		t.Fatalf("open registry: %v", err)
	}
	t.Cleanup(func() { _ = reg.Close() })

	boot, err := reg.EnsureSystemAdmin(context.Background(), acceptanceAdminPassword)
	if err != nil {
		t.Fatalf("system admin: %v", err)
	}

	manager := worlds.NewManager(
		reg, filepath.Join(dir, "worlds"), beaverdream.WorldMigrationsFS, logger,
	)
	if _, err := manager.Recover(context.Background()); err != nil {
		t.Fatalf("recover: %v", err)
	}
	heroes, err := manager.Create(context.Background(), boot.IdentityID, "heroes")
	if err != nil {
		t.Fatalf("create heroes: %v", err)
	}
	atlas, err := manager.Create(context.Background(), boot.IdentityID, "atlas")
	if err != nil {
		t.Fatalf("create atlas: %v", err)
	}

	modelHandler := mockllm.Handler()
	if opts.model != nil {
		modelHandler = opts.model(modelHandler)
	}
	mockServer := httptest.NewServer(modelHandler)
	t.Cleanup(mockServer.Close)
	client := openai.NewClient(
		option.WithBaseURL(mockServer.URL),
		option.WithAPIKey("test"),
	)

	// Listen first, so the public URLs the app hands out are the real ones.
	appLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen app: %v", err)
	}
	playLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		_ = appLn.Close()
		t.Fatalf("listen play: %v", err)
	}
	appURL := "http://" + appLn.Addr().String()
	playURL := "http://" + playLn.Addr().String()
	appPort := strconvPort(t, appLn.Addr())
	playPort := strconvPort(t, playLn.Addr())

	app := handlers.NewApp(logger, reg, manager, func() *webmaster.ChangesetRunner {
		return webmaster.New(client, "mock-model", 3, "")
	}, handlers.AppConfig{
		Config: handlers.Config{
			AppPort:        appPort,
			PlayPort:       playPort,
			AppURL:         appURL,
			PlayURL:        playURL,
			AppSessionTTL:  7 * 24 * time.Hour,
			PlaySessionTTL: 0,
			DatastarSrc:    beaverdream.StaticRootPath("static/datastar.js"),
			ThreeSrc:       beaverdream.StaticRootPath("static/three.module.js"),
			ThreeAddonsSrc: beaverdream.StaticRootPath("static/three-addons.js"),
		},
		Budget:           opts.budget,
		ModelConcurrency: 1,
		WorldIdleTimeout: opts.idleTime,
	})
	t.Cleanup(app.Close)

	appSrv := &httptest.Server{Listener: appLn, Config: &http.Server{Handler: app.AppRouter()}}
	playSrv := &httptest.Server{Listener: playLn, Config: &http.Server{Handler: app.PlayRouter()}}
	appSrv.Start()
	playSrv.Start()
	t.Cleanup(appSrv.Close)
	t.Cleanup(playSrv.Close)

	return &instance{
		app: app, reg: reg, worlds: manager,
		appURL: appURL, playURL: playURL, mockURL: mockServer.URL,
		adminID: boot.IdentityID, adminLoginID: boot.LoginID,
		heroesID: heroes.ID, atlasID: atlas.ID,
	}
}

func strconvPort(t *testing.T, addr net.Addr) string {
	t.Helper()
	_, port, err := net.SplitHostPort(addr.String())
	if err != nil {
		t.Fatalf("split port: %v", err)
	}
	return port
}

// browser is one simulated browser with its own cookie jar. Cookies ignore
// ports, as they do on localhost, so both planes see the jar.
type browser struct {
	t      *testing.T
	jar    http.CookieJar
	client *http.Client
}

func (in *instance) newBrowser(t *testing.T) *browser {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("cookie jar: %v", err)
	}
	return &browser{
		t:   t,
		jar: jar,
		client: &http.Client{
			Jar: jar,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				return http.ErrUseLastResponse
			},
			Timeout: 10 * time.Second,
		},
	}
}

func (b *browser) request(method, target string, form url.Values) *http.Response {
	b.t.Helper()
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req, err := http.NewRequest(method, target, body)
	if err != nil {
		b.t.Fatalf("request: %v", err)
	}
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	req.Header.Set("Accept", "text/html")
	resp, err := b.client.Do(req)
	if err != nil {
		b.t.Fatalf("%s %s: %v", method, target, err)
	}
	return resp
}

func (b *browser) get(target string) *http.Response {
	return b.request(http.MethodGet, target, nil)
}

func (b *browser) post(target string, form url.Values) *http.Response {
	return b.request(http.MethodPost, target, form)
}

func body(t *testing.T, resp *http.Response) string {
	t.Helper()
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return string(data)
}

// csrf reads the token the app renders into its forms.
func (b *browser) csrf(in *instance, path string) string {
	b.t.Helper()
	resp := b.get(in.appURLFor(path))
	page := body(b.t, resp)
	const marker = `name="csrf" value="`
	at := strings.Index(page, marker)
	if at < 0 {
		b.t.Fatalf("%s has no csrf token", path)
	}
	rest := page[at+len(marker):]
	end := strings.Index(rest, `"`)
	return rest[:end]
}

func (in *instance) appURLFor(path string) string  { return in.appURL + path }
func (in *instance) playURLFor(path string) string { return in.playURL + path }

func (b *browser) login(in *instance, username, password string) *http.Response {
	b.t.Helper()
	resp := b.post(in.appURLFor("/login"), url.Values{
		"username": {username}, "password": {password}, "next": {"/"},
	})
	if resp.StatusCode != http.StatusSeeOther {
		b.t.Fatalf("login %s: status = %d, body %s", username, resp.StatusCode, body(b.t, resp))
	}
	_ = resp.Body.Close()
	return resp
}

// play presses enter and follows the handoff to the world, returning the final
// URL and body.
func (b *browser) play(in *instance, slug string) (string, string) {
	b.t.Helper()
	csrf := b.csrf(in, "/")
	resp := b.post(in.appURLFor("/worlds/"+slug+"/play"), url.Values{"csrf": {csrf}})
	if resp.StatusCode != http.StatusSeeOther {
		b.t.Fatalf("play %s: status = %d, body %s", slug, resp.StatusCode, body(b.t, resp))
	}
	target := resp.Header.Get("Location")
	_ = resp.Body.Close()
	if !strings.HasPrefix(target, in.playURL) {
		b.t.Fatalf("handoff target = %q, want the play origin", target)
	}
	handoff := b.get(target)
	if handoff.StatusCode != http.StatusSeeOther {
		b.t.Fatalf("handoff: status = %d, body %s", handoff.StatusCode, body(b.t, handoff))
	}
	final := handoff.Header.Get("Location")
	_ = handoff.Body.Close()
	page := b.get(in.playURL + final)
	return in.playURL + final, body(b.t, page)
}

func (b *browser) postWorld(worldURL string, form url.Values) *http.Response {
	b.t.Helper()
	return b.post(strings.TrimRight(worldURL, "/")+"/posts", form)
}

func (in *instance) worldDB(t *testing.T, id string) *worlds.Handle {
	t.Helper()
	handle, err := in.worlds.Open(context.Background(), id)
	if err != nil {
		t.Fatalf("open world: %v", err)
	}
	t.Cleanup(func() { _ = handle.Close() })
	return handle
}

// createAdminAPIToken signs the admin in and mints a token from that app
// session, the same way the account page does.
func (in *instance) createAdminAPIToken(
	ctx context.Context,
) (*registry.Session, string, error) {
	_, session, _, err := in.reg.Login(ctx, "admin", acceptanceAdminPassword)
	if err != nil {
		return nil, "", err
	}
	return in.reg.CreateAPIToken(ctx, in.adminID, in.adminLoginID, session.Hash)
}

func (in *instance) redeem(
	t *testing.T,
	code, username string,
) (*registry.Identity, *registry.Session, string) {
	t.Helper()
	identity, session, token, err := in.reg.RedeemInvite(
		context.Background(), code, username, "correct horse battery", username,
	)
	if err != nil {
		t.Fatalf("redeem invite: %v", err)
	}
	return identity, session, token
}
