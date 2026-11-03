package webmaster

import (
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
// the live script shows it while a rewrite is queued or running.
func TestThePageShowsWhenTheSiteIsRewriting(t *testing.T) {
	doc := SiteDoc(Site{Board: PostsPlaceholder}, nil)
	for _, want := range []string{
		`id="bd-busy"`,
		`display:none!important`,
		`bd-busy-spin`,
		`setBusy(state)`,
		`state === 'running' || state === 'queued'`,
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("the page is missing %q", want)
		}
	}
	// The spinner must not eat clicks on the page beneath it.
	if !strings.Contains(doc, `pointer-events:none!important`) {
		t.Error("the busy spinner can swallow clicks")
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
