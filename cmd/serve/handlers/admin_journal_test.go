package handlers_test

import (
	"context"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/Napam/beaverdream/cmd/serve/handlers"
	"github.com/Napam/beaverdream/db/generated/sqlc"
	"github.com/Napam/beaverdream/lib/webmaster"
)

// postClear posts the clear form for one key with the admin token.
func postClear(t *testing.T, h *handlers.Handlers, key string) *httptest.ResponseRecorder {
	t.Helper()
	return postAdmin(t, func(w http.ResponseWriter, r *http.Request) {
		h.ClearJournal(w, withURLParam(r, "key", key))
	}, "/admin/journal/"+key+"/clear", url.Values{})
}

// Clearing a key deletes only that key, records one event turn the webmaster
// reads in the room log, and never starts a model turn.
func TestClearJournalDeletesOneKeyAndRecordsAnEvent(t *testing.T) {
	h, queries := setup(t)
	ctx := context.Background()
	declareJournal(t, queries, `{"moves":{"max":10,"rules":1},"chat":{"max":10,"rules":1}}`)
	appendJournal(t, h.JournalAppend, "moves", `{"x":1}`, 0, visitorA)
	appendJournal(t, h.JournalAppend, "moves", `{"x":2}`, 2, visitorA)
	appendJournal(t, h.JournalAppend, "chat", `{"m":"hi"}`, 0, visitorB)

	rec := postClear(t, h, "moves")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "cleared moves (2 rows)") {
		t.Fatalf("clear = %d %q, want cleared moves (2 rows)", rec.Code, rec.Body.String())
	}

	if rows, err := queries.JournalLatest(ctx, "moves"); err != nil || rows.RowCount != 0 {
		t.Fatalf("moves rows = %+v (%v), want none", rows, err)
	}
	if rows, err := queries.JournalLatest(ctx, "chat"); err != nil || rows.RowCount != 1 {
		t.Fatalf("chat rows = %+v (%v), want the other key kept", rows, err)
	}

	log, err := queries.ListRoomLog(ctx, sqlc.ListRoomLogParams{
		ID: 0, ID_2: math.MaxInt64, Limit: 50,
	})
	if err != nil {
		t.Fatalf("room log: %v", err)
	}
	var events []sqlc.ListRoomLogRow
	for _, row := range log {
		if row.Kind == webmaster.TurnJournal {
			events = append(events, row)
		}
	}
	if len(events) != 1 || !strings.Contains(events[0].Body, `"moves" (2 rows, was match 2)`) {
		t.Fatalf("event turns = %+v, want one that names the key, rows and match", events)
	}
	if turns, err := queries.ListChangesets(ctx, 10); err != nil || len(turns) != 0 {
		t.Fatalf(
			"changesets = %d (%v), want none: a clear never asks the model",
			len(turns),
			err,
		)
	}

	// A second clear finds nothing and records nothing.
	again := postClear(t, h, "moves")
	if !strings.Contains(again.Body.String(), "moves has no rows") {
		t.Fatalf("second clear = %q, want no rows", again.Body.String())
	}
}

// A key is a lowercase name. Anything else is refused before it reaches a
// query, a path or a script.
func TestClearJournalRefusesAnOddKey(t *testing.T) {
	h, _ := setup(t)
	rec := postClear(t, h, `a'b`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("odd key = %d, want 404", rec.Code)
	}
}

// An action without the token is refused.
func TestClearJournalNeedsAdmin(t *testing.T) {
	h, _ := setup(t)
	rec := postFormAs(t, func(w http.ResponseWriter, r *http.Request) {
		h.ClearJournal(w, withURLParam(r, "key", "moves"))
	}, "/admin/journal/moves/clear", url.Values{}, "")
	if rec.Code == http.StatusOK || rec.Code == http.StatusSeeOther {
		t.Fatalf("clear without a token = %d, want a refusal", rec.Code)
	}
}

