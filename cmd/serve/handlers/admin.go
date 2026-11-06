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

// requireAdmin guards the admin routes. A request is allowed with the
// remembered login cookie or the admin token in a header or form field. The
// cookie is safe only because these routes live on the admin host; see
// AdminCookieName. A state change from another origin is refused outright,
// so the board's scripts cannot drive an action with a borrowed cookie.
func (h *Handlers) requireAdmin(w http.ResponseWriter, r *http.Request) bool {
	h.adminHeaders(w)
	authed, csrf := h.adminAccess(r)
	if !authed {
		// 404 rather than 403: the panel should not advertise itself.
		http.NotFound(w, r)
		return false
	}
	if !csrf {
		h.logger.Warn("Admin action refused for a missing token",
			slog.String("path", r.URL.Path),
		)
		http.Error(w, "Bad token", http.StatusForbidden)
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
// shows the password form until the browser remembers a login. The password
// form POSTs back to the same path; a correct password sets the signed,
// HttpOnly cookie and the page renders, so nothing ever puts the token in a
// URL. These routes are only served on the admin host, which has no board and
// so never runs the model's JavaScript.
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
	authed, _ := h.adminAccess(r)
	switch {
	case authed:
		h.adminDone(w, r, panel, "")
	case r.Method == http.MethodPost && h.tokenMatches(r.PostFormValue("password")):
		// Answer with a redirect, so the next request carries the new cookie
		// and the page gets its CSRF token, and a reload cannot re-post the
		// password.
		h.adminHeaders(w)
		h.setAdminSession(w, r)
		http.Redirect(w, r, r.URL.RequestURI(), http.StatusSeeOther)
	case r.Method == http.MethodPost:
		h.renderLogin(w, r, panel, true)
	default:
		h.renderLogin(w, r, panel, false)
	}
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
	view.Board = h.BoardURL(r)
	view.CSRF = h.csrfFor(r)
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
		Stream: h.adminStreamInit(panel),
	}
	switch panel.Page {
	case pages.PageOverview:
		turns, _, err := h.adminTurns(ctx, -1, nil)
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
		view.Notes = h.adminNotes(ctx)
	case pages.PageTurns:
		// The list always shows the running turn's progress. The inspector
		// is live when it follows the newest or holds the running turn; any
		// other picked turn stays as it was loaded. One snapshot covers the
		// list row and the inspector, so they cannot disagree.
		run := h.runProgress()
		live := liveInspector(panel.Turn, run)
		turns, detail, err := h.adminTurns(ctx, panel.Turn, run)
		if err != nil {
			return view, err
		}
		view.Turns = turns
		view.Turn = detail
		view.Queue = pages.NewQueue(h.queuedWork())
		if live {
			view.Run = run
			view.Panel.Live = true
		}
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
	case pages.PageJournal:
		site, err := h.queries.CurrentSite(ctx)
		if err != nil {
			return view, fmt.Errorf("load current site: %w", err)
		}
		view.States = h.adminStates(ctx, site)
		view.Journal, view.Browse = h.adminJournal(ctx, site, panel)
	}
	return view, nil
}

// adminStreamInit opens the page's stream. The remembered login rides along
// as a cookie, so no secret appears in the URL or the page. The stream closes
// when the page's mount is replaced, and the jitter keeps tabs from
// reconnecting in the same instant.
func (h *Handlers) adminStreamInit(panel pages.Panel) string {
	return fmt.Sprintf(
		"@get('%s', {requestCancellation: 'cleanup', retry: 'always', retryMaxCount: Infinity, retryInterval: %d})",
		panel.LivePath(),
		1000+rand.IntN(2000),
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
// A turn still running has no stored conversation yet, so its conversation so
// far comes from memory.
func (h *Handlers) adminTurns(
	ctx context.Context,
	pick int64,
	live *pages.RunProgress,
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
	addLiveTurn(rows, detail, live)
	return rows, detail, nil
}

// addLiveTurn lays the running turn out from its conversation so far, in the
// row and the inspector, so both show progress before it is stored. A row
// that is no longer running has its own stored conversation and keeps it.
func addLiveTurn(
	rows []pages.TurnRow,
	detail *pages.TurnDetail,
	live *pages.RunProgress,
) {
	if live == nil {
		return
	}
	for i := range rows {
		if rows[i].Run.ID == live.TurnID && rows[i].Run.Status == "running" {
			rows[i] = pages.NewTurnRow(rows[i].Run, live.Messages)
		}
	}
	if detail != nil && detail.Row.Run.ID == live.TurnID &&
		detail.Row.Run.Status == "running" {
		*detail = pages.NewTurnDetail(detail.Row.Run, live.Messages, webmaster.ToolsRunes())
	}
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
		view.Detail = fmt.Sprintf(
			"%s by %s (round %d, %s)",
			running.Kind,
			authors,
			running.Round,
			pages.RunPhase(running.Draft, running.RoundDone),
		)
	case pending > 0:
		view.State = "queued"
	}
	return view
}

// liveInspector reports whether the turns page with picked held follows the
// running turn: it follows the newest when nothing is picked, or the running
// turn was picked.
func liveInspector(picked int64, run *pages.RunProgress) bool {
	return picked == 0 || (run != nil && run.TurnID == picked)
}

// adminStates is the shared state the page and the prompt see, so a stuck
// value can be read without opening sqlite.
func (h *Handlers) adminStates(ctx context.Context, site sqlc.SiteVersion) []pages.StateView {
	live := h.siteWithState(ctx, site)
	states := make([]pages.StateView, 0, len(live.State))
	for key, value := range live.State {
		states = append(states, pages.StateView{Key: key, Value: string(value)})
	}
	sort.Slice(states, func(i, j int) bool { return states[i].Key < states[j].Key })
	return states
}

// adminNotes is what the next prompt will carry as the webmaster's notes.
func (h *Handlers) adminNotes(ctx context.Context) string {
	notes, err := h.queries.LatestNotes(ctx)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		h.logger.Warn("Failed to load the webmaster's notes", slog.Any("error", err))
	}
	return notes
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
		return fmt.Sprintf("undid version %d", current.ID), true
	}()
	if ok {
		h.adminDone(w, r, sitePanel, message)
	}
}

