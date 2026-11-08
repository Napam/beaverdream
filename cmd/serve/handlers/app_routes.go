package handlers

import (
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/Napam/beaverdream"
	"github.com/Napam/beaverdream/lib/registry"
	"github.com/Napam/beaverdream/lib/webmaster"
	"github.com/Napam/beaverdream/lib/webmaster/skills"
	"github.com/Napam/beaverdream/lib/worlds"
	"github.com/Napam/beaverdream/web/pages"
	"github.com/a-h/templ"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)

// AppRouter is the management plane: login, account, invites, the lobby and
// every admin page. It mounts no world route.
func (a *App) AppRouter() *chi.Mux {
	router := chi.NewRouter()
	router.Use(middleware.Recoverer)
	router.Use(a.securityHeaders)

	beaverdream.SetupStatic(router)
	router.Get("/favicon.ico", favicon)

	router.Get("/login", a.LoginPage)
	router.Post("/login", a.Login)
	router.Get("/invite/{code}", a.InvitePage)
	router.Post("/invite/{code}", a.RedeemInvite)

	router.Group(func(r chi.Router) {
		r.Use(a.requireApp)

		r.Get("/", a.Lobby)
		r.Post("/logout", a.Logout)
		r.Get("/account", a.Account)
		r.Post("/account/name", a.UpdateName)
		r.Post("/account/logout-all", a.LogoutAll)
		r.Post("/account/tokens", a.CreateAPIToken)
		r.Post("/account/tokens/revoke", a.RevokeAPIToken)
		r.Post("/worlds/{slug}/play", a.PlayHandoff)

		r.Group(func(ar chi.Router) {
			ar.Use(a.requireAppAdmin)
			ar.Get("/admin", a.AdminHome)
			ar.Get("/admin/invites", a.AdminInvites)
			ar.Get("/admin/accounts", a.AdminAccounts)
			ar.Get("/admin/model", a.AdminModel)
			ar.Post("/admin/worlds", a.CreateWorld)
			ar.Post("/admin/worlds/{slug}/delete", a.DeleteWorld)
			ar.Post("/admin/invites", a.CreateInvite)
			ar.Post("/admin/invites/{id}/revoke", a.RevokeInvite)
			ar.Post("/admin/identities/{id}/disable", a.DisableIdentity)
			ar.Get("/admin/worlds/{slug}", a.AdminWorldRoot)

			ar.Route("/admin/worlds/{slug}", func(wr chi.Router) {
				wr.Use(a.withWorldSlug)
				wr.Get("/", a.worldAdmin((*Handlers).Admin))
				wr.Get("/changesets", a.worldAdmin((*Handlers).AdminChangesets))
				wr.Get("/changesets/{id}", a.worldAdmin((*Handlers).AdminChangeset))
				wr.Get("/site", a.worldAdmin((*Handlers).AdminSite))
				wr.Get("/journal", a.worldAdmin((*Handlers).AdminJournal))
				wr.Post("/journal", a.worldAdmin((*Handlers).AdminJournal))
				wr.Post("/journal/{key}/clear", a.worldAdmin((*Handlers).ClearJournal))
				wr.Post("/holdings/{key}/clear", a.worldAdmin((*Handlers).ClearHoldings))
				wr.Get("/live", a.worldAdmin((*Handlers).AdminLive))
				wr.Post("/changeset", a.worldAdmin((*Handlers).SendInstruction))
				wr.Post("/retry/{id}", a.worldAdmin((*Handlers).RetryTurn))
				wr.Post("/stop/{id}", a.worldAdmin((*Handlers).StopTurn))
				wr.Post("/undo", a.worldAdmin((*Handlers).UndoLast))
				wr.Post("/heal", a.worldAdmin((*Handlers).HealSite))
				wr.Post("/versions/{id}/restore", a.worldAdmin((*Handlers).RestoreVersion))
				wr.Post("/reset", a.worldAdmin((*Handlers).ResetWorld))
			})
		})
	})

	return router
}

