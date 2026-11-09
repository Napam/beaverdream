package registry

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// ChangePassword keeps the current app session and revokes every other session,
// handoff and reset link for the identity. Hashing runs outside the write lock.
func (r *Registry) ChangePassword(
	ctx context.Context,
	identityID, loginID, sessionHash, current, next string,
) ([]string, error) {
	if identityID == SystemAdminID {
		return nil, ErrForbidden
	}
	if err := ValidatePassword(next); err != nil {
		return nil, err
	}
	const loginQuery = `SELECT l.secret_hash, i.disabled_at
		FROM logins l JOIN identities i ON i.id = l.identity_id
		WHERE l.id = ? AND l.identity_id = ? AND l.source = 'password'`
	var hash string
	var disabled sql.NullString
	err := r.db.QueryRowContext(ctx, loginQuery, loginID, identityID).Scan(&hash, &disabled)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && disabled.Valid) {
		return nil, ErrForbidden
	}
	if err != nil {
		return nil, fmt.Errorf("read password login: %w", err)
	}
	if !r.hasher.Verify(current, hash) {
		return nil, ErrInvalidCredentials
	}
	nextHash, err := r.hasher.Hash(next)
	if err != nil {
		return nil, err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin password change: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	now := r.timestamp()
	var found int
	err = tx.QueryRowContext(ctx, `SELECT 1 FROM sessions
		WHERE hash = ? AND identity_id = ? AND login_id = ?
		AND scope = 'app' AND revoked_at IS NULL
		AND (expires_at IS NULL OR expires_at > ?)`,
		sessionHash, identityID, loginID, now,
	).Scan(&found)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrSessionInvalid
	}
	if err != nil {
		return nil, fmt.Errorf("read password change session: %w", err)
	}
	var currentHash string
	err = tx.QueryRowContext(ctx, loginQuery, loginID, identityID).Scan(&currentHash, &disabled)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && disabled.Valid) {
		return nil, ErrForbidden
	}
	if err != nil {
		return nil, fmt.Errorf("recheck password login: %w", err)
	}
	if currentHash != hash {
		return nil, ErrInvalidCredentials
	}
	if _, err := tx.ExecContext(
		ctx,
		`UPDATE logins SET secret_hash = ? WHERE id = ?`,
		nextHash,
		loginID,
	); err != nil {
		return nil, fmt.Errorf("change password: %w", err)
	}
	rows, err := tx.QueryContext(ctx, `SELECT hash FROM sessions
		WHERE identity_id = ? AND hash <> ? AND revoked_at IS NULL`, identityID, sessionHash)
	if err != nil {
		return nil, fmt.Errorf("list other sessions: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var revoked []string
	for rows.Next() {
		var hash string
		if err := rows.Scan(&hash); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("read other session: %w", err)
		}
		revoked = append(revoked, hash)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, fmt.Errorf("read other sessions: %w", err)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("close other sessions: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE sessions SET revoked_at = ?
		WHERE identity_id = ? AND hash <> ? AND revoked_at IS NULL`, now, identityID, sessionHash); err != nil {
		return nil, fmt.Errorf("revoke other sessions: %w", err)
	}
	if _, err := tx.ExecContext(
		ctx,
		`DELETE FROM handoffs WHERE identity_id = ?`,
		identityID,
	); err != nil {
		return nil, fmt.Errorf("delete password change handoffs: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM password_resets
		WHERE login_id IN (SELECT id FROM logins WHERE identity_id = ?)`, identityID); err != nil {
		return nil, fmt.Errorf("delete password change resets: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit password change: %w", err)
	}
	return revoked, nil
}
