-- +goose Up
-- The whole world schema in one file. beaverdream starts as a plain page with
-- a message board: no posts, a light seed design, and empty model-declared
-- state.
--
-- turns is the room. Everything anyone says lives here in one order: visitor
-- posts, the webmaster's changesets, and the server events that move the page
-- without the model. turn.id is the conversation order, and the page a turn
-- produced hangs off that turn.
CREATE TABLE turns (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  -- post, changeset, reply, heal, undo, restore, journal, state, alert
  kind TEXT NOT NULL,
  author TEXT NOT NULL DEFAULT '',
  -- What prompted a changeset (an admin instruction, empty for posts), or
  -- the text of a server event. Never rendered on the board.
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

-- The board shows posts and the webmaster's replies. The view is the
-- code-owned post list: model text (said, notes, changes) can never reach it
-- by accident. kind tells the board which is which, never the author name.
CREATE VIEW posts AS
SELECT
  id,
  author,
  body,
  created_at,
  kind
FROM
  turns
WHERE
  kind IN ('post', 'reply');

-- site_versions is the render. The current site is simply max(id). Each turn
-- that changes the page appends a row, so a bad render never destroys the
-- previous one. html is the page body and holds literal {{posts}} where the
-- code-owned post list goes. turn_id names the turn that produced this
-- render, including server events.
CREATE TABLE site_versions (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  css TEXT NOT NULL DEFAULT '',
  html TEXT NOT NULL DEFAULT '',
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
-- choices. The page reads and writes it through /state. Values are JSON text,
-- never HTML.
CREATE TABLE shared_state (
  key TEXT PRIMARY KEY,
  value TEXT NOT NULL DEFAULT '',
  updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

-- Visitor state: counts one visitor owns that outlive every rewrite. A key is
-- declared in the version's state_schema with scope "visitor"; the holdings
-- stay here under the key's name whatever later schemas say, so a key that is
-- declared again gets its balances back. Heal keeps them; only reset and an
-- admin clear delete them.
CREATE TABLE state_holdings (
  -- The identity id as the board sees it (publicVisitor). Display names are
  -- not unique, so this is the stable key.
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

-- The journal is the one flexible store: an append-only log of small JSON
-- events grouped by a model-declared key. The server owns order, identity,
-- time and the rules stamp; it never reads what a payload means. A page reads
-- rows with ?since=<id> and folds them in its own script.
CREATE TABLE journal (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  key TEXT NOT NULL,
  -- The identity id as the board sees it (publicVisitor). Display names are
  -- not unique, so this is the stable key.
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

-- The seed is a plain light board: a title, one lede, a simple post form, and
-- an empty thread. The model takes it from here. Its layout lives in its own
-- stylesheet, not in the base, so a rewrite starts from nothing it did not
-- write. The form posts through Datastar, so the page stays put and the post
-- arrives over the live stream; without Datastar it is still a plain form post.
INSERT INTO
  site_versions (css, html)
VALUES
  (
    'body{padding:1.5rem;max-width:48rem;margin:0 auto}
a{color:var(--accent);text-decoration:none}
a:hover{text-decoration:underline}
h1{font-size:1.7em;line-height:1.25;letter-spacing:-.02em;margin:0 0 .4em}
.tagline{color:var(--muted);margin:0 0 1.25em;font-size:.95em}
.lede{color:var(--muted);max-width:34em;margin:0 0 1.25em;line-height:1.5}
main{margin-top:1.25em}
.colophon{margin-top:2em;padding-top:.8em;border-top:1px solid var(--border);color:var(--muted);font-size:.8em}
.postbox{background:var(--panel);border:1px solid var(--border);border-radius:4px;padding:.9em;display:flex;flex-direction:column;gap:.55em;margin:0 0 1.5em}
.postbox-title{margin:0;font-weight:700;font-size:.95em}
.postbox textarea{width:100%;min-height:5.5em;resize:vertical;background:color-mix(in srgb,var(--bg) 90%,#fff 10%);color:var(--fg);border:1px solid var(--border);border-radius:3px;padding:.5em .6em;font:inherit;font-size:.95em}
.postbox textarea:focus{outline:2px solid var(--accent);outline-offset:1px;border-color:transparent}
.postbox-actions{display:flex;align-items:center;justify-content:space-between;gap:1em;color:var(--muted);font-size:.8em}
.postbox button{background:var(--accent);color:#fff;border:0;border-radius:3px;padding:.5em 1em;font:inherit;font-weight:600;cursor:pointer}
.postbox button:hover{filter:brightness(1.08)}',
    '<h1>beaverdream</h1><p class="tagline">a world you build together</p>' ||
      '<main>' ||
      '<p class="lede">everyone here sees the same page. Naps is the webmaster. say what this place should become, or just talk.</p>' ||
      '<form class="postbox" method="post" action="posts" data-on:submit="@post(''posts'', {contentType: ''form''}); el.reset()">' ||
      '<p class="postbox-title">say something</p>' ||
      '<textarea name="body" required maxlength="2000" rows="3" placeholder="a hello, a request, anything" aria-label="post"></textarea>' ||
      '<div class="postbox-actions"><span>posting as {{name}}</span>' ||
      '<button type="submit">post</button></div>' ||
      '</form>' ||
      '<div class="thread">{{posts}}</div>' ||
      '</main>' ||
      '<footer class="colophon">one page, everyone in it</footer>'
  );

-- +goose Down
DROP VIEW IF EXISTS posts;

DROP TABLE IF EXISTS state_ops;

DROP TABLE IF EXISTS state_holdings;

DROP TABLE IF EXISTS journal;

DROP TABLE IF EXISTS room_summaries;

DROP TABLE IF EXISTS shared_state;

DROP TABLE IF EXISTS site_versions;

DROP TABLE IF EXISTS turns;
