package handlers_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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

const (
	visitorA = "aaaaaaaaaaaaaaaa"
	visitorB = "bbbbbbbbbbbbbbbb"
)

// publicID mirrors the server's derived id: rows never carry the cookie.
func publicID(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:8])
}

func declareJournal(t *testing.T, queries *sqlc.Queries, schema string) {
	t.Helper()
	ctx := context.Background()
	site, err := queries.CurrentSite(ctx)
	if err != nil {
		t.Fatalf("current site: %v", err)
	}
	if _, err := queries.CreateSiteVersion(ctx, sqlc.CreateSiteVersionParams{
		Css:           site.Css,
		Note:          site.Note,
		Header:        site.Header,
		Board:         site.Board,
		Footer:        site.Footer,
		StateSchema:   site.StateSchema,
		JournalSchema: schema,
		Reason:        "test declares a journal",
	}); err != nil {
		t.Fatalf("declare journal: %v", err)
	}
}

// appendJournal writes as a page built for rules 1, which is what every test
// schema declares unless it says otherwise.
func appendJournal(
	t *testing.T,
	h http.HandlerFunc,
	key string,
	payload string,
	match int64,
	visitor string,
) *httptest.ResponseRecorder {
	t.Helper()
	return appendJournalRules(t, h, key, payload, match, 1, visitor)
}

func appendJournalRules(
	t *testing.T,
	h http.HandlerFunc,
	key string,
	payload string,
	match int64,
	rules int64,
	visitor string,
) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"payload": json.RawMessage(payload),
		"match":   match,
		"rules":   rules,
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/j/"+key, strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(identityCookie("dave"))
	req.AddCookie(&http.Cookie{Name: handlers.VisitorCookieName, Value: visitor})
	req = withURLParam(req, "key", key)
	rec := httptest.NewRecorder()
	h(rec, req)
	return rec
}

func readJournal(
	t *testing.T,
	h http.HandlerFunc,
	key string,
	since int64,
) (handlers.JournalReadResponse, *httptest.ResponseRecorder) {
	t.Helper()
	req := httptest.NewRequest(
		http.MethodGet,
		"/j/"+key+"?since="+strconv.FormatInt(since, 10),
		nil,
	)
	req = withURLParam(req, "key", key)
	rec := httptest.NewRecorder()
	h(rec, req)
	var out handlers.JournalReadResponse
	if rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("decode: %v (%s)", err, rec.Body.String())
		}
	}
	return out, rec
}

func TestJournalAppendAndRead(t *testing.T) {
	h, queries := setup(t)
	declareJournal(t, queries, `{"moves":{"max":10,"rules":1}}`)

	rec := appendJournal(t, h.JournalAppend, "moves", `{"cell":4}`, 0, visitorA)
	if rec.Code != http.StatusOK {
		t.Fatalf("append status = %d, body %s", rec.Code, rec.Body.String())
	}

	out, readRec := readJournal(t, h.JournalRead, "moves", 0)
	if readRec.Code != http.StatusOK {
		t.Fatalf("read status = %d", readRec.Code)
	}
	if out.Match != 1 || out.Rules != 1 {
		t.Errorf("match/rules = %d/%d, want 1/1", out.Match, out.Rules)
	}
	if len(out.Rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(out.Rows))
	}
	row := out.Rows[0]
	if row.Visitor != publicID(visitorA) {
		t.Errorf("visitor = %q, want the derived id %q", row.Visitor, publicID(visitorA))
	}
	if string(row.Payload) != `{"cell":4}` {
		t.Errorf("payload = %s", row.Payload)
	}

	after, _ := readJournal(t, h.JournalRead, "moves", row.ID)
	if len(after.Rows) != 0 {
		t.Errorf("rows after the cursor = %d, want 0", len(after.Rows))
	}
}

