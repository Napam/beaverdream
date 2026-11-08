package handlers_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Napam/beaverdream/cmd/serve/handlers"
	"github.com/Napam/beaverdream/db/generated/sqlc"
)

// openAdminLive opens the stream of one admin page, named by its query.
func openAdminLive(t *testing.T, h *handlers.Handlers, query string, token string) *liveClient {
	t.Helper()
	return openSSE(t, h.AdminLive, "/admin/live?"+query,
		func(next http.Handler) http.Handler {
			return handlers.AuthForTest(h, next, "admin", "admin", true)
		},
		func(req *http.Request) *http.Request { return req })
}

// Each page opens one stream for its own panels and polls nothing. The token
// rides in the stream's request headers, never in its URL.
func TestAdminPageOpensTheLiveStream(t *testing.T) {
	h, _ := setup(t)
	rec := getAdmin(t, h.AdminSite, "/admin/site")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{
		`id="admin-stream-site-0"`,
		`@get(&#39;/admin/worlds/test/live?page=site&#39;`,
		`requestCancellation: &#39;cleanup&#39;`,
		`id="admin-body"`,
		`sign out`,
		`/static/datastar.js`,
		`/admin/worlds/test/heal`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the admin page is missing %q", want)
		}
	}
	if strings.Contains(body, "setInterval") {
		t.Error("the admin page still polls")
	}
	if strings.Contains(body, "/admin/live?token=") {
		t.Error("the stream URL carries the token")
	}
	if strings.Contains(body, testAdminToken) {
		t.Error("the page carries the admin token")
	}
}

// The first sync renders the card and the panels, and a state write reaches
// the feeds panel over the stream.
func TestAdminLivePushesThePanel(t *testing.T) {
	h, queries := setup(t)
	ctx := context.Background()
	seed, err := queries.CurrentSite(ctx)
	if err != nil {
		t.Fatalf("current site: %v", err)
	}
	if _, err := queries.CreateSiteVersion(ctx, sqlc.CreateSiteVersionParams{
		Css:         seed.Css,
		Html:        seed.Html,
		StateSchema: `{"likes":{"op":"counter"}}`,
		Reason:      "test declares state",
	}); err != nil {
		t.Fatalf("declare state: %v", err)
	}

	live := openAdminLive(t, h, "page=journal", testAdminToken)
	live.next(t, []string{`id="admin-live"`, `waiting for a post`})
	live.next(t, []string{`id="admin-journal"`, `likes = 0`})

	rec := datastarPost(t, h.SetState, "/state/likes", nil, "dave")
	if rec.Code != http.StatusNoContent {
		t.Fatalf("state write: status = %d, want 204", rec.Code)
	}
	live.next(t, []string{`id="admin-journal"`, `likes = 1`})
}

// The stream is admin-only and answers 204 so Datastar stops retrying a page
// the browser cannot fix.
func TestAdminLiveNeedsAnAdminSession(t *testing.T) {
	h, _ := setup(t)
	rec := httptest.NewRecorder()
	h.AdminLive(rec, httptest.NewRequest(http.MethodGet, "/admin/live?page=site", nil))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rec.Code)
	}

	// A signed-in member is not an admin.
	member := httptest.NewRecorder()
	req := handlers.WithTestPrincipal(
		httptest.NewRequest(http.MethodGet, "/admin/live?page=site", nil),
		"m", "member", false, true,
	)
	h.AdminLive(member, req)
	if member.Code != http.StatusNoContent {
		t.Fatalf("member: status = %d, want 204", member.Code)
	}
}

// A visitor rewrite lands in the panel's history without a reload. The same
// stream patch carries the version and the turn that produced it.
func TestAdminLivePatchesHistoryAfterARewrite(t *testing.T) {
	h, _ := setup(t)
	live := openAdminLive(t, h, "page=changesets", testAdminToken)
	live.next(t, []string{`id="admin-live"`})

	postForm(t, h.CreatePost, "/posts", url.Values{"body": {"mockedit please"}})
	live.next(t, []string{`id="admin-history"`, "mockedit please"})
}

// A run that keeps the page writes no version, and its turn still reaches the
// panel's history.
func TestAdminLivePatchesHistoryAfterAKeptTurn(t *testing.T) {
	h, _ := setup(t)
	live := openAdminLive(t, h, "page=changesets", testAdminToken)
	live.next(t, []string{`id="admin-history"`, `nothing yet`})

	postForm(t, h.CreatePost, "/posts", url.Values{"body": {"mocknoop please"}})
	live.next(t, []string{`id="admin-history"`, "mocknoop please"})
}

