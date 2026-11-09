package worlds_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Napam/beaverdream"
	"github.com/Napam/beaverdream/lib/registry"
	"github.com/Napam/beaverdream/lib/worlds"
)

type fixture struct {
	registry *registry.Registry
	manager  *worlds.Manager
	adminID  string
	dir      string
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	ctx := context.Background()
	reg, err := registry.Open(
		ctx,
		filepath.Join(t.TempDir(), "registry.db"),
		beaverdream.RegistryMigrationsFS,
		logger,
		registry.WithHasher(registry.NewArgon2id(1, 4*1024, 1)),
	)
	if err != nil {
		t.Fatalf("open registry: %v", err)
	}
	t.Cleanup(func() { _ = reg.Close() })
	admin, err := reg.EnsureSystemAdmin(ctx, "admin-password")
	if err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	dir := filepath.Join(t.TempDir(), "worlds")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("worlds dir: %v", err)
	}
	mgr := worlds.NewManager(reg, dir, beaverdream.WorldMigrationsFS, logger)
	return &fixture{registry: reg, manager: mgr, adminID: admin.IdentityID, dir: dir}
}

func TestCreateOpenAndDeleteWorld(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	info, err := f.manager.Create(ctx, f.adminID, "heroes")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if info.Status != registry.WorldReady {
		t.Fatalf("status = %q", info.Status)
	}
	path := f.manager.Path(info.ID)
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("world file: %v", err)
	}

	handle, err := f.manager.Open(ctx, info.ID)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	var versions int
	if err := handle.DB.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM site_versions`).Scan(&versions); err != nil {
		t.Fatalf("read seed: %v", err)
	}
	if versions != 1 {
		t.Fatalf("seed versions = %d", versions)
	}
	if _, err := handle.DB.ExecContext(ctx,
		`INSERT INTO turns (kind, author, body) VALUES ('post', 'dave', 'kept')`,
	); err != nil {
		t.Fatalf("insert post: %v", err)
	}
	if err := handle.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// Opening again runs the migrations again and keeps the world's data.
	again, err := f.manager.OpenBySlug(ctx, "heroes")
	if err != nil {
		t.Fatalf("open by slug: %v", err)
	}
	var body string
	if err := again.DB.QueryRowContext(ctx,
		`SELECT body FROM posts LIMIT 1`).Scan(&body); err != nil {
		t.Fatalf("read post: %v", err)
	}
	if body != "kept" {
		t.Fatalf("post body = %q", body)
	}
	if err := again.DB.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM site_versions`).Scan(&versions); err != nil {
		t.Fatalf("seed count again: %v", err)
	}
	if versions != 1 {
		t.Fatalf("second open reseeded: %d", versions)
	}
	if err := again.Close(); err != nil {
		t.Fatalf("close again: %v", err)
	}

	if err := f.manager.Delete(ctx, f.adminID, info.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	for _, suffix := range []string{"", "-wal", "-shm", "-journal"} {
		if _, err := os.Stat(path + suffix); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("file %q still exists: %v", suffix, err)
		}
	}
	if _, err := f.registry.GetWorld(ctx, info.ID); !errors.Is(err, registry.ErrNotFound) {
		t.Fatalf("row after delete = %v", err)
	}
	if _, err := f.manager.Open(ctx, info.ID); !errors.Is(err, registry.ErrNotFound) {
		t.Fatalf("open after delete = %v", err)
	}
}

func TestCreateRequiresAdminAndValidSlug(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	member, err := f.registry.CreateIdentityWithPassword(
		ctx, "member", "member-password", "Member",
	)
	if err != nil {
		t.Fatalf("member: %v", err)
	}
	if _, err := f.manager.Create(ctx, member.ID, "member-world"); !errors.Is(
		err, registry.ErrForbidden,
	) {
		t.Fatalf("member create = %v", err)
	}
	for _, slug := range []string{"", "Heroes", "two words", "-leading", "sla/sh"} {
		if _, err := f.manager.Create(ctx, f.adminID, slug); !errors.Is(
			err, worlds.ErrInvalidSlug,
		) {
			t.Fatalf("slug %q = %v", slug, err)
		}
	}
	if _, err := f.manager.Create(ctx, f.adminID, "heroes"); err != nil {
		t.Fatalf("valid create: %v", err)
	}
	if _, err := f.manager.Create(ctx, f.adminID, "heroes"); !errors.Is(
		err, registry.ErrWorldExists,
	) {
		t.Fatalf("duplicate slug = %v", err)
	}
}

func TestOpenDoesNotRecreateADeletedFile(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	info, err := f.manager.Create(ctx, f.adminID, "heroes")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	path := f.manager.Path(info.ID)
	if err := os.Remove(path); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if _, err := f.manager.Open(ctx, info.ID); err == nil {
		t.Fatal("open succeeded with no file")
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("open recreated the file: %v", err)
	}
}

