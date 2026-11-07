package handlers

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/Napam/beaverdream"
	"github.com/Napam/beaverdream/lib/registry"
	"github.com/Napam/beaverdream/lib/webmaster"
	"github.com/Napam/beaverdream/lib/worlds"
	"github.com/openai/openai-go/v3"
)

func TestPinnedStreamAfterDrainStarts(t *testing.T) {
	for _, shutdown := range []string{"delete", "close"} {
		for _, plane := range []string{"board", "admin"} {
			t.Run(shutdown+"/"+plane, func(t *testing.T) {
				ctx := context.Background()
				logger := slog.New(slog.NewTextHandler(io.Discard, nil))
				dir := t.TempDir()
				reg, err := registry.Open(ctx, filepath.Join(dir, "registry.db"),
					beaverdream.RegistryMigrationsFS, logger,
					registry.WithHasher(registry.NewArgon2id(1, 64, 1)))
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = reg.Close() }()
				boot, err := reg.EnsureBootstrapAdmin(ctx, "long-password")
				if err != nil {
					t.Fatal(err)
				}
				manager := worlds.NewManager(
					reg,
					filepath.Join(dir, "worlds"),
					beaverdream.WorldMigrationsFS,
					logger,
				)
				world, err := manager.Create(ctx, boot.IdentityID, "heroes")
				if err != nil {
					t.Fatal(err)
				}
				app := NewApp(logger, reg, manager, func() *webmaster.ChangesetRunner {
					return webmaster.New(openai.NewClient(), "mock-model", 3, "")
				}, AppConfig{})
				if shutdown == "delete" {
					defer app.Close()
				}
				scope := registry.ScopePlay
				if plane == "admin" {
					scope = registry.ScopeApp
				}
				session, token, err := reg.NewSession(ctx, boot.IdentityID, boot.LoginID, scope)
				if err != nil {
					t.Fatal(err)
				}
				rt, release, err := app.acquireBySlug(ctx, "heroes")
				if err != nil {
					t.Fatal(err)
				}
				if shutdown == "delete" {
					if err := reg.SetWorldStatus(
						ctx,
						world.ID,
						registry.WorldDeleting,
					); err != nil {
						t.Fatal(err)
					}
				}
				drained := make(chan struct{})
				go func() {
					if shutdown == "delete" {
						app.evict(ctx, world.ID)
					} else {
						app.Close()
					}
					close(drained)
				}()
				deadline := time.Now().Add(time.Second)
				for {
					rt.h.liveMu.Lock()
					stopped := rt.h.stopped > 0
					rt.h.liveMu.Unlock()
					if stopped {
						break
					}
					if time.Now().After(deadline) {
						release()
						t.Fatal("drain did not start")
					}
					time.Sleep(time.Millisecond)
				}
				p := &principal{
					IdentityID: boot.IdentityID, SessionHash: session.Hash,
					SessionToken: token, Scope: scope, Admin: plane == "admin",
				}
				req := withPrincipal(
					httptest.NewRequest(http.MethodGet, "/live?page=overview", nil),
					p,
				)
				rec := httptest.NewRecorder()
				done := make(chan struct{})
				go func() {
					if plane == "board" {
						rt.h.Live(rec, req)
					} else {
						rt.h.AdminLive(rec, req)
					}
					release()
					close(done)
				}()
				select {
				case <-done:
				case <-time.After(time.Second):
					t.Fatal("late stream stayed open")
				}
				if rec.Code != http.StatusNoContent {
					t.Fatalf("status = %d", rec.Code)
				}
				select {
				case <-drained:
				case <-time.After(time.Second):
					t.Fatal("drain waited for late stream")
				}
			})
		}
	}
}
