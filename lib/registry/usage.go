package registry

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Budget is a daily token allowance in UTC days. Zero means unlimited.
type Budget struct {
	Identity int64
	Global   int64
}

// BudgetStatus is what the world's GET /budget returns: today's usage and
// the configured limits. A zero limit is unlimited.
type BudgetStatus struct {
	IdentityUsed  int64
	GlobalUsed    int64
	IdentityLimit int64
	GlobalLimit   int64
}

// CheckBudget refuses a post when the identity or the instance has already
// reached the limit. The next turn is not predicted, so usage exactly at the
// limit is refused; a turn already running may overshoot by one. Admins are
// never refused.
func (r *Registry) CheckBudget(ctx context.Context, identityID string, budget Budget) error {
	admin, err := r.IsAdmin(ctx, identityID)
	if err != nil {
		return err
	}
	if admin {
		return nil
	}
	dayKey := day(r.now())
	identityUsed, globalUsed, err := r.usageSums(ctx, r.db, identityID, dayKey)
	if err != nil {
		return err
	}
	if budget.Identity > 0 && identityUsed >= budget.Identity {
		return ErrBudget
	}
	if budget.Global > 0 && globalUsed >= budget.Global {
		return ErrBudget
	}
	return nil
}

// BudgetStatus reports usage and limits for the world. An admin gets the same
// numbers but is never refused.
func (r *Registry) BudgetStatus(
	ctx context.Context,
	identityID string,
	budget Budget,
) (BudgetStatus, error) {
	dayKey := day(r.now())
	identityUsed, globalUsed, err := r.usageSums(ctx, r.db, identityID, dayKey)
	if err != nil {
		return BudgetStatus{}, err
	}
	return BudgetStatus{
		IdentityUsed:  identityUsed,
		GlobalUsed:    globalUsed,
		IdentityLimit: budget.Identity,
		GlobalLimit:   budget.Global,
	}, nil
}

// RecordUsage charges one identity for one turn.
func (r *Registry) RecordUsage(
	ctx context.Context,
	identityID, worldID string,
	tokens, turnID int64,
) error {
	return r.RecordBatchUsage(ctx, worldID, tokens, []string{identityID}, turnID)
}

// RecordBatchUsage charges a turn with several authors. The tokens are split
// evenly across the distinct identities in the batch; any remainder goes to
// the first charged identity, so the charged rows add up to the whole amount.
// Admin identities are exempt: their share is not written and does not count
// toward the global total. Duplicate names count once.
func (r *Registry) RecordBatchUsage(
	ctx context.Context,
	worldID string,
	tokens int64,
	identityIDs []string,
	turnID int64,
) error {
	if tokens <= 0 || len(identityIDs) == 0 || worldID == "" {
		return nil
	}
	seen := make(map[string]struct{}, len(identityIDs))
	distinct := make([]string, 0, len(identityIDs))
	for _, id := range identityIDs {
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		distinct = append(distinct, id)
	}
	if len(distinct) == 0 {
		return nil
	}

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin usage: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	charged := make([]string, 0, len(distinct))
	for _, id := range distinct {
		var found int
		err := tx.QueryRowContext(ctx,
			`SELECT 1 FROM identities i
			 WHERE i.id = ? AND i.disabled_at IS NULL
			   AND EXISTS (
			     SELECT 1 FROM identity_roles r
			     WHERE r.identity_id = i.id AND r.role = ? AND r.revoked_at IS NULL
			   )`,
			id, RoleAdmin,
		).Scan(&found)
		if err == nil {
			continue
		}
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("read usage admin: %w", err)
		}
		charged = append(charged, id)
	}
	if len(charged) == 0 {
		return nil
	}

	share := tokens / int64(len(charged))
	remainder := tokens - share*int64(len(charged))
	nowTime := r.now().UTC()
	now := FormatTimestamp(nowTime)
	dayKey := day(nowTime)
	for i, id := range charged {
		amount := share
		if i == 0 {
			amount += remainder
		}
		if amount == 0 {
			continue
		}
		idPart, err := r.newID()
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO usage
			   (id, identity_id, world_id, day, turn_id, tokens, created_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?)`,
			idPart, id, worldID, dayKey, turnID, amount, now,
		); err != nil {
			return fmt.Errorf("insert usage: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit usage: %w", err)
	}
	return nil
}

// UsageOn returns one identity's and the instance's total for a UTC day.
func (r *Registry) UsageOn(
	ctx context.Context,
	identityID string,
	at time.Time,
) (int64, int64, error) {
	return r.usageSums(ctx, r.db, identityID, day(at))
}

type queryer interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func (r *Registry) usageSums(
	ctx context.Context,
	q queryer,
	identityID, dayKey string,
) (int64, int64, error) {
	var identityUsed int64
	err := q.QueryRowContext(ctx,
		`SELECT CAST(COALESCE(SUM(tokens), 0) AS INTEGER)
		 FROM usage WHERE identity_id = ? AND day = ?`,
		identityID, dayKey,
	).Scan(&identityUsed)
	if err != nil {
		return 0, 0, fmt.Errorf("read identity usage: %w", err)
	}
	var globalUsed int64
	err = q.QueryRowContext(ctx,
		`SELECT CAST(COALESCE(SUM(tokens), 0) AS INTEGER)
		 FROM usage WHERE day = ?`,
		dayKey,
	).Scan(&globalUsed)
	if err != nil {
		return 0, 0, fmt.Errorf("read global usage: %w", err)
	}
	return identityUsed, globalUsed, nil
}
