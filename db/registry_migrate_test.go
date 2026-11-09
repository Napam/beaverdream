package db_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"testing/fstest"

	"github.com/Napam/beaverdream"
	"github.com/pressly/goose/v3"
	_ "modernc.org/sqlite"
)

func openRegistryDB(t *testing.T) *sql.DB {
	t.Helper()
	conn, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "registry.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	// One connection so the pragma applies to every statement.
	conn.SetMaxOpenConns(1)
	if _, err := conn.ExecContext(context.Background(), `PRAGMA foreign_keys = ON`); err != nil {
		t.Fatalf("foreign keys: %v", err)
	}
	return conn
}

// The registry splits into auth and data. Dropping the data migration keeps
// identities, logins, sessions and roles, and a later up recreates the data
// tables empty.
func TestRegistryDataMigrationResetKeepsAuth(t *testing.T) {
	ctx := context.Background()
	conn := openRegistryDB(t)

	provider, err := goose.NewProvider(goose.DialectSQLite3, conn, beaverdream.RegistryMigrationsFS)
	if err != nil {
		t.Fatalf("provider: %v", err)
	}
	if _, err := provider.Up(ctx); err != nil {
		t.Fatalf("up: %v", err)
	}

	authInserts := []string{
		`INSERT INTO identities (id, display_name) VALUES ('identity-1', 'Ada')`,
		`INSERT INTO logins (id, identity_id, source, external_id) VALUES ('login-1', 'identity-1', 'password', 'ada')`,
		`INSERT INTO sessions (hash, identity_id, login_id, scope) VALUES ('session-1', 'identity-1', 'login-1', 'app')`,
		`INSERT INTO identity_roles (id, identity_id, role) VALUES ('role-1', 'identity-1', 'ADMIN')`,
	}
	dataInserts := []string{
		`INSERT INTO invites (id, code_hash, created_by, max_uses) VALUES ('invite-1', 'invite-code', 'identity-1', 3)`,
		`INSERT INTO worlds (id, slug, status, created_by) VALUES ('world-1', 'first-world', 'ready', 'identity-1')`,
		`INSERT INTO handoffs (code_hash, identity_id, login_id, world_id, expires_at) VALUES ('handoff-1', 'identity-1', 'login-1', 'world-1', '2999-01-01T00:00:00.000Z')`,
		`INSERT INTO usage (id, identity_id, world_id, day) VALUES ('usage-1', 'identity-1', 'world-1', '2026-01-01')`,
		`INSERT INTO password_resets (login_id, code_hash, expires_at) VALUES ('login-1', 'reset-code', '2999-01-01T00:00:00.000Z')`,
	}
	for _, statement := range append(authInserts, dataInserts...) {
		if _, err := conn.ExecContext(ctx, statement); err != nil {
			t.Fatalf("%s: %v", statement, err)
		}
	}

	if _, err := provider.DownTo(ctx, 1); err != nil {
		t.Fatalf("down to 1: %v", err)
	}

	authTables := []string{"identities", "logins", "sessions", "identity_roles"}
	for _, table := range authTables {
		if got := countRows(t, ctx, conn, table); got != 1 {
			t.Errorf("%s rows after down = %d, want 1", table, got)
		}
	}
	for _, table := range []string{"invites", "worlds", "handoffs", "usage", "password_resets"} {
		if tableExists(t, ctx, conn, table) {
			t.Errorf("%s still exists after down to 1", table)
		}
	}

	if _, err := provider.Up(ctx); err != nil {
		t.Fatalf("up again: %v", err)
	}

	for _, table := range []string{
		"invites", "worlds", "handoffs", "usage", "password_resets",
	} {
		if !tableExists(t, ctx, conn, table) {
			t.Errorf("%s missing after the second up", table)
			continue
		}
		if got := countRows(t, ctx, conn, table); got != 0 {
			t.Errorf("%s rows after recreate = %d, want 0", table, got)
		}
	}
	for _, table := range authTables {
		if got := countRows(t, ctx, conn, table); got != 1 {
			t.Errorf("%s rows after recreate = %d, want 1", table, got)
		}
	}

	var version int
	if err := conn.QueryRowContext(ctx,
		`SELECT MAX(version_id) FROM goose_db_version WHERE is_applied = 1`,
	).Scan(&version); err != nil {
		t.Fatalf("read goose version: %v", err)
	}
	if version != 3 {
		t.Errorf("goose version = %d, want 3", version)
	}
}

