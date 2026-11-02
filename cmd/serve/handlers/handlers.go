package handlers

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/Napam/beaverdream/db/generated/sqlc"
	"github.com/Napam/beaverdream/lib/webmaster"
)

const (
	maxBodyLen   = 2000
	maxAuthorLen = 32
	// recentLimit is how many posts reach the model. The board shows every
	// post; the model only needs enough of them to keep the tone.
	recentLimit = 20
	// mutationTimeout bounds one model call sequence. A local model can be
	// slow, but not this slow.
	mutationTimeout = 2 * time.Minute
)

// Config holds the environment knobs the handlers enforce.
type Config struct {
	// AdminToken guards /admin. Every admin request must carry it in the
	// query string or form; there is no cookie.
	AdminToken string
	// MaxMutationsPerDay is a cost kill switch. Zero disables the cap.
	MaxMutationsPerDay int
}

// runState is what the live endpoint and the admin panel show while a
// mutation is in flight.
type runState struct {
	Kind    string    `json:"kind"`
	Authors []string  `json:"authors,omitempty"`
	Round   int       `json:"round"`
	Since   time.Time `json:"since"`
}

// Handlers serves the board and the admin panel.
type Handlers struct {
	logger  *slog.Logger
	db      *sql.DB
	queries *sqlc.Queries
	mutator *webmaster.Mutator
	cfg     Config

	// mu serializes mutations. One process, one site: a second mutation
	// waiting beats two of them writing versions over each other.
	mu sync.Mutex

	// stateMu serializes shared-state writes, so two clicks cannot read the
	// same counter and write the same next value.
	stateMu sync.Mutex

	// The worker coalesces posts that arrive while a mutation is running, so
	// a burst becomes one rewrite instead of a queue of them.
	liveMu        sync.Mutex
	version       int64
	pending       []sqlc.Post
	running       *runState
	lastError     string
	lastRewriteAt map[string]time.Time
	lastStateAt   map[string]time.Time
	lastActivity  time.Time
	wake          chan struct{}
	stop          chan struct{}
	closeOnce     sync.Once
	workerWG      sync.WaitGroup
}

// New wires the handlers.
func New(
	logger *slog.Logger,
	dbConn *sql.DB,
	queries *sqlc.Queries,
	mutator *webmaster.Mutator,
	cfg Config,
) *Handlers {
	h := &Handlers{
		logger:        logger,
		db:            dbConn,
		queries:       queries,
		mutator:       mutator,
		cfg:           cfg,
		lastRewriteAt: make(map[string]time.Time),
		lastStateAt:   make(map[string]time.Time),
		lastActivity:  time.Now(),
		wake:          make(chan struct{}, 1),
		stop:          make(chan struct{}),
	}
	if site, err := queries.CurrentSite(context.Background()); err == nil {
		h.version = site.ID
	}
	mutator.OnRound = h.setRunningRound
	h.workerWG.Add(2)
	go func() {
		defer h.workerWG.Done()
		h.worker()
	}()
	go func() {
		defer h.workerWG.Done()
		h.healer()
	}()
	return h
}

// Close stops the background workers and waits for them to finish. Tests call
// it; the server can too.
func (h *Handlers) Close() {
	h.closeOnce.Do(func() {
		close(h.stop)
		h.workerWG.Wait()
	})
}

func siteFromRow(row sqlc.SiteVersion) webmaster.Site {
	return webmaster.Site{
		Version:     row.ID,
		CSS:         row.Css,
		Note:        row.Note,
		Header:      row.Header,
		Board:       row.Board,
		Footer:      row.Footer,
		StateSchema: row.StateSchema,
	}
}

// siteWithState attaches the model-declared shared state to a version before
// it is rendered or sent to the model.
func (h *Handlers) siteWithState(ctx context.Context, row sqlc.SiteVersion) webmaster.Site {
	site := siteFromRow(row)
	schema := parseStateSchema(row.StateSchema)
	if len(schema) == 0 {
		return site
	}

	rows, err := h.queries.ListSiteState(ctx)
	if err != nil {
		h.logger.Error("Failed to load shared state", slog.Any("error", err))
		return site
	}
	site.State = stateValues(schema, rows)
	return site
}

func sameSiteContent(a, b sqlc.SiteVersion) bool {
	return a.Css == b.Css &&
		a.Note == b.Note &&
		a.Header == b.Header &&
		a.Board == b.Board &&
		a.Footer == b.Footer
}

// normalizeStateSchema keeps an empty schema as a valid JSON object.
func normalizeStateSchema(schema string) string {
	if strings.TrimSpace(schema) == "" {
		return "{}"
	}
	return schema
}

