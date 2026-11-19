-- name: BumpVersionStat :exec
INSERT INTO
  version_stats (version_id, kind, key, n)
VALUES
  (?, ?, ?, 1)
ON CONFLICT (version_id, kind, key) DO UPDATE
SET
  n = n + 1;

-- name: AddVersionVisitor :exec
INSERT
OR IGNORE INTO version_visitors (version_id, visitor)
VALUES
  (?, ?);

-- name: ListVersionStats :many
SELECT
  kind,
  key,
  CAST(n AS INTEGER) AS n
FROM
  version_stats
WHERE
  version_id = ?
ORDER BY
  kind,
  n DESC,
  key;

-- name: CountVersionVisitors :one
SELECT
  CAST(COUNT(*) AS INTEGER)
FROM
  version_visitors
WHERE
  version_id = ?;

-- name: PruneVersionStats :exec
-- Keep the newest ? versions. Older versions keep their render and their
-- history; only the counters go, because the model reads the count of the
-- page it just shipped.
DELETE FROM version_stats
WHERE
  version_id NOT IN (
    SELECT
      id
    FROM
      site_versions
    ORDER BY
      id DESC
    LIMIT
      ?
  );

-- name: PruneVersionVisitors :exec
DELETE FROM version_visitors
WHERE
  version_id NOT IN (
    SELECT
      id
    FROM
      site_versions
    ORDER BY
      id DESC
    LIMIT
      ?
  );
