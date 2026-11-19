package pages

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Napam/beaverdream/lib/webmaster"
	"golang.org/x/net/html"
)

// renderLobby returns the parsed page and every world card in document order.
func renderLobby(t *testing.T, view AppView) (*html.Node, []*html.Node) {
	t.Helper()
	var out bytes.Buffer
	if err := AppLobby(view).Render(context.Background(), &out); err != nil {
		t.Fatalf("render lobby: %v", err)
	}
	doc, err := html.Parse(&out)
	if err != nil {
		t.Fatalf("parse lobby: %v", err)
	}
	var cards []*html.Node
	walkHTML(doc, func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "article" && htmlAttr(n, "data-health") != "" {
			cards = append(cards, n)
		}
	})
	return doc, cards
}

func walkHTML(n *html.Node, visit func(*html.Node)) {
	visit(n)
	for child := n.FirstChild; child != nil; child = child.NextSibling {
		walkHTML(child, visit)
	}
}

func htmlAttr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}

// findHTML returns the first element that matches, or nil.
func findHTML(n *html.Node, match func(*html.Node) bool) *html.Node {
	var found *html.Node
	walkHTML(n, func(node *html.Node) {
		if found == nil && node.Type == html.ElementNode && match(node) {
			found = node
		}
	})
	return found
}

func hasClass(n *html.Node, class string) bool {
	for _, word := range strings.Fields(htmlAttr(n, "class")) {
		if word == class {
			return true
		}
	}
	return false
}

// byAttr matches one element attribute by value.
func byAttr(key, value string) func(*html.Node) bool {
	return func(n *html.Node) bool { return htmlAttr(n, key) == value }
}

// formTo matches the form that posts to an action.
func formTo(action string) func(*html.Node) bool {
	return func(n *html.Node) bool {
		return n.Data == "form" && htmlAttr(n, "action") == action
	}
}

func hasPageSize(n *html.Node) bool { return htmlAttr(n, "data-page-size") != "" }

// The member lobby shows the same health a reader needs at a glance: a failed
// card before an alerts card, each with its own enter form, and no page-size
// bar for a member.
func TestLobbyGalleryShowsHealthInOrder(t *testing.T) {
	now := time.Now()
	view := AppView{
		Name: "Ada",
		CSRF: "csrf-1",
		Worlds: []AppWorld{
			{
				Slug:        "broken",
				HealthKnown: true,
				LastTurnAt:  now.Add(-2 * time.Hour),
				LastStatus:  "error",
				PageBytes:   webmaster.PageBytes(),
			},
			{
				Slug:        "buzzy",
				HealthKnown: true,
				LastTurnAt:  now.Add(-5 * time.Minute),
				LastStatus:  "changed",
				Alerts:      2,
			},
		},
	}

	_, cards := renderLobby(t, view)
	if len(cards) != 2 {
		t.Fatalf("cards = %d, want 2", len(cards))
	}
	if got := htmlAttr(cards[0], "data-health"); got != "error" {
		t.Errorf("first card health = %q, want error", got)
	}
	if got := htmlAttr(cards[1], "data-health"); got != "alerts" {
		t.Errorf("second card health = %q, want alerts", got)
	}
	// The handler sorts with HealthRank before it renders, so the rank order
	// and the document order agree.
	if view.Worlds[0].HealthRank() >= view.Worlds[1].HealthRank() {
		t.Error("the error card does not rank before the alerts card")
	}

	for i, slug := range []string{"broken", "buzzy"} {
		play := "/worlds/" + slug + "/play"
		if findHTML(cards[i], formTo(play)) == nil {
			t.Errorf("%s card has no enter form for %s", slug, play)
		}
	}

	if findHTML(cards[0], byAttr("data-chip", "failed")) == nil {
		t.Error("the failed world has no failed chip")
	}
	alerts := findHTML(cards[1], byAttr("data-chip", "alerts"))
	if alerts == nil {
		t.Fatal("the world with open alerts has no alerts chip")
	}
	if got := htmlAttr(alerts, "data-alerts"); got != "2" {
		t.Errorf("alerts chip count = %q, want 2", got)
	}

	for _, card := range cards {
		if findHTML(card, hasPageSize) != nil {
			t.Error("a member card carries the page-size bar")
		}
	}
}

// An admin gets the page-size bar, with the percent and the bar red when the
// page is near the cap.
func TestLobbyGalleryPageBarForAdmins(t *testing.T) {
	full := AppWorld{
		Slug:        "heroes",
		HealthKnown: true,
		LastStatus:  "changed",
		PageBytes:   webmaster.PageBytes() * 95 / 100,
	}
	member, _ := renderLobby(t, AppView{Name: "Ada", Worlds: []AppWorld{full}})
	if findHTML(member, hasPageSize) != nil {
		t.Fatal("a member view shows the page-size bar")
	}

	_, cards := renderLobby(t, AppView{Name: "Ada", Admin: true, Worlds: []AppWorld{full}})
	if len(cards) != 1 {
		t.Fatalf("cards = %d, want 1", len(cards))
	}
	bar := findHTML(cards[0], hasPageSize)
	if bar == nil {
		t.Fatal("an admin card has no page-size bar")
	}
	if got := htmlAttr(bar, "data-page-size"); got != itoa(full.PagePercent()) {
		t.Errorf("page size = %q, want %s", got, itoa(full.PagePercent()))
	}
	if findHTML(bar, func(n *html.Node) bool { return hasClass(n, "bg-red-500") }) == nil {
		t.Error("a nearly full page bar is not red")
	}
}

// A world the registry has no row for still renders as a card with its enter
// form: a fresh install and a deleted health row must not break the lobby.
func TestLobbyGalleryRendersWithoutHealthRows(t *testing.T) {
	_, cards := renderLobby(t, AppView{
		Name:   "Ada",
		Worlds: []AppWorld{{Slug: "fresh"}},
	})
	if len(cards) != 1 {
		t.Fatalf("cards = %d, want 1", len(cards))
	}
	if got := htmlAttr(cards[0], "data-health"); got != "none" {
		t.Errorf("health = %q, want none", got)
	}
	if findHTML(cards[0], formTo("/worlds/fresh/play")) == nil {
		t.Error("the world with no health row has no enter form")
	}
}
