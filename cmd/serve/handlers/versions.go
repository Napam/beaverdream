package handlers

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"time"

	"github.com/Napam/beaverdream/db/world/generated/sqlc"
	"github.com/Napam/beaverdream/lib/webmaster"
)

// versionStatsKept is how many versions keep their play counters. Older
// versions keep their render and their turns; only the report the model reads
// for the page it just shipped falls off.
const versionStatsKept = 50

// The kinds of event a version's play report counts.
const (
	statLoad           = "load"
	statJournal        = "journal"
	statJournalRefused = "journal_refused"
	statState          = "state"
	statStateRefused   = "state_refused"
	statAlert          = "alert"
)

// inTx runs fn with one transaction's queries. The whole callback must succeed
// or nothing lands.
func (h *Handlers) inTx(ctx context.Context, fn func(*sqlc.Queries) error) error {
	tx, err := h.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := fn(h.queries.WithTx(tx)); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit transaction: %w", err)
	}
	return nil
}

// insertVersion is createVersion inside a transaction the caller already owns.
func insertVersion(
	ctx context.Context,
	q *sqlc.Queries,
	params sqlc.CreateSiteVersionParams,
) (sqlc.SiteVersion, error) {
	version, err := q.CreateSiteVersion(ctx, params)
	if err != nil {
		return sqlc.SiteVersion{}, fmt.Errorf("store site version: %w", err)
	}
	// The report is only read for the newest versions, so the counters fall
	// off with the write. A failed prune leaves extra rows and nothing else,
	// so it never turns a version write into a failure.
	_ = q.PruneVersionStats(ctx, versionStatsKept)
	_ = q.PruneVersionVisitors(ctx, versionStatsKept)
	return version, nil
}

// countVersion adds one event to a version's play report. The report is a
// courtesy to the model, not part of the request's contract: a failed write is
// logged at debug and dropped.
func countVersion(
	ctx context.Context,
	logger *slog.Logger,
	q *sqlc.Queries,
	versionID int64,
	kind string,
	key string,
) {
	if versionID <= 0 || q == nil {
		return
	}
	err := q.BumpVersionStat(ctx, sqlc.BumpVersionStatParams{
		VersionID: versionID,
		Kind:      kind,
		Key:       key,
	})
	if err != nil {
		logger.Debug("Could not count a play event",
			slog.Int64("version_id", versionID),
			slog.String("kind", kind),
			slog.String("key", key),
			slog.Any("error", err),
		)
	}
}

// addVersionVisitor records that one identity opened a version. The same
// identity opening it again is the same visitor, so the second insert is
// ignored.
func addVersionVisitor(
	ctx context.Context,
	logger *slog.Logger,
	q *sqlc.Queries,
	versionID int64,
	visitor string,
) {
	if versionID <= 0 || visitor == "" || q == nil {
		return
	}
	err := q.AddVersionVisitor(ctx, sqlc.AddVersionVisitorParams{
		VersionID: versionID,
		Visitor:   visitor,
	})
	if err != nil {
		logger.Debug("Could not count a visitor",
			slog.Int64("version_id", versionID),
			slog.Any("error", err),
		)
	}
}

// maxStatKeyLen bounds the key a refusal is filed under. A declared key is
// short, but a request can name any key at all, and the report must not grow
// rows with kilobyte keys.
const maxStatKeyLen = 48

// countJournalRefusal records why an append was turned down, so the model sees
// which mistake its own page keeps making.
func (h *Handlers) countJournalRefusal(ctx context.Context, versionID int64, key, reason string) {
	countVersion(ctx, h.logger, h.queries, versionID, statJournalRefused,
		truncate(key, maxStatKeyLen)+":"+reason)
}

// countStateRefusal is countJournalRefusal for a shared or visitor state op.
func (h *Handlers) countStateRefusal(ctx context.Context, versionID int64, key, reason string) {
	countVersion(ctx, h.logger, h.queries, versionID, statStateRefused,
		truncate(key, maxStatKeyLen)+":"+reason)
}

