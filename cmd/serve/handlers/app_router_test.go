//nolint:bodyclose // bodies are closed by body() or intentionally streamed.
package handlers_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Napam/beaverdream/cmd/serve/handlers"
	"github.com/Napam/beaverdream/lib/registry"
	"github.com/Napam/beaverdream/lib/webmaster"
	"github.com/Napam/beaverdream/lib/webmaster/skills"
)

func TestInviteGETNeverSpendsAUse(t *testing.T) {
	in := newInstance(t, instanceOptions{})
	_, code, err := in.reg.CreateInvite(context.Background(), in.adminID, registry.InviteParams{
		Label: "friends", MaxUses: 2,
	})
	if err != nil {
		t.Fatalf("create invite: %v", err)
	}
	b := in.newBrowser(t)
	for range 10 {
		resp := b.get(in.appURLFor("/invite/" + code))
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET invite: status = %d", resp.StatusCode)
		}
		_ = resp.Body.Close()
	}
	invites, err := in.reg.ListInvites(context.Background(), in.adminID)
	if err != nil {
		t.Fatalf("list invites: %v", err)
	}
	if len(invites) != 1 || invites[0].Uses != 0 {
		t.Fatalf("GET spent a use: %+v", invites)
	}
	resp := b.post(in.appURLFor("/invite/"+code), url.Values{
		"username": {"alice"}, "password": {"correct horse battery"}, "display_name": {"Alice"},
	})
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("redeem: status = %d, body %s", resp.StatusCode, body(t, resp))
	}
	_ = resp.Body.Close()
	invites, _ = in.reg.ListInvites(context.Background(), in.adminID)
	if invites[0].Uses != 1 {
		t.Fatalf("uses = %d, want 1", invites[0].Uses)
	}
}

func TestInviteConcurrentMaxUses(t *testing.T) {
	in := newInstance(t, instanceOptions{})
	_, code, err := in.reg.CreateInvite(context.Background(), in.adminID, registry.InviteParams{
		Label: "burst", MaxUses: 3,
	})
	if err != nil {
		t.Fatalf("create invite: %v", err)
	}
	var ok atomic.Int32
	var wg sync.WaitGroup
	for i := range 20 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			b := in.newBrowser(t)
			resp := b.post(in.appURLFor("/invite/"+code), url.Values{
				"username": {fmt.Sprintf("user%02d", i)},
				"password": {"correct horse battery"},
			})
			if resp.StatusCode == http.StatusSeeOther {
				ok.Add(1)
			}
			_ = resp.Body.Close()
		}(i)
	}
	wg.Wait()
	if got := ok.Load(); got != 3 {
		t.Fatalf("successes = %d, want exactly 3", got)
	}
	invites, _ := in.reg.ListInvites(context.Background(), in.adminID)
	if invites[0].Uses != 3 {
		t.Fatalf("uses = %d, want 3", invites[0].Uses)
	}
	identities, err := in.reg.ListIdentities(context.Background(), in.adminID)
	if err != nil {
		t.Fatalf("list identities: %v", err)
	}
	// admin plus three accounts.
	if len(identities) != 4 {
		t.Fatalf("identities = %d, want 4", len(identities))
	}
}

func TestInvalidInvitesLookTheSame(t *testing.T) {
	clock := time.Now().UTC()
	in := newInstance(t, instanceOptions{clock: func() time.Time { return clock }})
	ctx := context.Background()
	mk := func(label string, expires *time.Time) string {
		_, code, err := in.reg.CreateInvite(ctx, in.adminID, registry.InviteParams{
			Label: label, MaxUses: 1, ExpiresAt: expires,
		})
		if err != nil {
			t.Fatalf("create %s: %v", label, err)
		}
		return code
	}
	revokedCode := mk("revoked", nil)
	usedCode := mk("used", nil)
	expiredCode := mk("expired", ptr(clock.Add(-time.Minute)))
	invites, _ := in.reg.ListInvites(ctx, in.adminID)
	for _, invite := range invites {
		if invite.Label == "revoked" {
			if err := in.reg.RevokeInvite(ctx, in.adminID, invite.ID); err != nil {
				t.Fatalf("revoke: %v", err)
			}
		}
	}
	if _, _, _, err := in.reg.RedeemInvite(
		ctx,
		usedCode,
		"usedone",
		"correct horse battery",
		"",
	); err != nil {
		t.Fatalf("spend used code: %v", err)
	}
	b := in.newBrowser(t)
	statuses := map[string]int{}
	bodies := map[string]string{}
	for name, code := range map[string]string{
		"unknown": "not-a-real-code", "revoked": revokedCode,
		"used": usedCode, "expired": expiredCode,
	} {
		resp := b.post(in.appURLFor("/invite/"+code), url.Values{
			"username": {"invitee"}, "password": {"correct horse battery"},
		})
		statuses[name] = resp.StatusCode
		bodies[name] = body(t, resp)
	}
	wantStatus := statuses["unknown"]
	wantBody := bodies["unknown"]
	for name := range statuses {
		if statuses[name] != wantStatus || bodies[name] != wantBody {
			t.Errorf("%s differs: status %d body %q", name, statuses[name], bodies[name])
		}
	}
	if wantStatus != http.StatusBadRequest {
		t.Errorf("invalid invite status = %d, want 400", wantStatus)
	}
}

func ptr[T any](value T) *T { return &value }

// A bad form value must say what is wrong and cost no use, or a short password
// reads as a dead invite and the person asks for a new link.
func TestInviteFormSaysWhyItFailedAndKeepsTheUse(t *testing.T) {
	in := newInstance(t, instanceOptions{})
	_, code, err := in.reg.CreateInvite(context.Background(), in.adminID, registry.InviteParams{
		Label: "form", MaxUses: 1,
	})
	if err != nil {
		t.Fatalf("create invite: %v", err)
	}
	b := in.newBrowser(t)
	uses := func() int {
		invites, err := in.reg.ListInvites(context.Background(), in.adminID)
		if err != nil {
			t.Fatalf("list invites: %v", err)
		}
		return invites[0].Uses
	}

	short := body(t, b.post(in.appURLFor("/invite/"+code), url.Values{
		"username": {"mamma"}, "password": {"mamma"}, "display_name": {"Mamma"},
	}))
	if !strings.Contains(short, `value="mamma"`) {
		t.Fatal("the retry form dropped the typed username")
	}
	if got := uses(); got != 0 {
		t.Fatalf("a short password spent %d uses", got)
	}

	badCode := body(t, b.post(in.appURLFor("/invite/not-a-code"), url.Values{
		"username": {"mamma"}, "password": {"correct horse battery"}, "display_name": {"Mamma"},
	}))
	if short == badCode {
		t.Fatal("a short password reads exactly like a dead invite")
	}

	taken := body(t, b.post(in.appURLFor("/invite/"+code), url.Values{
		"username": {"admin"}, "password": {"correct horse battery"},
	}))
	if taken == badCode {
		t.Fatal("a taken username reads exactly like a dead invite")
	}
	if got := uses(); got != 0 {
		t.Fatalf("a taken username spent %d uses", got)
	}

	resp := b.post(in.appURLFor("/invite/"+code), url.Values{
		"username": {"mamma"}, "password": {"correct horse battery"}, "display_name": {"Mamma"},
	})
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("retry: status = %d, body %s", resp.StatusCode, body(t, resp))
	}
	_ = resp.Body.Close()
	if got := uses(); got != 1 {
		t.Fatalf("retry left uses = %d, want 1", got)
	}
}

