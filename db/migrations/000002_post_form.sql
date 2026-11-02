-- +goose Up
-- The post form used to be code-owned. It is now part of the page the model
-- writes, so the seed version needs a working one.
UPDATE site_versions
SET
  footer = '<footer class="colophon">built by a machine that is easily persuaded</footer>' ||
    '<form method="post" action="/posts">' ||
    '<input name="author" maxlength="32" placeholder="name" autocomplete="off">' ||
    '<textarea name="body" required maxlength="2000" rows="3" placeholder="say something and the board changes"></textarea>' ||
    '<button type="submit">post</button>' ||
    '</form>'
WHERE
  id = (
    SELECT
      MIN(id)
    FROM
      site_versions
  );

-- +goose Down
-- Nothing to undo: the form belongs in the page now.
