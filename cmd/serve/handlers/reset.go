package handlers

import (
	"database/sql"
	"log/slog"
	"net/http"

	"github.com/Napam/beaverdream/web/pages"
)

// ResetWorld returns the world to the state the first migrations created: one
// seed version, no posts, and no changeset history. It keeps the oldest
// version instead of reinserting it, so the seed content lives in one place.
// The next changeset becomes version 2.
func (h *Handlers) ResetWorld(w http.ResponseWriter, r *http.Request) {
	if !h.requireAdmin(w, r) {
		return
	}

	// Stop the turn in flight before waiting for the lock. A model call holds
	// h.mu for minutes; without the cancel the admin request would hang until
	// that turn finished, and the turn is about to be deleted anyway. The
	// deferred resume also covers a panic, which would otherwise leave work
	// paused for the life of the process.
	h.stopWork(errWorldReset)
	defer h.resumeWork()

	// Serialize with changesets so a rewrite cannot land in the middle of the
	// reset or write a version that references a deleted post. The panel is
	// rendered after both locks are released, so a slow admin client does not
	// block posts or state writes.
	message, ok := func() (string, bool) {
		h.mu.Lock()
		defer h.mu.Unlock()
		h.stateMu.Lock()
		defer h.stateMu.Unlock()

		ctx := r.Context()
		tx, err := h.db.BeginTx(ctx, nil)
		if err != nil {
			h.logger.Error("Failed to start the reset", slog.Any("error", err))
			http.Error(w, "Could not reset", http.StatusInternalServerError)
			return "", false
		}
		defer func() { _ = tx.Rollback() }()

		var seedID sql.NullInt64
		if err := tx.QueryRowContext(ctx, `SELECT MIN(id) FROM site_versions`).
			Scan(&seedID); err != nil {
			h.logger.Error("Failed to find the seed version", slog.Any("error", err))
			http.Error(w, "Could not reset", http.StatusInternalServerError)
			return "", false
		}
		if !seedID.Valid {
			h.logger.Error("Cannot reset a database with no site version")
			http.Error(w, "Could not reset", http.StatusInternalServerError)
			return "", false
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
			{`DELETE FROM shared_state`, nil},
			{`DELETE FROM journal`, nil},
			{`DELETE FROM state_holdings`, nil},
			{`DELETE FROM state_ops`, nil},
			// Rewind every id sequence. The room starts empty, so the first post
			// is turn 1 again.
			{
				`DELETE FROM sqlite_sequence
				 WHERE name IN ('turns', 'room_summaries', 'journal', 'state_ops')`,
				nil,
			},
			// AUTOINCREMENT remembers the highest id ever used. Bring it back to
			// the seed so the next version is seed+1 instead of one past the
			// history that was just deleted.
			{
				`UPDATE sqlite_sequence SET seq = ? WHERE name = 'site_versions'`,
				[]any{seedID.Int64},
			},
		}
		for _, statement := range statements {
			if _, err := tx.ExecContext(ctx, statement.query, statement.args...); err != nil {
				h.logger.Error("Failed to reset the world", slog.Any("error", err))
				http.Error(w, "Could not reset", http.StatusInternalServerError)
				return "", false
			}
		}

		if err := tx.Commit(); err != nil {
			h.logger.Error("Failed to commit the reset", slog.Any("error", err))
			http.Error(w, "Could not reset", http.StatusInternalServerError)
			return "", false
		}

		h.holdingsChanged()
		h.resetLive(seedID.Int64)
		h.logger.Info("World reset", slog.Int64("seed_version", seedID.Int64))
		return "world reset to the original version", true
	}()
	if ok {
		h.adminDone(w, r, h.panel(pages.PageSite), message)
	}
}
