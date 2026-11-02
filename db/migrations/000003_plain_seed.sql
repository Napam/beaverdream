-- +goose Up
-- The seed is a plain light board: a title, a simple post form, and the
-- thread. The model takes it from here.
UPDATE site_versions
SET
  note = 'welcome. be nice to each other.',
  header = '<h1>beaverdream</h1><p class="tagline">a small message board</p>',
  board = '<main>' ||
    '<form class="postbox" method="post" action="/posts">' ||
    '<p class="postbox-title">post a message</p>' ||
    '<textarea name="body" required maxlength="2000" rows="3" placeholder="say something" aria-label="message"></textarea>' ||
    '<div class="postbox-actions"><span>posting as {{name}}</span>' ||
    '<button type="submit">post</button></div>' ||
    '</form>' ||
    '<div class="thread">{{posts}}</div>' ||
    '</main>',
  footer = '<footer class="colophon">beaverdream</footer>'
WHERE
  id = (
    SELECT
      MIN(id)
    FROM
      site_versions
  );

-- +goose Down
-- Nothing to undo: this is the starting design.
