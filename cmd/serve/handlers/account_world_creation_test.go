//nolint:bodyclose // bodies are closed by body() or drained as streams.
package handlers_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/Napam/beaverdream/cmd/serve/handlers"
	"github.com/Napam/beaverdream/lib/registry"
)

func TestSelfPasswordChangeHTTP(t *testing.T) {
	in := newInstance(t, instanceOptions{})
	ctx := context.Background()
	member, err := in.reg.CreateIdentityWithPassword(ctx, "member", "old-password", "Member")
	if err != nil {
		t.Fatal(err)
	}
	if err := in.reg.GrantAdmin(ctx, in.adminID, member.ID); err != nil {
		t.Fatal(err)
	}
	one, two := in.newBrowser(t), in.newBrowser(t)
	one.login(in, "member", "old-password")
	two.login(in, "member", "old-password")
	one.play(in, "heroes")
	two.play(in, "heroes")
	_, source, _, err := in.reg.Login(ctx, "member", "old-password")
	if err != nil {
		t.Fatal(err)
	}
	_, api, err := in.reg.CreateAPIToken(ctx, member.ID, source.LoginID, source.Hash)
	if err != nil {
		t.Fatal(err)
	}
	page := body(t, one.get(in.appURLFor("/account")))
	for _, want := range []string{`action="/account/password#password"`, `autocomplete="current-password"`, `name="username" autocomplete="username" value="member"`} {
		if !strings.Contains(page, want) {
			t.Errorf("account missing %s", want)
		}
	}
	csrf := one.csrf(in, "/account")
	form := url.Values{
		"csrf":             {csrf},
		"current_password": {"old-password"},
		"new_password":     {"new-password"},
		"confirm_password": {"new-password"},
	}
	resp := one.post(in.appURLFor("/account/password"), nil)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("no csrf = %d", resp.StatusCode)
	}
	_ = body(t, resp)
	for _, fields := range []url.Values{
		{"current_password": {"wrong"}},
		{"confirm_password": {"mismatch"}},
		{"new_password": {"short"}, "confirm_password": {"short"}},
		{"new_password": {strings.Repeat("x", 1025)}, "confirm_password": {strings.Repeat("x", 1025)}},
	} {
		attempt := make(url.Values, len(form))
		for k, v := range form {
			attempt[k] = v
		}
		for k, v := range fields {
			attempt[k] = v
		}
		resp := one.post(in.appURLFor("/account/password"), attempt)
		if resp.StatusCode != http.StatusBadRequest ||
			resp.Header.Get("Cache-Control") != "no-store" ||
			!strings.HasPrefix(resp.Header.Get("Content-Type"), "text/html") {
			t.Fatalf("invalid password response = %d, %v", resp.StatusCode, resp.Header)
		}
		if page := body(
			t,
			resp,
		); !strings.Contains(page, `role="alert"`) ||
			strings.Contains(page, `value="old-password"`) {
			t.Fatal("invalid password form has no alert or echoes password")
		}
	}
	streamDone := make([]chan struct{}, 0, 2)
	for _, b := range []*browser{one, two} {
		stream := b.get(in.playURLFor("/worlds/heroes/live"))
		if stream.StatusCode != http.StatusOK {
			t.Fatalf("live = %d", stream.StatusCode)
		}
		done := make(chan struct{})
		go func() { _, _ = io.Copy(io.Discard, stream.Body); _ = stream.Body.Close(); close(done) }()
		streamDone = append(streamDone, done)
	}
	resp = one.post(in.appURLFor("/account/password"), form)
	if resp.StatusCode != http.StatusSeeOther ||
		resp.Header.Get("Location") != "/account?password=changed#password-notice" {
		t.Fatalf("change = %d, %v, %s", resp.StatusCode, resp.Header, body(t, resp))
	}
	_ = body(t, resp)
	for _, done := range streamDone {
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Fatal("play stream survived password change")
		}
	}
	page = body(t, one.get(in.appURLFor("/account?password=changed")))
	if !strings.Contains(page, `role="status"`) ||
		!strings.Contains(page, `action="/account/password#password"`) {
		t.Fatal("current browser lost its account or notice")
	}
	if strings.Contains(body(t, two.get(in.appURLFor("/account"))), `action="/account/password`) {
		t.Fatal("other browser stayed signed in")
	}
	if strings.Contains(body(t, one.get(in.playURLFor("/worlds/heroes/"))), `data-version=`) {
		t.Fatal("current browser's world survived")
	}
	if _, _, err := in.reg.Authenticate(
		ctx,
		api,
		registry.ScopeAPI,
	); !errors.Is(
		err,
		registry.ErrSessionInvalid,
	) {
		t.Fatalf("api after change = %v", err)
	}
	old := in.newBrowser(t)
	resp = old.post(
		in.appURLFor("/login"),
		url.Values{"username": {"member"}, "password": {"old-password"}},
	)
	if resp.StatusCode == http.StatusSeeOther {
		t.Fatal("old password still works")
	}
	_ = body(t, resp)
	in.newBrowser(t).login(in, "member", "new-password")
	one.play(in, "heroes")
}

