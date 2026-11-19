package handlers_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/Napam/beaverdream/db/world/generated/sqlc"
	"github.com/Napam/beaverdream/lib/webmaster"
)

// versionStats reads one version's counters as "kind|key" -> count, the way
// the play report keys them.
func versionStats(t *testing.T, queries *sqlc.Queries, versionID int64) map[string]int64 {
	t.Helper()
	rows, err := queries.ListVersionStats(context.Background(), versionID)
	if err != nil {
		t.Fatalf("list version stats: %v", err)
	}
	stats := make(map[string]int64, len(rows))
	for _, row := range rows {
		stats[row.Kind+"|"+row.Key] = row.N
	}
	return stats
}

// One render is one load, and the same identity opening it twice is still one
// visitor.
func TestIndexCountsLoadsAndVisitors(t *testing.T) {
	h, queries := setup(t)
	ctx := context.Background()
	site, err := queries.CurrentSite(ctx)
	if err != nil {
		t.Fatalf("current site: %v", err)
	}

	render := func(name string) {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req = withIdentity(req, name)
		rec := httptest.NewRecorder()
		h.Index(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("index status = %d", rec.Code)
		}
	}
	render("dave")
	render("dave")
	render("kit")

	if got := versionStats(t, queries, site.ID)["load|"]; got != 3 {
		t.Errorf("loads = %d, want 3", got)
	}
	visitors, err := queries.CountVersionVisitors(ctx, site.ID)
	if err != nil {
		t.Fatalf("count visitors: %v", err)
	}
	if visitors != 2 {
		t.Errorf("visitors = %d, want 2", visitors)
	}
}

// An append that lands counts under its feed key.
func TestJournalAppendCountsItsKey(t *testing.T) {
	h, queries := setup(t)
	ctx := context.Background()
	declareJournal(t, queries, `{"moves":{"max":10,"rules":1}}`)
	site, err := queries.CurrentSite(ctx)
	if err != nil {
		t.Fatalf("current site: %v", err)
	}

	if rec := appendJournal(
		t,
		h.JournalAppend,
		"moves",
		`{"x":1}`,
		0,
		visitorA,
	); rec.Code != http.StatusOK {
		t.Fatalf("append status = %d", rec.Code)
	}

	if got := versionStats(t, queries, site.ID)["journal|moves"]; got != 1 {
		t.Errorf("journal moves = %d, want 1", got)
	}
}

// A metered-out append is a refusal the model should see, filed under the key
// and the reason the page got.
func TestJournalRefusalCountsSlow(t *testing.T) {
	h, queries := setup(t)
	ctx := context.Background()
	declareJournal(t, queries, `{"moves":{"max":10,"rules":1}}`)
	site, err := queries.CurrentSite(ctx)
	if err != nil {
		t.Fatalf("current site: %v", err)
	}

	// The bucket refills with time, so keep writing until the meter refuses
	// instead of trusting that a burst fits in one clock tick.
	refused := false
	for i := 0; i < 2*webmaster.JournalBurst; i++ {
		rec := appendJournal(t, h.JournalAppend, "moves", `{"n":`+strconv.Itoa(i)+`}`, 0, visitorA)
		if rec.Code == http.StatusTooManyRequests {
			refused = true
			break
		}
	}
	if !refused {
		t.Fatal("the journal never refused a write")
	}

	stats := versionStats(t, queries, site.ID)
	if got := stats["journal_refused|moves:slow"]; got != 1 {
		t.Errorf("slow refusals = %d, want 1 (%v)", got, stats)
	}
}

// A shared counter and a visitor counter both count under their own key.
func TestStateOpsCountTheirKey(t *testing.T) {
	h, queries := setup(t)
	ctx := context.Background()
	declareState(t, queries,
		`{"votes":{"op":"counter"},"coins":{"op":"counter","scope":"visitor","max":5}}`)
	site, err := queries.CurrentSite(ctx)
	if err != nil {
		t.Fatalf("current site: %v", err)
	}

	if rec := stateForm(t, h.SetState, "votes", url.Values{}, "dave"); rec.Code != http.StatusOK {
		t.Fatalf("global op status = %d", rec.Code)
	}
	if rec := stateForm(
		t,
		h.SetState,
		"coins",
		url.Values{"add": {"1"}},
		"dave",
	); rec.Code != http.StatusOK {
		t.Fatalf("visitor op status = %d", rec.Code)
	}

	stats := versionStats(t, queries, site.ID)
	if got := stats["state|votes"]; got != 1 {
		t.Errorf("state votes = %d, want 1", got)
	}
	if got := stats["state|coins"]; got != 1 {
		t.Errorf("state coins = %d, want 1", got)
	}
}

