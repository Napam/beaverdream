package registry_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"regexp"
	"sync"
	"testing"
	"time"

	"github.com/Napam/beaverdream"
	"github.com/Napam/beaverdream/lib/registry"
)

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = t
}

func newRegistry(t *testing.T, clock *fakeClock) *registry.Registry {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	reg, err := registry.Open(
		context.Background(),
		filepath.Join(t.TempDir(), "registry.db"),
		beaverdream.RegistryMigrationsFS,
		logger,
		registry.WithHasher(registry.NewArgon2id(1, 4*1024, 1)),
		registry.WithClock(clock.Now),
	)
	if err != nil {
		t.Fatalf("open registry: %v", err)
	}
	t.Cleanup(func() { _ = reg.Close() })
	return reg
}

func newAdmin(t *testing.T, reg *registry.Registry) registry.BootstrapResult {
	t.Helper()
	result, err := reg.EnsureBootstrapAdmin(context.Background(), "admin-password")
	if err != nil {
		t.Fatalf("bootstrap admin: %v", err)
	}
	return result
}

func TestRegistryTimestampForm(t *testing.T) {
	tm := time.Date(2026, 1, 2, 3, 4, 5, 123456789, time.UTC)
	if got, want := registry.FormatTimestamp(tm), "2026-01-02T03:04:05.123Z"; got != want {
		t.Fatalf("FormatTimestamp = %q, want %q", got, want)
	}
	parsed, err := registry.ParseTimestamp("2026-01-02T03:04:05.123Z")
	if err != nil {
		t.Fatalf("ParseTimestamp: %v", err)
	}
	if !parsed.Equal(time.Date(2026, 1, 2, 3, 4, 5, 123000000, time.UTC)) {
		t.Fatalf("parsed = %v", parsed)
	}
}

func TestBootstrapAdminRestoresAndRotates(t *testing.T) {
	ctx := context.Background()
	clock := &fakeClock{t: time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)}
	reg := newRegistry(t, clock)
	first := newAdmin(t, reg)
	if first.Generated || first.PasswordChanged {
		t.Fatalf("first bootstrap generated=%v changed=%v", first.Generated, first.PasswordChanged)
	}
	admin, err := reg.IsAdmin(ctx, first.IdentityID)
	if err != nil || !admin {
		t.Fatalf("bootstrap admin role = %v, %v", admin, err)
	}

	_, oldSession, oldToken, err := reg.Login(ctx, "admin", "admin-password")
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	if oldSession.Scope != registry.ScopeApp {
		t.Fatalf("scope = %q", oldSession.Scope)
	}
	if _, _, err := reg.Authenticate(ctx, oldToken, registry.ScopeApp); err != nil {
		t.Fatalf("authenticate: %v", err)
	}

	same, err := reg.EnsureBootstrapAdmin(ctx, "admin-password")
	if err != nil {
		t.Fatalf("same bootstrap: %v", err)
	}
	if same.PasswordChanged {
		t.Fatal("same password reported as changed")
	}
	if _, _, err := reg.Authenticate(ctx, oldToken, registry.ScopeApp); err != nil {
		t.Fatalf("same password revoked the session: %v", err)
	}

	rotated, err := reg.EnsureBootstrapAdmin(ctx, "new-admin-password")
	if err != nil {
		t.Fatalf("rotate bootstrap: %v", err)
	}
	if !rotated.PasswordChanged {
		t.Fatal("rotated password not reported")
	}
	if _, _, err := reg.Authenticate(ctx, oldToken, registry.ScopeApp); !errors.Is(
		err, registry.ErrSessionInvalid,
	) {
		t.Fatalf("old session after rotation: %v", err)
	}
	if _, _, _, err := reg.Login(ctx, "admin", "admin-password"); !errors.Is(
		err, registry.ErrInvalidCredentials,
	) {
		t.Fatalf("old password after rotation: %v", err)
	}
	_, _, _, err = reg.Login(ctx, "admin", "new-admin-password")
	if err != nil {
		t.Fatalf("new password: %v", err)
	}

	if err := reg.DisableIdentity(ctx, first.IdentityID, first.IdentityID); err != nil {
		t.Fatalf("disable self: %v", err)
	}
	if _, _, _, err := reg.Login(ctx, "admin", "new-admin-password"); !errors.Is(
		err, registry.ErrInvalidCredentials,
	) {
		t.Fatalf("disabled admin login: %v", err)
	}
	if _, err := reg.EnsureBootstrapAdmin(ctx, "new-admin-password"); err != nil {
		t.Fatalf("restore disabled admin: %v", err)
	}
	if admin, err := reg.IsAdmin(ctx, first.IdentityID); err != nil || !admin {
		t.Fatalf("restored admin = %v, %v", admin, err)
	}

	generated, err := reg.EnsureBootstrapAdmin(ctx, "")
	if err != nil {
		t.Fatalf("generated bootstrap: %v", err)
	}
	if !generated.Generated || len(generated.Password) < 32 {
		t.Fatalf("generated result = %+v", generated)
	}
}

