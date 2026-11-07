package handlers_test

import (
	"context"
	"encoding/json"
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

// Reset must reach every table a migration adds, not only the ones it was
// written for: anything left behind outlives the board it belonged to.
func TestResetLeavesOnlyTheSeed(t *testing.T) {
	h, queries := setup(t)
	ctx := context.Background()

	if _, err := queries.CreatePost(
		ctx,
		sqlc.CreatePostParams{Author: "dave", Body: "hi"},
	); err != nil {
		t.Fatalf("create post: %v", err)
	}
	for _, query := range []string{
		`INSERT INTO site_versions (html, turn_id) VALUES ('{{posts}}', 1)`,
		`INSERT INTO room_summaries (through_turn_id, text) VALUES (1, 'gist')`,
		`INSERT INTO site_state (key, value) VALUES ('likes', '3')`,
		`INSERT INTO journal (key, payload) VALUES ('moves', '{}')`,
		`INSERT INTO state_holdings (visitor, key, amount, name) VALUES ('v', 'coins', 3, 'dave')`,
		`INSERT INTO state_ops (op_id, visitor, op, source, key, delta)
		 VALUES (1, 'v', 'add', 'coins', 'coins', 3)`,
	} {
		if err := h.ExecForTest(ctx, query); err != nil {
			t.Fatalf("seed %q: %v", query, err)
		}
	}
	if rec := postAdmin(t, h.ResetBoard, "/admin/reset", url.Values{}); rec.Code != http.StatusOK {
		t.Fatalf("reset status = %d", rec.Code)
	}

	counts, err := h.TableCountsForTest(ctx)
	if err != nil {
		t.Fatalf("count tables: %v", err)
	}
	for table, n := range counts {
		want := 0
		switch table {
		case "site_versions":
			want = 1
		case "goose_db_version", "sqlite_sequence":
			continue
		}
		if n != want {
			t.Errorf("%s has %d rows after reset, want %d", table, n, want)
		}
	}
	// Every id sequence starts over except the seed's, which points at it.
	sequences, err := h.SequencesForTest(ctx)
	if err != nil {
		t.Fatalf("read sequences: %v", err)
	}
	for name, seq := range sequences {
		if name != "site_versions" && name != "goose_db_version" {
			t.Errorf("sequence %s is still at %d after reset", name, seq)
		}
	}
}

// walletSchema has a shared counter, two visitor counters and a recipe.
const walletSchema = `{
	"visits": {"op": "counter"},
	"coins": {"op": "counter", "scope": "visitor", "label": "Gold", "per_hour": 100},
	"rod": {"op": "counter", "scope": "visitor", "max": 2},
	"buy_rod": {"op": "recipe", "cost": {"coins": 30}, "gain": {"rod": 1}}
}`

func declareState(t *testing.T, queries *sqlc.Queries, schema string) sqlc.SiteVersion {
	t.Helper()
	ctx := context.Background()
	seed, err := queries.CurrentSite(ctx)
	if err != nil {
		t.Fatalf("current site: %v", err)
	}
	site, err := queries.CreateSiteVersion(ctx, sqlc.CreateSiteVersionParams{
		Css:         seed.Css,
		Html:        seed.Html + "<!-- " + schema + " -->",
		StateSchema: schema,
		Reason:      "test declares state",
	})
	if err != nil {
		t.Fatalf("declare state: %v", err)
	}
	return site
}

type opResult struct {
	OK     bool             `json:"ok"`
	Reason string           `json:"reason"`
	Mine   map[string]int64 `json:"mine"`
}

// stateOp sends one visitor op the way bd.state does: a fetch that wants JSON,
// from a browser with a name and a visitor cookie.
func stateOp(t *testing.T, h *handlers.Handlers, path, visitor string) (int, opResult) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, nil)
	req.Header.Set("Accept", "application/json")
	req = handlers.WithTestPrincipal(req, visitor, "dave", false, true)
	key, _, _ := strings.Cut(strings.TrimPrefix(req.URL.Path, "/x/state/"), "?")
	req = withURLParam(req, "key", key)
	rec := httptest.NewRecorder()
	h.SetState(rec, req)
	var got opResult
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("%s: unreadable answer %q: %v", path, rec.Body.String(), err)
	}
	return rec.Code, got
}

