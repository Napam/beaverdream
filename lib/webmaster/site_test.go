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
		Site{Header: "<h1>beaverdream</h1>", Board: PostsPlaceholder},
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
	doc := SiteDoc(Site{Board: PostsPlaceholder}, nil)
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
		Board:         `<main>{{posts}}</main>`,
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
		`<main><div id="bd-posts" style="display:contents"><article class="post" id="p9">`,
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("the page is missing %q", want)
		}
	}
	// Nothing polls any more.
	if strings.Contains(doc, "setInterval") {
		t.Error("the code-owned page still polls")
	}

	hashed := SiteDoc(Site{Board: PostsPlaceholder, Datastar: "/static/datastar.abc.js"}, nil)
	if !strings.Contains(hashed, `src="/static/datastar.abc.js"`) {
		t.Error("the page ignores the hashed Datastar path")
	}
}

// A visitor can name themselves "{{posts}}". The placeholder is split before
// the name is substituted, so the name cannot mint a second post container.
func TestANameCannotAddAPostContainer(t *testing.T) {
	doc := SiteDoc(
		Site{Name: "{{posts}}", Board: "<main>{{name}}{{posts}}</main>"},
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
		Site{Name: "O'Brien", Board: PostsPlaceholder},
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
	doc := SiteDoc(Site{Version: 4, Board: PostsPlaceholder}, posts)
	if !strings.Contains(doc, `data-post="9"`) {
		t.Errorf("page does not carry the newest post id: %.200s", doc)
	}

	empty := SiteDoc(Site{Version: 4, Board: PostsPlaceholder}, nil)
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
