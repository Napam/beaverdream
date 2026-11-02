package handlers

import (
	"database/sql"
	"log/slog"
	"net/http"
)

// ResetBoard returns the board to the state the first migrations created: one
// seed version, the seed posts, and no mutation history. It keeps the oldest
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
		{`DELETE FROM mutation_runs`, nil},
		{`DELETE FROM site_versions WHERE id > ?`, []any{seedID.Int64}},
		{`UPDATE site_versions SET trigger_post_id = NULL WHERE id = ?`, []any{seedID.Int64}},
		{`DELETE FROM posts`, nil},
		{`DELETE FROM sqlite_sequence WHERE name IN ('mutation_runs', 'posts')`, nil},
		// Reset restores the board the migrations seeded, including the posts
		// that make an empty board feel alive.
		{`INSERT INTO posts (author, body, created_at)
			SELECT author, body, strftime('%Y-%m-%dT%H:%M:%fZ', 'now', age)
			FROM seed_posts ORDER BY id`, nil},
		{`INSERT OR REPLACE INTO sqlite_sequence (name, seq)
			VALUES ('posts', (SELECT MAX(id) FROM posts))`, nil},
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
