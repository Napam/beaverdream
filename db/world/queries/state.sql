-- name: ListSharedState :many
SELECT
  *
FROM
  shared_state
ORDER BY
  key;

-- name: GetSharedState :one
SELECT
  *
FROM
  shared_state
WHERE
  key = ?;

-- name: UpsertSharedState :exec
INSERT INTO
  shared_state (key, value, updated_at)
VALUES
  (?, ?, strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
ON CONFLICT (key) DO UPDATE
SET
  value = excluded.value,
  updated_at = excluded.updated_at;

-- name: DeleteSharedState :exec
DELETE FROM shared_state;