// PlayRouter is the play plane. It mounts no login, invite, lobby or admin
// route, and every play path lives under one world's slug.
func (a *App) PlayRouter() *chi.Mux {
	router := chi.NewRouter()
	router.Use(middleware.Recoverer)

	beaverdream.SetupStatic(router)
	router.Get("/favicon.ico", favicon)
	router.Get("/handoff", a.ExchangeHandoff)
	router.Get("/", a.gate)

	router.Group(func(r chi.Router) {
		r.Use(a.requirePlay)
		r.Get("/worlds/{slug}", a.RedirectWorld)
		r.Route("/worlds/{slug}", func(wr chi.Router) {
			wr.Use(a.withWorldSlug)
			wr.Get("/", a.worldPlay((*Handlers).Index))
			wr.Get("/live", a.worldPlay((*Handlers).Live))
			wr.Post("/posts", a.worldPlay((*Handlers).CreatePost))
			wr.Get("/version", a.worldPlay((*Handlers).Version))
			wr.Get("/state", a.worldPlay((*Handlers).State))
			wr.Post("/state/{key}", a.worldPlay((*Handlers).SetState))
			wr.Get("/journal/{key}", a.worldPlay((*Handlers).JournalRead))
			wr.Post("/journal/{key}", a.worldPlay((*Handlers).JournalAppend))
			wr.Post("/errors", a.worldPlay((*Handlers).ClientError))
			wr.Get("/budget", a.worldPlay((*Handlers).Budget))
		})
	})

	// Any other play path is the gate when anonymous and a 404 when signed
	// in; nothing falls through to a code-owned page at the root.
	router.NotFound(a.playNotFound)
	return router
}

func (a *App) playNotFound(w http.ResponseWriter, r *http.Request) {
	if _, err := a.authenticatePlay(r); err == nil {
		http.NotFound(w, r)
		return
	}
	// A world path still gets the gate; an app-only path is simply not here.
	if strings.HasPrefix(r.URL.Path, "/worlds/") {
		a.gate(w, r)
		return
	}
	http.NotFound(w, r)
}

func favicon(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, webmaster.FaviconPath, http.StatusMovedPermanently)
}

// worldPlay adapts a world handler to an HTTP handler.
func (a *App) worldPlay(fn func(*Handlers, http.ResponseWriter, *http.Request)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		h := a.worldHandler(r)
		if h == nil {
			http.NotFound(w, r)
			return
		}
		fn(h, w, r)
	}
}

func (a *App) worldAdmin(fn func(*Handlers, http.ResponseWriter, *http.Request)) http.HandlerFunc {
	return a.worldPlay(fn)
}

func (a *App) worldHandler(r *http.Request) *Handlers {
	if rt := worldFrom(r); rt != nil {
		return rt.h
	}
	return nil
}

// LoginPage shows the app sign-in form.
func (a *App) LoginPage(w http.ResponseWriter, r *http.Request) {
	if p, err := a.authenticateApp(r); err == nil && p != nil && !p.API {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	a.renderLogin(w, r, r.URL.Query().Get("next"), "")
}

func (a *App) renderLogin(w http.ResponseWriter, r *http.Request, next, failed string) {
	if next == "" || !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") {
		next = "/"
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if failed != "" {
		w.WriteHeader(http.StatusUnauthorized)
	}
	_ = pages.AppLogin(next, failed, a.loginURL(r), a.cfg.Config.DatastarSrc).Render(r.Context(), w)
}

// Login verifies a password and sets an app session.
func (a *App) Login(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Bad form", http.StatusBadRequest)
		return
	}
	next := r.FormValue("next")
	identity, _, token, err := a.reg.Login(
		r.Context(),
		r.FormValue("username"),
		r.FormValue("password"),
	)
	if err != nil {
		if errors.Is(err, registry.ErrInvalidCredentials) {
			a.renderLogin(w, r, next, "wrong username or password")
			return
		}
		a.logger.Error("Failed to sign in", slog.Any("error", err))
		http.Error(w, "Could not sign in", http.StatusInternalServerError)
		return
	}
	a.setAppSession(w, r, token, a.cfg.Config.AppSessionTTL)
	if next == "" || !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") {
		next = "/"
	}
	a.logger.Info("Identity signed in", slog.String("identity_id", identity.ID))
	http.Redirect(w, r, next, http.StatusSeeOther)
}

// Logout revokes this app session and forgets the app cookie. Play sessions
// stay, because a world runs in a different browser context.
func (a *App) Logout(w http.ResponseWriter, r *http.Request) {
	if !a.requireCSRF(w, r) {
		return
	}
	p := principalFrom(r)
	if p != nil && !p.API {
		if err := a.reg.RevokeSession(r.Context(), p.SessionToken); err != nil {
			a.logger.Warn("Failed to revoke an app session", slog.Any("error", err))
		}
		a.cancelSessionStreams(p.SessionHash)
	}
	clearSessionCookie(w, r, AppCookieName)
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

// InvitePage shows the redemption form. A GET never spends a use.
func (a *App) InvitePage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_ = pages.AppInvite("", "", "").Render(r.Context(), w)
}

