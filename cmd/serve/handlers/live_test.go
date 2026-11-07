package handlers_test

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/Napam/beaverdream/cmd/serve/handlers"
	"github.com/Napam/beaverdream/db/generated/sqlc"
)

// datastarAccept is what a Datastar action sends. It includes text/html, so a
// handler that redirects on it would get the board patched into the page.
const datastarAccept = "text/event-stream, text/html, application/json"

type sseEvent struct {
	name string
	data string
}

type liveClient struct {
	events chan sseEvent
	seen   []sseEvent
}

type cursor struct {
	Version int64 `json:"version"`
	Post    int64 `json:"post"`
	Journal int64 `json:"journal"`
}

// openLive opens the board stream the way a page does: with its name cookie
// and its cursor in the datastar query parameter.
func openLive(t *testing.T, h *handlers.Handlers, at cursor) *liveClient {
	t.Helper()
	signals, err := json.Marshal(map[string]cursor{"_bd": at})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return openSSE(
		t,
		h.Live,
		"/live?datastar="+url.QueryEscape(string(signals)),
		func(next http.Handler) http.Handler {
			return handlers.AuthForTest(h, next, "dave", "dave", false)
		},
		func(req *http.Request) *http.Request {
			req.Header.Set("Datastar-Request", "true")
			return req
		},
	)
}

// openSSE opens any event stream in the test server. set adds the headers the
// caller needs.
func openSSE(
	t *testing.T,
	handler http.HandlerFunc,
	path string,
	wrap func(http.Handler) http.Handler,
	set func(*http.Request) *http.Request,
) *liveClient {
	t.Helper()
	var served http.Handler = handler
	if wrap != nil {
		served = wrap(served)
	}
	srv := httptest.NewServer(served)
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+path, nil)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if set != nil {
		req = set(req)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("open stream: %v", err)
	}
	t.Cleanup(func() {
		cancel()
		_ = resp.Body.Close()
	})
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("content type = %q, want an event stream", ct)
	}

	c := &liveClient{events: make(chan sseEvent, 256)}
	go func() {
		defer close(c.events)
		scanner := bufio.NewScanner(resp.Body)
		scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)
		var event sseEvent
		var data []string
		for scanner.Scan() {
			line := scanner.Text()
			switch {
			case line == "":
				if event.name != "" {
					event.data = strings.Join(data, "\n")
					c.events <- event
				}
				event, data = sseEvent{}, nil
			case strings.HasPrefix(line, "event: "):
				event.name = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: "):
				data = append(data, strings.TrimPrefix(line, "data: "))
			}
		}
	}()
	return c
}

// next waits for an event that has every fragment and none of the banned
// ones. Events it passes over stay in seen for the failure message.
func (c *liveClient) next(t *testing.T, want []string, banned ...string) sseEvent {
	t.Helper()
	deadline := time.After(10 * time.Second)
	for {
		select {
		case event, ok := <-c.events:
			if !ok {
				t.Fatalf("stream closed while waiting for %q; saw %+v", want, c.seen)
			}
			c.seen = append(c.seen, event)
			if hasAll(event.data, want) && !hasAny(event.data, banned) {
				return event
			}
		case <-deadline:
			t.Fatalf("timed out waiting for %q; saw %+v", want, c.seen)
		}
	}
}

// quiet fails if an event with fragment arrives within wait.
func (c *liveClient) quiet(t *testing.T, fragment string, wait time.Duration) {
	t.Helper()
	deadline := time.After(wait)
	for {
		select {
		case event, ok := <-c.events:
			if !ok {
				t.Fatalf("stream closed while checking for %q", fragment)
			}
			c.seen = append(c.seen, event)
			if strings.Contains(event.data, fragment) {
				t.Fatalf("unexpected %q in %+v", fragment, event)
			}
		case <-deadline:
			return
		}
	}
}

func hasAll(s string, parts []string) bool {
	for _, part := range parts {
		if !strings.Contains(s, part) {
			return false
		}
	}
	return true
}

func hasAny(s string, parts []string) bool {
	for _, part := range parts {
		if strings.Contains(s, part) {
			return true
		}
	}
	return false
}

// datastarPost sends a form the way the seed's data-on:submit does.
func datastarPost(
	t *testing.T,
	h http.HandlerFunc,
	path string,
	form url.Values,
	name string,
) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", datastarAccept)
	req.Header.Set("Datastar-Request", "true")
	if name != "" {
		req = withIdentity(req, name)
	}
	if key, ok := strings.CutPrefix(req.URL.Path, "/x/state/"); ok {
		req = withURLParam(req, "key", key)
	}
	rec := httptest.NewRecorder()
	h(rec, req)
	return rec
}

