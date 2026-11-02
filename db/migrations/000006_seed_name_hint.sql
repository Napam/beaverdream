-- +goose Up
-- The seed form now shows the visitor's name. Databases that applied the older
-- seed need the same copy.
UPDATE site_versions
SET
  board = replace(
    board,
    '<span>no account needed</span>',
    '<span>posting as {{name}}</span>'
  )
WHERE
  id = (
    SELECT
      MIN(id)
    FROM
      site_versions
  );

-- +goose Down
UPDATE site_versions
SET
  board = replace(
    board,
    '<span>posting as {{name}}</span>',
    '<span>no account needed</span>'
  )
WHERE
  id = (
    SELECT
      MIN(id)
    FROM
      site_versions
  );
