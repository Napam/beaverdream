package registry

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Scope names the plane a session belongs to.
type Scope string

const (
	// ScopeApp is the management plane: account, invites, API tokens,
	// admin pages. App sessions expire after DefaultAppSessionTTL.
	ScopeApp Scope = "app"
	// ScopePlay is one world's board. Play sessions have no expiry by
	// default and cannot change credentials.
	ScopePlay Scope = "play"
	// ScopeAPI is a script token. It skips CSRF and only an admin may hold
	// one for now.
	ScopeAPI Scope = "api"
)

// Session is one stored browser or script session. The token itself is never
// stored; creation returns it once.
type Session struct {
	Hash       string
	IdentityID string
	LoginID    string
	Scope      Scope
	CreatedAt  time.Time
	LastUsedAt *time.Time
	ExpiresAt  *time.Time
	RevokedAt  *time.Time
}

// ActiveAt reports whether the session has not been revoked and has not
// reached its exact expiry instant.
func (s Session) ActiveAt(now time.Time) bool {
	if s.RevokedAt != nil {
		return false
	}
	if s.ExpiresAt != nil && !s.ExpiresAt.After(now) {
		return false
	}
	return true
}

func validScope(scope Scope) bool {
	switch scope {
	case ScopeApp, ScopePlay, ScopeAPI:
		return true
	default:
		return false
	}
}

func (r *Registry) ttlForScope(scope Scope) time.Duration {
	switch scope {
	case ScopeApp:
		return r.appTTL
	case ScopePlay:
		return r.playTTL
	default:
		return 0
	}
}

// Login verifies a password login and creates an app session.
func (r *Registry) Login(
	ctx context.Context,
	username, password string,
) (*Identity, *Session, string, error) {
	username = strings.TrimSpace(username)

	var (
		loginID    string
		identityID string
		hash       string
		disabledAt sql.NullString
	)
	err := r.db.QueryRowContext(ctx,
		`SELECT l.id, l.identity_id, l.secret_hash, i.disabled_at
		 FROM logins l
		 JOIN identities i ON i.id = l.identity_id
		 WHERE l.source = 'password' AND l.external_id = ?`,
		username,
	).Scan(&loginID, &identityID, &hash, &disabledAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil, "", ErrInvalidCredentials
	}
	if err != nil {
		return nil, nil, "", fmt.Errorf("read login: %w", err)
	}
	if disabledAt.Valid {
		return nil, nil, "", ErrInvalidCredentials
	}
	if !r.hasher.Verify(password, hash) {
		return nil, nil, "", ErrInvalidCredentials
	}
	now := r.timestamp()
	if _, err := r.db.ExecContext(ctx,
		`UPDATE logins SET last_login_at = ? WHERE id = ?`, now, loginID,
	); err != nil {
		return nil, nil, "", fmt.Errorf("touch login: %w", err)
	}
	session, token, err := r.NewSession(ctx, identityID, loginID, ScopeApp)
	if err != nil {
		return nil, nil, "", err
	}
	identity, err := r.GetIdentity(ctx, identityID)
	if err != nil {
		return nil, nil, "", err
	}
	return identity, session, token, nil
}

// NewSession creates a session with the TTL configured for its scope. A zero
// or negative TTL means no expiry.
func (r *Registry) NewSession(
	ctx context.Context,
	identityID, loginID string,
	scope Scope,
) (*Session, string, error) {
	return r.NewSessionTTL(ctx, identityID, loginID, scope, r.ttlForScope(scope))
}

