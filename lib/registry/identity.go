package registry

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// RoleAdmin is the only role for now.
const RoleAdmin = "ADMIN"

// BootstrapUsername is the fixed username of the bootstrap admin login.
const BootstrapUsername = "admin"

// BootstrapAdminID is the fixed identity id of the bootstrap admin. It is all
// zeroes so the account is recognizable and cannot be disabled or demoted by
// another admin.
const BootstrapAdminID = "00000000000000000000000000000000"

// Identity is one person. Display names are not unique.
type Identity struct {
	ID          string
	DisplayName string
	// InviteID is empty when no invite made the identity.
	InviteID   string
	CreatedAt  time.Time
	DisabledAt *time.Time
	// Admin reports an active ADMIN role, whether or not the identity is
	// disabled. IsAdmin is the stricter check.
	Admin bool
}

// Disabled reports whether the identity cannot sign in or play.
func (i Identity) Disabled() bool { return i.DisabledAt != nil }

// Login is one way to sign in. SecretHash is deliberately not part of this
// type: only the registry holds a password hash.
type Login struct {
	ID          string
	IdentityID  string
	Source      string
	ExternalID  string
	CreatedAt   time.Time
	LastLoginAt *time.Time
}

// BootstrapResult is what EnsureBootstrapAdmin did.
type BootstrapResult struct {
	IdentityID string
	LoginID    string
	// Password is the configured password, or the generated one when the
	// caller passed an empty string. The caller logs it; the registry never
	// writes a plaintext password anywhere.
	Password string
	// Generated is true when Password was generated for this start.
	Generated bool
	// PasswordChanged is true when this start revoked the admin's existing
	// sessions because the bootstrap password no longer matched.
	PasswordChanged bool
}

// CreateIdentityWithPassword makes a person and one password login. It is the
// path tests use outside an invite; a public signup must go through
// RedeemInvite.
func (r *Registry) CreateIdentityWithPassword(
	ctx context.Context,
	username, password, displayName string,
) (*Identity, error) {
	username, err := CleanUsername(username)
	if err != nil {
		return nil, err
	}
	if err := ValidatePassword(password); err != nil {
		return nil, err
	}
	displayName = CleanDisplayName(displayName, username)
	hash, err := r.hasher.Hash(password)
	if err != nil {
		return nil, err
	}

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin identity: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	now := r.timestamp()
	identityID, err := r.newID()
	if err != nil {
		return nil, err
	}
	loginID, err := r.newID()
	if err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO identities (id, display_name, created_at) VALUES (?, ?, ?)`,
		identityID, displayName, now,
	); err != nil {
		return nil, fmt.Errorf("insert identity: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO logins (id, identity_id, source, external_id, secret_hash, created_at)
		 VALUES (?, ?, 'password', ?, ?, ?)`,
		loginID, identityID, username, hash, now,
	); err != nil {
		if isUniqueViolation(err) {
			return nil, ErrUsernameTaken
		}
		return nil, fmt.Errorf("insert login: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit identity: %w", err)
	}
	return &Identity{
		ID:          identityID,
		DisplayName: displayName,
		CreatedAt:   r.now().UTC(),
	}, nil
}

