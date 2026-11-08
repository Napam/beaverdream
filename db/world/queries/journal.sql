-- name: AppendJournal :one
INSERT INTO
  journal (key, visitor, payload, match, rules)
VALUES
  (?, ?, ?, ?, ?)
RETURNING
  *;

-- name: ListJournalSince :many
SELECT
  *
FROM
  journal
WHERE
  key = ?
  AND id > ?
ORDER BY
  id ASC
LIMIT
  ?;

-- name: ListJournalAfter :many
-- Rows of every key after a cursor, for the live stream. One cursor covers
-- every key, because ids are global.
SELECT
  *
FROM
  journal
WHERE
  id > ?
ORDER BY
  id ASC
LIMIT
  ?;

-- name: MaxJournalID :one
SELECT
  CAST(COALESCE(MAX(id), 0) AS INTEGER)
FROM
  journal;

-- name: JournalLatest :one
SELECT
  CAST(COALESCE(MAX(match), 0) AS INTEGER) AS current_match,
  CAST(COALESCE(MAX(rules), 0) AS INTEGER) AS current_rules,
  CAST(COUNT(*) AS INTEGER) AS row_count
FROM
  journal
WHERE
  key = ?;

-- name: TrimJournal :exec
-- Keep the newest ? rows for a key. Older rows fall off, so one key cannot
-- grow without bound.
DELETE FROM journal
WHERE
  journal.key = ?1
  AND journal.id <= COALESCE(
    (
      SELECT
        j.id
      FROM
        journal j
      WHERE
        j.key = ?1
      ORDER BY
        j.id DESC
      LIMIT
        1 OFFSET ?2
    ),
    0
  );

-- name: JournalTail :many
-- The newest rows of one match under one rules number: the game the page is
-- drawing right now.
SELECT
  *
FROM
  journal
WHERE
  key = ?
  AND match = ?
  AND rules = ?
ORDER BY
  id DESC
LIMIT
  ?;

-- name: JournalRulesUsed :many
-- The highest rules number stored under each key, declared or not. A restore
-- or a dropped key can leave rows stamped above the current schema, and a
-- rules bump must clear them.
SELECT
  key,
  CAST(MAX(rules) AS INTEGER) AS rules
FROM
  journal
GROUP BY
  key;

-- name: DeleteJournal :exec
DELETE FROM journal;

-- name: CountJournal :one
SELECT
  CAST(COUNT(*) AS INTEGER)
FROM
  journal;

-- name: DeleteJournalKey :exec
DELETE FROM journal
WHERE
  key = ?;

-- name: JournalKeyStats :many
-- One row per key that has rows, declared or not. Every aggregate is bounded
-- by the trim, so the scan stays small.
SELECT
  j.key,
  CAST(COUNT(*) AS INTEGER) AS row_count,
  CAST(MIN(j.id) AS INTEGER) AS first_id,
  CAST(MAX(j.id) AS INTEGER) AS last_id,
  CAST(MIN(j.created_at) AS TEXT) AS first_at,
  CAST(MAX(j.created_at) AS TEXT) AS last_at,
  CAST(COUNT(DISTINCT j.visitor) AS INTEGER) AS visitors,
  CAST(SUM(LENGTH(j.payload)) AS INTEGER) AS payload_bytes,
  CAST(MAX(LENGTH(j.payload)) AS INTEGER) AS largest_payload,
  CAST(MIN(j.rules) AS INTEGER) AS min_rules,
  CAST(MAX(j.rules) AS INTEGER) AS max_rules,
  CAST(MAX(j.match) AS INTEGER) AS current_match,
  CAST(
    SUM(
      CASE
        WHEN j.match < (
          SELECT
            MAX(m.match)
          FROM
            journal m
          WHERE
            m.key = j.key
        ) THEN 1
        ELSE 0
      END
    ) AS INTEGER
  ) AS older_match_rows,
  CAST(
    SUM(
      CASE
        WHEN j.created_at >= CAST(sqlc.arg(since) AS TEXT) THEN 1
        ELSE 0
      END
    ) AS INTEGER
  ) AS recent_rows
FROM
  journal j
GROUP BY
  j.key
ORDER BY
  j.key;

-- name: JournalMatches :many
-- The matches a key holds, newest first, for the browser's match picker.
SELECT
  match,
  CAST(COUNT(*) AS INTEGER) AS row_count
FROM
  journal
WHERE
  key = ?
GROUP BY
  match
ORDER BY
  match DESC
LIMIT
  50;

-- name: JournalPage :many
-- A page of one key, newest first. A zero match, an empty visitor or a zero
-- before leaves that filter off.
SELECT
  *
FROM
  journal
WHERE
  key = sqlc.arg(key)
  AND (CAST(sqlc.arg(match) AS INTEGER) = 0 OR match = sqlc.arg(match))
  AND (CAST(sqlc.arg(visitor) AS TEXT) = '' OR visitor = sqlc.arg(visitor))
  AND (CAST(sqlc.arg(before) AS INTEGER) = 0 OR id < sqlc.arg(before))
ORDER BY
  id DESC
LIMIT
  sqlc.arg(page_size);