// wantOp checks one op's outcome. An empty reason means it must succeed.
func wantOp(t *testing.T, h *handlers.Handlers, path, visitor, reason string) opResult {
	t.Helper()
	code, got := stateOp(t, h, path, visitor)
	if reason == "" && (!got.OK || code != http.StatusOK) {
		t.Fatalf("%s: status %d, %+v, want ok", path, code, got)
	}
	if reason != "" && (got.OK || got.Reason != reason || code < 400) {
		t.Fatalf("%s: status %d, %+v, want refused as %s", path, code, got, reason)
	}
	return got
}

func TestVisitorCountersAddSpendAndRefuse(t *testing.T) {
	h, queries := setup(t)
	h.NoStateCooldown()
	declareState(t, queries, walletSchema)

	if got := wantOp(t, h, "/x/state/coins?add=5", visitorA, ""); got.Mine["coins"] != 5 {
		t.Fatalf("mine after add = %v, want 5 coins", got.Mine)
	}
	wantOp(t, h, "/x/state/coins?spend=7", visitorA, "short")
	if got := wantOp(t, h, "/x/state/coins?spend=5", visitorA, ""); got.Mine["coins"] != 0 {
		t.Fatalf("mine after spend = %v, want 0 coins", got.Mine)
	}
	wantOp(t, h, "/x/state/coins?add=0", visitorA, "bad")
	wantOp(t, h, "/x/state/coins?add=1.5", visitorA, "bad")
	wantOp(t, h, "/x/state/coins?add=1&spend=1", visitorA, "bad")
	wantOp(t, h, "/x/state/coins", visitorA, "bad")
	wantOp(t, h, "/x/state/fish?add=1", visitorA, "gone")

	wantOp(t, h, "/x/state/coins?add=3", visitorB, "")
	if got := wantOp(t, h, "/x/state/coins?add=1", visitorA, ""); got.Mine["coins"] != 1 {
		t.Fatalf("visitor A holds %v, want only their own coin", got.Mine)
	}
}

func TestVisitorCountersKeepTheirHourBudget(t *testing.T) {
	h, queries := setup(t)
	h.NoStateCooldown()
	declareState(t, queries, walletSchema)

	wantOp(t, h, "/x/state/coins?add=60", visitorA, "")
	wantOp(t, h, "/x/state/coins?add=50", visitorA, "cap")
	wantOp(t, h, "/x/state/coins?add=40", visitorA, "")
	// Spending does not give the budget back.
	wantOp(t, h, "/x/state/coins?spend=100", visitorA, "")
	wantOp(t, h, "/x/state/coins?add=1", visitorA, "cap")
	// Each visitor has their own budget.
	wantOp(t, h, "/x/state/coins?add=100", visitorB, "")
}

// A trade either moves every count or none. The third rod passes its cost
// but would pass the max, so the coins stay.
func TestTradeIsAllOrNothing(t *testing.T) {
	h, queries := setup(t)
	h.NoStateCooldown()
	declareState(t, queries, walletSchema)

	wantOp(t, h, "/x/state/buy_rod", visitorA, "short")
	wantOp(t, h, "/x/state/coins?add=100", visitorA, "")
	wantOp(t, h, "/x/state/buy_rod", visitorA, "")
	got := wantOp(t, h, "/x/state/buy_rod", visitorA, "")
	if got.Mine["coins"] != 40 || got.Mine["rod"] != 2 {
		t.Fatalf("after two trades = %v, want 40 coins and 2 rods", got.Mine)
	}
	wantOp(t, h, "/x/state/buy_rod", visitorA, "full")
	got = wantOp(t, h, "/x/state/coins?add=0", visitorA, "bad")
	if got.Mine != nil {
		t.Fatalf("a refusal carried holdings: %+v", got)
	}
	if got := wantOp(t, h, "/x/state/coins?spend=40", visitorA, ""); got.Mine["rod"] != 2 {
		t.Fatalf("the refused trade moved something: %v", got.Mine)
	}
}