// NewSessionTTL creates a session with an explicit TTL. ttl <= 0 means the
// session never expires.
func (r *Registry) NewSessionTTL(
	ctx context.Context,
	identityID, loginID string,
	scope Scope,
	ttl time.Duration,
) (*Session, string, error) {
	if !validScope(scope) {
		return nil, "", fmt.Errorf("%w: bad session scope", ErrInvalidInput)
	}
	token, err := r.newToken()
	if err != nil {
		return nil, "", err
	}
	hash := hashToken(token)
	nowTime := r.now().UTC()
	now := FormatTimestamp(nowTime)
	var expiresAt any
	var expiresValue *time.Time
	if ttl > 0 {
		expiry := nowTime.Add(ttl)
		expiresAt = FormatTimestamp(expiry)
		expiresValue = &expiry
	}
	if _, err := r.db.ExecContext(ctx,
		`INSERT INTO sessions (hash, identity_id, login_id, scope, created_at, expires_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		hash, identityID, loginID, string(scope), now, expiresAt,
	); err != nil {
		return nil, "", fmt.Errorf("insert session: %w", err)
	}
	return &Session{
		Hash:       hash,
		IdentityID: identityID,
		LoginID:    loginID,
		Scope:      scope,
		CreatedAt:  nowTime,
		ExpiresAt:  expiresValue,
	}, token, nil
}

// Authenticate looks a token up by hash and checks revocation, expiry, the
// identity's disabled state and the requested scope. The database is read on
// every call, so revocation takes effect on the next request.
func (r *Registry) Authenticate(
	ctx context.Context,
	token string,
	scope Scope,
) (*Identity, *Session, error) {
	if token == "" || !validScope(scope) {
		return nil, nil, ErrSessionInvalid
	}
	hash := hashToken(token)
	var (
		session    Session
		scopeValue string
		createdAt  string
		lastUsed   sql.NullString
		expiresAt  sql.NullString
		revokedAt  sql.NullString
		identity   Identity
		idCreated  string
		disabledAt sql.NullString
		isAdmin    bool
	)
	err := r.db.QueryRowContext(ctx,
		`SELECT s.hash, s.identity_id, s.login_id, s.scope, s.created_at,
		        s.last_used_at, s.expires_at, s.revoked_at,
		        i.id, i.display_name, COALESCE(i.invite_id, ''), i.created_at,
		        i.disabled_at,
		        EXISTS (
		          SELECT 1 FROM identity_roles r
		          WHERE r.identity_id = i.id AND r.role = ? AND r.revoked_at IS NULL
		        )
		 FROM sessions s
		 JOIN identities i ON i.id = s.identity_id
		 WHERE s.hash = ?`,
		RoleAdmin, hash,
	).Scan(
		&session.Hash, &session.IdentityID, &session.LoginID, &scopeValue,
		&createdAt, &lastUsed, &expiresAt, &revokedAt,
		&identity.ID, &identity.DisplayName, &identity.InviteID, &idCreated,
		&disabledAt, &isAdmin,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil, ErrSessionInvalid
	}
	if err != nil {
		return nil, nil, fmt.Errorf("read session: %w", err)
	}

	session.Scope = Scope(scopeValue)
	if session.CreatedAt, err = scanTime(createdAt); err != nil {
		return nil, nil, err
	}
	if session.LastUsedAt, err = nullableTime(lastUsed); err != nil {
		return nil, nil, err
	}
	if session.ExpiresAt, err = nullableTime(expiresAt); err != nil {
		return nil, nil, err
	}
	if session.RevokedAt, err = nullableTime(revokedAt); err != nil {
		return nil, nil, err
	}
	if identity.CreatedAt, err = scanTime(idCreated); err != nil {
		return nil, nil, err
	}
	if identity.DisabledAt, err = nullableTime(disabledAt); err != nil {
		return nil, nil, err
	}

	now := r.now().UTC()
	if !session.ActiveAt(now) || identity.Disabled() || session.Scope != scope {
		return nil, nil, ErrSessionInvalid
	}
	if scope == ScopeAPI && !isAdmin {
		return nil, nil, ErrSessionInvalid
	}
	_, _ = r.db.ExecContext(ctx,
		`UPDATE sessions SET last_used_at = ? WHERE hash = ?`,
		FormatTimestamp(now), hash,
	)
	return &identity, &session, nil
}

// RevokeSession revokes one session token.
func (r *Registry) RevokeSession(ctx context.Context, token string) error {
	if token == "" {
		return ErrSessionInvalid
	}
	res, err := r.db.ExecContext(ctx,
		`UPDATE sessions SET revoked_at = ?
		 WHERE hash = ? AND revoked_at IS NULL`,
		r.timestamp(), hashToken(token),
	)
	if err != nil {
		return fmt.Errorf("revoke session: %w", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("revoke session rows: %w", err)
	}
	if affected == 0 {
		return ErrNotFound
	}
	return nil
}

// insertSessionTx writes a session inside an existing transaction and returns
// the raw token.
func (r *Registry) insertSessionTx(
	ctx context.Context,
	tx *sql.Tx,
	identityID, loginID string,
	scope Scope,
	ttl time.Duration,
) (*Session, string, error) {
	token, err := r.newToken()
	if err != nil {
		return nil, "", err
	}
	hash := hashToken(token)
	nowTime := r.now().UTC()
	var expiresAt any
	var expiresValue *time.Time
	if ttl > 0 {
		expiry := nowTime.Add(ttl)
		expiresAt = FormatTimestamp(expiry)
		expiresValue = &expiry
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO sessions (hash, identity_id, login_id, scope, created_at, expires_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		hash, identityID, loginID, string(scope), FormatTimestamp(nowTime), expiresAt,
	); err != nil {
		return nil, "", fmt.Errorf("insert session: %w", err)
	}
	return &Session{
		Hash:       hash,
		IdentityID: identityID,
		LoginID:    loginID,
		Scope:      scope,
		CreatedAt:  nowTime,
		ExpiresAt:  expiresValue,
	}, token, nil
}
