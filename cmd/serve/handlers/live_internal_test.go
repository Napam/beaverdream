package handlers

import (
	"testing"
	"time"

	"github.com/Napam/beaverdream/db/world/generated/sqlc"
	"github.com/Napam/beaverdream/lib/webmaster"
)

func TestCancelingAdminStreamsLeavesPlayStreamsOpen(t *testing.T) {
	h := newHub()
	play, stopPlay := h.subscribeAs(topicAll, "member", "session")
	defer stopPlay()
	admin, stopAdmin := h.subscribeAs(topicAll|topicDraft, "member", "session")
	defer stopAdmin()
	other, stopOther := h.subscribeAs(topicAll|topicDraft, "other", "other-session")
	defer stopOther()

	h.cancelIdentityAdmin("member")

	select {
	case <-admin.closed:
	default:
		t.Fatal("the demoted identity's admin stream stayed open")
	}
	select {
	case <-play.closed:
		t.Fatal("the demoted identity's play stream was closed")
	default:
	}
	select {
	case <-other.closed:
		t.Fatal("another identity's admin stream was closed")
	default:
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

// A 60ms game loop sends about 17 updates a second. That has to fit under the
// write budget, or motion stutters as rows are dropped.
func TestJournalBucketKeepsUpWithAGameLoop(t *testing.T) {
	now := time.Now()
	bucket := &journalBucket{at: now}
	for i := 0; i < 200; i++ {
		now = now.Add(60 * time.Millisecond)
		if !takeJournalToken(bucket, now) {
			t.Fatalf("update %d was dropped", i+1)
		}
	}
}

func TestJournalBucketBurstsThenRefills(t *testing.T) {
	now := time.Now()
	bucket := &journalBucket{tokens: webmaster.JournalBurst, at: now}
	for i := 0; i < webmaster.JournalBurst; i++ {
		if !takeJournalToken(bucket, now) {
			t.Fatalf("burst write %d was dropped", i+1)
		}
	}
	if takeJournalToken(bucket, now) {
		t.Fatal("a write past the burst was allowed")
	}
	// At 20 a second, one token arrives every 50ms.
	now = now.Add(50 * time.Millisecond)
	if !takeJournalToken(bucket, now) {
		t.Fatal("the bucket did not refill")
	}
}

func TestAllowJournalWriteMetersEachVisitorAndKey(t *testing.T) {
	h := &Handlers{}
	for i := 0; i < webmaster.JournalBurst; i++ {
		if !h.allowJournalWrite("dave", "moves") {
			t.Fatalf("burst write %d was refused", i+1)
		}
	}
	if h.allowJournalWrite("dave", "moves") {
		t.Fatal("a visitor's burst did not limit them")
	}
	if !h.allowJournalWrite("kit", "moves") {
		t.Fatal("one visitor's burst limited another")
	}
	if !h.allowJournalWrite("dave", "chat") {
		t.Fatal("one key's burst limited another")
	}
}
