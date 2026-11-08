-- +goose Up
-- Auth and authorization: identities, logins, sessions and roles. This is the
-- part a data reset keeps. task db.migrate.down rolls back only 000002_data.sql.
--
-- The registry is a sqlite file of its own. World files live in WORLDS_DIR and
-- never share a connection or a transaction with it.
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
  -- app (management plane), play (world access), api (a script token).
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

-- +goose Down
DROP TABLE IF EXISTS identity_roles;

DROP TABLE IF EXISTS sessions;

DROP TABLE IF EXISTS logins;

DROP TABLE IF EXISTS identities;
