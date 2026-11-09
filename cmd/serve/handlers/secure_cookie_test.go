//nolint:bodyclose // recorder bodies are read through rec.Body.
package handlers_test

import (
	"bytes"
	"context"
	"crypto/tls"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/Napam/beaverdream/cmd/serve/handlers"
	"github.com/Napam/beaverdream/lib/registry"
	"github.com/Napam/beaverdream/lib/webmaster"
)

// newSecureApp builds a second app over the same registry and worlds with the
// startup cookie mode set to secure. The LAN app the instance already serves
// stays untouched.
func newSecureApp(t *testing.T, in *instance) *handlers.App {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	app := handlers.NewApp(
		logger,
		in.reg,
		in.worlds,
		func() *webmaster.ChangesetRunner {
			return webmaster.New(openAIForReview(in), "mock-model", 3, "")
		},
		handlers.AppConfig{Config: handlers.Config{
			SecureCookies:  true,
			AppURL:         in.appURL,
			PlayURL:        in.playURL,
			AppSessionTTL:  7 * 24 * time.Hour,
			PlaySessionTTL: 0,
		}},
	)
	t.Cleanup(app.Close)
	return app
}

// serveOnce sends one request straight to a router, so the response's
// Set-Cookie headers can be inspected without a jar that refuses Secure
// cookies from an http origin.
func serveOnce(
	t *testing.T,
	handler http.Handler,
	method, path string,
	form url.Values,
	cookies []*http.Cookie,
	bearer string,
	withTLS bool,
) *httptest.ResponseRecorder {
	t.Helper()
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req := httptest.NewRequest(method, "http://app.test"+path, body)
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	if withTLS {
		req.TLS = &tls.ConnectionState{}
	}
	for _, cookie := range cookies {
		req.AddCookie(cookie)
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func setCookie(rec *httptest.ResponseRecorder, name string) *http.Cookie {
	for _, cookie := range rec.Result().Cookies() {
		if cookie.Name == name {
			return cookie
		}
	}
	return nil
}

func sessionCookieFor(name, value string) []*http.Cookie {
	return []*http.Cookie{{Name: name, Value: value}}
}

func TestSecureCookieNamesAndFlags(t *testing.T) {
	in := newInstance(t, instanceOptions{})
	secure := newSecureApp(t, in)
	ctx := context.Background()

	login := serveOnce(
		t, secure.AppRouter(), http.MethodPost, "/login",
		url.Values{"username": {"admin"}, "password": {acceptanceAdminPassword}},
		nil, "", false,
	)
	if login.Code != http.StatusSeeOther {
		t.Fatalf("login: status = %d, body %s", login.Code, login.Body.String())
	}
	appCookie := setCookie(login, handlers.SecureAppCookieName)
	if appCookie == nil {
		t.Fatal("secure login set no __Host- app cookie")
	}
	if !appCookie.Secure || !appCookie.HttpOnly || appCookie.Path != "/" ||
		appCookie.Domain != "" {
		t.Fatalf("secure app cookie = %+v", appCookie)
	}
	if plain := setCookie(login, handlers.AppCookieName); plain != nil {
		t.Fatal("secure login also set a plain-named app cookie")
	}

	account := serveOnce(
		t, secure.AppRouter(), http.MethodGet, "/account",
		nil, sessionCookieFor(handlers.SecureAppCookieName, appCookie.Value), "", false,
	)
	if account.Code != http.StatusOK ||
		!strings.Contains(account.Body.String(), `action="/account/name"`) {
		t.Fatalf("the __Host- app cookie did not authenticate: status %d", account.Code)
	}

	// The plain name is never read in secure mode, even with a valid token.
	plainRead := serveOnce(
		t, secure.AppRouter(), http.MethodGet, "/account",
		nil, sessionCookieFor(handlers.AppCookieName, appCookie.Value), "", false,
	)
	if strings.Contains(plainRead.Body.String(), `action="/account/name"`) {
		t.Fatal("a plain-named app cookie authenticated in secure mode")
	}

	// The handoff writes the play cookie under the __Host- name too.
	_, session, _, err := in.reg.Login(ctx, "admin", acceptanceAdminPassword)
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	code, _, err := in.reg.CreateHandoff(
		ctx, in.adminID, in.adminLoginID, session.Hash, in.heroesID,
	)
	if err != nil {
		t.Fatalf("create handoff: %v", err)
	}
	handoff := serveOnce(
		t, secure.PlayRouter(), http.MethodGet, "/handoff?code="+url.QueryEscape(code),
		nil, nil, "", false,
	)
	if handoff.Code != http.StatusSeeOther {
		t.Fatalf("handoff: status = %d", handoff.Code)
	}
	playCookie := setCookie(handoff, handlers.SecurePlayCookieName)
	if playCookie == nil || !playCookie.Secure {
		t.Fatalf("handoff play cookie = %+v", playCookie)
	}
	if plain := setCookie(handoff, handlers.PlayCookieName); plain != nil {
		t.Fatal("secure handoff also set a plain-named play cookie")
	}

	// Read path: only the prefixed play cookie authenticates. A valid session
	// under the plain name gets the gate instead of a 404 for a missing world.
	_, playToken, err := in.reg.NewSession(ctx, in.adminID, in.adminLoginID, registry.ScopePlay)
	if err != nil {
		t.Fatalf("play session: %v", err)
	}
	secureRead := serveOnce(
		t, secure.PlayRouter(), http.MethodGet, "/worlds/no-such-world/",
		nil, sessionCookieFor(handlers.SecurePlayCookieName, playToken), "", false,
	)
	if secureRead.Code != http.StatusNotFound {
		t.Fatalf("__Host- play cookie: status = %d, want 404", secureRead.Code)
	}
	plainPlay := serveOnce(
		t, secure.PlayRouter(), http.MethodGet, "/worlds/no-such-world/",
		nil, sessionCookieFor(handlers.PlayCookieName, playToken), "", false,
	)
	if plainPlay.Code != http.StatusOK {
		t.Fatalf("plain play cookie: status = %d, want the gate", plainPlay.Code)
	}
}

func TestLANModeIgnoresTLSOnTheRequest(t *testing.T) {
	in := newInstance(t, instanceOptions{})
	login := serveOnce(
		t, in.app.AppRouter(), http.MethodPost, "/login",
		url.Values{"username": {"admin"}, "password": {acceptanceAdminPassword}},
		nil, "", true,
	)
	if login.Code != http.StatusSeeOther {
		t.Fatalf("login: status = %d, body %s", login.Code, login.Body.String())
	}
	cookie := setCookie(login, handlers.AppCookieName)
	if cookie == nil {
		t.Fatal("LAN login set no plain app cookie")
	}
	if cookie.Secure {
		t.Fatal("a TLS request flipped a LAN app into a Secure cookie")
	}
	if secureName := setCookie(login, handlers.SecureAppCookieName); secureName != nil {
		t.Fatal("a TLS request switched the cookie name")
	}
}

func TestLogoutClearsTheModesCookieName(t *testing.T) {
	in := newInstance(t, instanceOptions{})
	secure := newSecureApp(t, in)
	ctx := context.Background()

	_, _, token, err := in.reg.Login(ctx, "admin", acceptanceAdminPassword)
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	csrf := in.reg.CSRFToken(token)
	rec := serveOnce(
		t, secure.AppRouter(), http.MethodPost, "/logout",
		url.Values{"csrf": {csrf}},
		sessionCookieFor(handlers.SecureAppCookieName, token), "", false,
	)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("logout: status = %d", rec.Code)
	}

	var cleared bool
	for _, line := range rec.Result().Header.Values("Set-Cookie") {
		if strings.HasPrefix(line, handlers.AppCookieName+"=") {
			t.Fatal("logout cleared the plain app cookie in secure mode")
		}
		if strings.HasPrefix(line, handlers.SecureAppCookieName+"=") {
			cleared = true
			if !strings.Contains(line, "Max-Age=0") || !strings.Contains(line, "Secure") {
				t.Fatalf("clear cookie = %q", line)
			}
		}
	}
	if !cleared {
		t.Fatal("logout did not clear the __Host- app cookie")
	}
	if _, _, err := in.reg.Authenticate(ctx, token, registry.ScopeApp); err == nil {
		t.Fatal("logout left the app session valid")
	}
}

// A same-site page can add a second app cookie at a deeper path in LAN mode.
// The duplicate must refuse the write routes instead of reading as "signed
// out" and letting the form mint a session over the victim's cookie.
func TestDuplicateAppCookiesRefuseSignIn(t *testing.T) {
	in := newInstance(t, instanceOptions{})
	ctx := context.Background()

	// The victim is signed in; a second account supplies valid credentials.
	_, _, victimToken, err := in.reg.Login(ctx, "admin", acceptanceAdminPassword)
	if err != nil {
		t.Fatalf("victim login: %v", err)
	}
	_, code, _ := in.reg.CreateInvite(ctx, in.adminID, registry.InviteParams{MaxUses: 1})
	in.redeem(t, code, "attacker")
	_, _, attackerToken, err := in.reg.Login(ctx, "attacker", "correct horse battery")
	if err != nil {
		t.Fatalf("attacker login: %v", err)
	}

	rec := serveOnce(
		t, in.app.AppRouter(), http.MethodPost, "/login",
		url.Values{"username": {"attacker"}, "password": {"correct horse battery"}},
		[]*http.Cookie{
			{Name: handlers.AppCookieName, Value: attackerToken},
			{Name: handlers.AppCookieName, Value: victimToken},
		}, "", false,
	)
	if rec.Code == http.StatusSeeOther {
		t.Fatalf(
			"duplicate app cookies let a login POST through: status = %d, Set-Cookie = %v",
			rec.Code,
			rec.Result().Header.Values("Set-Cookie"),
		)
	}
	if cookies := rec.Result().Cookies(); len(cookies) != 0 {
		t.Fatalf("the refused login still set cookies: %+v", cookies)
	}
	if _, _, err := in.reg.Authenticate(ctx, victimToken, registry.ScopeApp); err != nil {
		t.Fatalf("the bypass replaced or revoked the victim session: %v", err)
	}
}

// The invite routes are the other guarded writes. A duplicate must not spend
// the code or create the account.
func TestDuplicateAppCookiesRefuseInvite(t *testing.T) {
	in := newInstance(t, instanceOptions{})
	ctx := context.Background()

	_, _, victimToken, err := in.reg.Login(ctx, "admin", acceptanceAdminPassword)
	if err != nil {
		t.Fatalf("victim login: %v", err)
	}
	_, attackerCode, _ := in.reg.CreateInvite(ctx, in.adminID, registry.InviteParams{MaxUses: 1})
	in.redeem(t, attackerCode, "attacker")
	_, _, attackerToken, err := in.reg.Login(ctx, "attacker", "correct horse battery")
	if err != nil {
		t.Fatalf("attacker login: %v", err)
	}
	targetInvite, code, err := in.reg.CreateInvite(
		ctx,
		in.adminID,
		registry.InviteParams{MaxUses: 1},
	)
	if err != nil {
		t.Fatalf("create invite: %v", err)
	}
	identitiesBefore, err := in.reg.ListIdentities(ctx, in.adminID)
	if err != nil {
		t.Fatalf("list identities: %v", err)
	}

	duplicates := []*http.Cookie{
		{Name: handlers.AppCookieName, Value: attackerToken},
		{Name: handlers.AppCookieName, Value: victimToken},
	}

	rec := serveOnce(
		t, in.app.AppRouter(), http.MethodPost, "/invite/"+code,
		url.Values{"username": {"newperson"}, "password": {"correct horse battery"}},
		duplicates, "", false,
	)
	if rec.Code == http.StatusSeeOther {
		t.Fatalf("duplicate app cookies let an invite POST through: status = %d", rec.Code)
	}
	if cookies := rec.Result().Cookies(); len(cookies) != 0 {
		t.Fatalf("the refused invite POST set cookies: %+v", cookies)
	}
	invites, err := in.reg.ListInvites(ctx, in.adminID)
	if err != nil {
		t.Fatalf("list invites: %v", err)
	}
	for _, invite := range invites {
		if invite.ID == targetInvite.ID && invite.Uses != 0 {
			t.Fatalf("the refused invite POST spent a use: %+v", invite)
		}
	}
	identitiesAfter, err := in.reg.ListIdentities(ctx, in.adminID)
	if err != nil {
		t.Fatalf("list identities: %v", err)
	}
	if len(identitiesAfter) != len(identitiesBefore) {
		t.Fatalf("the refused invite POST created an identity: %d -> %d",
			len(identitiesBefore), len(identitiesAfter))
	}

	get := serveOnce(
		t, in.app.AppRouter(), http.MethodGet, "/invite/"+code,
		nil, duplicates, "", false,
	)
	if get.Code != http.StatusUnauthorized {
		t.Fatalf("duplicate app cookies on invite GET: status = %d, want 401", get.Code)
	}
}

// An expired cookie is not a shadow. Exactly one that no longer authenticates
// must proceed, or a stale cookie locks the browser out of its own sign-in.
func TestExpiredAppCookieStillProceeds(t *testing.T) {
	clock := time.Now().UTC()
	in := newInstance(t, instanceOptions{clock: func() time.Time { return clock }})
	ctx := context.Background()

	_, _, token, err := in.reg.Login(ctx, "admin", acceptanceAdminPassword)
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	clock = clock.Add(8 * 24 * time.Hour)
	if _, _, err := in.reg.Authenticate(ctx, token, registry.ScopeApp); err == nil {
		t.Fatal("the test app session did not expire")
	}
	_, code, err := in.reg.CreateInvite(ctx, in.adminID, registry.InviteParams{MaxUses: 1})
	if err != nil {
		t.Fatalf("create invite: %v", err)
	}

	expired := sessionCookieFor(handlers.AppCookieName, token)
	login := serveOnce(
		t, in.app.AppRouter(), http.MethodPost, "/login",
		url.Values{"username": {"admin"}, "password": {acceptanceAdminPassword}},
		expired, "", false,
	)
	if login.Code != http.StatusSeeOther {
		t.Fatalf("expired cookie login: status = %d, want 303", login.Code)
	}

	invite := serveOnce(
		t, in.app.AppRouter(), http.MethodGet, "/invite/"+code,
		nil, expired, "", false,
	)
	if invite.Code != http.StatusOK ||
		!strings.Contains(invite.Body.String(), `name="username"`) {
		t.Fatalf("expired cookie invite GET: status = %d, want the invite form", invite.Code)
	}
}

func TestSignedInGuardOnSignInAndInvite(t *testing.T) {
	in := newInstance(t, instanceOptions{})
	secure := newSecureApp(t, in)
	ctx := context.Background()

	_, _, token, err := in.reg.Login(ctx, "admin", acceptanceAdminPassword)
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	csrf := in.reg.CSRFToken(token)
	appCookie := sessionCookieFor(handlers.SecureAppCookieName, token)

	assertSignedInPage := func(t *testing.T, rec *httptest.ResponseRecorder) {
		t.Helper()
		page := rec.Body.String()
		if rec.Code != http.StatusOK {
			t.Fatalf("signed-in guard: status = %d", rec.Code)
		}
		if !strings.Contains(page, `action="/logout"`) ||
			!strings.Contains(page, `name="csrf" value="`+csrf+`"`) {
			t.Fatal("signed-in page has no sign-out form for this session")
		}
		if cookies := rec.Result().Cookies(); len(cookies) != 0 {
			t.Fatalf("the guard changed cookies: %+v", cookies)
		}
	}

	t.Run("signed-in POST login is refused", func(t *testing.T) {
		rec := serveOnce(
			t, secure.AppRouter(), http.MethodPost, "/login",
			url.Values{"username": {"admin"}, "password": {acceptanceAdminPassword}},
			appCookie, "", false,
		)
		assertSignedInPage(t, rec)
	})

	t.Run("signed-out POST login proceeds", func(t *testing.T) {
		rec := serveOnce(
			t, secure.AppRouter(), http.MethodPost, "/login",
			url.Values{"username": {"admin"}, "password": {acceptanceAdminPassword}},
			nil, "", false,
		)
		if rec.Code != http.StatusSeeOther {
			t.Fatalf("status = %d, want 303", rec.Code)
		}
	})

	t.Run("invalid cookie proceeds", func(t *testing.T) {
		rec := serveOnce(
			t, secure.AppRouter(), http.MethodPost, "/login",
			url.Values{"username": {"admin"}, "password": {acceptanceAdminPassword}},
			sessionCookieFor(handlers.SecureAppCookieName, "not-a-session"), "", false,
		)
		if rec.Code != http.StatusSeeOther {
			t.Fatalf("status = %d, want 303", rec.Code)
		}
	})

	t.Run("bearer header does not bypass the guard", func(t *testing.T) {
		_, apiToken, err := in.createAdminAPIToken(ctx)
		if err != nil {
			t.Fatalf("api token: %v", err)
		}
		rec := serveOnce(
			t, secure.AppRouter(), http.MethodPost, "/login",
			url.Values{"username": {"admin"}, "password": {acceptanceAdminPassword}},
			appCookie, apiToken, false,
		)
		assertSignedInPage(t, rec)

		// A bearer client with no cookie is not the guard's business.
		rec = serveOnce(
			t, secure.AppRouter(), http.MethodPost, "/login",
			url.Values{"username": {"admin"}, "password": {acceptanceAdminPassword}},
			nil, apiToken, false,
		)
		if rec.Code != http.StatusSeeOther {
			t.Fatalf("bearer-only login: status = %d, want 303", rec.Code)
		}
	})

	_, code, err := in.reg.CreateInvite(ctx, in.adminID, registry.InviteParams{MaxUses: 1})
	if err != nil {
		t.Fatalf("create invite: %v", err)
	}

	t.Run("signed-in GET invite shows the page", func(t *testing.T) {
		rec := serveOnce(
			t, secure.AppRouter(), http.MethodGet, "/invite/"+code,
			nil, appCookie, "", false,
		)
		assertSignedInPage(t, rec)
	})

	t.Run("signed-in POST invite is refused", func(t *testing.T) {
		rec := serveOnce(
			t, secure.AppRouter(), http.MethodPost, "/invite/"+code,
			url.Values{
				"username": {"newperson"}, "password": {"correct horse battery"},
			},
			appCookie, "", false,
		)
		assertSignedInPage(t, rec)

		invites, err := in.reg.ListInvites(ctx, in.adminID)
		if err != nil {
			t.Fatalf("list invites: %v", err)
		}
		if len(invites) != 1 || invites[0].Uses != 0 {
			t.Fatalf("the guard spent an invite use: %+v", invites)
		}
		identities, err := in.reg.ListIdentities(ctx, in.adminID)
		if err != nil {
			t.Fatalf("list identities: %v", err)
		}
		if len(identities) != 1 {
			t.Fatalf("the guard created an identity: %d accounts", len(identities))
		}
	})

	t.Run("signed-out GET invite shows the form", func(t *testing.T) {
		rec := serveOnce(
			t, secure.AppRouter(), http.MethodGet, "/invite/"+code,
			nil, nil, "", false,
		)
		if rec.Code != http.StatusOK ||
			!strings.Contains(rec.Body.String(), `name="username"`) {
			t.Fatalf("status = %d, want the invite form", rec.Code)
		}
	})
}

// The refusal warnings must name the route, not the path: an invite code is a
// live secret and the path carries it. Both guard branches, GET and POST.
func TestInviteRefusalLogOmitsTheCode(t *testing.T) {
	in := newInstance(t, instanceOptions{})
	ctx := context.Background()

	_, _, victimToken, err := in.reg.Login(ctx, "admin", acceptanceAdminPassword)
	if err != nil {
		t.Fatalf("victim login: %v", err)
	}
	_, attackerCode, _ := in.reg.CreateInvite(
		ctx, in.adminID, registry.InviteParams{MaxUses: 1},
	)
	in.redeem(t, attackerCode, "attacker")
	_, _, attackerToken, err := in.reg.Login(ctx, "attacker", "correct horse battery")
	if err != nil {
		t.Fatalf("attacker login: %v", err)
	}
	_, code, err := in.reg.CreateInvite(ctx, in.adminID, registry.InviteParams{MaxUses: 1})
	if err != nil {
		t.Fatalf("create invite: %v", err)
	}

	duplicates := []*http.Cookie{
		{Name: handlers.AppCookieName, Value: attackerToken},
		{Name: handlers.AppCookieName, Value: victimToken},
	}
	cases := []struct {
		name    string
		method  string
		cookies []*http.Cookie
	}{
		{"signed-in GET", http.MethodGet, sessionCookieFor(handlers.AppCookieName, victimToken)},
		{"signed-in POST", http.MethodPost, sessionCookieFor(handlers.AppCookieName, victimToken)},
		{"duplicate GET", http.MethodGet, duplicates},
		{"duplicate POST", http.MethodPost, duplicates},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var logs bytes.Buffer
			logger := slog.New(slog.NewJSONHandler(&logs, nil))
			app := handlers.NewApp(
				logger,
				in.reg,
				in.worlds,
				func() *webmaster.ChangesetRunner {
					return webmaster.New(openAIForReview(in), "mock-model", 3, "")
				},
				handlers.AppConfig{Config: handlers.Config{
					AppURL:        in.appURL,
					PlayURL:       in.playURL,
					AppSessionTTL: 7 * 24 * time.Hour,
				}},
			)
			t.Cleanup(app.Close)

			serveOnce(
				t, app.AppRouter(), tc.method, "/invite/"+code,
				url.Values{"username": {"newperson"}, "password": {"correct horse battery"}},
				tc.cookies, "", false,
			)

			out := logs.String()
			if strings.Contains(out, code) {
				t.Fatalf("the refusal log contains the invite code: %s", out)
			}
			if !strings.Contains(out, `"route":"/invite/{code}"`) {
				t.Fatalf("the refusal log has no route pattern: %s", out)
			}
			if strings.Contains(out, `"path"`) {
				t.Fatalf("the refusal log still carries a raw path: %s", out)
			}
		})
	}
}