func TestAppGateScopesAndFrameHeaders(t *testing.T) {
	in := newInstance(t, instanceOptions{})
	b := in.newBrowser(t)

	login := b.get(in.appURLFor("/admin"))
	if login.StatusCode != http.StatusOK || strings.Contains(body(t, login), `id="admin-body"`) {
		t.Fatal("anonymous admin request is not the login page")
	}

	page := b.get(in.playURLFor("/worlds/heroes/"))
	if page.StatusCode != http.StatusOK || strings.Contains(body(t, page), "data-version=") {
		t.Fatal("anonymous request did not get the gate page")
	}
	live := b.get(in.playURLFor("/worlds/heroes/live"))
	if live.StatusCode != http.StatusNoContent {
		t.Fatalf("anonymous live: status = %d, want 204", live.StatusCode)
	}
	_ = live.Body.Close()

	for _, target := range []string{"/login", "/admin", "/invite/nope", "/does-not-exist"} {
		resp := b.get(in.appURLFor(target))
		if got := resp.Header.Get("X-Frame-Options"); got != "DENY" {
			t.Errorf("%s: X-Frame-Options = %q, want DENY", target, got)
		}
		if got := resp.Header.Get("Cross-Origin-Opener-Policy"); got != "same-origin" {
			t.Errorf("%s: Cross-Origin-Opener-Policy = %q, want same-origin", target, got)
		}
		_ = resp.Body.Close()
	}
	if got := page.Header.Get("Cross-Origin-Opener-Policy"); got != "" {
		t.Errorf("the play plane has Cross-Origin-Opener-Policy = %q, want none", got)
	}
}

// next must stay on this origin. A browser turns a backslash into a slash, so
// prefix matching alone let "/\evil.com" send a fresh session off-origin.
func TestLoginNextStaysOnThisOrigin(t *testing.T) {
	in := newInstance(t, instanceOptions{})
	cases := []struct {
		name string
		next string
		want string
	}{
		{"empty", "", "/"},
		{"root", "/", "/"},
		{"local", "/account", "/account"},
		{"local with query", "/worlds/heroes/?x=1", "/worlds/heroes/?x=1"},
		{"absolute", "https://evil.com", "/"},
		{"scheme relative", "//evil.com", "/"},
		{"backslash", `/\evil.com`, "/"},
		{"backslash only", `\evil.com`, "/"},
		{"encoded backslash", "/%5Cevil.com", "/"},
		{"triple slash", "///evil.com", "/"},
		{"tab", "/\tevil.com", "/"},
		{"crlf", "/x\r\nLocation:%20https://evil.com", "/"},
		{"javascript", "javascript:alert(1)", "/"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := in.newBrowser(t)
			resp := b.post(in.appURLFor("/login"), url.Values{
				"username": {"admin"},
				"password": {acceptanceAdminPassword},
				"next":     {tc.next},
			})
			location := resp.Header.Get("Location")
			_ = resp.Body.Close()
			if resp.StatusCode != http.StatusSeeOther {
				t.Fatalf("next %q: status = %d, want 303", tc.next, resp.StatusCode)
			}
			if location != tc.want {
				t.Fatalf("next %q redirected to %q, want %q", tc.next, location, tc.want)
			}
		})
	}

	// The GET render uses the same rule, so its hidden field cannot carry a
	// value the POST would then refuse.
	page := body(t, in.newBrowser(t).get(in.appURLFor("/login?next=%2F%5Cevil.com")))
	if !strings.Contains(page, `name="next" value="/"`) {
		t.Fatal("the login form kept a hostile next value")
	}
}

// A handoff refused because this browser is already another visitor says so;
// the anonymous gate carries no notice.
func TestRefusedHandoffExplainsItself(t *testing.T) {
	in := newInstance(t, instanceOptions{})
	ctx := context.Background()

	_, code, _ := in.reg.CreateInvite(ctx, in.adminID, registry.InviteParams{MaxUses: 1})
	in.redeem(t, code, "intruder")
	intruder := in.newBrowser(t)
	intruder.login(in, "intruder", "correct horse battery")
	intruderCode := mintHandoffCode(t, in, intruder, "heroes")

	victim := in.newBrowser(t)
	victim.login(in, "admin", acceptanceAdminPassword)
	victim.play(in, "heroes")

	login := in.appURL + "/login"
	plain := body(t, in.newBrowser(t).get(in.playURLFor("/")))
	if !strings.Contains(plain, login) {
		t.Fatal("the anonymous gate has no login link")
	}

	resp := victim.get(in.playURLFor("/handoff?code=" + intruderCode))
	refused := body(t, resp)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("cross-identity handoff: status = %d, want 400", resp.StatusCode)
	}
	if refused == plain {
		t.Fatal("the refused handoff reads exactly like the anonymous gate")
	}
	if !strings.Contains(refused, login) {
		t.Fatal("the refused handoff lost the login link")
	}
}

// A world may be named after a machine endpoint. Its page is still a page, so
// an anonymous visit gets the gate, while the endpoint path on any world is
// refused as JSON.
func TestMachinePathsReadTheSegmentAfterTheSlug(t *testing.T) {
	in := newInstance(t, instanceOptions{})
	slugs := []string{"state", "statecraft", "budget", "errors", "journal"}
	for _, slug := range slugs {
		if _, err := in.worlds.Create(context.Background(), in.adminID, slug); err != nil {
			t.Fatalf("create %s: %v", slug, err)
		}
	}

	anonymous := in.newBrowser(t)
	for _, slug := range slugs {
		resp := anonymous.get(in.playURLFor("/worlds/" + slug + "/"))
		page := body(t, resp)
		if resp.StatusCode != http.StatusOK || strings.Contains(page, "data-version=") {
			t.Errorf("%s page: status = %d, want the gate", slug, resp.StatusCode)
		}
	}

	for _, target := range []string{
		"/worlds/heroes/state",
		"/worlds/heroes/journal/moves",
		"/worlds/heroes/budget",
		"/worlds/heroes/version",
	} {
		resp := anonymous.get(in.playURLFor(target))
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s: status = %d, want 401", target, resp.StatusCode)
		}
		_ = resp.Body.Close()
	}
	resp := anonymous.post(in.playURLFor("/worlds/heroes/errors"), nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("anonymous errors post: status = %d, want 401", resp.StatusCode)
	}
	_ = resp.Body.Close()
}

func TestCSRFOnCookieAppWrites(t *testing.T) {
	in := newInstance(t, instanceOptions{})
	admin := in.newBrowser(t)
	admin.login(in, "admin", acceptanceAdminPassword)

	missing := admin.post(in.appURLFor("/admin/worlds"), url.Values{"slug": {"neworld"}})
	if missing.StatusCode != http.StatusForbidden {
		t.Fatalf("world create without csrf: status = %d, want 403", missing.StatusCode)
	}
	_ = missing.Body.Close()

	csrf := admin.csrf(in, "/")
	ok := admin.post(in.appURLFor("/admin/worlds"), url.Values{"slug": {"neworld"}, "csrf": {csrf}})
	if ok.StatusCode != http.StatusSeeOther {
		t.Fatalf("world create with csrf: status = %d, body %s", ok.StatusCode, body(t, ok))
	}
	_ = ok.Body.Close()

	// The handoff is an app write too.
	missing = admin.post(in.appURLFor("/worlds/heroes/play"), nil)
	if missing.StatusCode != http.StatusForbidden {
		t.Fatalf("handoff without csrf: status = %d, want 403", missing.StatusCode)
	}
	_ = missing.Body.Close()
}

