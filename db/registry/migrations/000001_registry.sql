-- +goose Up
-- The registry is a sqlite file of its own. It holds every identity, login,
-- session, invite, role, daily usage row and the world index. World files live
-- in WORLDS_DIR and never share a connection or a transaction with it.
--
-- Ids are 128-bit random values written as 32 lowercase hex characters. Times
-- are the text form the world migrations use, so text comparisons such as
-- expires_at > ? are exact and an expiry instant itself is refused.
CREATE TABLE identities (
  id TEXT PRIMARY KEY,
  display_name TEXT NOT NULL DEFAULT '',
  -- The invite that made the identity, or null for the bootstrap admin and
  -- accounts made by another path. Deliberately a soft reference: an invite
  -- can be deleted without deleting the people it admitted.
  invite_id TEXT,
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
  disabled_at TEXT
);

CREATE INDEX identities_invite ON identities (invite_id);

CREATE TABLE logins (
  id TEXT PRIMARY KEY,
  identity_id TEXT NOT NULL REFERENCES identities (id) ON DELETE CASCADE,
  -- password for now. Another way to sign in becomes another row.
  source TEXT NOT NULL,
  -- The username for source = 'password'.
  external_id TEXT NOT NULL,
  -- argon2id PHC string. Empty only for a login that has no secret yet.
  secret_hash TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
  last_login_at TEXT,
  UNIQUE (source, external_id)
);

CREATE INDEX logins_identity ON logins (identity_id);

CREATE TABLE sessions (
  -- SHA-256 of the token, hex. The token itself is shown once and never stored.
  hash TEXT PRIMARY KEY,
  identity_id TEXT NOT NULL REFERENCES identities (id) ON DELETE CASCADE,
  login_id TEXT NOT NULL REFERENCES logins (id) ON DELETE CASCADE,
  -- app (management plane), play (a board), api (a script token).
  scope TEXT NOT NULL CHECK (scope IN ('app', 'play', 'api')),
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
  last_used_at TEXT,
  -- Null means no expiry, which is the default for play sessions and tokens.
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

-- A revoked role can be granted again, so only active rows are unique.
CREATE UNIQUE INDEX identity_roles_active ON identity_roles (identity_id, role)
WHERE
  revoked_at IS NULL;

CREATE TABLE invites (
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

CREATE INDEX invites_created_at ON invites (created_at);

-- One row per world. The file path is never stored: the file is
-- <WORLDS_DIR>/<id>.db, derived from the immutable id, so a slug never touches
-- the filesystem. creating and deleting are recoverable states: a start
-- finishes or removes them.
CREATE TABLE worlds (
  id TEXT PRIMARY KEY,
  slug TEXT NOT NULL UNIQUE,
  status TEXT NOT NULL DEFAULT 'creating' CHECK (status IN ('creating', 'ready', 'deleting')),
  created_by TEXT REFERENCES identities (id) ON DELETE SET NULL,
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
  updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

CREATE INDEX worlds_status ON worlds (status);

-- One-time codes that move a browser from the app port to a world's play port.
-- The code is bound to the identity, the login and the world, and is spent by
-- one immediate transaction that writes the play session.
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

-- Charges, one row per identity and turn share. day is the UTC date, so the
-- rolling window is a plain text range and needs no time zone math in SQL.
-- world_id is recorded because turn ids are per world file. Admin identities
-- are never written here.
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