func TestInviteRedemptionRollsBackDuplicateUsername(t *testing.T) {
	ctx := context.Background()
	clock := &fakeClock{t: time.Date(2026, 4, 5, 6, 7, 8, 0, time.UTC)}
	reg := newRegistry(t, clock)
	admin := newAdmin(t, reg)

	invite, code, err := reg.CreateInvite(ctx, admin.IdentityID, registry.InviteParams{
		Label:   "friends",
		MaxUses: 2,
	})
	if err != nil {
		t.Fatalf("create invite: %v", err)
	}

	identity, session, token, err := reg.RedeemInvite(
		ctx, code, "alice", "alice-password", "Alice",
	)
	if err != nil {
		t.Fatalf("redeem alice: %v", err)
	}
	if identity.InviteID != invite.ID {
		t.Fatalf("invite id = %q, want %q", identity.InviteID, invite.ID)
	}
	if session.Scope != registry.ScopeApp {
		t.Fatalf("session scope = %q", session.Scope)
	}
	if _, _, err := reg.Authenticate(ctx, token, registry.ScopeApp); err != nil {
		t.Fatalf("redeemed session: %v", err)
	}

	if _, _, _, err := reg.RedeemInvite(
		ctx, code, "alice", "another-password", "",
	); !errors.Is(err, registry.ErrUsernameTaken) {
		t.Fatalf("duplicate username = %v", err)
	}
	invites, err := reg.ListInvites(ctx, admin.IdentityID)
	if err != nil {
		t.Fatalf("list invites: %v", err)
	}
	if len(invites) != 1 || invites[0].Uses != 1 {
		t.Fatalf("uses after rollback = %+v", invites)
	}

	if _, _, _, err := reg.RedeemInvite(ctx, code, "bob", "bob-password", ""); err != nil {
		t.Fatalf("redeem bob: %v", err)
	}
	if _, _, _, err := reg.RedeemInvite(
		ctx, code, "carol", "carol-password", "",
	); !errors.Is(err, registry.ErrInviteInvalid) {
		t.Fatalf("exhausted invite = %v", err)
	}
	if _, _, _, err := reg.Login(ctx, "alice", "alice-password"); err != nil {
		t.Fatalf("sign in from a new browser: %v", err)
	}
}