// A finished run moves the overview's charts and budget, and the turns
// page's inspector, which shows the prompt section by section.
func TestAdminLivePatchesTheInspectorAndCharts(t *testing.T) {
	h, _ := setup(t)
	overview := openAdminLive(t, h, "page=overview", testAdminToken)
	overview.next(t, []string{`id="admin-metrics"`, "no finished changesets yet"})
	turns := openAdminLive(t, h, "page=changesets", testAdminToken)
	turns.next(t, []string{`id="admin-turn"`, "No changesets yet"})

	postForm(t, h.CreatePost, "/posts", url.Values{"body": {"mockedit charts please"}})
	overview.next(
		t,
		[]string{`id="admin-metrics"`, "tokens per changeset", "last 1 finished changeset"},
	)
	overview.next(t, []string{`id="admin-budget"`, "turns since the summary"})
	turns.next(t, []string{
		`id="admin-turn"`,
		"following the newest turn",
		"what the model read",
		"room log",
		"stylesheet",
		"new post",
		"mockedit charts please",
		"round 1",
	})
}

// A turn has its own page. The panel fetches it with the token in a header and
// gets the body patched; without the token the path asks for the password and
// posts back to itself. Only a changeset opens.
func TestAdminTurnPageShowsOneTurn(t *testing.T) {
	h, queries := setup(t)
	postAdmin(
		t,
		h.SendInstruction,
		"/admin/changeset",
		url.Values{"instruction": {"mockedit first"}},
	)
	postAdmin(
		t,
		h.SendInstruction,
		"/admin/changeset",
		url.Values{"instruction": {"mocknoop second"}},
	)
	turns, err := queries.ListChangesets(context.Background(), 10)
	if err != nil || len(turns) != 2 {
		t.Fatalf("turns = %d, err = %v", len(turns), err)
	}
	first := strconv.FormatInt(turns[1].ID, 10)

	open := func(id string, admin bool) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/admin/changesets/"+id, nil)
		req.Header.Set("Datastar-Request", "true")
		if admin {
			req = withAdmin(req)
		}
		rec := httptest.NewRecorder()
		h.AdminChangeset(rec, withURLParam(req, "id", id))
		return rec
	}

	rec := open(first, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	for _, want := range []string{
		"selector #admin-body",
		"showing turn #" + first,
		"follow the newest",
		"mockedit first",
		"what the model read",
		"/admin/worlds/test/live?page=changesets&amp;turn=" + first,
	} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Errorf("turn page is missing %q", want)
		}
	}

	anonymous := open(first, false)
	if anonymous.Code != http.StatusNotFound {
		t.Errorf("anonymous: status = %d, want 404", anonymous.Code)
	}

	created, err := queries.CreatePost(context.Background(), sqlc.CreatePostParams{
		Author: "dave",
		Body:   "just a post",
	})
	if err != nil {
		t.Fatalf("create post: %v", err)
	}
	post := strconv.FormatInt(created.ID, 10)
	if rec := open(post, true); rec.Code != http.StatusNotFound {
		t.Errorf("a non-changeset: status = %d, want 404", rec.Code)
	}
}

// An admin page needs an authenticated admin. A missing or non-admin
// principal gets 404, not the panel.
func TestAdminNeedsAnAdminPrincipal(t *testing.T) {
	h, _ := setup(t)
	for name, req := range map[string]*http.Request{
		"anonymous": httptest.NewRequest(http.MethodGet, "/admin", nil),
		"member":    handlers.WithTestPrincipal(httptest.NewRequest(http.MethodGet, "/admin", nil), "m", "member", false, true),
	} {
		rec := httptest.NewRecorder()
		h.Admin(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s: status = %d, want 404", name, rec.Code)
		}
	}
	rec := httptest.NewRecorder()
	h.Admin(rec, withAdmin(httptest.NewRequest(http.MethodGet, "/admin", nil)))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `id="admin-body"`) {
		t.Fatalf("admin status = %d", rec.Code)
	}
}

// A post waiting for the worker shows in the turns table as one queued row
// above the newest turn, because the worker answers a burst as one turn.
func TestAdminChangesetsShowsQueuedPosts(t *testing.T) {
	h, queries := setup(t)
	ctx := context.Background()

	// A stored turn, so the test can check the queued row sits above it.
	postForm(t, h.CreatePost, "/posts", url.Values{"body": {"mocknoop earlier"}})
	waitFor(t, "the earlier turn", func() bool {
		turns, err := queries.ListChangesets(ctx, 1)
		return err == nil && len(turns) == 1 && turns[0].Status != "running"
	})

	for _, post := range []sqlc.CreatePostParams{
		{Author: "dave", Body: "first queued"},
		{Author: "kit", Body: "second queued"},
	} {
		row, err := queries.CreatePost(ctx, post)
		if err != nil {
			t.Fatalf("create queued post: %v", err)
		}
		h.HoldPost(createdPostRow(row))
	}

	body := getAdmin(t, h.AdminChangesets, "/admin/changesets").Body.String()
	for _, want := range []string{
		"queued",
		">next<",
		"dave, kit",
		"first queued (+1 more)",
		"1 changeset · 2 queued, newest first",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the queued row is missing %q", want)
		}
	}
	next := strings.Index(body, ">next<")
	earlier := strings.Index(body, "mocknoop earlier")
	if next < 0 || earlier < 0 || next > earlier {
		t.Errorf("the queued row at %d is not above the stored turn at %d",
			next, earlier)
	}

	// The stream drops the row as soon as the worker drains the queue.
	live := openAdminLive(t, h, "page=changesets", testAdminToken)
	live.next(t, []string{`id="admin-history"`, ">next<"})
	go h.RunPending()
	live.next(t, []string{`id="admin-history"`}, ">next<")
	waitFor(t, "the queued batch to finish", func() bool {
		turns, err := queries.ListChangesets(ctx, 2)
		return err == nil && len(turns) == 2 && turns[0].Status != "running"
	})
}

