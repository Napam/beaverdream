package db_test

import (
	"context"
	"database/sql"
	"io"
	"log/slog"
	"path/filepath"
	"slices"
	"testing"

	"github.com/Napam/beaverdream"
	"github.com/Napam/beaverdream/db"
	"github.com/pressly/goose/v3"
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
	if _, err := db.RunMigrations(ctx, beaverdream.WorldMigrationsFS, conn, logger); err != nil {
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
		`INSERT INTO shared_state (key, value) VALUES ('likes', '3')`,
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

// A world upgraded to the play report keeps its render and its journal. The
// counters start empty because the versions already written have no browser
// history to count, and a downgrade drops only the new tables.
func TestVersionStatsMigrationKeepsTheWorld(t *testing.T) {
	ctx := context.Background()
	conn, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "world.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	provider, err := goose.NewProvider(goose.DialectSQLite3, conn, beaverdream.WorldMigrationsFS)
	if err != nil {
		t.Fatalf("provider: %v", err)
	}
	if _, err := provider.UpTo(ctx, 2); err != nil {
		t.Fatalf("migrate to version 2: %v", err)
	}

	var versionID int64
	if err := conn.QueryRowContext(
		ctx,
		`INSERT INTO site_versions (html, reason) VALUES ('<p>old page</p>{{posts}}', 'before stats')
		 RETURNING id`,
	).Scan(&versionID); err != nil {
		t.Fatalf("insert version: %v", err)
	}
	if _, err := conn.ExecContext(
		ctx,
		`INSERT INTO journal (key, visitor, payload, match, rules) VALUES ('moves', 'v', '{"x":1}', 1, 1)`,
	); err != nil {
		t.Fatalf("insert journal row: %v", err)
	}

	if _, err := provider.Up(ctx); err != nil {
		t.Fatalf("migrate up: %v", err)
	}

	tables := func() map[string]bool {
		t.Helper()
		rows, err := conn.QueryContext(
			ctx,
			`SELECT name FROM sqlite_master WHERE type = 'table' AND name IN ('version_stats', 'version_visitors')`,
		)
		if err != nil {
			t.Fatalf("read tables: %v", err)
		}
		defer func() { _ = rows.Close() }()
		found := make(map[string]bool)
		for rows.Next() {
			var name string
			if err := rows.Scan(&name); err != nil {
				t.Fatalf("scan table: %v", err)
			}
			found[name] = true
		}
		if err := rows.Err(); err != nil {
			t.Fatalf("read tables: %v", err)
		}
		return found
	}

	for _, table := range []string{"version_stats", "version_visitors"} {
		if !tables()[table] {
			t.Fatalf("%s was not created", table)
		}
		var count int
		if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+table).
			Scan(&count); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		if count != 0 {
			t.Errorf("%s starts with %d rows, want none", table, count)
		}
	}

	var html string
	if err := conn.QueryRowContext(ctx,
		`SELECT html FROM site_versions WHERE id = ?`, versionID,
	).Scan(&html); err != nil {
		t.Fatalf("read version: %v", err)
	}
	if html != "<p>old page</p>{{posts}}" {
		t.Errorf("version html = %q", html)
	}
	var key string
	if err := conn.QueryRowContext(ctx, `SELECT key FROM journal`).Scan(&key); err != nil {
		t.Fatalf("read journal: %v", err)
	}
	if key != "moves" {
		t.Errorf("journal key = %q", key)
	}

	if _, err := provider.DownTo(ctx, 2); err != nil {
		t.Fatalf("migrate down: %v", err)
	}
	found := tables()
	for _, table := range []string{"version_stats", "version_visitors"} {
		if found[table] {
			t.Errorf("%s survived the downgrade", table)
		}
	}
	if err := conn.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM site_versions WHERE id = ?`, versionID,
	).Scan(new(int)); err != nil {
		t.Errorf("version did not survive the downgrade: %v", err)
	}
}

// A world that ran before turn_triggers existed keeps its history: the
// upgrade gives each post turn the posts it answered under the old rule, the
// newest 10, and leaves admin instructions without any.
func TestTurnTriggersMigrationBackfillsExistingTurns(t *testing.T) {
	ctx := context.Background()
	conn, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "world.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	provider, err := goose.NewProvider(goose.DialectSQLite3, conn, beaverdream.WorldMigrationsFS)
	if err != nil {
		t.Fatalf("provider: %v", err)
	}
	if _, err := provider.UpTo(ctx, 1); err != nil {
		t.Fatalf("migrate to the first version: %v", err)
	}

	insert := func(kind, body string) int64 {
		t.Helper()
		res, err := conn.ExecContext(ctx,
			`INSERT INTO turns (kind, author, body) VALUES (?, 'x', ?)`, kind, body)
		if err != nil {
			t.Fatalf("insert %s: %v", kind, err)
		}
		id, _ := res.LastInsertId()
		return id
	}
	first, second := insert("post", "one"), insert("post", "two")
	answered := insert("changeset", "")
	insert("post", "between")
	instruction := insert("changeset", "make it pink")
	var burst []int64
	for range 12 {
		burst = append(burst, insert("post", "burst"))
	}
	capped := insert("changeset", "")
	// Two retries of the capped turn: no posts of their own between them and
	// the turn before.
	retry, retryOfRetry := insert("changeset", ""), insert("changeset", "")

	if _, err := provider.Up(ctx); err != nil {
		t.Fatalf("migrate up: %v", err)
	}

	triggers := func(turn int64) []int64 {
		t.Helper()
		rows, err := conn.QueryContext(ctx,
			`SELECT post_id FROM turn_triggers WHERE turn_id = ? ORDER BY post_id`, turn)
		if err != nil {
			t.Fatalf("read triggers: %v", err)
		}
		defer func() { _ = rows.Close() }()
		var ids []int64
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err != nil {
				t.Fatalf("scan: %v", err)
			}
			ids = append(ids, id)
		}
		if err := rows.Err(); err != nil {
			t.Fatalf("read triggers: %v", err)
		}
		return ids
	}
	if got := triggers(answered); len(got) != 2 || got[0] != first || got[1] != second {
		t.Errorf("first turn triggers = %v, want %d and %d", got, first, second)
	}
	if got := triggers(instruction); len(got) != 0 {
		t.Errorf("an admin instruction got triggers %v", got)
	}
	got := triggers(capped)
	if len(got) != 10 || got[0] != burst[2] || got[9] != burst[11] {
		t.Errorf("capped turn triggers = %v, want the newest 10 of the burst", got)
	}
	// A failed retry from before the upgrade can still be retried: it reads
	// the posts of the turn it copied, and so does a retry of that retry.
	for _, turn := range []int64{retry, retryOfRetry} {
		if again := triggers(turn); !slices.Equal(again, got) {
			t.Errorf("retry %d triggers = %v, want the capped turn's %v", turn, again, got)
		}
	}
}