// currentVersionID reads the newest version id, or zero when the read fails.
func (h *Handlers) currentVersionID(ctx context.Context) int64 {
	raw, err := h.queries.CurrentVersionID(ctx)
	if err != nil {
		return 0
	}
	id, _ := raw.(int64)
	return id
}

// versionPlay is what happened to one version, for the next prompt. A version
// with no counters gets nil, so the prompt says nothing at all.
func versionPlay(
	ctx context.Context,
	logger *slog.Logger,
	q *sqlc.Queries,
	version sqlc.SiteVersion,
) *webmaster.PlayReport {
	rows, err := q.ListVersionStats(ctx, version.ID)
	if err != nil {
		logger.Debug("Could not read a version's play report",
			slog.Int64("version_id", version.ID),
			slog.Any("error", err),
		)
		return nil
	}
	if len(rows) == 0 {
		return nil
	}
	visitors, err := q.CountVersionVisitors(ctx, version.ID)
	if err != nil {
		logger.Debug("Could not read a version's visitor count",
			slog.Int64("version_id", version.ID),
			slog.Any("error", err),
		)
	}
	play := &webmaster.PlayReport{
		Version:  version.ID,
		Age:      versionAge(version.CreatedAt),
		Visitors: visitors,
	}
	for _, row := range rows {
		play.Counters = append(play.Counters, webmaster.PlayCounter{
			Kind: row.Kind,
			Key:  row.Key,
			N:    row.N,
		})
	}
	return play
}

// versionAge is how long ago a version was written. An unparsable timestamp
// reads as just now, which is the least misleading thing to tell the model.
func versionAge(createdAt string) time.Duration {
	at, err := time.Parse(sqliteTime, createdAt)
	if err != nil {
		return 0
	}
	age := time.Since(at)
	if age < 0 {
		return 0
	}
	return age
}

// carryVersion copies a version forward and records the event as a turn. Undo,
// restore and heal all use it, so history moves as one piece and the
// webmaster can read what happened.
func (h *Handlers) carryVersion(
	ctx context.Context,
	source sqlc.SiteVersion,
	kind string,
	author string,
	body string,
) (sqlc.SiteVersion, error) {
	return h.carryVersionAfter(ctx, source, kind, author, body, nil)
}

// carryVersionAfter is carryVersion with a first step in the same
// transaction. Heal uses it to clear the shared state and the journal, so the
// old data is gone exactly when the plain page lands, and not at all if the
// write fails.
func (h *Handlers) carryVersionAfter(
	ctx context.Context,
	source sqlc.SiteVersion,
	kind string,
	author string,
	body string,
	before func(*sqlc.Queries) error,
) (sqlc.SiteVersion, error) {
	var version sqlc.SiteVersion
	err := h.inTx(ctx, func(q *sqlc.Queries) error {
		if before != nil {
			if err := before(q); err != nil {
				return err
			}
		}
		turn, err := q.CreateTurn(ctx, sqlc.CreateTurnParams{
			Kind:   kind,
			Author: author,
			Body:   body,
		})
		if err != nil {
			return fmt.Errorf("record event turn: %w", err)
		}
		created, err := insertVersion(ctx, q, sqlc.CreateSiteVersionParams{
			Css:           source.Css,
			Html:          source.Html,
			StateSchema:   normalizeSchema(source.StateSchema),
			JournalSchema: normalizeSchema(source.JournalSchema),
			TurnID:        sql.NullInt64{Int64: turn.ID, Valid: true},
			Reason:        body,
		})
		version = created
		return err
	})
	if err != nil {
		return version, err
	}
	// A carried version is a new page: the lobby card shows its size and the
	// alerts against the page it replaced are closed.
	h.recordWorldHealth(ctx, "", len(source.Css)+len(source.Html), true)
	return version, nil
}