func TestInviteExpiryBoundaryAndRevocation(t *testing.T) {
	ctx := context.Background()
	start := time.Date(2026, 5, 6, 7, 8, 9, 0, time.UTC)
	clock := &fakeClock{t: start}
	reg := newRegistry(t, clock)
	admin := newAdmin(t, reg)

	atNow := start
	expired, code, err := reg.CreateInvite(ctx, admin.IdentityID, registry.InviteParams{
		MaxUses:   1,
		ExpiresAt: &atNow,
	})
	if err != nil || expired == nil {
		t.Fatalf("create exact-expiry invite: %v", err)
	}
	if _, _, _, err := reg.RedeemInvite(
		ctx, code, "early", "early-password", "",
	); !errors.Is(err, registry.ErrInviteInvalid) {
		t.Fatalf("invite at exact expiry = %v", err)
	}

	slightlyLater := start.Add(time.Millisecond)
	ok, code, err := reg.CreateInvite(ctx, admin.IdentityID, registry.InviteParams{
		MaxUses:   2,
		ExpiresAt: &slightlyLater,
	})
	if err != nil || ok == nil {
		t.Fatalf("create millisecond invite: %v", err)
	}
	if _, _, _, err := reg.RedeemInvite(
		ctx, code, "ontime", "ontime-password", "",
	); err != nil {
		t.Fatalf("redeem before expiry: %v", err)
	}
	clock.Set(slightlyLater)
	if _, _, _, err := reg.RedeemInvite(
		ctx, code, "late", "late-password", "",
	); !errors.Is(err, registry.ErrInviteInvalid) {
		t.Fatalf("redeem at exact expiry = %v", err)
	}

	revoked, revokedCode, err := reg.CreateInvite(ctx, admin.IdentityID, registry.InviteParams{
		MaxUses: 1,
	})
	if err != nil {
		t.Fatalf("create revoked invite: %v", err)
	}
	if err := reg.RevokeInvite(ctx, admin.IdentityID, revoked.ID); err != nil {
		t.Fatalf("revoke invite: %v", err)
	}
	if _, _, _, err := reg.RedeemInvite(
		ctx, revokedCode, "revoked", "revoked-password", "",
	); !errors.Is(err, registry.ErrInviteInvalid) {
		t.Fatalf("revoked invite = %v", err)
	}

	if _, _, err := reg.CreateInvite(ctx, admin.IdentityID, registry.InviteParams{
		MaxUses: 0,
	}); !errors.Is(err, registry.ErrInvalidInput) {
		t.Fatalf("max uses 0 = %v", err)
	}
}

func TestConcurrentInviteRedemptionStopsAtMaxUses(t *testing.T) {
	ctx := context.Background()
	clock := &fakeClock{t: time.Date(2026, 6, 7, 8, 9, 10, 0, time.UTC)}
	reg := newRegistry(t, clock)
	admin := newAdmin(t, reg)
	invite, code, err := reg.CreateInvite(ctx, admin.IdentityID, registry.InviteParams{
		MaxUses: 3,
	})
	if err != nil {
		t.Fatalf("create invite: %v", err)
	}

	const attempts = 20
	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		success int
		refused int
	)
	for i := range attempts {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, _, _, err := reg.RedeemInvite(
				ctx,
				code,
				fmt.Sprintf("player%02d", i),
				"player-password",
				"",
			)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				success++
			case errors.Is(err, registry.ErrInviteInvalid):
				refused++
			default:
				t.Errorf("redeem %d: %v", i, err)
			}
		}(i)
	}
	wg.Wait()
	if success != 3 || refused != attempts-3 {
		t.Fatalf("success = %d, refused = %d", success, refused)
	}
	invites, err := reg.ListInvites(ctx, admin.IdentityID)
	if err != nil {
		t.Fatalf("list invites: %v", err)
	}
	if invites[0].ID != invite.ID || invites[0].Uses != 3 {
		t.Fatalf("invite after the burst = %+v", invites[0])
	}
}

