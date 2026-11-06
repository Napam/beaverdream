package handlers

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/Napam/beaverdream/db/generated/sqlc"
	"github.com/Napam/beaverdream/lib/env"
	"github.com/Napam/beaverdream/lib/webmaster"
	"github.com/Napam/beaverdream/web/pages"
)

const (
	// roomLimit is the smallest number of turns the model reads back. The
	// query limit is the larger of this and the turn backstop, so the log
	// never drops its oldest line before compaction folds it in. A sliding
	// start would lose those turns and the cached prompt prefix every turn.
	roomLimit = 2000
	// defaultCompactAfterTurns is the backstop for many very short turns.
	defaultCompactAfterTurns = 10_000
	// compactMaxTurns caps one summarizer input, so a large backlog is folded
	// in several mutations instead of one call that overflows the model.
	compactMaxTurns = 1000
	// compactMaxChunks bounds the summarizer calls in one mutation.
	compactMaxChunks = 8
	// summarizeTimeout is separate from the mutation budget so a slow summary
	// cannot starve the rewrite that follows it.
	summarizeTimeout = 5 * time.Minute
	// draftEvery bounds how often a streaming round repaints the admin panel.
	// A fast model sends dozens of chunks a second; the panel needs a few.
	draftEvery = 100 * time.Millisecond
)

// mutationTimeout bounds one model call sequence. It is a backstop: a quiet
// stream is cut by the idle limits long before. A whole legal page is about
// 17 minutes of generation, and a few rounds follow it. TURN_TIMEOUT
// overrides it for a provider that is slower still.
var mutationTimeout = env.GetEnvPositiveDuration("TURN_TIMEOUT", time.Hour)

// Config holds the environment knobs the handlers enforce.
type Config struct {
	// AdminToken is the admin password. It signs the remembered login cookie
	// and still works in the X-Admin-Token header or a form field for scripts.
	AdminToken string
	// AdminPort is the port that serves /admin, so the login cookie lives on
	// an origin the board's scripts never run on. The board's port redirects
	// there. Empty means the port after Port.
	AdminPort string
	// AdminURL is the public URL of the admin panel, for a proxy with its own
	// admin hostname. When set, the board redirects /admin there instead of to
	// AdminPort on the request's host. Empty keeps the host and AdminPort.
	AdminURL string
	// Port is the board's port, used for the link back from the admin pages.
	Port string
	// CompactAfterTokens is the estimated whole-prompt size that triggers
	// compaction. Zero uses webmaster.CompactAfterTokens.
	CompactAfterTokens int
	// CompactAfterTurns is the turn-count backstop for very short turns.
	// Zero uses the default.
	CompactAfterTurns int
	// DatastarSrc is the script path the board loads Datastar from, content
	// hashed in production. Empty uses webmaster.DatastarPath.
	DatastarSrc string
	// ThreeSrc is the path the import map gives "three", content hashed in
	// production. Empty uses webmaster.ThreePath.
	ThreeSrc string
	// ThreeAddonsSrc is the path the import map gives "three/addons". Empty
	// uses webmaster.ThreeAddonsPath.
	ThreeAddonsSrc string
}

// runState is what the live endpoint and the admin panel show while a
// mutation is in flight. Messages is the conversation so far, the prompt and
// every finished round, and Draft is the round the model is writing now, so
// the turn inspector can follow the turn before it is stored. /version is
// public and carries this as JSON, so model text stays out of it.
type runState struct {
	Kind       string              `json:"kind"`
	Authors    []string            `json:"authors,omitempty"`
	Round      int                 `json:"round"`
	Since      time.Time           `json:"since"`
	TurnID     int64               `json:"turn_id,omitempty"`
	RoundSince time.Time           `json:"-"`
	Messages   []webmaster.Message `json:"-"`
	Draft      *webmaster.Draft    `json:"-"`
	RoundDone  bool                `json:"-"`
	// Finishing is true once the model is done and the turn is being stored.
	// A stop after that point would change nothing but the label.
	Finishing bool `json:"-"`
}

