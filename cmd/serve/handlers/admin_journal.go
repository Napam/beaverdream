package handlers

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"time"

	"github.com/Napam/beaverdream/db/generated/sqlc"
	"github.com/Napam/beaverdream/lib/webmaster"
	"github.com/Napam/beaverdream/web/pages"
	"github.com/go-chi/chi/v5"
)

const (
	// adminJournalPage is how many rows the browser shows at a time.
	adminJournalPage = 50
	// adminJournalEvery spaces the journal page's live patches. Every append
	// publishes, and the stats scan every key, so a busy feed is shown about
	// once a second.
	adminJournalEvery = time.Second
)

// journalKeyName is what a key can be. Declared keys already follow it; the
// check keeps a stray row from putting odd text in a path or a script.
var journalKeyName = regexp.MustCompile(`^[a-z0-9_-]{1,64}$`)

// journalPanel reads the journal page's selection from a query: the key being
// browsed, a match, a visitor, and the row id to page back from.
func journalPanel(q url.Values) pages.Panel {
	panel := pages.Panel{Page: pages.PageJournal}
	if key := q.Get("feed"); journalKeyName.MatchString(key) {
		panel.Feed = key
	}
	panel.Match, _ = strconv.ParseInt(q.Get("match"), 10, 64)
	panel.Before, _ = strconv.ParseInt(q.Get("before"), 10, 64)
	if visitor := q.Get("visitor"); validVisitorID(visitor) {
		panel.Visitor = visitor
	}
	panel.Match = max(panel.Match, 0)
	panel.Before = max(panel.Before, 0)
	return panel
}

// AdminJournal is the journal page.
func (h *Handlers) AdminJournal(w http.ResponseWriter, r *http.Request) {
	h.servePanel(w, r, journalPanel(r.URL.Query()))
}

// ClearJournal deletes every row of one key. Open pages hold those rows in
// memory, so their streams are told to read again, and the room log records
// the clear: the world the webmaster built on changed under it. The write
// takes stateMu, like an append, so a row cannot land after the delete under
// the old match. It never calls the model and never takes mu.
func (h *Handlers) ClearJournal(w http.ResponseWriter, r *http.Request) {
	if !h.requireAdmin(w, r) {
		return
	}
	key := chi.URLParam(r, "key")
	panel := journalPanel(r.URL.Query())
	if !journalKeyName.MatchString(key) {
		http.NotFound(w, r)
		return
	}

	message, err := h.clearJournal(r.Context(), key)
	if err != nil {
		h.logger.Error(
			"Failed to clear the journal",
			slog.String("key", key),
			slog.Any("error", err),
		)
		message = "clear failed: " + err.Error()
	}
	// The cleared key has nothing left to page through.
	if panel.Feed == key {
		panel.Match, panel.Before, panel.Visitor = 0, 0, ""
	}
	h.adminDone(w, r, panel, message)
}

