-- +goose Up
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
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
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

INSERT INTO
  site_versions (css, note, header, board, footer)
VALUES
  (
    '',
    'nothing has happened yet. post something and i will redecorate.',
    '<h1>beaverdream</h1><p class="tagline">a board that rewrites itself</p>',
    '<main>{{posts}}</main>',
    '<footer class="colophon">built by a machine that is easily persuaded</footer>'
  );

-- +goose Down
DROP TABLE IF EXISTS mutation_runs;

DROP TABLE IF EXISTS site_versions;

DROP TABLE IF EXISTS posts;