// A recipe's gain uses the gained key's hour budget like an add does.
func TestTradeGainsCountAgainstTheHourBudget(t *testing.T) {
	h, queries := setup(t)
	h.NoStateCooldown()
	declareState(t, queries, `{
		"coins": {"op": "counter", "scope": "visitor"},
		"gem": {"op": "counter", "scope": "visitor", "per_hour": 1},
		"buy_gem": {"op": "recipe", "cost": {"coins": 1}, "gain": {"gem": 1}}
	}`)
	wantOp(t, h, "/x/state/coins?add=5", visitorA, "")
	wantOp(t, h, "/x/state/buy_gem", visitorA, "")
	wantOp(t, h, "/x/state/buy_gem", visitorA, "cap")
}

// Visitor counters are not shared values: they never take the global +1 and
// never show in $shared, bd-state or GET /x/state.
func TestVisitorKeysStayOutOfSharedValues(t *testing.T) {
	h, queries := setup(t)
	declareState(t, queries, walletSchema)

	rec := datastarPost(t, h.SetState, "/x/state/coins", nil, "dave")
	if rec.Code < 400 {
		t.Fatalf("a bare post to a visitor counter: status = %d, want a refusal", rec.Code)
	}
	rec = datastarPost(t, h.SetState, "/x/state/visits", nil, "dave")
	if rec.Code != http.StatusNoContent {
		t.Fatalf("the global counter: status = %d, want 204", rec.Code)
	}

	get := httptest.NewRequest(http.MethodGet, "/x/state", nil)
	got := httptest.NewRecorder()
	h.State(got, get)
	var body struct {
		State map[string]json.RawMessage `json:"state"`
	}
	if err := json.Unmarshal(got.Body.Bytes(), &body); err != nil {
		t.Fatalf("read state: %v", err)
	}
	if string(body.State["visits"]) != "1" || len(body.State) != 1 {
		t.Fatalf("state = %v, want only visits at 1", body.State)
	}
	if rows, _ := queries.HoldingTotals(context.Background()); len(rows) != 0 {
		t.Fatalf("holdings after a bare post: %+v", rows)
	}
}

// The cooldown is per browser and key. A looping script is stopped before it
// writes, and someone else who picked the same name is not.
func TestStateCooldownKeysOnTheBrowser(t *testing.T) {
	h, queries := setup(t)
	declareState(t, queries, walletSchema)

	wantOp(t, h, "/x/state/coins?add=1", visitorA, "")
	for range 5 {
		wantOp(t, h, "/x/state/coins?add=1", visitorA, "slow")
	}
	wantOp(t, h, "/x/state/coins?add=1", visitorB, "")

	counts, err := h.TableCountsForTest(context.Background())
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if counts["state_ops"] != 2 {
		t.Fatalf("state_ops = %d, want the two writes that went through", counts["state_ops"])
	}
}

// Holdings belong to the name. Heal hides them, a schema that reuses the name
// as a shared counter leaves them alone, and declaring it as a visitor
// counter again brings them back.
func TestHoldingsOutliveTheirKey(t *testing.T) {
	h, queries := setup(t)
	h.NoStateCooldown()
	global := declareState(t, queries, `{"coins": {"op": "counter"}}`)
	wallet := declareState(t, queries, walletSchema)

	wantOp(t, h, "/x/state/coins?add=7", visitorA, "")

	if rec := postAdmin(t, h.HealBoard, "/admin/heal", url.Values{}); rec.Code != http.StatusOK {
		t.Fatalf("heal status = %d", rec.Code)
	}
	wantOp(t, h, "/x/state/coins?add=1", visitorA, "gone")

	restore := func(id int64) {
		t.Helper()
		rec := postAdminAt(t, h.RestoreVersion, "/admin/restore", strconv.FormatInt(id, 10))
		if rec.Code != http.StatusOK {
			t.Fatalf("restore %d: status = %d", id, rec.Code)
		}
	}
	restore(global.ID)
	if rec := datastarPost(
		t,
		h.SetState,
		"/x/state/coins",
		nil,
		"dave",
	); rec.Code != http.StatusNoContent {
		t.Fatalf("the shared coins counter: status = %d, want 204", rec.Code)
	}

	restore(wallet.ID)
	if got := wantOp(t, h, "/x/state/coins?add=1", visitorA, ""); got.Mine["coins"] != 8 {
		t.Fatalf("coins after coming back = %v, want 8", got.Mine)
	}
}

