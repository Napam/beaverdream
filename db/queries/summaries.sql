-- name: GetLatestSummary :one
SELECT
  *
FROM
  room_summaries
ORDER BY
  id DESC
LIMIT
  1;

-- name: SummaryStats :one
-- Totals for the uncompacted stretch. Compaction decides on these, not on the
-- capped chunk, or a large backlog would never reach the threshold.
SELECT
  count(*) AS turns,
  COALESCE(
    CAST(
      SUM(
        MIN(length(t.body), 280) + length(t.said) + length(t.changes) + 16
      ) AS INTEGER
    ),
    0
  ) AS chars
FROM
  turns t
WHERE
  t.id > ?
  AND t.id <= ?
  AND NOT (
    t.kind = 'webmaster'
    AND t.status IN ('running', 'error')
  );

-- name: ListTurnsForSummary :many
-- The oldest turns that the latest summary does not cover yet. Ascending so a
-- backlog is folded in order, one chunk per call.
SELECT
  t.id,
  t.kind,
  t.author,
  t.body,
  t.said,
  t.changes,
  t.status
FROM
  turns t
WHERE
  t.id > ?
  AND t.id <= ?
  AND NOT (
    t.kind = 'webmaster'
    AND t.status IN ('running', 'error')
  )
ORDER BY
  t.id ASC
LIMIT
  ?;

-- name: CreateSummary :one
INSERT INTO
  room_summaries (through_turn_id, text)
VALUES
  (?, ?)
RETURNING
  *;
