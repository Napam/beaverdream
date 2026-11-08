package handlers

import (
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/Napam/beaverdream/lib/registry"
	"github.com/Napam/beaverdream/web/pages"
	"github.com/go-chi/chi/v5"
)

// The two cookies never share a name. Cookies ignore ports, so a distinct name
// is what keeps the handoff from overwriting the app session on localhost. In
// secure mode both names carry the __Host- prefix, which a host-only Secure
// cookie needs; the play host can then never write or shadow the app cookie.
const (
	AppCookieName  = "beaverdream_app"
	PlayCookieName = "beaverdream_play"
	// SecureAppCookieName and SecurePlayCookieName are the names used when
	// Config.SecureCookies is set.
	SecureAppCookieName  = "__Host-beaverdream_app"
	SecurePlayCookieName = "__Host-beaverdream_play"
)

// appCookieName is the one app cookie name this process reads and writes.
func (a *App) appCookieName() string {
	if a.cfg.Config.SecureCookies {
		return SecureAppCookieName
	}
	return AppCookieName
}

// playCookieName is the one play cookie name this process reads and writes.
func (a *App) playCookieName() string {
	if a.cfg.Config.SecureCookies {
		return SecurePlayCookieName
	}
	return PlayCookieName
}

// securityHeaders are on every app response. The app may be framed by nobody:
// the world is another origin and must not be able to trick an admin click. It
// also opens no popups, so nothing may keep a window-opener reference to it.
func (a *App) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Content-Security-Policy", "frame-ancestors 'none'")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Cross-Origin-Opener-Policy", "same-origin")
		next.ServeHTTP(w, r)
	})
}

func bearerToken(r *http.Request) string {
	header := strings.TrimSpace(r.Header.Get("Authorization"))
	if len(header) < 7 || !strings.EqualFold(header[:7], "bearer ") {
		return ""
	}
	return strings.TrimSpace(header[7:])
}

// sessionCookie reads the request's only cookie of that name, or "" when the
// request carries none. More than one cookie of the name fails closed as
// anonymous: Request.Cookie returns the first match, and a browser orders a
// deeper-path cookie first, so a page can shadow the real session with its own.
// Picking one would hand over the identity; making both unusable is a denial of
// service at that deeper path, which is the acceptable cost.
func sessionCookie(r *http.Request, name string) string {
	value := ""
	seen := false
	for _, cookie := range r.Cookies() {
		if cookie.Name != name {
			continue
		}
		if seen {
			return ""
		}
		value = cookie.Value
		seen = true
	}
	return value
}

// appCookieCount counts the request's cookies of one name. This process writes
// at most one, at Path=/, so more than one can only be a shadow from a deeper
// path. Callers that must tell "no cookie" from "a shadow" use this;
// sessionCookie itself keeps failing closed to anonymous.
func appCookieCount(r *http.Request, name string) int {
	count := 0
	for _, cookie := range r.Cookies() {
		if cookie.Name == name {
			count++
		}
	}
	return count
}

// authenticateApp reads a bearer token or the app cookie. A bearer header is
// authoritative: a bad token never falls back to the cookie.
func (a *App) authenticateApp(r *http.Request) (*principal, error) {
	if token := bearerToken(r); token != "" {
		identity, session, err := a.reg.Authenticate(r.Context(), token, registry.ScopeAPI)
		if err != nil {
			return nil, err
		}
		return &principal{
			IdentityID:   identity.ID,
			DisplayName:  identity.DisplayName,
			LoginID:      session.LoginID,
			SessionHash:  session.Hash,
			SessionToken: token,
			Scope:        registry.ScopeAPI,
			Admin:        true,
			API:          true,
			CSRFOK:       true,
		}, nil
	}
	return a.authenticateAppCookie(r)
}

