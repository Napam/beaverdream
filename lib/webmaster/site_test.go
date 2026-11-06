package webmaster

import (
	"encoding/json"
	"os"
	"regexp"
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

// The board and the name page carry the beaver icon, so the tab is not blank.
func TestThePagesCarryTheBeaverIcon(t *testing.T) {
	icon := `<link rel="icon" type="image/svg+xml" href="` + FaviconPath + `">`
	for name, doc := range map[string]string{
		"board": SiteDoc(Site{HTML: PostsPlaceholder}, nil),
		"name":  IdentityDoc(""),
	} {
		if !strings.Contains(doc, icon) {
			t.Errorf("the %s page is missing %s", name, icon)
		}
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
		State:         map[string]json.RawMessage{"likes": json.RawMessage("2")},
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
// when the board has posts it does not show.
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
		t.Errorf("empty board does not carry data-post=0: %.200s", empty)
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

// The system prompt names every export of the vendored addons bundle, so an
// addon added to the bundle is never hidden from the model. The bundle's
// header lists its entry file.
func TestSystemPromptListsTheThreeAddons(t *testing.T) {
	raw, err := os.ReadFile("../../web/vendor/three-addons-v0.187.0.min.js")
	if err != nil {
		t.Fatalf("read the addons bundle: %v", err)
	}
	header, _, _ := strings.Cut(string(raw), "*/")
	exports := regexp.MustCompile(`export (?:\* as (\w+)|\{([^}]*)\})`)
	var names []string
	for _, m := range exports.FindAllStringSubmatch(header, -1) {
		if m[1] != "" {
			names = append(names, m[1])
			continue
		}
		for _, name := range strings.Split(m[2], ",") {
			names = append(names, strings.TrimSpace(name))
		}
	}
	if len(names) < 10 {
		t.Fatalf("found %d exports in the bundle header: %v", len(names), names)
	}
	sys := systemPrompt()
	for _, name := range names {
		if !regexp.MustCompile(`\b` + name + `\b`).MatchString(sys) {
			t.Errorf("the system prompt does not name the addon %s", name)
		}
	}
}