func TestPasswordChangeSystemAdminAndBearerRefused(t *testing.T) {
	in := newInstance(t, instanceOptions{})
	admin := in.newBrowser(t)
	admin.login(in, "admin", acceptanceAdminPassword)
	if page := body(
		t,
		admin.get(in.appURLFor("/account")),
	); strings.Contains(page, `action="/account/password`) ||
		!strings.Contains(page, "ADMIN_PASSWORD") {
		t.Fatal("system admin has password form or no explanation")
	}
	resp := admin.post(
		in.appURLFor("/account/password"),
		url.Values{"csrf": {admin.csrf(in, "/account")}},
	)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("system admin change = %d", resp.StatusCode)
	}
	_ = body(t, resp)
	_, token, err := in.createAdminAPIToken(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		path := "/account"
		if method == http.MethodPost {
			path += "/password"
		}
		req, err := http.NewRequest(method, in.appURLFor(path), nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		page := body(t, resp)
		if method == http.MethodPost {
			if resp.StatusCode != http.StatusForbidden {
				t.Fatalf("bearer change = %d", resp.StatusCode)
			}
		} else if strings.Contains(page, `action="/account/password`) || !strings.Contains(page, "sign in with a browser") {
			t.Fatal("bearer saw password form or no explanation")
		}
	}
}

func TestMemberCreatesSharedWorldButCannotManageIt(t *testing.T) {
	in := newInstance(t, instanceOptions{})
	ctx := context.Background()
	member, err := in.reg.CreateIdentityWithPassword(ctx, "member", "member-password", "Ada")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := in.reg.CreateIdentityWithPassword(
		ctx,
		"other",
		"other-password",
		"Other",
	); err != nil {
		t.Fatal(err)
	}
	b := in.newBrowser(t)
	b.login(in, "member", "member-password")
	csrf := b.csrf(in, "/")
	page := body(t, b.get(in.appURLFor("/")))
	if !strings.Contains(page, `action="/worlds#make-world"`) ||
		strings.Contains(page, `href="/admin"`) {
		t.Fatal("member lobby form/admin link wrong")
	}
	for _, tc := range []struct {
		slug, csrf string
		status     int
		text       string
	}{
		{"valid", "", http.StatusForbidden, ""},
		{"Heroes", csrf, http.StatusOK, "use lowercase letters, digits and dashes."},
		{"heroes", csrf, http.StatusOK, "that name is taken."},
	} {
		resp := b.post(in.appURLFor("/worlds"), url.Values{"slug": {tc.slug}, "csrf": {tc.csrf}})
		if resp.StatusCode != tc.status {
			t.Fatalf("create %s = %d", tc.slug, resp.StatusCode)
		}
		page := body(t, resp)
		if tc.text != "" &&
			(!strings.Contains(page, tc.text) || !strings.Contains(page, `value="`+tc.slug+`"`)) {
			t.Fatalf("missing error/retained slug: %s", page)
		}
	}
	resp := in.newBrowser(t).post(in.appURLFor("/worlds"), url.Values{"slug": {"anon"}})
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("anon create = %d", resp.StatusCode)
	}
	_ = body(t, resp)
	resp = b.post(in.appURLFor("/worlds"), url.Values{"slug": {"garden"}, "csrf": {csrf}})
	if resp.StatusCode != http.StatusSeeOther ||
		resp.Header.Get("Location") != "/?created=garden#world-garden" {
		t.Fatalf("member create = %d, %v, %s", resp.StatusCode, resp.Header, body(t, resp))
	}
	_ = body(t, resp)
	world, err := in.reg.GetWorldBySlug(ctx, "garden")
	if err != nil || world.CreatedBy != member.ID || world.Status != registry.WorldReady {
		t.Fatalf("world = %+v, %v", world, err)
	}
	page = body(t, b.get(in.appURLFor("/?created=garden")))
	if !strings.Contains(page, `id="world-garden"`) ||
		!strings.Contains(page, `your world is ready. press enter.`) {
		t.Fatal("new world card not marked")
	}
	other := in.newBrowser(t)
	other.login(in, "other", "other-password")
	if !strings.Contains(body(t, other.get(in.appURLFor("/"))), `id="world-garden"`) {
		t.Fatal("world not shared")
	}
	b.play(in, "garden")
	for _, path := range []string{"/admin", "/admin/worlds/garden/"} {
		resp := b.get(in.appURLFor(path))
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("member GET %s = %d", path, resp.StatusCode)
		}
		_ = body(t, resp)
	}
	for _, action := range []string{"delete", "rename", "reset", "heal", "changeset"} {
		resp := b.post(
			in.appURLFor("/admin/worlds/garden/"+action),
			url.Values{"csrf": {csrf}, "slug": {"renamed"}, "instruction": {"pink"}},
		)
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("member %s = %d", action, resp.StatusCode)
		}
		_ = body(t, resp)
	}
	if got, err := in.reg.GetWorldBySlug(ctx, "garden"); err != nil || got.ID != world.ID {
		t.Fatalf("management changed world: %+v, %v", got, err)
	}
	handle := in.worldDB(t, world.ID)
	var turns, versions int
	if err := handle.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM turns`).
		Scan(&turns); err != nil {
		t.Fatal(err)
	}
	if err := handle.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM site_versions`).
		Scan(&versions); err != nil {
		t.Fatal(err)
	}
	if turns != 0 || versions != 1 {
		t.Fatalf("management changed content: %d turns, %d versions", turns, versions)
	}
	admin := in.newBrowser(t)
	admin.login(in, "admin", acceptanceAdminPassword)
	page = body(t, admin.get(in.appURLFor("/admin")))
	if !strings.Contains(strings.Join(strings.Fields(page), " "), "by Ada (member)") {
		t.Fatalf("creator not identified: %s", page)
	}
	if _, err := in.reg.CreateWorldRecord(ctx, "missing-creator", "no-creator", ""); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body(t, admin.get(in.appURLFor("/admin"))), "no-creator") {
		t.Fatal("NULL creator world missing")
	}
}

