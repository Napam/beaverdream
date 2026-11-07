package registry

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

// Invite is one redeemable link. The code itself is never stored or returned
// again; CreateInvite returns it once.
type Invite struct {
	ID        string
	Label     string
	CreatedBy string
	MaxUses   int
	Uses      int
	ExpiresAt *time.Time
	RevokedAt *time.Time
	CreatedAt time.Time
}

// InviteParams describes a new invite.
type InviteParams struct {
	Label     string
	MaxUses   int
	ExpiresAt *time.Time
}

// CreateInvite makes an invite and returns its code. Only an admin may call
// it.
func (r *Registry) CreateInvite(
	ctx context.Context,
	actorID string,
	params InviteParams,
) (*Invite, string, error) {
	if err := r.RequireAdmin(ctx, actorID); err != nil {
		return nil, "", err
	}
	if params.MaxUses <= 0 || params.MaxUses > 100000 {
		return nil, "", fmt.Errorf("%w: max uses must be 1 to 100000", ErrInvalidInput)
	}
	label := strings.Join(strings.Fields(params.Label), " ")
	if utf8.RuneCountInString(label) > 120 {
		label = string([]rune(label)[:120])
	}
	code, err := r.newToken()
	if err != nil {
		return nil, "", err
	}
	id, err := r.newID()
	if err != nil {
		return nil, "", err
	}
	nowTime := r.now().UTC()
	var expiresAt any
	if params.ExpiresAt != nil {
		expiresAt = FormatTimestamp(*params.ExpiresAt)
	}
	if _, err := r.db.ExecContext(ctx,
		`INSERT INTO invites
		   (id, code_hash, label, created_by, max_uses, expires_at, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		id, hashToken(code), label, actorID, params.MaxUses, expiresAt,
		FormatTimestamp(nowTime),
	); err != nil {
		return nil, "", fmt.Errorf("insert invite: %w", err)
	}
	return &Invite{
		ID:        id,
		Label:     label,
		CreatedBy: actorID,
		MaxUses:   params.MaxUses,
		ExpiresAt: params.ExpiresAt,
		CreatedAt: nowTime,
	}, code, nil
}

// RevokeInvite stops future redemptions and leaves the row for the list.
func (r *Registry) RevokeInvite(ctx context.Context, actorID, inviteID string) error {
	if err := r.RequireAdmin(ctx, actorID); err != nil {
		return err
	}
	res, err := r.db.ExecContext(ctx,
		`UPDATE invites SET revoked_at = COALESCE(revoked_at, ?) WHERE id = ?`,
		r.timestamp(), inviteID,
	)
	if err != nil {
		return fmt.Errorf("revoke invite: %w", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("revoke invite rows: %w", err)
	}
	if affected == 0 {
		return ErrNotFound
	}
	return nil
}

// ListInvites returns every invite, newest first, for the admin page.
func (r *Registry) ListInvites(ctx context.Context, actorID string) ([]Invite, error) {
	if err := r.RequireAdmin(ctx, actorID); err != nil {
		return nil, err
	}
	rows, err := r.db.QueryContext(ctx,
		`SELECT id, label, COALESCE(created_by, ''), max_uses, uses,
		        expires_at, revoked_at, created_at
		 FROM invites ORDER BY created_at DESC, id DESC`,
	)
	if err != nil {
		return nil, fmt.Errorf("list invites: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var invites []Invite
	for rows.Next() {
		invite, err := scanInvite(rows)
		if err != nil {
			return nil, err
		}
		invites = append(invites, *invite)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list invites rows: %w", err)
	}
	return invites, nil
}

// RedeemInvite spends one use and creates the identity, its password login and
// its first app session in one immediate transaction. A taken username rolls
// the use back with it.
//
// Every failure of the code itself is ErrInviteInvalid, so a redeemer cannot
// learn whether it was unknown, expired, used up or revoked.
func (r *Registry) RedeemInvite(
	ctx context.Context,
	code, username, password, displayName string,
) (*Identity, *Session, string, error) {
	username, err := CleanUsername(username)
	if err != nil {
		return nil, nil, "", err
	}
	if err := ValidatePassword(password); err != nil {
		return nil, nil, "", err
	}
	displayName = CleanDisplayName(displayName, username)
	hash, err := r.hasher.Hash(password)
	if err != nil {
		return nil, nil, "", err
	}

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, nil, "", fmt.Errorf("begin redeem: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	nowTime := r.now().UTC()
	now := FormatTimestamp(nowTime)

	var inviteID string
	err = tx.QueryRowContext(ctx,
		`UPDATE invites SET uses = uses + 1
		 WHERE code_hash = ?
		   AND revoked_at IS NULL
		   AND uses < max_uses
		   AND (expires_at IS NULL OR expires_at > ?)
		 RETURNING id`,
		hashToken(code), now,
	).Scan(&inviteID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil, "", ErrInviteInvalid
	}
	if err != nil {
		return nil, nil, "", fmt.Errorf("consume invite: %w", err)
	}

	var taken int
	err = tx.QueryRowContext(ctx,
		`SELECT 1 FROM logins WHERE source = 'password' AND external_id = ?`,
		username,
	).Scan(&taken)
	if err == nil {
		return nil, nil, "", ErrUsernameTaken
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, nil, "", fmt.Errorf("check username: %w", err)
	}

	identityID, err := r.newID()
	if err != nil {
		return nil, nil, "", err
	}
	loginID, err := r.newID()
	if err != nil {
		return nil, nil, "", err
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO identities (id, display_name, invite_id, created_at)
		 VALUES (?, ?, ?, ?)`,
		identityID, displayName, inviteID, now,
	); err != nil {
		return nil, nil, "", fmt.Errorf("insert invited identity: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO logins (id, identity_id, source, external_id, secret_hash, created_at)
		 VALUES (?, ?, 'password', ?, ?, ?)`,
		loginID, identityID, username, hash, now,
	); err != nil {
		if isUniqueViolation(err) {
			return nil, nil, "", ErrUsernameTaken
		}
		return nil, nil, "", fmt.Errorf("insert invited login: %w", err)
	}
	session, token, err := r.insertSessionTx(ctx, tx, identityID, loginID, ScopeApp, r.appTTL)
	if err != nil {
		return nil, nil, "", err
	}
	if err := tx.Commit(); err != nil {
		return nil, nil, "", fmt.Errorf("commit redeem: %w", err)
	}
	return &Identity{
		ID:          identityID,
		DisplayName: displayName,
		InviteID:    inviteID,
		CreatedAt:   nowTime,
	}, session, token, nil
}

func scanInvite(row rowScanner) (*Invite, error) {
	var (
		invite    Invite
		expires   sql.NullString
		revoked   sql.NullString
		createdAt string
	)
	if err := row.Scan(
		&invite.ID, &invite.Label, &invite.CreatedBy, &invite.MaxUses, &invite.Uses,
		&expires, &revoked, &createdAt,
	); err != nil {
		return nil, err
	}
	var err error
	if invite.ExpiresAt, err = nullableTime(expires); err != nil {
		return nil, err
	}
	if invite.RevokedAt, err = nullableTime(revoked); err != nil {
		return nil, err
	}
	t, err := scanTime(createdAt)
	if err != nil {
		return nil, err
	}
	invite.CreatedAt = t
	return &invite, nil
}
