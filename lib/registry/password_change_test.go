package registry_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Napam/beaverdream/lib/registry"
)

func TestPasswordChangeKeepsOnlyCurrentAppSession(t *testing.T) {
	ctx := context.Background()
	reg := newRegistry(t, &fakeClock{t: time.Now().UTC()})
	admin := newAdmin(t, reg)
	member, err := reg.CreateIdentityWithPassword(ctx, "member", "old-password", "Member")
	if err != nil {
		t.Fatal(err)
	}
	if err := reg.GrantAdmin(ctx, admin.IdentityID, member.ID); err != nil {
		t.Fatal(err)
	}
	_, current, token, err := reg.Login(ctx, "member", "old-password")
	if err != nil {
		t.Fatal(err)
	}
	var sessions []*registry.Session
	var tokens []string
	for _, scope := range []registry.Scope{registry.ScopeApp, registry.ScopePlay, registry.ScopeAPI} {
		session, token, err := reg.NewSession(ctx, member.ID, current.LoginID, scope)
		if err != nil {
			t.Fatal(err)
		}
		sessions, tokens = append(sessions, session), append(tokens, token)
	}
	world, err := reg.CreateWorldRecord(ctx, "world", "heroes", admin.IdentityID)
	if err != nil {
		t.Fatal(err)
	}
	if err := reg.SetWorldStatus(ctx, world.ID, registry.WorldReady); err != nil {
		t.Fatal(err)
	}
	handoff, _, err := reg.CreateHandoff(ctx, member.ID, current.LoginID, current.Hash, world.ID)
	if err != nil {
		t.Fatal(err)
	}
	reset, _, err := reg.CreatePasswordReset(ctx, admin.IdentityID, member.ID)
	if err != nil {
		t.Fatal(err)
	}
	revoked, err := reg.ChangePassword(
		ctx,
		member.ID,
		current.LoginID,
		current.Hash,
		"old-password",
		"new-password",
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(revoked) != len(sessions) || slices.Contains(revoked, current.Hash) {
		t.Fatalf("revoked = %v", revoked)
	}
	for i, session := range sessions {
		if !slices.Contains(revoked, session.Hash) {
			t.Errorf("missing revoked hash %s", session.Hash)
		}
		if _, _, err := reg.Authenticate(
			ctx,
			tokens[i],
			session.Scope,
		); !errors.Is(
			err,
			registry.ErrSessionInvalid,
		) {
			t.Errorf("other %s session = %v", session.Scope, err)
		}
	}
	if _, _, err := reg.Authenticate(ctx, token, registry.ScopeApp); err != nil {
		t.Fatalf("retained session: %v", err)
	}
	if _, _, _, err := reg.Login(
		ctx,
		"member",
		"old-password",
	); !errors.Is(
		err,
		registry.ErrInvalidCredentials,
	) {
		t.Fatalf("old password = %v", err)
	}
	if _, _, _, err := reg.Login(ctx, "member", "new-password"); err != nil {
		t.Fatalf("new password: %v", err)
	}
	if _, _, _, _, err := reg.ExchangeHandoff(
		ctx,
		handoff,
	); !errors.Is(
		err,
		registry.ErrHandoffInvalid,
	) {
		t.Fatalf("old handoff = %v", err)
	}
	if valid, err := reg.PasswordResetValid(ctx, reset); err != nil || valid {
		t.Fatalf("old reset valid = %v, %v", valid, err)
	}
	if _, err := reg.ResetPassword(
		ctx,
		reset,
		"reset-password",
	); !errors.Is(
		err,
		registry.ErrResetInvalid,
	) {
		t.Fatalf("old reset redemption = %v", err)
	}
	if _, err := reg.ChangePassword(
		ctx,
		member.ID,
		current.LoginID,
		current.Hash,
		"old-password",
		"another-password",
	); !errors.Is(
		err,
		registry.ErrInvalidCredentials,
	) {
		t.Fatalf("repeat with old password = %v", err)
	}
}

func TestPasswordChangeRefusals(t *testing.T) {
	for _, name := range []string{"wrong-password", "short", "long", "disabled", "system", "loginless", "identity", "login", "session", "play", "expired", "revoked"} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			clock := &fakeClock{t: time.Now().UTC()}
			reg := newRegistry(t, clock)
			admin := newAdmin(t, reg)
			member, err := reg.CreateIdentityWithPassword(ctx, "member", "old-password", "Member")
			if err != nil {
				t.Fatal(err)
			}
			_, session, token, err := reg.Login(ctx, "member", "old-password")
			if err != nil {
				t.Fatal(err)
			}
			id, login, hash, current, next := member.ID, session.LoginID, session.Hash, "old-password", "new-password"
			want := registry.ErrForbidden
			switch name {
			case "wrong-password":
				current, want = "wrong", registry.ErrInvalidCredentials
			case "short":
				next, want = "short", registry.ErrInvalidInput
			case "long":
				next, want = strings.Repeat("x", 1025), registry.ErrInvalidInput
			case "disabled":
				if err := reg.DisableIdentity(ctx, admin.IdentityID, member.ID); err != nil {
					t.Fatal(err)
				}
			case "system":
				id = registry.SystemAdminID
			case "loginless":
				if _, err := reg.DB().
					ExecContext(ctx, `INSERT INTO identities (id) VALUES ('loginless')`); err != nil {
					t.Fatal(err)
				}
				id = "loginless"
			case "identity":
				id = "wrong"
			case "login":
				login = "wrong"
			case "session":
				hash, want = "wrong", registry.ErrSessionInvalid
			case "play":
				play, _, err := reg.NewSession(ctx, id, login, registry.ScopePlay)
				if err != nil {
					t.Fatal(err)
				}
				hash, want = play.Hash, registry.ErrSessionInvalid
			case "expired":
				clock.Set(*session.ExpiresAt)
				want = registry.ErrSessionInvalid
			case "revoked":
				if err := reg.RevokeSession(ctx, token); err != nil {
					t.Fatal(err)
				}
				want = registry.ErrSessionInvalid
			}
			if revoked, err := reg.ChangePassword(
				ctx,
				id,
				login,
				hash,
				current,
				next,
			); !errors.Is(err, want) ||
				len(revoked) != 0 {
				t.Fatalf("change = %v, %v; want %v", revoked, err, want)
			}
			var stored string
			if err := reg.DB().
				QueryRowContext(ctx, `SELECT secret_hash FROM logins WHERE id = ?`, session.LoginID).
				Scan(&stored); err != nil {
				t.Fatal(err)
			}
			if !registry.NewArgon2id(1, 1024, 1).Verify("old-password", stored) {
				t.Fatal("refusal changed the password")
			}
		})
	}
}