// RedeemInvite creates an account and a first app session. Every code failure
// answers the same status and body; a bad form value says what is wrong, so a
// short password does not read as a dead invite.
func (a *App) RedeemInvite(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Bad form", http.StatusBadRequest)
		return
	}
	code := chi.URLParam(r, "code")
	username := r.FormValue("username")
	displayName := r.FormValue("display_name")
	fail := func(message string) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusBadRequest)
		_ = pages.AppInvite(message, username, displayName).Render(r.Context(), w)
	}
	cleanName, err := registry.CleanUsername(username)
	if err != nil {
		fail("usernames are 3 to 32 characters and start with a letter or digit. " +
			"dots, dashes and underscores go after that.")
		return
	}
	if err := registry.ValidatePassword(r.FormValue("password")); err != nil {
		fail("passwords need at least 8 characters.")
		return
	}
	// This public endpoint cannot revoke a cookie session without CSRF. A
	// successful redemption replaces the cookie but leaves the old session.
	identity, _, token, err := a.reg.RedeemInvite(
		r.Context(), code,
		cleanName,
		r.FormValue("password"),
		displayName,
	)
	if err != nil {
		switch {
		case errors.Is(err, registry.ErrUsernameTaken):
			fail("that username is taken. pick another.")
		case errors.Is(err, registry.ErrInviteInvalid):
			fail("that invite cannot be used.")
		default:
			a.logger.Error("Failed to redeem an invite", slog.Any("error", err))
			fail("could not create the account. try again.")
		}
		return
	}
	a.setAppSession(w, r, token, a.cfg.Config.AppSessionTTL)
	a.logger.Info("Invite redeemed", slog.String("identity_id", identity.ID))
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// Lobby lists the ready worlds.
func (a *App) Lobby(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r)
	worlds, err := a.reg.ListReadyWorlds(r.Context())
	if err != nil {
		a.logger.Error("Failed to list worlds", slog.Any("error", err))
		http.Error(w, "Could not load the worlds", http.StatusInternalServerError)
		return
	}
	view := pages.AppView{Name: p.DisplayName, Admin: p.Admin, CSRF: p.CSRFToken}
	for _, world := range worlds {
		view.Worlds = append(view.Worlds, appWorld(world, r, a.cfg.Config))
	}
	a.renderApp(w, r, pages.AppLobby(view))
}

func appWorld(world registry.World, r *http.Request, cfg Config) pages.AppWorld {
	return pages.AppWorld{
		ID:        world.ID,
		Slug:      world.Slug,
		Status:    world.Status,
		CreatedAt: world.CreatedAt,
		WorldURL:  playOrigin(r, cfg) + "/worlds/" + url.PathEscape(world.Slug) + "/",
	}
}

// PlayHandoff mints a single-use code bound to the identity, login and world,
// then sends the browser to the play port.
func (a *App) PlayHandoff(w http.ResponseWriter, r *http.Request) {
	if !a.requireCSRF(w, r) {
		return
	}
	p := principalFrom(r)
	slug := chi.URLParam(r, "slug")
	world, err := a.reg.GetWorldBySlug(r.Context(), slug)
	if err != nil || world.Status != registry.WorldReady {
		http.NotFound(w, r)
		return
	}
	code, _, err := a.reg.CreateHandoff(r.Context(), p.IdentityID, p.LoginID, world.ID)
	if err != nil {
		a.logger.Error("Failed to create a handoff", slog.Any("error", err))
		http.Error(w, "Could not open the world", http.StatusInternalServerError)
		return
	}
	target := playOrigin(r, a.cfg.Config) + "/handoff?code=" + url.QueryEscape(code)
	http.Redirect(w, r, target, http.StatusSeeOther)
}

// ExchangeHandoff spends a code, sets the play cookie and drops the code from
// the URL.
func (a *App) ExchangeHandoff(w http.ResponseWriter, r *http.Request) {
	code := r.URL.Query().Get("code")
	if code == "" {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	identity, _, token, worldID, err := a.reg.ExchangeHandoff(r.Context(), code)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		_ = pages.AppGate(appOrigin(r, a.cfg.Config)+"/login").Render(r.Context(), w)
		return
	}
	a.setPlaySession(w, r, token, a.cfg.Config.PlaySessionTTL)
	world, err := a.reg.GetWorld(r.Context(), worldID)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	a.logger.Info("Play session opened",
		slog.String("identity_id", identity.ID),
		slog.String("world_id", worldID),
	)
	http.Redirect(w, r, "/worlds/"+url.PathEscape(world.Slug)+"/", http.StatusSeeOther)
}

