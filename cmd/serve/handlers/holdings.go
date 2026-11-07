package handlers

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/Napam/beaverdream/db/generated/sqlc"
	"github.com/Napam/beaverdream/lib/webmaster"
)

const (
	// topHoldingsPerKey is how many holders each $tops list carries.
	topHoldingsPerKey = 10
	// stateOpsKept is how long op rows stay: the hour window and some audit.
	stateOpsKept = 24 * time.Hour
	// sqliteTime matches strftime('%Y-%m-%dT%H:%M:%fZ'), so the strings
	// compare in time order.
	sqliteTime = "2006-01-02T15:04:05.000Z"
)

// The refusals a visitor op can answer with. The page shows them, so they are
// short words, not sentences.
const (
	refusedShort = "short" // not enough to pay
	refusedFull  = "full"  // the holding would pass its max
	refusedCap   = "cap"   // the hour budget is used up
	refusedSlow  = "slow"  // the cooldown
	refusedGone  = "gone"  // not declared on the current page
	refusedBad   = "bad"   // a missing or out-of-range amount
)

var refusalStatus = map[string]int{
	refusedShort: http.StatusConflict,
	refusedFull:  http.StatusConflict,
	refusedCap:   http.StatusTooManyRequests,
	refusedSlow:  http.StatusTooManyRequests,
	refusedGone:  http.StatusNotFound,
	refusedBad:   http.StatusBadRequest,
}

// refusal is an op the rules turned down. Nothing was written.
type refusal struct{ reason string }

func (r refusal) Error() string { return r.reason }

// topHolding is one row of a $tops list. ID is the public visitor id, the
// same value the page has as bd.visitor.
type topHolding struct {
	Name   string `json:"name"`
	ID     string `json:"id"`
	Amount int64  `json:"amount"`
}

// stateChange is one key an op moves, by a signed amount.
type stateChange struct {
	key   string
	delta int64
}

