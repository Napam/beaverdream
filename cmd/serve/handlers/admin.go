package handlers

import (
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/Napam/beaverdream/db/generated/sqlc"
	"github.com/Napam/beaverdream/lib/webmaster"
	"github.com/Napam/beaverdream/web/pages"
	"github.com/go-chi/chi/v5"
)

// requireAdmin guards the admin routes. The token must be present on every
// request (query for GET, form value for POST). No cookie on purpose: the
// model's page runs arbitrary same-origin JavaScript, and a cookie would hand
// that script the admin panel.
func (h *Handlers) requireAdmin(w http.ResponseWriter, r *http.Request) bool {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Content-Security-Policy", "frame-ancestors 'none'")
	// Without this, following a link out of admin sends the token in Referer,
	// where the model's page script could read it.
	w.Header().Set("Referrer-Policy", "no-referrer")

	token := r.URL.Query().Get("token")
	if token == "" {
		token = r.PostFormValue("token")
	}
	if h.cfg.AdminToken == "" ||
		subtle.ConstantTimeCompare([]byte(token), []byte(h.cfg.AdminToken)) != 1 {
		// 404 rather than 403: the panel should not advertise itself.
		http.NotFound(w, r)
		return false
	}
	return true
}

// Admin shows the current render, the prompt history, and a way to talk to the
// model directly.
func (h *Handlers) Admin(w http.ResponseWriter, r *http.Request) {
	if !h.requireAdmin(w, r) {
		return
	}
	ctx := r.Context()

	site, err := h.queries.CurrentSite(ctx)
	if err != nil {
		h.logger.Error("Failed to load current site", slog.Any("error", err))
		http.Error(w, "Could not load the site", http.StatusInternalServerError)
		return
	}
	runs, err := h.queries.ListMutationRuns(ctx, 50)
	if err != nil {
		h.logger.Error("Failed to list mutation runs", slog.Any("error", err))
		http.Error(w, "Could not load history", http.StatusInternalServerError)
		return
	}
	versions, err := h.queries.ListSiteVersions(ctx, 20)
	if err != nil {
		h.logger.Error("Failed to list versions", slog.Any("error", err))
		http.Error(w, "Could not load versions", http.StatusInternalServerError)
		return
	}

	views := make([]pages.RunView, 0, len(runs))
	for _, run := range runs {
		var messages []webmaster.Message
		if err := json.Unmarshal([]byte(run.Messages), &messages); err != nil {
			h.logger.Warn("Stored mutation messages are not valid JSON",
				slog.Int64("run_id", run.ID),
				slog.Any("error", err),
			)
		}
		views = append(views, pages.RunView{Run: run, Messages: messages})
	}

	if err := pages.AdminPage(
		site,
		views,
		versions,
		r.URL.Query().Get("msg"),
		h.cfg.AdminToken,
	).Render(ctx, w); err != nil {
		h.logger.Error("Failed to render admin page", slog.Any("error", err))
	}
}

// AdminMutate sends a free-form instruction straight to the model. It can also
// store the instruction as a real post, which is useful for setting up a mood
// without pretending to be a visitor.
func (h *Handlers) AdminMutate(w http.ResponseWriter, r *http.Request) {
	if !h.requireAdmin(w, r) {
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Bad form", http.StatusBadRequest)
		return
	}

	instruction := truncate(strings.TrimSpace(r.FormValue("instruction")), maxBodyLen)
	if instruction == "" {
		h.redirectAdmin(w, r, "nothing to send")
		return
	}

	var trigger *sqlc.Post
	if r.FormValue("also_post") != "" {
		post, err := h.queries.CreatePost(r.Context(), sqlc.CreatePostParams{
			Author: "admin",
			Body:   instruction,
		})
		if err != nil {
			h.logger.Error("Failed to store admin post", slog.Any("error", err))
		} else {
			trigger = &post
		}
	}

	if h.overDailyCap(r.Context()) {
		h.redirectAdmin(w, r, "daily mutation cap reached")
		return
	}

	ctx, cancel := mutationContext(r.Context())
	defer cancel()
	res, err := h.mutate(ctx, "admin", instruction, trigger)
	switch {
	case err != nil:
		h.redirectAdmin(w, r, "mutation failed: "+err.Error())
	case res.Changed:
		h.redirectAdmin(w, r, "changed: "+res.Reason)
	default:
		h.redirectAdmin(w, r, "no change: "+res.Reason)
	}
}

// RestoreVersion copies an old version forward as a new one. History stays
// append-only, so a restore is itself undoable.
func (h *Handlers) RestoreVersion(w http.ResponseWriter, r *http.Request) {
	if !h.requireAdmin(w, r) {
		return
	}
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.Error(w, "Bad version id", http.StatusBadRequest)
		return
	}

	version, err := h.queries.GetSiteVersion(r.Context(), id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if _, err := h.queries.CreateSiteVersion(r.Context(), sqlc.CreateSiteVersionParams{
		Css:    version.Css,
		Note:   version.Note,
		Header: version.Header,
		Board:  version.Board,
		Footer: version.Footer,
		Reason: fmt.Sprintf("restored version %d", id),
	}); err != nil {
		h.logger.Error("Failed to restore version", slog.Any("error", err))
		http.Error(w, "Could not restore", http.StatusInternalServerError)
		return
	}

	h.redirectAdmin(w, r, fmt.Sprintf("restored version %d", id))
}

func (h *Handlers) redirectAdmin(w http.ResponseWriter, r *http.Request, message string) {
	query := url.Values{}
	query.Set("token", h.cfg.AdminToken)
	query.Set("msg", message)
	http.Redirect(w, r, "/admin?"+query.Encode(), http.StatusSeeOther)
}