func TestAdminTogglesMemberWorldCreation(t *testing.T) {
	in := newInstance(t, instanceOptions{})
	ctx := context.Background()
	if _, err := in.reg.CreateIdentityWithPassword(
		ctx,
		"member",
		"member-password",
		"Member",
	); err != nil {
		t.Fatal(err)
	}
	member, admin := in.newBrowser(t), in.newBrowser(t)
	member.login(in, "member", "member-password")
	admin.login(in, "admin", acceptanceAdminPassword)
	csrf, memberCSRF := admin.csrf(in, "/admin"), member.csrf(in, "/")
	for _, tc := range []struct {
		b      *browser
		form   url.Values
		status int
	}{
		{admin, url.Values{"open": {"0"}}, http.StatusForbidden},
		{admin, url.Values{"csrf": {csrf}, "open": {"bad"}}, http.StatusBadRequest},
		{member, url.Values{"csrf": {memberCSRF}, "open": {"0"}}, http.StatusNotFound},
	} {
		resp := tc.b.post(in.appURLFor("/admin/worlds/creation"), tc.form)
		if resp.StatusCode != tc.status {
			t.Fatalf("toggle refusal = %d, want %d", resp.StatusCode, tc.status)
		}
		_ = body(t, resp)
	}
	for _, open := range []string{"0", "1"} {
		resp := admin.post(
			in.appURLFor("/admin/worlds/creation"),
			url.Values{"csrf": {csrf}, "open": {open}},
		)
		if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/admin" {
			t.Fatalf("toggle = %d, %v", resp.StatusCode, resp.Header)
		}
		_ = body(t, resp)
		page := body(t, member.get(in.appURLFor("/")))
		if strings.Contains(page, `action="/worlds#make-world"`) != (open == "1") {
			t.Fatal("member form disagrees with policy")
		}
		if open == "0" {
			if !strings.Contains(page, "making worlds is closed") {
				t.Fatal("no closed explanation")
			}
			if page := body(
				t,
				admin.get(in.appURLFor("/admin")),
			); !strings.Contains(page, "admins only") ||
				!strings.Contains(page, "open to members") {
				t.Fatal("toggle state wrong")
			}
			if page := body(
				t,
				admin.get(in.appURLFor("/")),
			); !strings.Contains(page, `action="/worlds#make-world"`) ||
				!strings.Contains(page, "closed for members") {
				t.Fatal("admin lobby closed incorrectly")
			}
			resp := member.post(
				in.appURLFor("/worlds"),
				url.Values{"csrf": {memberCSRF}, "slug": {"refused"}},
			)
			if strings.Count(body(t, resp), "making worlds is closed") != 1 {
				t.Fatal("closed error missing or repeated")
			}
			if _, err := in.reg.GetWorldBySlug(
				ctx,
				"refused",
			); !errors.Is(
				err,
				registry.ErrNotFound,
			) {
				t.Fatalf("closed create = %v", err)
			}
			for _, path := range []string{"/worlds", "/admin/worlds"} {
				slug := "admin-lobby"
				if path == "/admin/worlds" {
					slug = "admin-panel"
				}
				resp := admin.post(in.appURLFor(path), url.Values{"csrf": {csrf}, "slug": {slug}})
				if resp.StatusCode != http.StatusSeeOther {
					t.Fatalf("admin closed create = %d, %s", resp.StatusCode, body(t, resp))
				}
				_ = body(t, resp)
			}
			member.play(in, "heroes")
		}
	}
}