func currentVersion(t *testing.T, queries *sqlc.Queries) int64 {
	t.Helper()
	site, err := queries.CurrentSite(context.Background())
	if err != nil {
		t.Fatalf("current site: %v", err)
	}
	return site.ID
}

func cursorSignal(c cursor) string {
	return fmt.Sprintf(`"_bd":{"version":%d,"post":%d,"journal":%d}`, c.Version, c.Post, c.Journal)
}

// A post reaches every open page in place. The poster gets 204, not a
// redirect: the post arrives over the stream like everyone else's. The first
// post replaces the empty board; later ones are prepended alone.
func TestLiveStreamPushesPostsInPlace(t *testing.T) {
	h, queries := setup(t)
	version := currentVersion(t, queries)
	live := openLive(t, h, cursor{Version: version})
	live.next(t, []string{`"shared":`})

	rec := datastarPost(t, h.CreatePost, "/posts", url.Values{"body": {"first mocknoop"}}, "dave")
	if rec.Code != http.StatusNoContent {
		t.Fatalf("datastar post: status = %d, want 204", rec.Code)
	}
	live.next(t, []string{"selector #bd-posts", "mode inner", "first mocknoop"})
	live.next(t, []string{`bdLive.posts(1, "replace", [1])`})
	live.next(t, []string{cursorSignal(cursor{Version: version, Post: 1})})

	// Another name, so the rewrite cooldown does not matter.
	rec = datastarPost(t, h.CreatePost, "/posts", url.Values{"body": {"second mocknoop"}}, "erin")
	if rec.Code != http.StatusNoContent {
		t.Fatalf("second post: status = %d, want 204", rec.Code)
	}
	live.next(
		t,
		[]string{"selector #bd-posts", "mode prepend", "second mocknoop"},
		"first mocknoop",
	)
	// The webmaster turn may reserve its id before or after this post, so the
	// id itself varies.
	live.next(t, []string{`bdLive.posts(`, `"prepend", [`})
}

// A page that reconnects sends the cursor it holds and gets only what it
// lacks. A cursor past the newest post means a reset deleted posts, so the
// page gets the whole board again.
func TestLiveStreamResumesFromThePagesCursor(t *testing.T) {
	h, queries := setup(t)
	ctx := context.Background()
	version := currentVersion(t, queries)
	for _, body := range []string{"one", "two"} {
		if _, err := queries.CreatePost(
			ctx,
			sqlc.CreatePostParams{Author: "dave", Body: body},
		); err != nil {
			t.Fatalf("create post: %v", err)
		}
	}

	behind := openLive(t, h, cursor{Version: version, Post: 1})
	behind.next(t, []string{"mode prepend", ">two<"}, ">one<")

	ahead := openLive(t, h, cursor{Version: version, Post: 5})
	ahead.next(t, []string{"mode inner", ">two<", ">one<"})

	current := openLive(t, h, cursor{Version: version, Post: 2})
	current.quiet(t, "#bd-posts", 300*time.Millisecond)
}

// A page more than one batch behind gets the whole board instead of every
// missed post. The replacement is what the page would have been served.
func TestLiveStreamReplacesTheBoardWhenThePageIsFarBehind(t *testing.T) {
	h, queries := setup(t)
	ctx := context.Background()
	version := currentVersion(t, queries)
	for i := 1; i <= 201; i++ {
		if _, err := queries.CreatePost(ctx, sqlc.CreatePostParams{
			Author: "dave",
			Body:   fmt.Sprintf("body-%03d", i),
		}); err != nil {
			t.Fatalf("create post %d: %v", i, err)
		}
	}

	live := openLive(t, h, cursor{Version: version, Post: 1})
	live.next(
		t,
		[]string{"selector #bd-posts", "mode inner", "body-201", "body-002"},
		"body-001",
	)
}

