-- +goose Up
-- The whole schema in one file. beaverdream starts as a plain message board:
-- four seed posts, a light seed design, and empty model-declared state.
--
-- posts is the truth: everything the board knows came from here.
CREATE TABLE posts (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  author TEXT NOT NULL DEFAULT '',
  body TEXT NOT NULL,
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

-- site_versions is the render. The current site is simply max(id). Each
-- mutation appends a row, so a bad render never destroys the previous one.
-- board holds literal {{posts}} where the code-owned post list goes.
CREATE TABLE site_versions (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  css TEXT NOT NULL DEFAULT '',
  note TEXT NOT NULL DEFAULT '',
  header TEXT NOT NULL DEFAULT '',
  board TEXT NOT NULL DEFAULT '',
  footer TEXT NOT NULL DEFAULT '',
  trigger_post_id INTEGER REFERENCES posts (id),
  reason TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
  state_schema TEXT NOT NULL DEFAULT '{}'
);

-- mutation_runs keeps the conversation behind each render so the admin page
-- can show exactly what the model saw and did.
CREATE TABLE mutation_runs (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  kind TEXT NOT NULL,
  trigger_post_id INTEGER REFERENCES posts (id),
  instruction TEXT NOT NULL DEFAULT '',
  reason TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL,
  error_text TEXT NOT NULL DEFAULT '',
  messages TEXT NOT NULL DEFAULT '[]',
  version_id INTEGER REFERENCES site_versions (id),
  duration_ms INTEGER NOT NULL DEFAULT 0,
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

CREATE INDEX mutation_runs_created_at ON mutation_runs (created_at);

-- A board with no posts looks abandoned, and nobody posts into an empty room.
-- These are real rows, not model output, so the board starts with something to
-- read. The last line is the only wink; it reads as nothing until the first
-- rewrite. Reset copies them back.
CREATE TABLE seed_posts (
  id INTEGER PRIMARY KEY,
  author TEXT NOT NULL,
  body TEXT NOT NULL,
  age TEXT NOT NULL
);

INSERT INTO
  seed_posts (id, author, body, age)
VALUES
  (1, 'Anonymous', 'first', '-2 days'),
  (
    2,
    'kit',
    'is the east bank still flooded or can we walk through yet',
    '-1 day'
  ),
  (
    3,
    'Anonymous',
    '>>2 still flooded. took the long way',
    '-1 day'
  ),
  (
    4,
    'oldtimer',
    'anyone else think the site looked different yesterday',
    '-5 hours'
  );

INSERT INTO
  posts (author, body, created_at)
SELECT
  author,
  body,
  strftime('%Y-%m-%dT%H:%M:%fZ', 'now', age)
FROM
  seed_posts
ORDER BY
  id;

-- The model can declare a tiny shared state schema: counters, lists and
-- choices. The page reads and writes it through /x/state. Values are JSON
-- text, never HTML.
CREATE TABLE site_state (
  key TEXT PRIMARY KEY,
  value TEXT NOT NULL DEFAULT '',
  updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

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

-- The seed is a plain light board: a title, a simple post form, and the
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
DROP TABLE IF EXISTS user_overrides;

DROP TABLE IF EXISTS site_state;

DROP TABLE IF EXISTS seed_posts;

DROP TABLE IF EXISTS mutation_runs;

DROP TABLE IF EXISTS site_versions;

DROP TABLE IF EXISTS posts;