// A turn in flight shows its finished rounds on the turns page before the
// conversation is stored, then the stored turn replaces them when it lands.
func TestAdminChangesetsShowsRoundsWhileRunning(t *testing.T) {
	var calls atomic.Int32
	release := make(chan struct{})
	var once sync.Once
	stop := func() { once.Do(func() { close(release) }) }
	h, queries := setupWithModel(
		t,
		handlers.Config{},
		func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if calls.Add(1) >= 2 {
					<-release
				}
				next.ServeHTTP(w, r)
			})
		},
	)
	// After the server's cleanup, so a fatal before stop cannot leave a held
	// call blocking httptest.Server.Close.
	t.Cleanup(stop)

	postForm(t, h.CreatePost, "/posts", url.Values{"body": {"mockbadedit show some progress"}})

	// The first round is rejected and its done deferred, so the worker asks
	// again. That second call is held here, with round one already recorded.
	var body string
	waitFor(t, "the running turn's first round", func() bool {
		if calls.Load() < 2 {
			return false
		}
		body = getAdmin(t, h.AdminChangesets, "/admin/changesets").Body.String()
		return strings.Contains(body, "in flight")
	})
	for _, want := range []string{
		"in flight",
		"round 2 with the model",
		"waiting for the model",
		`id="admin-draft"`,
		"what the model read",
		"mockbadedit show some progress",
		"round 1",
		"rejected",
		"old text not found",
		// The finished round shows its whole call, arguments included.
		"no such text",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the running inspector is missing %q", want)
		}
	}

	stop()
	waitFor(t, "the turn to finish", func() bool {
		turns, err := queries.ListChangesets(context.Background(), 1)
		return err == nil && len(turns) == 1 && turns[0].Status != "running"
	})
	body = getAdmin(t, h.AdminChangesets, "/admin/changesets").Body.String()
	if strings.Contains(body, "in flight") || strings.Contains(body, `id="admin-draft"`) {
		t.Error("the finished inspector still shows the live view")
	}
	if !strings.Contains(body, "what the model read") {
		t.Error("the finished inspector did not replace the live rounds")
	}
	// The stored turn has two rounds. Folding the live ones in as well would
	// double the count while the finish transaction and endRun overlap.
	if !strings.Contains(body, "2 rounds") || strings.Contains(body, "4 rounds") {
		t.Error("the finished turn counted its rounds twice")
	}
}

// The turns stream carries a round as soon as it finishes, then the stored
// turn when the run lands, so the page never waits for the whole conversation.
func TestAdminLivePushesARunningRound(t *testing.T) {
	var calls atomic.Int32
	release := make(chan struct{})
	var once sync.Once
	stop := func() { once.Do(func() { close(release) }) }
	h, _ := setupWithModel(
		t,
		handlers.Config{},
		func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if calls.Add(1) >= 2 {
					<-release
				}
				next.ServeHTTP(w, r)
			})
		},
	)
	// After the server's cleanup, so a fatal before stop cannot leave a held
	// call blocking httptest.Server.Close.
	t.Cleanup(stop)
	live := openAdminLive(t, h, "page=changesets", testAdminToken)
	live.next(t, []string{`id="admin-turn"`, "No changesets yet"})

	postForm(t, h.CreatePost, "/posts", url.Values{"body": {"mockbadedit stream progress"}})
	live.next(t, []string{
		`id="admin-turn"`,
		"in flight",
		"round 1",
		"rejected",
		"old text not found",
	})

	stop()
	live.next(t, []string{
		`id="admin-turn"`,
		"what the model read",
		"mockbadedit stream progress",
	})
}

