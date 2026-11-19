-- +goose Up
-- What visitors did on one version of the page. The model shipped the page
-- and never sees the browser, so these counters are its only play report:
-- loads, journal appends and refusals, state ops, alerts. Rows hang off the
-- version, so reset and a pruned version take them along.
CREATE TABLE version_stats (
  version_id INTEGER NOT NULL REFERENCES site_versions (id) ON DELETE CASCADE,
  -- load, journal, journal_refused, state, state_refused, alert
  kind TEXT NOT NULL,
  -- The feed or state key, or "<key>:<reason>" for a refusal, or the alert
  -- message for an alert. Empty for a page load.
  key TEXT NOT NULL DEFAULT '',
  n INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (version_id, kind, key)
);

-- One row per identity that opened the version, so 42 loads from one person
-- counts once as a visitor and 42 times as a load.
CREATE TABLE version_visitors (
  version_id INTEGER NOT NULL REFERENCES site_versions (id) ON DELETE CASCADE,
  visitor TEXT NOT NULL,
  PRIMARY KEY (version_id, visitor)
);

-- +goose Down
DROP TABLE IF EXISTS version_visitors;

DROP TABLE IF EXISTS version_stats;
