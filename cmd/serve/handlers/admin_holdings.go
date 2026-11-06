package handlers

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"time"

	"github.com/Napam/beaverdream/db/generated/sqlc"
	"github.com/Napam/beaverdream/lib/webmaster"
	"github.com/Napam/beaverdream/web/pages"
	"github.com/go-chi/chi/v5"
)

// adminStateOpsShown is how many of the picked visitor's op rows are listed.
const adminStateOpsShown = 50

// adminHoldings is one card per visitor counter that is declared now or still
// holds something.
func (h *Handlers) adminHoldings(ctx context.Context, site sqlc.SiteVersion) []pages.HoldingView {
	schema := parseStateSchema(site.StateSchema)
	byKey := make(map[string]pages.HoldingView)
	for _, key := range visitorKeys(schema) {
		byKey[key] = pages.HoldingView{Key: key, Label: schema[key].Label, Declared: true}
	}
	for _, total := range h.holdingTotals(ctx) {
		view := byKey[total.Key]
		view.Key, view.Total, view.Holders = total.Key, total.Total, total.Holders
		byKey[total.Key] = view
	}
	since := time.Now().UTC().Add(-time.Hour).Format(sqliteTime)
	gained, err := h.queries.GainedByKeySince(ctx, since)
	if err != nil {
		h.logRead("Admin could not read the hour's gains", err)
	}
	for _, row := range gained {
		if view, ok := byKey[row.Key]; ok {
			view.GainedHour = row.Gained
			byKey[row.Key] = view
		}
	}
	holders, err := h.topHolders(ctx)
	if err != nil {
		h.logRead("Admin could not read the top holders", err)
	}
	for key, rows := range holders {
		view, ok := byKey[key]
		if !ok {
			continue
		}
		for _, row := range rows {
			view.Top = append(view.Top, pages.HolderView{
				Visitor: row.visitor, Name: row.name, Amount: row.amount,
			})
		}
		byKey[key] = view
	}

	views := make([]pages.HoldingView, 0, len(byKey))
	for _, view := range byKey {
		views = append(views, view)
	}
	sort.Slice(views, func(i, j int) bool {
		if views[i].Declared != views[j].Declared {
			return views[i].Declared
		}
		return views[i].Key < views[j].Key
	})
	return views
}

// adminStateOps is the picked visitor's recent op rows, newest first.
func (h *Handlers) adminStateOps(ctx context.Context, panel pages.Panel) []pages.StateOpView {
	if panel.Visitor == "" {
		return nil
	}
	rows, err := h.queries.ListVisitorOps(ctx, sqlc.ListVisitorOpsParams{
		Visitor: panel.Visitor,
		Limit:   adminStateOpsShown,
	})
	if err != nil {
		h.logRead("Admin could not read a visitor's state ops", err)
		return nil
	}
	ops := make([]pages.StateOpView, 0, len(rows))
	for _, row := range rows {
		ops = append(ops, pages.StateOpView{
			OpID: row.OpID, Op: row.Op, Source: row.Source, Key: row.Key,
			Delta: row.Delta, Name: row.Name, At: row.CreatedAt,
		})
	}
	return ops
}

// ClearHoldings deletes what every visitor holds of one key, and its op
// rows, and records the clear as an event turn in the same transaction: the
// world the webmaster built on changed under it. It takes stateMu like an
// op, never mu, and never calls the model.
func (h *Handlers) ClearHoldings(w http.ResponseWriter, r *http.Request) {
	if !h.requireAdmin(w, r) {
		return
	}
	key := chi.URLParam(r, "key")
	if !journalKeyName.MatchString(key) {
		http.NotFound(w, r)
		return
	}
	message, err := h.clearHoldings(r.Context(), key)
	if err != nil {
		h.logger.Error(
			"Failed to clear visitor holdings",
			slog.String("key", key),
			slog.Any("error", err),
		)
		message = "clear failed: " + err.Error()
	}
	h.adminDone(w, r, journalPanel(r.URL.Query()), message)
}

func (h *Handlers) clearHoldings(ctx context.Context, key string) (string, error) {
	h.stateMu.Lock()
	defer h.stateMu.Unlock()

	// Holders are visitors with more than 0, the same count the admin card
	// shows. Rows at 0 go too, without an event of their own.
	var holders int64
	err := h.inTx(ctx, func(q *sqlc.Queries) error {
		n, err := q.CountHolders(ctx, key)
		if err != nil {
			return fmt.Errorf("count holders: %w", err)
		}
		holders = n
		if _, err := q.DeleteHoldingsForKey(ctx, key); err != nil {
			return fmt.Errorf("delete holdings: %w", err)
		}
		if n == 0 {
			return nil
		}
		if err := q.DeleteStateOpsForKey(ctx, key); err != nil {
			return fmt.Errorf("delete ops: %w", err)
		}
		_, err = q.CreateTurn(ctx, sqlc.CreateTurnParams{
			Kind:   webmaster.TurnState,
			Author: "admin",
			Body: fmt.Sprintf(
				"the admin cleared visitor key %q (%s); everyone holds 0 now",
				key, plural(n, "holder"),
			),
		})
		if err != nil {
			return fmt.Errorf("record event turn: %w", err)
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	h.holdingsChanged()
	if holders == 0 {
		return fmt.Sprintf("nobody holds any %s", key), nil
	}
	h.hub.publish(topicState | topicTurns)
	return fmt.Sprintf("cleared %s (%s)", key, plural(holders, "holder")), nil
}
