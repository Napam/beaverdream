package registry_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Napam/beaverdream"
	"github.com/Napam/beaverdream/lib/registry"
)

func TestPasswordResetChangesPasswordAndRevokesAccess(t *testing.T) {
	ctx := context.Background()
	clock := &fakeClock{t: time.Date(2026, 5, 1, 2, 3, 4, 0, time.UTC)}
	reg := newRegistry(t, clock)
	admin := newAdmin(t, reg)
	member, err := reg.CreateIdentityWithPassword(
		ctx, "member", "old-password", "Member",
	)
	if err != nil {
		t.Fatalf("member: %v", err)
	}
	if err := reg.GrantAdmin(ctx, admin.IdentityID, member.ID); err != nil {
		t.Fatalf("grant member admin: %v", err)
	}
	identity, appSession, appToken, err := reg.Login(ctx, "member", "old-password")
	if err != nil {
		t.Fatalf("member login: %v", err)
	}
	if identity.ID != member.ID {
		t.Fatalf("login identity = %q", identity.ID)
	}
	_, apiToken, err := reg.CreateAPIToken(
		ctx, member.ID, appSession.LoginID, appSession.Hash,
	)
	if err != nil {
		t.Fatalf("api token: %v", err)
	}
	_, playToken, err := reg.NewSession(
		ctx, member.ID, appSession.LoginID, registry.ScopePlay,
	)
	if err != nil {
		t.Fatalf("play session: %v", err)
	}
	world, err := reg.CreateWorldRecord(ctx, "w1", "heroes", admin.IdentityID)
	if err != nil {
		t.Fatalf("world: %v", err)
	}
	if err := reg.SetWorldStatus(ctx, world.ID, registry.WorldReady); err != nil {
		t.Fatalf("ready world: %v", err)
	}
	handoff, _, err := reg.CreateHandoff(
		ctx, member.ID, appSession.LoginID, appSession.Hash, world.ID,
	)
	if err != nil {
		t.Fatalf("handoff: %v", err)
	}

	code, expires, err := reg.CreatePasswordReset(ctx, admin.IdentityID, member.ID)
	if err != nil {
		t.Fatalf("create reset: %v", err)
	}
	if !expires.Equal(clock.Now().Add(registry.DefaultPasswordResetTTL)) {
		t.Fatalf("reset expiry = %v", expires)
	}
	if valid, err := reg.PasswordResetValid(ctx, code); err != nil || !valid {
		t.Fatalf("reset valid = %v, %v", valid, err)
	}

	resetID, err := reg.ResetPassword(ctx, code, "new-password")
	if err != nil {
		t.Fatalf("reset password: %v", err)
	}
	if resetID != member.ID {
		t.Fatalf("reset identity = %q", resetID)
	}

	for name, token := range map[string]struct {
		token string
		scope registry.Scope
	}{
		"app":  {appToken, registry.ScopeApp},
		"play": {playToken, registry.ScopePlay},
		"api":  {apiToken, registry.ScopeAPI},
	} {
		if _, _, err := reg.Authenticate(ctx, token.token, token.scope); !errors.Is(
			err, registry.ErrSessionInvalid,
		) {
			t.Errorf("%s session after reset = %v", name, err)
		}
	}
	if _, _, err := reg.CreateAPIToken(
		ctx, member.ID, appSession.LoginID, appSession.Hash,
	); !errors.Is(err, registry.ErrSessionInvalid) {
		t.Fatalf("api token from revoked session = %v", err)
	}
	if _, _, _, err := reg.Login(ctx, "member", "old-password"); !errors.Is(
		err, registry.ErrInvalidCredentials,
	) {
		t.Fatalf("old password after reset = %v", err)
	}
	if _, _, _, err := reg.Login(ctx, "member", "new-password"); err != nil {
		t.Fatalf("new password after reset: %v", err)
	}
	if _, _, _, _, err := reg.ExchangeHandoff(ctx, handoff); !errors.Is(
		err, registry.ErrHandoffInvalid,
	) {
		t.Fatalf("handoff after reset = %v", err)
	}
	if _, _, err := reg.CreateHandoff(
		ctx, member.ID, appSession.LoginID, appSession.Hash, world.ID,
	); !errors.Is(err, registry.ErrSessionInvalid) {
		t.Fatalf("handoff from revoked session = %v", err)
	}
	if _, err := reg.ResetPassword(ctx, code, "another-password"); !errors.Is(
		err, registry.ErrResetInvalid,
	) {
		t.Fatalf("second reset = %v", err)
	}
}