// oldRegistrySchema is the pre-split registry migration: one file with every
// table. A registry created before the split records it as version 1.
const oldRegistrySchema = `-- +goose Up
CREATE TABLE identities (
  id TEXT PRIMARY KEY,
  display_name TEXT NOT NULL DEFAULT '',
  invite_id TEXT,
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
  disabled_at TEXT
);

CREATE INDEX identities_invite ON identities (invite_id);

CREATE TABLE logins (
  id TEXT PRIMARY KEY,
  identity_id TEXT NOT NULL REFERENCES identities (id) ON DELETE CASCADE,
  source TEXT NOT NULL,
  external_id TEXT NOT NULL,
  secret_hash TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
  last_login_at TEXT,
  UNIQUE (source, external_id)
);

CREATE INDEX logins_identity ON logins (identity_id);

CREATE TABLE sessions (
  hash TEXT PRIMARY KEY,
  identity_id TEXT NOT NULL REFERENCES identities (id) ON DELETE CASCADE,
  login_id TEXT NOT NULL REFERENCES logins (id) ON DELETE CASCADE,
  scope TEXT NOT NULL CHECK (scope IN ('app', 'play', 'api')),
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
  last_used_at TEXT,
  expires_at TEXT,
  revoked_at TEXT
);

CREATE INDEX sessions_identity ON sessions (identity_id);

CREATE INDEX sessions_expires ON sessions (expires_at);

CREATE TABLE identity_roles (
  id TEXT PRIMARY KEY,
  identity_id TEXT NOT NULL REFERENCES identities (id) ON DELETE CASCADE,
  role TEXT NOT NULL CHECK (role IN ('ADMIN')),
  granted_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
  revoked_at TEXT
);

CREATE UNIQUE INDEX identity_roles_active ON identity_roles (identity_id, role)
WHERE
  revoked_at IS NULL;

CREATE TABLE invites (
  id TEXT PRIMARY KEY,
  code_hash TEXT NOT NULL UNIQUE,
  label TEXT NOT NULL DEFAULT '',
  created_by TEXT REFERENCES identities (id) ON DELETE SET NULL,
  max_uses INTEGER NOT NULL CHECK (max_uses > 0),
  uses INTEGER NOT NULL DEFAULT 0 CHECK (uses >= 0),
  expires_at TEXT,
  revoked_at TEXT,
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

CREATE INDEX invites_created_at ON invites (created_at);

CREATE TABLE worlds (
  id TEXT PRIMARY KEY,
  slug TEXT NOT NULL UNIQUE,
  status TEXT NOT NULL DEFAULT 'creating' CHECK (status IN ('creating', 'ready', 'deleting')),
  created_by TEXT REFERENCES identities (id) ON DELETE SET NULL,
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
  updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

CREATE INDEX worlds_status ON worlds (status);

CREATE TABLE handoffs (
  code_hash TEXT PRIMARY KEY,
  identity_id TEXT NOT NULL REFERENCES identities (id) ON DELETE CASCADE,
  login_id TEXT NOT NULL REFERENCES logins (id) ON DELETE CASCADE,
  world_id TEXT NOT NULL REFERENCES worlds (id) ON DELETE CASCADE,
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
  expires_at TEXT NOT NULL,
  used_at TEXT
);

CREATE INDEX handoffs_expires ON handoffs (expires_at);

CREATE TABLE usage (
  id TEXT PRIMARY KEY,
  identity_id TEXT NOT NULL,
  world_id TEXT NOT NULL,
  day TEXT NOT NULL,
  turn_id INTEGER NOT NULL DEFAULT 0,
  tokens INTEGER NOT NULL DEFAULT 0 CHECK (tokens >= 0),
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

CREATE INDEX usage_day_identity ON usage (day, identity_id);

CREATE INDEX usage_day ON usage (day);

-- +goose Down
DROP TABLE IF EXISTS usage;

DROP TABLE IF EXISTS handoffs;

DROP TABLE IF EXISTS worlds;

DROP TABLE IF EXISTS invites;

DROP TABLE IF EXISTS identity_roles;

DROP TABLE IF EXISTS sessions;

DROP TABLE IF EXISTS logins;

DROP TABLE IF EXISTS identities;
`