// AdminWorldRoot sends the world admin mount to its trailing-slash page.
func (a *App) AdminWorldRoot(w http.ResponseWriter, r *http.Request) {
	slug := chi.URLParam(r, "slug")
	target := "/admin/worlds/" + url.PathEscape(slug) + "/"
	if r.URL.RawQuery != "" {
		target += "?" + r.URL.RawQuery
	}
	http.Redirect(w, r, target, http.StatusMovedPermanently)
}

// RedirectWorld sends /worlds/{slug} to the trailing-slash page so relative
// model links resolve inside the world.
func (a *App) RedirectWorld(w http.ResponseWriter, r *http.Request) {
	slug := chi.URLParam(r, "slug")
	http.Redirect(w, r, "/worlds/"+url.PathEscape(slug)+"/", http.StatusMovedPermanently)
}

// Account is the app account page.
func (a *App) Account(w http.ResponseWriter, r *http.Request) {
	a.renderAccount(w, r, "")
}

func (a *App) renderAccount(w http.ResponseWriter, r *http.Request, token string) {
	p := principalFrom(r)
	view := pages.AppView{
		Name:        p.DisplayName,
		Admin:       p.Admin,
		CSRF:        p.CSRFToken,
		NewAPIToken: token,
	}
	a.renderApp(w, r, pages.AppAccount(view))
}

