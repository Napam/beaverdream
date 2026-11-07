package handlers

import (
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/Napam/beaverdream/lib/registry"
	"github.com/Napam/beaverdream/web/pages"
)

// The two cookies never share a name. Cookies ignore ports, so a distinct name
// is what keeps the handoff from overwriting the app session on localhost.
const (
	AppCookieName  = "beaverdream_app"
	PlayCookieName = "beaverdream_play"
)

// securityHeaders are on every app response. The app may be framed by nobody:
// a board is another origin and must not be able to trick an admin click.
func (a *App) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Content-Security-Policy", "frame-ancestors 'none'")
		w.Header().Set("Referrer-Policy", "no-referrer")
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

func sessionCookie(r *http.Request, name string) string {
	cookie, err := r.Cookie(name)
	if err != nil {
		return ""
	}
	return cookie.Value
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
	token := sessionCookie(r, AppCookieName)
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
	token := sessionCookie(r, PlayCookieName)
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

// requirePlay authenticates a board request. A stream without a session gets
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
	case "state", "journal", "errors", "budget":
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
	_ = pages.AppGate(login).Render(r.Context(), w)
}

// loginURL is where the app login form posts. It stays on the app origin so
// the app cookie is set there.
func (a *App) loginURL(r *http.Request) string {
	return appOrigin(r, a.cfg.Config) + "/login"
}

// setAppSession writes the app cookie. A zero TTL is a session cookie.
func (a *App) setAppSession(
	w http.ResponseWriter,
	r *http.Request,
	token string,
	ttl time.Duration,
) {
	setSessionCookie(w, r, AppCookieName, token, ttl)
}

func (a *App) setPlaySession(
	w http.ResponseWriter,
	r *http.Request,
	token string,
	ttl time.Duration,
) {
	setSessionCookie(w, r, PlayCookieName, token, ttl)
}

func setSessionCookie(
	w http.ResponseWriter,
	r *http.Request,
	name, token string,
	ttl time.Duration,
) {
	cookie := &http.Cookie{
		Name:     name,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   r.TLS != nil,
	}
	if ttl > 0 {
		cookie.Expires = time.Now().Add(ttl)
		cookie.MaxAge = int(ttl.Seconds())
	}
	http.SetCookie(w, cookie)
}

func clearSessionCookie(w http.ResponseWriter, r *http.Request, name string) {
	http.SetCookie(w, &http.Cookie{
		Name:     name,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   r.TLS != nil,
	})
}

// requireCSRF refuses a cookie-authenticated app write without a valid token.
// A bearer request already skipped the check in requireApp.
func (a *App) requireCSRF(w http.ResponseWriter, r *http.Request) bool {
	p := principalFrom(r)
	if p != nil && (p.API || p.CSRFOK) {
		return true
	}
	http.Error(w, "Bad token", http.StatusForbidden)
	return false
}

// cancelIdentityStreams reaches every loaded world's hub. Disabling an identity
// must close its boards everywhere, not just the world it happens to use.
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
