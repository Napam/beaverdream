-- +goose Up
-- The model can declare a tiny shared state schema: counters, lists and
-- choices. The page reads and writes it through /x/state. Values are JSON
-- text, never HTML.
ALTER TABLE site_versions
ADD COLUMN state_schema TEXT NOT NULL DEFAULT '{}';

CREATE TABLE site_state (
  key TEXT PRIMARY KEY,
  value TEXT NOT NULL DEFAULT '',
  updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

-- +goose Down
DROP TABLE IF EXISTS site_state;

ALTER TABLE site_versions
DROP COLUMN state_schema;