// authenticateAppCookie reads only the app cookie and never the bearer header.
// The sign-in and invite routes use it: a browser form cannot attach an
// Authorization header, so a token there must not change the page it gets.
func (a *App) authenticateAppCookie(r *http.Request) (*principal, error) {
	token := sessionCookie(r, a.appCookieName())
	if token == "" {
		return nil, registry.ErrSessionInvalid
	}
	identity, session, err := a.reg.Authenticate(r.Context(), token, registry.ScopeApp)
	if err != nil {
		return nil, err
	}
	return &principal{
		IdentityID:   identity.ID,
		DisplayName:  identity.DisplayName,
		LoginID:      session.LoginID,
		SessionHash:  session.Hash,
		SessionToken: token,
		Scope:        registry.ScopeApp,
		Admin:        isAdmin(r, a.reg, identity.ID),
		CSRFToken:    a.reg.CSRFToken(token),
	}, nil
}

func (a *App) authenticatePlay(r *http.Request) (*principal, error) {
	token := sessionCookie(r, a.playCookieName())
	if token == "" {
		return nil, registry.ErrSessionInvalid
	}
	identity, session, err := a.reg.Authenticate(r.Context(), token, registry.ScopePlay)
	if err != nil {
		return nil, err
	}
	return &principal{
		IdentityID:   identity.ID,
		DisplayName:  identity.DisplayName,
		LoginID:      session.LoginID,
		SessionHash:  session.Hash,
		SessionToken: token,
		Scope:        registry.ScopePlay,
		Admin:        isAdmin(r, a.reg, identity.ID),
	}, nil
}

func isAdmin(r *http.Request, reg *registry.Registry, identityID string) bool {
	admin, err := reg.IsAdmin(r.Context(), identityID)
	return err == nil && admin
}

// requireApp authenticates the app plane. Anonymous GETs get the login page;
// anonymous writes get 401. A cookie POST also verifies its CSRF token here, so
// every app handler can rely on principal.CSRFOK.
func (a *App) requireApp(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, err := a.authenticateApp(r)
		if err != nil {
			if errors.Is(err, registry.ErrSessionInvalid) {
				a.anonymousApp(w, r)
				return
			}
			a.logger.Error("Failed to authenticate an app request", slog.Any("error", err))
			http.Error(w, "Could not sign in", http.StatusInternalServerError)
			return
		}
		if r.Method == http.MethodPost && !p.API {
			_ = r.ParseForm()
			p.CSRFOK = a.reg.VerifyCSRF(p.SessionToken, r.PostFormValue("csrf"))
		}
		next.ServeHTTP(w, withPrincipal(r, p))
	})
}

// requireAppAdmin adds the ADMIN role check. A missing session is the login
// page; a signed-in non-admin is refused and learns nothing.
func (a *App) requireAppAdmin(next http.Handler) http.Handler {
	return a.requireApp(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := principalFrom(r)
		if p == nil || !p.Admin {
			http.NotFound(w, r)
			return
		}
		next.ServeHTTP(w, r)
	}))
}

// requirePlay authenticates a world request. A stream without a session gets
// 204 so Datastar stops retrying; every other path gets the gate page.
func (a *App) requirePlay(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, err := a.authenticatePlay(r)
		if err != nil {
			if strings.HasSuffix(strings.TrimRight(r.URL.Path, "/"), "/live") ||
				r.URL.Path == "/live" {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			// Machine endpoints answer JSON, never a page a fetch would try
			// to parse as data.
			if isMachinePath(r.URL.Path) {
				writeStateError(w, http.StatusUnauthorized, "sign in to play")
				return
			}
			a.gate(w, r)
			return
		}
		next.ServeHTTP(w, withPrincipal(r, p))
	})
}

// isMachinePath reports a play endpoint whose answer is data, so an anonymous
// request is refused in JSON instead of being shown the gate. The endpoint is
// read after the world slug, because a world may itself be named state or
// budget.
func isMachinePath(path string) bool {
	rest, ok := strings.CutPrefix(path, "/worlds/")
	if !ok {
		return false
	}
	_, endpoint, ok := strings.Cut(rest, "/")
	if !ok {
		return false
	}
	segment, _, _ := strings.Cut(endpoint, "/")
	switch segment {
	case "state", "journal", "errors", "budget", "version":
		return true
	}
	return false
}