func TestPasswordChangeRechecksSessionAndHash(t *testing.T) {
	for _, action := range []string{"reset", "revoke", "change"} {
		t.Run(action, func(t *testing.T) {
			ctx := context.Background()
			hasher := &blockingHasher{
				inner:   registry.NewArgon2id(1, 1024, 1),
				started: make(chan struct{}),
				release: make(chan struct{}),
			}
			defer hasher.releaseVerify()
			reg := newRegistryWithHasher(t, &fakeClock{t: time.Now().UTC()}, hasher)
			admin := newAdmin(t, reg)
			member, err := reg.CreateIdentityWithPassword(ctx, "member", "old-password", "Member")
			if err != nil {
				t.Fatal(err)
			}
			var login string
			if err := reg.DB().
				QueryRowContext(ctx, `SELECT id FROM logins WHERE identity_id = ?`, member.ID).
				Scan(&login); err != nil {
				t.Fatal(err)
			}
			session, token, err := reg.NewSession(ctx, member.ID, login, registry.ScopeApp)
			if err != nil {
				t.Fatal(err)
			}
			errs := make(chan error, 1)
			go func() {
				_, err := reg.ChangePassword(
					ctx,
					member.ID,
					login,
					session.Hash,
					"old-password",
					"loser-password",
				)
				errs <- err
			}()
			select {
			case <-hasher.started:
			case <-time.After(3 * time.Second):
				t.Fatal("change never reached Verify")
			}
			want := registry.ErrSessionInvalid
			switch action {
			case "reset":
				code, _, err := reg.CreatePasswordReset(ctx, admin.IdentityID, member.ID)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := reg.ResetPassword(ctx, code, "winner-password"); err != nil {
					t.Fatal(err)
				}
			case "revoke":
				if err := reg.RevokeSession(ctx, token); err != nil {
					t.Fatal(err)
				}
			case "change":
				winner, err := hasher.Hash("winner-password")
				if err != nil {
					t.Fatal(err)
				}
				if _, err := reg.DB().
					ExecContext(ctx, `UPDATE logins SET secret_hash = ? WHERE id = ?`, winner, login); err != nil {
					t.Fatal(err)
				}
				want = registry.ErrInvalidCredentials
			}
			hasher.releaseVerify()
			select {
			case err := <-errs:
				if !errors.Is(err, want) {
					t.Fatalf("raced change = %v", err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("change did not finish")
			}
			password := "winner-password"
			if action == "revoke" {
				password = "old-password"
			}
			if _, _, _, err := reg.Login(ctx, "member", password); err != nil {
				t.Fatalf("winning password: %v", err)
			}
			if _, _, _, err := reg.Login(
				ctx,
				"member",
				"loser-password",
			); !errors.Is(
				err,
				registry.ErrInvalidCredentials,
			) {
				t.Fatalf("losing password = %v", err)
			}
		})
	}
}

func TestPasswordChangeHashFailureWritesNothing(t *testing.T) {
	ctx := context.Background()
	reg := newRegistryWithHasher(
		t,
		&fakeClock{t: time.Now().UTC()},
		failingHasher{inner: registry.NewArgon2id(1, 1024, 1)},
	)
	admin := newAdmin(t, reg)
	member, err := reg.CreateIdentityWithPassword(ctx, "member", "old-password", "Member")
	if err != nil {
		t.Fatal(err)
	}
	_, session, token, err := reg.Login(ctx, "member", "old-password")
	if err != nil {
		t.Fatal(err)
	}
	_, other, err := reg.NewSession(ctx, member.ID, session.LoginID, registry.ScopePlay)
	if err != nil {
		t.Fatal(err)
	}
	world, err := reg.CreateWorldRecord(ctx, "w1", "heroes", admin.IdentityID)
	if err != nil {
		t.Fatal(err)
	}
	if err := reg.SetWorldStatus(ctx, world.ID, registry.WorldReady); err != nil {
		t.Fatal(err)
	}
	handoff, _, err := reg.CreateHandoff(ctx, member.ID, session.LoginID, session.Hash, world.ID)
	if err != nil {
		t.Fatal(err)
	}
	code, _, err := reg.CreatePasswordReset(ctx, admin.IdentityID, member.ID)
	if err != nil {
		t.Fatal(err)
	}
	if revoked, err := reg.ChangePassword(
		ctx,
		member.ID,
		session.LoginID,
		session.Hash,
		"old-password",
		"fail-hash",
	); err == nil ||
		len(revoked) != 0 {
		t.Fatalf("hash failure = %v, %v", revoked, err)
	}
	if _, _, err := reg.Authenticate(ctx, token, registry.ScopeApp); err != nil {
		t.Fatal(err)
	}
	if _, _, err := reg.Authenticate(ctx, other, registry.ScopePlay); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := reg.Login(ctx, "member", "old-password"); err != nil {
		t.Fatal(err)
	}
	if valid, err := reg.PasswordResetValid(ctx, code); err != nil || !valid {
		t.Fatalf("reset valid = %v, %v", valid, err)
	}
	if _, _, _, _, err := reg.ExchangeHandoff(ctx, handoff); err != nil {
		t.Fatalf("handoff after failure: %v", err)
	}
}