func TestSessionScopesExpiryAndCSRF(t *testing.T) {
	ctx := context.Background()
	start := time.Date(2026, 7, 8, 9, 10, 11, 0, time.UTC)
	clock := &fakeClock{t: start}
	reg := newRegistry(t, clock)
	admin := newAdmin(t, reg)

	_, loginSession, loginToken, err := reg.Login(ctx, "admin", "admin-password")
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	if _, _, err := reg.Authenticate(ctx, loginToken, registry.ScopePlay); !errors.Is(
		err, registry.ErrSessionInvalid,
	) {
		t.Fatalf("app token on play scope = %v", err)
	}
	if !reg.VerifyCSRF(loginToken, reg.CSRFToken(loginToken)) {
		t.Fatal("csrf token does not verify")
	}
	if reg.VerifyCSRF(loginToken, reg.CSRFToken("other")) {
		t.Fatal("csrf token verifies for another session")
	}

	short, shortToken, err := reg.NewSessionTTL(
		ctx, admin.IdentityID, loginSession.LoginID, registry.ScopeApp, time.Hour,
	)
	if err != nil {
		t.Fatalf("new short session: %v", err)
	}
	clock.Set(start.Add(time.Hour - time.Millisecond))
	if _, _, err := reg.Authenticate(ctx, shortToken, registry.ScopeApp); err != nil {
		t.Fatalf("before exact expiry: %v", err)
	}
	clock.Set(start.Add(time.Hour))
	if _, _, err := reg.Authenticate(ctx, shortToken, registry.ScopeApp); !errors.Is(
		err, registry.ErrSessionInvalid,
	) {
		t.Fatalf("at exact expiry = %v", err)
	}
	if short.ExpiresAt == nil {
		t.Fatal("short session has no expiry")
	}

	apiSession, apiToken, err := reg.CreateAPIToken(
		ctx, admin.IdentityID, loginSession.LoginID,
	)
	if err != nil {
		t.Fatalf("create api token: %v", err)
	}
	if apiSession.Scope != registry.ScopeAPI || apiSession.ExpiresAt != nil {
		t.Fatalf("api session = %+v", apiSession)
	}
	if _, _, err := reg.Authenticate(ctx, apiToken, registry.ScopeAPI); err != nil {
		t.Fatalf("api authenticate: %v", err)
	}
	if _, _, err := reg.Authenticate(ctx, apiToken, registry.ScopeApp); !errors.Is(
		err, registry.ErrSessionInvalid,
	) {
		t.Fatalf("api token on app scope = %v", err)
	}
	if err := reg.RevokeAPIToken(ctx, apiToken); err != nil {
		t.Fatalf("revoke api token: %v", err)
	}
	if _, _, err := reg.Authenticate(ctx, apiToken, registry.ScopeAPI); !errors.Is(
		err, registry.ErrSessionInvalid,
	) {
		t.Fatalf("revoked api token = %v", err)
	}

	member, err := reg.CreateIdentityWithPassword(
		ctx, "member", "member-password", "Member",
	)
	if err != nil {
		t.Fatalf("create member: %v", err)
	}
	var memberLogin string
	if err := reg.DB().QueryRowContext(ctx,
		`SELECT id FROM logins WHERE identity_id = ?`, member.ID,
	).Scan(&memberLogin); err != nil {
		t.Fatalf("read member login: %v", err)
	}
	if _, _, err := reg.CreateAPIToken(ctx, member.ID, memberLogin); !errors.Is(
		err, registry.ErrForbidden,
	) {
		t.Fatalf("member api token = %v", err)
	}
}