func TestJournalRejectsUndeclaredKey(t *testing.T) {
	h, queries := setup(t)
	declareJournal(t, queries, `{"moves":{"max":10,"rules":1}}`)

	rec := appendJournal(t, h.JournalAppend, "nope", `{"x":1}`, 0, visitorA)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

func TestJournalMatchBoundary(t *testing.T) {
	h, queries := setup(t)
	declareJournal(t, queries, `{"moves":{"max":10,"rules":1}}`)

	if rec := appendJournal(
		t,
		h.JournalAppend,
		"moves",
		`{"x":1}`,
		1,
		visitorA,
	); rec.Code != http.StatusOK {
		t.Fatalf("first append = %d", rec.Code)
	}
	if rec := appendJournal(
		t,
		h.JournalAppend,
		"moves",
		`{"x":2}`,
		2,
		visitorA,
	); rec.Code != http.StatusOK {
		t.Fatalf("new match append = %d, body %s", rec.Code, rec.Body.String())
	}
	if rec := appendJournal(
		t,
		h.JournalAppend,
		"moves",
		`{"x":3}`,
		1,
		visitorA,
	); rec.Code != http.StatusBadRequest {
		t.Fatalf("old match = %d, want 400", rec.Code)
	}
	if rec := appendJournal(
		t,
		h.JournalAppend,
		"moves",
		`{"x":4}`,
		4,
		visitorA,
	); rec.Code != http.StatusBadRequest {
		t.Fatalf("skipped match = %d, want 400", rec.Code)
	}

	out, _ := readJournal(t, h.JournalRead, "moves", 0)
	if out.Match != 2 {
		t.Errorf("match = %d, want 2", out.Match)
	}
}

func TestJournalTrimKeepsNewest(t *testing.T) {
	h, queries := setup(t)
	declareJournal(t, queries, `{"moves":{"max":2,"rules":1}}`)

	for _, payload := range []string{`{"n":1}`, `{"n":2}`, `{"n":3}`} {
		if rec := appendJournal(
			t,
			h.JournalAppend,
			"moves",
			payload,
			0,
			visitorA,
		); rec.Code != http.StatusOK {
			t.Fatalf("append = %d", rec.Code)
		}
	}

	out, _ := readJournal(t, h.JournalRead, "moves", 0)
	if len(out.Rows) != 2 {
		t.Fatalf("rows = %d, want 2", len(out.Rows))
	}
	if string(out.Rows[0].Payload) != `{"n":2}` || string(out.Rows[1].Payload) != `{"n":3}` {
		t.Errorf("kept %s and %s, want the newest two", out.Rows[0].Payload, out.Rows[1].Payload)
	}
}

func TestJournalAssignsVisitor(t *testing.T) {
	h, queries := setup(t)
	declareJournal(t, queries, `{"moves":{"max":10,"rules":1}}`)

	body := strings.NewReader(`{"payload":{"cell":1},"rules":1}`)
	req := httptest.NewRequest(http.MethodPost, "/j/moves", body)
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(identityCookie("dave"))
	req = withURLParam(req, "key", "moves")
	rec := httptest.NewRecorder()
	h.JournalAppend(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
	found := ""
	for _, cookie := range rec.Result().Cookies() {
		if cookie.Name == handlers.VisitorCookieName {
			found = cookie.Value
		}
	}
	if len(found) != 16 {
		t.Fatalf("visitor cookie = %q, want a 16 character id", found)
	}
}

func TestClientErrorCollapsesBurst(t *testing.T) {
	h, queries := setup(t)
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		req := httptest.NewRequest(
			http.MethodPost,
			"/x/err",
			strings.NewReader(`{"message":"reduce is not a function"}`),
		)
		rec := httptest.NewRecorder()
		h.ClientError(rec, req)
		if rec.Code != http.StatusNoContent {
			t.Fatalf("status = %d, want 204", rec.Code)
		}
	}

	rows, err := queries.ListRoomLog(ctx, sqlc.ListRoomLogParams{
		ID:    0,
		ID_2:  1 << 40,
		Limit: 100,
	})
	if err != nil {
		t.Fatalf("room log: %v", err)
	}
	found := 0
	for _, row := range rows {
		if row.Kind == "alert" {
			found++
		}
	}
	if found != 1 {
		t.Fatalf("alert turns = %d, want 1 after a burst", found)
	}
}

// TestGameRequestShipsAWorkingFeed walks the whole loop a visitor sees: a post
// asks for a game, the model declares a feed and ships the applet, the page
// carries the runtime, and a second visitor can write to the feed.
func TestGameRequestShipsAWorkingFeed(t *testing.T) {
	h, queries := setup(t)
	ctx := context.Background()

	rec := postForm(t, h.CreatePost, "/posts", url.Values{
		"body": {"make a two player game mockgame"},
	})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("post status = %d, want 303", rec.Code)
	}

	waitFor(t, "the game version", func() bool {
		site, err := queries.CurrentSite(ctx)
		return err == nil &&
			strings.Contains(site.JournalSchema, `"moves"`) &&
			strings.Contains(site.Board, `bd.journal("moves")`)
	})

	site, err := queries.CurrentSite(ctx)
	if err != nil {
		t.Fatalf("current site: %v", err)
	}
	if !strings.Contains(site.Board, "{{posts}}") {
		t.Fatal("the game page dropped the posts placeholder")
	}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(identityCookie("dave"))
	rec2 := httptest.NewRecorder()
	h.Index(rec2, req)
	page := rec2.Body.String()
	if !strings.Contains(page, `id="bd-journal"`) {
		t.Fatal("the served page has no journal config")
	}
	if !strings.Contains(page, `"key":"moves"`) && !strings.Contains(page, `"moves":`) {
		t.Fatal("the journal config does not name the feed")
	}

	if rec := appendJournal(
		t,
		h.JournalAppend,
		"moves",
		`{"join":1}`,
		0,
		visitorB,
	); rec.Code != http.StatusOK {
		t.Fatalf("join = %d, body %s", rec.Code, rec.Body.String())
	}
	out, _ := readJournal(t, h.JournalRead, "moves", 0)
	if len(out.Rows) != 1 || out.Rows[0].Visitor != publicID(visitorB) {
		t.Fatalf("rows = %+v, want the join from %s", out.Rows, visitorB)
	}
}