// anonymousApp shows the login page to a browser and refuses a script.
func (a *App) anonymousApp(w http.ResponseWriter, r *http.Request) {
	if bearerToken(r) != "" || r.Method != http.MethodGet || !wantsHTML(r) {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	next := r.URL.RequestURI()
	h := a.cfg.Config
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_ = pages.AppLogin(next, "", a.loginURL(r), h.DatastarSrc).Render(r.Context(), w)
}

// gate is the play plane's only anonymous page. It points at the app login.
func (a *App) gate(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	login := appOrigin(r, a.cfg.Config) + "/login"
	_ = pages.AppGate(login, "").Render(r.Context(), w)
}

// loginURL is where the app login form posts. It stays on the app origin so
// the app cookie is set there.
func (a *App) loginURL(r *http.Request) string {
	return appOrigin(r, a.cfg.Config) + "/login"
}

// setAppSession writes the app cookie. A zero TTL is a session cookie.
func (a *App) setAppSession(
	w http.ResponseWriter,
	token string,
	ttl time.Duration,
) {
	a.setSessionCookie(w, a.appCookieName(), token, ttl)
}

func (a *App) setPlaySession(
	w http.ResponseWriter,
	token string,
	ttl time.Duration,
) {
	a.setSessionCookie(w, a.playCookieName(), token, ttl)
}

// setSessionCookie writes one host-only cookie. Secure comes from the startup
// mode, never from the request, so a proxy that terminates TLS cannot change
// which name or flag this process writes.
func (a *App) setSessionCookie(
	w http.ResponseWriter,
	name, token string,
	ttl time.Duration,
) {
	cookie := &http.Cookie{
		Name:     name,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   a.cfg.Config.SecureCookies,
	}
	if ttl > 0 {
		cookie.Expires = time.Now().Add(ttl)
		cookie.MaxAge = int(ttl.Seconds())
	}
	http.SetCookie(w, cookie)
}

// clearSessionCookie expires the mode's cookie name. The plain names of the
// other plane are never cleared: secure mode never reads them, and a
// Domain-scoped cookie cannot be cleared host-only anyway.
func (a *App) clearSessionCookie(w http.ResponseWriter, name string) {
	http.SetCookie(w, &http.Cookie{
		Name:     name,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   a.cfg.Config.SecureCookies,
	})
}

// logRoute is the log-safe name of the matched route. chi's pattern keeps its
// placeholders, so a route parameter such as an invite code never reaches the
// log. A request that matched no route gets a fixed label, never its path.
func logRoute(r *http.Request) string {
	if rctx := chi.RouteContext(r.Context()); rctx != nil {
		if pattern := rctx.RoutePattern(); pattern != "" {
			return pattern
		}
	}
	return "unmatched"
}

// requireCSRF refuses a cookie-authenticated app write without a valid token.
// A bearer request already skipped the check in requireApp. The refusal logs
// the route and identity only: never the token, cookie or form.
func (a *App) requireCSRF(w http.ResponseWriter, r *http.Request) bool {
	p := principalFrom(r)
	if p != nil && (p.API || p.CSRFOK) {
		return true
	}
	attrs := []any{slog.String("route", logRoute(r))}
	if p != nil {
		attrs = append(attrs, slog.String("identity_id", p.IdentityID))
	}
	a.logger.Warn("App write refused for a missing token", attrs...)
	http.Error(w, "Bad token", http.StatusForbidden)
	return false
}

// cancelIdentityStreams reaches every loaded world's hub. Disabling an identity
// must close its streams everywhere, not just the world it happens to use.
func (a *App) cancelIdentityStreams(identityID string) {
	a.mu.Lock()
	runtimes := make([]*worldRuntime, 0, len(a.runtimes))
	for _, rt := range a.runtimes {
		runtimes = append(runtimes, rt)
	}
	a.mu.Unlock()
	for _, rt := range runtimes {
		rt.h.hub.cancelIdentity(identityID)
	}
}

func (a *App) cancelSessionStreams(sessionHash string) {
	a.mu.Lock()
	runtimes := make([]*worldRuntime, 0, len(a.runtimes))
	for _, rt := range a.runtimes {
		runtimes = append(runtimes, rt)
	}
	a.mu.Unlock()
	for _, rt := range runtimes {
		rt.h.hub.cancelSession(sessionHash)
	}
}