func TestHandoffIsSingleUseBoundAndExpires(t *testing.T) {
	ctx := context.Background()
	start := time.Date(2026, 8, 9, 10, 11, 12, 0, time.UTC)
	clock := &fakeClock{t: start}
	reg := newRegistry(t, clock)
	admin := newAdmin(t, reg)
	login, loginSession, _, err := reg.Login(ctx, "admin", "admin-password")
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	world, err := reg.CreateWorldRecord(ctx, "w1", "heroes", admin.IdentityID)
	if err != nil {
		t.Fatalf("create world: %v", err)
	}
	if err := reg.SetWorldStatus(ctx, world.ID, registry.WorldReady); err != nil {
		t.Fatalf("ready world: %v", err)
	}

	code, _, err := reg.CreateHandoff(ctx, login.ID, loginSession.LoginID, world.ID)
	if err != nil {
		t.Fatalf("create handoff: %v", err)
	}
	identity, session, token, worldID, err := reg.ExchangeHandoff(ctx, code)
	if err != nil {
		t.Fatalf("exchange: %v", err)
	}
	if identity.ID != login.ID || worldID != world.ID {
		t.Fatalf("exchange identity=%q world=%q", identity.ID, worldID)
	}
	if session.Scope != registry.ScopePlay || session.ExpiresAt != nil {
		t.Fatalf("play session = %+v", session)
	}
	clock.Set(start.Add(100 * 365 * 24 * time.Hour))
	if _, _, err := reg.Authenticate(ctx, token, registry.ScopePlay); err != nil {
		t.Fatalf("play session expired: %v", err)
	}
	if _, _, _, _, err := reg.ExchangeHandoff(ctx, code); !errors.Is(
		err, registry.ErrHandoffInvalid,
	) {
		t.Fatalf("second exchange = %v", err)
	}

	expiring, _, err := reg.CreateHandoff(ctx, login.ID, loginSession.LoginID, world.ID)
	if err != nil {
		t.Fatalf("create expiring handoff: %v", err)
	}
	clock.Set(start.Add(100*365*24*time.Hour + registry.DefaultHandoffTTL))
	if _, _, _, _, err := reg.ExchangeHandoff(ctx, expiring); !errors.Is(
		err, registry.ErrHandoffInvalid,
	) {
		t.Fatalf("handoff at exact expiry = %v", err)
	}

	disabled, _, err := reg.CreateHandoff(ctx, login.ID, loginSession.LoginID, world.ID)
	if err != nil {
		t.Fatalf("create disabled handoff: %v", err)
	}
	if err := reg.DisableIdentity(ctx, admin.IdentityID, admin.IdentityID); err != nil {
		t.Fatalf("disable admin: %v", err)
	}
	if _, _, _, _, err := reg.ExchangeHandoff(ctx, disabled); !errors.Is(
		err, registry.ErrHandoffInvalid,
	) {
		t.Fatalf("handoff for disabled identity = %v", err)
	}
}

func TestBudgetSplitsDistinctIdentitiesAndExemptsAdmins(t *testing.T) {
	ctx := context.Background()
	start := time.Date(2026, 9, 10, 11, 12, 13, 0, time.UTC)
	clock := &fakeClock{t: start}
	reg := newRegistry(t, clock)
	admin := newAdmin(t, reg)
	alice, err := reg.CreateIdentityWithPassword(ctx, "alice", "alice-password", "Alice")
	if err != nil {
		t.Fatalf("alice: %v", err)
	}
	bob, err := reg.CreateIdentityWithPassword(ctx, "bob", "bob-password", "Bob")
	if err != nil {
		t.Fatalf("bob: %v", err)
	}

	if err := reg.RecordBatchUsage(
		ctx, "world-1", 100, []string{alice.ID, bob.ID, alice.ID}, 7,
	); err != nil {
		t.Fatalf("record batch: %v", err)
	}
	aliceUsed, globalUsed, err := reg.UsageOn(ctx, alice.ID, start)
	if err != nil {
		t.Fatalf("usage: %v", err)
	}
	if aliceUsed != 50 || globalUsed != 100 {
		t.Fatalf("alice usage = %d, global = %d", aliceUsed, globalUsed)
	}
	if err := reg.CheckBudget(ctx, alice.ID, registry.Budget{Identity: 50}); !errors.Is(
		err, registry.ErrBudget,
	) {
		t.Fatalf("identity at limit = %v", err)
	}
	if err := reg.CheckBudget(ctx, alice.ID, registry.Budget{Identity: 51}); err != nil {
		t.Fatalf("identity under limit: %v", err)
	}
	if err := reg.CheckBudget(ctx, alice.ID, registry.Budget{Global: 100}); !errors.Is(
		err, registry.ErrBudget,
	) {
		t.Fatalf("global at limit = %v", err)
	}

	if err := reg.RecordBatchUsage(
		ctx, "world-2", 100, []string{admin.IdentityID}, 8,
	); err != nil {
		t.Fatalf("record admin batch: %v", err)
	}
	if _, globalUsed, err = reg.UsageOn(ctx, alice.ID, start); err != nil {
		t.Fatalf("usage after admin: %v", err)
	}
	if globalUsed != 100 {
		t.Fatalf("admin usage leaked into global: %d", globalUsed)
	}
	if err := reg.CheckBudget(
		ctx, admin.IdentityID, registry.Budget{Identity: 1, Global: 1},
	); err != nil {
		t.Fatalf("admin budget: %v", err)
	}

	status, err := reg.BudgetStatus(ctx, alice.ID, registry.Budget{Identity: 200, Global: 500})
	if err != nil {
		t.Fatalf("budget status: %v", err)
	}
	if status.IdentityUsed != 50 || status.GlobalUsed != 100 ||
		status.IdentityLimit != 200 || status.GlobalLimit != 500 {
		t.Fatalf("budget status = %+v", status)
	}

	clock.Set(start.Add(24 * time.Hour))
	if used, global, err := reg.UsageOn(ctx, alice.ID, clock.Now()); err != nil {
		t.Fatalf("next day usage: %v", err)
	} else if used != 0 || global != 0 {
		t.Fatalf("next day usage = %d, %d", used, global)
	}
}

