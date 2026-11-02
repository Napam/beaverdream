package handlers

import (
	"strings"
	"testing"
	"time"

	"github.com/Napam/beaverdream/db/generated/sqlc"
)

func TestBatchBodyKeepsEveryPostAndAuthor(t *testing.T) {
	got := batchBody([]sqlc.Post{
		{ID: 4, Author: "kit", Body: "one"},
		{ID: 5, Author: "", Body: "two"},
	})
	for _, want := range []string{"#4 kit: one", "#5 anon: two"} {
		if !strings.Contains(got, want) {
			t.Errorf("batch body is missing %q: %s", want, got)
		}
	}
}

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
