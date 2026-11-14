-- +goose Up
-- The posts a changeset was started to answer. The worker coalesces a burst
-- into one turn, an admin turn can run between a post and its turn, and a
-- retry answers the same posts again, so the range of ids between two turns
-- proves nothing. This table is the record.
CREATE TABLE turn_triggers (
  turn_id INTEGER NOT NULL REFERENCES turns (id) ON DELETE CASCADE,
  post_id INTEGER NOT NULL,
  PRIMARY KEY (turn_id, post_id)
);

CREATE INDEX turn_triggers_post_id ON turn_triggers (post_id);

-- Turns that ran before this table existed get the posts the old rule gave
-- them: the posts between the turn and the changeset before it, the newest 10
-- like the worker's cap. Admin instructions carry their text in body and are
-- retried from it, so they get none.
INSERT INTO
  turn_triggers (turn_id, post_id)
SELECT
  turn_id,
  post_id
FROM
  (
    SELECT
      t.id AS turn_id,
      p.id AS post_id,
      ROW_NUMBER() OVER (
        PARTITION BY
          t.id
        ORDER BY
          p.id DESC
      ) AS newest
    FROM
      turns t
      JOIN turns p ON p.kind = 'post'
      AND p.id < t.id
      AND p.id > COALESCE(
        (
          SELECT
            MAX(prev.id)
          FROM
            turns prev
          WHERE
            prev.kind = 'changeset'
            AND prev.id < t.id
        ),
        0
      )
    WHERE
      t.kind = 'changeset'
      AND t.body = ''
  )
WHERE
  newest <= 10;

-- A retry of a post turn has no posts between it and the turn before it, and
-- the old rule walked back to the turn it copied. Give such a turn the
-- triggers of the nearest earlier turn that has them, so a failed retry from
-- before this table can still be retried. The select is read before the insert
-- lands, so a chain of retries all copy the original.
INSERT INTO
  turn_triggers (turn_id, post_id)
SELECT
  t.id,
  tt.post_id
FROM
  turns t
  JOIN turn_triggers tt ON tt.turn_id = (
    SELECT
      MAX(earlier.turn_id)
    FROM
      turn_triggers earlier
    WHERE
      earlier.turn_id < t.id
  )
WHERE
  t.kind = 'changeset'
  AND t.body = ''
  AND NOT EXISTS (
    SELECT
      1
    FROM
      turn_triggers own
    WHERE
      own.turn_id = t.id
  );

-- +goose Down
DROP TABLE IF EXISTS turn_triggers;
