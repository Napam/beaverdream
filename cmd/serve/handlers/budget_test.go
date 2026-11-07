package handlers_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Napam/beaverdream/cmd/serve/handlers"
	"github.com/Napam/beaverdream/db/generated/sqlc"
	"github.com/Napam/beaverdream/lib/registry"
	"github.com/Napam/beaverdream/lib/webmaster"
)

// A successful post charges the turn to its author after the world commit.
func TestUsageIsChargedAfterACommittedTurn(t *testing.T) {
	h, queries := setup(t)
	ctx := context.Background()
	postForm(t, h.CreatePost, "/posts", url.Values{"body": {"mockedit charge me"}})
	waitFor(t, "the turn", func() bool {
		turns, err := queries.ListChangesets(ctx, 1)
		return err == nil && len(turns) == 1 && turns[0].Status != "running"
	})
	used, global, err := h.RegistryForTest().UsageOn(ctx, "dave", time.Now().UTC())
	if err != nil {
		t.Fatalf("usage: %v", err)
	}
	if used <= 0 || global < used {
		t.Fatalf("usage = %d, global = %d, want a charged row", used, global)
	}
	rows, err := usageRows(t, h, "dave")
	if err != nil {
		t.Fatalf("usage rows: %v", err)
	}
	if len(rows) != 1 || rows[0].worldID != h.WorldForTest().ID || rows[0].turnID <= 0 {
		t.Fatalf("usage rows = %+v", rows)
	}
}

// A batch charges each distinct identity once, even when two posts share an
// author, and the world id qualifies every row.
func TestBatchUsageSplitsDistinctIdentityIDs(t *testing.T) {
	h, queries := setup(t)
	ctx := context.Background()
	first, err := queries.CreatePost(ctx, sqlc.CreatePostParams{Author: "id-a", Body: "one"})
	if err != nil {
		t.Fatalf("post a: %v", err)
	}
	second, err := queries.CreatePost(ctx, sqlc.CreatePostParams{Author: "id-b", Body: "two"})
	if err != nil {
		t.Fatalf("post b: %v", err)
	}
	third, err := queries.CreatePost(ctx, sqlc.CreatePostParams{Author: "id-a", Body: "three"})
	if err != nil {
		t.Fatalf("post a again: %v", err)
	}
	h.HoldPost(sqlc.Post(first))
	h.HoldPost(sqlc.Post(second))
	h.HoldPost(sqlc.Post(third))
	h.RunPending()

	a, _, err := h.RegistryForTest().UsageOn(ctx, "id-a", time.Now().UTC())
	if err != nil {
		t.Fatalf("usage a: %v", err)
	}
	b, global, err := h.RegistryForTest().UsageOn(ctx, "id-b", time.Now().UTC())
	if err != nil {
		t.Fatalf("usage b: %v", err)
	}
	if a <= 0 || b <= 0 {
		t.Fatalf("batch usage a=%d b=%d, want both charged", a, b)
	}
	if global != a+b {
		t.Errorf("global = %d, want the sum of the two rows %d", global, a+b)
	}
	for _, id := range []string{"id-a", "id-b"} {
		rows, err := usageRows(t, h, id)
		if err != nil {
			t.Fatalf("rows %s: %v", id, err)
		}
		if len(rows) != 1 || rows[0].worldID != h.WorldForTest().ID {
			t.Errorf("%s rows = %+v", id, rows)
		}
	}
}

// A budget refusal answers 429 reason budget and stores nothing.
func TestBudgetRefusalStoresNothing(t *testing.T) {
	h, queries := setup(t)
	h.SetBudgetForTest(registry.Budget{Identity: 1, Global: 1000})
	ctx := context.Background()
	if err := h.RegistryForTest().RecordUsage(ctx, "dave", h.WorldForTest().ID, 1, 1); err != nil {
		t.Fatalf("seed usage: %v", err)
	}
	before, _ := queries.CurrentSite(ctx)
	rec := postForm(t, h.CreatePost, "/posts", url.Values{"body": {"too late"}})
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429", rec.Code)
	}
	if posts, _ := queries.ListPosts(ctx); len(posts) != 0 {
		t.Errorf("refused post was stored: %+v", posts)
	}
	after, _ := queries.CurrentSite(ctx)
	if after.ID != before.ID {
		t.Errorf("refused post wrote version %d", after.ID)
	}
	if turns, _ := queries.ListChangesets(ctx, 10); len(turns) != 0 {
		t.Errorf("refused post started a turn")
	}
}

type usageRow struct {
	worldID string
	turnID  int64
	tokens  int64
}

