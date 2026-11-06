-- +goose Up
-- The webmaster can answer on the board. A finished turn with something to
-- say writes one reply row (kind = 'reply', author 'webmaster') in its finish
-- transaction, so the reply gets the newest id and reaches open pages like
-- any new post. The posts view now lists posts and replies, with kind so the
-- board can mark replies by kind and never by author name. said, notes and
-- changes still never reach the board: a reply row holds only the reply.
DROP VIEW posts;

CREATE VIEW posts AS
SELECT
  id,
  author,
  body,
  created_at,
  kind
FROM
  turns
WHERE
  kind IN ('post', 'reply');

-- +goose Down
DROP VIEW posts;

DELETE FROM turns
WHERE
  kind = 'reply';

CREATE VIEW posts AS
SELECT
  id,
  author,
  body,
  created_at
FROM
  turns
WHERE
  kind = 'post';
