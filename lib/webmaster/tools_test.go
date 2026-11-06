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
// page are the point of the toy, so they must survive the editor.
func TestSetSiteKeepsScriptsAndHandlers(t *testing.T) {
	html := `<script>document.body.remove()</script>` +
		`<div onclick="alert(1)">click</div>` + PostsPlaceholder
	ed := newEditor(Site{HTML: "original " + PostsPlaceholder})
	got := ed.run("set_site", args(t, map[string]string{"html": html}))
	if got != "ok" {
		t.Fatalf("got %q, want ok", got)
	}
	if ed.site.HTML != html {
		t.Fatalf("html was altered: %q", ed.site.HTML)
	}
}

func TestSetSiteEnforcesThePageCap(t *testing.T) {
	ed := newEditor(Site{})
	got := ed.run("set_site", args(t, map[string]string{
		"html": PostsPlaceholder + strings.Repeat("x", maxPageLen),
	}))
	if !strings.HasPrefix(got, "rejected: page is ") || !strings.Contains(got, "remove at least") {
		t.Fatalf("got %q, want a page-size rejection with the overage", got)
	}
	if ed.dirty {
		t.Fatal("a rejected set_site marked the site dirty")
	}
}

// The old per-html cap was 192000 bytes. The page cap is larger and covers
// both fields, so a page that would not fit before now passes.
func TestSetSiteAcceptsAPageOverTheOldCap(t *testing.T) {
	ed := newEditor(Site{})
	got := ed.run("set_site", args(t, map[string]string{
		"html": PostsPlaceholder + strings.Repeat("x", 200_000),
	}))
	if got != "ok" {
		t.Fatalf("got %q, want ok", got)
	}
}

func TestPageCapCountsCSSAndHTMLTogether(t *testing.T) {
	ed := newEditor(Site{})
	got := ed.run("set_site", args(t, map[string]string{
		"css":  strings.Repeat("c", maxPageLen-100),
		"html": PostsPlaceholder + strings.Repeat("h", 200),
	}))
	if !strings.HasPrefix(got, "rejected: page is ") {
		t.Fatalf("got %q, want a page-size rejection", got)
	}
	if ed.site.CSS != "" || ed.site.HTML != "" {
		t.Fatalf("a rejected page changed the site: %+v", ed.site)
	}
}

// The placeholder is where the posts go. One rule covers every path that
// writes the html, so a whole rewrite cannot drop it either.
func TestHTMLWithoutPlaceholderIsRejected(t *testing.T) {
	ed := newEditor(Site{HTML: "<main>" + PostsPlaceholder + "</main>"})
	got := ed.run("set_site", args(t, map[string]string{"html": "<main>no posts</main>"}))
	if !strings.Contains(got, "{{posts}}") || !strings.HasPrefix(got, "rejected:") {
		t.Fatalf("got %q, want a placeholder rejection", got)
	}
	if ed.dirty || ed.site.HTML != "<main>"+PostsPlaceholder+"</main>" {
		t.Fatalf("html changed: %q", ed.site.HTML)
	}
}

// A second placeholder would make a second post list with the same ids, and
// the live stream patches only the first. Edits and whole writes both refuse it.
func TestHTMLWithTwoPlaceholdersIsRejected(t *testing.T) {
	page := "<main>" + PostsPlaceholder + "</main>"
	ed := newEditor(Site{HTML: page})
	got := ed.run("set_site", args(t, map[string]string{
		"html": page + "<!-- " + PostsPlaceholder + " -->",
	}))
	if !strings.HasPrefix(got, "rejected:") || !strings.Contains(got, "exactly once") {
		t.Fatalf("set_site: got %q, want an exactly-once rejection", got)
	}
	got = ed.run("edit", args(t, map[string]string{
		"target": "html", "old": "", "new": "<script>x='" + PostsPlaceholder + "'</script>",
	}))
	if !strings.HasPrefix(got, "rejected:") {
		t.Fatalf("edit: got %q, want a rejection", got)
	}
	if ed.dirty || ed.site.HTML != page {
		t.Fatalf("html changed: %q", ed.site.HTML)
	}
}

// The placeholder is the only structure: a page that is nothing else is fine.
func TestHTMLCanBeJustThePosts(t *testing.T) {
	ed := newEditor(Site{HTML: "<main>" + PostsPlaceholder + "</main>"})
	if got := ed.run(
		"set_site",
		args(t, map[string]string{"html": PostsPlaceholder}),
	); got != "ok" {
		t.Fatalf("got %q, want ok", got)
	}
}

func TestEditAppendsToAnEmptyStylesheet(t *testing.T) {
	ed := newEditor(Site{})
	got := ed.run("edit", args(t, map[string]string{
		"target": "css", "old": "", "new": "h1{color:red}",
	}))
	if got != "ok" || ed.site.CSS != "h1{color:red}" || !ed.dirty {
		t.Fatalf("got %q, css %q, dirty %v", got, ed.site.CSS, ed.dirty)
	}
}