func TestBudgetRemainderGoesToFirstChargedIdentity(t *testing.T) {
	ctx := context.Background()
	start := time.Date(2026, 10, 11, 12, 13, 14, 0, time.UTC)
	clock := &fakeClock{t: start}
	reg := newRegistry(t, clock)
	newAdmin(t, reg)
	ids := make([]string, 0, 3)
	for _, name := range []string{"ann", "ben", "cat"} {
		identity, err := reg.CreateIdentityWithPassword(
			ctx, name, name+"-password", name,
		)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		ids = append(ids, identity.ID)
	}
	if err := reg.RecordBatchUsage(ctx, "world", 10, ids, 1); err != nil {
		t.Fatalf("record: %v", err)
	}
	want := []int64{4, 3, 3}
	for i, id := range ids {
		used, _, err := reg.UsageOn(ctx, id, start)
		if err != nil {
			t.Fatalf("usage %d: %v", i, err)
		}
		if used != want[i] {
			t.Fatalf("usage %d = %d, want %d", i, used, want[i])
		}
	}
}

func TestRevokedAndDisabledSessionsFail(t *testing.T) {
	ctx := context.Background()
	clock := &fakeClock{t: time.Date(2026, 11, 12, 13, 14, 15, 0, time.UTC)}
	reg := newRegistry(t, clock)
	admin := newAdmin(t, reg)
	member, err := reg.CreateIdentityWithPassword(
		ctx, "dave", "dave-password", "Dave",
	)
	if err != nil {
		t.Fatalf("member: %v", err)
	}
	_, _, token, err := reg.Login(ctx, "dave", "dave-password")
	if err != nil {
		t.Fatalf("member login: %v", err)
	}
	if err := reg.RevokeSession(ctx, token); err != nil {
		t.Fatalf("revoke session: %v", err)
	}
	if _, _, err := reg.Authenticate(ctx, token, registry.ScopeApp); !errors.Is(
		err, registry.ErrSessionInvalid,
	) {
		t.Fatalf("revoked session = %v", err)
	}

	_, _, second, err := reg.Login(ctx, "dave", "dave-password")
	if err != nil {
		t.Fatalf("second login: %v", err)
	}
	if err := reg.DisableIdentity(ctx, admin.IdentityID, member.ID); err != nil {
		t.Fatalf("disable member: %v", err)
	}
	if _, _, err := reg.Authenticate(ctx, second, registry.ScopeApp); !errors.Is(
		err, registry.ErrSessionInvalid,
	) {
		t.Fatalf("disabled identity session = %v", err)
	}
	if _, _, _, err := reg.Login(ctx, "dave", "dave-password"); !errors.Is(
		err, registry.ErrInvalidCredentials,
	) {
		t.Fatalf("disabled identity login = %v", err)
	}
	if _, err := reg.ListIdentities(ctx, member.ID); !errors.Is(err, registry.ErrForbidden) {
		t.Fatalf("member list identities = %v", err)
	}
	identities, err := reg.ListIdentities(ctx, admin.IdentityID)
	if err != nil {
		t.Fatalf("admin list identities: %v", err)
	}
	if len(identities) != 2 {
		t.Fatalf("identities = %d", len(identities))
	}
	if err := reg.SetDisplayName(ctx, member.ID, "  New   Name  "); !errors.Is(
		err, registry.ErrNotFound,
	) {
		t.Fatalf("disabled set name = %v", err)
	}
	if err := reg.SetDisplayName(ctx, admin.IdentityID, "  New   Name  "); err != nil {
		t.Fatalf("set name: %v", err)
	}
	if identity, err := reg.GetIdentity(ctx, admin.IdentityID); err != nil ||
		identity.DisplayName != "New Name" {
		t.Fatalf("display name = %+v, %v", identity, err)
	}
}