func TestBearerOnlyAndScopeSeparation(t *testing.T) {
	in := newInstance(t, instanceOptions{})
	_, token, err := in.reg.CreateAPIToken(context.Background(), in.adminID, in.adminLoginID)
	if err != nil {
		t.Fatalf("mint api token: %v", err)
	}
	do := func(method, target, bearer string) *http.Response {
		req, _ := http.NewRequest(method, target, nil)
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("%s %s: %v", method, target, err)
		}
		return resp
	}

	resp := do(http.MethodGet, in.appURLFor("/admin"), token)
	if resp.StatusCode != http.StatusOK || !strings.Contains(body(t, resp), `id="app-admin"`) {
		t.Fatalf("bearer admin: status = %d", resp.StatusCode)
	}
	resp = do(http.MethodGet, in.playURLFor("/worlds/heroes/live"), token)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("bearer on play live: status = %d, want 204", resp.StatusCode)
	}
	_ = resp.Body.Close()

	// A bearer request is not ambient, so it skips CSRF.
	req, _ := http.NewRequest(http.MethodPost, in.appURLFor("/admin/worlds"),
		strings.NewReader("slug=bearerworld"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("bearer world create: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("bearer world create: status = %d", resp.StatusCode)
	}
	_ = resp.Body.Close()
	if _, err := in.reg.GetWorldBySlug(context.Background(), "bearerworld"); err != nil {
		t.Fatalf("bearer world create did not create the world: %v", err)
	}

	// A query or form token is not a credential.
	resp = do(http.MethodGet, in.appURLFor("/admin?token="+token), "")
	if strings.Contains(body(t, resp), `id="app-admin"`) {
		t.Fatal("a query token opened the admin panel")
	}
	req, _ = http.NewRequest(http.MethodGet, in.appURLFor("/admin"), nil)
	req.Header.Set("X-Admin-Token", token)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("legacy header: %v", err)
	}
	if strings.Contains(body(t, resp), `id="app-admin"`) {
		t.Fatal("X-Admin-Token opened the admin panel")
	}

	if err := in.reg.RevokeAPIToken(context.Background(), token); err != nil {
		t.Fatalf("revoke token: %v", err)
	}
	resp = do(http.MethodGet, in.appURLFor("/admin"), token)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("revoked bearer: status = %d, want 401", resp.StatusCode)
	}
	_ = resp.Body.Close()
}

func TestRoutesStayOnTheirPlane(t *testing.T) {
	in := newInstance(t, instanceOptions{})
	b := in.newBrowser(t)
	admin := in.newBrowser(t)
	admin.login(in, "admin", acceptanceAdminPassword)

	resp := admin.get(in.appURLFor("/worlds/heroes/live"))
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("app serves a play route: status = %d", resp.StatusCode)
	}
	_ = resp.Body.Close()

	// The play plane answers app paths with its gate, never a login form.
	resp = b.get(in.playURLFor("/admin"))
	if page := body(t, resp); strings.Contains(page, `name="password"`) ||
		strings.Contains(page, `id="admin-body"`) {
		t.Error("play plane served an admin page")
	}
	resp = b.get(in.playURLFor("/login"))
	if page := body(t, resp); strings.Contains(page, `name="password"`) {
		t.Error("play plane served the app login form")
	}
}

func TestNonAdminCannotManageWorldsOrInvites(t *testing.T) {
	in := newInstance(t, instanceOptions{})
	_, code, _ := in.reg.CreateInvite(
		context.Background(),
		in.adminID,
		registry.InviteParams{MaxUses: 1},
	)
	in.redeem(t, code, "member")
	b := in.newBrowser(t)
	b.login(in, "member", "correct horse battery")
	csrf := b.csrf(in, "/")

	for _, target := range []string{"/admin/worlds", "/admin/invites"} {
		resp := b.post(in.appURLFor(target), url.Values{
			"csrf": {csrf}, "slug": {"nope"}, "label": {"x"}, "max_uses": {"1"},
		})
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("%s: status = %d, want 404", target, resp.StatusCode)
		}
		_ = resp.Body.Close()
	}
}

func TestBudgetRefusesBeforeStoringAndAdminIsExempt(t *testing.T) {
	in := newInstance(t, instanceOptions{budget: registry.Budget{Identity: 100, Global: 1000}})
	_, code, _ := in.reg.CreateInvite(
		context.Background(),
		in.adminID,
		registry.InviteParams{MaxUses: 1},
	)
	id, _, _ := in.redeem(t, code, "payer")
	if err := in.reg.RecordUsage(context.Background(), id.ID, in.heroesID, 100, 1); err != nil {
		t.Fatalf("seed usage: %v", err)
	}
	b := in.newBrowser(t)
	b.login(in, "payer", "correct horse battery")
	worldURL, _ := b.play(in, "heroes")
	before := countWorldRows(t, in, in.heroesID, "turns")
	resp := b.postWorld(worldURL, url.Values{"body": {"this cannot run"}})
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("over budget: status = %d, want 429", resp.StatusCode)
	}
	_ = resp.Body.Close()
	if after := countWorldRows(t, in, in.heroesID, "turns"); after != before {
		t.Errorf("refused post stored %d turns", after-before)
	}
	if used, _, err := in.reg.UsageOn(
		context.Background(),
		id.ID,
		time.Now().UTC(),
	); err != nil ||
		used != 100 {
		t.Errorf("usage = %d, err = %v, want unchanged 100", used, err)
	}

	adminIn := newInstance(t, instanceOptions{budget: registry.Budget{Identity: 1, Global: 1}})
	admin := adminIn.newBrowser(t)
	admin.login(adminIn, "admin", acceptanceAdminPassword)
	adminWorld, _ := admin.play(adminIn, "heroes")
	resp = admin.postWorld(adminWorld, url.Values{"body": {"mockedit admin is exempt"}})
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("admin post: status = %d, body %s", resp.StatusCode, body(t, resp))
	}
	_ = resp.Body.Close()
	waitFor(t, "the admin turn", func() bool {
		return countWorldRows(t, adminIn, adminIn.heroesID, "turns") > 0
	})
	used, _, err := adminIn.reg.UsageOn(context.Background(), adminIn.adminID, time.Now().UTC())
	if err != nil {
		t.Fatalf("admin usage: %v", err)
	}
	if used != 0 {
		t.Errorf("admin usage = %d, want 0", used)
	}
}

func TestResetKeepsTheRegistryAndOtherWorlds(t *testing.T) {
	in := newInstance(t, instanceOptions{})
	_, code, _ := in.reg.CreateInvite(
		context.Background(),
		in.adminID,
		registry.InviteParams{MaxUses: 1},
	)
	id, _, token := in.redeem(t, code, "player")
	b := in.newBrowser(t)
	b.login(in, "player", "correct horse battery")
	heroesURL, _ := b.play(in, "heroes")
	if resp := b.postWorld(
		heroesURL,
		url.Values{"body": {"mocknoop heroes"}},
	); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("post heroes: status = %d", resp.StatusCode)
	} else {
		_ = resp.Body.Close()
	}
	waitFor(
		t,
		"the heroes turn",
		func() bool { return countWorldRows(t, in, in.heroesID, "changesets") > 0 },
	)
	// The charge is a registry write after the world commit, so wait for it
	// before snapshotting the total the reset must preserve.
	waitFor(t, "the turn's usage", func() bool {
		used, _, err := in.reg.UsageOn(context.Background(), id.ID, time.Now().UTC())
		return err == nil && used > 0
	})
	usedBefore, _, err := in.reg.UsageOn(context.Background(), id.ID, time.Now().UTC())
	if err != nil {
		t.Fatalf("usage: %v", err)
	}

	admin := in.newBrowser(t)
	admin.login(in, "admin", acceptanceAdminPassword)
	csrf := admin.csrf(in, "/")
	resp := admin.post(in.appURLFor("/admin/worlds/heroes/reset"), url.Values{"csrf": {csrf}})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("reset: status = %d, body %s", resp.StatusCode, body(t, resp))
	}
	_ = resp.Body.Close()

	if _, _, err := in.reg.Authenticate(
		context.Background(),
		token,
		registry.ScopeApp,
	); err != nil {
		t.Errorf("app session died with the reset: %v", err)
	}
	if _, err := in.reg.GetIdentity(context.Background(), id.ID); err != nil {
		t.Errorf("identity died with the reset: %v", err)
	}
	if _, err := in.reg.GetWorld(context.Background(), in.atlasID); err != nil {
		t.Errorf("other world died with the reset: %v", err)
	}
	if used, _, _ := in.reg.UsageOn(
		context.Background(),
		id.ID,
		time.Now().UTC(),
	); used != usedBefore {
		t.Errorf("usage = %d after reset, was %d", used, usedBefore)
	}

	// The old play session still opens the other world and can post there.
	atlasURL := in.playURLFor("/worlds/atlas/")
	page := b.get(atlasURL)
	if page.StatusCode != http.StatusOK || !strings.Contains(body(t, page), "data-version=") {
		t.Fatal("the old play session no longer opens another world")
	}
	resp = b.postWorld(in.playURLFor("/worlds/atlas"), url.Values{"body": {"after reset"}})
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("post to atlas: status = %d", resp.StatusCode)
	}
	_ = resp.Body.Close()
}