func postFromRow(row sqlc.Post) webmaster.Post {
	return webmaster.Post{
		ID:     row.ID,
		Author: row.Author,
		Body:   row.Body,
		When:   formatWhen(row.CreatedAt),
	}
}

// formatWhen turns the stored timestamp into something short enough for a
// post header.
func formatWhen(s string) string {
	return webmaster.FormatWhen(s)
}

// recentPosts returns the newest limit posts, oldest first, which is the order
// both the page and the prompt want.
func (h *Handlers) recentPosts(ctx context.Context, limit int64) ([]sqlc.Post, error) {
	rows, err := h.queries.ListRecentPosts(ctx, limit)
	if err != nil {
		return nil, fmt.Errorf("list recent posts: %w", err)
	}
	for i, j := 0, len(rows)-1; i < j; i, j = i+1, j-1 {
		rows[i], rows[j] = rows[j], rows[i]
	}
	return rows, nil
}

// mutate runs one admin mutation and waits for it. It takes h.mu, then starts
// the timeout, so time spent queued behind another mutation does not eat the
// model's budget.
func (h *Handlers) mutate(
	ctx context.Context,
	kind string,
	instruction string,
	triggerPost *sqlc.Post,
) (webmaster.Result, error) {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.touchActivity()
	h.beginRun(kind, nil)
	ctx, cancel := mutationContext(ctx)
	defer cancel()

	var triggers []sqlc.Post
	if triggerPost != nil {
		triggers = []sqlc.Post{*triggerPost}
	}
	res, err := h.mutateHeld(ctx, kind, instruction, triggers)
	h.endRun(err)
	return res, err
}

// mutateHeld is mutate with h.mu already held. triggerPosts is empty for admin
// instructions, which never become posts. A burst arrives as several triggers.
// On a model error the site is left alone; a partial rewrite is worse than no
// rewrite.
func (h *Handlers) mutateHeld(
	ctx context.Context,
	kind string,
	instruction string,
	triggerPosts []sqlc.Post,
) (webmaster.Result, error) {
	current, err := h.queries.CurrentSite(ctx)
	if err != nil {
		return webmaster.Result{}, fmt.Errorf("current site: %w", err)
	}

	rows, err := h.recentPosts(ctx, recentLimit)
	if err != nil {
		return webmaster.Result{}, err
	}
	posts := make([]webmaster.Post, len(rows))
	for i, row := range rows {
		posts[i] = postFromRow(row)
	}

	triggers := make([]webmaster.Post, 0, len(triggerPosts))
	var triggerID sql.NullInt64
	for _, post := range triggerPosts {
		triggers = append(triggers, postFromRow(post))
		triggerID = sql.NullInt64{Int64: post.ID, Valid: true}
	}
	if len(triggers) == 0 {
		triggers = []webmaster.Post{{Author: "admin", Body: instruction}}
	}

	currentSite := h.siteWithState(ctx, current)
	if seed, err := h.queries.SeedSiteVersion(ctx); err == nil {
		currentSite.Plain = sameSiteContent(current, seed)
		lastPlain, err := h.queries.LastVersionMatching(ctx, sqlc.LastVersionMatchingParams{
			Css:    seed.Css,
			Note:   seed.Note,
			Header: seed.Header,
			Board:  seed.Board,
			Footer: seed.Footer,
		})
		if err == nil {
			if id, ok := lastPlain.(int64); ok && id > 0 {
				currentSite.ChangesSincePlain = int(current.ID - id)
			}
		}
	}

	start := time.Now()
	res, mutErr := h.mutator.MutatePosts(ctx, currentSite, posts, triggers)
	elapsed := time.Since(start).Milliseconds()

	if mutErr != nil {
		res.Changed = false
	}

	status := "unchanged"
	var versionID sql.NullInt64
	if res.Changed {
		version, err := h.queries.CreateSiteVersion(ctx, sqlc.CreateSiteVersionParams{
			Css:           res.Site.CSS,
			Note:          res.Site.Note,
			Header:        res.Site.Header,
			Board:         res.Site.Board,
			Footer:        res.Site.Footer,
			StateSchema:   normalizeStateSchema(res.Site.StateSchema),
			TriggerPostID: triggerID,
			Reason:        res.Reason,
		})
		if err != nil {
			mutErr = fmt.Errorf("store site version: %w", err)
		} else {
			versionID = sql.NullInt64{Int64: version.ID, Valid: true}
			h.setVersion(version.ID)
			status = "changed"
		}
	}
	if mutErr != nil {
		status = "error"
	}

	messages, err := json.Marshal(res.Messages)
	if err != nil {
		h.logger.Error("Failed to marshal mutation messages", slog.Any("error", err))
	}
	errorText := ""
	if mutErr != nil {
		errorText = mutErr.Error()
	}

	run, err := h.queries.CreateMutationRun(ctx, sqlc.CreateMutationRunParams{
		Kind:          kind,
		TriggerPostID: triggerID,
		Instruction:   instruction,
		Reason:        res.Reason,
		Status:        status,
		ErrorText:     errorText,
		Messages:      string(messages),
		VersionID:     versionID,
		DurationMs:    elapsed,
	})
	if err != nil {
		h.logger.Error("Failed to store mutation run", slog.Any("error", err))
	} else {
		h.logger.Info("Mutation finished",
			slog.Int64("run_id", run.ID),
			slog.String("kind", kind),
			slog.String("status", status),
			slog.Int64("version_id", versionID.Int64),
			slog.Int64("duration_ms", elapsed),
		)
	}

	return res, mutErr
}

