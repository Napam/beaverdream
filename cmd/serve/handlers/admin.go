package handlers

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/Napam/beaverdream/db/generated/sqlc"
	"github.com/Napam/beaverdream/lib/webmaster"
	"github.com/Napam/beaverdream/web/pages"
	"github.com/go-chi/chi/v5"
	"github.com/starfederation/datastar-go/datastar"
)

// requireAdmin guards the admin routes. The token must be present on every
// request: a header for the stream and fetches, a hidden form field for
// actions. No cookie on purpose: the model's page runs arbitrary same-origin
// JavaScript, and a cookie would hand that script the admin panel.
func (h *Handlers) requireAdmin(w http.ResponseWriter, r *http.Request) bool {
	h.adminHeaders(w)
	if !h.adminTokenOK(r) {
		// 404 rather than 403: the panel should not advertise itself.
		http.NotFound(w, r)
		return false
	}
	return true
}

// adminTokenOK reports a request that carries the admin token: the stream and
// fetch calls send it as a header, action forms as a hidden field. The query
// string is deliberately not accepted, so the token never lands in a URL,
// history entry or referrer.
func (h *Handlers) adminTokenOK(r *http.Request) bool {
	token := r.Header.Get("X-Admin-Token")
	if token == "" {
		token = r.PostFormValue("token")
	}
	return h.tokenMatches(token)
}

func (h *Handlers) tokenMatches(token string) bool {
	return h.cfg.AdminToken != "" &&
		subtle.ConstantTimeCompare([]byte(token), []byte(h.cfg.AdminToken)) == 1
}

// adminHeaders are the ones every admin response carries.
func (h *Handlers) adminHeaders(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Content-Security-Policy", "frame-ancestors 'none'")
	// Without this, following a link out of admin sends the URL in Referer,
	// where the model's page script could read it.
	w.Header().Set("Referrer-Policy", "no-referrer")
}

// Admin shows the password form until the request presents the admin token,
// then the panel. A browser GET cannot set a header, so the password form
// POSTs here; the token then lives in hidden form fields and the stream's
// request headers, never in the URL. There is no session cookie on purpose:
// the board runs model-written JavaScript on the same origin.
func (h *Handlers) Admin(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		if !h.tokenMatches(r.PostFormValue("password")) {
			h.renderLogin(w, r, true)
			return
		}
		h.renderAdmin(w, r, "")
		return
	}
	if !h.adminTokenOK(r) {
		h.renderLogin(w, r, false)
		return
	}
	h.renderAdmin(w, r, "")
}

// renderLogin is the only page an unauthenticated visitor gets. A failed
// password answers 401, so a tool can tell the attempt apart from a success.
func (h *Handlers) renderLogin(w http.ResponseWriter, r *http.Request, failed bool) {
	h.adminHeaders(w)
	if failed {
		w.WriteHeader(http.StatusUnauthorized)
	}
	if err := pages.AdminLogin(failed).Render(r.Context(), w); err != nil {
		h.logger.Error("Failed to render the admin login", slog.Any("error", err))
	}
}

// renderAdmin renders the whole panel. Actions use it as the plain fallback
// when Datastar is not driving the form.
func (h *Handlers) renderAdmin(w http.ResponseWriter, r *http.Request, flash string) {
	h.adminHeaders(w)
	view, err := h.loadAdmin(r.Context(), flash)
	if err != nil {
		h.logger.Error("Failed to load the admin page", slog.Any("error", err))
		http.Error(w, "Could not load the admin page", http.StatusInternalServerError)
		return
	}
	datastar := h.cfg.DatastarSrc
	if datastar == "" {
		datastar = webmaster.DatastarPath
	}
	// The token travels as a request header on the stream, not in its URL.
	// The jitter keeps tabs from reconnecting in the same instant.
	streamInit := fmt.Sprintf(
		"@get('/admin/live', {retry: 'always', retryMaxCount: Infinity, retryInterval: %d, headers: {'X-Admin-Token': %s}})",
		1000+rand.IntN(2000),
		strconv.Quote(h.cfg.AdminToken),
	)
	if err := pages.AdminPage(view, datastar, streamInit).Render(r.Context(), w); err != nil {
		h.logger.Error("Failed to render admin page", slog.Any("error", err))
	}
}