func TestWorldIsolationAndPageContract(t *testing.T) {
	in := newInstance(t, instanceOptions{})
	_, code, _ := in.reg.CreateInvite(
		context.Background(),
		in.adminID,
		registry.InviteParams{MaxUses: 1},
	)
	in.redeem(t, code, "player")
	b := in.newBrowser(t)
	b.login(in, "player", "correct horse battery")
	worldURL, page := b.play(in, "heroes")
	for _, want := range []string{
		`data-world="` + in.heroesID + `"`,
		`data-world-slug="heroes"`,
		`data-base="/worlds/heroes"`,
		`"base":"/worlds/heroes"`,
		`"store":"bd:` + in.heroesID + `:"`,
		`action="posts"`,
	} {
		if !strings.Contains(page, want) {
			t.Errorf("heroes page is missing %q", want)
		}
	}
	if strings.Contains(page, `action="/posts"`) {
		t.Error("the seed still posts to an absolute root path")
	}
	if !strings.Contains(page, in.appURL+"/account") {
		t.Error("the page's account link does not point at the app")
	}

	// The same play session opens atlas without another handoff.
	other := b.get(in.playURLFor("/worlds/atlas/"))
	if other.StatusCode != http.StatusOK {
		t.Fatalf("atlas with the same play cookie: status = %d", other.StatusCode)
	}
	atlasPage := body(t, other)
	if strings.Contains(atlasPage, `data-world="`+in.heroesID+`"`) {
		t.Error("atlas rendered heroes' world id")
	}
	_ = worldURL
}

func TestHandoffIsSingleUse(t *testing.T) {
	in := newInstance(t, instanceOptions{})
	ctx := context.Background()
	code, _, err := in.reg.CreateHandoff(ctx, in.adminID, in.adminLoginID, in.heroesID)
	if err != nil {
		t.Fatalf("handoff: %v", err)
	}
	b := in.newBrowser(t)
	resp := b.get(in.playURLFor("/handoff?code=" + code))
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("first exchange: status = %d", resp.StatusCode)
	}
	_ = resp.Body.Close()
	second := in.newBrowser(t)
	resp = second.get(in.playURLFor("/handoff?code=" + code))
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("second exchange: status = %d, want 400", resp.StatusCode)
	}
	_ = resp.Body.Close()
}

func TestDisablingAnIdentityClosesStreamsInEveryWorld(t *testing.T) {
	in := newInstance(t, instanceOptions{})
	_, code, _ := in.reg.CreateInvite(
		context.Background(),
		in.adminID,
		registry.InviteParams{MaxUses: 1},
	)
	id, _, _ := in.redeem(t, code, "player")
	player := in.newBrowser(t)
	player.login(in, "player", "correct horse battery")
	player.play(in, "heroes")

	streams := make([]*http.Response, 0, 2)
	for _, slug := range []string{"heroes", "atlas"} {
		resp := player.get(in.playURLFor("/worlds/" + slug + "/live"))
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s live: status = %d", slug, resp.StatusCode)
		}
		streams = append(streams, resp)
	}
	time.Sleep(100 * time.Millisecond)

	admin := in.newBrowser(t)
	admin.login(in, "admin", acceptanceAdminPassword)
	csrf := admin.csrf(in, "/")
	resp := admin.post(
		in.appURLFor("/admin/identities/"+id.ID+"/disable"),
		url.Values{"csrf": {csrf}},
	)
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("disable: status = %d, body %s", resp.StatusCode, body(t, resp))
	}
	_ = resp.Body.Close()

	for i, stream := range streams {
		done := make(chan struct{})
		go func(resp *http.Response) {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
			close(done)
		}(stream)
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Fatalf("stream %d did not close after disable", i)
		}
	}

	// The next play request is refused at the gate, and live asks Datastar to
	// stop retrying.
	page := player.get(in.playURLFor("/worlds/heroes/"))
	if !strings.Contains(body(t, page), "go to the app") {
		t.Error("a disabled identity still gets the page")
	}
	live := player.get(in.playURLFor("/worlds/atlas/live"))
	if live.StatusCode != http.StatusNoContent {
		t.Errorf("disabled live: status = %d, want 204", live.StatusCode)
	}
	_ = live.Body.Close()
}

func countRunningTurns(t *testing.T, in *instance, worldID string) int {
	t.Helper()
	handle := in.worldDB(t, worldID)
	var n int
	if err := handle.DB.QueryRowContext(
		context.Background(),
		`SELECT COUNT(*) FROM turns WHERE kind = 'changeset' AND status = 'running'`,
	).Scan(&n); err != nil {
		t.Fatalf("count running turns: %v", err)
	}
	return n
}

func countWorldRows(t *testing.T, in *instance, worldID, table string) int {
	t.Helper()
	handle := in.worldDB(t, worldID)
	query := `SELECT COUNT(*) FROM turns WHERE kind = 'changeset'`
	if table == "turns" {
		query = `SELECT COUNT(*) FROM turns`
	}
	if table == "site versions" {
		query = `SELECT COUNT(*) FROM site_versions`
	}
	var n int
	if err := handle.DB.QueryRowContext(context.Background(), query).Scan(&n); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return n
}

func TestWorldAdminRoutesArePrefixedAndWorking(t *testing.T) {
	in := newInstance(t, instanceOptions{})
	admin := in.newBrowser(t)
	admin.login(in, "admin", acceptanceAdminPassword)

	resp := admin.get(in.appURLFor("/admin/worlds/heroes/"))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("world admin: status = %d", resp.StatusCode)
	}
	page := body(t, resp)
	for _, want := range []string{
		`id="admin-body"`,
		`/admin/worlds/heroes/changesets`,
		`/admin/worlds/heroes/site`,
		`/admin/worlds/heroes/journal`,
		in.playURL + "/worlds/heroes/",
	} {
		if !strings.Contains(page, want) {
			t.Errorf("world admin page is missing %q", want)
		}
	}
	if strings.Contains(page, `href="/admin/changesets"`) {
		t.Error("world admin page links to the root admin")
	}

	// The renamed routes answer through the router, not only as handlers.
	csrf := admin.csrf(in, "/admin/worlds/heroes/")
	resp = admin.post(in.appURLFor("/admin/worlds/heroes/changeset"), url.Values{
		"csrf":        {csrf},
		"instruction": {"mocknoop keep it"},
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("send instruction: status = %d", resp.StatusCode)
	}
	_ = resp.Body.Close()

	handle := in.worldDB(t, in.heroesID)
	var turnID int64
	if err := handle.DB.QueryRowContext(
		context.Background(),
		`SELECT COALESCE(MAX(id), 0) FROM turns WHERE kind = 'changeset'`,
	).Scan(&turnID); err != nil {
		t.Fatalf("newest changeset: %v", err)
	}
	if turnID == 0 {
		t.Fatal("the instruction turn was not stored")
	}

	resp = admin.get(in.appURLFor("/admin/worlds/heroes/changesets"))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("changeset list: status = %d", resp.StatusCode)
	}
	if list := body(t, resp); !strings.Contains(list, `id="admin-body"`) {
		t.Error("changeset list did not render the admin panel")
	}

	resp = admin.get(in.appURLFor(fmt.Sprintf("/admin/worlds/heroes/changesets/%d", turnID)))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("changeset detail: status = %d", resp.StatusCode)
	}
	if detail := body(t, resp); !strings.Contains(detail, `id="admin-body"`) {
		t.Error("changeset detail did not render the admin panel")
	}

	resp = admin.get(in.appURLFor("/admin/worlds/heroes/turns"))
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("retired turns route: status = %d, want 404", resp.StatusCode)
	}
	_ = resp.Body.Close()

	// A member cannot reach another world's admin.
	_, code, _ := in.reg.CreateInvite(
		context.Background(),
		in.adminID,
		registry.InviteParams{MaxUses: 1},
	)
	in.redeem(t, code, "member")
	member := in.newBrowser(t)
	member.login(in, "member", "correct horse battery")
	resp = member.get(in.appURLFor("/admin/worlds/heroes/"))
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("member world admin: status = %d, want 404", resp.StatusCode)
	}
	_ = resp.Body.Close()
}

