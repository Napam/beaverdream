-- name: ListPosts :many
SELECT
  *
FROM
  posts
ORDER BY
  id ASC;

-- name: ListRecentPosts :many
SELECT
  *
FROM
  posts
ORDER BY
  id DESC
LIMIT
  ?;

-- name: CreatePost :one
INSERT INTO
  posts (author, body)
VALUES
  (?, ?)
RETURNING
  *;
