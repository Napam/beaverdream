package registry

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// CreateAPIToken mints a script token for an admin identity. It is a normal
// session row with scope api and no expiry, so one revoke kills it and the
// password is never sent in a header. The raw token is returned once. The
// originating app session must still be active inside the same transaction, so
// a password reset cannot revoke it and then be followed by a fresh token.
func (r *Registry) CreateAPIToken(
	ctx context.Context,
	actorID, loginID, sessionHash string,
) (*Session, string, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, "", fmt.Errorf("begin api token: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := requireAdminTx(ctx, tx, actorID); err != nil {
		return nil, "", err
	}
	var active int
	err = tx.QueryRowContext(ctx,
		`SELECT 1 FROM sessions
		 WHERE hash = ? AND identity_id = ? AND login_id = ?
		   AND scope = 'app' AND revoked_at IS NULL
		   AND (expires_at IS NULL OR expires_at > ?)`,
		sessionHash, actorID, loginID, r.timestamp(),
	).Scan(&active)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, "", ErrSessionInvalid
	}
	if err != nil {
		return nil, "", fmt.Errorf("read api session: %w", err)
	}
	session, token, err := r.insertSessionTx(ctx, tx, actorID, loginID, ScopeAPI, 0)
	if err != nil {
		return nil, "", err
	}
	if err := tx.Commit(); err != nil {
		return nil, "", fmt.Errorf("commit api token: %w", err)
	}
	return session, token, nil
}

// RevokeAPIToken revokes a token by its raw value. It is RevokeSession with a
// name that says what the caller means.
func (r *Registry) RevokeAPIToken(ctx context.Context, token string) error {
	return r.RevokeSession(ctx, token)
}
