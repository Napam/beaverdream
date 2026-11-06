-- name: GetHolding :one
SELECT
  amount
FROM
  state_holdings
WHERE
  visitor = ?
  AND key = ?;

-- name: UpsertHolding :exec
INSERT INTO
  state_holdings (visitor, key, amount, name)
VALUES
  (?, ?, ?, ?)
ON CONFLICT (visitor, key) DO UPDATE
SET
  amount = excluded.amount,
  name = excluded.name;

-- name: GainedSince :one
SELECT
  CAST(COALESCE(SUM(delta), 0) AS INTEGER) AS gained
FROM
  state_ops
WHERE
  visitor = ?
  AND key = ?
  AND delta > 0
  AND created_at > ?;

-- name: InsertStateOp :one
INSERT INTO
  state_ops (op_id, visitor, name, op, source, key, delta)
VALUES
  (?, ?, ?, ?, ?, ?, ?)
RETURNING
  id;

-- name: SetStateOpID :exec
UPDATE state_ops
SET
  op_id = ?
WHERE
  id = ?;

-- name: PruneStateOps :exec
DELETE FROM state_ops
WHERE
  created_at < ?;

-- name: ListVisitorHoldings :many
SELECT
  key,
  amount
FROM
  state_holdings
WHERE
  visitor = ?
ORDER BY
  key;

-- name: HoldingTotals :many
SELECT
  key,
  CAST(SUM(amount) AS INTEGER) AS total,
  COUNT(*) AS holders
FROM
  state_holdings
WHERE
  amount > 0
GROUP BY
  key
ORDER BY
  key;

-- name: GainedByKeySince :many
SELECT
  key,
  CAST(SUM(delta) AS INTEGER) AS gained
FROM
  state_ops
WHERE
  delta > 0
  AND created_at > ?
GROUP BY
  key;

-- name: ListVisitorOps :many
SELECT
  *
FROM
  state_ops
WHERE
  visitor = ?
ORDER BY
  id DESC
LIMIT
  ?;

-- name: CountHolders :one
SELECT
  COUNT(*)
FROM
  state_holdings
WHERE
  key = ?
  AND amount > 0;

-- name: DeleteHoldingsForKey :execrows
DELETE FROM state_holdings
WHERE
  key = ?;

-- name: DeleteStateOpsForKey :exec
DELETE FROM state_ops
WHERE
  key = ?;