func TestDeleteRemovesWalShmAndJournal(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	info, err := f.manager.Create(ctx, f.adminID, "heroes")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	path := f.manager.Path(info.ID)
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		if err := os.WriteFile(path+suffix, []byte("junk"), 0o600); err != nil {
			t.Fatalf("write %s: %v", suffix, err)
		}
	}
	if err := f.manager.Delete(ctx, f.adminID, info.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	for _, suffix := range []string{"", "-wal", "-shm", "-journal"} {
		if _, err := os.Stat(path + suffix); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("%s survived delete: %v", suffix, err)
		}
	}
}

func TestRecoverFinishesAndRemovesUnfinishedWorlds(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)

	// creating with a usable (empty) file is migrated and becomes ready.
	finish, err := f.registry.CreateWorldRecord(ctx, "finish01", "finish", f.adminID)
	if err != nil {
		t.Fatalf("create creating row: %v", err)
	}
	finishPath := f.manager.Path(finish.ID)
	if err := os.WriteFile(finishPath, nil, 0o600); err != nil {
		t.Fatalf("write empty file: %v", err)
	}

	// creating with no file only has a row; it is deleted.
	missing, err := f.registry.CreateWorldRecord(ctx, "missing1", "missing", f.adminID)
	if err != nil {
		t.Fatalf("create missing row: %v", err)
	}

	// creating with a broken file is removed with the file.
	broken, err := f.registry.CreateWorldRecord(ctx, "broken01", "broken", f.adminID)
	if err != nil {
		t.Fatalf("create broken row: %v", err)
	}
	brokenPath := f.manager.Path(broken.ID)
	if err := os.WriteFile(brokenPath, []byte("not sqlite"), 0o600); err != nil {
		t.Fatalf("write broken file: %v", err)
	}

	// deleting removes the row and every file.
	deleting, err := f.manager.Create(ctx, f.adminID, "gone")
	if err != nil {
		t.Fatalf("create deleting world: %v", err)
	}
	if err := f.registry.SetWorldStatus(ctx, deleting.ID, registry.WorldDeleting); err != nil {
		t.Fatalf("mark deleting: %v", err)
	}
	deletingPath := f.manager.Path(deleting.ID)
	if err := os.WriteFile(deletingPath+"-wal", []byte("junk"), 0o600); err != nil {
		t.Fatalf("write deleting wal: %v", err)
	}

	result, err := f.manager.Recover(ctx)
	if err != nil {
		t.Fatalf("recover: %v", err)
	}
	if len(result.Finished) != 1 || result.Finished[0] != finish.ID {
		t.Fatalf("finished = %v", result.Finished)
	}
	got := make(map[string]bool)
	for _, id := range result.Removed {
		got[id] = true
	}
	for _, id := range []string{missing.ID, broken.ID, deleting.ID} {
		if !got[id] {
			t.Fatalf("removed = %v, missing %q", result.Removed, id)
		}
		if _, err := f.registry.GetWorld(ctx, id); !errors.Is(err, registry.ErrNotFound) {
			t.Fatalf("row %q survived: %v", id, err)
		}
	}
	if _, err := os.Stat(brokenPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("broken file survived: %v", err)
	}
	if _, err := os.Stat(deletingPath + "-wal"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("deleting wal survived: %v", err)
	}
	if _, err := f.registry.GetWorld(ctx, finish.ID); err != nil {
		t.Fatalf("finished row: %v", err)
	}
	handle, err := f.manager.Open(ctx, finish.ID)
	if err != nil {
		t.Fatalf("open finished world: %v", err)
	}
	if err := handle.Close(); err != nil {
		t.Fatalf("close finished world: %v", err)
	}
	ready, err := f.registry.ListReadyWorlds(ctx)
	if err != nil {
		t.Fatalf("ready list: %v", err)
	}
	if len(ready) != 1 || ready[0].ID != finish.ID {
		t.Fatalf("ready = %+v", ready)
	}
}

func TestRecoveryWithNothingToDo(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	if _, err := f.manager.Create(ctx, f.adminID, "heroes"); err != nil {
		t.Fatalf("create: %v", err)
	}
	result, err := f.manager.Recover(ctx)
	if err != nil {
		t.Fatalf("recover: %v", err)
	}
	if len(result.Finished) != 0 || len(result.Removed) != 0 {
		t.Fatalf("recovery = %+v", result)
	}
}

func TestWorldIDIsRandomHexAndPathsStayInsideDir(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	info, err := f.manager.Create(ctx, f.adminID, "heroes")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if len(info.ID) != 32 {
		t.Fatalf("world id = %q", info.ID)
	}
	for _, r := range info.ID {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			t.Fatalf("world id has a non-hex character: %q", info.ID)
		}
	}
	if filepath.Dir(f.manager.Path(info.ID)) != f.manager.Dir() {
		t.Fatalf("path escaped the worlds dir: %q", f.manager.Path(info.ID))
	}
	if info.CreatedAt.IsZero() || info.CreatedAt.After(time.Now().Add(time.Minute)) {
		t.Fatalf("created at = %v", info.CreatedAt)
	}
}