// A clear that leaves the newest row id where it was still reaches an open
// page: its stream tells it to read its feeds again.
func TestLiveStreamResyncsAfterAClear(t *testing.T) {
	h, queries := setup(t)
	declareJournal(t, queries, `{"moves":{"max":10,"rules":1},"chat":{"max":10,"rules":1}}`)
	appendJournal(t, h.JournalAppend, "moves", `{"x":1}`, 0, visitorA)
	appendJournal(t, h.JournalAppend, "chat", `{"m":"hi"}`, 0, visitorB)
	version := currentVersion(t, queries)

	// The page holds the newest id, which belongs to the key that stays.
	live := openLive(t, h, cursor{Version: version, Journal: 2})
	live.next(t, []string{`"shared":`})

	postClear(t, h, "moves")
	live.next(t, []string{"bd._resync()"})
}

// The journal page lists each key with its stats and the rows of the picked
// one, and shows hostile payload text as text.
func TestAdminJournalPageShowsStatsAndRows(t *testing.T) {
	h, queries := setup(t)
	declareJournal(t, queries, `{"moves":{"max":10,"rules":1},"chat":{"max":10,"rules":1}}`)
	appendJournal(t, h.JournalAppend, "moves", `{"x":1}`, 0, visitorA)
	appendJournal(t, h.JournalAppend, "moves", `{"x":2}`, 0, visitorB)
	appendJournal(t, h.JournalAppend, "chat", `{"m":"<script>alert(1)</script>"}`, 0, visitorA)

	rec := getAdmin(t, h.AdminJournal, "/admin/journal?key=chat")
	body := rec.Body.String()
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	for _, want := range []string{
		`id="admin-journal"`,
		"2 / 10 rows",
		"2 visitors",
		"1 / 10 rows",
		"rows of",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("journal page lacks %q", want)
		}
	}
	if strings.Contains(body, "<script>alert(1)</script>") {
		t.Error("a payload reached the page as markup")
	}
	if !strings.Contains(body, "alert(1)") {
		t.Error("the payload text is not shown")
	}
	// The picked key is chat, so the moves rows are not listed.
	if strings.Contains(body, `{&#34;x&#34;:2}`) || strings.Contains(body, `{"x":2}`) {
		t.Error("rows of another key are listed")
	}
}

// With nothing picked, the first key that has rows is browsed. A visitor
// filter and a before cursor narrow the rows, newest first.
func TestAdminJournalBrowseFiltersAndPages(t *testing.T) {
	h, queries := setup(t)
	declareJournal(t, queries, `{"moves":{"max":200,"rules":1}}`)
	for i := range 60 {
		who := visitorA
		if i%2 == 1 {
			who = visitorB
		}
		appendJournalAs(t, h, who, i)
	}

	body := getAdmin(t, h.AdminJournal, "/admin/journal").Body.String()
	if !strings.Contains(body, "older rows") || !strings.Contains(body, "before=11") {
		t.Errorf("the default page has no link to older rows from id 11")
	}

	filtered := getAdmin(t, h.AdminJournal,
		"/admin/journal?key=moves&visitor="+publicID(visitorA)).Body.String()
	if strings.Contains(filtered, "visitor "+publicID(visitorB)) {
		t.Error("the filter shows the other visitor")
	}
	if !strings.Contains(filtered, "show everyone") {
		t.Error("a visitor filter has no way back")
	}

	older := getAdmin(t, h.AdminJournal, "/admin/journal?key=moves&before=11").Body.String()
	if !strings.Contains(older, "back to the newest") || strings.Contains(older, "older rows") {
		t.Error("the last page links the wrong way")
	}
}

func appendJournalAs(t *testing.T, h *handlers.Handlers, visitor string, n int) {
	t.Helper()
	rec := appendJournal(t, h.JournalAppend, "moves", `{"n":`+strconv.Itoa(n)+`}`, 0, visitor)
	if rec.Code != http.StatusOK {
		t.Fatalf("append %d: status %d %s", n, rec.Code, rec.Body.String())
	}
}

