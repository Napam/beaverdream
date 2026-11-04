package handlers

import (
	"testing"

	"github.com/Napam/beaverdream/db/generated/sqlc"
)

// An open page takes a stylesheet in place only when nothing else changed.
// Markup, either schema, or a stylesheet that breaks out of its tag reloads:
// the page carries the schemas and the journal config, and in-place text
// cannot remove elements a breakout created.
func TestSwapKind(t *testing.T) {
	seed := sqlc.SiteVersion{
		Css:           "",
		Html:          "<h1>beaverdream</h1><main>{{posts}}</main><footer></footer>",
		StateSchema:   "{}",
		JournalSchema: "{}",
	}
	cases := []struct {
		name   string
		change func(*sqlc.SiteVersion)
		want   string
	}{
		{"css only", func(v *sqlc.SiteVersion) { v.Css = "body{background:#000}" }, "css"},
		{"markup", func(v *sqlc.SiteVersion) { v.Html += "<p>extra</p>" }, "reload"},
		{"state schema", func(v *sqlc.SiteVersion) {
			v.StateSchema = `{"likes":{"op":"counter"}}`
		}, "reload"},
		{"journal schema", func(v *sqlc.SiteVersion) {
			v.JournalSchema = `{"moves":{"max":10,"rules":1}}`
		}, "reload"},
		{"style breakout", func(v *sqlc.SiteVersion) {
			v.Css = `body{color:red}</style><script>alert(1)</script>`
		}, "reload"},
		{"empty journal schema is no change", func(v *sqlc.SiteVersion) {
			v.JournalSchema = ""
			v.Css = "a{color:red}"
		}, "css"},
	}
	for _, c := range cases {
		next := seed
		c.change(&next)
		if got := swapKind(seed, next); got != c.want {
			t.Errorf("%s: swap = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestHubCoalescesTopics(t *testing.T) {
	h := newHub()
	sub, cancel := h.subscribe(topicAll)
	defer cancel()

	h.publish(topicPosts)
	h.publish(topicState)
	h.publish(topicPosts)

	select {
	case <-sub.wake:
	default:
		t.Fatal("a publish did not wake the subscriber")
	}
	if got := sub.take(); got != topicPosts|topicState {
		t.Fatalf("topics = %b, want posts and state", got)
	}
	if got := sub.take(); got != 0 {
		t.Fatalf("topics after take = %b, want none", got)
	}

	cancel()
	if len(h.subs) != 0 {
		t.Fatal("cancel left the subscriber in the hub")
	}
	h.publish(topicAll)
	if got := sub.take(); got != 0 {
		t.Fatal("a cancelled subscriber still receives topics")
	}
}

// A board stream does not ask for drafts, so a streaming round many times a
// second never wakes it.
func TestHubWakesOnlyForWantedTopics(t *testing.T) {
	h := newHub()
	board, cancelBoard := h.subscribe(topicAll)
	defer cancelBoard()
	admin, cancelAdmin := h.subscribe(topicAll | topicDraft)
	defer cancelAdmin()

	h.publish(topicDraft)

	select {
	case <-board.wake:
		t.Fatal("a draft woke a board stream")
	default:
	}
	select {
	case <-admin.wake:
	default:
		t.Fatal("a draft did not wake the admin stream")
	}
	if got := admin.take(); got != topicDraft {
		t.Fatalf("admin topics = %b, want the draft", got)
	}
}
