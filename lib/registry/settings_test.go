package registry_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/Napam/beaverdream"
	"github.com/Napam/beaverdream/lib/registry"
)

func TestWorldCreationSetting(t *testing.T) {
	ctx := context.Background()
	clock := &fakeClock{t: time.Now().UTC()}
	reg := newRegistry(t, clock)
	admin := newAdmin(t, reg)
	member, err := reg.CreateIdentityWithPassword(ctx, "member", "member-password", "Member")
	if err != nil {
		t.Fatal(err)
	}
	assertOpen := func(want bool) {
		t.Helper()
		if got, err := reg.WorldCreationOpen(ctx); err != nil || got != want {
			t.Fatalf("open = %v, %v; want %v", got, err, want)
		}
	}
	assertOpen(true)
	for _, open := range []bool{false, false, true} {
		if err := reg.SetWorldCreationOpen(ctx, admin.IdentityID, open); err != nil {
			t.Fatal(err)
		}
		assertOpen(open)
	}
	if err := reg.SetWorldCreationOpen(
		ctx,
		member.ID,
		false,
	); !errors.Is(
		err,
		registry.ErrForbidden,
	) {
		t.Fatalf("member setting write = %v", err)
	}
	assertOpen(true)
	if err := reg.GrantAdmin(ctx, admin.IdentityID, member.ID); err != nil {
		t.Fatal(err)
	}
	if err := reg.DisableIdentity(ctx, admin.IdentityID, member.ID); err != nil {
		t.Fatal(err)
	}
	if err := reg.SetWorldCreationOpen(
		ctx,
		member.ID,
		false,
	); !errors.Is(
		err,
		registry.ErrForbidden,
	) {
		t.Fatalf("disabled admin setting write = %v", err)
	}
	assertOpen(true)
	if _, err := reg.DB().
		ExecContext(ctx, `DELETE FROM settings WHERE key = ?`, registry.SettingWorldCreationOpen); err != nil {
		t.Fatal(err)
	}
	assertOpen(true)
}

func TestWorldCreationSettingSurvivesReopen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "registry.db")
	open := func() *registry.Registry {
		t.Helper()
		reg, err := registry.Open(
			ctx,
			path,
			beaverdream.RegistryMigrationsFS,
			slog.New(
				slog.NewTextHandler(io.Discard, nil),
			),
			registry.WithHasher(registry.NewArgon2id(1, 1024, 1)),
		)
		if err != nil {
			t.Fatal(err)
		}
		return reg
	}
	reg := open()
	admin := newAdmin(t, reg)
	if err := reg.SetWorldCreationOpen(ctx, admin.IdentityID, false); err != nil {
		t.Fatal(err)
	}
	if err := reg.Close(); err != nil {
		t.Fatal(err)
	}
	reg = open()
	defer func() { _ = reg.Close() }()
	if got, err := reg.WorldCreationOpen(ctx); err != nil || got {
		t.Fatalf("open after restart = %v, %v", got, err)
	}
}