// A version whose only change is the journal schema must reload: the page
// carries the key list and the match numbers, so swapping CSS alone would
// leave the runtime with a stale config.
func TestVersionReloadsWhenOnlyTheJournalChanges(t *testing.T) {
	h, queries := setup(t)
	ctx := context.Background()

	seed, err := queries.CurrentSite(ctx)
	if err != nil {
		t.Fatalf("current site: %v", err)
	}
	if _, err := queries.CreateSiteVersion(ctx, sqlc.CreateSiteVersionParams{
		Css:           seed.Css,
		Note:          seed.Note,
		Header:        seed.Header,
		Board:         seed.Board,
		Footer:        seed.Footer,
		StateSchema:   seed.StateSchema,
		JournalSchema: `{"moves":{"max":10,"rules":1}}`,
		Reason:        "journal only",
	}); err != nil {
		t.Fatalf("create version: %v", err)
	}

	resp := getVersion(t, h, "/version?have=1")
	if resp.Swap != "reload" {
		t.Fatalf("swap = %q, want reload", resp.Swap)
	}
}

// A tab still running a replaced reducer sends the old rules number. Its write
// is refused before it can land under rules it does not follow.
func TestJournalRefusesStaleRules(t *testing.T) {
	h, queries := setup(t)
	declareJournal(t, queries, `{"moves":{"max":10,"rules":2}}`)

	for _, rules := range []int64{0, 1, 3} {
		rec := appendJournalRules(t, h.JournalAppend, "moves", `{"x":1}`, 0, rules, visitorA)
		if rec.Code != http.StatusConflict {
			t.Fatalf("rules %d: status = %d, want 409", rules, rec.Code)
		}
	}
	if rec := appendJournalRules(
		t,
		h.JournalAppend,
		"moves",
		`{"x":1}`,
		0,
		2,
		visitorA,
	); rec.Code != http.StatusOK {
		t.Fatalf("current rules: status = %d, body %s", rec.Code, rec.Body.String())
	}

	out, _ := readJournal(t, h.JournalRead, "moves", 0)
	if len(out.Rows) != 1 || out.Rows[0].Rules != 2 {
		t.Fatalf("rows = %+v, want one row under rules 2", out.Rows)
	}
}

// The runtime matches this refusal by its text, so the handler must send the
// shared constant.
func TestJournalClosedMatchSendsTheSharedText(t *testing.T) {
	h, queries := setup(t)
	declareJournal(t, queries, `{"moves":{"max":10,"rules":1}}`)

	appendJournal(t, h.JournalAppend, "moves", `{"x":1}`, 2, visitorA)
	rec := appendJournal(t, h.JournalAppend, "moves", `{"x":2}`, 1, visitorB)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	var body struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Error != webmaster.JournalMatchClosed {
		t.Fatalf("error = %q, want %q", body.Error, webmaster.JournalMatchClosed)
	}
}

func countAlerts(t *testing.T, queries *sqlc.Queries) int {
	t.Helper()
	rows, err := queries.ListRoomLog(context.Background(), sqlc.ListRoomLogParams{
		ID:    0,
		ID_2:  1 << 40,
		Limit: 100,
	})
	if err != nil {
		t.Fatalf("room log: %v", err)
	}
	found := 0
	for _, row := range rows {
		if row.Kind == "alert" {
			found++
		}
	}
	return found
}

func sendClientError(t *testing.T, h *handlers.Handlers, body string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/x/err", strings.NewReader(body))
	rec := httptest.NewRecorder()
	h.ClientError(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rec.Code)
	}
}

// An error from a replaced page is about code nobody is served any more, so
// it never reaches the room.
func TestClientErrorDropsReplacedPages(t *testing.T) {
	h, queries := setup(t)
	ctx := context.Background()

	old, err := queries.CurrentSite(ctx)
	if err != nil {
		t.Fatalf("current site: %v", err)
	}
	declareJournal(t, queries, `{"moves":{"rules":1}}`)
	current, err := queries.CurrentSite(ctx)
	if err != nil {
		t.Fatalf("current site: %v", err)
	}

	sendClientError(t, h, `{"message":"old page broke","version":`+
		strconv.FormatInt(old.ID, 10)+`}`)
	if got := countAlerts(t, queries); got != 0 {
		t.Fatalf("alerts = %d, want 0 from a replaced page", got)
	}

	sendClientError(t, h, `{"message":"current page broke","version":`+
		strconv.FormatInt(current.ID, 10)+`}`)
	if got := countAlerts(t, queries); got != 1 {
		t.Fatalf("alerts = %d, want 1 from the current page", got)
	}
}