// Each stream pushes its own visitor's $mine and the same $tops to everyone.
func TestLiveStreamPushesOnlyThisVisitorsHoldings(t *testing.T) {
	h, queries := setup(t)
	site := declareState(t, queries, walletSchema)

	open := func(visitor string) *liveClient {
		signals, _ := json.Marshal(map[string]cursor{"_bd": {Version: site.ID}})
		return openSSE(t, h.Live, "/live?datastar="+url.QueryEscape(string(signals)),
			func(next http.Handler) http.Handler {
				return handlers.AuthForTest(h, next, visitor, "dave", false)
			},
			func(req *http.Request) *http.Request {
				req.Header.Set("Datastar-Request", "true")
				return req
			})
	}
	a, b := open(visitorA), open(visitorB)
	a.next(t, []string{`"mine":{"coins":0,"rod":0}`})
	b.next(t, []string{`"mine":{"coins":0,"rod":0}`})

	wantOp(t, h, "/x/state/coins?add=5", visitorA, "")
	a.next(t, []string{`"mine":{"coins":5,"rod":0}`, `"amount":5`})
	b.next(t, []string{`"tops":`, `"amount":5`}, `"mine":`)
}

// The first render already has $mine and $tops, and the page finds its own
// top row by bd.visitor. Names on the top list do not depend on the op log.
func TestFirstRenderSeedsTheVisitorsHoldings(t *testing.T) {
	h, queries := setup(t)
	declareState(t, queries, walletSchema)
	wantOp(t, h, "/x/state/coins?add=5", visitorA, "")
	if err := h.ExecForTest(context.Background(), `DELETE FROM state_ops`); err != nil {
		t.Fatalf("prune: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req = handlers.WithTestPrincipal(req, visitorA, "dave", false, true)
	rec := httptest.NewRecorder()
	h.Index(rec, req)
	page := rec.Body.String()
	if !strings.Contains(page, `&#34;mine&#34;:{&#34;coins&#34;:5,&#34;rod&#34;:0}`) {
		t.Fatalf("the live mount has no $mine")
	}

	_, rest, _ := strings.Cut(page, `<script type="application/json" id="bd-journal">`)
	raw, _, _ := strings.Cut(rest, `</script>`)
	var conf struct {
		Visitor string                 `json:"visitor"`
		Mine    map[string]int64       `json:"mine"`
		Tops    map[string][]topRecord `json:"tops"`
	}
	if err := json.Unmarshal([]byte(raw), &conf); err != nil {
		t.Fatalf("read the runtime config %q: %v", raw, err)
	}
	top := conf.Tops["coins"]
	if len(top) != 1 || top[0].ID != conf.Visitor || top[0].Name != "dave" || top[0].Amount != 5 {
		t.Fatalf("tops = %+v, want dave's own row with id %s", conf.Tops, conf.Visitor)
	}
	if conf.Mine["coins"] != 5 {
		t.Fatalf("runtime mine = %v", conf.Mine)
	}
}

type topRecord struct {
	Name   string `json:"name"`
	ID     string `json:"id"`
	Amount int64  `json:"amount"`
}

// Clearing a visitor key empties only that key, records one event turn the
// webmaster reads in the room log, and a second clear records nothing.
func TestClearHoldingsDeletesOneKeyAndRecordsAnEvent(t *testing.T) {
	h, queries := setup(t)
	h.NoStateCooldown()
	ctx := context.Background()
	declareState(t, queries, walletSchema)
	wantOp(t, h, "/x/state/coins?add=50", visitorA, "")
	wantOp(t, h, "/x/state/buy_rod", visitorA, "")
	wantOp(t, h, "/x/state/coins?add=5", visitorB, "")

	clear := func() {
		t.Helper()
		rec := postAdmin(t, func(w http.ResponseWriter, r *http.Request) {
			h.ClearHoldings(w, withURLParam(r, "key", "coins"))
		}, "/admin/state/coins/clear", url.Values{})
		if rec.Code != http.StatusOK {
			t.Fatalf("clear status = %d", rec.Code)
		}
	}
	clear()

	totals, err := queries.HoldingTotals(ctx)
	if err != nil {
		t.Fatalf("totals: %v", err)
	}
	if len(totals) != 1 || totals[0].Key != "rod" || totals[0].Total != 1 {
		t.Fatalf("totals = %+v, want only the rod left", totals)
	}
	// The cleared key's ops are gone too, so its hour budget starts over.
	wantOp(t, h, "/x/state/coins?add=100", visitorA, "")

	clear()
	log, err := queries.ListRoomLog(
		ctx,
		sqlc.ListRoomLogParams{ID: 0, ID_2: math.MaxInt64, Limit: 50},
	)
	if err != nil {
		t.Fatalf("room log: %v", err)
	}
	events := 0
	for _, row := range log {
		if row.Kind == webmaster.TurnState {
			events++
		}
	}
	if events != 2 {
		t.Fatalf("state event turns = %d, want one per clear that removed something", events)
	}
	if turns, err := queries.ListWebmasterTurns(ctx, 10); err != nil || len(turns) != 0 {
		t.Fatalf("webmaster turns = %d (%v), want none", len(turns), err)
	}

	// The data page lists the declared counters and a picked visitor's ops.
	page := getAdmin(t, h.AdminJournal, "/admin/journal?visitor="+visitorA)
	body := page.Body.String()
	if !strings.Contains(body, visitorA) || !strings.Contains(body, "buy_rod") {
		t.Fatalf("the data page is missing visitor A's ops")
	}
}

// A model turn declares visitor counters and a recipe through the real tool
// path, and the board can earn and trade with them right after.
func TestModelDeclaredWalletWorksEndToEnd(t *testing.T) {
	h, queries := setup(t)
	h.NoStateCooldown()
	ctx := context.Background()

	rec := postForm(t, h.CreatePost, "/posts", url.Values{"body": {"mockwallet"}})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("post status = %d", rec.Code)
	}
	waitFor(t, "the wallet page", func() bool {
		site, err := queries.CurrentSite(ctx)
		return err == nil && site.ID > 1
	})

	wantOp(t, h, "/x/state/buy_hat", visitorA, "short")
	wantOp(t, h, "/x/state/coins?add=10", visitorA, "")
	if got := wantOp(
		t,
		h,
		"/x/state/buy_hat",
		visitorA,
		"",
	); got.Mine["hat"] != 1 ||
		got.Mine["coins"] != 0 {
		t.Fatalf("after the trade = %v, want a hat and no gold", got.Mine)
	}
}

// A board request without a play session cannot own counts. Shared keys still
// work from a signed-in request, because they are not tied to an owner.
func TestStateWritesWithoutTheVisitorCookie(t *testing.T) {
	h, queries := setup(t)
	declareState(t, queries, walletSchema)

	rec := datastarPost(t, h.SetState, "/x/state/coins", url.Values{"add": {"5"}}, "")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("a visitor op without a play session: status = %d, want 401", rec.Code)
	}
	if rec := datastarPost(
		t,
		h.SetState,
		"/x/state/visits",
		nil,
		"dave",
	); rec.Code != http.StatusNoContent {
		t.Fatalf("the first shared write: status = %d, want 204", rec.Code)
	}
	if rec := datastarPost(
		t,
		h.SetState,
		"/x/state/visits",
		nil,
		"dave",
	); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("a second shared write at once: status = %d, want 429", rec.Code)
	}
	counts, err := h.TableCountsForTest(context.Background())
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if counts["state_holdings"] != 0 || counts["state_ops"] != 0 {
		t.Fatalf("cookieless writes stored %v", counts)
	}
}

