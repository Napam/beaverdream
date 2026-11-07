package registry

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// CreateAPIToken mints a script token for an admin identity. It is a normal
// session row with scope api and no expiry, so one revoke kills it and the
// password is never sent in a header. The raw token is returned once.
func (r *Registry) CreateAPIToken(
	ctx context.Context,
	actorID, loginID string,
) (*Session, string, error) {
	if err := r.RequireAdmin(ctx, actorID); err != nil {
		return nil, "", err
	}
	var found int
	err := r.db.QueryRowContext(ctx,
		`SELECT 1 FROM logins WHERE id = ? AND identity_id = ?`,
		loginID, actorID,
	).Scan(&found)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, "", ErrNotFound
	}
	if err != nil {
		return nil, "", fmt.Errorf("read api login: %w", err)
	}
	return r.NewSessionTTL(ctx, actorID, loginID, ScopeAPI, 0)
}

// RevokeAPIToken revokes a token by its raw value. It is RevokeSession with a
// name that says what the caller means.
func (r *Registry) RevokeAPIToken(ctx context.Context, token string) error {
	return r.RevokeSession(ctx, token)
}