// The prompt shows the newest rows of the match being played, with who wrote
// them, and nothing from an older match or older rules.
func TestJournalStateShowsTheNewestMatch(t *testing.T) {
	h, queries := setup(t)
	ctx := context.Background()
	declareJournal(t, queries, `{"moves":{"max":10,"rules":1}}`)

	appendJournal(t, h.JournalAppend, "moves", `{"cell":0}`, 1, visitorA)
	appendJournal(t, h.JournalAppend, "moves", `{"cell":1}`, 1, visitorB)
	appendJournal(t, h.JournalAppend, "moves", `{"start":1}`, 2, visitorA)
	appendJournal(t, h.JournalAppend, "moves", `{"cell":4}`, 2, visitorB)

	state := h.JournalState(ctx, `{"moves":{"max":10,"rules":1}}`)["moves"]
	if state.Match != 2 || state.Rows != 4 {
		t.Fatalf("state = %+v, want match 2 with 4 rows", state)
	}
	if len(state.Last) != 2 {
		t.Fatalf("last = %+v, want the two rows of match 2", state.Last)
	}
	first, second := state.Last[0], state.Last[1]
	if string(first.Payload) != `{"start":1}` || first.Visitor != publicID(visitorA) {
		t.Errorf("first = %+v, want visitor A's start", first)
	}
	if string(second.Payload) != `{"cell":4}` || second.Visitor != publicID(visitorB) ||
		second.Match != 2 {
		t.Errorf("second = %+v, want visitor B's move in match 2", second)
	}

	// Under new rules the old match's rows are a different game.
	bumped := h.JournalState(ctx, `{"moves":{"max":10,"rules":2}}`)["moves"]
	if len(bumped.Last) != 0 {
		t.Fatalf("last = %+v, want nothing under rules 2", bumped.Last)
	}
}

// The schema tool's floor counts rows under every key, declared or not.
func TestJournalRulesUsedCountsUndeclaredKeys(t *testing.T) {
	h, queries := setup(t)
	ctx := context.Background()
	declareJournal(t, queries, `{"moves":{"max":10,"rules":3},"chat":{"max":10,"rules":1}}`)
	appendJournalRules(t, h.JournalAppend, "moves", `{"x":1}`, 0, 3, visitorA)
	appendJournal(t, h.JournalAppend, "chat", `{"x":1}`, 0, visitorA)
	declareJournal(t, queries, `{"chat":{"max":10,"rules":1}}`)

	used := h.JournalRulesUsed(ctx)
	if used["moves"] != 3 || used["chat"] != 1 {
		t.Fatalf("used = %v, want moves 3 and chat 1", used)
	}
}

// Heal clears the journal with the version it writes. A write that fails
// leaves the game and the shared state where they were.
func TestHealKeepsTheJournalWhenTheWriteFails(t *testing.T) {
	h, queries := setup(t)
	ctx := context.Background()
	declareJournal(t, queries, `{"moves":{"max":10,"rules":1}}`)
	appendJournal(t, h.JournalAppend, "moves", `{"x":1}`, 0, visitorA)
	if err := queries.UpsertSiteState(ctx, sqlc.UpsertSiteStateParams{
		Key:   "votes",
		Value: "7",
	}); err != nil {
		t.Fatalf("upsert state: %v", err)
	}

	if err := h.ExecForTest(ctx, `CREATE TRIGGER no_versions BEFORE INSERT ON site_versions
		BEGIN SELECT RAISE(ABORT, 'no versions'); END`); err != nil {
		t.Fatalf("create trigger: %v", err)
	}
	postForm(t, h.HealBoard, "/admin/heal?token="+testAdminToken, url.Values{})
	if count, err := queries.CountJournal(ctx); err != nil || count != 1 {
		t.Fatalf("journal rows after a failed heal = %d (%v), want 1", count, err)
	}
	if state, err := queries.ListSiteState(ctx); err != nil || len(state) != 1 {
		t.Fatalf("state after a failed heal = %+v (%v), want the vote kept", state, err)
	}

	if err := h.ExecForTest(ctx, `DROP TRIGGER no_versions`); err != nil {
		t.Fatalf("drop trigger: %v", err)
	}
	postForm(t, h.HealBoard, "/admin/heal?token="+testAdminToken, url.Values{})
	if count, err := queries.CountJournal(ctx); err != nil || count != 0 {
		t.Fatalf("journal rows after a heal = %d (%v), want 0", count, err)
	}
}