func TestEditAppendsOnANewLine(t *testing.T) {
	ed := newEditor(Site{CSS: "body{margin:0}"})
	ed.run("edit", args(t, map[string]string{"target": "css", "old": "", "new": "h1{color:red}"}))
	if ed.site.CSS != "body{margin:0}\nh1{color:red}" {
		t.Fatalf("css = %q", ed.site.CSS)
	}
}

func TestEditReplacesAUniqueMatch(t *testing.T) {
	ed := newEditor(Site{HTML: PostsPlaceholder + `<footer class="colophon">beaverdream</footer>`})
	got := ed.run("edit", args(t, map[string]string{
		"target": "html", "old": ">beaverdream<", "new": ">beaver nightmare<",
	}))
	if got != "ok" {
		t.Fatalf("got %q, want ok", got)
	}
	if ed.site.HTML != PostsPlaceholder+`<footer class="colophon">beaver nightmare</footer>` {
		t.Fatalf("html = %q", ed.site.HTML)
	}
}

func TestEditRejectsMissingText(t *testing.T) {
	ed := newEditor(Site{CSS: "body{margin:0}"})
	got := ed.run("edit", args(t, map[string]string{
		"target": "css", "old": "h1{color:blue}", "new": "h1{color:red}",
	}))
	if !strings.HasPrefix(got, "rejected: old text not found in css") {
		t.Fatalf("got %q, want a not-found rejection", got)
	}
	if ed.dirty || ed.site.CSS != "body{margin:0}" || ed.rejected != 1 {
		t.Fatalf("css %q, dirty %v, rejected %d", ed.site.CSS, ed.dirty, ed.rejected)
	}
}

func TestEditRejectsAnAmbiguousMatch(t *testing.T) {
	ed := newEditor(Site{CSS: "a{color:red}b{color:red}c{color:red}"})
	got := ed.run("edit", args(t, map[string]string{
		"target": "css", "old": "color:red", "new": "color:blue",
	}))
	if !strings.Contains(got, "matches 3 places") {
		t.Fatalf("got %q, want the match count", got)
	}
	if ed.dirty {
		t.Fatal("an ambiguous edit marked the site dirty")
	}
}

// Models often copy a section with different indentation or line breaks.
func TestEditToleratesWhitespaceDrift(t *testing.T) {
	ed := newEditor(Site{CSS: "h1 {\n\tcolor: red;\n}\np { margin: 0 }"})
	got := ed.run("edit", args(t, map[string]string{
		"target": "css", "old": "h1 { color: red; }", "new": "h1 { color: blue; }",
	}))
	if got != "ok" {
		t.Fatalf("got %q, want ok", got)
	}
	if ed.site.CSS != "h1 { color: blue; }\np { margin: 0 }" {
		t.Fatalf("css = %q", ed.site.CSS)
	}
}