// A feed that rewinds, because a reset deleted its rows, reads every started
// feed again instead of asking for rows that no longer exist.
func TestLiveStreamResyncsWhenTheJournalRewinds(t *testing.T) {
	h, queries := setup(t)
	ctx := context.Background()
	version := currentVersion(t, queries)
	row, err := queries.AppendJournal(ctx, sqlc.AppendJournalParams{
		Key:     "taps",
		Visitor: visitorA,
		Payload: `{"n":1}`,
		Match:   1,
		Rules:   1,
	})
	if err != nil {
		t.Fatalf("append journal: %v", err)
	}

	live := openLive(t, h, cursor{Version: version, Journal: row.ID + 5})
	live.next(t, []string{"bd._resync()"})
}

// A feed a whole batch behind reads again rather than carrying 500 rows in one
// script.
func TestLiveStreamResyncsAFeedMoreThanOneBatchBehind(t *testing.T) {
	h, queries := setup(t)
	ctx := context.Background()
	version := currentVersion(t, queries)
	for i := 1; i <= 500; i++ {
		if _, err := queries.AppendJournal(ctx, sqlc.AppendJournalParams{
			Key:     "taps",
			Visitor: visitorA,
			Payload: `{"n":1}`,
			Match:   1,
			Rules:   1,
		}); err != nil {
			t.Fatalf("append journal %d: %v", i, err)
		}
	}

	live := openLive(t, h, cursor{Version: version})
	live.next(t, []string{"bd._resync()"})
}