// UpdateName changes the display name every world reads.
func (a *App) UpdateName(w http.ResponseWriter, r *http.Request) {
	if !a.requireCSRF(w, r) {
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Bad form", http.StatusBadRequest)
		return
	}
	p := principalFrom(r)
	name := registry.CleanDisplayName(r.FormValue("display_name"), p.DisplayName)
	if err := a.reg.SetDisplayName(r.Context(), p.IdentityID, name); err != nil {
		a.logger.Error("Failed to set the display name", slog.Any("error", err))
		http.Error(w, "Could not save the name", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/account", http.StatusSeeOther)
}

// LogoutAll revokes every session of the identity, closes every stream in
// every loaded world and forgets both cookies.
func (a *App) LogoutAll(w http.ResponseWriter, r *http.Request) {
	if !a.requireCSRF(w, r) {
		return
	}
	p := principalFrom(r)
	if err := a.reg.RevokeAllSessions(r.Context(), p.IdentityID); err != nil {
		a.logger.Error("Failed to revoke every session", slog.Any("error", err))
		http.Error(w, "Could not sign out", http.StatusInternalServerError)
		return
	}
	a.cancelIdentityStreams(p.IdentityID)
	clearSessionCookie(w, r, AppCookieName)
	clearSessionCookie(w, r, PlayCookieName)
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

// CreateAPIToken mints an admin script token and shows it once.
func (a *App) CreateAPIToken(w http.ResponseWriter, r *http.Request) {
	if !a.requireCSRF(w, r) {
		return
	}
	p := principalFrom(r)
	_, token, err := a.reg.CreateAPIToken(r.Context(), p.IdentityID, p.LoginID)
	if err != nil {
		if errors.Is(err, registry.ErrForbidden) {
			http.Error(w, "Only an admin may mint a token", http.StatusForbidden)
			return
		}
		a.logger.Error("Failed to mint an api token", slog.Any("error", err))
		http.Error(w, "Could not create a token", http.StatusInternalServerError)
		return
	}
	a.renderAccount(w, r, token)
}

// RevokeAPIToken revokes a token by its raw value.
func (a *App) RevokeAPIToken(w http.ResponseWriter, r *http.Request) {
	if !a.requireCSRF(w, r) {
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Bad form", http.StatusBadRequest)
		return
	}
	token := strings.TrimSpace(r.FormValue("token"))
	if token != "" {
		_, session, err := a.reg.Authenticate(r.Context(), token, registry.ScopeAPI)
		if err == nil {
			if err := a.reg.RevokeAPIToken(r.Context(), token); err != nil {
				a.logger.Warn("Failed to revoke an api token", slog.Any("error", err))
			} else {
				a.cancelSessionStreams(session.Hash)
			}
		}
	}
	http.Redirect(w, r, "/account", http.StatusSeeOther)
}

// AdminHome shows the world list, the admin's landing page.
func (a *App) AdminHome(w http.ResponseWriter, r *http.Request) {
	a.renderAdmin(w, r, pages.AdminWorlds, "", "")
}

// AdminInvites shows the invite list and the create form. Creating an invite
// POSTs to the same path.
func (a *App) AdminInvites(w http.ResponseWriter, r *http.Request) {
	a.renderAdmin(w, r, pages.AdminInvites, "", "")
}

// AdminAccounts shows the accounts and their disable buttons.
func (a *App) AdminAccounts(w http.ResponseWriter, r *http.Request) {
	a.renderAdmin(w, r, pages.AdminAccounts, "", "")
}

// AdminModel shows the skills and tools the webmaster can load and call.
func (a *App) AdminModel(w http.ResponseWriter, r *http.Request) {
	a.renderAdmin(w, r, pages.AdminModel, "", "")
}

func (a *App) renderAdmin(w http.ResponseWriter, r *http.Request, section, message, token string) {
	view, ok := a.adminView(r, section, message, "", token)
	if !ok {
		http.NotFound(w, r)
		return
	}
	a.renderApp(w, r, pages.AppAdmin(view))
}

func (a *App) renderAdminWith(
	w http.ResponseWriter,
	r *http.Request,
	section string,
	extra pages.AppView,
) {
	view, ok := a.adminView(r, section, "", extra.NewInviteLink, extra.NewAPIToken)
	if !ok {
		http.NotFound(w, r)
		return
	}
	a.renderApp(w, r, pages.AppAdmin(view))
}

func (a *App) adminView(
	r *http.Request,
	section, message, link, token string,
) (pages.AppView, bool) {
	p := principalFrom(r)
	if p == nil {
		return pages.AppView{}, false
	}
	view := pages.AppView{
		Name:          p.DisplayName,
		Admin:         p.Admin,
		CSRF:          p.CSRFToken,
		AdminSection:  section,
		Error:         message,
		NewInviteLink: link,
		NewAPIToken:   token,
	}
	worldRows, err := a.reg.ListWorlds(r.Context())
	if err != nil {
		a.logger.Error("Failed to list worlds", slog.Any("error", err))
	}
	for _, world := range worldRows {
		view.Worlds = append(view.Worlds, appWorld(world, r, a.cfg.Config))
	}
	invites, err := a.reg.ListInvites(r.Context(), p.IdentityID)
	if err != nil {
		a.logger.Error("Failed to list invites", slog.Any("error", err))
	}
	for _, invite := range invites {
		view.Invites = append(view.Invites, pages.AppInviteRow{
			ID:        invite.ID,
			Label:     invite.Label,
			MaxUses:   invite.MaxUses,
			Uses:      invite.Uses,
			ExpiresAt: invite.ExpiresAt,
			Revoked:   invite.RevokedAt != nil,
			CreatedAt: invite.CreatedAt,
		})
	}
	identities, err := a.reg.ListIdentities(r.Context(), p.IdentityID)
	if err != nil {
		a.logger.Error("Failed to list identities", slog.Any("error", err))
	}
	for _, identity := range identities {
		view.Identities = append(view.Identities, pages.AppIdentity{
			ID:          identity.ID,
			DisplayName: identity.DisplayName,
			Disabled:    identity.Disabled(),
			CreatedAt:   identity.CreatedAt,
		})
	}
	if section == pages.AdminModel {
		for _, skill := range skills.All {
			view.Skills = append(view.Skills, pages.AppSkill{
				Name:    skill.Name,
				Summary: skill.Summary,
				Body:    skill.Body,
			})
		}
		for _, tool := range webmaster.Tools() {
			view.Tools = append(view.Tools, pages.AppTool{
				Name:        tool.Name,
				Description: tool.Description,
				Parameters:  tool.Parameters,
			})
		}
	}
	return view, true
}

func (a *App) renderApp(w http.ResponseWriter, r *http.Request, component templ.Component) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if err := component.Render(r.Context(), w); err != nil {
		a.logger.Error("Failed to render an app page", slog.Any("error", err))
	}
}

// CreateWorld makes a world file and its registry row.
func (a *App) CreateWorld(w http.ResponseWriter, r *http.Request) {
	if !a.requireCSRF(w, r) {
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Bad form", http.StatusBadRequest)
		return
	}
	p := principalFrom(r)
	slug := strings.TrimSpace(r.FormValue("slug"))
	world, err := a.worlds.Create(r.Context(), p.IdentityID, slug)
	switch {
	case errors.Is(err, registry.ErrWorldExists), errors.Is(err, worlds.ErrInvalidSlug):
		a.renderAdmin(w, r, pages.AdminWorlds, "that world name is not available", "")
		return
	case err != nil:
		a.logger.Error("Failed to create a world", slog.Any("error", err))
		a.renderAdmin(w, r, pages.AdminWorlds, "could not create the world", "")
		return
	}
	a.logger.Info("World created",
		slog.String("world_id", world.ID), slog.String("slug", world.Slug))
	http.Redirect(w, r, "/admin", http.StatusSeeOther)
}

// DeleteWorld deletes one world and nothing in the registry else.
func (a *App) DeleteWorld(w http.ResponseWriter, r *http.Request) {
	if !a.requireCSRF(w, r) {
		return
	}
	p := principalFrom(r)
	slug := chi.URLParam(r, "slug")
	world, err := a.reg.GetWorldBySlug(r.Context(), slug)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	// Refuse new plays and turns before the runtime drains.
	if err := a.reg.SetWorldStatus(r.Context(), world.ID, registry.WorldDeleting); err != nil {
		a.logger.Error("Failed to mark a world deleting", slog.Any("error", err))
		a.renderAdmin(w, r, pages.AdminWorlds, "could not delete the world", "")
		return
	}
	if !a.evict(r.Context(), world.ID) {
		a.renderAdmin(w, r, pages.AdminWorlds, "could not delete the world", "")
		return
	}
	if err := a.worlds.Delete(r.Context(), p.IdentityID, world.ID); err != nil {
		a.logger.Error("Failed to delete a world", slog.Any("error", err))
		a.renderAdmin(w, r, pages.AdminWorlds, "could not delete the world", "")
		return
	}
	a.logger.Info(
		"World deleted",
		slog.String("world_id", world.ID),
		slog.String("slug", world.Slug),
	)
	http.Redirect(w, r, "/admin", http.StatusSeeOther)
}

// CreateInvite makes a single-use or multi-use code and shows the link once.
func (a *App) CreateInvite(w http.ResponseWriter, r *http.Request) {
	if !a.requireCSRF(w, r) {
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Bad form", http.StatusBadRequest)
		return
	}
	p := principalFrom(r)
	uses, _ := strconv.Atoi(r.FormValue("max_uses"))
	if uses <= 0 {
		uses = 1
	}
	_, code, err := a.reg.CreateInvite(r.Context(), p.IdentityID, registry.InviteParams{
		Label:   r.FormValue("label"),
		MaxUses: uses,
	})
	if err != nil {
		a.logger.Error("Failed to create an invite", slog.Any("error", err))
		a.renderAdmin(w, r, pages.AdminInvites, "could not create the invite", "")
		return
	}
	view := pages.AppView{NewInviteLink: appOrigin(r, a.cfg.Config) + pages.InvitePath(code)}
	// Rebuild the rest of the dashboard around the one-time link.
	a.renderAdminWith(w, r, pages.AdminInvites, view)
}

// RevokeInvite stops future redemptions and leaves the identity untouched.
func (a *App) RevokeInvite(w http.ResponseWriter, r *http.Request) {
	if !a.requireCSRF(w, r) {
		return
	}
	p := principalFrom(r)
	if err := a.reg.RevokeInvite(r.Context(), p.IdentityID, chi.URLParam(r, "id")); err != nil &&
		!errors.Is(err, registry.ErrNotFound) {
		a.logger.Warn("Failed to revoke an invite", slog.Any("error", err))
	}
	http.Redirect(w, r, "/admin/invites", http.StatusSeeOther)
}

// DisableIdentity disables one account and closes its streams in every loaded
// world.
func (a *App) DisableIdentity(w http.ResponseWriter, r *http.Request) {
	if !a.requireCSRF(w, r) {
		return
	}
	p := principalFrom(r)
	id := chi.URLParam(r, "id")
	if err := a.reg.DisableIdentity(r.Context(), p.IdentityID, id); err != nil {
		a.logger.Warn("Failed to disable an identity", slog.Any("error", err))
	}
	a.cancelIdentityStreams(id)
	http.Redirect(w, r, "/admin/accounts", http.StatusSeeOther)
}