// loadAdmin reads everything the panel shows, so the first render and the
// patch after an action use the same value.
func (h *Handlers) loadAdmin(ctx context.Context, flash string) (pages.AdminView, error) {
	site, err := h.queries.CurrentSite(ctx)
	if err != nil {
		return pages.AdminView{}, fmt.Errorf("load current site: %w", err)
	}
	runs, err := h.adminRuns(ctx)
	if err != nil {
		return pages.AdminView{}, err
	}
	versions, err := h.queries.ListSiteVersions(ctx, 12)
	if err != nil {
		return pages.AdminView{}, fmt.Errorf("list versions: %w", err)
	}

	// The panel shows the same journal and state the page and the prompt see,
	// so a stuck feed can be read without opening sqlite.
	feeds, states := h.adminFeeds(ctx, site)

	summaryText, summaryThrough := h.adminSummary(ctx)

	return pages.AdminView{
		Site:           site,
		Live:           h.adminLiveView(ctx),
		Feeds:          feeds,
		States:         states,
		Runs:           runs,
		Versions:       versions,
		Summary:        summaryText,
		SummaryThrough: summaryThrough,
		Flash:          flash,
		Token:          h.cfg.AdminToken,
	}, nil
}

// adminRuns loads the newest turns. The newest conversations are the ones worth
// opening; older runs keep their summaries so the page does not carry every
// prompt.
func (h *Handlers) adminRuns(ctx context.Context) ([]pages.RunView, error) {
	runs, err := h.queries.ListWebmasterTurns(ctx, 20)
	if err != nil {
		return nil, fmt.Errorf("list webmaster turns: %w", err)
	}
	views := make([]pages.RunView, 0, len(runs))
	for i, run := range runs {
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
	return views, nil
}

// adminSummary is the compacted memory the prompt would read, if any.
func (h *Handlers) adminSummary(ctx context.Context) (string, int64) {
	summary, err := h.queries.GetLatestSummary(ctx)
	switch {
	case err == nil:
		return summary.Text, summary.ThroughTurnID
	case errors.Is(err, sql.ErrNoRows):
	default:
		h.logger.Warn("Failed to load compacted memory", slog.Any("error", err))
	}
	return "", 0
}

// adminLiveView is the status card's snapshot, built the same way for the
// first render and for every stream patch.
func (h *Handlers) adminLiveView(ctx context.Context) pages.LiveView {
	version, pending, running, lastError := h.liveSnapshot()
	if raw, err := h.queries.CurrentVersionID(ctx); err == nil {
		if id, ok := raw.(int64); ok && id > 0 {
			version = id
		}
	}
	if version == 0 {
		if id, err := h.currentVersion(ctx); err == nil {
			version = id
		}
	}
	today, err := h.queries.CountWebmasterTurnsSince(ctx, utcDayStart())
	if err != nil {
		h.logger.Error("Failed to count mutations", slog.Any("error", err))
	}

	view := pages.LiveView{
		State:   "idle",
		Version: version,
		Pending: pending,
		Today:   today,
		Cap:     h.cfg.MaxMutationsPerDay,
		Detail:  "waiting for a post",
		Error:   lastError,
	}
	switch {
	case running != nil:
		authors := strings.Join(running.Authors, ", ")
		if authors == "" {
			authors = "the board"
		}
		view.State = "running"
		view.Detail = fmt.Sprintf("%s by %s (round %d)", running.Kind, authors, running.Round)
	case pending > 0:
		view.State = "queued"
	}
	return view
}

// adminFeeds is what the panel shows of the journal and shared state.
func (h *Handlers) adminFeeds(
	ctx context.Context,
	site sqlc.SiteVersion,
) ([]pages.FeedView, []pages.StateView) {
	live := h.siteWithState(ctx, site)
	feeds := make([]pages.FeedView, 0, len(live.Journal))
	for key, state := range live.Journal {
		feeds = append(feeds, pages.FeedView{
			Key:   key,
			Match: state.Match,
			Rules: state.Rules,
			Rows:  state.Rows,
			Last:  state.Last,
		})
	}
	sort.Slice(feeds, func(i, j int) bool { return feeds[i].Key < feeds[j].Key })

	states := make([]pages.StateView, 0, len(live.State))
	for key, value := range live.State {
		states = append(states, pages.StateView{Key: key, Value: string(value)})
	}
	sort.Slice(states, func(i, j int) bool { return states[i].Key < states[j].Key })
	return feeds, states
}

// UndoLast copies the version before the current one forward. It is the
// one-click answer to a rewrite that went too far.
func (h *Handlers) UndoLast(w http.ResponseWriter, r *http.Request) {
	if !h.requireAdmin(w, r) {
		return
	}

	// The lock covers the write only. Rendering the panel is slow and must not
	// hold up posts or state writes.
	message, ok := func() (string, bool) {
		h.mu.Lock()
		defer h.mu.Unlock()

		current, err := h.queries.CurrentSite(r.Context())
		if err != nil {
			h.logger.Error("Failed to load current site", slog.Any("error", err))
			http.Error(w, "Could not undo", http.StatusInternalServerError)
			return "", false
		}
		if current.ID <= 1 {
			return "nothing to undo", true
		}

		previous, err := h.queries.GetSiteVersion(r.Context(), current.ID-1)
		if err != nil {
			return "the previous version is gone", true
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
			return "", false
		}
		h.setVersion(restored.ID)
		h.touchActivity()
		return fmt.Sprintf("undid version %d", current.ID), true
	}()
	if ok {
		h.adminDone(w, r, message)
	}
}

// HealBoard puts the plain seed back now instead of waiting for the idle
// timer. Posts stay; shared state and the journal are cleared with it.
func (h *Handlers) HealBoard(w http.ResponseWriter, r *http.Request) {
	if !h.requireAdmin(w, r) {
		return
	}
	changed, err := h.heal(r.Context())
	if err != nil {
		h.logger.Error("Failed to heal the board", slog.Any("error", err))
		h.adminDone(w, r, "heal failed: "+err.Error())
		return
	}
	if !changed {
		h.adminDone(w, r, "the board is already plain")
		return
	}
	h.adminDone(w, r, "the board went back to the seed")
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
		h.adminDone(w, r, "nothing to send")
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
			h.hub.publish(topicPosts)
		}
	}

	if h.overDailyCap(r.Context()) {
		h.adminDone(w, r, "daily mutation cap reached")
		return
	}

	res, err := h.mutate(r.Context(), "admin", instruction, trigger)
	switch {
	case err != nil:
		h.adminDone(w, r, "mutation failed: "+err.Error())
	case res.Changed:
		h.adminDone(w, r, "changed: "+res.Reason)
	default:
		h.adminDone(w, r, "no change: "+res.Reason)
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
	// that mutation writes its version. Take the same lock, but release it
	// before rendering the panel.
	message, ok := func() (string, bool) {
		h.mu.Lock()
		defer h.mu.Unlock()

		version, err := h.queries.GetSiteVersion(r.Context(), id)
		if err != nil {
			http.NotFound(w, r)
			return "", false
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
			return "", false
		}
		h.setVersion(restored.ID)
		h.touchActivity()
		return fmt.Sprintf("restored version %d", id), true
	}()
	if ok {
		h.adminDone(w, r, message)
	}
}

// adminDone answers an admin action without a redirect: a Datastar request
// gets the panel patched in place, and a plain form gets the whole page. The
// token never needs to appear in a URL.
func (h *Handlers) adminDone(w http.ResponseWriter, r *http.Request, message string) {
	if !isDatastar(r) {
		h.renderAdmin(w, r, message)
		return
	}
	h.adminHeaders(w)
	view, err := h.loadAdmin(r.Context(), message)
	if err != nil {
		h.logger.Error("Failed to load the admin page", slog.Any("error", err))
		http.Error(w, "Could not load the admin page", http.StatusInternalServerError)
		return
	}
	var b strings.Builder
	if err := pages.AdminBody(view).Render(r.Context(), &b); err != nil {
		h.logger.Error("Failed to render the admin panel", slog.Any("error", err))
		http.Error(w, "Could not render the admin panel", http.StatusInternalServerError)
		return
	}
	sse := datastar.NewSSE(w, r)
	if err := sse.PatchElements(
		b.String(),
		datastar.WithSelectorID("admin-body"),
		datastar.WithModeOuter(),
	); err != nil {
		h.logger.Warn("Failed to patch the admin panel", slog.Any("error", err))
	}
}