// clearJournal deletes one key's rows and records the event in one
// transaction. It returns what to tell the admin.
func (h *Handlers) clearJournal(ctx context.Context, key string) (string, error) {
	h.stateMu.Lock()
	defer h.stateMu.Unlock()

	var rows, match int64
	err := h.inTx(ctx, func(q *sqlc.Queries) error {
		latest, err := q.JournalLatest(ctx, key)
		if err != nil {
			return fmt.Errorf("read the key: %w", err)
		}
		rows, match = latest.RowCount, latest.CurrentMatch
		if rows == 0 {
			return nil
		}
		if err := q.DeleteJournalKey(ctx, key); err != nil {
			return fmt.Errorf("delete rows: %w", err)
		}
		_, err = q.CreateTurn(ctx, sqlc.CreateTurnParams{
			Kind:   webmaster.TurnJournal,
			Author: "admin",
			Body: fmt.Sprintf(
				"the admin cleared journal %q (%d rows, was match %d); it starts empty at match 1",
				key, rows, match,
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
	if rows == 0 {
		return fmt.Sprintf("%s has no rows", key), nil
	}
	h.journalClears.Add(1)
	h.hub.publish(topicJournal | topicTurns)
	return fmt.Sprintf("cleared %s (%s)", key, plural(rows, "row")), nil
}

func plural(n int64, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return strconv.FormatInt(n, 10) + " " + word + "s"
}

// adminJournal builds the journal page: one card per key that has rows or is
// declared, and the row browser for the picked key.
func (h *Handlers) adminJournal(
	ctx context.Context,
	site sqlc.SiteVersion,
	panel pages.Panel,
) ([]pages.JournalKeyView, *pages.JournalBrowse) {
	schema := webmaster.ParseJournalSchema(site.JournalSchema)
	since := time.Now().UTC().Add(-time.Hour).Format("2006-01-02T15:04:05.000Z")
	stats, err := h.queries.JournalKeyStats(ctx, since)
	if err != nil {
		h.logRead("Admin could not read journal stats", err)
	}

	byKey := make(map[string]pages.JournalKeyView, len(stats)+len(schema))
	for key, def := range schema {
		byKey[key] = pages.JournalKeyView{
			Key: key, Declared: true, Max: def.Max, Rules: def.Rules, Match: 1,
		}
	}
	for _, s := range stats {
		if !journalKeyName.MatchString(s.Key) {
			continue
		}
		view := byKey[s.Key]
		view.Key = s.Key
		view.Rows = int(s.RowCount)
		view.Match = max(int(s.CurrentMatch), 1)
		view.FirstID, view.LastID = s.FirstID, s.LastID
		view.FirstAt, view.LastAt = s.FirstAt, s.LastAt
		view.Visitors = int(s.Visitors)
		view.PayloadBytes, view.LargestPayload = s.PayloadBytes, s.LargestPayload
		view.MinRules, view.MaxRules = int(s.MinRules), int(s.MaxRules)
		view.OlderMatchRows = int(s.OlderMatchRows)
		view.RecentRows = int(s.RecentRows)
		byKey[s.Key] = view
	}
	keys := make([]pages.JournalKeyView, 0, len(byKey))
	for _, view := range byKey {
		keys = append(keys, view)
	}
	pages.SortJournalKeys(keys)

	// With nothing picked, the first key that has rows is browsed.
	if panel.Feed == "" {
		for _, view := range keys {
			if view.Rows > 0 {
				panel.Feed = view.Key
				break
			}
		}
	}
	var browse *pages.JournalBrowse
	if _, ok := byKey[panel.Feed]; ok && panel.Feed != "" {
		browse = h.adminJournalBrowse(ctx, panel)
	}
	return keys, browse
}

// adminJournalBrowse reads one page of the picked key, newest first, under
// the picked filters.
func (h *Handlers) adminJournalBrowse(
	ctx context.Context,
	panel pages.Panel,
) *pages.JournalBrowse {
	browse := &pages.JournalBrowse{
		Key: panel.Feed, Match: panel.Match, Visitor: panel.Visitor, Before: panel.Before,
	}
	matches, err := h.queries.JournalMatches(ctx, panel.Feed)
	if err != nil {
		h.logRead("Admin could not read journal matches", err)
	}
	for _, m := range matches {
		browse.Matches = append(browse.Matches, pages.JournalMatchView{
			Match: m.Match, Rows: int(m.RowCount),
		})
	}
	// One extra row says whether an older page exists.
	rows, err := h.queries.JournalPage(ctx, sqlc.JournalPageParams{
		Key:      panel.Feed,
		Match:    panel.Match,
		Visitor:  panel.Visitor,
		Before:   panel.Before,
		PageSize: adminJournalPage + 1,
	})
	if err != nil {
		h.logRead("Admin could not read journal rows", err)
		return browse
	}
	if len(rows) > adminJournalPage {
		rows = rows[:adminJournalPage]
		browse.OlderBefore = rows[len(rows)-1].ID
	}
	for _, row := range rows {
		browse.Rows = append(browse.Rows, pages.JournalRowView{
			ID:      row.ID,
			Visitor: row.Visitor,
			Match:   row.Match,
			Rules:   row.Rules,
			At:      row.CreatedAt,
			Payload: row.Payload,
		})
	}
	return browse
}
