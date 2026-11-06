-- +goose Up
-- Visitor state: counts one visitor owns that outlive every rewrite. A key is
-- declared in the version's state_schema with scope "visitor"; the holdings
-- stay here under the key's name whatever later schemas say, so a key that
-- is declared again gets its balances back. Heal keeps them; only reset and
-- an admin clear delete them.
CREATE TABLE state_holdings (
  -- Raw visitor cookie id. Never published; the world sees its hash.
  visitor TEXT NOT NULL,
  key TEXT NOT NULL,
  amount INTEGER NOT NULL CHECK (amount >= 0),
  -- The display name on the latest write, for the top lists.
  name TEXT NOT NULL DEFAULT '',
  PRIMARY KEY (visitor, key)
);

CREATE INDEX state_holdings_key ON state_holdings (key, amount DESC);

-- Successful visitor ops only, one row per key changed. The rows of one trade
-- share op_id. This is the audit log and the rolling-hour window for
-- per_hour; rows older than a day are pruned on write.
CREATE TABLE state_ops (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  op_id INTEGER NOT NULL,
  visitor TEXT NOT NULL,
  name TEXT NOT NULL DEFAULT '',
  -- add, spend, trade
  op TEXT NOT NULL,
  -- The key for add and spend, the recipe name for trade.
  source TEXT NOT NULL,
  key TEXT NOT NULL,
  delta INTEGER NOT NULL,
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

CREATE INDEX state_ops_window ON state_ops (visitor, key, created_at);

CREATE INDEX state_ops_created_at ON state_ops (created_at);

-- +goose Down
DROP TABLE IF EXISTS state_ops;

DROP TABLE IF EXISTS state_holdings;
