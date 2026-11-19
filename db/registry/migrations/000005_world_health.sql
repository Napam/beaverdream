-- +goose Up
-- The lobby's view of each world, so a broken, stale or nearly full world is
-- visible without opening it. The world file stays the source of truth: this
-- row is a cache written after a turn lands and filled once at startup.
CREATE TABLE world_health (
  world_id TEXT PRIMARY KEY REFERENCES worlds (id) ON DELETE CASCADE,
  -- The last stored turn, '' before a world has played.
  last_turn_at TEXT NOT NULL DEFAULT '',
  -- changed, kept, silent, error, alert, or '' for a server event.
  last_status TEXT NOT NULL DEFAULT '',
  -- length(css) + length(html) of the current version, in bytes.
  page_bytes INTEGER NOT NULL DEFAULT 0,
  -- Browser errors recorded against the current version.
  alerts INTEGER NOT NULL DEFAULT 0,
  updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

-- +goose Down
DROP TABLE IF EXISTS world_health;