func TestPasswordResetLinkReplacesAndExpires(t *testing.T) {
	ctx := context.Background()
	clock := &fakeClock{t: time.Date(2026, 5, 2, 3, 4, 5, 0, time.UTC)}
	reg := newRegistry(t, clock)
	admin := newAdmin(t, reg)
	member, err := reg.CreateIdentityWithPassword(
		ctx, "member", "old-password", "Member",
	)
	if err != nil {
		t.Fatalf("member: %v", err)
	}

	first, _, err := reg.CreatePasswordReset(ctx, admin.IdentityID, member.ID)
	if err != nil {
		t.Fatalf("first reset: %v", err)
	}
	second, expires, err := reg.CreatePasswordReset(ctx, admin.IdentityID, member.ID)
	if err != nil {
		t.Fatalf("second reset: %v", err)
	}
	if valid, err := reg.PasswordResetValid(ctx, first); err != nil || valid {
		t.Fatalf("old reset valid = %v, %v", valid, err)
	}
	if valid, err := reg.PasswordResetValid(ctx, second); err != nil || !valid {
		t.Fatalf("new reset valid = %v, %v", valid, err)
	}

	clock.Set(expires)
	if valid, err := reg.PasswordResetValid(ctx, second); err != nil || valid {
		t.Fatalf("reset at exact expiry = %v, %v", valid, err)
	}
	if _, err := reg.ResetPassword(ctx, second, "new-password"); !errors.Is(
		err, registry.ErrResetInvalid,
	) {
		t.Fatalf("reset at exact expiry = %v", err)
	}
}

func TestPasswordResetRefusals(t *testing.T) {
	ctx := context.Background()
	clock := &fakeClock{t: time.Date(2026, 5, 3, 4, 5, 6, 0, time.UTC)}
	reg := newRegistry(t, clock)
	admin := newAdmin(t, reg)
	member, err := reg.CreateIdentityWithPassword(
		ctx, "member", "member-password", "Member",
	)
	if err != nil {
		t.Fatalf("member: %v", err)
	}
	other, err := reg.CreateIdentityWithPassword(
		ctx, "other", "other-password", "Other",
	)
	if err != nil {
		t.Fatalf("other: %v", err)
	}
	if _, _, err := reg.CreatePasswordReset(ctx, member.ID, other.ID); !errors.Is(
		err, registry.ErrForbidden,
	) {
		t.Fatalf("non-admin issued a reset = %v", err)
	}
	if _, _, err := reg.CreatePasswordReset(
		ctx, admin.IdentityID, registry.SystemAdminID,
	); !errors.Is(err, registry.ErrForbidden) {
		t.Fatalf("system admin reset = %v", err)
	}
	if _, _, err := reg.CreatePasswordReset(
		ctx, admin.IdentityID, "missing",
	); !errors.Is(err, registry.ErrNotFound) {
		t.Fatalf("missing identity reset = %v", err)
	}
	if _, err := reg.DB().ExecContext(ctx,
		`INSERT INTO identities (id, display_name) VALUES ('loginless', 'Loginless')`,
	); err != nil {
		t.Fatalf("insert loginless identity: %v", err)
	}
	if _, _, err := reg.CreatePasswordReset(
		ctx, admin.IdentityID, "loginless",
	); !errors.Is(err, registry.ErrForbidden) {
		t.Fatalf("loginless reset = %v", err)
	}
	code, _, err := reg.CreatePasswordReset(ctx, admin.IdentityID, member.ID)
	if err != nil {
		t.Fatalf("create reset: %v", err)
	}
	if err := reg.DisableIdentity(ctx, admin.IdentityID, member.ID); err != nil {
		t.Fatalf("disable member: %v", err)
	}
	if valid, err := reg.PasswordResetValid(ctx, code); err != nil || valid {
		t.Fatalf("disabled target reset valid = %v, %v", valid, err)
	}
	if _, err := reg.ResetPassword(ctx, code, "new-password"); !errors.Is(
		err, registry.ErrResetInvalid,
	) {
		t.Fatalf("disabled target reset = %v", err)
	}
	if _, _, err := reg.CreatePasswordReset(
		ctx, admin.IdentityID, member.ID,
	); !errors.Is(err, registry.ErrForbidden) {
		t.Fatalf("disabled reset = %v", err)
	}
}

func TestPasswordResetConcurrentRedemptionHasOneWinner(t *testing.T) {
	ctx := context.Background()
	clock := &fakeClock{t: time.Date(2026, 5, 4, 5, 6, 7, 0, time.UTC)}
	reg := newRegistry(t, clock)
	admin := newAdmin(t, reg)
	member, err := reg.CreateIdentityWithPassword(
		ctx, "member", "old-password", "Member",
	)
	if err != nil {
		t.Fatalf("member: %v", err)
	}
	code, _, err := reg.CreatePasswordReset(ctx, admin.IdentityID, member.ID)
	if err != nil {
		t.Fatalf("create reset: %v", err)
	}

	passwords := []string{"first-password", "second-password"}
	errs := make(chan error, len(passwords))
	var wg sync.WaitGroup
	for _, password := range passwords {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := reg.ResetPassword(ctx, code, password)
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)

	winners, invalid := 0, 0
	for err := range errs {
		switch {
		case err == nil:
			winners++
		case errors.Is(err, registry.ErrResetInvalid):
			invalid++
		default:
			t.Fatalf("concurrent reset = %v", err)
		}
	}
	if winners != 1 || invalid != 1 {
		t.Fatalf("winners = %d, invalid = %d", winners, invalid)
	}
}