// A refused op carries the reason the handler answered with, so the model can
// tell a page that asks for an undeclared key from one that spends too much.
func TestStateRefusalsCountTheirReason(t *testing.T) {
	h, queries := setup(t)
	ctx := context.Background()
	declareState(t, queries,
		`{"coins":{"op":"counter","scope":"visitor","max":5}}`)
	site, err := queries.CurrentSite(ctx)
	if err != nil {
		t.Fatalf("current site: %v", err)
	}

	if rec := stateForm(
		t,
		h.SetState,
		"nope",
		url.Values{},
		"dave",
	); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown key status = %d", rec.Code)
	}
	if rec := stateForm(
		t,
		h.SetState,
		"coins",
		url.Values{"spend": {"5"}},
		"kit",
	); rec.Code != http.StatusConflict {
		t.Fatalf("short spend status = %d", rec.Code)
	}

	stats := versionStats(t, queries, site.ID)
	if got := stats["state_refused|nope:gone"]; got != 1 {
		t.Errorf("gone refusals = %d, want 1 (%v)", got, stats)
	}
	if got := stats["state_refused|coins:short"]; got != 1 {
		t.Errorf("short refusals = %d, want 1 (%v)", got, stats)
	}
}

// A browser error is counted against the page that reported it.
func TestClientErrorCountsAnAlert(t *testing.T) {
	h, queries := setup(t)
	ctx := context.Background()
	site, err := queries.CurrentSite(ctx)
	if err != nil {
		t.Fatalf("current site: %v", err)
	}

	body, err := json.Marshal(map[string]any{
		"message": "TypeError: cannot read properties of undefined",
		"version": site.ID,
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/errors", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ClientError(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d", rec.Code)
	}

	stats := versionStats(t, queries, site.ID)
	if got := stats["alert|TypeError: cannot read properties of undefined"]; got != 1 {
		t.Errorf("alerts = %d, want 1 (%v)", got, stats)
	}
}

// The report is only read for the newest versions, so writing the version
// after the kept 50 drops the oldest counters and nothing else.
func TestWritingThe51stVersionPrunesTheOldestReport(t *testing.T) {
	h, queries := setup(t)
	ctx := context.Background()
	seed, err := queries.CurrentSite(ctx)
	if err != nil {
		t.Fatalf("current site: %v", err)
	}
	if err := queries.BumpVersionStat(ctx, sqlc.BumpVersionStatParams{
		VersionID: seed.ID,
		Kind:      "load",
	}); err != nil {
		t.Fatalf("count the seed load: %v", err)
	}
	if err := queries.AddVersionVisitor(ctx, sqlc.AddVersionVisitorParams{
		VersionID: seed.ID,
		Visitor:   "dave",
	}); err != nil {
		t.Fatalf("count the seed visitor: %v", err)
	}

	// 49 more versions make 50 in total; the undo below writes the 51st.
	for i := 0; i < 49; i++ {
		if _, err := queries.CreateSiteVersion(ctx, sqlc.CreateSiteVersionParams{
			Css:    seed.Css,
			Html:   seed.Html,
			Reason: "history",
		}); err != nil {
			t.Fatalf("create history version: %v", err)
		}
	}
	if rec := postAdmin(t, h.UndoLast, "/admin/undo", url.Values{}); rec.Code != http.StatusOK {
		t.Fatalf("undo status = %d", rec.Code)
	}

	current, err := queries.CurrentSite(ctx)
	if err != nil {
		t.Fatalf("current site: %v", err)
	}
	if current.ID != seed.ID+50 {
		t.Fatalf("current version = %d, want %d", current.ID, seed.ID+50)
	}
	if got := versionStats(t, queries, seed.ID); len(got) != 0 {
		t.Errorf("the oldest version kept its counters: %v", got)
	}
	visitors, err := queries.CountVersionVisitors(ctx, seed.ID)
	if err != nil {
		t.Fatalf("count visitors: %v", err)
	}
	if visitors != 0 {
		t.Errorf("the oldest version kept %d visitors", visitors)
	}
}