// A model that reformats a rule while copying it still hits, in both
// directions: spaces added around punctuation, and spaces removed.
func TestEditToleratesPunctuationSpacing(t *testing.T) {
	cases := []struct {
		name, field, old, want string
	}{
		{
			name:  "spaces added",
			field: "a{color:red}\nb{margin:0}",
			old:   "a { color: red }",
			want:  "a{color:blue}\nb{margin:0}",
		},
		{
			name:  "spaces removed",
			field: "h1 {\n  color: red;\n  margin: 0;\n}",
			old:   "h1{color:red;margin:0;}",
			want:  "a{color:blue}",
		},
		{
			name:  "space after a tag",
			field: "<h1>beaverdream</h1>\n<p class=\"tagline\">\n  a small board</p>",
			old:   `<p class="tagline">a small board</p>`,
			want:  "<h1>beaverdream</h1>\na{color:blue}",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, matches := applyEdit(tc.field, tc.old, "a{color:blue}")
			if matches != 1 {
				t.Fatalf("matches = %d, want 1", matches)
			}
			if got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// Loosening the match must not turn two places into one.
func TestEditPunctuationMatchStillRejectsAmbiguity(t *testing.T) {
	field := "a { color: red }\nb { margin: 0 }\na{color:red}"
	if _, matches := applyEdit(field, "a {color : red}", "x"); matches != 2 {
		t.Fatalf("matches = %d, want 2", matches)
	}

	// The whitespace step finds two places, so the looser step never runs.
	field = "a { color: red }\na  {  color:  red  }"
	if _, matches := applyEdit(field, "a { color: red }", "x"); matches != 1 {
		t.Fatalf("exact matches = %d, want 1", matches)
	}
	if _, matches := applyEdit(field, "a {\ncolor: red }", "x"); matches != 2 {
		t.Fatalf("whitespace matches = %d, want 2", matches)
	}
}

func TestEditCannotRemoveThePlaceholder(t *testing.T) {
	board := `<main><div class="thread">` + PostsPlaceholder + `</div></main>`
	ed := newEditor(Site{HTML: board})
	got := ed.run("edit", args(t, map[string]string{
		"target": "html", "old": PostsPlaceholder, "new": "",
	}))
	if !strings.HasPrefix(got, "rejected: html must contain {{posts}}") {
		t.Fatalf("got %q, want a placeholder rejection", got)
	}
	if ed.site.HTML != board {
		t.Fatalf("html changed: %q", ed.site.HTML)
	}
}

func TestEditEnforcesThePageCap(t *testing.T) {
	ed := newEditor(Site{
		CSS:  strings.Repeat("c", maxPageLen-10),
		HTML: PostsPlaceholder,
	})
	got := ed.run("edit", args(t, map[string]string{
		"target": "css", "old": "", "new": strings.Repeat("x", 20),
	}))
	if !strings.HasPrefix(got, "rejected: page is ") {
		t.Fatalf("got %q, want a page-size rejection", got)
	}
	if ed.dirty {
		t.Fatal("a rejected edit marked the site dirty")
	}
}

func TestEditsApplyInOrder(t *testing.T) {
	ed := newEditor(Site{HTML: "<h1>beaverdream</h1>" + PostsPlaceholder})
	ed.run("edit", args(t, map[string]string{
		"target": "html", "old": "beaverdream", "new": "beaver dream",
	}))
	got := ed.run("edit", args(t, map[string]string{
		"target": "html", "old": "beaver dream", "new": "BEAVER DREAM",
	}))
	if got != "ok" || ed.site.HTML != "<h1>BEAVER DREAM</h1>"+PostsPlaceholder {
		t.Fatalf("got %q, html %q", got, ed.site.HTML)
	}
}

func TestEditRejectsAnUnknownTarget(t *testing.T) {
	ed := newEditor(Site{})
	got := ed.run("edit", args(t, map[string]string{"target": "sidebar", "old": "", "new": "x"}))
	if !strings.Contains(got, "unknown target") {
		t.Fatalf("got %q, want an unknown-target rejection", got)
	}
}

func TestEditWithoutAChangeIsNotDirty(t *testing.T) {
	ed := newEditor(Site{CSS: "h1{color:red}"})
	got := ed.run("edit", args(t, map[string]string{
		"target": "css", "old": "color:red", "new": "color:red",
	}))
	if got != "ok (no change)" || ed.dirty {
		t.Fatalf("got %q, dirty %v", got, ed.dirty)
	}
}

// The stylesheet is served as-is too. A breakout is a legitimate (if rude)
// way for a visitor to change the page.
func TestSetSiteUpdatesOnlyProvidedFields(t *testing.T) {
	ed := newEditor(Site{CSS: "old css", HTML: "<main>old " + PostsPlaceholder + "</main>"})
	got := ed.run("set_site", args(t, map[string]any{"css": "body{color:red}"}))
	if got != "ok" {
		t.Fatalf("got %q, want ok", got)
	}
	if ed.site.CSS != "body{color:red}" {
		t.Errorf("provided fields were not applied: %+v", ed.site)
	}
	if ed.site.HTML != "<main>old "+PostsPlaceholder+"</main>" {
		t.Errorf("untouched fields changed: %+v", ed.site)
	}
}

// A rejected field rejects the whole call. A later successful call must not
// carry a stylesheet that slipped in before the rejection.
func TestSetSiteRejectsTheWholeCall(t *testing.T) {
	original := Site{
		CSS:  "body{margin:0}",
		HTML: "<h1>beaverdream</h1><main>" + PostsPlaceholder + "</main>",
	}
	ed := newEditor(original)
	got := ed.run("set_site", args(t, map[string]string{
		"css":  "body{background:red}",
		"html": "<main>no posts</main>",
	}))
	if !strings.HasPrefix(got, "rejected:") {
		t.Fatalf("got %q, want a rejection", got)
	}
	if ed.site.CSS != original.CSS || ed.site.HTML != original.HTML || ed.dirty {
		t.Fatalf("a rejected set_site changed the page: %+v, dirty %v", ed.site, ed.dirty)
	}
}

func TestSetSiteWithIdenticalContentIsNotDirty(t *testing.T) {
	ed := newEditor(Site{CSS: "body{margin:0}", HTML: "<main>" + PostsPlaceholder + "</main>"})
	got := ed.run("set_site", args(t, map[string]string{
		"css":  "  body{margin:0}\n",
		"html": "<main>" + PostsPlaceholder + "</main>",
	}))
	if got != "ok (no change)" || ed.dirty {
		t.Fatalf("got %q, dirty %v", got, ed.dirty)
	}
}

func TestSetSiteNeedsAField(t *testing.T) {
	ed := newEditor(Site{})
	got := ed.run("set_site", args(t, map[string]any{}))
	if !strings.HasPrefix(got, "rejected:") {
		t.Fatalf("got %q, want a rejection", got)
	}
}

func TestSetSiteKeepsCSSBreakoutsAndURLs(t *testing.T) {
	css := `body{background:url(https://example.com/x.png)}` +
		`</style><script>alert(1)</script>`
	ed := newEditor(Site{CSS: "original"})
	got := ed.run("set_site", args(t, map[string]string{"css": css}))
	if got != "ok" {
		t.Fatalf("got %q, want ok", got)
	}
	if ed.site.CSS != css {
		t.Fatalf("css was altered: %q", ed.site.CSS)
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
		Site{Version: 3, HTML: "<h1>hi</h1><main>" + PostsPlaceholder + "</main>"},
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
}

// The base stylesheet styles only code-owned markup. Layout and the form are
// the model's, so a rewrite does not start inside a column it has to undo.
func TestBaseCSSHasNoLayout(t *testing.T) {
	for _, layout := range []string{"max-width", ".postbox", "main{", "h1"} {
		if strings.Contains(BaseCSS, layout) {
			t.Errorf("base stylesheet carries layout %q", layout)
		}
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

// Page scripts read a post's fields from data attributes, not by scraping its
// spans. The author is visitor input, so the attribute is escaped too.
func TestRenderPostsCarriesDataAttributes(t *testing.T) {
	got := RenderPosts([]Post{{ID: 3, Author: `d"><b>`, When: "Nov 4 21:00", Body: "hi"}})
	want := `<article class="post" id="p3" data-id="3" data-author="d&#34;&gt;&lt;b&gt;" data-when="Nov 4 21:00">`
	if !strings.HasPrefix(got, want) {
		t.Errorf("got %s, want it to start with %s", got, want)
	}
}

func TestSiteDocHasInPlaceStyleTargets(t *testing.T) {
	doc := SiteDoc(
		Site{Version: 4, CSS: "body{background:#000}", HTML: PostsPlaceholder},
		nil,
	)
	for _, want := range []string{`id="bd-css"`, `data-version="4"`} {
		if !strings.Contains(doc, want) {
			t.Errorf("page is missing %q", want)
		}
	}
	if strings.Contains(doc, "beaver-note") {
		t.Error("the page still renders a note above the board")
	}
	if strings.Index(doc, "beaverdream:update") > strings.Index(doc, "</head>") {
		t.Error("live script is not in the head, where model markup cannot swallow it")
	}
}

func TestSiteDocSubstitutesTheVisitorName(t *testing.T) {
	doc := SiteDoc(
		Site{Name: `<b>dave</b>`, HTML: `<p>{{name}}</p>` + PostsPlaceholder},
		nil,
	)
	if !strings.Contains(doc, `&lt;b&gt;dave&lt;/b&gt;`) {
		t.Errorf("name was not escaped into the page: %s", doc)
	}
	if strings.Contains(doc, "{{name}}") {
		t.Error("name placeholder was not replaced")
	}
}

func TestSiteDocEmbedsSharedStateSafely(t *testing.T) {
	doc := SiteDoc(
		Site{
			HTML:        PostsPlaceholder,
			StateSchema: `{"votes":{"op":"counter"}}`,
			State: map[string]json.RawMessage{
				"votes": json.RawMessage(`{"html":"<b>x</b>"}`),
			},
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
		Site{HTML: "<main>no placeholder here</main>"},
		[]Post{{ID: 1, Body: "still here"}},
	)
	if !strings.Contains(doc, "still here") {
		t.Fatalf("post disappeared: %s", doc)
	}
}

func TestSiteDocEscapesPostBodies(t *testing.T) {
	doc := SiteDoc(Site{HTML: PostsPlaceholder}, []Post{{ID: 1, Body: `<script>x</script>`}})
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
			HTML:    `<script>document.title='owned'</script>` + PostsPlaceholder,
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

func TestDoneStoresNotes(t *testing.T) {
	ed := newEditor(Site{})
	got := ed.run("done", args(t, map[string]any{
		"reason": "not now.",
		"notes":  "dave promised to come back. kit owes me one.",
	}))
	if got != "ok" {
		t.Fatalf("got %q, want ok", got)
	}
	if ed.notes == nil || !strings.Contains(*ed.notes, "kit owes me one") {
		t.Fatalf("notes = %v", ed.notes)
	}
	if !ed.done || ed.dirty {
		t.Fatalf("done = %v, dirty = %v", ed.done, ed.dirty)
	}
}