// The instance admin is split into sections, and the world pages name their
// world and offer a way back and across.
func TestAppAdminSectionsAndWorldSwitcher(t *testing.T) {
	in := newInstance(t, instanceOptions{})
	admin := in.newBrowser(t)
	admin.login(in, "admin", acceptanceAdminPassword)

	resp := admin.get(in.appURLFor("/admin"))
	page := body(t, resp)
	for _, want := range []string{
		`id="app-admin"`,
		`href="/admin/invites"`,
		`href="/admin/accounts"`,
		`href="/admin/worlds/heroes/"`,
		`action="/admin/worlds"`,
	} {
		if !strings.Contains(page, want) {
			t.Errorf("worlds page is missing %q", want)
		}
	}

	resp = admin.get(in.appURLFor("/admin/invites"))
	page = body(t, resp)
	if !strings.Contains(page, `action="/admin/invites"`) {
		t.Error("invites page is missing its create form")
	}
	if !strings.Contains(page, `aria-current="page"`) {
		t.Error("invites page does not mark the active tab")
	}
	if strings.Contains(page, "new world slug") {
		t.Error("invites page shows the world form")
	}

	resp = admin.get(in.appURLFor("/admin/accounts"))
	page = body(t, resp)
	if !strings.Contains(page, "signed out everywhere") {
		t.Error("accounts page is missing the disable note")
	}
	if strings.Contains(page, `action="/admin/invites"`) {
		t.Error("accounts page shows the invite form")
	}

	// A second world turns the breadcrumb into a switcher.
	if _, err := in.worlds.Create(context.Background(), in.adminID, "second"); err != nil {
		t.Fatalf("create second world: %v", err)
	}
	resp = admin.get(in.appURLFor("/admin/worlds/heroes/"))
	page = body(t, resp)
	for _, want := range []string{
		`href="/admin"`,
		`id="admin-world-switcher"`,
		`value="/admin/worlds/heroes/"`,
		`value="/admin/worlds/second/"`,
	} {
		if !strings.Contains(page, want) {
			t.Errorf("world admin page is missing %q", want)
		}
	}
}

func TestAdminModelListsEverySkillAndTool(t *testing.T) {
	in := newInstance(t, instanceOptions{})
	admin := in.newBrowser(t)
	admin.login(in, "admin", acceptanceAdminPassword)

	page := body(t, admin.get(in.appURLFor("/admin/model")))
	for _, skill := range skills.All {
		if !strings.Contains(page, `data-skill="`+skill.Name+`"`) {
			t.Errorf("model page is missing skill %q", skill.Name)
		}
	}
	registered := webmaster.Tools()
	if len(registered) == 0 {
		t.Fatal("no tools are registered")
	}
	for _, tool := range registered {
		if !strings.Contains(page, `data-tool="`+tool.Name+`"`) {
			t.Errorf("model page is missing tool %q", tool.Name)
		}
	}
	if !strings.Contains(page, `href="/admin/model"`) ||
		!strings.Contains(page, `aria-current="page"`) {
		t.Error("model page does not mark its tab")
	}

	anon := in.newBrowser(t)
	refused := body(t, anon.get(in.appURLFor("/admin/model")))
	if strings.Contains(refused, `data-skill=`) {
		t.Error("an anonymous request read the model page")
	}
}

func TestUserPagesShowTheBackOfficeDoorwayOnlyToAdmins(t *testing.T) {
	in := newInstance(t, instanceOptions{})
	admin := in.newBrowser(t)
	admin.login(in, "admin", acceptanceAdminPassword)
	page := body(t, admin.get(in.appURLFor("/")))
	if !strings.Contains(page, `href="/admin"`) {
		t.Error("the lobby has no way into the back office for an admin")
	}
	if strings.Contains(page, `aria-current="page"`) {
		t.Error("the user lobby marks a tab as current")
	}
	if !strings.Contains(page, `href="/admin/worlds/heroes/"`) {
		t.Error("the admin's lobby row has no manage link")
	}

	_, code, err := in.reg.CreateInvite(
		context.Background(),
		in.adminID,
		registry.InviteParams{MaxUses: 1},
	)
	if err != nil {
		t.Fatalf("create invite: %v", err)
	}
	player := in.newBrowser(t)
	resp := player.post(in.appURLFor("/invite/"+code), url.Values{
		"username": {"player"}, "password": {"correct horse battery"},
	})
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("redeem: status = %d", resp.StatusCode)
	}
	_ = resp.Body.Close()
	page = body(t, player.get(in.appURLFor("/")))
	if strings.Contains(page, `href="/admin`) {
		t.Error("a non-admin lobby shows the back office")
	}
	if strings.Contains(page, ">manage<") {
		t.Error("a non-admin lobby shows a manage button")
	}
}

func TestDeleteWorldRemovesOnlyThatWorld(t *testing.T) {
	in := newInstance(t, instanceOptions{budget: registry.Budget{}})
	admin := in.newBrowser(t)
	admin.login(in, "admin", acceptanceAdminPassword)
	csrf := admin.csrf(in, "/")
	resp := admin.post(in.appURLFor("/admin/worlds"), url.Values{
		"slug": {"tempworld"}, "csrf": {csrf},
	})
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("create: status = %d, body %s", resp.StatusCode, body(t, resp))
	}
	_ = resp.Body.Close()
	world, err := in.reg.GetWorldBySlug(context.Background(), "tempworld")
	if err != nil {
		t.Fatalf("read created world: %v", err)
	}
	file := in.worlds.Path(world.ID)
	if _, err := os.Stat(file); err != nil {
		t.Fatalf("world file missing: %v", err)
	}

	resp = admin.post(
		in.appURLFor("/admin/worlds/"+world.Slug+"/delete"),
		url.Values{"csrf": {csrf}},
	)
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("delete: status = %d, body %s", resp.StatusCode, body(t, resp))
	}
	_ = resp.Body.Close()
	if _, err := in.reg.GetWorld(context.Background(), world.ID); err == nil {
		t.Error("world row survived delete")
	}
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Errorf("world file survived delete: %v", err)
	}
	if _, err := in.reg.GetWorld(context.Background(), in.heroesID); err != nil {
		t.Errorf("delete removed another world: %v", err)
	}
	// The play route no longer opens it and does not recreate the file.
	member := in.newBrowser(t)
	_, code, _ := in.reg.CreateInvite(
		context.Background(),
		in.adminID,
		registry.InviteParams{MaxUses: 1},
	)
	in.redeem(t, code, "member")
	member.login(in, "member", "correct horse battery")
	member.play(in, "heroes")
	resp = member.get(in.playURLFor("/worlds/tempworld/"))
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("deleted world play: status = %d, want 404", resp.StatusCode)
	}
	_ = resp.Body.Close()
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Errorf("a request recreated the deleted world file")
	}
}