// The served page carries the newest journal id, so a reload does not push
// every row the page already shows.
func TestIndexPageCarriesTheJournalCursor(t *testing.T) {
	h, queries := setup(t)
	row, err := queries.AppendJournal(context.Background(), sqlc.AppendJournalParams{
		Key:     "taps",
		Visitor: visitorA,
		Payload: `{"n":1}`,
		Match:   1,
		Rules:   1,
	})
	if err != nil {
		t.Fatalf("append journal: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req = withIdentity(req, "dave")
	rec := httptest.NewRecorder()
	h.Index(rec, req)

	want := fmt.Sprintf(`&#34;journal&#34;:%d`, row.ID)
	if !strings.Contains(rec.Body.String(), want) {
		t.Fatalf("the page is missing %s", want)
	}
}

// A rewrite that only touches the stylesheet is patched into open pages. The
// spinner shows while it runs and hides when it lands.
func TestLiveStreamSwapsAStylesheetInPlace(t *testing.T) {
	h, queries := setup(t)
	version := currentVersion(t, queries)
	live := openLive(t, h, cursor{Version: version})
	live.next(t, []string{`"shared":`})

	postForm(t, h.CreatePost, "/posts", url.Values{"body": {"mockedit please"}})
	live.next(t, []string{`id="bd-busy"`, "display:block!important"})
	live.next(t, []string{`<style id="bd-css">`, "h1{color:#c00}"})
	live.next(t, []string{"bdLive.css("}, "bdLive.reload(")
	live.next(t, []string{`id="bd-busy"`, "display:none!important"})
}

// A markup change reloads open pages, and a page that connects with an old
// version reloads at once.
func TestLiveStreamReloadsOnAMarkupChange(t *testing.T) {
	h, queries := setup(t)
	version := currentVersion(t, queries)
	live := openLive(t, h, cursor{Version: version})
	live.next(t, []string{`"shared":`})

	postForm(t, h.CreatePost, "/posts", url.Values{"body": {"make it matrix"}})
	live.next(t, []string{"bdLive.reload("}, "bdLive.css(")

	late := openLive(t, h, cursor{Version: version})
	late.next(t, []string{"bdLive.reload("})
}

// A reload waits for the visitor to stop typing, so the page has not adopted
// the new version yet. The cursor must stay old; otherwise a stream that dies
// during the wait reconnects with the new version and never reloads.
func TestLiveStreamKeepsTheOldVersionWhileAReloadWaits(t *testing.T) {
	h, queries := setup(t)
	old := currentVersion(t, queries)
	live := openLive(t, h, cursor{Version: old})
	live.next(t, []string{`"shared":`})

	postForm(t, h.CreatePost, "/posts", url.Values{"body": {"make it matrix"}})

	// The post cursor patch and the rewrite's reload can arrive in either
	// order when the model beats the stream. Collect both.
	var signals struct {
		BD cursor `json:"_bd"`
	}
	var sawReload, sawCursor bool
	deadline := time.After(10 * time.Second)
	for !sawReload || !sawCursor {
		select {
		case event, ok := <-live.events:
			if !ok {
				t.Fatal("stream closed before the reload and the cursor")
			}
			if strings.Contains(event.data, "bdLive.reload(") {
				sawReload = true
			}
			if strings.Contains(event.data, `"_bd":`) {
				raw := strings.TrimSpace(strings.TrimPrefix(event.data, "signals"))
				if err := json.Unmarshal([]byte(raw), &signals); err != nil {
					t.Fatalf("decode cursor: %v: %s", err, event.data)
				}
				sawCursor = true
			}
		case <-deadline:
			t.Fatalf("timed out; reload=%v cursor=%v", sawReload, sawCursor)
		}
	}
	if signals.BD.Version != old {
		t.Fatalf("cursor version = %d, want %d until the page reloads", signals.BD.Version, old)
	}
	if signals.BD.Post != 1 {
		t.Fatalf("cursor post = %d, want 1", signals.BD.Post)
	}
}

// A page that sent no cursor adopts the current version instead of reloading,
// or it would come back without one and reload forever.
func TestLiveStreamAdoptsAMissingVersion(t *testing.T) {
	h, queries := setup(t)
	version := currentVersion(t, queries)
	live := openLive(t, h, cursor{})
	live.next(t, []string{cursorSignal(cursor{Version: version})})
	live.quiet(t, "bdLive.reload(", 300*time.Millisecond)
}

// Shared state is pushed as the $shared signal to every open page. The
// Datastar click that wrote it gets 204, not a redirect to the board.
func TestLiveStreamPushesSharedState(t *testing.T) {
	h, queries := setup(t)
	ctx := context.Background()
	seed, err := queries.CurrentSite(ctx)
	if err != nil {
		t.Fatalf("current site: %v", err)
	}
	site, err := queries.CreateSiteVersion(ctx, sqlc.CreateSiteVersionParams{
		Css:         seed.Css,
		Html:        seed.Html,
		StateSchema: `{"likes":{"op":"counter"},"poll":{"op":"choice","options":["yes","no"]}}`,
		Reason:      "test declares state",
	})
	if err != nil {
		t.Fatalf("declare state: %v", err)
	}

	live := openLive(t, h, cursor{Version: site.ID})
	live.next(t, []string{`"shared":{`, `"likes":0`})

	rec := datastarPost(t, h.SetState, "/x/state/likes", nil, "dave")
	if rec.Code != http.StatusNoContent {
		t.Fatalf("datastar state write: status = %d, want 204", rec.Code)
	}
	live.next(t, []string{`"shared":{`, `"likes":1`})
	live.next(t, []string{"bdLive.state(", `"likes":1`})

	rec = datastarPost(t, h.SetState, "/x/state/poll?value=yes", nil, "erin")
	if rec.Code != http.StatusNoContent {
		t.Fatalf("choice from the query string: status = %d, want 204", rec.Code)
	}
	live.next(t, []string{`"poll":{"yes":1}`})
}

// Journal rows reach open pages as they are written, so a feed needs no poll.
func TestLiveStreamPushesJournalRows(t *testing.T) {
	h, queries := setup(t)
	declareJournal(t, queries, `{"moves":{"max":10,"rules":1}}`)
	version := currentVersion(t, queries)

	live := openLive(t, h, cursor{Version: version})
	live.next(t, []string{`"shared":`})

	rec := appendJournal(t, h.JournalAppend, "moves", `{"cell":4}`, 0, visitorA)
	if rec.Code != http.StatusOK {
		t.Fatalf("append: status = %d, body %s", rec.Code, rec.Body.String())
	}
	live.next(t, []string{`bd._rows([{"id":1,"key":"moves"`, `"payload":{"cell":4}`})
	live.next(t, []string{cursorSignal(cursor{Version: version, Journal: 1})})

	// A page that reconnects with the cursor gets nothing twice.
	again := openLive(t, h, cursor{Version: version, Journal: 1})
	again.quiet(t, "bd._rows(", 300*time.Millisecond)
}

// The stream is for people with a name, like the board. 204 tells Datastar
// not to retry.
func TestLiveStreamNeedsAName(t *testing.T) {
	h, _ := setup(t)
	rec := httptest.NewRecorder()
	h.Live(rec, httptest.NewRequest(http.MethodGet, "/live", nil))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rec.Code)
	}
}

// A plain form post still works without Datastar: it is stored and sent back
// to the board.
func TestAPlainFormPostStillRedirects(t *testing.T) {
	h, _ := setup(t)
	rec := postForm(t, h.CreatePost, "/posts", url.Values{"body": {"plain mocknoop"}})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", rec.Code)
	}
}
