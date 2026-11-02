package handlers

import (
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode"
)

// IdentityCookieName stores the name a visitor picked on their first visit.
// There are no accounts: the value is just a display name, and two visitors
// may pick the same one.
const IdentityCookieName = "beaverdream_name"

const identityCookieAge = 365 * 24 * time.Hour

// identityFromRequest returns the cleaned name stored in the visitor's cookie,
// or an empty string when they have not picked one yet.
func identityFromRequest(r *http.Request) string {
	cookie, err := r.Cookie(IdentityCookieName)
	if err != nil {
		return ""
	}
	value, err := url.QueryUnescape(cookie.Value)
	if err != nil {
		value = cookie.Value
	}
	return cleanIdentity(value)
}

// cleanIdentity keeps a display name to letters, digits, spaces and a few
// harmless punctuation marks, collapses runs of whitespace, and caps the
// result at maxAuthorLen runes.
func cleanIdentity(value string) string {
	value = strings.Map(func(r rune) rune {
		switch {
		case unicode.IsLetter(r), unicode.IsDigit(r):
			return r
		case r == ' ', r == '_', r == '-', r == '.', r == '\'':
			return r
		default:
			return -1
		}
	}, value)
	value = strings.Join(strings.Fields(value), " ")
	return truncate(value, maxAuthorLen)
}

// SetIdentity stores the chosen name in a cookie and sends the visitor to the
// board. An empty or unusable name leaves the cookie untouched, so the gate
// asks again.
func (h *Handlers) SetIdentity(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Bad form", http.StatusBadRequest)
		return
	}

	name := cleanIdentity(r.FormValue("name"))
	if name == "" || strings.EqualFold(name, "admin") {
		// Anonymous is the default, and "admin" is reserved for the admin
		// panel's own posts so the model can tell them apart.
		name = "Anonymous"
	}
	http.SetCookie(w, &http.Cookie{
		Name:     IdentityCookieName,
		Value:    url.QueryEscape(name),
		Path:     "/",
		MaxAge:   int(identityCookieAge.Seconds()),
		Expires:  time.Now().Add(identityCookieAge),
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
	http.Redirect(w, r, "/", http.StatusSeeOther)
}
