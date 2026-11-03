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