func TestDisplayNameIsOnTheIdentityAndPostAuthorHasStableID(t *testing.T) {
	in := newInstance(t, instanceOptions{})
	_, code, _ := in.reg.CreateInvite(
		context.Background(),
		in.adminID,
		registry.InviteParams{MaxUses: 2},
	)
	id, _, _ := in.redeem(t, code, "player")
	one := in.newBrowser(t)
	one.login(in, "player", "correct horse battery")
	worldURL, _ := one.play(in, "heroes")
	two := in.newBrowser(t)
	two.login(in, "player", "correct horse battery")
	two.play(in, "heroes")

	resp := one.postWorld(worldURL, url.Values{"body": {"hello from one"}})
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("post: status = %d", resp.StatusCode)
	}
	_ = resp.Body.Close()
	waitFor(t, "the post", func() bool {
		return countWorldRows(t, in, in.heroesID, "turns") > 0
	})
	page := body(t, two.get(in.playURLFor("/worlds/heroes/")))
	if !strings.Contains(page, "player") ||
		!strings.Contains(page, `data-author="`+id.ID+`"`) ||
		!strings.Contains(page, `data-author-id="`+id.ID+`"`) {
		t.Errorf("the other browser does not show the identity's name and id")
	}

	// A rename is visible in the other browser on the next render. No name
	// cookie exists to go stale.
	csrf := one.csrf(in, "/account")
	resp = one.post(in.appURLFor("/account/name"), url.Values{
		"display_name": {"Renamed"}, "csrf": {csrf},
	})
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("rename: status = %d", resp.StatusCode)
	}
	_ = resp.Body.Close()
	page = body(t, two.get(in.playURLFor("/worlds/heroes/")))
	if !strings.Contains(page, "Renamed") {
		t.Error("the other browser did not see the new display name")
	}
}

func TestInvitedCredentialsWorkFromANewBrowser(t *testing.T) {
	in := newInstance(t, instanceOptions{})
	_, code, _ := in.reg.CreateInvite(
		context.Background(),
		in.adminID,
		registry.InviteParams{MaxUses: 1},
	)
	id, _, _ := in.redeem(t, code, "alice")
	one := in.newBrowser(t)
	one.login(in, "alice", "correct horse battery")
	two := in.newBrowser(t)
	two.login(in, "alice", "correct horse battery")
	for i, b := range []*browser{one, two} {
		worldURL, _ := b.play(in, "heroes")
		if worldURL == "" {
			t.Fatalf("browser %d has no page", i)
		}
	}
	if _, err := in.reg.GetIdentity(context.Background(), id.ID); err != nil {
		t.Fatalf("identity: %v", err)
	}
}

func TestIdleWorldUnloadsAndReopens(t *testing.T) {
	in := newInstance(t, instanceOptions{idleTime: 80 * time.Millisecond})
	admin := in.newBrowser(t)
	admin.login(in, "admin", acceptanceAdminPassword)
	admin.play(in, "heroes")
	waitFor(t, "the world to load", func() bool { return in.app.LoadedForTest() > 0 })
	waitFor(t, "the idle unload", func() bool { return in.app.LoadedForTest() == 0 })
	page := admin.get(in.playURLFor("/worlds/heroes/"))
	if page.StatusCode != http.StatusOK || !strings.Contains(body(t, page), "data-version=") {
		t.Fatal("a reloaded world does not serve its page")
	}
}

func TestCookieContractAndScopeSeparation(t *testing.T) {
	in := newInstance(t, instanceOptions{})
	admin := in.newBrowser(t)
	loginResp := admin.post(in.appURLFor("/login"), url.Values{
		"username": {"admin"}, "password": {acceptanceAdminPassword}, "next": {"/"},
	})
	if loginResp.StatusCode != http.StatusSeeOther {
		t.Fatalf("login: status = %d", loginResp.StatusCode)
	}
	appSetCookie := loginResp.Header.Get("Set-Cookie")
	_ = loginResp.Body.Close()
	if !strings.Contains(appSetCookie, handlers.AppCookieName+"=") ||
		!strings.Contains(appSetCookie, "HttpOnly") ||
		!strings.Contains(appSetCookie, "SameSite=Lax") ||
		strings.Contains(appSetCookie, "Domain=") ||
		!strings.Contains(appSetCookie, "Expires=") {
		t.Fatalf("app Set-Cookie = %q", appSetCookie)
	}

	appURL, _ := url.Parse(in.appURL)
	playURL, _ := url.Parse(in.playURL)
	var appCookie, playCookie *http.Cookie
	for _, cookie := range admin.jar.Cookies(appURL) {
		if cookie.Name == handlers.AppCookieName {
			appCookie = cookie
		}
	}
	if appCookie == nil {
		t.Fatal("login set no app cookie")
	}
	_, appSession, err := in.reg.Authenticate(
		context.Background(),
		appCookie.Value,
		registry.ScopeApp,
	)
	if err != nil {
		t.Fatalf("app session: %v", err)
	}
	if appSession.ExpiresAt == nil || time.Until(*appSession.ExpiresAt) < 6*24*time.Hour {
		t.Errorf("app session expiry = %v, want about 7 days", appSession.ExpiresAt)
	}

	// Press enter manually, so the handoff's Set-Cookie is visible.
	csrf := admin.csrf(in, "/")
	playResp := admin.post(in.appURLFor("/worlds/heroes/play"), url.Values{"csrf": {csrf}})
	target := playResp.Header.Get("Location")
	_ = playResp.Body.Close()
	handoff := admin.get(target)
	playSetCookie := handoff.Header.Get("Set-Cookie")
	_ = handoff.Body.Close()
	if !strings.Contains(playSetCookie, handlers.PlayCookieName+"=") ||
		!strings.Contains(playSetCookie, "HttpOnly") ||
		!strings.Contains(playSetCookie, "SameSite=Lax") ||
		strings.Contains(playSetCookie, "Domain=") ||
		strings.Contains(playSetCookie, "Expires=") {
		t.Fatalf("play Set-Cookie = %q", playSetCookie)
	}
	for _, cookie := range admin.jar.Cookies(playURL) {
		if cookie.Name == handlers.PlayCookieName {
			playCookie = cookie
		}
	}
	if playCookie == nil {
		t.Fatal("handoff set no play cookie")
	}
	_, playSession, err := in.reg.Authenticate(
		context.Background(),
		playCookie.Value,
		registry.ScopePlay,
	)
	if err != nil {
		t.Fatalf("play session: %v", err)
	}
	if playSession.ExpiresAt != nil {
		t.Errorf("play session expiry = %v, want none", playSession.ExpiresAt)
	}

	// Each cookie is refused on the other plane even though the browser may
	// send both to both ports.
	appOnly := in.newBrowser(t)
	appOnly.jar.SetCookies(
		playURL,
		[]*http.Cookie{{Name: handlers.AppCookieName, Value: appCookie.Value}},
	)
	resp := appOnly.get(in.playURLFor("/worlds/heroes/"))
	if strings.Contains(body(t, resp), "data-version=") {
		t.Error("the app cookie opened a world")
	}

	playOnly := in.newBrowser(t)
	playOnly.jar.SetCookies(
		appURL,
		[]*http.Cookie{{Name: handlers.PlayCookieName, Value: playCookie.Value}},
	)
	resp = playOnly.get(in.appURLFor("/account"))
	if strings.Contains(body(t, resp), "sign out everywhere") {
		t.Error("the play cookie opened the account page")
	}
}

func TestBootstrapRotationRevokesEverySession(t *testing.T) {
	in := newInstance(t, instanceOptions{})
	admin := in.newBrowser(t)
	admin.login(in, "admin", acceptanceAdminPassword)
	admin.play(in, "heroes")
	_, token, err := in.reg.CreateAPIToken(context.Background(), in.adminID, in.adminLoginID)
	if err != nil {
		t.Fatalf("api token: %v", err)
	}

	if _, err := in.reg.EnsureBootstrapAdmin(context.Background(), "a new password"); err != nil {
		t.Fatalf("rotate: %v", err)
	}
	if _, _, err := in.reg.Authenticate(
		context.Background(),
		token,
		registry.ScopeAPI,
	); err == nil {
		t.Error("the old api token survived rotation")
	}
	resp := admin.get(in.appURLFor("/account"))
	if strings.Contains(body(t, resp), "sign out everywhere") {
		t.Error("the old app session survived rotation")
	}
	resp = admin.get(in.playURLFor("/worlds/heroes/"))
	if strings.Contains(body(t, resp), "data-version=") {
		t.Error("the old play session survived rotation")
	}
	fresh := in.newBrowser(t)
	fresh.login(in, "admin", "a new password")
}

