//nolint:bodyclose // response bodies are closed by body() or in each test.
package handlers_test

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/Napam/beaverdream/cmd/serve/handlers"
	"github.com/Napam/beaverdream/lib/registry"
)

// sessionToken reads one named cookie out of a browser jar.
func sessionToken(t *testing.T, b *browser, rawURL, name string) string {
	t.Helper()
	u, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("parse %s: %v", rawURL, err)
	}
	for _, cookie := range b.jar.Cookies(u) {
		if cookie.Name == name {
			return cookie.Value
		}
	}
	t.Fatalf("%s is not in the jar", name)
	return ""
}

// mintHandoffCode presses enter the real way and returns the code from the
// redirect, without following it.
func mintHandoffCode(t *testing.T, in *instance, b *browser, slug string) string {
	t.Helper()
	csrf := b.csrf(in, "/")
	resp := b.post(in.appURLFor("/worlds/"+slug+"/play"), url.Values{"csrf": {csrf}})
	location := resp.Header.Get("Location")
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("handoff: status = %d, want 303", resp.StatusCode)
	}
	target, err := url.Parse(location)
	if err != nil {
		t.Fatalf("parse handoff location: %v", err)
	}
	code := target.Query().Get("code")
	if code == "" {
		t.Fatalf("handoff location has no code: %q", location)
	}
	return code
}

// A deeper-path cookie shadow sends two cookies of one name, and the browser
// orders the longer path first. Request.Cookie would return the attacker's, so
// every duplicate must fail closed instead. That turns the shadow into a
// refused request: a duplicate at a deeper path is a denial of service, which
// is acceptable, and picking either cookie would be an identity hijack.
func TestDuplicatePlayCookieIsRefused(t *testing.T) {
	in := newInstance(t, instanceOptions{})
	ctx := context.Background()

	admin := in.newBrowser(t)
	admin.login(in, "admin", acceptanceAdminPassword)
	admin.play(in, "heroes")
	adminToken := sessionToken(t, admin, in.playURL, handlers.PlayCookieName)

	_, code, _ := in.reg.CreateInvite(ctx, in.adminID, registry.InviteParams{MaxUses: 1})
	in.redeem(t, code, "intruder")
	intruder := in.newBrowser(t)
	intruder.login(in, "intruder", "correct horse battery")
	intruder.play(in, "heroes")
	intruderToken := sessionToken(t, intruder, in.playURL, handlers.PlayCookieName)

	req, err := http.NewRequest(http.MethodGet, in.playURLFor("/worlds/heroes/"), nil)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	// Attacker's deeper-path cookie first, the admin's Path=/ cookie second.
	req.Header.Set("Cookie",
		handlers.PlayCookieName+"="+intruderToken+"; "+
			handlers.PlayCookieName+"="+adminToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("duplicate play cookie request: %v", err)
	}
	page := body(t, resp)

	if strings.Contains(page, `data-name="intruder"`) {
		t.Fatal("a duplicate play cookie resolved the request to the attacker identity")
	}
	if strings.Contains(page, `data-version="`) {
		t.Fatal("a duplicate play cookie authenticated a play session")
	}
	if !strings.Contains(page, "go to the app") {
		t.Fatalf("a duplicate play cookie did not get the gate page")
	}
}

// The same shadow on the app plane: the attacker's non-admin cookie must not
// be picked, so the request is anonymous instead of resolving to the attacker
// (which used to downgrade the admin panel to a 404).
func TestDuplicateAppCookieIsRefused(t *testing.T) {
	in := newInstance(t, instanceOptions{})
	ctx := context.Background()

	admin := in.newBrowser(t)
	admin.login(in, "admin", acceptanceAdminPassword)
	adminToken := sessionToken(t, admin, in.appURL, handlers.AppCookieName)

	_, code, _ := in.reg.CreateInvite(ctx, in.adminID, registry.InviteParams{MaxUses: 1})
	_, _, intruderToken := in.redeem(t, code, "intruder")

	req, err := http.NewRequest(http.MethodGet, in.appURLFor("/admin"), nil)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	req.Header.Set("Accept", "text/html")
	req.Header.Set("Cookie",
		handlers.AppCookieName+"="+intruderToken+"; "+
			handlers.AppCookieName+"="+adminToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("duplicate app cookie request: %v", err)
	}
	page := body(t, resp)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("duplicate app cookie: status = %d, want the login page", resp.StatusCode)
	}
	if strings.Contains(page, `id="app-admin"`) || strings.Contains(page, `id="admin-body"`) {
		t.Fatal("a duplicate app cookie authenticated an app session")
	}
	if !strings.Contains(page, `name="password"`) {
		t.Fatal("a duplicate app cookie did not get the login page")
	}
}

// A live world page can fetch /handoff with a visitor's own code. With a valid
// play session for another identity already in the browser, the exchange must
// refuse, revoke the session it just minted, and leave the cookie alone.
//
// Trade-off, accepted: a person who switches app accounts and re-enters the
// same world in one browser is refused until they clear the play cookie. That
// is the cost of binding the exchange to the browser's current play identity.
func TestHandoffCannotReplaceAnExistingPlayIdentity(t *testing.T) {
	in := newInstance(t, instanceOptions{})
	ctx := context.Background()

	_, code, _ := in.reg.CreateInvite(ctx, in.adminID, registry.InviteParams{MaxUses: 1})
	intruderIdentity, _, _ := in.redeem(t, code, "intruder")
	intruderID := intruderIdentity.ID
	intruder := in.newBrowser(t)
	intruder.login(in, "intruder", "correct horse battery")
	intruderCode := mintHandoffCode(t, in, intruder, "heroes")

	victim := in.newBrowser(t)
	victim.login(in, "admin", acceptanceAdminPassword)
	victim.play(in, "heroes")
	before := sessionToken(t, victim, in.playURL, handlers.PlayCookieName)

	resp := victim.get(in.playURLFor("/handoff?code=" + intruderCode))
	page := body(t, resp)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("cross-identity handoff: status = %d, want 400", resp.StatusCode)
	}
	if !strings.Contains(page, "go to the app") {
		t.Fatal("cross-identity handoff did not render the gate")
	}
	after := sessionToken(t, victim, in.playURL, handlers.PlayCookieName)
	if after != before {
		t.Fatal("cross-identity handoff replaced the play cookie")
	}

	// The exchange minted a play session for the intruder before it saw the
	// browser's identity, and the refusal must revoke it rather than leave a
	// usable session behind.
	var intruderPlaySessions int
	err := in.reg.DB().QueryRowContext(ctx, `
		SELECT COUNT(*) FROM sessions
		WHERE identity_id = ? AND scope = 'play' AND revoked_at IS NULL`,
		intruderID,
	).Scan(&intruderPlaySessions)
	if err != nil {
		t.Fatalf("count the intruder's play sessions: %v", err)
	}
	if intruderPlaySessions != 0 {
		t.Fatalf("the refused handoff left %d active play session(s) for the intruder",
			intruderPlaySessions)
	}

	// The browser is still the admin on the play plane.
	world := body(t, victim.get(in.playURLFor("/worlds/heroes/")))
	if !strings.Contains(world, `data-name="admin"`) {
		t.Fatal("cross-identity handoff changed the play identity")
	}
}
