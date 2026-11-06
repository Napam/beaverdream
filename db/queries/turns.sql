-- name: CreatePost :one
INSERT INTO
  turns (kind, author, body)
VALUES
  ('post', ?, ?)
RETURNING
  id,
  author,
  body,
  created_at,
  kind;

-- name: CreateReply :one
-- The webmaster's words on the board: done's reply, written in its turn's
-- finish transaction so it takes the newest id, or a say while the turn runs.
INSERT INTO
  turns (kind, author, body)
VALUES
  ('reply', 'webmaster', ?)
RETURNING
  id;

-- name: ListPosts :many
SELECT
  *
FROM
  posts
ORDER BY
  id ASC;

-- name: GetPost :one
SELECT
  *
FROM
  posts
WHERE
  id = ?;

-- name: LatestPostID :one
SELECT
  CAST(COALESCE(MAX(id), 0) AS INTEGER)
FROM
  posts;

-- name: ListRecentPosts :many
SELECT
  *
FROM
  posts
ORDER BY
  id DESC
LIMIT
  ?;

-- name: ListPostsForTurn :many
-- The posts a webmaster turn answered: the ones between it and the webmaster
-- turn before it, which is the range the worker coalesced into that turn.
-- Replies are the webmaster's own words, never a trigger.
SELECT
  p.*
FROM
  posts p
WHERE
  p.kind = 'post'
  AND p.id < sqlc.arg(turn_id)
  AND p.id > COALESCE(
    (
      SELECT
        MAX(t.id)
      FROM
        turns t
      WHERE
        t.kind = 'webmaster'
        AND t.id < sqlc.arg(turn_id)
    ),
    0
  )
ORDER BY
  p.id ASC;

-- name: PreviousWebmasterTurnID :one
-- The webmaster turn before this one, or 0 when there is none.
SELECT
  CAST(COALESCE(MAX(id), 0) AS INTEGER)
FROM
  turns
WHERE
  kind = 'webmaster'
  AND id < ?;

-- name: ListPostsAfter :many
-- The posts an open page has not shown yet, newest first, the order the board
-- reads.
SELECT
  *
FROM
  posts
WHERE
  id > ?
ORDER BY
  id DESC
LIMIT
  ?;

-- name: CreateTurn :one
INSERT INTO
  turns (kind, author, body)
VALUES
  (?, ?, ?)
RETURNING
  *;

-- name: StartTurn :one
INSERT INTO
  turns (kind, author, body, status, notes)
VALUES
  (
    ?,
    ?,
    ?,
    'running',
    COALESCE(
      (
        SELECT
          notes
        FROM
          turns
        WHERE
          kind = 'webmaster'
          AND status != 'running'
        ORDER BY
          id DESC
        LIMIT
          1
      ),
      ''
    )
  )
RETURNING
  *;

-- name: FinishTurn :exec
UPDATE turns
SET
  said = ?,
  changes = ?,
  notes = ?,
  status = ?,
  error_text = ?,
  messages = ?,
  duration_ms = ?
WHERE
  id = ?;

-- name: FailTurn :exec
-- Close a turn that never reached its finish transaction without touching
-- notes or messages. The row already inherited the previous notes at start.
UPDATE turns
SET
  status = 'error',
  error_text = ?,
  messages = '[]'
WHERE
  id = ?;

-- name: FailRunningTurns :exec
-- A restart leaves the turn that was running behind. It never finished, so it
-- must not look like a turn the model can read back.
UPDATE turns
SET
  status = 'error',
  error_text = 'interrupted by a restart',
  messages = '[]'
WHERE
  kind = 'webmaster'
  AND status = 'running';

-- name: ListRoomLog :many
-- The conversation the webmaster reads back, after the compacted history and
-- before the turn it is answering. Only finished turns are included.
SELECT
  t.id,
  t.kind,
  t.author,
  t.body,
  t.said,
  t.changes,
  t.status,
  v.id AS version_id
FROM
  turns t
  LEFT JOIN site_versions v ON v.turn_id = t.id
WHERE
  t.id > ?
  AND t.id < ?
  AND NOT (
    t.kind = 'webmaster'
    AND t.status IN ('running', 'error')
  )
ORDER BY
  t.id DESC
LIMIT
  ?;

-- name: LatestNotes :one
SELECT
  notes
FROM
  turns
WHERE
  kind = 'webmaster'
  AND status != 'running'
ORDER BY
  id DESC
LIMIT
  1;

-- name: ListWebmasterTurns :many
SELECT
  t.*,
  v.id AS version_id
FROM
  turns t
  LEFT JOIN site_versions v ON v.turn_id = t.id
WHERE
  t.kind = 'webmaster'
ORDER BY
  t.id DESC
LIMIT
  ?;

-- name: GetWebmasterTurn :one
SELECT
  t.*,
  v.id AS version_id
FROM
  turns t
  LEFT JOIN site_versions v ON v.turn_id = t.id
WHERE
  t.kind = 'webmaster'
  AND t.id = ?;

-- name: CountWebmasterTurnsSince :one
SELECT
  count(*)
FROM
  turns
WHERE
  kind = 'webmaster'
  AND created_at >= ?;

-- name: CountChangedSinceHeal :one
SELECT
  count(*)
FROM
  turns
WHERE
  kind = 'webmaster'
  AND status = 'changed'
  AND id > COALESCE(
    (
      SELECT
        MAX(id)
      FROM
        turns
      WHERE
        kind = 'heal'
    ),
    0
  );
