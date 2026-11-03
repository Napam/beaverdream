package handlers

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/Napam/beaverdream/db/generated/sqlc"
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
	return version, nil
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
	var version sqlc.SiteVersion
	err := h.inTx(ctx, func(q *sqlc.Queries) error {
		turn, err := q.CreateTurn(ctx, sqlc.CreateTurnParams{
			Kind:   kind,
			Author: author,
			Body:   body,
		})
		if err != nil {
			return fmt.Errorf("record event turn: %w", err)
		}
		created, err := insertVersion(ctx, q, sqlc.CreateSiteVersionParams{
			Css:         source.Css,
			Note:        source.Note,
			Header:      source.Header,
			Board:       source.Board,
			Footer:      source.Footer,
			StateSchema: normalizeStateSchema(source.StateSchema),
			TurnID:      sql.NullInt64{Int64: turn.ID, Valid: true},
			Reason:      body,
		})
		version = created
		return err
	})
	return version, err
}