type failingHasher struct {
	inner registry.Hasher
}

func (h failingHasher) Hash(password string) (string, error) {
	if password == "fail-hash" {
		return "", errors.New("hash failure")
	}
	return h.inner.Hash(password)
}

func (h failingHasher) Verify(password, encoded string) bool {
	return h.inner.Verify(password, encoded)
}

func TestResetHashFailureKeepsCodeUsable(t *testing.T) {
	ctx := context.Background()
	clock := &fakeClock{t: time.Date(2026, 5, 6, 7, 8, 9, 0, time.UTC)}
	reg := newRegistryWithHasher(t, clock, failingHasher{
		inner: registry.NewArgon2id(1, 1024, 1),
	})
	admin := newAdmin(t, reg)
	member, err := reg.CreateIdentityWithPassword(
		ctx, "member", "old-password", "Member",
	)
	if err != nil {
		t.Fatalf("member: %v", err)
	}
	code, _, err := reg.CreatePasswordReset(ctx, admin.IdentityID, member.ID)
	if err != nil {
		t.Fatalf("create reset: %v", err)
	}
	if _, err := reg.ResetPassword(ctx, code, "fail-hash"); err == nil {
		t.Fatal("hash failure did not fail the reset")
	}
	if valid, err := reg.PasswordResetValid(ctx, code); err != nil || !valid {
		t.Fatalf("reset after hash failure = %v, %v", valid, err)
	}
	if _, err := reg.ResetPassword(ctx, code, "new-password"); err != nil {
		t.Fatalf("reset after hash failure: %v", err)
	}
}

type blockingHasher struct {
	inner   registry.Hasher
	started chan struct{}
	release chan struct{}
	once    sync.Once
	unlock  sync.Once
}

func (h *blockingHasher) Hash(password string) (string, error) {
	return h.inner.Hash(password)
}

func (h *blockingHasher) Verify(password, encoded string) bool {
	h.once.Do(func() { close(h.started) })
	<-h.release
	return h.inner.Verify(password, encoded)
}

func (h *blockingHasher) releaseVerify() {
	h.unlock.Do(func() { close(h.release) })
}

func newRegistryWithHasher(
	t *testing.T,
	clock *fakeClock,
	hasher registry.Hasher,
) *registry.Registry {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	reg, err := registry.Open(
		context.Background(),
		filepath.Join(t.TempDir(), "registry.db"),
		beaverdream.RegistryMigrationsFS,
		logger,
		registry.WithHasher(hasher),
		registry.WithClock(clock.Now),
	)
	if err != nil {
		t.Fatalf("open registry: %v", err)
	}
	t.Cleanup(func() { _ = reg.Close() })
	return reg
}

func TestLoginRejectsPasswordChangedDuringVerification(t *testing.T) {
	ctx := context.Background()
	clock := &fakeClock{t: time.Date(2026, 5, 5, 6, 7, 8, 0, time.UTC)}
	hasher := &blockingHasher{
		inner:   registry.NewArgon2id(1, 1024, 1),
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	defer hasher.releaseVerify()
	reg := newRegistryWithHasher(t, clock, hasher)
	admin := newAdmin(t, reg)
	member, err := reg.CreateIdentityWithPassword(
		ctx, "member", "old-password", "Member",
	)
	if err != nil {
		t.Fatalf("member: %v", err)
	}
	code, _, err := reg.CreatePasswordReset(ctx, admin.IdentityID, member.ID)
	if err != nil {
		t.Fatalf("create reset: %v", err)
	}

	errs := make(chan error, 1)
	go func() {
		_, _, _, err := reg.Login(ctx, "member", "old-password")
		errs <- err
	}()
	select {
	case <-hasher.started:
	case <-time.After(3 * time.Second):
		t.Fatal("Login never reached Verify")
	}
	if _, err := reg.ResetPassword(ctx, code, "new-password"); err != nil {
		t.Fatalf("reset password: %v", err)
	}
	hasher.releaseVerify()
	select {
	case err := <-errs:
		if !errors.Is(err, registry.ErrInvalidCredentials) {
			t.Fatalf("raced login = %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Login did not finish")
	}
	var sessions int
	if err := reg.DB().QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sessions WHERE identity_id = ?`, member.ID,
	).Scan(&sessions); err != nil {
		t.Fatalf("count member sessions: %v", err)
	}
	if sessions != 0 {
		t.Fatalf("raced login created %d sessions", sessions)
	}
}
