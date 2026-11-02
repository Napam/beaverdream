-- +goose Up
-- A version can carry a different page for a few visitor names. The overrides
-- live with the version, so undo, restore and heal move them with the rest of
-- the page. Post bodies stay out of these rows: they only hold model markup.
CREATE TABLE user_overrides (
  version_id INTEGER NOT NULL REFERENCES site_versions (id) ON DELETE CASCADE,
  name TEXT NOT NULL,
  name_key TEXT NOT NULL,
  fields TEXT NOT NULL DEFAULT '{}',
  PRIMARY KEY (version_id, name_key)
);

CREATE INDEX user_overrides_name_key ON user_overrides (name_key);

-- +goose Down
DROP TABLE IF EXISTS user_overrides;
