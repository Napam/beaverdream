package handlers

import (
	"context"
	"net/http"

	"github.com/Napam/beaverdream/lib/registry"
)

// principal is the authenticated identity behind one request. The auth
// middleware writes it into the request context; handlers never read a login
// cookie themselves. A request without one is anonymous.
type principal struct {
	IdentityID  string
	DisplayName string
	LoginID     string
	SessionHash string
	// SessionToken is the raw cookie or bearer token. It derives the CSRF
	// token and is never stored or logged.
	SessionToken string
	Scope        registry.Scope
	Admin        bool
	// API is true when the bearer token authenticated the request. Such a
	// request is not ambient and skips CSRF.
	API bool
	// CSRFOK is true when this request may change state: an API token, or a
	// cookie request whose csrf field verified.
	CSRFOK bool
	// CSRFToken is the value the admin forms render. It is empty for an API
	// request, which has no form.
	CSRFToken string
}

type principalKey struct{}

// withPrincipal returns a request carrying the authenticated identity. Only
// the auth middleware calls it in production; tests use a fixture.
func withPrincipal(r *http.Request, p *principal) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), principalKey{}, p))
}

// principalFrom returns the authenticated identity, or nil.
func principalFrom(r *http.Request) *principal {
	p, _ := r.Context().Value(principalKey{}).(*principal)
	return p
}

// streamSessionValid closes the gap between authentication and subscription.
func (h *Handlers) streamSessionValid(r *http.Request, p *principal) bool {
	if h.reg == nil || p.SessionToken == "" {
		return h.reg == nil
	}
	identity, session, err := h.reg.Authenticate(r.Context(), p.SessionToken, p.Scope)
	if err != nil || identity.ID != p.IdentityID || session.Hash != p.SessionHash {
		return false
	}
	if p.Admin {
		admin, err := h.reg.IsAdmin(r.Context(), p.IdentityID)
		return err == nil && admin
	}
	return true
}
