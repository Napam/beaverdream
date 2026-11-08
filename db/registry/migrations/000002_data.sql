-- +goose Up
-- Everything that is not auth: invites, the world index, handoffs and daily
-- usage. task db.migrate.down drops this migration; task db.migrate.up
-- recreates it, keeping accounts and sessions from 000001_auth.sql.
--
-- CREATE ... IF NOT EXISTS lets a pre-split registry already at version 1
-- upgrade without a wipe: goose sees version 1 as applied and runs this one.
CREATE TABLE IF NOT EXISTS invites (
  id TEXT PRIMARY KEY,
  -- SHA-256 of the code, hex. The code itself is shown once.
  code_hash TEXT NOT NULL UNIQUE,
  label TEXT NOT NULL DEFAULT '',
  -- Null means the bootstrap admin.
  created_by TEXT REFERENCES identities (id) ON DELETE SET NULL,
  max_uses INTEGER NOT NULL CHECK (max_uses > 0),
  uses INTEGER NOT NULL DEFAULT 0 CHECK (uses >= 0),
  expires_at TEXT,
  revoked_at TEXT,
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

CREATE INDEX IF NOT EXISTS invites_created_at ON invites (created_at);

-- One row per world. The file path is never stored: the file is
-- <WORLDS_DIR>/<id>.db, derived from the immutable id, so a slug never touches
-- the filesystem. creating and deleting are recoverable states: a start
-- finishes or removes them.
CREATE TABLE IF NOT EXISTS worlds (
  id TEXT PRIMARY KEY,
  slug TEXT NOT NULL UNIQUE,
  status TEXT NOT NULL DEFAULT 'creating' CHECK (status IN ('creating', 'ready', 'deleting')),
  created_by TEXT REFERENCES identities (id) ON DELETE SET NULL,
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
  updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

CREATE INDEX IF NOT EXISTS worlds_status ON worlds (status);

-- One-time codes that move a browser from the app port to a world's play port.
-- The code is bound to the identity, the login and the world, and is spent by
-- one immediate transaction that writes the play session.
CREATE TABLE IF NOT EXISTS handoffs (
  code_hash TEXT PRIMARY KEY,
  identity_id TEXT NOT NULL REFERENCES identities (id) ON DELETE CASCADE,
  login_id TEXT NOT NULL REFERENCES logins (id) ON DELETE CASCADE,
  world_id TEXT NOT NULL REFERENCES worlds (id) ON DELETE CASCADE,
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
  expires_at TEXT NOT NULL,
  used_at TEXT
);

CREATE INDEX IF NOT EXISTS handoffs_expires ON handoffs (expires_at);

-- Charges, one row per identity and turn share. day is the UTC date, so the
-- rolling window is a plain text range and needs no time zone math in SQL.
-- world_id is recorded because turn ids are per world file. Admin identities
-- are never written here.
CREATE TABLE IF NOT EXISTS usage (
  id TEXT PRIMARY KEY,
  identity_id TEXT NOT NULL,
  world_id TEXT NOT NULL,
  day TEXT NOT NULL,
  turn_id INTEGER NOT NULL DEFAULT 0,
  tokens INTEGER NOT NULL DEFAULT 0 CHECK (tokens >= 0),
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

CREATE INDEX IF NOT EXISTS usage_day_identity ON usage (day, identity_id);

CREATE INDEX IF NOT EXISTS usage_day ON usage (day);

-- +goose Down
DROP TABLE IF EXISTS usage;

DROP TABLE IF EXISTS handoffs;

DROP TABLE IF EXISTS worlds;

DROP TABLE IF EXISTS invites;
