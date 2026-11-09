package registry

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// DefaultPasswordResetTTL is how long an admin-issued reset link stays valid.
const DefaultPasswordResetTTL = time.Hour

// CreatePasswordReset mints a single-use code for one account's password
// login. The code is returned once; only its hash is stored. An older code for
// the same login stops working.
func (r *Registry) CreatePasswordReset(
	ctx context.Context,
	actorID, identityID string,
) (string, time.Time, error) {
	if identityID == SystemAdminID {
		return "", time.Time{}, fmt.Errorf("%w: the system admin is not resettable", ErrForbidden)
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("begin password reset: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := requireAdminTx(ctx, tx, actorID); err != nil {
		return "", time.Time{}, err
	}
	var disabledAt sql.NullString
	err = tx.QueryRowContext(ctx,
		`SELECT disabled_at FROM identities WHERE id = ?`, identityID,
	).Scan(&disabledAt)
	if errors.Is(err, sql.ErrNoRows) {
		return "", time.Time{}, ErrNotFound
	}
	if err != nil {
		return "", time.Time{}, fmt.Errorf("read reset identity: %w", err)
	}
	if disabledAt.Valid {
		return "", time.Time{}, fmt.Errorf("%w: a disabled account cannot be reset", ErrForbidden)
	}
	var loginID string
	err = tx.QueryRowContext(ctx,
		`SELECT id FROM logins
		 WHERE identity_id = ? AND source = 'password'
		 ORDER BY created_at, id LIMIT 1`,
		identityID,
	).Scan(&loginID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", time.Time{}, fmt.Errorf("%w: the account has no password login", ErrForbidden)
	}
	if err != nil {
		return "", time.Time{}, fmt.Errorf("read reset login: %w", err)
	}

	code, err := r.newToken()
	if err != nil {
		return "", time.Time{}, err
	}
	nowTime := r.now().UTC()
	expires := nowTime.Add(DefaultPasswordResetTTL)
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO password_resets (login_id, code_hash, expires_at)
		 VALUES (?, ?, ?)
		 ON CONFLICT (login_id) DO UPDATE SET
		   code_hash = excluded.code_hash,
		   expires_at = excluded.expires_at`,
		loginID, hashToken(code), FormatTimestamp(expires),
	); err != nil {
		return "", time.Time{}, fmt.Errorf("insert password reset: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return "", time.Time{}, fmt.Errorf("commit password reset: %w", err)
	}
	return code, expires, nil
}

// PasswordResetValid reports whether a code can still be redeemed. Unknown,
// expired, disabled and system-admin rows all read as false.
func (r *Registry) PasswordResetValid(ctx context.Context, code string) (bool, error) {
	if code == "" {
		return false, nil
	}
	var found int
	err := r.db.QueryRowContext(ctx,
		`SELECT 1
		 FROM password_resets p
		 JOIN logins l ON l.id = p.login_id
		 JOIN identities i ON i.id = l.identity_id
		 WHERE p.code_hash = ? AND p.expires_at > ?
		   AND l.source = 'password'
		   AND i.disabled_at IS NULL AND i.id <> ?`,
		hashToken(code), r.timestamp(), SystemAdminID,
	).Scan(&found)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read password reset: %w", err)
	}
	return true, nil
}

// ResetPassword consumes a code and replaces one password login's secret. It
// revokes every session and deletes every handoff for the identity in the same
// transaction, so nothing minted before the reset survives it.
func (r *Registry) ResetPassword(
	ctx context.Context,
	code, password string,
) (string, error) {
	if err := ValidatePassword(password); err != nil {
		return "", err
	}
	valid, err := r.PasswordResetValid(ctx, code)
	if err != nil {
		return "", err
	}
	if !valid {
		return "", ErrResetInvalid
	}
	hash, err := r.hasher.Hash(password)
	if err != nil {
		return "", err
	}

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return "", fmt.Errorf("begin reset password: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var loginID, identityID string
	err = tx.QueryRowContext(ctx,
		`SELECT p.login_id, l.identity_id
		 FROM password_resets p
		 JOIN logins l ON l.id = p.login_id
		 JOIN identities i ON i.id = l.identity_id
		 WHERE p.code_hash = ? AND p.expires_at > ?
		   AND l.source = 'password'
		   AND i.disabled_at IS NULL AND i.id <> ?`,
		hashToken(code), r.timestamp(), SystemAdminID,
	).Scan(&loginID, &identityID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrResetInvalid
	}
	if err != nil {
		return "", fmt.Errorf("read password reset: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM password_resets
		 WHERE login_id IN (SELECT id FROM logins WHERE identity_id = ?)`,
		identityID,
	); err != nil {
		return "", fmt.Errorf("consume password reset: %w", err)
	}
	res, err := tx.ExecContext(ctx,
		`UPDATE logins SET secret_hash = ? WHERE id = ?`, hash, loginID,
	)
	if err != nil {
		return "", fmt.Errorf("update password: %w", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return "", fmt.Errorf("update password rows: %w", err)
	}
	if affected == 0 {
		return "", ErrResetInvalid
	}
	now := r.timestamp()
	if _, err := tx.ExecContext(ctx,
		`UPDATE sessions SET revoked_at = ?
		 WHERE identity_id = ? AND revoked_at IS NULL`,
		now, identityID,
	); err != nil {
		return "", fmt.Errorf("revoke reset sessions: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM handoffs WHERE identity_id = ?`, identityID,
	); err != nil {
		return "", fmt.Errorf("delete reset handoffs: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return "", fmt.Errorf("commit reset password: %w", err)
	}
	return identityID, nil
}
