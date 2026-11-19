package handlers

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"sort"
	"time"

	"github.com/Napam/beaverdream/lib/registry"
	"github.com/Napam/beaverdream/web/pages"
)

// attachWorldHealth adds the cached health to the lobby's worlds and orders
// them: error first, then open alerts, then newest last turn, then slug. A
// world with no row keeps the zero health, so a fresh install renders.
func (a *App) attachWorldHealth(
	ctx context.Context,
	worlds []pages.AppWorld,
) []pages.AppWorld {
	if a.reg != nil && len(worlds) > 0 {
		ids := make([]string, len(worlds))
		for i, world := range worlds {
			ids[i] = world.ID
		}
		health, err := a.reg.GetWorldHealth(ctx, ids)
		if err != nil {
			a.logger.Error("Failed to read world health", slog.Any("error", err))
		} else {
			for i := range worlds {
				row, ok := health[worlds[i].ID]
				if !ok {
					continue
				}
				worlds[i].HealthKnown = true
				worlds[i].LastTurnAt = row.LastTurnAt
				worlds[i].LastStatus = row.LastStatus
				worlds[i].PageBytes = row.PageBytes
				worlds[i].Alerts = row.Alerts
			}
		}
	}
	sort.SliceStable(worlds, func(i, j int) bool {
		left, right := worlds[i], worlds[j]
		if left.HealthRank() != right.HealthRank() {
			return left.HealthRank() < right.HealthRank()
		}
		if !left.LastTurnAt.Equal(right.LastTurnAt) {
			return left.LastTurnAt.After(right.LastTurnAt)
		}
		return left.Slug < right.Slug
	})
	return worlds
}

// recordWorldHealth writes the lobby's cached row after a turn. It is best
// effort: a failed cache write is logged and never changes the request.
func (h *Handlers) recordWorldHealth(
	ctx context.Context,
	status string,
	pageBytes int,
	resetAlerts bool,
) {
	if h.reg == nil {
		return
	}
	world := h.worldInfo()
	if world.ID == "" {
		return
	}
	err := h.reg.SetWorldHealth(ctx, registry.WorldHealth{
		WorldID:    world.ID,
		LastTurnAt: time.Now().UTC(),
		LastStatus: status,
		PageBytes:  max(pageBytes, 0),
		KeepAlerts: !resetAlerts,
	})
	if err != nil {
		h.logger.Warn("Failed to record world health",
			slog.String("world_id", world.ID),
			slog.Any("error", err),
		)
	}
}

// BackfillWorldHealth fills the lobby cache for ready worlds that have no row
// yet. It is the one place other than a finished turn that reads a world file
// for the lobby: each world is opened, its newest version and last turn are
// read, and the row is written. A world that is already open is fine, since a
// second connection only reads; a world that fails to open is skipped and
// logged, never fatal.
func (a *App) BackfillWorldHealth(ctx context.Context) {
	if a == nil || a.reg == nil || a.worlds == nil {
		return
	}
	start := time.Now()
	worlds, err := a.reg.ListReadyWorlds(ctx)
	if err != nil {
		a.logger.Error("Failed to list worlds for the health backfill", slog.Any("error", err))
		return
	}
	ids := make([]string, 0, len(worlds))
	for _, world := range worlds {
		ids = append(ids, world.ID)
	}
	health, err := a.reg.GetWorldHealth(ctx, ids)
	if err != nil {
		a.logger.Error("Failed to read world health", slog.Any("error", err))
		return
	}
	filled, skipped := 0, 0
	for _, world := range worlds {
		if _, ok := health[world.ID]; ok {
			continue
		}
		if err := a.backfillOneWorldHealth(ctx, world); err != nil {
			skipped++
			a.logger.Warn("Skipped a world in the health backfill",
				slog.String("world_id", world.ID),
				slog.String("slug", world.Slug),
				slog.Any("error", err),
			)
			continue
		}
		filled++
	}
	a.logger.Info("Backfilled world health",
		slog.Int("worlds", filled),
		slog.Int("skipped", skipped),
		slog.Duration("elapsed", time.Since(start)),
	)
}

// backfillOneWorldHealth reads one world's newest page size and last turn and
// stores them. It never keeps the handle open.
func (a *App) backfillOneWorldHealth(ctx context.Context, world registry.World) error {
	handle, err := a.worlds.Open(ctx, world.ID)
	if err != nil {
		return err
	}
	defer func() { _ = handle.Close() }()

	pageBytes := 0
	err = handle.DB.QueryRowContext(ctx,
		`SELECT length(css) + length(html) FROM site_versions ORDER BY id DESC LIMIT 1`,
	).Scan(&pageBytes)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}

	var (
		lastTurn string
		status   string
	)
	err = handle.DB.QueryRowContext(ctx,
		`SELECT created_at, status FROM turns
		 ORDER BY (kind = 'changeset') DESC, id DESC
		 LIMIT 1`,
	).Scan(&lastTurn, &status)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	var lastTurnAt time.Time
	if lastTurn != "" {
		if lastTurnAt, err = registry.ParseTimestamp(lastTurn); err != nil {
			return err
		}
	}
	return a.reg.SetWorldHealth(ctx, registry.WorldHealth{
		WorldID:    world.ID,
		LastTurnAt: lastTurnAt,
		LastStatus: status,
		PageBytes:  pageBytes,
	})
}
