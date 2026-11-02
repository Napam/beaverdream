-- name: ListSiteState :many
SELECT
  *
FROM
  site_state
ORDER BY
  key;

-- name: GetSiteState :one
SELECT
  *
FROM
  site_state
WHERE
  key = ?;

-- name: UpsertSiteState :exec
INSERT INTO
  site_state (key, value, updated_at)
VALUES
  (?, ?, strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
ON CONFLICT (key) DO UPDATE
SET
  value = excluded.value,
  updated_at = excluded.updated_at;

-- name: DeleteSiteState :exec
DELETE FROM site_state;