// The journal page's stream patches the picked selection, and a clear reaches
// it.
func TestAdminLivePushesTheJournalPage(t *testing.T) {
	h, queries := setup(t)
	declareJournal(t, queries, `{"moves":{"max":10,"rules":1}}`)
	appendJournal(t, h.JournalAppend, "moves", `{"x":1}`, 0, visitorA)

	live := openAdminLive(t, h, "page=journal&key=moves", testAdminToken)
	live.next(t, []string{`id="admin-live"`})
	live.next(t, []string{`id="admin-journal"`, "1 / 10 rows"})

	appendJournal(t, h.JournalAppend, "moves", `{"x":2}`, 0, visitorB)
	live.next(t, []string{`id="admin-journal"`, "2 / 10 rows"})
}

// The page reads a stored journal schema without trusting it: a key the admin
// name rule refuses never reaches a card, a path or a script.
func TestAdminJournalSkipsAnOddDeclaredKey(t *testing.T) {
	h, queries := setup(t)
	declareJournal(t, queries,
		`{"feed":{"max":10,"rules":1},"evil'key":{"max":10,"rules":1}}`)

	body := getAdmin(t, h.AdminJournal, "/admin/journal").Body.String()
	if !strings.Contains(body, "feed") {
		t.Fatal("the page does not list the key that follows the name rule")
	}
	if strings.Contains(body, "evil") {
		t.Error("a stored key that fails the name rule reached the page")
	}
}

// The declared side of the holdings list takes the same read gate. A stored
// schema with a visitor key the admin name rule refuses never reaches a card,
// even when no holding row exists to carry it.
func TestAdminHoldingsSkipsAnOddDeclaredKey(t *testing.T) {
	h, queries := setup(t)
	declareState(t, queries,
		`{"coins":{"op":"counter","scope":"visitor"},`+
			`"evil'key":{"op":"counter","scope":"visitor"}}`)

	body := getAdmin(t, h.AdminJournal, "/admin/journal").Body.String()
	if !strings.Contains(body, "coins") {
		t.Fatal("the page does not list the declared visitor counter that follows the name rule")
	}
	if strings.Contains(body, "evil") {
		t.Error("a declared visitor key that fails the name rule reached the page")
	}
}

// Holdings come from stored totals too, so they take the same read gate as
// journal keys.
func TestAdminHoldingsSkipsAnOddStoredKey(t *testing.T) {
	h, queries := setup(t)
	declareState(t, queries, `{"coins":{"op":"counter","scope":"visitor"}}`)
	for _, query := range []string{
		`INSERT INTO state_holdings (visitor, key, amount, name) VALUES ('v', 'coins', 3, 'dave')`,
		`INSERT INTO state_holdings (visitor, key, amount, name) VALUES ('v', 'evil''key', 4, 'dave')`,
	} {
		if err := h.ExecForTest(context.Background(), query); err != nil {
			t.Fatalf("seed holding: %v", err)
		}
	}

	body := getAdmin(t, h.AdminJournal, "/admin/journal").Body.String()
	if !strings.Contains(body, "coins") {
		t.Fatal("the page does not list the visitor counter that follows the name rule")
	}
	if strings.Contains(body, "evil") {
		t.Error("a stored holding key that fails the name rule reached the page")
	}
}

// The overview shows the notes the next prompt will carry, next to the
// room summary.
func TestAdminOverviewShowsTheNotes(t *testing.T) {
	h, queries := setup(t)
	ctx := context.Background()

	empty := getAdmin(t, h.Admin, "/admin").Body.String()
	if !strings.Contains(empty, "no notes yet") {
		t.Fatal("an empty room should say there are no notes")
	}

	turn, err := queries.CreateTurn(ctx, sqlc.CreateTurnParams{
		Kind: webmaster.TurnChangeset, Author: "webmaster", Body: "seed",
	})
	if err != nil {
		t.Fatalf("create turn: %v", err)
	}
	if err := queries.FinishTurn(ctx, sqlc.FinishTurnParams{
		Said: "ok", Status: "kept", Notes: "kit owes me one", Messages: "[]", ID: turn.ID,
	}); err != nil {
		t.Fatalf("finish turn: %v", err)
	}
	body := getAdmin(t, h.Admin, "/admin").Body.String()
	if !strings.Contains(body, "kit owes me one") {
		t.Error("the overview does not show the notes")
	}
}