func TestLogoutEverywhereClosesEveryStream(t *testing.T) {
	in := newInstance(t, instanceOptions{})
	_, code, _ := in.reg.CreateInvite(
		context.Background(),
		in.adminID,
		registry.InviteParams{MaxUses: 1},
	)
	in.redeem(t, code, "player")
	one := in.newBrowser(t)
	one.login(in, "player", "correct horse battery")
	one.play(in, "heroes")
	two := in.newBrowser(t)
	two.login(in, "player", "correct horse battery")
	two.play(in, "heroes")

	streams := []*http.Response{
		one.get(in.playURLFor("/worlds/heroes/live")),
		two.get(in.playURLFor("/worlds/heroes/live")),
	}
	time.Sleep(100 * time.Millisecond)

	csrf := one.csrf(in, "/account")
	resp := one.post(in.appURLFor("/account/logout-all"), url.Values{"csrf": {csrf}})
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("logout all: status = %d", resp.StatusCode)
	}
	_ = resp.Body.Close()
	for i, stream := range streams {
		done := make(chan struct{})
		go func(resp *http.Response) {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
			close(done)
		}(stream)
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Fatalf("stream %d stayed open after log out everywhere", i)
		}
	}
	for i, b := range []*browser{one, two} {
		if strings.Contains(body(t, b.get(in.playURLFor("/worlds/heroes/"))), "data-version=") {
			t.Errorf("browser %d still plays after log out everywhere", i)
		}
	}
}

func TestDeleteWorldClosesItsStreamsAndLeavesOthersAlone(t *testing.T) {
	in := newInstance(t, instanceOptions{})
	_, code, _ := in.reg.CreateInvite(
		context.Background(),
		in.adminID,
		registry.InviteParams{MaxUses: 1},
	)
	in.redeem(t, code, "player")
	player := in.newBrowser(t)
	player.login(in, "player", "correct horse battery")
	player.play(in, "heroes")
	heroesStream := player.get(in.playURLFor("/worlds/heroes/live"))
	atlasStream := player.get(in.playURLFor("/worlds/atlas/live"))
	if heroesStream.StatusCode != http.StatusOK || atlasStream.StatusCode != http.StatusOK {
		t.Fatalf("streams: heroes %d atlas %d", heroesStream.StatusCode, atlasStream.StatusCode)
	}
	time.Sleep(100 * time.Millisecond)

	admin := in.newBrowser(t)
	admin.login(in, "admin", acceptanceAdminPassword)
	csrf := admin.csrf(in, "/")
	resp := admin.post(
		in.appURLFor("/admin/worlds/heroes/delete"),
		url.Values{"csrf": {csrf}},
	)
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("delete: status = %d, body %s", resp.StatusCode, body(t, resp))
	}
	_ = resp.Body.Close()

	done := make(chan struct{})
	go func() {
		_, _ = io.Copy(io.Discard, heroesStream.Body)
		_ = heroesStream.Body.Close()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("the deleted world's stream stayed open")
	}
	// The other world's stream is untouched.
	go func() {
		time.Sleep(150 * time.Millisecond)
		_ = atlasStream.Body.Close()
	}()
	if _, err := in.reg.GetWorld(context.Background(), in.heroesID); err == nil {
		t.Error("deleted world row survived")
	}
	if _, err := in.reg.GetWorld(context.Background(), in.atlasID); err != nil {
		t.Error("delete removed the other world")
	}
	resp = player.get(in.playURLFor("/worlds/atlas/"))
	if resp.StatusCode != http.StatusOK || !strings.Contains(body(t, resp), "data-version=") {
		t.Error("the other world stopped working after a delete")
	}
}

func TestBudgetStatusRouteAndGlobalLimit(t *testing.T) {
	in := newInstance(t, instanceOptions{budget: registry.Budget{Identity: 1000, Global: 150}})
	_, code, _ := in.reg.CreateInvite(
		context.Background(),
		in.adminID,
		registry.InviteParams{MaxUses: 1},
	)
	first, _, _ := in.redeem(t, code, "first")
	_, code, _ = in.reg.CreateInvite(
		context.Background(),
		in.adminID,
		registry.InviteParams{MaxUses: 1},
	)
	second, _, _ := in.redeem(t, code, "second")
	if err := in.reg.RecordUsage(context.Background(), first.ID, in.heroesID, 100, 1); err != nil {
		t.Fatalf("usage first: %v", err)
	}
	if err := in.reg.RecordUsage(context.Background(), second.ID, in.atlasID, 50, 1); err != nil {
		t.Fatalf("usage second: %v", err)
	}

	one := in.newBrowser(t)
	one.login(in, "first", "correct horse battery")
	worldURL, _ := one.play(in, "heroes")
	resp := one.get(in.playURLFor("/worlds/heroes/budget"))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("budget: status = %d", resp.StatusCode)
	}
	bodyText := body(t, resp)
	if !strings.Contains(bodyText, `"used":100`) || !strings.Contains(bodyText, `"limit":1000`) {
		t.Errorf("budget = %s, want this identity's numbers", bodyText)
	}

	// The global total is already at its limit, so a different identity with
	// room of its own is still refused.
	before := countWorldRows(t, in, in.heroesID, "turns")
	resp = one.postWorld(worldURL, url.Values{"body": {"global is full"}})
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("global refusal: status = %d, want 429", resp.StatusCode)
	}
	_ = resp.Body.Close()
	if after := countWorldRows(t, in, in.heroesID, "turns"); after != before {
		t.Error("a global refusal stored a turn")
	}

	anonymous := in.newBrowser(t)
	resp = anonymous.get(in.playURLFor("/worlds/heroes/budget"))
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("anonymous budget: status = %d, want 401", resp.StatusCode)
	}
	_ = resp.Body.Close()
}

func TestInviteLinkIsShownOnceAndWorldSlugsAreUnique(t *testing.T) {
	in := newInstance(t, instanceOptions{})
	admin := in.newBrowser(t)
	admin.login(in, "admin", acceptanceAdminPassword)
	csrf := admin.csrf(in, "/")

	resp := admin.post(in.appURLFor("/admin/invites"), url.Values{
		"label": {"one"}, "max_uses": {"1"}, "csrf": {csrf},
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("create invite: status = %d", resp.StatusCode)
	}
	page := body(t, resp)
	marker := in.appURL + "/invite/"
	at := strings.Index(page, marker)
	if at < 0 {
		t.Fatal("the create response does not show the invite link")
	}
	code := page[at+len(marker):]
	if end := strings.IndexAny(code, `"< `); end >= 0 {
		code = code[:end]
	}
	if len(code) < 32 {
		t.Fatalf("invite code = %q", code)
	}
	if strings.Contains(body(t, admin.get(in.appURLFor("/admin"))), code) {
		t.Error("the dashboard shows the raw invite code again")
	}

	first := admin.post(in.appURLFor("/admin/worlds"), url.Values{"slug": {"same"}, "csrf": {csrf}})
	if first.StatusCode != http.StatusSeeOther {
		t.Fatalf("create world: status = %d", first.StatusCode)
	}
	_ = first.Body.Close()
	second := admin.post(
		in.appURLFor("/admin/worlds"),
		url.Values{"slug": {"same"}, "csrf": {csrf}},
	)
	if second.StatusCode == http.StatusSeeOther {
		t.Fatal("a duplicate slug was accepted")
	}
	_ = second.Body.Close()
	if _, err := in.reg.GetWorldBySlug(context.Background(), "same"); err != nil {
		t.Fatalf("the first world is gone: %v", err)
	}
	invalid := admin.post(
		in.appURLFor("/admin/worlds"),
		url.Values{"slug": {"../bad"}, "csrf": {csrf}},
	)
	if invalid.StatusCode == http.StatusSeeOther {
		t.Error("an invalid slug was accepted")
	}
	_ = invalid.Body.Close()
}

func TestAdminMintsAnAPITokenThroughTheAccountRoute(t *testing.T) {
	in := newInstance(t, instanceOptions{})
	admin := in.newBrowser(t)
	admin.login(in, "admin", acceptanceAdminPassword)
	csrf := admin.csrf(in, "/account")
	resp := admin.post(in.appURLFor("/account/tokens"), url.Values{"csrf": {csrf}})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("mint: status = %d", resp.StatusCode)
	}
	page := body(t, resp)
	// The token sits in the one-time flash field.
	const start = `readonly value="`
	at := strings.Index(page, start)
	if at < 0 {
		t.Fatal("the response does not show the token")
	}
	rest := page[at+len(start):]
	end := strings.Index(rest, `"`)
	token := rest[:end]
	if len(token) < 64 {
		t.Fatalf("token = %q", token)
	}
	req, _ := http.NewRequest(http.MethodGet, in.appURLFor("/admin"), nil)
	req.Header.Set("Authorization", "Bearer "+token)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("bearer: %v", err)
	}
	if page := body(t, res); !strings.Contains(page, `id="app-admin"`) {
		t.Error("the minted token does not authenticate")
	}
}