// heldEdit answers the first model call with a stream frozen halfway through
// an edit until stop is called; every later call passes through to the mock.
// The caller registers stop as cleanup after setup, so a held call can never
// block the test server's close.
func heldEdit(t *testing.T) (func(http.Handler) http.Handler, func()) {
	t.Helper()
	release := make(chan struct{})
	var once sync.Once
	stop := func() { once.Do(func() { close(release) }) }
	var calls atomic.Int32
	wrap := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if calls.Add(1) > 1 {
				next.ServeHTTP(w, r)
				return
			}
			w.Header().Set("Content-Type", "text/event-stream")
			flusher := w.(http.Flusher)
			chunk := func(delta string) {
				_, _ = io.WriteString(w, `data: {"id":"x","object":"chat.completion.chunk",`+
					`"model":"m","choices":[{"index":0,"delta":`+delta+`}]}`+"\n\n")
				flusher.Flush()
			}
			chunk(`{"role":"assistant","reasoning_content":"pondering the heading"}`)
			chunk(`{"tool_calls":[{"index":0,"id":"c1","type":"function",` +
				`"function":{"name":"edit","arguments":"{\"target\":\"css\",\"old\":\"\",\"new\":\"h1{color:"}}]}`)
			<-release
			chunk(`{"tool_calls":[{"index":0,"function":{"arguments":"red}\"}"}}]}`)
			_, _ = io.WriteString(
				w,
				`data: {"id":"x","object":"chat.completion.chunk","model":"m",`+
					`"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`+"\n\n",
			)
			_, _ = io.WriteString(w, "data: [DONE]\n\n")
		})
	}
	return wrap, stop
}

// waitForRunningTurn waits for the worker to reserve a turn, then returns it.
// The reservation lands before the prompt is built, so the row exists while
// the model call is still held.
func waitForRunningTurn(t *testing.T, queries *sqlc.Queries) int64 {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		turns, err := queries.ListChangesets(context.Background(), 5)
		if err != nil {
			t.Fatalf("list turns: %v", err)
		}
		for _, run := range turns {
			if run.Status == "running" {
				return run.ID
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("no turn started running")
	return 0
}

// The round being written reaches the turns page while it streams: its
// reasoning, the call it is writing and the arguments as far as they came,
// in the draft element alone. The stored turn replaces it when it lands.
func TestAdminLiveStreamsTheDraft(t *testing.T) {
	wrap, stop := heldEdit(t)
	h, _ := setupWithModel(t, handlers.Config{}, wrap)
	t.Cleanup(stop)
	live := openAdminLive(t, h, "page=changesets", testAdminToken)
	live.next(t, []string{`id="admin-turn"`, "No changesets yet"})

	postForm(t, h.CreatePost, "/posts", url.Values{"body": {"paint the heading"}})
	// Each sync sends the status card before the draft.
	live.next(t, []string{`id="admin-live"`, "writing edit"})
	// The draft gets its own patch. A slow stream can also fold it into a
	// whole inspector render first, so the selector is what is checked.
	live.next(t, []string{
		"selector #admin-draft",
		"writing edit",
		"pondering the heading",
		"h1{color:",
	}, "what the model read")

	stop()
	live.next(t, []string{`id="admin-turn"`, "what the model read", "h1{color:red}"}, "in flight")
}

// A turn opened by its id is live like the newest one: the inspector shows it
// in flight, and the page gets one more patch with the stored conversation
// when the turn lands.
func TestAdminLiveHoldsARunningTurn(t *testing.T) {
	wrap, stop := heldEdit(t)
	h, queries := setupWithModel(t, handlers.Config{}, wrap)
	t.Cleanup(stop)

	postForm(t, h.CreatePost, "/posts", url.Values{"body": {"paint the heading"}})
	id := waitForRunningTurn(t, queries)
	// live=1 is what the page sends when it rendered the turn in flight.
	live := openAdminLive(
		t,
		h,
		"page=changesets&turn="+strconv.FormatInt(id, 10)+"&live=1",
		testAdminToken,
	)
	live.next(t, []string{`id="admin-turn"`, "in flight"})

	stop()
	live.next(t, []string{`id="admin-turn"`, "h1{color:red}"}, "in flight")
	live.quiet(t, "in flight", 200*time.Millisecond)
}

// An admin instruction waiting behind a running turn shows as its own queued
// row, and the form sends on Enter and clears the field.
func TestAdminChangesetsShowsAWaitingInstruction(t *testing.T) {
	h, _ := setup(t)
	h.HoldInstruction("make it pink")

	body := getAdmin(t, h.AdminChangesets, "/admin/changesets").Body.String()
	for _, want := range []string{">next<", "make it pink", "1 queued"} {
		if !strings.Contains(body, want) {
			t.Errorf("the waiting instruction is missing %q", want)
		}
	}

	overview := getAdmin(t, h.Admin, "/admin").Body.String()
	for _, want := range []string{"requestSubmit()", "elements.instruction.value"} {
		if !strings.Contains(overview, want) {
			t.Errorf("the instruction form is missing %q", want)
		}
	}
}
