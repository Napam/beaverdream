package webmaster

import (
	"encoding/json"
	"strings"
	"testing"
)

func args(t *testing.T, v any) string {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}
	return string(data)
}

// The page is intentionally unsandboxed. Scripts and event handlers in a
// region are the point of the toy, so they must survive the editor.
func TestSetRegionKeepsScriptsAndHandlers(t *testing.T) {
	html := `<script>document.body.remove()</script>` +
		`<div onclick="alert(1)">click</div>`
	ed := newEditor(Site{Header: "original"})
	got := ed.run("set_region", args(t, map[string]string{
		"region": RegionHeader,
		"html":   html,
	}))
	if got != "ok" {
		t.Fatalf("got %q, want ok", got)
	}
	if ed.site.Header != html {
		t.Fatalf("region was altered: %q", ed.site.Header)
	}
}

func TestSetRegionRejectsUnknownRegion(t *testing.T) {
	ed := newEditor(Site{})
	got := ed.run("set_region", args(t, map[string]string{
		"region": "sidebar",
		"html":   "<p>hello there</p>",
	}))
	if !strings.Contains(got, "unknown region") {
		t.Fatalf("got %q, want an unknown-region rejection", got)
	}
}

func TestSetRegionEnforcesLength(t *testing.T) {
	ed := newEditor(Site{})
	got := ed.run("set_region", args(t, map[string]string{
		"region": RegionBoard,
		"html":   strings.Repeat("x", maxRegionLen+1),
	}))
	if !strings.HasPrefix(got, "rejected:") {
		t.Fatalf("got %q, want a rejection", got)
	}
}

// The stylesheet is served as-is too. A breakout is a legitimate (if rude)
// way for a visitor to change the page.
func TestSetCSSKeepsBreakoutsAndURLs(t *testing.T) {
	css := `body{background:url(https://example.com/x.png)}` +
		`</style><script>alert(1)</script>`
	ed := newEditor(Site{CSS: "original"})
	got := ed.run("set_css", args(t, map[string]string{"css": css}))
	if got != "ok" {
		t.Fatalf("got %q, want ok", got)
	}
	if ed.site.CSS != css {
		t.Fatalf("css was altered: %q", ed.site.CSS)
	}
}

func TestSetNoteTruncates(t *testing.T) {
	ed := newEditor(Site{})
	ed.run("set_note", args(t, map[string]string{"text": strings.Repeat("a", maxNoteLen+50)}))
	if got := len([]rune(ed.site.Note)); got != maxNoteLen {
		t.Fatalf("note length = %d, want %d", got, maxNoteLen)
	}
}

func TestSetStateSchemaAcceptsAValidSchema(t *testing.T) {
	ed := newEditor(Site{})
	got := ed.run("set_state_schema", args(t, map[string]any{"keys": []map[string]any{
		{"name": "votes_pink", "op": "counter"},
		{"name": "vibes", "op": "list", "max": 5},
		{"name": "weather", "op": "choice", "options": []string{"rain", "sun"}},
	}}))
	if got != "ok" {
		t.Fatalf("got %q, want ok", got)
	}
	for _, want := range []string{
		`"votes_pink":{"op":"counter"}`,
		`"vibes":{"op":"list","max":5}`,
		`"weather":{"op":"choice","options":["rain","sun"]}`,
	} {
		if !strings.Contains(ed.site.StateSchema, want) {
			t.Errorf("schema is missing %q: %s", want, ed.site.StateSchema)
		}
	}
}

func TestSetStateSchemaRejectsBadKeys(t *testing.T) {
	cases := []map[string]any{
		{"name": "Bad Name", "op": "counter"},
		{"name": "votes", "op": "delete"},
		{"name": "weather", "op": "choice", "options": []string{"rain"}},
	}
	for _, key := range cases {
		ed := newEditor(Site{})
		got := ed.run("set_state_schema", args(t, map[string]any{"keys": []map[string]any{key}}))
		if !strings.HasPrefix(got, "rejected:") {
			t.Errorf("key %v: got %q, want a rejection", key, got)
		}
	}
}

func TestDoneStopsAndKeepsReason(t *testing.T) {
	ed := newEditor(Site{})
	ed.run("done", args(t, map[string]string{"reason": "  left it alone  "}))
	if !ed.done {
		t.Fatal("done was not recorded")
	}
	if ed.reason != "left it alone" {
		t.Fatalf("reason = %q", ed.reason)
	}
}

