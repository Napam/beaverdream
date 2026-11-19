//nolint:bodyclose // bodies are closed by body() or intentionally streamed.
package handlers_test

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/Napam/beaverdream/lib/registry"
)

// A finished turn and a browser alert both land in the registry's lobby cache,
// and a new page clears the alerts that were open against the old one.
func TestWorldHealthFollowsTurnsAndAlerts(t *testing.T) {
	in := newInstance(t, instanceOptions{})
	b := in.newBrowser(t)
	b.login(in, "admin", acceptanceAdminPassword)

	worldURL, _ := b.play(in, "heroes")
	if resp := b.postWorld(
		worldURL,
		url.Values{"body": {"make it matrix"}},
	); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("post: status = %d, body %s", resp.StatusCode, body(t, resp))
	}
	waitFor(t, "the turn health row", func() bool {
		health, err := in.reg.GetWorldHealth(context.Background(), []string{in.heroesID})
		if err != nil {
			return false
		}
		row, ok := health[in.heroesID]
		return ok && row.LastStatus == "changed"
	})

	handle := in.worldDB(t, in.heroesID)
	var (
		pageBytes int
		versionID int64
	)
	if err := handle.DB.QueryRowContext(context.Background(),
		`SELECT length(css) + length(html) FROM site_versions ORDER BY id DESC LIMIT 1`,
	).Scan(&pageBytes); err != nil {
		t.Fatalf("read page size: %v", err)
	}
	if err := handle.DB.QueryRowContext(context.Background(),
		`SELECT MAX(id) FROM site_versions`,
	).Scan(&versionID); err != nil {
		t.Fatalf("read version: %v", err)
	}
	health, err := in.reg.GetWorldHealth(context.Background(), []string{in.heroesID})
	if err != nil {
		t.Fatalf("read health: %v", err)
	}
	if got := health[in.heroesID]; got.PageBytes != pageBytes || got.Alerts != 0 {
		t.Errorf("health = %+v, want %d page bytes and no alerts", got, pageBytes)
	}
	if health[in.heroesID].LastTurnAt.IsZero() {
		t.Error("the finished turn left no last turn time")
	}

	// A reported script error bumps the count and marks the row as an alert.
	payload := fmt.Sprintf(`{"message":"boom","version":%d}`, versionID)
	req, err := http.NewRequest(
		http.MethodPost,
		in.playURLFor("/worlds/heroes/errors"),
		strings.NewReader(payload),
	)
	if err != nil {
		t.Fatalf("alert request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := b.client.Do(req)
	if err != nil {
		t.Fatalf("post alert: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("alert status = %d, want 204", resp.StatusCode)
	}
	waitFor(t, "the alert health row", func() bool {
		health, err := in.reg.GetWorldHealth(context.Background(), []string{in.heroesID})
		if err != nil {
			return false
		}
		row, ok := health[in.heroesID]
		return ok && row.Alerts == 1 && row.LastStatus == "alert"
	})

	// The next changed page closes the alert and records its own size.
	if resp := b.postWorld(
		worldURL,
		url.Values{"body": {"make it pink"}},
	); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("second post: status = %d, body %s", resp.StatusCode, body(t, resp))
	}
	waitFor(t, "the new page to clear the alert", func() bool {
		health, err := in.reg.GetWorldHealth(context.Background(), []string{in.heroesID})
		if err != nil {
			return false
		}
		row, ok := health[in.heroesID]
		return ok && row.Alerts == 0 && row.LastStatus == "changed"
	})
}

// The gallery renders both a world with an error row and one with no row at
// all, and puts the broken one first.
func TestLobbyGalleryShowsHealthAndSurvivesAMissingRow(t *testing.T) {
	in := newInstance(t, instanceOptions{})
	ctx := context.Background()
	if err := in.reg.SetWorldHealth(ctx, registry.WorldHealth{
		WorldID:    in.heroesID,
		LastTurnAt: time.Now().Add(-2 * time.Hour),
		LastStatus: "error",
		PageBytes:  900000,
	}); err != nil {
		t.Fatalf("set health: %v", err)
	}

	b := in.newBrowser(t)
	b.login(in, "admin", acceptanceAdminPassword)
	resp := b.get(in.appURLFor("/admin"))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("admin status = %d, body %s", resp.StatusCode, body(t, resp))
	}
	page := body(t, resp)

	if !strings.Contains(page, "world-heroes") || !strings.Contains(page, "world-atlas") {
		t.Fatal("the gallery is missing a world slug")
	}
	if !strings.Contains(page, `data-health="error"`) {
		t.Error("the page carries no error state for the broken world")
	}
	if !strings.Contains(page, `data-health="none"`) {
		t.Error("a world with no health row broke the page instead of rendering as unknown")
	}
	if strings.Index(page, "world-heroes") > strings.Index(page, "world-atlas") {
		t.Error("the broken world does not sort first")
	}
}

// The one-time backfill reads each world that has no row and leaves a ready
// world playable: it only reads the newest version and the last turn.
func TestBackfillWorldHealthFillsMissingRows(t *testing.T) {
	in := newInstance(t, instanceOptions{})
	ctx := context.Background()
	if err := in.reg.SetWorldHealth(ctx, registry.WorldHealth{
		WorldID:    in.atlasID,
		LastStatus: "kept",
		PageBytes:  7,
	}); err != nil {
		t.Fatalf("seed a health row: %v", err)
	}

	in.app.BackfillWorldHealth(ctx)

	health, err := in.reg.GetWorldHealth(ctx, []string{in.heroesID, in.atlasID})
	if err != nil {
		t.Fatalf("read health: %v", err)
	}
	heroes, ok := health[in.heroesID]
	if !ok {
		t.Fatal("the backfill left the world without a row")
	}
	if heroes.PageBytes <= 0 {
		t.Errorf("backfilled page bytes = %d, want the seed page size", heroes.PageBytes)
	}
	if heroes.LastStatus != "" || !heroes.LastTurnAt.IsZero() {
		t.Errorf("backfilled health = %+v, want an untouched turn", heroes)
	}
	if atlas := health[in.atlasID]; atlas.PageBytes != 7 {
		t.Errorf("the backfill overwrote an existing row: %+v", atlas)
	}
}
