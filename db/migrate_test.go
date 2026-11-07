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
	_ "modernc.org/sqlite"
)

// A fresh world file gets the whole schema in one migration: the seed, a post
// list that carries posts and replies, and every store the handlers use.
func TestMigrationsCreateTheWorldSchema(t *testing.T) {
	ctx := context.Background()
	conn, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "world.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if _, err := db.RunMigrations(ctx, beaverdream.MigrationsFS, conn, logger); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	var seeds int
	if err := conn.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM site_versions WHERE turn_id IS NULL`,
	).Scan(&seeds); err != nil {
		t.Fatalf("count seed: %v", err)
	}
	if seeds != 1 {
		t.Fatalf("seed rows = %d, want 1", seeds)
	}

	if _, err := conn.ExecContext(ctx,
		`INSERT INTO turns (kind, author, body) VALUES ('post', 'dave', 'still here')`,
	); err != nil {
		t.Fatalf("insert post: %v", err)
	}
	var body string
	if err := conn.QueryRowContext(ctx, `SELECT body FROM posts`).Scan(&body); err != nil {
		t.Fatalf("read post: %v", err)
	}
	if body != "still here" {
		t.Errorf("post body = %q", body)
	}

	if _, err := conn.ExecContext(ctx,
		`INSERT INTO turns (kind, author, body) VALUES ('reply', 'webmaster', 'there')`,
	); err != nil {
		t.Fatalf("insert reply: %v", err)
	}
	var kind string
	if err := conn.QueryRowContext(ctx,
		`SELECT kind FROM posts WHERE body = 'there'`,
	).Scan(&kind); err != nil {
		t.Fatalf("read reply: %v", err)
	}
	if kind != "reply" {
		t.Errorf("reply kind = %q, want reply", kind)
	}

	for _, statement := range []string{
		`INSERT INTO site_state (key, value) VALUES ('likes', '3')`,
		`INSERT INTO state_holdings (visitor, key, amount, name) VALUES ('v', 'coins', 3, 'dave')`,
		`INSERT INTO state_ops (op_id, visitor, op, source, key, delta) VALUES (1, 'v', 'add', 'coins', 'coins', 3)`,
		`INSERT INTO journal (key, payload) VALUES ('moves', '{}')`,
		`INSERT INTO room_summaries (through_turn_id, text) VALUES (1, 'gist')`,
	} {
		if _, err := conn.ExecContext(ctx, statement); err != nil {
			t.Errorf("%s: %v", statement, err)
		}
	}
}