func usageRows(t *testing.T, h *handlers.Handlers, identityID string) ([]usageRow, error) {
	t.Helper()
	rows, err := h.RegistryForTest().DB().QueryContext(context.Background(),
		`SELECT world_id, turn_id, tokens FROM usage WHERE identity_id = ? ORDER BY created_at`,
		identityID,
	)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []usageRow
	for rows.Next() {
		var row usageRow
		if err := rows.Scan(&row.worldID, &row.turnID, &row.tokens); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

// An admin retry is exempt; a post retry is a new turn and is charged once.
func TestRetryChargingPolicy(t *testing.T) {
	h, queries := setup(t)
	ctx := context.Background()

	// Admin instruction retry: no usage row.
	adminTurn, err := queries.StartTurn(ctx, sqlc.StartTurnParams{
		Kind: webmaster.TurnChangeset, Author: "admin", Body: "mockedit admin retry",
	})
	if err != nil {
		t.Fatalf("start admin turn: %v", err)
	}
	if err := queries.FailTurn(
		ctx,
		sqlc.FailTurnParams{ErrorText: "failed", ID: adminTurn.ID},
	); err != nil {
		t.Fatalf("fail admin turn: %v", err)
	}
	id := strconv.FormatInt(adminTurn.ID, 10)
	if rec := postAdminAt(t, h.RetryTurn, "/admin/retry/"+id, id); rec.Code != http.StatusOK {
		t.Fatalf("admin retry: status = %d", rec.Code)
	}
	if used, _, _ := h.RegistryForTest().UsageOn(ctx, "admin", time.Now().UTC()); used != 0 {
		t.Errorf("admin retry charged %d tokens", used)
	}

	// Post retry: the posts are read back from turns and charged once.
	post, err := queries.CreatePost(
		ctx,
		sqlc.CreatePostParams{Author: "id-post", Body: "mockedit retry me"},
	)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	_ = post
	postTurn, err := queries.StartTurn(ctx, sqlc.StartTurnParams{
		Kind: webmaster.TurnChangeset, Author: "id-post",
	})
	if err != nil {
		t.Fatalf("start post turn: %v", err)
	}
	if err := queries.FailTurn(
		ctx,
		sqlc.FailTurnParams{ErrorText: "failed", ID: postTurn.ID},
	); err != nil {
		t.Fatalf("fail post turn: %v", err)
	}
	id = strconv.FormatInt(postTurn.ID, 10)
	if rec := postAdminAt(t, h.RetryTurn, "/admin/retry/"+id, id); rec.Code != http.StatusOK {
		t.Fatalf("post retry: status = %d", rec.Code)
	}
	used, _, err := h.RegistryForTest().UsageOn(ctx, "id-post", time.Now().UTC())
	if err != nil {
		t.Fatalf("usage: %v", err)
	}
	if used <= 0 {
		t.Fatal("a successful post retry was not charged")
	}
	rows, err := usageRows(t, h, "id-post")
	if err != nil {
		t.Fatalf("usage rows: %v", err)
	}
	if len(rows) != 1 || rows[0].turnID == postTurn.ID {
		t.Errorf("usage rows = %+v, want one row for the new retry turn", rows)
	}
}

// A plain over-budget post gets 429 and its text back on an app-owned form; a
// Datastar post gets JSON with reason budget and no redirect.
func TestBudgetRefusalTransport(t *testing.T) {
	h, _ := setup(t)
	h.SetBudgetForTest(registry.Budget{Identity: 1, Global: 1})
	ctx := context.Background()
	if err := h.RegistryForTest().RecordUsage(ctx, "dave", h.WorldForTest().ID, 1, 1); err != nil {
		t.Fatalf("seed usage: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/posts", strings.NewReader("body=keep+my+words"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "text/html")
	req = withIdentity(req, "dave")
	rec := httptest.NewRecorder()
	h.CreatePost(rec, req)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("plain status = %d, want 429", rec.Code)
	}
	page := rec.Body.String()
	if !strings.Contains(page, "keep my words") || !strings.Contains(page, "/worlds/test/posts") {
		t.Errorf("plain refusal lost the draft: %s", page)
	}

	datastar := datastarPost(t, h.CreatePost, "/posts", url.Values{"body": {"gone"}}, "dave")
	if datastar.Code != http.StatusTooManyRequests ||
		!strings.Contains(datastar.Body.String(), `"reason":"budget"`) {
		t.Errorf("datastar refusal = %d %s", datastar.Code, datastar.Body.String())
	}
}
