package webmaster

import (
	"encoding/json"
	"strings"
	"testing"
)

// A new version replaces the page in one frame. No fades, no eased color
// changes, no enter/leave classes.
func TestThePageSwitchesWithoutATransition(t *testing.T) {
	doc := SiteDoc(
		Site{HTML: "<h1>beaverdream</h1>" + PostsPlaceholder},
		nil,
	)
	for _, banned := range []string{
		"transition",
		"animation",
		"bd-leaving",
		"bd-entering",
		"bdFadeIn",
	} {
		if strings.Contains(stripBusy(doc), banned) {
			t.Errorf("the page contains %q; it should switch without a transition", banned)
		}
	}
}

// The page carries the beaver icon, so the tab is not blank.
func TestThePageCarriesTheBeaverIcon(t *testing.T) {
	icon := `<link rel="icon" type="image/svg+xml" href="` + FaviconPath + `">`
	doc := SiteDoc(Site{HTML: PostsPlaceholder}, nil)
	if !strings.Contains(doc, icon) {
		t.Errorf("the page is missing %s", icon)
	}
}

// stripBusy removes the code-owned spinner and its keyframes, so a test about
// page transitions can look at everything else the shell serves.
func stripBusy(doc string) string {
	doc = strings.Replace(doc, busyCSS, "", 1)
	const open = `<div id="bd-busy"`
	if start := strings.Index(doc, open); start >= 0 {
		if end := strings.Index(doc[start:], `</div>`); end >= 0 {
			doc = doc[:start] + doc[start+end+len(`</div>`):]
		}
	}
	return doc
}

// The busy spinner is the one animation the shell owns. It starts hidden and
// the live stream patches it while a rewrite is queued or running.
func TestThePageShowsWhenTheSiteIsRewriting(t *testing.T) {
	doc := SiteDoc(Site{HTML: PostsPlaceholder}, nil)
	for _, want := range []string{
		`id="bd-busy"`,
		`display:none!important`,
		`bd-busy-spin`,
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("the page is missing %q", want)
		}
	}
	if !strings.Contains(BusyIndicator(true), `display:block!important`) {
		t.Error("the busy spinner does not show when busy")
	}
	// The spinner must not eat clicks on the page beneath it.
	if !strings.Contains(doc, `pointer-events:none!important`) {
		t.Error("the busy spinner can swallow clicks")
	}
}

// Every page loads Datastar and opens one live stream, carrying what it
// already shows so the stream pushes only what it lacks.
func TestThePageOpensTheLiveStream(t *testing.T) {
	doc := SiteDoc(Site{
		Version:       4,
		HTML:          `<main>{{posts}}</main>`,
		StateSchema:   `{"likes":{"op":"counter"}}`,
		Shared:        map[string]json.RawMessage{"likes": json.RawMessage("2")},
		JournalCursor: 12,
	}, []Post{{ID: 9, Author: "a", Body: "hi"}})
	for _, want := range []string{
		`<script type="module" src="/static/datastar.js"></script>`,
		`id="bd-live"`,
		`data-init="@get(&#39;/live&#39;`,
		`retry: &#39;always&#39;`,
		`retryMaxCount: Infinity`,
		`filterSignals: {include: /^_bd\./`,
		`&#34;_bd&#34;:{&#34;version&#34;:4,&#34;post&#34;:9,&#34;journal&#34;:12}`,
		`&#34;shared&#34;:{&#34;likes&#34;:2}`,
		`<main><div id="bd-posts" style="display:contents"><article class="post" id="p9" `,
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("the page is missing %q", want)
		}
	}
	// Nothing polls any more.
	if strings.Contains(doc, "setInterval") {
		t.Error("the code-owned page still polls")
	}

	hashed := SiteDoc(Site{HTML: PostsPlaceholder, Datastar: "/static/datastar.abc.js"}, nil)
	if !strings.Contains(hashed, `src="/static/datastar.abc.js"`) {
		t.Error("the page ignores the hashed Datastar path")
	}
}

// A visitor can name themselves "{{posts}}". The placeholder is split before
// the name is substituted, so the name cannot mint a second post container.
func TestANameCannotAddAPostContainer(t *testing.T) {
	doc := SiteDoc(
		Site{Name: "{{posts}}", HTML: "<main>{{name}}{{posts}}</main>"},
		[]Post{{ID: 1, Author: "a", Body: "hi"}},
	)
	if n := strings.Count(doc, `id="bd-posts"`); n != 1 {
		t.Errorf("the page has %d post containers, want 1", n)
	}
}

// The viewer's name is on the document so model-written JavaScript can make
// the page personal without reading the HttpOnly cookie.
func TestThePageCarriesTheViewersName(t *testing.T) {
	doc := SiteDoc(
		Site{Name: "O'Brien", HTML: PostsPlaceholder},
		nil,
	)
	if !strings.Contains(doc, `data-name="O&#39;Brien"`) {
		t.Errorf("the page does not carry the escaped viewer name: %s", doc[:200])
	}
}

// data-post is the newest post id on the page, so the live script can tell
// when the page has posts it does not show.
func TestThePageCarriesTheNewestPostID(t *testing.T) {
	posts := []Post{
		{ID: 7, Author: "a", Body: "newest"},
		{ID: 9, Author: "b", Body: "out of order"},
		{ID: 3, Author: "c", Body: "oldest"},
	}
	doc := SiteDoc(Site{Version: 4, HTML: PostsPlaceholder}, posts)
	if !strings.Contains(doc, `data-post="9"`) {
		t.Errorf("page does not carry the newest post id: %.200s", doc)
	}

	empty := SiteDoc(Site{Version: 4, HTML: PostsPlaceholder}, nil)
	if !strings.Contains(empty, `data-post="0"`) {
		t.Errorf("empty page does not carry data-post=0: %.200s", empty)
	}
}