// Handlers serves the board and the admin panel.
type Handlers struct {
	logger  *slog.Logger
	db      *sql.DB
	queries *sqlc.Queries
	mutator *webmaster.Mutator
	cfg     Config

	// hub tells the open pages' live streams what changed. Every write to
	// the board publishes; the streams do the reading.
	hub *hub

	// mu serializes mutations. One process, one site: a second mutation
	// waiting beats two of them writing versions over each other.
	mu sync.Mutex

	// stateMu serializes shared-state writes, so two clicks cannot read the
	// same counter and write the same next value.
	stateMu sync.Mutex

	// journalClears counts admin clears of a journal key. A stream that has
	// seen fewer tells its page to read its feeds again, because a clear can
	// leave the newest row id where it was and the page would never notice.
	journalClears atomic.Int64
	// holdingsGen counts writes to state_holdings; holders caches the top
	// lists for one generation.
	holdingsGen atomic.Int64
	holders     holdersCache

	// The worker coalesces posts that arrive while a mutation is running, so
	// a burst becomes one rewrite instead of a queue of them.
	liveMu  sync.Mutex
	version int64
	pending []sqlc.Post
	// waiting holds the admin instructions that have been sent and are
	// waiting for the mutation lock, so the panel can show them queued.
	waiting       []string
	running       *runState
	lastError     string
	lastRewriteAt map[string]time.Time
	lastStateAt   map[string]time.Time
	// stateCooldown is the gap between one browser's writes to one key.
	stateCooldown time.Duration
	journalBucket map[string]*journalBucket
	// alertSeen collapses repeated browser errors into one event each.
	alertSeen map[string]time.Time
	// draftQueued is true while a draft publish waits out draftEvery.
	draftQueued bool
	// cancelRun stops the model call in flight; stopped counts the resets
	// that have not finished yet, so overlapping resets keep work paused.
	// Both guarded by liveMu.
	cancelRun context.CancelCauseFunc
	stopped   int
	wake      chan struct{}
	stop      chan struct{}
	closeOnce sync.Once
	workerWG  sync.WaitGroup

	compactAfterTokens int
	compactAfterTurns  int
	// pageOverBudget makes the "page alone is too big" log fire once per
	// stretch. Guarded by mu.
	pageOverBudget bool
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
		hub:           newHub(),
		lastRewriteAt: make(map[string]time.Time),
		lastStateAt:   make(map[string]time.Time),
		stateCooldown: stateCooldown,
		journalBucket: make(map[string]*journalBucket),
		alertSeen:     make(map[string]time.Time),
		wake:          make(chan struct{}, 1),
		stop:          make(chan struct{}),

		compactAfterTokens: cfg.CompactAfterTokens,
		compactAfterTurns:  cfg.CompactAfterTurns,
	}

	if h.cfg.Port == "" {
		h.cfg.Port = "6930"
	}
	if h.cfg.AdminPort == "" {
		h.cfg.AdminPort = "6932"
	}
	if h.compactAfterTokens <= 0 {
		h.compactAfterTokens = webmaster.CompactAfterTokens
	}
	if h.compactAfterTurns <= 0 {
		h.compactAfterTurns = defaultCompactAfterTurns
	}
	// Compaction treats the budget as the whole prompt. A page cap that fills
	// it switches compaction off once the page grows that large.
	if pageTokens := webmaster.PageBytes() / 4; pageTokens >= h.compactAfterTokens {
		logger.Warn("MAX_PAGE_BYTES fills the compaction budget; compaction will stop",
			slog.Int("page_tokens", pageTokens),
			slog.Int("budget_tokens", h.compactAfterTokens),
		)
	}
	if site, err := queries.CurrentSite(context.Background()); err == nil {
		h.version = site.ID
	}
	// A turn that was running when the process died never finished. Mark it
	// so it cannot be read back as something the webmaster said.
	if err := queries.FailRunningTurns(context.Background()); err != nil {
		logger.Error("Failed to close interrupted turns", slog.Any("error", err))
	}
	mutator.OnRound = h.setRunningRound
	mutator.OnRoundDone = h.logRoundDone
	mutator.OnDraft = h.setDraft
	mutator.OnMessage = h.addRunMessage
	h.workerWG.Add(1)
	go func() {
		defer h.workerWG.Done()
		h.worker()
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
		Version:       row.ID,
		CSS:           row.Css,
		HTML:          row.Html,
		StateSchema:   row.StateSchema,
		JournalSchema: row.JournalSchema,
	}
}

// siteWithState attaches the model-declared shared state and journal to a
// version before it is rendered or sent to the model.
func (h *Handlers) siteWithState(ctx context.Context, row sqlc.SiteVersion) webmaster.Site {
	site := siteFromRow(row)
	site.Journal = h.journalState(ctx, row.JournalSchema)
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
		a.Html == b.Html &&
		normalizeSchema(a.StateSchema) == normalizeSchema(b.StateSchema) &&
		normalizeSchema(a.JournalSchema) == normalizeSchema(b.JournalSchema)
}

