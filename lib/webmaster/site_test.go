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
		if strings.Contains(doc, banned) {
			t.Errorf("the page contains %q; it should switch without a transition", banned)
		}
	}
}
