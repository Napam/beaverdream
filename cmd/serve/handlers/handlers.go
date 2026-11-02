package handlers

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
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
}

// New wires the handlers.
func New(
	logger *slog.Logger,
	dbConn *sql.DB,
	queries *sqlc.Queries,
	mutator *webmaster.Mutator,
	cfg Config,
) *Handlers {
	return &Handlers{
		logger:  logger,
		db:      dbConn,
		queries: queries,
		mutator: mutator,
		cfg:     cfg,
	}
}

func siteFromRow(row sqlc.SiteVersion) webmaster.Site {
	return webmaster.Site{
		Version: row.ID,
		CSS:     row.Css,
		Note:    row.Note,
		Header:  row.Header,
		Board:   row.Board,
		Footer:  row.Footer,
	}
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

// mutate runs one mutation and records it. triggerPost is nil for admin
// instructions, which never become posts. On a model error the site is left
// alone; a partial rewrite is worse than no rewrite.
func (h *Handlers) mutate(
	ctx context.Context,
	kind string,
	instruction string,
	triggerPost *sqlc.Post,
) (webmaster.Result, error) {
	h.mu.Lock()
	defer h.mu.Unlock()

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

	trigger := webmaster.Post{Author: "admin", Body: instruction}
	var triggerID sql.NullInt64
	if triggerPost != nil {
		trigger = postFromRow(*triggerPost)
		triggerID = sql.NullInt64{Int64: triggerPost.ID, Valid: true}
	}

	start := time.Now()
	res, mutErr := h.mutator.Mutate(ctx, siteFromRow(current), posts, trigger)
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
			TriggerPostID: triggerID,
			Reason:        res.Reason,
		})
		if err != nil {
			mutErr = fmt.Errorf("store site version: %w", err)
		} else {
			versionID = sql.NullInt64{Int64: version.ID, Valid: true}
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

// mutateInBackground runs a post mutation after the response has been sent.
// The handler mutex still serializes mutations, so a queue of posts is fine;
// the poster just does not wait for the model.
func (h *Handlers) mutateInBackground(post sqlc.Post) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), mutationTimeout)
		defer cancel()
		if _, err := h.mutate(ctx, "post", post.Body, &post); err != nil {
			h.logger.Error(
				"Mutation failed",
				slog.Int64("post_id", post.ID),
				slog.Any("error", err),
			)
		}
	}()
}

// overDailyCap reports whether the cost kill switch has tripped.
func (h *Handlers) overDailyCap(ctx context.Context) bool {
	if h.cfg.MaxMutationsPerDay <= 0 {
		return false
	}
	start := time.Now().UTC().Format("2006-01-02T00:00:00.000Z")
	count, err := h.queries.CountMutationsSince(ctx, start)
	if err != nil {
		h.logger.Error("Failed to count mutations", slog.Any("error", err))
		return false
	}
	return count >= int64(h.cfg.MaxMutationsPerDay)
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