// normalizeSchema keeps an empty schema as a valid JSON object.
func normalizeSchema(schema string) string {
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

// logRead logs a query that failed on a request's context, unless the client
// left first. A closed tab or a reload cancels its streams and reads, which is
// the page working as intended, not a server fault. A deadline still logs: no
// read has one today, and a slow query should stay visible if one is added.
func (h *Handlers) logRead(message string, err error) {
	if errors.Is(err, context.Canceled) {
		return
	}
	h.logger.Error(message, slog.Any("error", err))
}

// createdPost adapts the insert row to the view model the rest of the handler
// already speaks.
func createdPost(row sqlc.CreatePostRow) sqlc.Post {
	return sqlc.Post(row)
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
	var triggers []sqlc.Post
	if triggerPost != nil {
		triggers = []sqlc.Post{*triggerPost}
	}
	return h.mutateBatch(ctx, kind, instruction, triggers)
}

// mutateBatch is mutate for a set of triggers: a coalesced burst, or the posts
// a retried turn answered.
func (h *Handlers) mutateBatch(
	ctx context.Context,
	kind string,
	instruction string,
	triggers []sqlc.Post,
) (webmaster.Result, error) {
	if kind == "admin" {
		h.addWaiting(instruction)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if kind == "admin" {
		h.removeWaiting(instruction)
	}
	return h.mutateLocked(ctx, kind, instruction, triggers)
}

// addWaiting and removeWaiting show an admin instruction as queued while it
// waits for the mutation lock behind a running turn.
func (h *Handlers) addWaiting(instruction string) {
	h.liveMu.Lock()
	h.waiting = append(h.waiting, instruction)
	h.liveMu.Unlock()
	h.hub.publish(topicBusy)
}

func (h *Handlers) removeWaiting(instruction string) {
	h.liveMu.Lock()
	for i, text := range h.waiting {
		if text == instruction {
			h.waiting = append(h.waiting[:i], h.waiting[i+1:]...)
			break
		}
	}
	h.liveMu.Unlock()
	h.hub.publish(topicBusy)
}

// mutateLocked runs one mutation with h.mu held. A caller that has to read the
// rows behind the triggers takes the lock itself, so a reset cannot slip
// between the read and the run.
func (h *Handlers) mutateLocked(
	ctx context.Context,
	kind string,
	instruction string,
	triggers []sqlc.Post,
) (webmaster.Result, error) {
	ctx, cancel := mutationContext(ctx)
	defer cancel(nil)
	if !h.startRun(cancel) {
		return webmaster.Result{}, errors.New("the board is resetting")
	}
	defer h.clearRun()

	h.beginRun(kind, postAuthors(triggers))
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
	batchStart := time.Now()
	current, err := h.queries.CurrentSite(ctx)
	if err != nil {
		return webmaster.Result{}, fmt.Errorf("current site: %w", err)
	}

	triggers := make([]webmaster.Post, 0, len(triggerPosts))
	for _, post := range triggerPosts {
		triggers = append(triggers, postFromRow(post))
	}
	if len(triggers) == 0 {
		triggers = []webmaster.Post{{Author: "admin", Body: instruction}}
	}

	// Reserving the turn before reading the log is what makes the transcript
	// honest: posts that arrive during this run get a higher id, so the next
	// turn sees them after this one, where they belong.
	//
	// The author column names who the turn answers, so the admin panel can
	// tell turns apart after they finish.
	turnBody := ""
	if kind == "admin" {
		turnBody = instruction
	}
	authors := []string{"admin"}
	if len(triggerPosts) > 0 {
		authors = postAuthors(triggerPosts)
	}
	turn, err := h.queries.StartTurn(ctx, sqlc.StartTurnParams{
		Kind:   webmaster.TurnWebmaster,
		Author: strings.Join(authors, ", "),
		Body:   turnBody,
	})
	if err != nil {
		return webmaster.Result{}, fmt.Errorf("start turn: %w", err)
	}
	h.setRunningTurn(turn.ID)

	postIDs := make([]int64, 0, len(triggers))
	for _, post := range triggers {
		if post.ID > 0 {
			postIDs = append(postIDs, post.ID)
		}
	}
	startAttrs := []any{
		slog.Int64("turn_id", turn.ID),
		slog.String("kind", kind),
		slog.Int("posts", len(postIDs)),
		slog.Any("post_ids", postIDs),
		slog.Any("authors", authors),
	}
	if kind == "admin" {
		startAttrs = append(startAttrs, slog.String("instruction", instruction))
	}
	h.logger.Info("Mutation started", startAttrs...)

	// A turn that never reaches the finish transaction must not stay
	// "running": the next prompt would treat it as a turn in flight forever.
	finished := false
	defer func() {
		if finished {
			return
		}
		closeCtx, cancel := context.WithTimeout(
			context.WithoutCancel(ctx),
			10*time.Second,
		)
		defer cancel()
		if err := h.queries.FailTurn(closeCtx, sqlc.FailTurnParams{
			ErrorText: "the mutation did not finish",
			ID:        turn.ID,
		}); err != nil {
			h.logger.Error(
				"Failed to close an unfinished turn",
				slog.Int64("turn_id", turn.ID),
				slog.Any("error", err),
			)
		}
	}()

	currentSite := h.siteWithState(ctx, current)
	currentSite.JournalRulesUsed = h.journalRulesUsed(ctx)
	currentSite.Holdings = h.holdingTotals(ctx)
	if seed, err := h.queries.SeedSiteVersion(ctx); err == nil {
		currentSite.Plain = sameSiteContent(current, seed)
	}
	if count, err := h.queries.CountChangedSinceHeal(ctx); err == nil {
		currentSite.ChangesSinceHeal = int(count)
	}

	room, err := h.roomFor(ctx, turn.ID)
	if err != nil {
		return webmaster.Result{}, err
	}

	start := time.Now()
	res, mutErr := h.mutator.MutatePosts(ctx, currentSite, room, triggers)
	elapsed := time.Since(start).Milliseconds()
	h.setFinishing()

	if mutErr != nil {
		res.Changed = false
	}

	status := "silent"
	switch {
	case res.Changed:
		status = "changed"
	case res.Done:
		status = "kept"
	}
	if mutErr != nil {
		status = "error"
	}

	// A turn that ran out of rounds without done is the one case where the
	// reason line is the only clue, so say what the last round was doing.
	if mutErr == nil && !res.Done {
		h.logger.Warn("Mutation ended without done",
			slog.Int64("turn_id", turn.ID),
			slog.Int("rounds", len(res.Rounds)),
			slog.Int("max_rounds", h.mutator.MaxRounds()),
			slog.String("status", status),
			slog.String("reason", res.Reason),
			slog.Bool("changed", res.Changed),
			slog.String("last_round", lastRoundLine(res)),
		)
	}

	// Notes survive a turn that changed nothing, and a failed turn: they are
	// the webmaster's memory, not part of the page.
	notes := room.Notes
	if res.Notes != nil {
		notes = *res.Notes
	}

	messagesJSON := "[]"
	if data, err := json.Marshal(res.Messages); err != nil {
		h.logger.Error("Failed to marshal mutation messages", slog.Any("error", err))
	} else {
		messagesJSON = string(data)
	}
	errorText := ""
	if mutErr != nil {
		errorText = mutErr.Error()
		if errors.Is(context.Cause(ctx), errAdminStop) {
			errorText = errAdminStop.Error()
			mutErr = errAdminStop
		}
	}

	// The version and the finished turn land together, so a reader never sees
	// a "changed" turn without its page, or a page with no explanation. The
	// write uses a detached context: on a model timeout the mutation context
	// is already dead, and the record of the failure still has to land.
	finishCtx, cancel := context.WithTimeout(
		context.WithoutCancel(ctx),
		10*time.Second,
	)
	defer cancel()

	var versionID sql.NullInt64
	txErr := h.inTx(finishCtx, func(q *sqlc.Queries) error {
		if res.Changed {
			version, err := insertVersion(finishCtx, q, sqlc.CreateSiteVersionParams{
				Css:           res.Site.CSS,
				Html:          res.Site.HTML,
				StateSchema:   normalizeSchema(res.Site.StateSchema),
				JournalSchema: normalizeSchema(res.Site.JournalSchema),
				TurnID:        sql.NullInt64{Int64: turn.ID, Valid: true},
				Reason:        res.Reason,
			})
			if err != nil {
				return err
			}
			versionID = sql.NullInt64{Int64: version.ID, Valid: true}
		}
		return q.FinishTurn(finishCtx, sqlc.FinishTurnParams{
			Said:       res.Reason,
			Changes:    res.Changes,
			Notes:      notes,
			Status:     status,
			ErrorText:  errorText,
			Messages:   messagesJSON,
			DurationMs: elapsed,
			ID:         turn.ID,
		})
	})
	var postToVersion time.Duration
	if txErr != nil {
		mutErr = fmt.Errorf("store turn: %w", txErr)
		status = "error"
	} else {
		finished = true
		// The conversation is stored now, so the inspector can drop the live
		// rounds before compaction runs.
		h.finishRunningTurn()
		if versionID.Valid {
			h.setVersion(versionID.Int64)
			if oldest, ok := oldestPostTime(triggerPosts); ok {
				postToVersion = time.Since(oldest)
			}
			if what := webmaster.PostsSwallowedBy(res.Site); what != "" {
				h.logger.Warn("The new page hides its posts",
					slog.Int64("version_id", versionID.Int64),
					slog.String("cause", what),
				)
			}
		}
	}

	// Compaction runs after the turn has landed, so a slow summary delays the
	// next turn rather than this one's page. It is best effort: a failed
	// summary must not take the mutation down with it, and the same turns are
	// retried next time. The page the next turn reads counts against the
	// budget. The mutation deadline may already be spent, so compaction runs
	// on its own clock; summarize bounds each model call.
	//
	// A reset cancels this turn and then waits for the lock. Summarizing turns
	// the reset is about to delete would put a model call back in that wait,
	// so a canceled turn skips it.
	nextPage := currentSite
	if res.Changed {
		nextPage = res.Site
	}
	compactStart := time.Now()
	if ctx.Err() != context.Canceled {
		if err := h.compactRoom(
			context.WithoutCancel(ctx),
			turn.ID,
			int64(webmaster.PageRunes(nextPage)),
		); err != nil {
			h.logger.Error("Failed to compact the room", slog.Any("error", err))
		}
	}
	compactMs := time.Since(compactStart).Milliseconds()

	attrs := []any{
		slog.Int64("turn_id", turn.ID),
		slog.String("kind", kind),
		slog.String("status", status),
		slog.Int64("version_id", versionID.Int64),
		slog.Int64("duration_ms", elapsed),
		slog.String("said", res.Reason),
		slog.String("changes", res.Changes),
	}
	attrs = append(attrs, roundAttrs(res)...)
	attrs = append(attrs,
		slog.Int("summary_runes", utf8.RuneCountInString(room.Summary)),
		slog.Int("notes_runes", utf8.RuneCountInString(room.Notes)),
		slog.Int("log_runes", webmaster.LogRunes(room.Log, triggerIDs(triggers))),
		slog.Int("page_runes", webmaster.PageRunes(currentSite)),
		slog.Int64("compact_ms", compactMs),
	)
	if oldest, ok := oldestPostTime(triggerPosts); ok {
		attrs = append(attrs,
			slog.Int64("queue_wait_ms", batchStart.Sub(oldest).Milliseconds()),
			slog.Int64("post_to_version_ms", postToVersion.Milliseconds()),
		)
	}
	h.logger.Info("Mutation finished", attrs...)

	return res, mutErr
}

// roundAttrs sums the rounds of one mutation for the finish log. A done-only
// last round after earlier rounds is a round spent on one sentence, which the
// prompt asks the model to avoid.
func roundAttrs(res webmaster.Result) []any {
	var prompt, cached, rejected, truncated int64
	for _, round := range res.Rounds {
		prompt += round.PromptTokens
		cached += round.CachedTokens
		rejected += int64(round.Rejected)
		if round.FinishReason == "length" {
			truncated++
		}
	}
	doneOnly := false
	if n := len(res.Rounds); n > 1 {
		last := res.Rounds[n-1].Tools
		doneOnly = len(last) == 1 && last[0] == "done"
	}
	cacheRatio := 0.0
	if prompt > 0 {
		cacheRatio = math.Round(float64(cached)/float64(prompt)*100) / 100
	}
	return []any{
		slog.Int("rounds", len(res.Rounds)),
		slog.Int("done_round", res.DoneRound),
		slog.Bool("done", res.Done),
		slog.Bool("last_round_done_only", doneOnly),
		slog.Int64("rejected", rejected),
		slog.Int64("truncated_rounds", truncated),
		slog.Int64("prompt_tokens", prompt),
		slog.Float64("cached_ratio", cacheRatio),
	}
}

// lastRoundLine names what the final round called and what came back, so a
// turn that ran out of rounds says what the model was busy with.
func lastRoundLine(res webmaster.Result) string {
	if len(res.Rounds) == 0 {
		return ""
	}
	return strings.Join(res.Rounds[len(res.Rounds)-1].Outcomes, " | ")
}

// triggerIDs is the set of posts a turn answers, which the log skips.
func triggerIDs(triggers []webmaster.Post) map[int64]bool {
	ids := make(map[int64]bool, len(triggers))
	for _, trigger := range triggers {
		ids[trigger.ID] = true
	}
	return ids
}

// oldestPostTime is when the first post of a batch was written. Admin turns
// have no posts.
func oldestPostTime(posts []sqlc.Post) (time.Time, bool) {
	var oldest time.Time
	for _, post := range posts {
		created, err := time.Parse(time.RFC3339Nano, post.CreatedAt)
		if err != nil {
			continue
		}
		if oldest.IsZero() || created.Before(oldest) {
			oldest = created
		}
	}
	return oldest, !oldest.IsZero()
}

// compactRoom folds the uncompacted history before the turn id before into
// one summary when it reaches either the message or the token threshold. It
// measures the whole stretch, then folds it in chunks until it is under the
// threshold. After it runs, the next prompt starts from that summary and only
// the turns after it. The caller runs it once a turn has finished, so the
// finished turn itself stays in the next log.
//
// The token budget covers the whole prompt: the transcript plus pageRunes, the
// size of the page the next turn reads. If the page alone fills the budget,
// compacting cannot help, so it logs once and leaves the history alone instead
// of summarizing on every turn.
func (h *Handlers) compactRoom(
	ctx context.Context,
	before int64,
	pageRunes int64,
) error {
	if pageRunes/4 >= int64(h.compactAfterTokens) {
		if !h.pageOverBudget {
			h.pageOverBudget = true
			h.logger.Info("The page alone exceeds the compaction budget; not compacting",
				slog.Int64("page_tokens", pageRunes/4),
				slog.Int("budget_tokens", h.compactAfterTokens),
			)
		}
		return nil
	}
	h.pageOverBudget = false

	from, previous, err := h.latestSummary(ctx)
	if err != nil {
		return err
	}
	upTo := before - 1
	// Queued posts have not had their turn yet; the next turn shows them as
	// NEW POST. Leave them out of the summary so they are not answered twice.
	if pending := h.oldestPendingID(); pending > 0 && pending-1 < upTo {
		upTo = pending - 1
	}
	if upTo <= from {
		return nil
	}

	for chunks := 0; chunks < compactMaxChunks; chunks++ {
		stats, err := h.queries.SummaryStats(ctx, sqlc.SummaryStatsParams{
			ID:   from,
			ID_2: upTo,
		})
		if err != nil {
			return fmt.Errorf("measure the room: %w", err)
		}
		if stats.Turns == 0 {
			return nil
		}
		chars := numericInt64(stats.Chars)
		if stats.Turns < int64(h.compactAfterTurns) &&
			(chars+pageRunes)/4 < int64(h.compactAfterTokens) {
			return nil
		}

		rows, err := h.queries.ListTurnsForSummary(ctx, sqlc.ListTurnsForSummaryParams{
			ID:    from,
			ID_2:  upTo,
			Limit: compactMaxTurns,
		})
		if err != nil {
			return fmt.Errorf("list turns to compact: %w", err)
		}
		if len(rows) == 0 {
			return nil
		}

		turns := make([]webmaster.Turn, len(rows))
		for i, row := range rows {
			turns[i] = webmaster.Turn{
				ID:      row.ID,
				Kind:    row.Kind,
				Author:  row.Author,
				Body:    row.Body,
				Said:    row.Said,
				Changes: row.Changes,
				Status:  row.Status,
			}
		}

		text, err := h.summarize(ctx, previous, turns)
		if err != nil {
			return err
		}
		through := rows[len(rows)-1].ID
		if _, err := h.queries.CreateSummary(ctx, sqlc.CreateSummaryParams{
			ThroughTurnID: through,
			Text:          text,
		}); err != nil {
			return fmt.Errorf("store summary: %w", err)
		}

		h.logger.Info("Room compacted",
			slog.Int64("through_turn_id", through),
			slog.Int("turns", len(turns)),
			slog.Int64("estimated_tokens", (chars+pageRunes)/4),
		)

		from = through
		previous = text
		if len(rows) < compactMaxTurns {
			return nil
		}
	}
	return nil
}

// summarize gives the summarizer its own budget. It must not inherit an
// almost-expired mutation deadline, even though it still runs under h.mu.
func (h *Handlers) summarize(
	ctx context.Context,
	previous string,
	turns []webmaster.Turn,
) (string, error) {
	summaryCtx, cancel := context.WithTimeout(
		context.WithoutCancel(ctx),
		summarizeTimeout,
	)
	defer cancel()
	text, err := h.mutator.Summarize(summaryCtx, previous, turns)
	if err != nil {
		return "", fmt.Errorf("summarize: %w", err)
	}
	return text, nil
}

// numericInt64 reads an aggregate that sqlc types as interface{}.
func numericInt64(value any) int64 {
	switch number := value.(type) {
	case int64:
		return number
	case int:
		return int64(number)
	case float64:
		return int64(number)
	}
	return 0
}

// latestSummary returns the compacted history and the last turn it covers.
func (h *Handlers) latestSummary(ctx context.Context) (int64, string, error) {
	row, err := h.queries.GetLatestSummary(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, "", nil
	}
	if err != nil {
		return 0, "", fmt.Errorf("load summary: %w", err)
	}
	return row.ThroughTurnID, row.Text, nil
}

// roomFor loads the conversation a turn is answering: the compacted history,
// the turns after it, and the webmaster's latest notes.
func (h *Handlers) roomFor(ctx context.Context, before int64) (webmaster.Room, error) {
	from, summary, err := h.latestSummary(ctx)
	if err != nil {
		return webmaster.Room{}, err
	}

	rows, err := h.queries.ListRoomLog(ctx, sqlc.ListRoomLogParams{
		ID:    from,
		ID_2:  before,
		Limit: int64(max(roomLimit, h.compactAfterTurns)),
	})
	if err != nil {
		return webmaster.Room{}, fmt.Errorf("list room log: %w", err)
	}
	turns := make([]webmaster.Turn, len(rows))
	for i, row := range rows {
		turns[len(rows)-1-i] = webmaster.Turn{
			ID:      row.ID,
			Kind:    row.Kind,
			Author:  row.Author,
			Body:    row.Body,
			Said:    row.Said,
			Changes: row.Changes,
			Status:  row.Status,
		}
		if row.VersionID.Valid {
			turns[len(rows)-1-i].VersionID = row.VersionID.Int64
		}
	}

	notes, err := h.queries.LatestNotes(ctx)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return webmaster.Room{}, fmt.Errorf("load notes: %w", err)
	}
	return webmaster.Room{Summary: summary, Log: turns, Notes: notes}, nil
}

// mutateInBackground queues a post for the worker. Posts that arrive while a
// mutation is running are collected into the next one, so a burst becomes one
// rewrite rather than a long queue.
func (h *Handlers) mutateInBackground(post sqlc.Post) {
	h.liveMu.Lock()
	h.pending = append(h.pending, post)
	pending := len(h.pending)
	h.liveMu.Unlock()
	h.hub.publish(topicBusy)

	h.logger.Info("Post queued for a rewrite",
		slog.Int64("post_id", post.ID),
		slog.String("author", post.Author),
		slog.Int("pending", pending),
	)

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
	if h.stopped > 0 {
		h.liveMu.Unlock()
		return
	}
	batch := h.pending
	h.pending = nil
	h.liveMu.Unlock()

	// A batch that turns out empty or capped never starts a run, so the
	// spinner hears about the drained queue here. A batch that runs is
	// published by beginRun, without a not-busy flash in between.
	batch = h.liveOnly(context.Background(), batch)
	if len(batch) == 0 {
		h.hub.publish(topicBusy)
		return
	}
	if len(batch) > maxBatchPosts {
		dropped := batch[:len(batch)-maxBatchPosts]
		droppedIDs := make([]int64, len(dropped))
		for i, post := range dropped {
			droppedIDs[i] = post.ID
		}
		h.logger.Warn("Queued posts dropped from the batch",
			slog.Int("dropped", len(dropped)),
			slog.Any("post_ids", droppedIDs),
			slog.Int("kept", maxBatchPosts),
		)
		batch = batch[len(batch)-maxBatchPosts:]
	}

	batchIDs := make([]int64, len(batch))
	for i, post := range batch {
		batchIDs[i] = post.ID
	}
	h.logger.Info("Mutation batch ready",
		slog.Int("posts", len(batch)),
		slog.Any("post_ids", batchIDs),
	)

	ctx, cancel := runContext(context.Background())
	if !h.startRun(cancel) {
		cancel(nil)
		return
	}
	h.beginRun("post", postAuthors(batch))
	_, err := h.mutateHeld(ctx, "post", "", batch)
	cancel(nil)
	h.clearRun()
	h.endRun(err)

	// A reset cancels the turn in flight on purpose, so that is not a failure.
	if err != nil && !errors.Is(err, context.Canceled) {
		h.logger.Error(
			"Mutation failed",
			slog.Int64("post_id", batch[len(batch)-1].ID),
			slog.Int("batch", len(batch)),
			slog.Any("error", err),
		)
	}
}

const maxBatchPosts = 10

// liveOnly drops queued posts that a reset deleted. Post ids are rewound by a
// reset, so an id alone proves nothing: the stored row must match the queued
// post on id, author, body and creation time. A different post that reuses the
// id has a different timestamp, or different text. Needs h.mu.
func (h *Handlers) liveOnly(ctx context.Context, batch []sqlc.Post) []sqlc.Post {
	kept := make([]sqlc.Post, 0, len(batch))
	for _, post := range batch {
		stored, err := h.queries.GetPost(ctx, post.ID)
		if err != nil {
			if !errors.Is(err, sql.ErrNoRows) {
				h.logger.Error("Failed to check a queued post",
					slog.Int64("post_id", post.ID), slog.Any("error", err))
			}
			continue
		}
		if stored != post {
			continue
		}
		kept = append(kept, post)
	}
	return kept
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
	now := time.Now()
	h.running = &runState{Kind: kind, Authors: authors, Round: 1, Since: now, RoundSince: now}
	h.lastError = ""
	h.liveMu.Unlock()
	h.hub.publish(topicBusy)
}

func (h *Handlers) endRun(err error) {
	h.liveMu.Lock()
	h.running = nil
	if err != nil {
		h.lastError = err.Error()
	}
	h.liveMu.Unlock()
	// The run's turn and any summary have landed by now, version or not.
	h.hub.publish(topicBusy | topicTurns)
}

// startRun remembers how to cancel the model call. It returns false when a
// reset has already stopped work, so the caller drops its batch instead of
// starting a turn the reset would have to wait for.
func (h *Handlers) startRun(cancel context.CancelCauseFunc) bool {
	h.liveMu.Lock()
	defer h.liveMu.Unlock()
	if h.stopped > 0 {
		return false
	}
	h.cancelRun = cancel
	return true
}

func (h *Handlers) clearRun() {
	h.liveMu.Lock()
	h.cancelRun = nil
	h.liveMu.Unlock()
}

// stopWork cancels the model call in flight and drops the queue, so a reset
// does not wait minutes for a turn it is about to delete. Nothing new starts
// until every reset that called this has resumed.
func (h *Handlers) stopWork() {
	h.liveMu.Lock()
	h.stopped++
	h.pending = nil
	cancel := h.cancelRun
	h.cancelRun = nil
	h.liveMu.Unlock()
	if cancel != nil {
		cancel(errBoardReset)
	}
	h.hub.publish(topicBusy)
}

// errBoardReset and errAdminStop are the causes a turn's context carries when
// it is canceled on purpose.
var (
	errBoardReset = errors.New("the board was reset")
	errAdminStop  = errors.New("stopped by admin")
)

// stopTurn cancels the running turn when it is the one the admin looked at.
// A stop that arrives after the turn ended names an old id and does nothing,
// so it cannot cancel the next turn. It never takes h.mu, which the running
// turn holds, and it leaves the queue alone: the posts still get their turn.
func (h *Handlers) stopTurn(turnID int64) bool {
	h.liveMu.Lock()
	cancel := h.cancelRun
	match := h.running != nil && h.running.TurnID == turnID && turnID != 0 &&
		!h.running.Finishing
	h.liveMu.Unlock()
	if !match || cancel == nil {
		return false
	}
	cancel(errAdminStop)
	return true
}

// resumeWork lets the worker run again. A post that arrived during the reset
// is still queued, and its wake was used up, so it gets one now.
func (h *Handlers) resumeWork() {
	h.liveMu.Lock()
	if h.stopped > 0 {
		h.stopped--
	}
	queued := h.stopped == 0 && len(h.pending) > 0
	h.liveMu.Unlock()
	if queued {
		select {
		case h.wake <- struct{}{}:
		default:
		}
	}
}

func (h *Handlers) setRunningRound(round int) {
	h.liveMu.Lock()
	if h.running != nil {
		h.running.Round = round
		h.running.RoundSince = time.Now()
		h.running.Draft = nil
		h.running.RoundDone = false
	}
	h.liveMu.Unlock()
	h.logger.Info("Mutation round started", slog.Int("round", round))
	h.hub.publish(topicBusy)
}

// setFinishing marks the run as past its model calls, so a stop that arrives
// now is refused instead of mislabeling a turn that is already complete.
func (h *Handlers) setFinishing() {
	h.liveMu.Lock()
	if h.running != nil {
		h.running.Finishing = true
	}
	h.liveMu.Unlock()
}

// setRunningTurn names the stored turn this run is writing, and tells the
// turns page. The row exists from here on, so its inspector can show the
// rounds as they finish.
func (h *Handlers) setRunningTurn(id int64) {
	h.liveMu.Lock()
	if h.running != nil {
		h.running.TurnID = id
	}
	h.liveMu.Unlock()
	h.hub.publish(topicTurns)
}

// finishRunningTurn drops the live conversation once it is stored, and tells
// the turns page to read the stored turn. It runs before compaction, so the
// inspector does not wait on the summarizer.
func (h *Handlers) finishRunningTurn() {
	h.liveMu.Lock()
	if h.running != nil {
		h.running.TurnID = 0
		h.running.Messages = nil
		h.running.Draft = nil
		h.running.RoundDone = false
	}
	h.liveMu.Unlock()
	h.hub.publish(topicTurns)
}

// addRunMessage keeps the conversation as it grows: the prompt first, then
// each finished round, which replaces the draft it grew from. The turns page
// shows it in full before the turn is stored.
func (h *Handlers) addRunMessage(message webmaster.Message) {
	h.liveMu.Lock()
	if h.running != nil {
		h.running.Messages = append(h.running.Messages, message)
		if message.Role == "assistant" {
			h.running.Draft = nil
			h.running.RoundDone = true
		}
	}
	h.liveMu.Unlock()
	h.hub.publish(topicBusy)
}

// setDraft keeps the round being written and repaints the admin panel at
// most once per draftEvery. The publish waits out the interval and then
// carries the newest draft, so the last chunk before a pause is never lost.
func (h *Handlers) setDraft(draft webmaster.Draft) {
	h.liveMu.Lock()
	if h.running == nil {
		h.liveMu.Unlock()
		return
	}
	h.running.Draft = &draft
	queue := !h.draftQueued
	h.draftQueued = true
	h.liveMu.Unlock()
	if !queue {
		return
	}
	time.AfterFunc(draftEvery, func() {
		h.liveMu.Lock()
		h.draftQueued = false
		h.liveMu.Unlock()
		h.hub.publish(topicDraft)
	})
}

func (h *Handlers) logRoundDone(stats webmaster.RoundStats) {
	attrs := []any{
		slog.Int("round", stats.Round),
		slog.Int64("ms", stats.Elapsed.Milliseconds()),
		slog.Int64("first_token_ms", stats.FirstToken.Milliseconds()),
		slog.Int64("prompt_tokens", stats.PromptTokens),
		slog.Int64("cached_tokens", stats.CachedTokens),
		slog.Int64("completion_tokens", stats.CompletionTokens),
		slog.Int("reply_runes", stats.ReplyRunes),
		slog.Int("reasoning_runes", stats.ReasoningRunes),
		slog.String("tools", strings.Join(stats.Tools, ",")),
		slog.Int("rejected", stats.Rejected),
		slog.String("finish_reason", stats.FinishReason),
		slog.String("outcomes", strings.Join(stats.Outcomes, " | ")),
	}
	if stats.Deferred != "" {
		attrs = append(attrs, slog.String("done_deferred", stats.Deferred))
	}
	if stats.Resampled {
		attrs = append(attrs, slog.Bool("resampled", true))
	}
	if stats.Stalled {
		attrs = append(attrs, slog.Bool("stalled", true))
	}
	if t := stats.Trace; t != nil {
		attrs = append(attrs,
			slog.String("backend_model", t.Model),
			slog.String("fingerprint", t.Fingerprint),
			slog.Int("attempts", t.Attempts),
			slog.Int64("headers_ms", t.Headers.Milliseconds()),
			slog.Int64("tool_name_ms", t.Name.Milliseconds()),
			slog.Int64("last_event_ms", t.LastEvent.Milliseconds()),
			slog.Int64("longest_gap_ms", t.LongestGap.Milliseconds()),
			slog.String("gap_phase", t.GapPhase),
			slog.Int64("reasoning_tokens", t.ReasoningTokens),
			slog.Bool("finished", t.Finished),
		)
	}
	h.logger.Info("Mutation round finished", attrs...)
}

// setVersion records the current version. A new one is published, so open
// pages restyle or reload. Its schema may declare other keys, so the state
// signals are pushed again too: a stylesheet-only change does not reload.
func (h *Handlers) setVersion(version int64) {
	h.liveMu.Lock()
	changed := h.version != version
	h.version = version
	h.liveMu.Unlock()
	if changed {
		h.hub.publish(topicSite | topicState)
	}
}

// resetLive points the live view at the seed. The queue is left alone: a post
// that landed during the reset is still owed a turn, and stopWork already
// dropped everything older.
func (h *Handlers) resetLive(version int64) {
	h.liveMu.Lock()
	h.version = version
	h.running = nil
	h.lastError = ""
	h.liveMu.Unlock()
	h.hub.publish(topicAll)
}

func (h *Handlers) liveSnapshot() (int64, int, *runState, string) {
	h.liveMu.Lock()
	defer h.liveMu.Unlock()
	var running *runState
	if h.running != nil {
		copied := *h.running
		running = &copied
	}
	return h.version, len(h.pending) + len(h.waiting), running, h.lastError
}

// queuedWork copies the posts waiting for the next turn and the admin
// instructions waiting behind the running one, so a page can show them
// without holding the lock while it renders.
func (h *Handlers) queuedWork() ([]sqlc.Post, []string) {
	h.liveMu.Lock()
	defer h.liveMu.Unlock()
	return append([]sqlc.Post(nil), h.pending...), append([]string(nil), h.waiting...)
}

// runProgress is the live turn for the inspector, or nil when nothing runs.
func (h *Handlers) runProgress() *pages.RunProgress {
	h.liveMu.Lock()
	defer h.liveMu.Unlock()
	if h.running == nil || h.running.TurnID == 0 {
		return nil
	}
	run := &pages.RunProgress{
		TurnID:     h.running.TurnID,
		Round:      h.running.Round,
		Since:      h.running.Since,
		RoundSince: h.running.RoundSince,
		Authors:    append([]string(nil), h.running.Authors...),
		Messages:   append([]webmaster.Message(nil), h.running.Messages...),
		RoundDone:  h.running.RoundDone,
	}
	if h.running.Draft != nil {
		draft := *h.running.Draft
		run.Draft = &draft
	}
	return run
}

// oldestPendingID is the lowest id among queued posts, or zero.
func (h *Handlers) oldestPendingID() int64 {
	h.liveMu.Lock()
	defer h.liveMu.Unlock()
	var oldest int64
	for _, post := range h.pending {
		if oldest == 0 || post.ID < oldest {
			oldest = post.ID
		}
	}
	return oldest
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
	rewriteCooldown = time.Second
	stateCooldown   = 250 * time.Millisecond
	// stateSlotsSweep is how many cooldown entries are kept before the
	// expired ones are swept.
	stateSlotsSweep = 1024
)

// heal copies the seed version forward. It is a restore, not a model call.
// Only the admin panel asks for it; a board left alone keeps its page.
func (h *Handlers) heal(ctx context.Context) (bool, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.stateMu.Lock()
	defer h.stateMu.Unlock()

	current, err := h.queries.CurrentSite(ctx)
	if err != nil {
		return false, fmt.Errorf("current site: %w", err)
	}
	seed, err := h.queries.SeedSiteVersion(ctx)
	if err != nil {
		return false, fmt.Errorf("seed site: %w", err)
	}
	if sameSiteContent(current, seed) {
		return false, nil
	}

	healed, err := h.carryVersionAfter(
		ctx,
		seed,
		webmaster.TurnHeal,
		"server",
		"the board went quiet; the plain page is back. you remember.",
		func(q *sqlc.Queries) error {
			if err := q.DeleteSiteState(ctx); err != nil {
				return fmt.Errorf("clear shared state: %w", err)
			}
			if err := q.DeleteJournal(ctx); err != nil {
				return fmt.Errorf("clear journal: %w", err)
			}
			return nil
		},
	)
	if err != nil {
		return false, err
	}
	h.setVersion(healed.ID)
	h.hub.publish(topicState | topicJournal)
	h.logger.Info("Board returned to the seed", slog.Int64("version_id", healed.ID))
	return true, nil
}

// allowStateWrite throttles one browser's writes to one key, so a looping
// script is stopped before it touches the database.
func (h *Handlers) allowStateWrite(visitor, key string) bool {
	now := time.Now()
	slot := visitor + "\x00" + key
	h.liveMu.Lock()
	defer h.liveMu.Unlock()
	if last, ok := h.lastStateAt[slot]; ok && now.Sub(last) < h.stateCooldown {
		return false
	}
	// An entry past its cooldown decides nothing, so a long-running board
	// sweeps them out instead of keeping every browser it ever saw.
	if len(h.lastStateAt) >= stateSlotsSweep {
		for old, at := range h.lastStateAt {
			if now.Sub(at) >= h.stateCooldown {
				delete(h.lastStateAt, old)
			}
		}
	}
	h.lastStateAt[slot] = now
	return true
}

// allowRewrite stops a script on the model's page from triggering a rewrite
// from every open tab in a loop. The post is still stored; only the costly
// model call is skipped.
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

// utcDayStart is the timestamp the "today" mutation count starts from.
func utcDayStart() string {
	return time.Now().UTC().Format("2006-01-02T00:00:00.000Z")
}

// mutationContext detaches the work from the request, so a refresh does not
// cancel a mutation halfway, then bounds it.
func mutationContext(ctx context.Context) (context.Context, context.CancelCauseFunc) {
	return runContext(context.WithoutCancel(ctx))
}

// runContext bounds one turn by mutationTimeout. Its cancel takes a cause, so
// a stop from the admin can be told from a timeout or a reset.
func runContext(parent context.Context) (context.Context, context.CancelCauseFunc) {
	base, cancelCause := context.WithCancelCause(parent)
	ctx, cancelTimeout := context.WithTimeout(base, mutationTimeout)
	return ctx, func(cause error) {
		cancelCause(cause)
		cancelTimeout()
	}
}

func truncate(s string, limit int) string {
	runes := []rune(s)
	if len(runes) <= limit {
		return s
	}
	return string(runes[:limit])
}
