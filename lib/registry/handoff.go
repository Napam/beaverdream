package registry

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// CreateHandoff mints a single-use code that moves one browser from the app
// port to one world on the play port. The code is bound to the identity, the
// login and the world, and is stored as a hash.
func (r *Registry) CreateHandoff(
	ctx context.Context,
	identityID, loginID, worldID string,
) (string, time.Time, error) {
	var ready int
	err := r.db.QueryRowContext(ctx,
		`SELECT 1 FROM worlds WHERE id = ? AND status = 'ready'`, worldID,
	).Scan(&ready)
	if errors.Is(err, sql.ErrNoRows) {
		return "", time.Time{}, ErrWorldNotReady
	}
	if err != nil {
		return "", time.Time{}, fmt.Errorf("read handoff world: %w", err)
	}
	var active int
	err = r.db.QueryRowContext(ctx,
		`SELECT 1 FROM identities WHERE id = ? AND disabled_at IS NULL`, identityID,
	).Scan(&active)
	if errors.Is(err, sql.ErrNoRows) {
		return "", time.Time{}, ErrSessionInvalid
	}
	if err != nil {
		return "", time.Time{}, fmt.Errorf("read handoff identity: %w", err)
	}
	err = r.db.QueryRowContext(ctx,
		`SELECT 1 FROM logins WHERE id = ? AND identity_id = ?`,
		loginID, identityID,
	).Scan(&active)
	if errors.Is(err, sql.ErrNoRows) {
		return "", time.Time{}, ErrSessionInvalid
	}
	if err != nil {
		return "", time.Time{}, fmt.Errorf("read handoff login: %w", err)
	}

	nowTime := r.now().UTC()
	now := FormatTimestamp(nowTime)
	expires := nowTime.Add(r.handoff)
	code, err := r.newToken()
	if err != nil {
		return "", time.Time{}, err
	}
	if _, err := r.db.ExecContext(ctx,
		`INSERT INTO handoffs
		   (code_hash, identity_id, login_id, world_id, created_at, expires_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		hashToken(code), identityID, loginID, worldID, now, FormatTimestamp(expires),
	); err != nil {
		return "", time.Time{}, fmt.Errorf("insert handoff: %w", err)
	}
	// Old codes have no value once spent or expired; drop them without
	// blocking the exchange path. Best effort, and the row we just wrote is
	// newer than every match.
	_, _ = r.db.ExecContext(ctx,
		`DELETE FROM handoffs
		 WHERE (expires_at <= ? OR used_at IS NOT NULL) AND created_at < ?`,
		now, FormatTimestamp(nowTime.Add(-time.Hour)),
	)
	return code, expires, nil
}

// ExchangeHandoff spends a code and creates a play session in one immediate
// transaction. A second exchange of the same code fails, as does a code whose
// identity was disabled or whose world stopped being ready.
func (r *Registry) ExchangeHandoff(
	ctx context.Context,
	code string,
) (*Identity, *Session, string, string, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, nil, "", "", fmt.Errorf("begin handoff: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	now := r.timestamp()
	var (
		identityID string
		loginID    string
		worldID    string
	)
	err = tx.QueryRowContext(ctx,
		`UPDATE handoffs SET used_at = ?
		 WHERE code_hash = ? AND used_at IS NULL AND expires_at > ?
		 RETURNING identity_id, login_id, world_id`,
		now, hashToken(code), now,
	).Scan(&identityID, &loginID, &worldID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil, "", "", ErrHandoffInvalid
	}
	if err != nil {
		return nil, nil, "", "", fmt.Errorf("consume handoff: %w", err)
	}

	row := tx.QueryRowContext(ctx,
		`SELECT id, display_name, COALESCE(invite_id, ''), created_at, disabled_at
		 FROM identities WHERE id = ? AND disabled_at IS NULL`,
		identityID,
	)
	identity, err := scanIdentity(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil, "", "", ErrHandoffInvalid
	}
	if err != nil {
		return nil, nil, "", "", fmt.Errorf("read handoff identity: %w", err)
	}
	var ready int
	err = tx.QueryRowContext(ctx,
		`SELECT 1 FROM worlds WHERE id = ? AND status = 'ready'`, worldID,
	).Scan(&ready)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil, "", "", ErrHandoffInvalid
	}
	if err != nil {
		return nil, nil, "", "", fmt.Errorf("read handoff world: %w", err)
	}

	session, token, err := r.insertSessionTx(
		ctx, tx, identityID, loginID, ScopePlay, r.playTTL,
	)
	if err != nil {
		return nil, nil, "", "", err
	}
	if err := tx.Commit(); err != nil {
		return nil, nil, "", "", fmt.Errorf("commit handoff: %w", err)
	}
	return identity, session, token, worldID, nil
}
