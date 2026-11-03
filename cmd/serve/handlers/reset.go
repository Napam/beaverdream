package handlers

import (
	"database/sql"
	"log/slog"
	"net/http"
)

// ResetBoard returns the board to the state the first migrations created: one
// seed version, no posts, and no mutation history. It keeps the oldest
// version instead of reinserting it, so the seed content lives in one place.
// The next mutation becomes version 2.
func (h *Handlers) ResetBoard(w http.ResponseWriter, r *http.Request) {
	if !h.requireAdmin(w, r) {
		return
	}

	// Serialize with mutations so a rewrite cannot land in the middle of the
	// reset or write a version that references a deleted post.
	h.mu.Lock()
	defer h.mu.Unlock()
	h.stateMu.Lock()
	defer h.stateMu.Unlock()

	ctx := r.Context()
	tx, err := h.db.BeginTx(ctx, nil)
	if err != nil {
		h.logger.Error("Failed to start the reset", slog.Any("error", err))
		http.Error(w, "Could not reset", http.StatusInternalServerError)
		return
	}
	defer func() { _ = tx.Rollback() }()

	var seedID sql.NullInt64
	if err := tx.QueryRowContext(ctx, `SELECT MIN(id) FROM site_versions`).
		Scan(&seedID); err != nil {
		h.logger.Error("Failed to find the seed version", slog.Any("error", err))
		http.Error(w, "Could not reset", http.StatusInternalServerError)
		return
	}
	if !seedID.Valid {
		h.logger.Error("Cannot reset a database with no site version")
		http.Error(w, "Could not reset", http.StatusInternalServerError)
		return
	}

	statements := []struct {
		query string
		args  []any
	}{
		// The room is wiped first, so the versions that referenced its turns
		// can be deleted without a dangling row.
		{`DELETE FROM room_summaries`, nil},
		{`DELETE FROM turns`, nil},
		// The cascade would do this, but reset must not depend on the
		// foreign-key pragma being on for every connection.
		{`DELETE FROM site_versions WHERE id > ?`, []any{seedID.Int64}},
		{`UPDATE site_versions SET turn_id = NULL WHERE id = ?`, []any{seedID.Int64}},
		{`DELETE FROM site_state`, nil},
		{`DELETE FROM journal`, nil},
		// Rewind every id sequence. The room starts empty, so the first post
		// is turn 1 again.
		{`DELETE FROM sqlite_sequence WHERE name IN ('turns', 'journal')`, nil},
		// AUTOINCREMENT remembers the highest id ever used. Bring it back to
		// the seed so the next version is seed+1 instead of one past the
		// history that was just deleted.
		{`UPDATE sqlite_sequence SET seq = ? WHERE name = 'site_versions'`, []any{seedID.Int64}},
	}
	for _, statement := range statements {
		if _, err := tx.ExecContext(ctx, statement.query, statement.args...); err != nil {
			h.logger.Error("Failed to reset the board", slog.Any("error", err))
			http.Error(w, "Could not reset", http.StatusInternalServerError)
			return
		}
	}

	if err := tx.Commit(); err != nil {
		h.logger.Error("Failed to commit the reset", slog.Any("error", err))
		http.Error(w, "Could not reset", http.StatusInternalServerError)
		return
	}

	h.resetLive(seedID.Int64)
	h.logger.Info("Board reset", slog.Int64("seed_version", seedID.Int64))
	h.redirectAdmin(w, r, "board reset to the original version")
}
