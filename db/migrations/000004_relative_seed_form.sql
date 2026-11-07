-- +goose Up
-- The board now lives under /w/{slug}/, so the seed form posts to a relative
-- path. The renderer never rewrites model markup, and 000001 is frozen, so the
-- stored seed is moved forward once here. Only the seed row is touched:
-- history produced by the model stays as it was written.
UPDATE site_versions
SET
  html = replace(
    replace(html, 'action="/posts"', 'action="posts"'),
    '@post(''/posts''',
    '@post(''posts'''
  )
WHERE
  turn_id IS NULL
  AND html LIKE '%action="/posts"%';

-- +goose Down
UPDATE site_versions
SET
  html = replace(
    replace(html, 'action="posts"', 'action="/posts"'),
    '@post(''posts''',
    '@post(''/posts'''
  )
WHERE
  turn_id IS NULL
  AND html LIKE '%action="posts"%';