// mutateInBackground queues a post for the worker. Posts that arrive while a
// mutation is running are collected into the next one, so a burst becomes one
// rewrite rather than a long queue.
func (h *Handlers) mutateInBackground(post sqlc.Post) {
	h.touchActivity()
	h.liveMu.Lock()
	h.pending = append(h.pending, post)
	h.liveMu.Unlock()

	select {
	case h.wake <- struct{}{}:
	default:
	}
}

// worker is the single background mutator. It drains the queue, runs one
// mutation for the batch, and goes back to waiting.
func (h *Handlers) worker() {
	for {
		select {
		case <-h.stop:
			return
		case <-h.wake:
		}
		h.runPending()
	}
}

func (h *Handlers) runPending() {
	// Take the mutation lock before looking at the queue. A reset that holds
	// the lock then clears pending, so its posts cannot run after the reset.
	h.mu.Lock()
	defer h.mu.Unlock()

	h.liveMu.Lock()
	batch := h.pending
	h.pending = nil
	h.liveMu.Unlock()

	if len(batch) == 0 {
		return
	}
	if h.overDailyCap(context.Background()) {
		h.setLastError("daily mutation cap reached")
		return
	}
	if len(batch) > maxBatchPosts {
		batch = batch[len(batch)-maxBatchPosts:]
	}

	body := batchBody(batch)
	h.beginRun("post", postAuthors(batch))
	ctx, cancel := context.WithTimeout(context.Background(), mutationTimeout)
	_, err := h.mutateHeld(ctx, "post", body, batch)
	cancel()
	h.endRun(err)

	if err != nil {
		h.logger.Error(
			"Mutation failed",
			slog.Int64("post_id", batch[len(batch)-1].ID),
			slog.Int("batch", len(batch)),
			slog.Any("error", err),
		)
	}
}

const maxBatchPosts = 10

// batchBody turns a burst into one prompt block. The newest post stays last.
func batchBody(posts []sqlc.Post) string {
	var b strings.Builder
	for _, post := range posts {
		author := post.Author
		if author == "" {
			author = "anon"
		}
		fmt.Fprintf(&b, "#%d %s: %s\n", post.ID, author, post.Body)
	}
	return strings.TrimSpace(b.String())
}

func postAuthors(posts []sqlc.Post) []string {
	seen := make(map[string]bool, len(posts))
	authors := make([]string, 0, len(posts))
	for _, post := range posts {
		author := post.Author
		if author == "" {
			author = "anon"
		}
		if seen[author] {
			continue
		}
		seen[author] = true
		authors = append(authors, author)
	}
	return authors
}

// beginRun, endRun and the snapshot helpers keep the live state in one place.
func (h *Handlers) beginRun(kind string, authors []string) {
	h.liveMu.Lock()
	defer h.liveMu.Unlock()
	h.running = &runState{Kind: kind, Authors: authors, Round: 1, Since: time.Now()}
	h.lastError = ""
}

func (h *Handlers) endRun(err error) {
	h.liveMu.Lock()
	defer h.liveMu.Unlock()
	h.running = nil
	if err != nil {
		h.lastError = err.Error()
	}
}

func (h *Handlers) setRunningRound(round int) {
	h.liveMu.Lock()
	if h.running != nil {
		h.running.Round = round
	}
	h.liveMu.Unlock()
}

func (h *Handlers) setVersion(version int64) {
	h.liveMu.Lock()
	h.version = version
	h.liveMu.Unlock()
}

func (h *Handlers) touchActivity() {
	h.liveMu.Lock()
	h.lastActivity = time.Now()
	h.liveMu.Unlock()
}

func (h *Handlers) setLastError(message string) {
	h.liveMu.Lock()
	h.lastError = message
	h.liveMu.Unlock()
}

func (h *Handlers) resetLive(version int64) {
	h.liveMu.Lock()
	h.version = version
	h.pending = nil
	h.running = nil
	h.lastError = ""
	h.lastActivity = time.Now()
	h.liveMu.Unlock()
}

