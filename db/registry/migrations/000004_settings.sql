-- +goose Up
-- Instance-wide switches an admin can change without a restart.
CREATE TABLE settings (
  key TEXT PRIMARY KEY,
  value TEXT NOT NULL,
  updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

INSERT INTO settings (key, value) VALUES ('world_creation_open', '1');

-- +goose Down
DROP TABLE IF EXISTS settings;
