-- +goose Up
-- The whole schema in one file. beaverdream starts as a plain message board:
-- no posts, a light seed design, and empty model-declared state.
--
-- turns is the room. Everything anyone says lives here in one order: visitor
-- posts, the webmaster's turns, and the server events that move the page
-- without the model. turn.id is the conversation order, and the page the
-- webmaster produced hangs off the turn that produced it.
CREATE TABLE turns (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  -- post, webmaster, heal, undo, restore, clear
  kind TEXT NOT NULL,
  author TEXT NOT NULL DEFAULT '',
  -- What prompted a webmaster turn (an admin instruction, empty for posts),
  -- or the text of a server event. Never rendered on the board.
  body TEXT NOT NULL DEFAULT '',
  -- The webmaster's own sentence from done. Read back to it next turn.
  said TEXT NOT NULL DEFAULT '',
  -- A server-written summary of what the turn changed. No markup.
  changes TEXT NOT NULL DEFAULT '',
  -- The webmaster's private notes, copied forward until it replaces them.
  notes TEXT NOT NULL DEFAULT '',
  -- running, changed, kept, silent, error. Empty for posts and events.
  status TEXT NOT NULL DEFAULT '',
  error_text TEXT NOT NULL DEFAULT '',
  -- The full model conversation, for /admin only. Never fed back.
  messages TEXT NOT NULL DEFAULT '[]',
  duration_ms INTEGER NOT NULL DEFAULT 0,
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

CREATE INDEX turns_kind_id ON turns (kind, id);

CREATE INDEX turns_kind_created_at ON turns (kind, created_at);

-- The board only shows posts. The view is the code-owned post list: model
-- text (said, notes, changes) can never reach it by accident.
CREATE VIEW posts AS
SELECT
  id,
  author,
  body,
  created_at
FROM
  turns
WHERE
  kind = 'post';

-- site_versions is the render. The current site is simply max(id). Each
-- mutation appends a row, so a bad render never destroys the previous one.
-- board holds literal {{posts}} where the code-owned post list goes. turn_id
-- names the turn that produced this render, including server events.
CREATE TABLE site_versions (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  css TEXT NOT NULL DEFAULT '',
  note TEXT NOT NULL DEFAULT '',
  header TEXT NOT NULL DEFAULT '',
  board TEXT NOT NULL DEFAULT '',
  footer TEXT NOT NULL DEFAULT '',
  state_schema TEXT NOT NULL DEFAULT '{}',
  -- Journal keys the page may append to. The model declares them; the server
  -- only checks names, sizes and order.
  journal_schema TEXT NOT NULL DEFAULT '{}',
  turn_id INTEGER REFERENCES turns (id) ON DELETE SET NULL,
  reason TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

-- One version per turn. The log joins versions by turn_id, so a second row
-- would duplicate the turn's line.
CREATE UNIQUE INDEX site_versions_turn_id ON site_versions (turn_id)
WHERE
  turn_id IS NOT NULL;

-- The room outlives the prompt window. When old turns fall out of the log, the
-- server folds them into a rolling summary. Each row covers everything up to
-- through_turn_id, chained from the previous row, so the newest row is the
-- webmaster's memory of its own past. Summaries never contain markup.
CREATE TABLE room_summaries (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  through_turn_id INTEGER NOT NULL REFERENCES turns (id) ON DELETE CASCADE,
  text TEXT NOT NULL,
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

CREATE INDEX room_summaries_through ON room_summaries (through_turn_id);

-- The model can declare a tiny shared state schema: counters, lists and
-- choices. The page reads and writes it through /x/state. Values are JSON
-- text, never HTML.
CREATE TABLE site_state (
  key TEXT PRIMARY KEY,
  value TEXT NOT NULL DEFAULT '',
  updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

-- The journal is the one flexible store: an append-only log of small JSON
-- events grouped by a model-declared key. The server owns order, identity,
-- time and the rules stamp; it never reads what a payload means. A page reads
-- rows with ?since=<id> and folds them in its own script.
CREATE TABLE journal (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  key TEXT NOT NULL,
  -- Opaque per-browser id, set by the server. Display names are not unique,
  -- so this is what can hold a seat.
  visitor TEXT NOT NULL DEFAULT '',
  -- Compact JSON written by the page. Never rendered by the server.
  payload TEXT NOT NULL DEFAULT '{}',
  -- A match boundary the page controls, so a rematch does not replay the
  -- previous game.
  match INTEGER NOT NULL DEFAULT 1,
  -- The rules number that was declared when the row was written, so changing
  -- the reducer does not replay old rows through new code.
  rules INTEGER NOT NULL DEFAULT 1,
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

CREATE INDEX journal_key_id ON journal (key, id);

-- The seed is a plain light board: a title, a simple post form, and an empty
-- thread. The model takes it from here.
INSERT INTO
  site_versions (css, note, header, board, footer)
VALUES
  (
    '',
    'welcome. be nice to each other.',
    '<h1>beaverdream</h1><p class="tagline">a small message board</p>',
    '<main>' ||
      '<form class="postbox" method="post" action="/posts">' ||
      '<p class="postbox-title">post a message</p>' ||
      '<textarea name="body" required maxlength="2000" rows="3" placeholder="say something" aria-label="message"></textarea>' ||
      '<div class="postbox-actions"><span>posting as {{name}}</span>' ||
      '<button type="submit">post</button></div>' ||
      '</form>' ||
      '<div class="thread">{{posts}}</div>' ||
      '</main>',
    '<footer class="colophon">beaverdream</footer>'
  );

-- +goose Down
DROP TABLE IF EXISTS journal;

DROP TABLE IF EXISTS room_summaries;

DROP TABLE IF EXISTS site_state;

DROP TABLE IF EXISTS site_versions;

DROP VIEW IF EXISTS posts;

DROP TABLE IF EXISTS turns;