// Reset drops the play report with the rest of the room: the seed version
// keeps its id, so its counters would otherwise outlive the world they
// measured.
func TestResetClearsThePlayReport(t *testing.T) {
	h, queries := setup(t)
	ctx := context.Background()
	seed, err := queries.CurrentSite(ctx)
	if err != nil {
		t.Fatalf("current site: %v", err)
	}
	if err := queries.BumpVersionStat(ctx, sqlc.BumpVersionStatParams{
		VersionID: seed.ID,
		Kind:      "load",
	}); err != nil {
		t.Fatalf("count a load: %v", err)
	}
	if err := queries.AddVersionVisitor(ctx, sqlc.AddVersionVisitorParams{
		VersionID: seed.ID,
		Visitor:   "dave",
	}); err != nil {
		t.Fatalf("count a visitor: %v", err)
	}

	if rec := postAdmin(t, h.ResetWorld, "/admin/reset", url.Values{}); rec.Code != http.StatusOK {
		t.Fatalf("reset status = %d", rec.Code)
	}

	if got := versionStats(t, queries, seed.ID); len(got) != 0 {
		t.Errorf("reset kept the counters: %v", got)
	}
	visitors, err := queries.CountVersionVisitors(ctx, seed.ID)
	if err != nil {
		t.Fatalf("count visitors: %v", err)
	}
	if visitors != 0 {
		t.Errorf("reset kept %d visitors", visitors)
	}
}

// The prompt reads the report the counters wrote: loads, visitors, journal
// writes and refusals, and the alerts.
func TestNextTurnReadsThePlayReport(t *testing.T) {
	h, queries := setup(t)
	ctx := context.Background()

	first := postFormAs(t, h.CreatePost, "/posts", url.Values{
		"body": {"mockedit the heading red"},
	}, "dave")
	if first.Code != http.StatusSeeOther {
		t.Fatalf("first post status = %d", first.Code)
	}
	waitFor(t, "the first rewrite", func() bool {
		site, err := queries.CurrentSite(ctx)
		return err == nil && site.ID > 1
	})
	shipped, err := queries.CurrentSite(ctx)
	if err != nil {
		t.Fatalf("current site: %v", err)
	}

	for i := 0; i < 2; i++ {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req = withIdentity(req, "kit")
		rec := httptest.NewRecorder()
		h.Index(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("index status = %d", rec.Code)
		}
	}

	second := postFormAs(t, h.CreatePost, "/posts", url.Values{
		"body": {"make it blue"},
	}, "dave")
	if second.Code != http.StatusSeeOther {
		t.Fatalf("second post status = %d", second.Code)
	}
	waitFor(t, "the second rewrite", func() bool {
		turns, err := queries.ListChangesets(ctx, 10)
		return err == nil && len(turns) == 2 && turns[0].Status != "running"
	})
	turns, err := queries.ListChangesets(ctx, 10)
	if err != nil {
		t.Fatalf("list turns: %v", err)
	}
	var messages []webmaster.Message
	if err := json.Unmarshal([]byte(turns[0].Messages), &messages); err != nil {
		t.Fatalf("decode messages: %v", err)
	}
	if len(messages) < 2 {
		t.Fatalf("the second turn stored %d messages", len(messages))
	}
	prompt := messages[1].Content
	var play *webmaster.PromptSection
	for i := range messages[1].Sections {
		if messages[1].Sections[i].Name == "play" {
			play = &messages[1].Sections[i]
		}
	}
	if play == nil {
		t.Fatalf("the second prompt has no play section:\n%s", prompt)
	}
	if play.Start < 0 || play.Start > play.End || play.End > len(prompt) {
		t.Fatalf(
			"the play section spans %d..%d of a %d byte prompt",
			play.Start,
			play.End,
			len(prompt),
		)
	}
	if !strings.Contains(prompt[play.Start:play.End], "v"+strconv.FormatInt(shipped.ID, 10)) {
		t.Errorf(
			"the play section does not carry the shipped version v%d:\n%s",
			shipped.ID,
			prompt[play.Start:play.End],
		)
	}
}

// The play section is not in the prompt when the version has no counters.
func TestFirstTurnHasNoPlayReport(t *testing.T) {
	h, queries := setup(t)
	ctx := context.Background()

	if rec := postFormAs(t, h.CreatePost, "/posts", url.Values{
		"body": {"make it pink"},
	}, "dave"); rec.Code != http.StatusSeeOther {
		t.Fatalf("post status = %d", rec.Code)
	}
	waitFor(t, "the first rewrite", func() bool {
		turns, err := queries.ListChangesets(ctx, 10)
		return err == nil && len(turns) == 1 && turns[0].Status != "running"
	})
	turns, err := queries.ListChangesets(ctx, 10)
	if err != nil {
		t.Fatalf("list turns: %v", err)
	}
	var messages []webmaster.Message
	if err := json.Unmarshal([]byte(turns[0].Messages), &messages); err != nil {
		t.Fatalf("decode messages: %v", err)
	}
	if len(messages) < 2 {
		t.Fatalf("the turn stored %d messages", len(messages))
	}
	for _, section := range messages[1].Sections {
		if section.Name == "play" {
			t.Fatalf("the first prompt invented a play section: %+v", section)
		}
	}
}
