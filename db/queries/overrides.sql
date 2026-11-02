-- name: ListUserOverrides :many
SELECT
  *
FROM
  user_overrides
WHERE
  version_id = ?
ORDER BY
  name_key;

-- name: GetUserOverride :one
SELECT
  *
FROM
  user_overrides
WHERE
  version_id = ?
  AND name_key = ?;

-- name: CreateUserOverride :exec
INSERT INTO
  user_overrides (version_id, name, name_key, fields)
VALUES
  (?, ?, ?, ?);
