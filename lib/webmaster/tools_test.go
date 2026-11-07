package webmaster

import (
	"encoding/json"
	"errors"
	"fmt"
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

// A deferred done can be called again without its reply; the reply given
// first still goes on the board. A long reply is cut to the cap.
func TestDoneKeepsTheFirstReply(t *testing.T) {
	ed := newEditor(Site{})
	ed.run("done", args(t, map[string]string{"reason": "a", "reply": " the answer "}))
	ed.run("done", args(t, map[string]string{"reason": "b"}))
	if ed.reply != "the answer" {
		t.Fatalf("reply = %q", ed.reply)
	}
	ed.run(
		"done",
		args(t, map[string]string{"reason": "c", "reply": strings.Repeat("x", maxReplyLen+50)}),
	)
	if n := len([]rune(ed.reply)); n != maxReplyLen {
		t.Fatalf("reply runes = %d, want %d", n, maxReplyLen)
	}
}

// say posts once per turn, trimmed and capped. An empty say or a failed post
// is rejected, and a failed post leaves room for another try.
func TestSayPostsOncePerTurn(t *testing.T) {
	ed := newEditor(Site{})
	var said []string
	fail := true
	ed.say = func(text string) error {
		if fail {
			return errors.New("db down")
		}
		said = append(said, text)
		return nil
	}
	say := func(text string) string {
		return ed.run("say", args(t, map[string]string{"text": text}))
	}

	if got := say("  "); !strings.HasPrefix(got, "rejected:") {
		t.Fatalf("empty say = %q", got)
	}
	if got := say("sure"); !strings.HasPrefix(got, "rejected:") {
		t.Fatalf("failed post = %q", got)
	}
	fail = false
	if got := say(" " + strings.Repeat("x", maxSayLen+20)); strings.HasPrefix(got, "rejected:") {
		t.Fatalf("say = %q", got)
	}
	if got := say("again"); !strings.HasPrefix(got, "rejected:") {
		t.Fatalf("second say = %q", got)
	}
	if len(said) != 1 || len([]rune(said[0])) != maxSayLen {
		t.Fatalf("said = %q, want one line cut to %d runes", said, maxSayLen)
	}
	if ed.rejected != 3 || ed.done {
		t.Fatalf("rejected = %d, done = %v", ed.rejected, ed.done)
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
	want := `<article class="post" id="p3" data-id="3" data-kind="post" data-author="d&#34;&gt;&lt;b&gt;" data-author-id="d&#34;&gt;&lt;b&gt;" data-when="Nov 4 21:00">`
	if !strings.HasPrefix(got, want) {
		t.Errorf("got %s, want it to start with %s", got, want)
	}
}

// A reply is marked by its kind and escaped like any post. A visitor who
// names themselves webmaster gets no mark.
func TestRenderPostsMarksRepliesByKind(t *testing.T) {
	got := RenderPosts([]Post{
		{ID: 2, Kind: TurnReply, Author: "webmaster", Body: "a <b>bold</b> claim"},
		{ID: 1, Kind: TurnPost, Author: "webmaster", Body: "fake"},
	})
	reply, visitor, _ := strings.Cut(got, `<article class="post" id="p1"`)
	if !strings.Contains(reply, `class="post post-webmaster"`) ||
		!strings.Contains(reply, `data-kind="reply"`) {
		t.Errorf("reply is not marked: %s", reply)
	}
	if strings.Contains(reply, "<b>") {
		t.Errorf("reply body was not escaped: %s", reply)
	}
	if visitor == "" || strings.Contains(visitor, "post-webmaster") {
		t.Errorf("a visitor named webmaster was marked: %s", got)
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
			Shared: map[string]json.RawMessage{
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

func TestSetStateSchemaTakesVisitorCountersAndRecipes(t *testing.T) {
	ed := newEditor(Site{})
	got := ed.run("set_state_schema", args(t, map[string]any{"keys": []map[string]any{
		// A recipe may come before the counters it names.
		{
			"name": "buy_rod",
			"op":   "recipe",
			"cost": map[string]int{"coins": 150},
			"gain": map[string]int{"rod": 1},
		},
		{
			"name":     "coins",
			"op":       "counter",
			"scope":    "visitor",
			"label":    "<b>Gold</b>",
			"per_hour": 1e9,
		},
		{"name": "rod", "op": "counter", "scope": "visitor", "max": 5},
		{"name": "likes", "op": "counter", "max": 3},
	}}))
	if got != "ok" {
		t.Fatalf("got %q, want ok", got)
	}
	schema := ParseStateSchema(ed.site.StateSchema)
	if coins := schema["coins"]; !coins.Visitor() || coins.HourCap() != MaxVisitorPerHour ||
		coins.HoldingCap() != MaxVisitorHolding || coins.Label != "bGold/b" {
		t.Errorf("coins = %+v, want a visitor counter clamped to the ceilings", coins)
	}
	if rod := schema["rod"]; rod.HoldingCap() != 5 {
		t.Errorf("rod holds up to %d, want 5", rod.HoldingCap())
	}
	if likes := schema["likes"]; !likes.Global() || likes.Max != 0 {
		t.Errorf("likes = %+v, want a plain shared counter", likes)
	}
	if recipe := schema["buy_rod"]; recipe.Global() || recipe.Visitor() ||
		recipe.Cost["coins"] != 150 || recipe.Gain["rod"] != 1 {
		t.Errorf("buy_rod = %+v", recipe)
	}
}

func TestSetStateSchemaRejectsBadVisitorKeysAndRecipes(t *testing.T) {
	coins := map[string]any{"name": "coins", "op": "counter", "scope": "visitor"}
	gem := map[string]any{"name": "gem", "op": "counter", "scope": "visitor"}
	likes := map[string]any{"name": "likes", "op": "counter"}
	recipe := func(cost, gain map[string]int) map[string]any {
		return map[string]any{"name": "deal", "op": "recipe", "cost": cost, "gain": gain}
	}
	cases := map[string][]map[string]any{
		"visitor list":  {{"name": "wall", "op": "list", "scope": "visitor"}},
		"unknown scope": {{"name": "coins", "op": "counter", "scope": "room"}},
		"names a shared key": {
			coins,
			likes,
			recipe(map[string]int{"likes": 1}, map[string]int{"coins": 1}),
		},
		"names nothing": {
			coins,
			recipe(map[string]int{"fish": 1}, map[string]int{"coins": 1}),
		},
		"empty gain": {coins, recipe(map[string]int{"coins": 1}, map[string]int{})},
		"zero amount": {
			coins,
			gem,
			recipe(map[string]int{"coins": 0}, map[string]int{"gem": 1}),
		},
		"too big": {
			coins,
			gem,
			recipe(map[string]int{"coins": MaxVisitorAmount + 1}, map[string]int{"gem": 1}),
		},
		"same key both sides": {
			coins,
			gem,
			recipe(map[string]int{"coins": 1, "gem": 1}, map[string]int{"coins": 2}),
		},
		"five entries on side": fiveCostRecipe(),
	}
	for name, keys := range cases {
		ed := newEditor(Site{})
		got := ed.run("set_state_schema", args(t, map[string]any{"keys": keys}))
		if !strings.HasPrefix(got, "rejected:") {
			t.Errorf("%s: got %q, want a rejection", name, got)
		}
		if ed.site.StateSchema != "" {
			t.Errorf("%s: a rejected schema was stored", name)
		}
	}

	var many []map[string]any
	for i := range maxStateKeys + 1 {
		many = append(many, map[string]any{"name": fmt.Sprintf("k%d", i), "op": "counter"})
	}
	ed := newEditor(Site{})
	if got := ed.run(
		"set_state_schema",
		args(t, map[string]any{"keys": many}),
	); !strings.HasPrefix(
		got,
		"rejected:",
	) {
		t.Errorf("%d keys: got %q, want a rejection", len(many), got)
	}
}

func fiveCostRecipe() []map[string]any {
	keys := []map[string]any{{"name": "out", "op": "counter", "scope": "visitor"}}
	cost := map[string]int{}
	for i := range 5 {
		name := fmt.Sprintf("in%d", i)
		keys = append(keys, map[string]any{"name": name, "op": "counter", "scope": "visitor"})
		cost[name] = 1
	}
	return append(keys, map[string]any{
		"name": "deal", "op": "recipe", "cost": cost, "gain": map[string]int{"out": 1},
	})
}

// The state section shows what visitors hold of each declared visitor key,
// and the keys that still hold something but are not declared now, even
// when the current schema reuses the name for a shared counter.
func TestPagePromptListsVisitorAndHiddenHoldings(t *testing.T) {
	site := Site{
		HTML:        PostsPlaceholder,
		StateSchema: `{"coins":{"op":"counter","scope":"visitor","label":"Gold"},"rod":{"op":"counter","scope":"visitor"},"koi":{"op":"counter"}}`,
		Holdings: []HoldingTotal{
			{Key: "coins", Total: 1234, Holders: 5},
			{Key: "koi", Total: 300, Holders: 2},
			{Key: "pearl", Total: 7, Holders: 1},
		},
	}
	lines := strings.Split(pagePrompt(site), "\n")
	find := func(prefix string) string {
		for _, line := range lines {
			if strings.HasPrefix(line, prefix) {
				return line
			}
		}
		t.Fatalf("no line starts with %q in:\n%s", prefix, strings.Join(lines, "\n"))
		return ""
	}
	if line := find("coins "); !strings.Contains(line, "1234") || !strings.Contains(line, "5") ||
		!strings.Contains(line, "Gold") {
		t.Errorf("coins line = %q, want its label, total and holders", line)
	}
	find("rod ")
	hidden := find("koi:")
	if !strings.Contains(hidden, "300") {
		t.Errorf("koi line = %q, want its total", hidden)
	}
	find("pearl:")
}