func TestACSSOnlySiteCarriesBothStamps(t *testing.T) {
	doc := SiteDoc(Site{Version: 5, CSS: "body{color:red}"}, []Post{{ID: 2}})
	for _, want := range []string{`data-version="5"`, `data-post="2"`} {
		if !strings.Contains(doc, want) {
			t.Errorf("page is missing %q", want)
		}
	}
}

// The import map gives model scripts "three" and "three/addons". It comes before
// every module script, or the browser ignores it.
func TestSiteDocMapsThree(t *testing.T) {
	doc := SiteDoc(Site{HTML: PostsPlaceholder}, nil)
	want := `<script type="importmap">{"imports":{"three":"/static/three.module.js",` +
		`"three/addons":"/static/three-addons.js"}}</script>`
	at := strings.Index(doc, want)
	if at < 0 {
		t.Fatalf("no import map for three in %.400q", doc)
	}
	if first := strings.Index(doc, `type="module"`); first < at {
		t.Error("a module script comes before the import map")
	}

	hashed := SiteDoc(Site{
		HTML:        PostsPlaceholder,
		Three:       "/static/three-abc.module.js",
		ThreeAddons: "/static/three-addons-def.js",
	}, nil)
	if !strings.Contains(
		hashed,
		`{"three":"/static/three-abc.module.js","three/addons":"/static/three-addons-def.js"}`,
	) {
		t.Error("the page ignores the hashed three.js paths")
	}
}

// journalConfigJSON reads the code-owned config tag the runtimes share.
func journalConfigJSON(t *testing.T, doc string) map[string]any {
	t.Helper()
	const open = `<script type="application/json" id="bd-journal">`
	start := strings.Index(doc, open)
	if start < 0 {
		t.Fatal("page is missing the journal config tag")
	}
	start += len(open)
	end := strings.Index(doc[start:], `</script>`)
	if end < 0 {
		t.Fatal("journal config tag is not closed")
	}
	var conf map[string]any
	if err := json.Unmarshal([]byte(doc[start:start+end]), &conf); err != nil {
		t.Fatalf("decode journal config: %v", err)
	}
	return conf
}

// The stream, reload and runtime routes live under the world's mount path.
func TestSiteDocUsesTheWorldBaseForRuntimeRoutes(t *testing.T) {
	doc := SiteDoc(Site{
		WorldID:   "w1",
		WorldSlug: "heroes",
		BasePath:  "/worlds/heroes/",
		HTML:      PostsPlaceholder,
	}, nil)
	for _, want := range []string{
		`data-base="/worlds/heroes"`,
		`data-world="w1"`,
		`data-world-slug="heroes"`,
		`data-init="@get(&#39;/worlds/heroes/live&#39;`,
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("the world page is missing %q", want)
		}
	}
	conf := journalConfigJSON(t, doc)
	if conf["base"] != "/worlds/heroes" {
		t.Errorf("runtime base = %v, want /worlds/heroes", conf["base"])
	}
	if conf["store"] != "bd:w1:" {
		t.Errorf("runtime store = %v, want bd:w1:", conf["store"])
	}
}

// A world keeps its saves under the immutable id, so reusing its slug cannot
// open another world's browser store.
func TestSiteDocNamespacesStorageByWorldID(t *testing.T) {
	withSlug := func(id string) map[string]any {
		return journalConfigJSON(t, SiteDoc(Site{
			WorldID:   id,
			WorldSlug: "same",
			BasePath:  "/worlds/same",
			HTML:      PostsPlaceholder,
		}, nil))
	}
	a := withSlug("id-a")
	b := withSlug("id-b")
	if a["store"] != "bd:id-a:" || b["store"] != "bd:id-b:" {
		t.Fatalf("stores = %v and %v, want each world id", a["store"], b["store"])
	}
	if a["store"] == b["store"] {
		t.Error("two worlds that share a slug share a browser store")
	}
}

// A page without a world keeps the root routes and storage keys tests use.
func TestSiteDocKeepsRootDefaults(t *testing.T) {
	doc := SiteDoc(Site{HTML: PostsPlaceholder}, nil)
	if !strings.Contains(doc, `data-init="@get(&#39;/live&#39;`) {
		t.Error("the root page does not open /live")
	}
	conf := journalConfigJSON(t, doc)
	if conf["base"] != "" || conf["store"] != "bd:" {
		t.Errorf("root runtime config = base %v store %v", conf["base"], conf["store"])
	}
	if !strings.Contains(doc, `<a href="/account">account</a>`) {
		t.Error("the root page does not link to the default account page")
	}
}

// The page's footer points at the app account. It never renders the old
// name form or its route.
func TestSiteDocLinksToTheAppAccount(t *testing.T) {
	doc := SiteDoc(Site{
		HTML:       PostsPlaceholder,
		AccountURL: `https://app.example/account?x=1&y=2`,
	}, nil)
	if !strings.Contains(doc, `<a href="https://app.example/account?x=1&amp;y=2">account</a>`) {
		t.Errorf("the account link is missing or unescaped: %s", doc)
	}
	if strings.Contains(doc, "/identity") || strings.Contains(doc, `name="name"`) {
		t.Error("the page still carries the old identity form")
	}
}
