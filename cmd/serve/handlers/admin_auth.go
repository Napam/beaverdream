package handlers

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Napam/beaverdream/web/pages"
)

// AdminCookieName is the remembered login. A cookie is allowed here because
// the admin lives on its own port, so the board cannot read an admin response
// and never learns the CSRF token below. Cookies ignore ports, so the board
// port does receive this cookie; HttpOnly keeps it out of the model's
// JavaScript, and the CSRF token keeps a forged action from working.
const AdminCookieName = "beaverdream_admin"

// adminCookieAge is how long a login lasts. The cookie carries a signed
// expiry, so there is no session table, and a restart with the same
// ADMIN_TOKEN keeps logins valid.
const adminCookieAge = 30 * 24 * time.Hour

// adminSession is the cookie value for a login that lasts until expiry: the
// expiry in seconds, signed with the admin token.
func (h *Handlers) adminSession(expiry time.Time) string {
	exp := base64.RawURLEncoding.EncodeToString([]byte(strconv.FormatInt(expiry.Unix(), 10)))
	return exp + "." + h.adminMAC(exp)
}

// adminMAC signs one expiry with the admin token. A new token invalidates
// every remembered login, which is the point of a password change.
func (h *Handlers) adminMAC(exp string) string {
	mac := hmac.New(sha256.New, []byte(h.cfg.AdminToken))
	mac.Write([]byte("admin-session:" + exp))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// adminSessionExpiry returns the signed expiry of a valid login cookie. A
// tampered value fails the comparison, not just the parse. Every cookie with
// the name is tried, because a page on the board port can plant one ahead of
// the real login; skipping the bad one keeps that from locking the admin out.
func (h *Handlers) adminSessionExpiry(r *http.Request) (string, bool) {
	for _, cookie := range r.CookiesNamed(AdminCookieName) {
		rawExp, mac, ok := strings.Cut(cookie.Value, ".")
		if !ok || subtle.ConstantTimeCompare([]byte(mac), []byte(h.adminMAC(rawExp))) != 1 {
			continue
		}
		expBytes, err := base64.RawURLEncoding.DecodeString(rawExp)
		if err != nil {
			continue
		}
		exp, err := strconv.ParseInt(string(expBytes), 10, 64)
		if err != nil || time.Now().Unix() >= exp {
			continue
		}
		return rawExp, true
	}
	return "", false
}

// adminSessionOK reports a valid, unexpired login cookie.
func (h *Handlers) adminSessionOK(r *http.Request) bool {
	_, ok := h.adminSessionExpiry(r)
	return ok
}

// adminCSRF is the token an action form carries for one login. It is derived
// from the signed expiry in the cookie, so there is no storage and it dies
// with the login. Only the admin page ever sees it; the board is another
// origin and cannot read that HTML, so it cannot forge an action even though
// the browser attaches the login cookie to its requests.
func (h *Handlers) adminCSRF(rawExp string) string {
	mac := hmac.New(sha256.New, []byte(h.cfg.AdminToken))
	mac.Write([]byte("admin-csrf:" + rawExp))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// csrfFor is the token to render into an action form for this request. It is
// empty without a login cookie, which is why the login answers with a redirect
// instead of the panel: the cookie only exists on the next request.
func (h *Handlers) csrfFor(r *http.Request) string {
	if expiry, ok := h.adminSessionExpiry(r); ok {
		return h.adminCSRF(expiry)
	}
	return ""
}

// adminAccess reports whether a request may see the panel, and whether it may
// change anything. The admin token in a header or form is a secret only a
// script has, so it needs no CSRF token. A login cookie authenticates reads,
// and a POST also needs the token derived from that same login.
func (h *Handlers) adminAccess(r *http.Request) (authed, csrfOK bool) {
	if h.adminTokenOK(r) {
		return true, true
	}
	expiry, ok := h.adminSessionExpiry(r)
	if !ok {
		return false, false
	}
	if r.Method != http.MethodPost {
		return true, true
	}
	given := r.PostFormValue("csrf")
	return true, subtle.ConstantTimeCompare([]byte(given), []byte(h.adminCSRF(expiry))) == 1
}

// adminAuthed reports a request allowed to see the panel: a remembered login,
// or the admin token in a header or form field for scripts and tests.
func (h *Handlers) adminAuthed(r *http.Request) bool {
	return h.adminSessionOK(r) || h.adminTokenOK(r)
}

func (h *Handlers) setAdminSession(w http.ResponseWriter, r *http.Request) {
	expiry := time.Now().Add(adminCookieAge)
	http.SetCookie(w, &http.Cookie{
		Name:     AdminCookieName,
		Value:    h.adminSession(expiry),
		Path:     "/",
		MaxAge:   int(adminCookieAge.Seconds()),
		Expires:  expiry,
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		Secure:   r.TLS != nil,
	})
}

func (h *Handlers) clearAdminSession(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     AdminCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
	})
}

// origin builds a URL on another port of the same host, keeping the path.
func origin(scheme string, host string, port string, path string) string {
	name := host
	if cut, _, err := net.SplitHostPort(host); err == nil {
		name = cut
	}
	return scheme + "://" + net.JoinHostPort(name, port) + path
}

// scheme is http, or https when the request arrived over TLS.
func scheme(r *http.Request) string {
	if r.TLS != nil {
		return "https"
	}
	return "http"
}

// adminURL is where the admin page lives for a request on the board's port:
// the same host, the admin port, and the path the visitor asked for.
func (h *Handlers) adminURL(r *http.Request) string {
	return origin(scheme(r), r.Host, h.cfg.AdminPort, r.URL.RequestURI())
}

// boardURL is the board, seen from the admin port. The header link uses it, so
// it works the same from localhost and from a LAN address.
func (h *Handlers) BoardURL(r *http.Request) string {
	return origin(scheme(r), r.Host, h.cfg.Port, "/")
}

// RedirectAdmin sends an admin path on the board's port to the admin port.
// The admin pages answer only there: serving them on both would put the login
// cookie's origin back on the board.
func (h *Handlers) RedirectAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/admin" || strings.HasPrefix(r.URL.Path, "/admin/") {
			http.Redirect(w, r, h.adminURL(r), http.StatusFound)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// AdminLogout forgets the remembered login. It is a POST like every other
// action, so a link on another page cannot sign the admin out.
func (h *Handlers) AdminLogout(w http.ResponseWriter, r *http.Request) {
	if !h.requireAdmin(w, r) {
		return
	}
	h.clearAdminSession(w)
	h.renderLogin(w, r, pages.Panel{Page: pages.PageOverview}, false)
}