func TestSiteDocSubstitutesPosts(t *testing.T) {
	doc := SiteDoc(
		Site{Version: 3, Header: "<h1>hi</h1>", Board: "<main>" + PostsPlaceholder + "</main>"},
		[]Post{{ID: 7, Author: "dave", Body: "hello", When: "today"}},
	)
	if !strings.Contains(doc, "hello") || !strings.Contains(doc, `id="p7"`) {
		t.Fatalf("doc is missing the post: %s", doc)
	}
	if strings.Contains(doc, PostsPlaceholder) {
		t.Fatal("placeholder was not replaced")
	}
	if !strings.Contains(doc, `data-version="3"`) {
		t.Fatal("page does not carry the version for live reload")
	}
	if strings.Contains(doc, "<form") {
		t.Fatal("server injected a post form")
	}
	if strings.Contains(doc, "beaver-controls") {
		t.Fatal("server injected the old composer")
	}
	if !strings.Contains(doc, ".postbox{") {
		t.Fatal("base stylesheet is missing the post form styles")
	}
}

func TestRenderPostsAddsBoardConventions(t *testing.T) {
	got := RenderPosts([]Post{{
		ID:     3,
		Author: "dave",
		When:   "now",
		Body:   ">>1\n>still flooded\n<script>x</script>",
	}})
	if !strings.Contains(got, `<a class="post-ref" href="#p1">&gt;&gt;1</a>`) {
		t.Errorf("missing a quote link: %s", got)
	}
	if !strings.Contains(got, `<span class="quote">&gt;still flooded</span>`) {
		t.Errorf("missing greentext: %s", got)
	}
	if !strings.Contains(got, "No.3") {
		t.Errorf("missing the post number: %s", got)
	}
	if strings.Contains(got, "<script>x</script>") {
		t.Errorf("post body was not escaped: %s", got)
	}
}

func TestSiteDocHasInPlaceStyleTargets(t *testing.T) {
	doc := SiteDoc(
		Site{Version: 4, CSS: "body{background:#000}", Note: "changed", Board: PostsPlaceholder},
		nil,
	)
	for _, want := range []string{`id="bd-css"`, `id="bd-note"`, `data-version="4"`} {
		if !strings.Contains(doc, want) {
			t.Errorf("page is missing %q", want)
		}
	}
	if strings.Index(doc, "beaverdream:update") > strings.Index(doc, "</head>") {
		t.Error("live script is not in the head, where regions cannot swallow it")
	}
}

func TestSiteDocEmbedsSharedStateSafely(t *testing.T) {
	doc := SiteDoc(
		Site{
			Board:       PostsPlaceholder,
			StateSchema: `{"votes":{"op":"counter"}}`,
			State:       map[string]string{"votes": `{"html":"<b>x</b>"}`},
		},
		nil,
	)
	if !strings.Contains(doc, `id="bd-state"`) {
		t.Fatal("shared state tag is missing")
	}
	if strings.Contains(doc, "<b>x</b>") {
		t.Fatal("shared state was embedded as raw HTML")
	}
	if !strings.Contains(doc, `\u003cb\u003e`) {
		t.Errorf("state is not JSON-escaped: %s", doc)
	}
}

func TestSiteDocKeepsPostsWhenPlaceholderMissing(t *testing.T) {
	doc := SiteDoc(
		Site{Board: "<main>no placeholder here</main>"},
		[]Post{{ID: 1, Body: "still here"}},
	)
	if !strings.Contains(doc, "still here") {
		t.Fatalf("post disappeared: %s", doc)
	}
}

func TestSiteDocEscapesPostBodies(t *testing.T) {
	doc := SiteDoc(Site{Board: PostsPlaceholder}, []Post{{ID: 1, Body: `<script>x</script>`}})
	if strings.Contains(doc, "<script>x</script>") {
		t.Fatalf("post body was not escaped: %s", doc)
	}
	if !strings.Contains(doc, "&lt;script&gt;") {
		t.Fatalf("post body is missing entirely: %s", doc)
	}
}

func TestSiteDocServesModelMarkupInline(t *testing.T) {
	doc := SiteDoc(
		Site{
			Version: 1,
			CSS:     `body{background:#000}`,
			Header:  `<script>document.title='owned'</script>`,
			Board:   PostsPlaceholder,
		},
		nil,
	)
	if !strings.Contains(doc, `<script>document.title='owned'</script>`) {
		t.Fatalf("model script was stripped: %s", doc)
	}
	if strings.Contains(doc, "<iframe") {
		t.Fatal("page is still wrapped in an iframe")
	}
}
