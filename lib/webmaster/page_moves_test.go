package webmaster

import (
	"strings"
	"testing"
)

// A page that already moves with keys gets the movement hint; a page that only
// listens for Enter does not.
func TestPageMoves(t *testing.T) {
	for html, want := range map[string]bool{
		`<script>addEventListener('keydown', e => { if (e.code === 'ArrowLeft') x-- })</script>`: true,
		`<script>addEventListener('keyup', e => keys[e.code] = false); var k = 'KeyD'</script>`:  true,
		`<script>onkeydown = step; requestAnimationFrame(step)</script>`:                         true,
		`<script>canvas.onclick = () => canvas.requestPointerLock()</script>`:                    true,
		`<textarea onkeydown="if (event.key === 'Enter') this.form.requestSubmit()">`:            false,
		`<script>requestAnimationFrame(spin)</script>`:                                           false,
	} {
		if got := pageMoves(html); got != want {
			t.Errorf("pageMoves(%q) = %v, want %v", html, got, want)
		}
	}
}

// The hint comes after the new post and only on a page that moves, so the
// prefix before the new post never changes. A world post alone adds nothing:
// the model asks for the skill itself.
func TestUserPromptHintsAMovingPage(t *testing.T) {
	room := Room{Log: []Turn{{ID: 1, Kind: TurnPost, Author: "kit", Body: "hi"}}}
	post := []Post{{ID: 2, Author: "dave", Body: "make pacman"}}

	plain, plainSections := userPromptSections(
		Site{CSS: "body{}", HTML: "{{posts}}"}, room, post,
	)
	if last := plainSections[len(plainSections)-1]; last.Name != "new post" {
		t.Errorf("last section is %q, want new post", last.Name)
	}

	moving, sections := userPromptSections(
		Site{
			CSS:  "body{}",
			HTML: `<script>addEventListener('keydown', e => { if (e.code === 'ArrowLeft') x-- })</script>{{posts}}`,
		},
		room,
		post,
	)
	last := sections[len(sections)-1]
	if last.Name != "controls" || last.End != len(moving) {
		t.Fatalf("last section is %+v, want the hint ending the prompt", last)
	}
	cut := strings.Index(plain, "CURRENT STYLESHEET")
	if moving[:cut] != plain[:cut] {
		t.Error("the page changed the history or the log in the prompt")
	}
	if strings.Index(moving, "NEW POST") > last.Start {
		t.Error("the movement hint comes before the new post")
	}
}

// A page with a journal gets the sharing hint after the new post and any
// movement hint; a page without one, or with an empty schema, does not.
func TestUserPromptHintsASharedPage(t *testing.T) {
	room := Room{Log: []Turn{{ID: 1, Kind: TurnPost, Author: "kit", Body: "hi"}}}
	post := []Post{{ID: 2, Author: "dave", Body: "make the sword bigger"}}
	moving := `<script>addEventListener('keydown', e => { if (e.code === 'ArrowLeft') x-- })</script>{{posts}}`

	for _, schema := range []string{"", "{}", "not json"} {
		_, sections := userPromptSections(Site{HTML: moving, JournalSchema: schema}, room, post)
		for _, s := range sections {
			if s.Name == "shared" {
				t.Errorf("schema %q got the sharing hint", schema)
			}
		}
	}

	plain, _ := userPromptSections(Site{HTML: moving}, room, post)
	shared, sections := userPromptSections(
		Site{HTML: moving, JournalSchema: `{"moves":{"max":10,"rules":1}}`}, room, post,
	)
	n := len(sections)
	if sections[n-1].Name != "shared" || sections[n-1].End != len(shared) {
		t.Fatalf("last section is %+v, want the sharing hint ending the prompt", sections[n-1])
	}
	if sections[n-2].Name != "controls" {
		t.Errorf("section before the sharing hint is %q, want controls", sections[n-2].Name)
	}
	cut := strings.Index(plain, "CURRENT STYLESHEET")
	if shared[:cut] != plain[:cut] {
		t.Error("the journal changed the history or the log in the prompt")
	}
}