// EnsureBootstrapAdmin makes the ADMIN_PASSWORD the truth for the admin
// identity on every start: it writes the password hash, clears disabled_at,
// grants ADMIN again, and revokes existing admin sessions when the password
// changed. A password comparison verifies the input against the old hash,
// because argon2 salts differ on every write. An admin from before
// BootstrapAdminID existed is moved to that fixed id in the same transaction,
// so its logins, sessions, roles, handoffs, invites, worlds and usage follow.
func (r *Registry) EnsureBootstrapAdmin(
	ctx context.Context,
	password string,
) (BootstrapResult, error) {
	generated := password == ""
	if generated {
		token, err := r.newToken()
		if err != nil {
			return BootstrapResult{}, err
		}
		password = token
	}
	if err := ValidatePassword(password); err != nil {
		return BootstrapResult{}, err
	}
	hash, err := r.hasher.Hash(password)
	if err != nil {
		return BootstrapResult{}, err
	}

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return BootstrapResult{}, fmt.Errorf("begin bootstrap: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	now := r.timestamp()
	var (
		identityID string
		loginID    string
		oldHash    string
	)
	err = tx.QueryRowContext(ctx,
		`SELECT i.id, l.id, l.secret_hash
		 FROM logins l
		 JOIN identities i ON i.id = l.identity_id
		 WHERE l.source = 'password' AND l.external_id = ?`,
		BootstrapUsername,
	).Scan(&identityID, &loginID, &oldHash)
	changed := false
	switch {
	case errors.Is(err, sql.ErrNoRows):
		identityID = BootstrapAdminID
		loginID, err = r.newID()
		if err != nil {
			return BootstrapResult{}, err
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO identities (id, display_name, created_at) VALUES (?, ?, ?)`,
			identityID, BootstrapUsername, now,
		); err != nil {
			return BootstrapResult{}, fmt.Errorf("insert admin identity: %w", err)
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO logins (id, identity_id, source, external_id, secret_hash, created_at)
			 VALUES (?, ?, 'password', ?, ?, ?)`,
			loginID, identityID, BootstrapUsername, hash, now,
		); err != nil {
			return BootstrapResult{}, fmt.Errorf("insert admin login: %w", err)
		}
	case err != nil:
		return BootstrapResult{}, fmt.Errorf("read admin login: %w", err)
	default:
		if identityID != BootstrapAdminID {
			if err := rekeyIdentityTx(ctx, tx, identityID, BootstrapAdminID); err != nil {
				return BootstrapResult{}, err
			}
			identityID = BootstrapAdminID
		}
		changed = !r.hasher.Verify(password, oldHash)
		if changed {
			if _, err := tx.ExecContext(ctx,
				`UPDATE sessions SET revoked_at = ?
				 WHERE identity_id = ? AND revoked_at IS NULL`,
				now, identityID,
			); err != nil {
				return BootstrapResult{}, fmt.Errorf("revoke admin sessions: %w", err)
			}
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE logins SET secret_hash = ? WHERE id = ?`, hash, loginID,
		); err != nil {
			return BootstrapResult{}, fmt.Errorf("update admin password: %w", err)
		}
	}

	if _, err := tx.ExecContext(ctx,
		`UPDATE identities
		 SET disabled_at = NULL, display_name = CASE
		   WHEN display_name = '' THEN ? ELSE display_name END
		 WHERE id = ?`,
		BootstrapUsername, identityID,
	); err != nil {
		return BootstrapResult{}, fmt.Errorf("restore admin identity: %w", err)
	}
	if err := grantRoleTx(ctx, tx, identityID, RoleAdmin, now); err != nil {
		return BootstrapResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return BootstrapResult{}, fmt.Errorf("commit bootstrap: %w", err)
	}
	return BootstrapResult{
		IdentityID:      identityID,
		LoginID:         loginID,
		Password:        password,
		Generated:       generated,
		PasswordChanged: changed,
	}, nil
}

// rekeyIdentityTx moves an identity to a new id. The new row is inserted
// first, then every reference is updated, then the old row goes, so foreign
// keys stay satisfied without turning enforcement off.
func rekeyIdentityTx(
	ctx context.Context,
	tx *sql.Tx,
	oldID, newID string,
) error {
	var exists int
	err := tx.QueryRowContext(ctx, `SELECT 1 FROM identities WHERE id = ?`, newID).Scan(&exists)
	if err == nil {
		return fmt.Errorf("%w: identity %q already exists", ErrInvalidInput, newID)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("read target identity: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO identities (id, display_name, invite_id, created_at, disabled_at)
		 SELECT ?, display_name, invite_id, created_at, disabled_at
		 FROM identities WHERE id = ?`,
		newID, oldID,
	); err != nil {
		return fmt.Errorf("copy identity: %w", err)
	}
	for _, query := range []string{
		`UPDATE logins SET identity_id = ? WHERE identity_id = ?`,
		`UPDATE sessions SET identity_id = ? WHERE identity_id = ?`,
		`UPDATE identity_roles SET identity_id = ? WHERE identity_id = ?`,
		`UPDATE handoffs SET identity_id = ? WHERE identity_id = ?`,
		`UPDATE invites SET created_by = ? WHERE created_by = ?`,
		`UPDATE worlds SET created_by = ? WHERE created_by = ?`,
		`UPDATE usage SET identity_id = ? WHERE identity_id = ?`,
	} {
		if _, err := tx.ExecContext(ctx, query, newID, oldID); err != nil {
			return fmt.Errorf("move identity references: %w", err)
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM identities WHERE id = ?`, oldID); err != nil {
		return fmt.Errorf("delete old identity: %w", err)
	}
	return nil
}

// GrantRole gives an identity an active role, unless it already has one.
func (r *Registry) GrantRole(ctx context.Context, identityID, role string) error {
	if err := validateRole(role); err != nil {
		return err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin grant role: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := grantRoleTx(ctx, tx, identityID, role, r.timestamp()); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit grant role: %w", err)
	}
	return nil
}

// GrantAdmin gives an identity the ADMIN role. actorID must be an active
// admin.
func (r *Registry) GrantAdmin(ctx context.Context, actorID, identityID string) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin grant admin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := requireAdminTx(ctx, tx, actorID); err != nil {
		return err
	}
	if err := requireIdentityTx(ctx, tx, identityID); err != nil {
		return err
	}
	if err := grantRoleTx(ctx, tx, identityID, RoleAdmin, r.timestamp()); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit grant admin: %w", err)
	}
	return nil
}

// RevokeAdmin removes the ADMIN role. The bootstrap admin keeps it: no other
// account can demote the zero id.
func (r *Registry) RevokeAdmin(ctx context.Context, actorID, identityID string) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin revoke admin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := requireAdminTx(ctx, tx, actorID); err != nil {
		return err
	}
	if identityID == BootstrapAdminID {
		return fmt.Errorf("%w: the bootstrap admin keeps its role", ErrForbidden)
	}
	if err := requireIdentityTx(ctx, tx, identityID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE identity_roles SET revoked_at = ?
		 WHERE identity_id = ? AND role = ? AND revoked_at IS NULL`,
		r.timestamp(), identityID, RoleAdmin,
	); err != nil {
		return fmt.Errorf("revoke admin: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit revoke admin: %w", err)
	}
	return nil
}

func requireIdentityTx(ctx context.Context, tx *sql.Tx, identityID string) error {
	var exists int
	err := tx.QueryRowContext(ctx,
		`SELECT 1 FROM identities WHERE id = ?`, identityID,
	).Scan(&exists)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("read identity: %w", err)
	}
	return nil
}

func grantRoleTx(
	ctx context.Context,
	tx *sql.Tx,
	identityID, role, now string,
) error {
	var exists int
	err := tx.QueryRowContext(ctx,
		`SELECT 1 FROM identity_roles
		 WHERE identity_id = ? AND role = ? AND revoked_at IS NULL`,
		identityID, role,
	).Scan(&exists)
	if err == nil {
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("read role: %w", err)
	}
	id, err := randomID()
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO identity_roles (id, identity_id, role, granted_at)
		 VALUES (?, ?, ?, ?)`,
		id, identityID, role, now,
	); err != nil {
		return fmt.Errorf("insert role: %w", err)
	}
	return nil
}

func validateRole(role string) error {
	if role != RoleAdmin {
		return fmt.Errorf("%w: unknown role %q", ErrInvalidInput, role)
	}
	return nil
}

// IsAdmin reports whether the identity has an active ADMIN role and is not
// disabled.
func (r *Registry) IsAdmin(ctx context.Context, identityID string) (bool, error) {
	if identityID == "" {
		return false, nil
	}
	var found int
	err := r.db.QueryRowContext(ctx,
		`SELECT 1
		 FROM identities i
		 WHERE i.id = ? AND i.disabled_at IS NULL
		   AND EXISTS (
		     SELECT 1 FROM identity_roles r
		     WHERE r.identity_id = i.id AND r.role = ? AND r.revoked_at IS NULL
		   )`,
		identityID, RoleAdmin,
	).Scan(&found)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read admin role: %w", err)
	}
	return true, nil
}

// RequireAdmin returns ErrForbidden unless identityID is an active admin.
func (r *Registry) RequireAdmin(ctx context.Context, identityID string) error {
	admin, err := r.IsAdmin(ctx, identityID)
	if err != nil {
		return err
	}
	if !admin {
		return ErrForbidden
	}
	return nil
}

// requireAdminTx is RequireAdmin inside the write transaction, so a concurrent
// demotion cannot land between the check and the change.
func requireAdminTx(ctx context.Context, tx *sql.Tx, identityID string) error {
	var found int
	err := tx.QueryRowContext(ctx,
		`SELECT 1
		 FROM identities i
		 WHERE i.id = ? AND i.disabled_at IS NULL
		   AND EXISTS (
		     SELECT 1 FROM identity_roles r
		     WHERE r.identity_id = i.id AND r.role = ? AND r.revoked_at IS NULL
		   )`,
		identityID, RoleAdmin,
	).Scan(&found)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrForbidden
	}
	if err != nil {
		return fmt.Errorf("read admin role: %w", err)
	}
	return nil
}

// DisableIdentity marks an identity disabled, revokes every session it holds
// and keeps its roles. Only an admin may disable an identity. The bootstrap
// admin is protected even from itself; the next bootstrap would restore it,
// so refusing the write keeps the zero id always usable.
func (r *Registry) DisableIdentity(ctx context.Context, actorID, identityID string) error {
	now := r.timestamp()
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin disable identity: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := requireAdminTx(ctx, tx, actorID); err != nil {
		return err
	}
	if identityID == BootstrapAdminID {
		return fmt.Errorf("%w: the bootstrap admin cannot be disabled", ErrForbidden)
	}
	res, err := tx.ExecContext(ctx,
		`UPDATE identities SET disabled_at = COALESCE(disabled_at, ?) WHERE id = ?`,
		now, identityID,
	)
	if err != nil {
		return fmt.Errorf("disable identity: %w", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("disable identity rows: %w", err)
	}
	if affected == 0 {
		return ErrNotFound
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE sessions SET revoked_at = ?
		 WHERE identity_id = ? AND revoked_at IS NULL`,
		now, identityID,
	); err != nil {
		return fmt.Errorf("revoke identity sessions: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit disable identity: %w", err)
	}
	return nil
}

// RevokeAllSessions logs an identity out everywhere.
func (r *Registry) RevokeAllSessions(ctx context.Context, identityID string) error {
	_, err := r.db.ExecContext(ctx,
		`UPDATE sessions SET revoked_at = ?
		 WHERE identity_id = ? AND revoked_at IS NULL`,
		r.timestamp(), identityID,
	)
	if err != nil {
		return fmt.Errorf("revoke all sessions: %w", err)
	}
	return nil
}

// GetIdentity reads one identity.
func (r *Registry) GetIdentity(ctx context.Context, identityID string) (*Identity, error) {
	row := r.db.QueryRowContext(ctx,
		`SELECT i.id, i.display_name, COALESCE(i.invite_id, ''), i.created_at,
		   i.disabled_at,
		   EXISTS (
		     SELECT 1 FROM identity_roles r
		     WHERE r.identity_id = i.id AND r.role = ? AND r.revoked_at IS NULL
		   )
		 FROM identities i WHERE i.id = ?`,
		RoleAdmin, identityID,
	)
	identity, err := scanIdentity(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("read identity: %w", err)
	}
	return identity, nil
}

// ListIdentities returns every identity for the admin page, newest first.
func (r *Registry) ListIdentities(ctx context.Context, actorID string) ([]Identity, error) {
	if err := r.RequireAdmin(ctx, actorID); err != nil {
		return nil, err
	}
	rows, err := r.db.QueryContext(ctx,
		`SELECT i.id, i.display_name, COALESCE(i.invite_id, ''), i.created_at,
		   i.disabled_at,
		   EXISTS (
		     SELECT 1 FROM identity_roles r
		     WHERE r.identity_id = i.id AND r.role = ? AND r.revoked_at IS NULL
		   )
		 FROM identities i ORDER BY i.created_at DESC, i.id DESC`,
		RoleAdmin,
	)
	if err != nil {
		return nil, fmt.Errorf("list identities: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var identities []Identity
	for rows.Next() {
		identity, err := scanIdentity(rows)
		if err != nil {
			return nil, fmt.Errorf("list identities row: %w", err)
		}
		identities = append(identities, *identity)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list identities rows: %w", err)
	}
	return identities, nil
}

// SetDisplayName changes an identity's own display name. Names stay
// non-unique.
func (r *Registry) SetDisplayName(ctx context.Context, identityID, name string) error {
	name = CleanDisplayName(name, "")
	if name == "" {
		return fmt.Errorf("%w: display name is empty", ErrInvalidInput)
	}
	res, err := r.db.ExecContext(ctx,
		`UPDATE identities SET display_name = ? WHERE id = ? AND disabled_at IS NULL`,
		name, identityID,
	)
	if err != nil {
		return fmt.Errorf("set display name: %w", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("set display name rows: %w", err)
	}
	if affected == 0 {
		return ErrNotFound
	}
	return nil
}

// CleanUsername trims and validates a username. Usernames are stored as typed
// but compared exactly; they are not case-folded.
func CleanUsername(value string) (string, error) {
	value = strings.TrimSpace(value)
	if n := utf8.RuneCountInString(value); n < 3 || n > 32 {
		return "", fmt.Errorf("%w: username must be 3 to 32 characters", ErrInvalidInput)
	}
	for i, r := range value {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case i > 0 && (r == '_' || r == '-' || r == '.'):
		default:
			return "", fmt.Errorf("%w: username has a bad character", ErrInvalidInput)
		}
	}
	return value, nil
}

// ValidatePassword requires at least eight bytes, at most one kilobyte.
func ValidatePassword(password string) error {
	if len(password) < 8 {
		return fmt.Errorf("%w: password must be at least 8 characters", ErrInvalidInput)
	}
	if len(password) > 1024 {
		return fmt.Errorf("%w: password is too long", ErrInvalidInput)
	}
	return nil
}

// CleanDisplayName trims, removes control characters, collapses whitespace and
// caps the name. An empty name falls back to the username.
func CleanDisplayName(value, fallback string) string {
	value = strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' || r == '\t' {
			return ' '
		}
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, value)
	value = strings.Join(strings.Fields(value), " ")
	if value == "" {
		value = fallback
	}
	runes := []rune(value)
	if len(runes) > 64 {
		value = string(runes[:64])
	}
	return value
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanIdentity(row rowScanner) (*Identity, error) {
	var (
		identity   Identity
		createdAt  string
		disabledAt sql.NullString
		admin      int
	)
	if err := row.Scan(
		&identity.ID,
		&identity.DisplayName,
		&identity.InviteID,
		&createdAt,
		&disabledAt,
		&admin,
	); err != nil {
		return nil, err
	}
	identity.Admin = admin != 0
	t, err := scanTime(createdAt)
	if err != nil {
		return nil, err
	}
	identity.CreatedAt = t
	disabled, err := nullableTime(disabledAt)
	if err != nil {
		return nil, err
	}
	identity.DisabledAt = disabled
	return &identity, nil
}

func randomID() (string, error) {
	// Roles only need a stable unique row id; reuse the registry id shape.
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", fmt.Errorf("random id: %w", err)
	}
	return fmt.Sprintf("%x", buf), nil
}
