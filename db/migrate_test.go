package db_test

import (
	"context"
	"database/sql"
	"io"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/Napam/beaverdream"
	"github.com/Napam/beaverdream/db"
	"github.com/pressly/goose/v3"
	_ "modernc.org/sqlite"
)

// A board created before visitor state keeps its posts and pages when the
// server starts on the new code.
func TestVersionOneDatabaseMigratesWithItsData(t *testing.T) {
	ctx := context.Background()
	conn, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "old.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	old, err := goose.NewProvider(goose.DialectSQLite3, conn, beaverdream.MigrationsFS)
	if err != nil {
		t.Fatalf("provider: %v", err)
	}
	if _, err := old.UpTo(ctx, 1); err != nil {
		t.Fatalf("migrate to 1: %v", err)
	}
	if _, err := conn.ExecContext(ctx,
		`INSERT INTO turns (kind, author, body) VALUES ('post', 'dave', 'still here')`,
	); err != nil {
		t.Fatalf("insert post: %v", err)
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if _, err := db.RunMigrations(ctx, beaverdream.MigrationsFS, conn, logger); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	var body string
	if err := conn.QueryRowContext(ctx, `SELECT body FROM posts`).Scan(&body); err != nil {
		t.Fatalf("read post: %v", err)
	}
	if body != "still here" {
		t.Errorf("post body = %q after the migration", body)
	}
	var versions int
	if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM site_versions`).
		Scan(&versions); err != nil {
		t.Fatalf("count versions: %v", err)
	}
	if versions != 1 {
		t.Errorf("versions = %d, want the seed", versions)
	}
	if _, err := conn.ExecContext(ctx,
		`INSERT INTO state_holdings (visitor, key, amount, name) VALUES ('v', 'coins', 3, 'dave')`,
	); err != nil {
		t.Errorf("the new tables are missing: %v", err)
	}
}
