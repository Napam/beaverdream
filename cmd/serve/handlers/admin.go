package handlers

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"math/rand/v2"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

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

// Admin, AdminTurns, AdminTurn and AdminSite are the admin pages. Each one
// shows the password form until the request presents the admin token. A
// browser GET cannot set a header, so the password form POSTs back to the same
// path; the token then lives in hidden form fields and the request headers of
// the stream and the panel's own navigation, never in the URL. There is no
// session cookie on purpose: the board runs model-written JavaScript on the
// same origin.
func (h *Handlers) Admin(w http.ResponseWriter, r *http.Request) {
	h.servePanel(w, r, pages.Panel{Page: pages.PageOverview})
}

func (h *Handlers) AdminTurns(w http.ResponseWriter, r *http.Request) {
	h.servePanel(w, r, pages.Panel{Page: pages.PageTurns})
}

func (h *Handlers) AdminTurn(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil || id <= 0 {
		http.NotFound(w, r)
		return
	}
	h.servePanel(w, r, pages.Panel{Page: pages.PageTurns, Turn: id})
}

func (h *Handlers) AdminSite(w http.ResponseWriter, r *http.Request) {
	h.servePanel(w, r, pages.Panel{Page: pages.PageSite})
}

// servePanel answers one admin page: the password form without the token, the
// page patched into #admin-body for the panel's own navigation, and the whole
// document otherwise.
func (h *Handlers) servePanel(w http.ResponseWriter, r *http.Request, panel pages.Panel) {
	if r.Method == http.MethodPost && r.Header.Get("X-Admin-Token") == "" {
		if !h.tokenMatches(r.PostFormValue("password")) {
			h.renderLogin(w, r, panel, true)
			return
		}
	} else if !h.adminTokenOK(r) {
		h.renderLogin(w, r, panel, false)
		return
	}
	h.adminDone(w, r, panel, "")
}

// renderLogin is the only page an unauthenticated visitor gets. A failed
// password answers 401, so a tool can tell the attempt apart from a success.
func (h *Handlers) renderLogin(
	w http.ResponseWriter,
	r *http.Request,
	panel pages.Panel,
	failed bool,
) {
	h.adminHeaders(w)
	if failed {
		w.WriteHeader(http.StatusUnauthorized)
	}
	if err := pages.AdminLogin(panel.Path(), failed).Render(r.Context(), w); err != nil {
		h.logger.Error("Failed to render the admin login", slog.Any("error", err))
	}
}

// errNoTurn is a turn page whose id names no webmaster turn.
var errNoTurn = errors.New("no such webmaster turn")

// adminDone answers an admin page or action without a redirect: a Datastar
// request gets the page patched into #admin-body, and anything else gets the
// whole document. The token never needs to appear in a URL.
func (h *Handlers) adminDone(
	w http.ResponseWriter,
	r *http.Request,
	panel pages.Panel,
	message string,
) {
	h.adminHeaders(w)
	view, err := h.loadAdmin(r.Context(), panel, message)
	if errors.Is(err, errNoTurn) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		h.logger.Error("Failed to load the admin page", slog.Any("error", err))
		http.Error(w, "Could not load the admin page", http.StatusInternalServerError)
		return
	}
	if !isDatastar(r) {
		datastar := h.cfg.DatastarSrc
		if datastar == "" {
			datastar = webmaster.DatastarPath
		}
		if err := pages.AdminPage(view, datastar).Render(r.Context(), w); err != nil {
			h.logger.Error("Failed to render admin page", slog.Any("error", err))
		}
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

// loadAdmin reads what one page shows, so the first render, a navigation and
// the patch after an action use the same value.
func (h *Handlers) loadAdmin(
	ctx context.Context,
	panel pages.Panel,
	flash string,
) (pages.AdminView, error) {
	view := pages.AdminView{
		Panel:  panel,
		Live:   h.adminLiveView(ctx),
		Flash:  flash,
		Token:  h.cfg.AdminToken,
		Stream: h.adminStreamInit(panel),
	}
	switch panel.Page {
	case pages.PageOverview:
		turns, _, err := h.adminTurns(ctx, -1)
		if err != nil {
			return view, err
		}
		site, err := h.queries.CurrentSite(ctx)
		if err != nil {
			return view, fmt.Errorf("load current site: %w", err)
		}
		view.Metrics = pages.NewMetrics(turns)
		view.Budget = h.adminBudget(ctx, site)
		view.Summary, view.SummaryThrough = h.adminSummary(ctx)
	case pages.PageTurns:
		turns, detail, err := h.adminTurns(ctx, panel.Turn)
		if err != nil {
			return view, err
		}
		view.Turns = turns
		view.Turn = detail
	case pages.PageSite:
		versions, err := h.queries.ListSiteVersions(ctx, adminVersions)
		if err != nil {
			return view, fmt.Errorf("list versions: %w", err)
		}
		if len(versions) == 0 {
			return view, errors.New("no site version")
		}
		// One read names the current version, so the table cannot mark an
		// older row current when a version lands between two queries.
		view.Site = versions[0]
		view.Versions = versions
		// The panel shows the same journal and state the page and the prompt
		// see, so a stuck feed can be read without opening sqlite.
		view.Feeds, view.States = h.adminFeeds(ctx, view.Site)
	}
	return view, nil
}

// adminStreamInit opens the page's stream. The token travels as a request
// header, not in the URL. The stream closes when the page's mount is replaced,
// and the jitter keeps tabs from reconnecting in the same instant.
func (h *Handlers) adminStreamInit(panel pages.Panel) string {
	return fmt.Sprintf(
		"@get('%s', {requestCancellation: 'cleanup', retry: 'always', retryMaxCount: Infinity, retryInterval: %d, headers: {'X-Admin-Token': %s}})",
		panel.LivePath(),
		1000+rand.IntN(2000),
		strconv.Quote(h.cfg.AdminToken),
	)
}

// The pages actions answer with: instructions from the overview, repairs
// from the site page.
var (
	overviewPanel = pages.Panel{Page: pages.PageOverview}
	sitePanel     = pages.Panel{Page: pages.PageSite}
)

const (
	// adminTurnCount is how many turns the list and the charts show.
	adminTurnCount = 40
	// adminVersions is how many versions the panel lists.
	adminVersions = 20
)

// adminTurns loads the newest turns as list rows, and one turn in full for the
// inspector: the newest when pick is 0, the picked one otherwise, none when
// pick is negative. Each conversation is decoded once, for its round stats.
func (h *Handlers) adminTurns(
	ctx context.Context,
	pick int64,
) ([]pages.TurnRow, *pages.TurnDetail, error) {
	runs, err := h.queries.ListWebmasterTurns(ctx, adminTurnCount)
	if err != nil {
		return nil, nil, fmt.Errorf("list webmaster turns: %w", err)
	}
	rows := make([]pages.TurnRow, 0, len(runs))
	var detail *pages.TurnDetail
	for i, run := range runs {
		messages := h.turnMessages(run)
		if (pick == 0 && i == 0) || run.ID == pick {
			d := pages.NewTurnDetail(run, messages, webmaster.ToolsRunes())
			detail = &d
		}
		rows = append(rows, pages.NewTurnRow(run, messages))
	}
	if pick > 0 && detail == nil {
		// Older than the list: read it on its own.
		row, err := h.queries.GetWebmasterTurn(ctx, pick)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil, errNoTurn
		}
		if err != nil {
			return nil, nil, fmt.Errorf("load turn %d: %w", pick, err)
		}
		run := sqlc.ListWebmasterTurnsRow(row)
		d := pages.NewTurnDetail(run, h.turnMessages(run), webmaster.ToolsRunes())
		detail = &d
	}
	return rows, detail, nil
}

