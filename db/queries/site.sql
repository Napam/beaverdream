-- name: CurrentVersionID :one
SELECT
  MAX(id)
FROM
  site_versions;

-- name: CurrentSite :one
SELECT
  *
FROM
  site_versions
ORDER BY
  id DESC
LIMIT
  1;

-- name: SeedSiteVersion :one
SELECT
  *
FROM
  site_versions
ORDER BY
  id ASC
LIMIT
  1;

-- name: GetSiteVersion :one
SELECT
  *
FROM
  site_versions
WHERE
  id = ?;

-- name: CreateSiteVersion :one
INSERT INTO
  site_versions (css, note, header, board, footer, state_schema, journal_schema, turn_id, reason)
VALUES
  (?, ?, ?, ?, ?, ?, ?, ?, ?)
RETURNING
  *;

-- name: ListSiteVersions :many
SELECT
  *
FROM
  site_versions
ORDER BY
  id DESC
LIMIT
  ?;
