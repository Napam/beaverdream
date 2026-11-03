package handlers

import (
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"errors"
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
	runs, err := h.queries.ListWebmasterTurns(ctx, 20)
	if err != nil {
		h.logger.Error("Failed to list webmaster turns", slog.Any("error", err))
		http.Error(w, "Could not load history", http.StatusInternalServerError)
		return
	}
	versions, err := h.queries.ListSiteVersions(ctx, 12)
	if err != nil {
		h.logger.Error("Failed to list versions", slog.Any("error", err))
		http.Error(w, "Could not load versions", http.StatusInternalServerError)
		return
	}

	views := make([]pages.RunView, 0, len(runs))
	for i, run := range runs {
		// The newest conversations are the ones worth opening. Older runs
		// keep their summaries so the page does not carry every prompt.
		loaded := i < 8
		var messages []webmaster.Message
		if loaded {
			if err := json.Unmarshal([]byte(run.Messages), &messages); err != nil {
				h.logger.Warn("Stored mutation messages are not valid JSON",
					slog.Int64("run_id", run.ID),
					slog.Any("error", err),
				)
			}
		}
		views = append(views, pages.RunView{
			Run:            run,
			Messages:       messages,
			MessagesLoaded: loaded,
		})
	}

	summaryText := ""
	var summaryThrough int64
	summary, err := h.queries.GetLatestSummary(ctx)
	switch {
	case err == nil:
		summaryText = summary.Text
		summaryThrough = summary.ThroughTurnID
	case errors.Is(err, sql.ErrNoRows):
	default:
		h.logger.Warn("Failed to load compacted memory", slog.Any("error", err))
	}

	if err := pages.AdminPage(
		site,
		views,
		versions,
		summaryText,
		summaryThrough,
		r.URL.Query().Get("msg"),
		h.cfg.AdminToken,
	).Render(ctx, w); err != nil {
		h.logger.Error("Failed to render admin page", slog.Any("error", err))
	}
}

// AdminStatus is the small live card on the admin page. It reports the same
// state the board sees, plus today's mutation count against the cap.
func (h *Handlers) AdminStatus(w http.ResponseWriter, r *http.Request) {
	if !h.requireAdmin(w, r) {
		return
	}

	version, pending, running, lastError := h.liveSnapshot()
	if raw, err := h.queries.CurrentVersionID(r.Context()); err == nil {
		if id, ok := raw.(int64); ok && id > 0 && id != version {
			version = id
			h.setVersion(id)
		}
	}
	if version == 0 {
		var err error
		version, err = h.currentVersion(r.Context())
		if err != nil {
			http.Error(w, "unknown", http.StatusInternalServerError)
			return
		}
	}
	today, err := h.queries.CountWebmasterTurnsSince(r.Context(), utcDayStart())
	if err != nil {
		h.logger.Error("Failed to count mutations", slog.Any("error", err))
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(struct {
		Version int64     `json:"version"`
		Pending int       `json:"pending"`
		Running *runState `json:"running,omitempty"`
		Error   string    `json:"error,omitempty"`
		Today   int64     `json:"today"`
		Cap     int       `json:"cap"`
	}{
		Version: version,
		Pending: pending,
		Running: running,
		Error:   lastError,
		Today:   today,
		Cap:     h.cfg.MaxMutationsPerDay,
	})
}

// UndoLast copies the version before the current one forward. It is the
// one-click answer to a rewrite that went too far.
func (h *Handlers) UndoLast(w http.ResponseWriter, r *http.Request) {
	if !h.requireAdmin(w, r) {
		return
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	current, err := h.queries.CurrentSite(r.Context())
	if err != nil {
		h.logger.Error("Failed to load current site", slog.Any("error", err))
		http.Error(w, "Could not undo", http.StatusInternalServerError)
		return
	}
	if current.ID <= 1 {
		h.redirectAdmin(w, r, "nothing to undo")
		return
	}

	previous, err := h.queries.GetSiteVersion(r.Context(), current.ID-1)
	if err != nil {
		h.redirectAdmin(w, r, "the previous version is gone")
		return
	}
	restored, err := h.carryVersion(
		r.Context(),
		previous,
		webmaster.TurnUndo,
		"admin",
		fmt.Sprintf("undid version %d", current.ID),
	)
	if err != nil {
		h.logger.Error("Failed to undo the last version", slog.Any("error", err))
		http.Error(w, "Could not undo", http.StatusInternalServerError)
		return
	}
	h.setVersion(restored.ID)
	h.touchActivity()

	h.redirectAdmin(w, r, fmt.Sprintf("undid version %d", current.ID))
}

// HealBoard puts the plain seed back now instead of waiting for the idle
// timer. Posts and shared state stay.
func (h *Handlers) HealBoard(w http.ResponseWriter, r *http.Request) {
	if !h.requireAdmin(w, r) {
		return
	}
	changed, err := h.heal(r.Context())
	if err != nil {
		h.logger.Error("Failed to heal the board", slog.Any("error", err))
		h.redirectAdmin(w, r, "heal failed: "+err.Error())
		return
	}
	if !changed {
		h.redirectAdmin(w, r, "the board is already plain")
		return
	}
	h.redirectAdmin(w, r, "the board went back to the seed")
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
			converted := createdPost(post)
			trigger = &converted
		}
	}

	if h.overDailyCap(r.Context()) {
		h.redirectAdmin(w, r, "daily mutation cap reached")
		return
	}

	res, err := h.mutate(r.Context(), "admin", instruction, trigger)
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

	// A restore that races a running mutation would be silently undone when
	// that mutation writes its version. Take the same lock.
	h.mu.Lock()
	defer h.mu.Unlock()

	version, err := h.queries.GetSiteVersion(r.Context(), id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	restored, err := h.carryVersion(
		r.Context(),
		version,
		webmaster.TurnRestore,
		"admin",
		fmt.Sprintf("restored version %d", id),
	)
	if err != nil {
		h.logger.Error("Failed to restore version", slog.Any("error", err))
		http.Error(w, "Could not restore", http.StatusInternalServerError)
		return
	}
	h.setVersion(restored.ID)
	h.touchActivity()

	h.redirectAdmin(w, r, fmt.Sprintf("restored version %d", id))
}

func (h *Handlers) redirectAdmin(w http.ResponseWriter, r *http.Request, message string) {
	query := url.Values{}
	query.Set("token", h.cfg.AdminToken)
	query.Set("msg", message)
	http.Redirect(w, r, "/admin?"+query.Encode(), http.StatusSeeOther)
}