func TestWorldSlugUniquenessAndLookup(t *testing.T) {
	ctx := context.Background()
	clock := &fakeClock{t: time.Date(2026, 12, 13, 14, 15, 16, 0, time.UTC)}
	reg := newRegistry(t, clock)
	admin := newAdmin(t, reg)
	first, err := reg.CreateWorldRecord(ctx, "abc", "heroes", admin.IdentityID)
	if err != nil {
		t.Fatalf("create world: %v", err)
	}
	if _, err := reg.CreateWorldRecord(ctx, "def", "heroes", admin.IdentityID); !errors.Is(
		err, registry.ErrWorldExists,
	) {
		t.Fatalf("duplicate slug = %v", err)
	}
	bySlug, err := reg.GetWorldBySlug(ctx, "heroes")
	if err != nil || bySlug.ID != first.ID {
		t.Fatalf("by slug = %+v, %v", bySlug, err)
	}
	if err := reg.SetWorldStatus(ctx, first.ID, registry.WorldReady); err != nil {
		t.Fatalf("ready: %v", err)
	}
	ready, err := reg.ListReadyWorlds(ctx)
	if err != nil || len(ready) != 1 || ready[0].ID != first.ID {
		t.Fatalf("ready worlds = %+v, %v", ready, err)
	}
	if err := reg.DeleteWorldRecord(ctx, first.ID); err != nil {
		t.Fatalf("delete row: %v", err)
	}
	if _, err := reg.GetWorld(ctx, first.ID); !errors.Is(err, registry.ErrNotFound) {
		t.Fatalf("deleted world = %v", err)
	}
}

func TestStoredTimestampsUseTheExactTextForm(t *testing.T) {
	ctx := context.Background()
	clock := &fakeClock{t: time.Date(2026, 2, 3, 4, 5, 6, 789000000, time.UTC)}
	reg := newRegistry(t, clock)
	newAdmin(t, reg)
	var createdAt string
	if err := reg.DB().QueryRowContext(ctx,
		`SELECT created_at FROM identities LIMIT 1`,
	).Scan(&createdAt); err != nil {
		t.Fatalf("read created_at: %v", err)
	}
	pattern := regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3}Z$`)
	if !pattern.MatchString(createdAt) {
		t.Fatalf("created_at = %q", createdAt)
	}
	if createdAt != "2026-02-03T04:05:06.789Z" {
		t.Fatalf("created_at = %q", createdAt)
	}
}

func TestOpenRunsMigrationsAgainWithoutLosingRows(t *testing.T) {
	ctx := context.Background()
	clock := &fakeClock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	path := filepath.Join(t.TempDir(), "registry.db")
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	open := func() *registry.Registry {
		reg, err := registry.Open(
			ctx, path, beaverdream.RegistryMigrationsFS, logger,
			registry.WithHasher(registry.NewArgon2id(1, 4*1024, 1)),
			registry.WithClock(clock.Now),
		)
		if err != nil {
			t.Fatalf("open: %v", err)
		}
		return reg
	}
	first := open()
	admin := newAdmin(t, first)
	if err := first.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	second := open()
	defer func() { _ = second.Close() }()
	if identity, err := second.GetIdentity(ctx, admin.IdentityID); err != nil ||
		identity.DisplayName != "admin" {
		t.Fatalf("identity after reopen = %+v, %v", identity, err)
	}
}