func TestIdleUnloadWaitsForAnOpenStream(t *testing.T) {
	in := newInstance(t, instanceOptions{idleTime: 80 * time.Millisecond})
	_, code, _ := in.reg.CreateInvite(
		context.Background(),
		in.adminID,
		registry.InviteParams{MaxUses: 1},
	)
	in.redeem(t, code, "player")
	player := in.newBrowser(t)
	player.login(in, "player", "correct horse battery")
	player.play(in, "heroes")
	stream := player.get(in.playURLFor("/worlds/heroes/live"))
	if stream.StatusCode != http.StatusOK {
		t.Fatalf("live: status = %d", stream.StatusCode)
	}
	time.Sleep(300 * time.Millisecond)
	if got := in.app.LoadedForTest(); got == 0 {
		t.Fatal("a world with an open stream was unloaded")
	}
	_ = stream.Body.Close()
	waitFor(t, "the world to unload", func() bool { return in.app.LoadedForTest() == 0 })
}

func TestModelConcurrencyIsCappedAcrossWorlds(t *testing.T) {
	var active atomic.Int32
	var peak atomic.Int32
	in := newInstance(t, instanceOptions{model: func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			now := active.Add(1)
			for {
				old := peak.Load()
				if now <= old || peak.CompareAndSwap(old, now) {
					break
				}
			}
			time.Sleep(80 * time.Millisecond)
			defer active.Add(-1)
			next.ServeHTTP(w, r)
		})
	}})
	admin := in.newBrowser(t)
	admin.login(in, "admin", acceptanceAdminPassword)
	heroesURL, _ := admin.play(in, "heroes")
	atlasURL := in.playURLFor("/worlds/atlas/")
	if page := admin.get(atlasURL); !strings.Contains(body(t, page), "data-version=") {
		t.Fatal("atlas did not open with the same play session")
	}

	var wg sync.WaitGroup
	for _, target := range []string{heroesURL, in.playURLFor("/worlds/atlas")} {
		wg.Add(1)
		go func(target string) {
			defer wg.Done()
			resp := admin.postWorld(target, url.Values{"body": {"mocknoop concurrency"}})
			if resp.StatusCode != http.StatusSeeOther {
				t.Errorf("post: status = %d", resp.StatusCode)
			}
			_ = resp.Body.Close()
		}(strings.TrimSuffix(target, "/"))
	}
	wg.Wait()
	waitFor(t, "both turns", func() bool {
		return countRunningTurns(t, in, in.heroesID) == 0 &&
			countWorldRows(t, in, in.heroesID, "turns") > 0 &&
			countRunningTurns(t, in, in.atlasID) == 0 &&
			countWorldRows(t, in, in.atlasID, "turns") > 0
	})
	if got := peak.Load(); got > 1 {
		t.Errorf("model peak concurrency = %d, want at most 1", got)
	}
}

// A browser that is already signed in cannot redeem an invite: the guard shows
// the sign-out page, the old session stays, and the code is still spendable by
// a browser with no session.
func TestRedeemingAnInviteWhileSignedInIsRefused(t *testing.T) {
	in := newInstance(t, instanceOptions{})
	admin := in.newBrowser(t)
	admin.login(in, "admin", acceptanceAdminPassword)
	appURL, _ := url.Parse(in.appURL)
	var oldToken string
	for _, cookie := range admin.jar.Cookies(appURL) {
		if cookie.Name == handlers.AppCookieName {
			oldToken = cookie.Value
		}
	}
	_, code, _ := in.reg.CreateInvite(
		context.Background(),
		in.adminID,
		registry.InviteParams{MaxUses: 1},
	)
	resp := admin.post(in.appURLFor("/invite/"+code), url.Values{
		"username": {"newperson"}, "password": {"correct horse battery"},
	})
	if resp.StatusCode == http.StatusSeeOther {
		t.Fatal("a signed-in browser redeemed an invite")
	}
	if page := body(t, resp); !strings.Contains(page, `action="/logout"`) {
		t.Fatal("the signed-in invite POST did not get the sign-out page")
	}
	if _, _, err := in.reg.Authenticate(
		context.Background(),
		oldToken,
		registry.ScopeApp,
	); err != nil {
		t.Error("the guard revoked the existing session")
	}
	invites, _ := in.reg.ListInvites(context.Background(), in.adminID)
	if len(invites) != 1 || invites[0].Uses != 0 {
		t.Fatalf("the refused redemption spent a use: %+v", invites)
	}

	// A fresh browser has no app cookie, so the normal flow is untouched.
	fresh := in.newBrowser(t)
	resp = fresh.post(in.appURLFor("/invite/"+code), url.Values{
		"username": {"newperson"}, "password": {"correct horse battery"},
	})
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("signed-out redeem: status = %d", resp.StatusCode)
	}
	_ = resp.Body.Close()
	resp = fresh.get(in.appURLFor("/"))
	if resp.StatusCode != http.StatusOK || !strings.Contains(body(t, resp), "newperson") {
		t.Error("the redeemed browser is not signed in as the new identity")
	}
}

func TestRevokingOneSessionLeavesTheOtherPlaneAlone(t *testing.T) {
	in := newInstance(t, instanceOptions{})
	_, code, _ := in.reg.CreateInvite(
		context.Background(),
		in.adminID,
		registry.InviteParams{MaxUses: 1},
	)
	in.redeem(t, code, "player")
	b := in.newBrowser(t)
	b.login(in, "player", "correct horse battery")
	b.play(in, "heroes")
	appURL, _ := url.Parse(in.appURL)
	var appToken string
	for _, cookie := range b.jar.Cookies(appURL) {
		if cookie.Name == handlers.AppCookieName {
			appToken = cookie.Value
		}
	}
	if err := in.reg.RevokeSession(context.Background(), appToken); err != nil {
		t.Fatalf("revoke app session: %v", err)
	}
	resp := b.get(in.appURLFor("/account"))
	if strings.Contains(body(t, resp), "sign out everywhere") {
		t.Error("the revoked app session still works")
	}
	page := b.get(in.playURLFor("/worlds/heroes/"))
	if !strings.Contains(body(t, page), "data-version=") {
		t.Error("revoking the app session closed the play session")
	}
}

func TestRevokingAnInviteDoesNotDisableTheIdentity(t *testing.T) {
	in := newInstance(t, instanceOptions{})
	invite, code, _ := in.reg.CreateInvite(
		context.Background(),
		in.adminID,
		registry.InviteParams{MaxUses: 1},
	)
	_, _, _ = in.redeem(t, code, "player")
	if err := in.reg.RevokeInvite(context.Background(), in.adminID, invite.ID); err != nil {
		t.Fatalf("revoke invite: %v", err)
	}
	b := in.newBrowser(t)
	b.login(in, "player", "correct horse battery")
	worldURL, page := b.play(in, "heroes")
	if !strings.Contains(page, "data-version=") {
		t.Fatal("revoking the invite disabled the identity")
	}
	resp := b.postWorld(worldURL, url.Values{"body": {"still allowed"}})
	if resp.StatusCode != http.StatusSeeOther {
		t.Errorf("post after invite revocation: status = %d", resp.StatusCode)
	}
	_ = resp.Body.Close()
}