// HealBoard puts the plain seed back. Posts stay; shared state and the journal
// are cleared with it.
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

	instruction := truncate(strings.TrimSpace(r.FormValue("instruction")), webmaster.MaxBodyLen)
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

// StopTurn cancels the turn that is running, when it is the one named. The
// turn ends as an error labeled "stopped by admin", the page stays as it was,
// and posts that queued meanwhile still get their turn. It does not take h.mu:
// the running turn holds it, so waiting there would wait for the thing to stop.
func (h *Handlers) StopTurn(w http.ResponseWriter, r *http.Request) {
	if !h.requireAdmin(w, r) {
		return
	}
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.Error(w, "Bad turn id", http.StatusBadRequest)
		return
	}
	panel := pages.Panel{Page: pages.PageTurns, Turn: id}
	if h.stopTurn(id) {
		h.adminDone(w, r, panel, "stop sent, the turn is ending")
		return
	}
	h.adminDone(w, r, panel, "that turn is not running")
}

// errNoRetryTurn is a retry whose id names no webmaster turn.
var errNoRetryTurn = errors.New("no such webmaster turn")

// RetryTurn runs an errored webmaster turn again as a new turn. An admin
// instruction is sent as it was stored; a turn that answered posts answers the
// same posts, found in the range the worker had coalesced into it. The failed
// turn stays, so a retry that fails again can be retried from its page.
func (h *Handlers) RetryTurn(w http.ResponseWriter, r *http.Request) {
	if !h.requireAdmin(w, r) {
		return
	}
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.Error(w, "Bad turn id", http.StatusBadRequest)
		return
	}
	panel := pages.Panel{Page: pages.PageTurns, Turn: id}

	res, refusal, err := h.runRetry(r.Context(), id)
	switch {
	case errors.Is(err, errNoRetryTurn):
		http.NotFound(w, r)
	case err != nil:
		h.adminDone(w, r, panel, "retry failed: "+err.Error())
	case refusal != "":
		h.adminDone(w, r, panel, refusal)
	case res.Changed:
		h.adminDone(w, r, pages.Panel{Page: pages.PageTurns}, "retried: "+res.Reason)
	default:
		h.adminDone(w, r, pages.Panel{Page: pages.PageTurns}, "retried, no change: "+res.Reason)
	}
}

// runRetry reads the failed turn and its posts under h.mu, so a reset cannot
// delete them between the read and the run. A refusal is a message for the
// panel, not an error.
func (h *Handlers) runRetry(
	ctx context.Context,
	id int64,
) (webmaster.Result, string, error) {
	h.mu.Lock()
	defer h.mu.Unlock()

	turn, err := h.queries.GetWebmasterTurn(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return webmaster.Result{}, "", errNoRetryTurn
	}
	if err != nil {
		return webmaster.Result{}, "", fmt.Errorf("load turn: %w", err)
	}
	if turn.Status != "error" {
		return webmaster.Result{}, "only a turn that errored can be retried", nil
	}

	kind, instruction := "post", ""
	var triggers []sqlc.Post
	if strings.TrimSpace(turn.Body) != "" {
		kind, instruction = "admin", turn.Body
	} else {
		triggers, err = h.postsAnsweredBy(ctx, id)
		if err != nil {
			return webmaster.Result{}, "", err
		}
		if len(triggers) == 0 {
			return webmaster.Result{}, "nothing to retry: the turn has no instruction and no posts", nil
		}
	}

	res, err := h.mutateLocked(ctx, kind, instruction, triggers)
	return res, "", err
}

// postsAnsweredBy is the batch a webmaster turn answered: the posts between it
// and the webmaster turn before it, trimmed to the newest maxBatchPosts like
// the worker trims a burst. A turn that retried another has no posts of its
// own, so it walks back to the turn it copied.
func (h *Handlers) postsAnsweredBy(ctx context.Context, id int64) ([]sqlc.Post, error) {
	for range maxRetryWalkBack {
		posts, err := h.queries.ListPostsForTurn(ctx, id)
		if err != nil {
			return nil, fmt.Errorf("list the turn's posts: %w", err)
		}
		if len(posts) > maxBatchPosts {
			posts = posts[len(posts)-maxBatchPosts:]
		}
		if len(posts) > 0 {
			return posts, nil
		}
		previous, err := h.queries.PreviousWebmasterTurnID(ctx, id)
		if err != nil {
			return nil, fmt.Errorf("find the turn before it: %w", err)
		}
		if previous == 0 {
			return nil, nil
		}
		id = previous
	}
	return nil, nil
}

// maxRetryWalkBack bounds the walk through retries of retries.
const maxRetryWalkBack = 20

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
		return fmt.Sprintf("restored version %d", id), true
	}()
	if ok {
		h.adminDone(w, r, sitePanel, message)
	}
}