// turnMessages decodes a stored conversation. A broken one shows as a turn
// with nothing stored rather than failing the panel.
func (h *Handlers) turnMessages(run sqlc.ListWebmasterTurnsRow) []webmaster.Message {
	var messages []webmaster.Message
	if err := json.Unmarshal([]byte(run.Messages), &messages); err != nil {
		h.logger.Warn("Stored mutation messages are not valid JSON",
			slog.Int64("run_id", run.ID),
			slog.Any("error", err),
		)
	}
	return messages
}

// adminBudget measures the room the way compactRoom does: the turns after the
// latest summary and the page the next turn reads.
func (h *Handlers) adminBudget(ctx context.Context, site sqlc.SiteVersion) pages.BudgetView {
	budget := pages.BudgetView{
		LimitTokens: h.compactAfterTokens,
		LimitTurns:  h.compactAfterTurns,
		PageRunes:   int64(webmaster.PageRunes(h.siteWithState(ctx, site))),
	}
	from, summary, err := h.latestSummary(ctx)
	if err != nil {
		h.logger.Warn("Failed to load compacted memory", slog.Any("error", err))
		return budget
	}
	budget.SummaryThrough = from
	budget.SummaryRunes = int64(utf8.RuneCountInString(summary))
	stats, err := h.queries.SummaryStats(ctx, sqlc.SummaryStatsParams{
		ID:   from,
		ID_2: math.MaxInt64,
	})
	if err != nil {
		h.logger.Warn("Failed to measure the room", slog.Any("error", err))
		return budget
	}
	budget.Turns = stats.Turns
	budget.LogRunes = numericInt64(stats.Chars)
	return budget
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
		h.adminDone(w, r, sitePanel, message)
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
		h.adminDone(w, r, sitePanel, "heal failed: "+err.Error())
		return
	}
	if !changed {
		h.adminDone(w, r, sitePanel, "the board is already plain")
		return
	}
	h.adminDone(w, r, sitePanel, "the board went back to the seed")
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
		h.adminDone(w, r, overviewPanel, "nothing to send")
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
		h.adminDone(w, r, overviewPanel, "daily mutation cap reached")
		return
	}

	res, err := h.mutate(r.Context(), "admin", instruction, trigger)
	switch {
	case err != nil:
		h.adminDone(w, r, overviewPanel, "mutation failed: "+err.Error())
	case res.Changed:
		h.adminDone(w, r, overviewPanel, "changed: "+res.Reason)
	default:
		h.adminDone(w, r, overviewPanel, "no change: "+res.Reason)
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
		h.adminDone(w, r, sitePanel, message)
	}
}