// The rows of one trade share an op id, and ids never repeat, even after the
// op log is pruned empty.
func TestStateOpIDsGroupATradeAndNeverRepeat(t *testing.T) {
	h, queries := setup(t)
	h.NoStateCooldown()
	ctx := context.Background()
	declareState(t, queries, walletSchema)

	wantOp(t, h, "/x/state/coins?add=60", visitorA, "")
	wantOp(t, h, "/x/state/buy_rod", visitorA, "")
	ids, err := h.StateOpIDsForTest(ctx)
	if err != nil {
		t.Fatalf("op ids: %v", err)
	}
	if len(ids) != 3 || ids[1] != ids[2] || ids[0] == ids[1] {
		t.Fatalf("op ids = %v, want the add alone and the trade's two rows together", ids)
	}

	if err := h.ExecForTest(ctx, `DELETE FROM state_ops`); err != nil {
		t.Fatalf("prune: %v", err)
	}
	wantOp(t, h, "/x/state/coins?add=1", visitorA, "")
	later, err := h.StateOpIDsForTest(ctx)
	if err != nil {
		t.Fatalf("op ids: %v", err)
	}
	if len(later) != 1 || later[0] <= ids[2] {
		t.Fatalf("op id after the prune = %v, want past %d", later, ids[2])
	}
}

