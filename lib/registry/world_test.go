package registry_test

import (
	"context"
	"testing"
	"time"

	"github.com/Napam/beaverdream/lib/registry"
)

// The health row is the lobby's cache: a full upsert, an alert bump that keeps
// the rest of the row, and a delete that takes the row with the world.
func TestWorldHealthUpsertBumpAndCascade(t *testing.T) {
	ctx := context.Background()
	clock := &fakeClock{t: time.Date(2026, 5, 6, 7, 8, 9, 0, time.UTC)}
	reg := newRegistry(t, clock)

	world, err := reg.CreateWorldRecord(ctx, "world-1", "first-world", "")
	if err != nil {
		t.Fatalf("create world: %v", err)
	}
	if _, err := reg.CreateWorldRecord(ctx, "world-2", "second-world", ""); err != nil {
		t.Fatalf("create second world: %v", err)
	}

	health, err := reg.GetWorldHealth(ctx, []string{"world-1", "world-2"})
	if err != nil {
		t.Fatalf("get before any write: %v", err)
	}
	if len(health) != 0 {
		t.Fatalf("health rows before a write = %d, want 0", len(health))
	}

	turnAt := clock.Now().Add(-2 * time.Hour)
	pageBytes := 1234
	if err := reg.SetWorldHealth(ctx, registry.WorldHealth{
		WorldID:    world.ID,
		LastTurnAt: turnAt,
		LastStatus: "changed",
		PageBytes:  pageBytes,
		Alerts:     2,
	}); err != nil {
		t.Fatalf("set health: %v", err)
	}

	health, err = reg.GetWorldHealth(ctx, []string{"world-1", "world-2"})
	if err != nil {
		t.Fatalf("get after write: %v", err)
	}
	got, ok := health["world-1"]
	if !ok {
		t.Fatal("world-1 has no health row")
	}
	if !got.LastTurnAt.Equal(turnAt) {
		t.Errorf("last turn = %v, want %v", got.LastTurnAt, turnAt)
	}
	if got.LastStatus != "changed" || got.PageBytes != pageBytes || got.Alerts != 2 {
		t.Errorf("health = %+v, want changed/%d/2 alerts", got, pageBytes)
	}
	if _, ok := health["world-2"]; ok {
		t.Error("world-2 has a health row it never got")
	}

	// A kept turn keeps the alert count it did not clear.
	if err := reg.SetWorldHealth(ctx, registry.WorldHealth{
		WorldID:    world.ID,
		LastStatus: "kept",
		PageBytes:  pageBytes,
		KeepAlerts: true,
	}); err != nil {
		t.Fatalf("set kept health: %v", err)
	}
	health, err = reg.GetWorldHealth(ctx, []string{"world-1"})
	if err != nil {
		t.Fatalf("get kept health: %v", err)
	}
	got = health["world-1"]
	if got.LastStatus != "kept" || got.Alerts != 2 {
		t.Errorf("kept health = %+v, want kept with 2 alerts", got)
	}
	if !got.LastTurnAt.IsZero() {
		t.Errorf("last turn = %v, want zero", got.LastTurnAt)
	}

	// A new page clears the alerts that were open against the old one.
	if err := reg.SetWorldHealth(ctx, registry.WorldHealth{
		WorldID:    world.ID,
		LastStatus: "changed",
		PageBytes:  pageBytes,
	}); err != nil {
		t.Fatalf("set cleared health: %v", err)
	}
	health, err = reg.GetWorldHealth(ctx, []string{"world-1"})
	if err != nil {
		t.Fatalf("get cleared health: %v", err)
	}
	if got = health["world-1"]; got.Alerts != 0 {
		t.Errorf("alerts after a new page = %d, want 0", got.Alerts)
	}

	// The bump works on a row of its own and leaves the page size alone.
	if err := reg.BumpWorldAlerts(ctx, "world-2"); err != nil {
		t.Fatalf("bump on a missing row: %v", err)
	}
	health, err = reg.GetWorldHealth(ctx, []string{"world-1", "world-2"})
	if err != nil {
		t.Fatalf("get bumped health: %v", err)
	}
	second := health["world-2"]
	if second.Alerts != 1 || second.LastStatus != "alert" || second.LastTurnAt.IsZero() {
		t.Errorf("bumped health = %+v, want 1 alert and an alert time", second)
	}
	if err := reg.BumpWorldAlerts(ctx, "world-2"); err != nil {
		t.Fatalf("second bump: %v", err)
	}
	health, err = reg.GetWorldHealth(ctx, []string{"world-2"})
	if err != nil {
		t.Fatalf("get second bump: %v", err)
	}
	if got = health["world-2"]; got.Alerts != 2 {
		t.Errorf("alerts after two bumps = %d, want 2", got.Alerts)
	}
	if got.PageBytes != 0 {
		t.Errorf("page bytes after a bump = %d, want the stored 0", got.PageBytes)
	}

	if err := reg.DeleteWorldRecord(ctx, "world-2"); err != nil {
		t.Fatalf("delete world: %v", err)
	}
	health, err = reg.GetWorldHealth(ctx, []string{"world-1", "world-2"})
	if err != nil {
		t.Fatalf("get after delete: %v", err)
	}
	if _, ok := health["world-2"]; ok {
		t.Error("the health row outlived its world")
	}
	if _, ok := health["world-1"]; !ok {
		t.Error("deleting one world took another world's health")
	}
}
