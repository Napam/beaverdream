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
	if !strings.Contains(doc, `action="/posts"`) || !strings.Contains(doc, `href="/admin"`) {
		t.Fatal("page does not carry the post form and admin link")
	}
	if !strings.Contains(doc, `.beaver-controls{position:fixed`) {
		t.Fatal("post form is not pinned to the viewport")
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
