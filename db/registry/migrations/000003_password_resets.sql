-- +goose Up
-- One outstanding password reset per password login. The link itself is shown
-- once; only its hash is stored. Issuing another link replaces the row, and a
-- successful redemption deletes it.
CREATE TABLE password_resets (
  login_id TEXT PRIMARY KEY REFERENCES logins (id) ON DELETE CASCADE,
  code_hash TEXT NOT NULL UNIQUE,
  expires_at TEXT NOT NULL
);

-- +goose Down
DROP TABLE IF EXISTS password_resets;