// A registry created before the split sits at version 1 with every table. Up
// runs only the data migration, so the old rows survive; the reset still works
// on the upgraded file.
func TestRegistryUpgradesPreSplitSchema(t *testing.T) {
	ctx := context.Background()
	conn := openRegistryDB(t)

	oldProvider, err := goose.NewProvider(goose.DialectSQLite3, conn, fstest.MapFS{
		"000001_init.sql": &fstest.MapFile{Data: []byte(oldRegistrySchema)},
	})
	if err != nil {
		t.Fatalf("old provider: %v", err)
	}
	if _, err := oldProvider.Up(ctx); err != nil {
		t.Fatalf("old up: %v", err)
	}

	inserts := []string{
		`INSERT INTO identities (id, display_name) VALUES ('identity-1', 'Ada')`,
		`INSERT INTO logins (id, identity_id, source, external_id) VALUES ('login-1', 'identity-1', 'password', 'ada')`,
		`INSERT INTO sessions (hash, identity_id, login_id, scope) VALUES ('session-1', 'identity-1', 'login-1', 'app')`,
		`INSERT INTO identity_roles (id, identity_id, role) VALUES ('role-1', 'identity-1', 'ADMIN')`,
		`INSERT INTO invites (id, code_hash, created_by, max_uses) VALUES ('invite-1', 'invite-code', 'identity-1', 3)`,
		`INSERT INTO worlds (id, slug, status, created_by) VALUES ('world-1', 'first-world', 'ready', 'identity-1')`,
		`INSERT INTO handoffs (code_hash, identity_id, login_id, world_id, expires_at) VALUES ('handoff-1', 'identity-1', 'login-1', 'world-1', '2999-01-01T00:00:00.000Z')`,
		`INSERT INTO usage (id, identity_id, world_id, day) VALUES ('usage-1', 'identity-1', 'world-1', '2026-01-01')`,
	}
	for _, statement := range inserts {
		if _, err := conn.ExecContext(ctx, statement); err != nil {
			t.Fatalf("%s: %v", statement, err)
		}
	}

	provider, err := goose.NewProvider(goose.DialectSQLite3, conn, beaverdream.RegistryMigrationsFS)
	if err != nil {
		t.Fatalf("provider: %v", err)
	}
	if _, err := provider.Up(ctx); err != nil {
		t.Fatalf("up: %v", err)
	}

	authTables := []string{"identities", "logins", "sessions", "identity_roles"}
	dataTables := []string{"invites", "worlds", "handoffs", "usage"}
	for _, table := range append(append([]string{}, authTables...), dataTables...) {
		if got := countRows(t, ctx, conn, table); got != 1 {
			t.Errorf("%s rows after upgrade = %d, want 1", table, got)
		}
	}
	if !tableExists(t, ctx, conn, "password_resets") {
		t.Fatal("password_resets missing after upgrade")
	}
	if got := countRows(t, ctx, conn, "password_resets"); got != 0 {
		t.Fatalf("password_resets rows after upgrade = %d, want 0", got)
	}
	if _, err := conn.ExecContext(ctx,
		`INSERT INTO password_resets (login_id, code_hash, expires_at)
		 VALUES ('login-1', 'reset-code', '2999-01-01T00:00:00.000Z')`,
	); err != nil {
		t.Fatalf("insert password reset: %v", err)
	}
	resetTables := append(append([]string{}, dataTables...), "password_resets")

	var version int
	if err := conn.QueryRowContext(ctx,
		`SELECT MAX(version_id) FROM goose_db_version WHERE is_applied = 1`,
	).Scan(&version); err != nil {
		t.Fatalf("read goose version: %v", err)
	}
	if version != 3 {
		t.Errorf("goose version = %d, want 3", version)
	}

	if _, err := provider.DownTo(ctx, 1); err != nil {
		t.Fatalf("down to 1: %v", err)
	}
	for _, table := range authTables {
		if got := countRows(t, ctx, conn, table); got != 1 {
			t.Errorf("%s rows after down = %d, want 1", table, got)
		}
	}
	for _, table := range resetTables {
		if tableExists(t, ctx, conn, table) {
			t.Errorf("%s still exists after down to 1", table)
		}
	}

	if _, err := provider.Up(ctx); err != nil {
		t.Fatalf("up again: %v", err)
	}
	for _, table := range resetTables {
		if !tableExists(t, ctx, conn, table) {
			t.Errorf("%s missing after the second up", table)
			continue
		}
		if got := countRows(t, ctx, conn, table); got != 0 {
			t.Errorf("%s rows after recreate = %d, want 0", table, got)
		}
	}
	for _, table := range authTables {
		if got := countRows(t, ctx, conn, table); got != 1 {
			t.Errorf("%s rows after recreate = %d, want 1", table, got)
		}
	}
}

func tableExists(t *testing.T, ctx context.Context, conn *sql.DB, table string) bool {
	t.Helper()
	var found string
	err := conn.QueryRowContext(ctx,
		`SELECT name FROM sqlite_master WHERE type = 'table' AND name = ?`, table,
	).Scan(&found)
	if err == sql.ErrNoRows {
		return false
	}
	if err != nil {
		t.Fatalf("look up table %s: %v", table, err)
	}
	return true
}

func countRows(t *testing.T, ctx context.Context, conn *sql.DB, table string) int {
	t.Helper()
	var count int
	if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+table).Scan(&count); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return count
}
