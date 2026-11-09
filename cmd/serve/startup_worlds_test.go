package main

import (
	"context"
	"io"
	"log/slog"
	"net"
	"path/filepath"
	"testing"

	"github.com/Napam/beaverdream"
	"github.com/Napam/beaverdream/lib/env"
	"github.com/Napam/beaverdream/lib/registry"
	"github.com/Napam/beaverdream/lib/worlds"
)

func TestStartupKeepsEmptyWorldRegistry(t *testing.T) {
	for _, state := range []string{"fresh", "deleted", "deleting"} {
		t.Run(state, func(t *testing.T) {
			ctx := context.Background()
			logger := slog.New(slog.NewTextHandler(io.Discard, nil))
			dir := t.TempDir()
			path, worldDir := filepath.Join(dir, "registry.db"), filepath.Join(dir, "worlds")
			if state != "fresh" {
				reg, err := registry.Open(ctx, path, beaverdream.RegistryMigrationsFS, logger,
					registry.WithHasher(registry.NewArgon2id(1, 64, 1)))
				if err != nil {
					t.Fatal(err)
				}
				boot, err := reg.EnsureSystemAdmin(ctx, "long-password")
				if err != nil {
					t.Fatal(err)
				}
				manager := worlds.NewManager(reg, worldDir, beaverdream.WorldMigrationsFS, logger)
				world, err := manager.Create(ctx, boot.IdentityID, "heroes")
				if err != nil {
					t.Fatal(err)
				}
				if state == "deleted" {
					err = manager.Delete(ctx, boot.IdentityID, world.ID)
				} else {
					err = reg.SetWorldStatus(ctx, world.ID, registry.WorldDeleting)
				}
				if err != nil {
					t.Fatal(err)
				}
				if err := reg.Close(); err != nil {
					t.Fatal(err)
				}
			}
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = listener.Close() }()
			_, port, err := net.SplitHostPort(listener.Addr().String())
			if err != nil {
				t.Fatal(err)
			}
			old := env.Vars
			defer func() { env.Vars = old }()
			env.Vars = &env.Env{
				Host: "127.0.0.1", AppPort: port, PlayPort: port,
				RegistryPath: path, WorldsDir: worldDir, AdminPassword: "long-password",
				OpenAIBaseURL: "http://127.0.0.1:1", OpenAIModel: "mock-model",
			}
			if err := run(logger); err == nil {
				t.Fatal("expected occupied port")
			}
			reg, err := registry.Open(ctx, path, beaverdream.RegistryMigrationsFS, logger)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = reg.Close() }()
			rows, err := reg.ListWorlds(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if len(rows) != 0 {
				t.Fatalf("startup created worlds: %+v", rows)
			}
		})
	}
}
