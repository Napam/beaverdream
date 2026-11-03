package handlers

import (
	"testing"
	"time"

	"github.com/Napam/beaverdream/db/generated/sqlc"
)

func TestPostAuthorsDeduplicates(t *testing.T) {
	got := postAuthors([]sqlc.Post{
		{Author: "dave"},
		{Author: ""},
		{Author: "dave"},
		{Author: "kit"},
	})
	want := []string{"dave", "anon", "kit"}
	if len(got) != len(want) {
		t.Fatalf("authors = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("authors = %v, want %v", got, want)
		}
	}
}

func TestAllowRewriteThrottlesByName(t *testing.T) {
	h := &Handlers{lastRewriteAt: make(map[string]time.Time)}

	if !h.allowRewrite("dave") {
		t.Fatal("first rewrite was throttled")
	}
	if h.allowRewrite("dave") {
		t.Fatal("second rewrite was not throttled")
	}
	if !h.allowRewrite("kit") {
		t.Fatal("a different name was throttled")
	}

	h.lastRewriteAt["dave"] = time.Now().Add(-rewriteCooldown)
	if !h.allowRewrite("dave") {
		t.Fatal("rewrite was still throttled after the cooldown")
	}
}