func TestCreationEmptyStatesAndUnreadablePolicy(t *testing.T) {
	in := newInstance(t, instanceOptions{})
	ctx := context.Background()
	for _, id := range []string{in.heroesID, in.atlasID} {
		if err := in.worlds.Delete(ctx, in.adminID, id); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := in.reg.CreateIdentityWithPassword(
		ctx,
		"member",
		"member-password",
		"Member",
	); err != nil {
		t.Fatal(err)
	}
	b := in.newBrowser(t)
	b.login(in, "member", "member-password")
	page := body(t, b.get(in.appURLFor("/")))
	if !strings.Contains(page, "make the first one below.") ||
		!strings.Contains(page, `action="/worlds#make-world"`) {
		t.Fatal("open empty lobby has no creation path")
	}
	csrf := b.csrf(in, "/")
	if err := in.reg.SetWorldCreationOpen(ctx, in.adminID, false); err != nil {
		t.Fatal(err)
	}
	page = body(t, b.get(in.appURLFor("/")))
	if !strings.Contains(page, "an admin can make one.") ||
		strings.Contains(page, "make the first one below.") {
		t.Fatal("closed empty lobby has misleading copy")
	}
	if _, err := in.reg.DB().ExecContext(ctx, `DROP TABLE settings`); err != nil {
		t.Fatal(err)
	}
	page = body(t, b.get(in.appURLFor("/")))
	if strings.Contains(page, `action="/worlds#make-world"`) ||
		strings.Contains(page, "closed right now") ||
		strings.Contains(page, "an admin can make one.") {
		t.Fatal("unreadable policy displayed as a closed policy")
	}
	resp := b.post(in.appURLFor("/worlds"), url.Values{"csrf": {csrf}, "slug": {"refused"}})
	if !strings.Contains(body(t, resp), `role="alert"`) {
		t.Fatal("failed create silently hid its error")
	}
}

func TestPasswordlessAccountHasNoPasswordForm(t *testing.T) {
	in := newInstance(t, instanceOptions{})
	ctx := context.Background()
	_, err := in.reg.CreateIdentityWithPassword(ctx, "member", "old-password", "Member")
	if err != nil {
		t.Fatal(err)
	}
	_, session, token, err := in.reg.Login(ctx, "member", "old-password")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := in.reg.DB().
		ExecContext(ctx, `UPDATE logins SET source = 'other' WHERE id = ?`, session.LoginID); err != nil {
		t.Fatal(err)
	}
	b := in.newBrowser(t)
	origin, err := url.Parse(in.appURL)
	if err != nil {
		t.Fatal(err)
	}
	b.jar.SetCookies(origin, []*http.Cookie{{Name: handlers.AppCookieName, Value: token}})
	page := body(t, b.get(in.appURLFor("/account")))
	if strings.Contains(page, `action="/account/password`) ||
		!strings.Contains(page, "this account does not use a password") {
		t.Fatal("passwordless account has a form or no explanation")
	}
	resp := b.post(in.appURLFor("/account/password"), url.Values{
		"csrf": {b.csrf(in, "/account")}, "current_password": {"old-password"},
		"new_password": {"new-password"}, "confirm_password": {"new-password"},
	})
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("passwordless POST = %d", resp.StatusCode)
	}
	_ = body(t, resp)
	if _, _, err := in.reg.Authenticate(ctx, token, registry.ScopeApp); err != nil {
		t.Fatalf("passwordless session was revoked: %v", err)
	}
}
