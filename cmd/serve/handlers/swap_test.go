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
		Header:        "<h1>beaverdream</h1>",
		Board:         "<main>{{posts}}</main>",
		Footer:        "<footer></footer>",
		StateSchema:   "{}",
		JournalSchema: "{}",
	}
	cases := []struct {
		name   string
		change func(*sqlc.SiteVersion)
		want   string
	}{
		{"css only", func(v *sqlc.SiteVersion) { v.Css = "body{background:#000}" }, "css"},
		{"markup", func(v *sqlc.SiteVersion) { v.Board += "<p>extra</p>" }, "reload"},
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
	sub, cancel := h.subscribe()
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