func (h *Handlers) liveSnapshot() (int64, int, *runState, string) {
	h.liveMu.Lock()
	defer h.liveMu.Unlock()
	var running *runState
	if h.running != nil {
		copied := *h.running
		running = &copied
	}
	return h.version, len(h.pending), running, h.lastError
}

func (h *Handlers) currentVersion(ctx context.Context) (int64, error) {
	version, _, _, _ := h.liveSnapshot()
	if version > 0 {
		return version, nil
	}
	site, err := h.queries.CurrentSite(ctx)
	if err != nil {
		return 0, err
	}
	h.setVersion(site.ID)
	return site.ID, nil
}

const (
	rewriteCooldown = 3 * time.Second
	stateCooldown   = 250 * time.Millisecond
	idleHealAfter   = 20 * time.Minute
)

// healer brings the board back to its plain seed after a quiet spell, so the
// next visitor gets the reveal again. Posts stay on the board.
func (h *Handlers) healer() {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-h.stop:
			return
		case <-ticker.C:
			h.healIfIdle(context.Background())
		}
	}
}

func (h *Handlers) healIfIdle(ctx context.Context) {
	h.liveMu.Lock()
	idle := time.Since(h.lastActivity)
	busy := h.running != nil || len(h.pending) > 0
	h.liveMu.Unlock()

	if busy || idle < idleHealAfter {
		return
	}
	if _, err := h.heal(ctx); err != nil {
		h.logger.Error("Failed to heal the board", slog.Any("error", err))
	}
}

// heal copies the seed version forward. It is a restore, not a model call.
func (h *Handlers) heal(ctx context.Context) (bool, error) {
	h.mu.Lock()
	defer h.mu.Unlock()

	current, err := h.queries.CurrentSite(ctx)
	if err != nil {
		return false, fmt.Errorf("current site: %w", err)
	}
	seed, err := h.queries.SeedSiteVersion(ctx)
	if err != nil {
		return false, fmt.Errorf("seed site: %w", err)
	}
	if current.Css == seed.Css &&
		current.Note == seed.Note &&
		current.Header == seed.Header &&
		current.Board == seed.Board &&
		current.Footer == seed.Footer {
		h.touchActivity()
		return false, nil
	}

	if err := h.queries.DeleteSiteState(ctx); err != nil {
		return false, fmt.Errorf("clear shared state: %w", err)
	}
	healed, err := h.queries.CreateSiteVersion(ctx, sqlc.CreateSiteVersionParams{
		Css:         seed.Css,
		Note:        seed.Note,
		Header:      seed.Header,
		Board:       seed.Board,
		Footer:      seed.Footer,
		StateSchema: normalizeStateSchema(seed.StateSchema),
		Reason:      "the board went quiet",
	})
	if err != nil {
		return false, fmt.Errorf("store healed version: %w", err)
	}
	h.setVersion(healed.ID)
	h.touchActivity()
	h.logger.Info("Board returned to the seed", slog.Int64("version_id", healed.ID))
	return true, nil
}

// allowRewrite stops a script on the model's page from triggering a rewrite
// from every open tab in a loop. The post is still stored; only the costly
// model call is skipped.
func (h *Handlers) allowStateWrite(name, key string) bool {
	now := time.Now()
	slot := name + "\x00" + key
	h.liveMu.Lock()
	defer h.liveMu.Unlock()
	if last, ok := h.lastStateAt[slot]; ok && now.Sub(last) < stateCooldown {
		return false
	}
	h.lastStateAt[slot] = now
	return true
}

func (h *Handlers) allowRewrite(name string) bool {
	now := time.Now()
	h.liveMu.Lock()
	defer h.liveMu.Unlock()
	if last, ok := h.lastRewriteAt[name]; ok && now.Sub(last) < rewriteCooldown {
		return false
	}
	h.lastRewriteAt[name] = now
	return true
}

// overDailyCap reports whether the cost kill switch has tripped.
func (h *Handlers) overDailyCap(ctx context.Context) bool {
	if h.cfg.MaxMutationsPerDay <= 0 {
		return false
	}
	count, err := h.queries.CountMutationsSince(ctx, utcDayStart())
	if err != nil {
		h.logger.Error("Failed to count mutations", slog.Any("error", err))
		return false
	}
	return count >= int64(h.cfg.MaxMutationsPerDay)
}

func utcDayStart() string {
	return time.Now().UTC().Format("2006-01-02T00:00:00.000Z")
}

// mutationContext detaches the work from the request, so a refresh does not
// cancel a mutation halfway, then bounds it.
func mutationContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), mutationTimeout)
}

func truncate(s string, limit int) string {
	runes := []rune(s)
	if len(runes) <= limit {
		return s
	}
	return string(runes[:limit])
}
