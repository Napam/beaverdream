-- name: CreateMutationRun :one
INSERT INTO
  mutation_runs (
    kind,
    trigger_post_id,
    instruction,
    reason,
    status,
    error_text,
    messages,
    version_id,
    duration_ms
  )
VALUES
  (?, ?, ?, ?, ?, ?, ?, ?, ?)
RETURNING
  *;

-- name: ListMutationRuns :many
SELECT
  *
FROM
  mutation_runs
ORDER BY
  id DESC
LIMIT
  ?;

-- name: CountMutationsSince :one
SELECT
  count(*)
FROM
  mutation_runs
WHERE
  created_at >= ?;