// GET /x/state is the shared half only: no visitor counters, no recipes.
func TestStateReadListsOnlySharedKeys(t *testing.T) {
	h, queries := setup(t)
	declareState(t, queries, walletSchema)

	rec := httptest.NewRecorder()
	h.State(rec, httptest.NewRequest(http.MethodGet, "/x/state", nil))
	var body struct {
		Schema map[string]json.RawMessage `json:"schema"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("read state: %v", err)
	}
	if len(body.Schema) != 1 || body.Schema["visits"] == nil {
		t.Fatalf("schema = %v, want only visits", body.Schema)
	}
}

// Clearing a key that only zero balances hold removes them but records no
// event: nobody lost anything.
func TestClearingZeroBalancesRecordsNoEvent(t *testing.T) {
	h, queries := setup(t)
	h.NoStateCooldown()
	ctx := context.Background()
	declareState(t, queries, walletSchema)
	wantOp(t, h, "/x/state/coins?add=5", visitorA, "")
	wantOp(t, h, "/x/state/coins?spend=5", visitorA, "")

	rec := postAdmin(t, func(w http.ResponseWriter, r *http.Request) {
		h.ClearHoldings(w, withURLParam(r, "key", "coins"))
	}, "/admin/state/coins/clear", url.Values{})
	if rec.Code != http.StatusOK {
		t.Fatalf("clear status = %d", rec.Code)
	}
	counts, err := h.TableCountsForTest(ctx)
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if counts["state_holdings"] != 0 {
		t.Fatalf("holdings left = %d", counts["state_holdings"])
	}
	log, err := queries.ListRoomLog(
		ctx,
		sqlc.ListRoomLogParams{ID: 0, ID_2: math.MaxInt64, Limit: 50},
	)
	if err != nil {
		t.Fatalf("room log: %v", err)
	}
	for _, row := range log {
		if row.Kind == webmaster.TurnState {
			t.Fatalf("a clear of zero balances recorded %q", row.Body)
		}
	}
}

// A page keeps its signals between pushes, so a key the new schema drops is
// sent as null, and an admin clear reaches the page without a reload.
func TestLiveStreamDropsKeysTheSchemaNoLongerDeclares(t *testing.T) {
	h, queries := setup(t)
	h.NoStateCooldown()
	declareState(t, queries, `{"coins":{"op":"counter","scope":"visitor"}}`)
	wallet := declareState(t, queries, walletSchema)
	plain := declareState(t, queries, `{"coins":{"op":"counter","scope":"visitor"}}`)
	restore := func(id int64) {
		t.Helper()
		rec := postAdminAt(t, h.RestoreVersion, "/admin/restore", strconv.FormatInt(id, 10))
		if rec.Code != http.StatusOK {
			t.Fatalf("restore %d: status = %d", id, rec.Code)
		}
	}
	restore(wallet.ID)
	wantOp(t, h, "/x/state/coins?add=5", visitorA, "")
	wantOp(t, h, "/x/state/coins?add=40", visitorB, "")
	wantOp(t, h, "/x/state/buy_rod", visitorB, "")

	site, err := queries.CurrentSite(context.Background())
	if err != nil {
		t.Fatalf("current site: %v", err)
	}
	signals, _ := json.Marshal(map[string]cursor{"_bd": {Version: site.ID}})
	live := openSSE(t, h.Live, "/live?datastar="+url.QueryEscape(string(signals)),
		func(next http.Handler) http.Handler {
			return handlers.AuthForTest(h, next, visitorA, "dave", false)
		},
		func(req *http.Request) *http.Request {
			req.Header.Set("Datastar-Request", "true")
			return req
		})
	live.next(t, []string{`"mine":{"coins":5,"rod":0}`})

	restore(plain.ID)
	live.next(t, []string{`"rod":null`})

	rec := postAdmin(t, func(w http.ResponseWriter, r *http.Request) {
		h.ClearHoldings(w, withURLParam(r, "key", "coins"))
	}, "/admin/state/coins/clear", url.Values{})
	if rec.Code != http.StatusOK {
		t.Fatalf("clear status = %d", rec.Code)
	}
	live.next(t, []string{`"mine":{"coins":0}`})
}
