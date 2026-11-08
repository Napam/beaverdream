package handlers

import (
	"context"
	"net/http"

	"github.com/Napam/beaverdream/db/world/generated/sqlc"
	"github.com/Napam/beaverdream/lib/registry"
	"github.com/Napam/beaverdream/lib/webmaster"
)

// WithTestPrincipal puts a synthetic signed-in identity on a request. Tests
// call handler methods directly, so the auth middleware is not in the path;
// this is the one seam that replaces it. Production never calls it.
func WithTestPrincipal(
	r *http.Request,
	id, name string,
	admin bool,
	csrfOK bool,
) *http.Request {
	return withPrincipal(r, &principal{
		IdentityID:   id,
		DisplayName:  name,
		SessionHash:  "test-session-" + id,
		SessionToken: "test-token-" + id,
		Scope:        registry.ScopePlay,
		Admin:        admin,
		CSRFOK:       csrfOK,
		CSRFToken:    "test-csrf",
	})
}

// WithTestAdmin is WithTestPrincipal for the app/admin plane.
func WithTestAdmin(r *http.Request, id, name string) *http.Request {
	return withPrincipal(r, &principal{
		IdentityID:   id,
		DisplayName:  name,
		LoginID:      "test-login-" + id,
		SessionHash:  "test-session-" + id,
		SessionToken: "test-token-" + id,
		Scope:        registry.ScopeApp,
		Admin:        true,
		CSRFOK:       true,
		CSRFToken:    "test-csrf",
	})
}

// TestAuth wraps a handler with the principal the production auth middleware
// would set. Event streams run through a real httptest server, so the identity
// has to live on the server side of the request.
func AuthForTest(h *Handlers, next http.Handler, id, name string, admin bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		_, err := h.reg.DB().
			ExecContext(ctx, `INSERT OR IGNORE INTO identities (id, display_name) VALUES (?, ?)`, id, name)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		login := "fixture-" + id
		_, err = h.reg.DB().
			ExecContext(ctx, `INSERT OR IGNORE INTO logins (id, identity_id, source, external_id) VALUES (?, ?, 'password', ?)`, login, id, login)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		if admin {
			if err = h.reg.GrantRole(ctx, id, registry.RoleAdmin); err != nil {
				http.Error(w, err.Error(), 500)
				return
			}
		}
		session, token, err := h.reg.NewSession(ctx, id, login, registry.ScopePlay)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		next.ServeHTTP(w, withPrincipal(r, &principal{
			IdentityID:   id,
			DisplayName:  name,
			SessionHash:  session.Hash,
			SessionToken: token,
			Scope:        registry.ScopePlay,
			Admin:        admin,
			CSRFOK:       true,
			CSRFToken:    "test-csrf",
		}))
	})
}

// JournalState and JournalRulesUsed expose what the prompt and the schema tool
// get about the journal, without running a model.
func (h *Handlers) JournalState(
	ctx context.Context,
	schema string,
) map[string]webmaster.JournalState {
	return h.journalState(ctx, schema)
}

func (h *Handlers) JournalRulesUsed(ctx context.Context) map[string]int {
	return h.journalRulesUsed(ctx)
}

// ExecForTest runs raw SQL on the handler's database, so a test can make a
// write fail with a trigger.
func (h *Handlers) ExecForTest(ctx context.Context, query string) error {
	_, err := h.db.ExecContext(ctx, query)
	return err
}

// QueuePost and RunPending let tests queue a post and drain the queue without
// racing the background worker.
func (h *Handlers) QueuePost(post sqlc.Post) { h.queuePost(post) }

func (h *Handlers) RunPending() { h.runPending() }

// HoldPost queues a post without waking the worker, so it stays pending.
func (h *Handlers) HoldPost(post sqlc.Post) {
	h.liveMu.Lock()
	h.pending = append(h.pending, post)
	h.liveMu.Unlock()
}

// HoldInstruction shows an admin instruction as waiting for a turn, the way a
// real one does while it waits for the changeset lock.
func (h *Handlers) HoldInstruction(text string) { h.addWaiting(text) }

// TableCountsForTest counts the rows of every table, so a test can check
// that reset reaches tables it does not know by name.
func (h *Handlers) TableCountsForTest(ctx context.Context) (map[string]int, error) {
	rows, err := h.db.QueryContext(ctx,
		`SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%'`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		names = append(names, name)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	counts := make(map[string]int, len(names))
	for _, name := range names {
		var n int
		if err := h.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM "`+name+`"`).
			Scan(&n); err != nil {
			return nil, err
		}
		counts[name] = n
	}
	return counts, nil
}

// NoStateCooldown lets a test write one key several times in a row.
func (h *Handlers) NoStateCooldown() { h.stateCooldown = 0 }

// StateOpIDsForTest lists op_id for every state_ops row in id order.
func (h *Handlers) StateOpIDsForTest(ctx context.Context) ([]int64, error) {
	rows, err := h.db.QueryContext(ctx, `SELECT op_id FROM state_ops ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// SequencesForTest reads sqlite_sequence: every AUTOINCREMENT table that has
// handed out an id, and the highest one.
func (h *Handlers) SequencesForTest(ctx context.Context) (map[string]int64, error) {
	rows, err := h.db.QueryContext(ctx, `SELECT name, seq FROM sqlite_sequence`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	seqs := make(map[string]int64)
	for rows.Next() {
		var name string
		var seq int64
		if err := rows.Scan(&name, &seq); err != nil {
			return nil, err
		}
		seqs[name] = seq
	}
	return seqs, rows.Err()
}

// LoadedForTest reports how many worlds the app currently holds open.
func (a *App) LoadedForTest() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.runtimes)
}

// RegistryForTest exposes the registry so a test can inspect usage rows.
func (h *Handlers) RegistryForTest() *registry.Registry { return h.reg }

// WorldForTest is the world this handler serves.
func (h *Handlers) WorldForTest() registry.World { return h.world }

// SetBudgetForTest changes the budget one world handler enforces.
func (h *Handlers) SetBudgetForTest(b registry.Budget) { h.budget = b }
