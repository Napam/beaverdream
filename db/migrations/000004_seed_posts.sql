-- +goose Up
-- A board with no posts looks abandoned, and nobody posts into an empty room.
-- These are real rows, not model output, so the board starts with something to
-- read. The last line is the only wink; it reads as nothing until the first
-- rewrite.
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
WHERE
  NOT EXISTS (
    SELECT
      1
    FROM
      posts
  )
ORDER BY
  id;

-- +goose Down
DROP TABLE IF EXISTS seed_posts;
