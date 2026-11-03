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
	"time"
	"unicode/utf8"

	"github.com/Napam/beaverdream/db/generated/sqlc"
	"github.com/Napam/beaverdream/lib/webmaster"
)

const (
	maxBodyLen = 2000
	// roomLimit is the smallest number of turns the model reads back. The
	// query limit is the larger of this and the turn backstop, so the log
	// never drops its oldest line before compaction folds it in. A sliding
	// start would lose those turns and the cached prompt prefix every turn.
	roomLimit = 2000
	// defaultCompactAfterTokens is the estimated transcript budget before
	// compaction. Estimated at four runes per token.
	defaultCompactAfterTokens = 128_000
	// defaultCompactAfterTurns is the backstop for many very short turns.
	defaultCompactAfterTurns = 10_000
	// compactMaxTurns caps one summarizer input, so a large backlog is folded
	// in several mutations instead of one call that overflows the model.
	compactMaxTurns = 1000
	// compactMaxChunks bounds the summarizer calls in one mutation.
	compactMaxChunks = 8
	// summarizeTimeout is separate from the mutation budget so a slow summary
	// cannot starve the rewrite that follows it.
	summarizeTimeout = 45 * time.Second
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
	// CompactAfterTokens is the estimated transcript size that triggers
	// compaction. Zero uses the default (128k).
	CompactAfterTokens int
	// CompactAfterTurns is the turn-count backstop for very short turns.
	// Zero uses the default.
	CompactAfterTurns int
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
	journalBucket map[string]*journalBucket
	// alertSeen collapses repeated browser errors into one event each.
	alertSeen    map[string]time.Time
	lastActivity time.Time
	wake         chan struct{}
	stop         chan struct{}
	closeOnce    sync.Once
	workerWG     sync.WaitGroup

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
		lastRewriteAt: make(map[string]time.Time),
		lastStateAt:   make(map[string]time.Time),
		journalBucket: make(map[string]*journalBucket),
		alertSeen:     make(map[string]time.Time),
		lastActivity:  time.Now(),
		wake:          make(chan struct{}, 1),
		stop:          make(chan struct{}),

		compactAfterTokens: cfg.CompactAfterTokens,
		compactAfterTurns:  cfg.CompactAfterTurns,
	}
	if h.compactAfterTokens <= 0 {
		h.compactAfterTokens = defaultCompactAfterTokens
	}
	if h.compactAfterTurns <= 0 {
		h.compactAfterTurns = defaultCompactAfterTurns
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
		Version:       row.ID,
		CSS:           row.Css,
		Note:          row.Note,
		Header:        row.Header,
		Board:         row.Board,
		Footer:        row.Footer,
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
		a.Header == b.Header &&
		a.Board == b.Board &&
		a.Footer == b.Footer &&
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
	turnBody := ""
	if kind == "admin" {
		turnBody = instruction
	}
	turn, err := h.queries.StartTurn(ctx, sqlc.StartTurnParams{
		Kind:   webmaster.TurnWebmaster,
		Author: "webmaster",
		Body:   turnBody,
	})
	if err != nil {
		return webmaster.Result{}, fmt.Errorf("start turn: %w", err)
	}

	postIDs := make([]int64, 0, len(triggers))
	authors := make([]string, 0, len(triggers))
	for _, post := range triggers {
		if post.ID > 0 {
			postIDs = append(postIDs, post.ID)
		}
		authors = append(authors, post.Author)
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
				Note:          res.Site.Note,
				Header:        res.Site.Header,
				Board:         res.Site.Board,
				Footer:        res.Site.Footer,
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
	nextPage := currentSite
	if res.Changed {
		nextPage = res.Site
	}
	compactStart := time.Now()
	if err := h.compactRoom(
		context.WithoutCancel(ctx),
		turn.ID,
		int64(webmaster.PageRunes(nextPage)),
	); err != nil {
		h.logger.Error("Failed to compact the room", slog.Any("error", err))
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
	var prompt, cached, rejected int64
	for _, round := range res.Rounds {
		prompt += round.PromptTokens
		cached += round.CachedTokens
		rejected += int64(round.Rejected)
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
		slog.Bool("last_round_done_only", doneOnly),
		slog.Int64("rejected", rejected),
		slog.Int64("prompt_tokens", prompt),
		slog.Float64("cached_ratio", cacheRatio),
	}
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
	h.touchActivity()
	h.liveMu.Lock()
	h.pending = append(h.pending, post)
	pending := len(h.pending)
	h.liveMu.Unlock()

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
	batch := h.pending
	h.pending = nil
	h.liveMu.Unlock()

	batch = h.liveOnly(context.Background(), batch)
	if len(batch) == 0 {
		return
	}
	if h.overDailyCap(context.Background()) {
		h.setLastError("daily mutation cap reached")
		h.logger.Warn("Mutation skipped: daily cap reached",
			slog.Int("posts", len(batch)),
		)
		return
	}
	if len(batch) > maxBatchPosts {
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

	h.beginRun("post", postAuthors(batch))
	ctx, cancel := context.WithTimeout(context.Background(), mutationTimeout)
	_, err := h.mutateHeld(ctx, "post", "", batch)
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
	h.logger.Info("Mutation round started", slog.Int("round", round))
}

func (h *Handlers) logRoundDone(stats webmaster.RoundStats) {
	h.logger.Info("Mutation round finished",
		slog.Int("round", stats.Round),
		slog.Int64("ms", stats.Elapsed.Milliseconds()),
		slog.Int64("prompt_tokens", stats.PromptTokens),
		slog.Int64("cached_tokens", stats.CachedTokens),
		slog.Int64("completion_tokens", stats.CompletionTokens),
		slog.String("tools", strings.Join(stats.Tools, ",")),
		slog.Int("rejected", stats.Rejected),
	)
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
		h.touchActivity()
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
	count, err := h.queries.CountWebmasterTurnsSince(ctx, utcDayStart())
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