// visitorKeys lists the declared visitor counters in name order.
func visitorKeys(schema map[string]webmaster.StateKey) []string {
	var keys []string
	for key, def := range schema {
		if def.Visitor() {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	return keys
}

// visitorHoldings is $mine: one visitor's count for every declared visitor
// key, 0 when they hold none. A failed read is an error, never a page of
// zeros.
func visitorHoldings(
	ctx context.Context,
	q *sqlc.Queries,
	schema map[string]webmaster.StateKey,
	visitor string,
) (map[string]int64, error) {
	mine := make(map[string]int64)
	for _, key := range visitorKeys(schema) {
		mine[key] = 0
	}
	if visitor == "" || len(mine) == 0 {
		return mine, nil
	}
	rows, err := q.ListVisitorHoldings(ctx, visitor)
	if err != nil {
		return nil, fmt.Errorf("load visitor holdings: %w", err)
	}
	for _, row := range rows {
		if _, ok := mine[row.Key]; ok {
			mine[row.Key] = row.Amount
		}
	}
	return mine, nil
}

// holderRow is one top holder with the raw visitor id, which only the admin
// sees.
type holderRow struct {
	visitor string
	name    string
	amount  int64
}

// holdersCache keeps the last top-holder read. Every op publishes, and every
// open stream would otherwise run the window query again for the same rows.
type holdersCache struct {
	mu   sync.Mutex
	gen  int64
	rows map[string][]holderRow
}

// holdingsChanged marks the cached top lists stale. Call it after the write
// commits, so a read that raced the write is thrown away.
func (h *Handlers) holdingsChanged() { h.holdingsGen.Add(1) }

// topHolders is the biggest holders of every key that has any, declared or
// not. The window function is plain SQL because sqlc cannot see a subquery's
// columns.
func (h *Handlers) topHolders(ctx context.Context) (map[string][]holderRow, error) {
	gen := h.holdingsGen.Load()
	h.holders.mu.Lock()
	defer h.holders.mu.Unlock()
	if h.holders.rows != nil && h.holders.gen == gen {
		return h.holders.rows, nil
	}

	rows, err := h.db.QueryContext(ctx, `
SELECT visitor, key, amount, name FROM (
  SELECT visitor, key, amount, name,
    ROW_NUMBER() OVER (PARTITION BY key ORDER BY amount DESC, visitor) AS place
  FROM state_holdings WHERE amount > 0
) WHERE place <= ? ORDER BY key, place`, topHoldingsPerKey)
	if err != nil {
		return nil, fmt.Errorf("load the top holders: %w", err)
	}
	defer func() { _ = rows.Close() }()
	tops := make(map[string][]holderRow)
	for rows.Next() {
		var key string
		var row holderRow
		if err := rows.Scan(&row.visitor, &key, &row.amount, &row.name); err != nil {
			return nil, fmt.Errorf("read the top holders: %w", err)
		}
		tops[key] = append(tops[key], row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read the top holders: %w", err)
	}
	h.holders.gen, h.holders.rows = gen, tops
	return tops, nil
}

// topHoldings is $tops: the biggest holders of every declared visitor key,
// each with the public id the page knows as bd.visitor.
func (h *Handlers) topHoldings(
	ctx context.Context,
	schema map[string]webmaster.StateKey,
) (map[string][]topHolding, error) {
	tops := make(map[string][]topHolding)
	keys := visitorKeys(schema)
	if len(keys) == 0 {
		return tops, nil
	}
	holders, err := h.topHolders(ctx)
	if err != nil {
		return nil, err
	}
	for _, key := range keys {
		tops[key] = []topHolding{}
		for _, row := range holders[key] {
			tops[key] = append(tops[key], topHolding{
				Name:   row.name,
				ID:     publicVisitor(row.visitor),
				Amount: row.amount,
			})
		}
	}
	return tops, nil
}

// holdingTotals is what the prompt and the admin get about every key that
// has holders, declared or not.
func (h *Handlers) holdingTotals(ctx context.Context) []webmaster.HoldingTotal {
	rows, err := h.queries.HoldingTotals(ctx)
	if err != nil {
		h.logRead("Could not load holding totals", err)
		return nil
	}
	totals := make([]webmaster.HoldingTotal, 0, len(rows))
	for _, row := range rows {
		totals = append(totals, webmaster.HoldingTotal{
			Key:     row.Key,
			Total:   row.Total,
			Holders: row.Holders,
		})
	}
	return totals
}

// visitorOp parses the amount for add or spend, or takes the recipe for a
// trade, and applies it. It answers the way SetState does: 204 to Datastar,
// JSON to a fetch, a redirect to a plain form.
func (h *Handlers) visitorOp(
	w http.ResponseWriter,
	r *http.Request,
	key string,
	def webmaster.StateKey,
	schema map[string]webmaster.StateKey,
	visitor, author string,
) {
	op := "trade"
	var changes []stateChange
	if def.Op == "recipe" {
		for _, item := range sortedItems(def.Cost) {
			changes = append(changes, stateChange{item, -def.Cost[item]})
		}
		for _, item := range sortedItems(def.Gain) {
			changes = append(changes, stateChange{item, def.Gain[item]})
		}
	} else {
		add, spend := r.FormValue("add"), r.FormValue("spend")
		raw := add
		op = "add"
		if spend != "" {
			raw, op = spend, "spend"
		}
		amount, err := strconv.ParseInt(raw, 10, 64)
		if (add == "") == (spend == "") || err != nil ||
			amount < 1 || amount > webmaster.MaxVisitorAmount {
			h.answerVisitorOp(w, r, nil, refusedBad)
			return
		}
		if op == "spend" {
			amount = -amount
		}
		changes = []stateChange{{key, amount}}
	}

	mine, err := h.applyVisitorOp(r.Context(), schema, visitor, author, op, key, changes)
	var refused refusal
	switch {
	case errors.As(err, &refused):
		h.answerVisitorOp(w, r, nil, refused.reason)
		return
	case err != nil:
		h.logger.Error("Failed to apply a visitor op", slog.Any("error", err))
		writeStateError(w, http.StatusInternalServerError, "could not store that")
		return
	}
	h.hub.publish(topicState)
	h.answerVisitorOp(w, r, mine, "")
}

func sortedItems(items map[string]int64) []string {
	names := make([]string, 0, len(items))
	for name := range items {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// answerVisitorOp writes the outcome. A refusal keeps its real status: a
// Datastar request only patches on 200, so the body never lands in a signal.
func (h *Handlers) answerVisitorOp(
	w http.ResponseWriter,
	r *http.Request,
	mine map[string]int64,
	reason string,
) {
	if wantsHTML(r) {
		http.Redirect(w, r, h.boardPath(), http.StatusSeeOther)
		return
	}
	if reason == "" && isDatastar(r) {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	if reason != "" {
		w.WriteHeader(refusalStatus[reason])
		writeJSON(w, map[string]any{"ok": false, "reason": reason, "error": reason})
		return
	}
	writeJSON(w, map[string]any{"ok": true, "mine": mine})
}

// applyVisitorOp moves every change in one transaction under stateMu. Costs
// must be held, gains must stay under the key's max and its hour budget; any
// miss rolls back the whole op and stores nothing. It returns the visitor's
// holdings after the op.
func (h *Handlers) applyVisitorOp(
	ctx context.Context,
	schema map[string]webmaster.StateKey,
	visitor, author, op, source string,
	changes []stateChange,
) (map[string]int64, error) {
	h.stateMu.Lock()
	defer h.stateMu.Unlock()

	tx, err := h.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	q := h.queries.WithTx(tx)

	now := time.Now().UTC()
	hourAgo := now.Add(-time.Hour).Format(sqliteTime)
	amounts := make([]int64, len(changes))
	for i, change := range changes {
		def := schema[change.key]
		if !def.Visitor() {
			return nil, refusal{refusedGone}
		}
		held, err := q.GetHolding(ctx, sqlc.GetHoldingParams{Visitor: visitor, Key: change.key})
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("read holding: %w", err)
		}
		next := held + change.delta
		if next < 0 {
			return nil, refusal{refusedShort}
		}
		if change.delta > 0 {
			if next > def.HoldingCap() {
				return nil, refusal{refusedFull}
			}
			gained, err := q.GainedSince(ctx, sqlc.GainedSinceParams{
				Visitor:   visitor,
				Key:       change.key,
				CreatedAt: hourAgo,
			})
			if err != nil {
				return nil, fmt.Errorf("read the hour window: %w", err)
			}
			if gained+change.delta > def.HourCap() {
				return nil, refusal{refusedCap}
			}
		}
		amounts[i] = next
	}

	// The rows of one op share op_id: the id of its first row, which
	// AUTOINCREMENT never hands out twice, even after a prune.
	var opID int64
	for i, change := range changes {
		if err := q.UpsertHolding(ctx, sqlc.UpsertHoldingParams{
			Visitor: visitor,
			Key:     change.key,
			Amount:  amounts[i],
			Name:    author,
		}); err != nil {
			return nil, fmt.Errorf("write holding: %w", err)
		}
		id, err := q.InsertStateOp(ctx, sqlc.InsertStateOpParams{
			OpID:    opID,
			Visitor: visitor,
			Name:    author,
			Op:      op,
			Source:  source,
			Key:     change.key,
			Delta:   change.delta,
		})
		if err != nil {
			return nil, fmt.Errorf("write op: %w", err)
		}
		if i == 0 {
			opID = id
			if err := q.SetStateOpID(ctx, sqlc.SetStateOpIDParams{OpID: id, ID: id}); err != nil {
				return nil, fmt.Errorf("write op id: %w", err)
			}
		}
	}
	if err := q.PruneStateOps(ctx, now.Add(-stateOpsKept).Format(sqliteTime)); err != nil {
		return nil, fmt.Errorf("prune ops: %w", err)
	}
	mine, err := visitorHoldings(ctx, q, schema, visitor)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}
	h.holdingsChanged()
	return mine, nil
}
