package handlers

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"sort"

	"github.com/Napam/beaverdream/db/generated/sqlc"
	"github.com/Napam/beaverdream/lib/webmaster"
)

// overridesFor loads every per-visitor override that belongs to a version. The
// rows are immutable, so the caller can keep the result for the whole request.
// Write paths must treat an error as fatal: silently dropping the map would
// give every visitor the normal page.
func (h *Handlers) overridesFor(
	ctx context.Context,
	versionID int64,
) (map[string]webmaster.Override, error) {
	rows, err := h.queries.ListUserOverrides(ctx, versionID)
	if err != nil {
		return nil, fmt.Errorf("list visitor overrides: %w", err)
	}
	overrides := make(map[string]webmaster.Override, len(rows))
	for _, row := range rows {
		override, err := webmaster.DecodeOverride(row.Fields)
		if err != nil {
			h.logger.Warn(
				"Stored visitor override is not valid JSON",
				slog.Int64("version_id", row.VersionID),
				slog.Any("error", err),
			)
			continue
		}
		override.Name = row.Name
		overrides[row.NameKey] = override
	}
	return overrides, nil
}

// visitorOverride returns the override that applies to one visitor on one
// version. A missing name or a missing row both mean the normal page.
func (h *Handlers) visitorOverride(
	ctx context.Context,
	versionID int64,
	name string,
) *webmaster.Override {
	key := webmaster.OverrideKey(name)
	if key == "" {
		return nil
	}
	row, err := h.queries.GetUserOverride(ctx, sqlc.GetUserOverrideParams{
		VersionID: versionID,
		NameKey:   key,
	})
	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			h.logger.Error("Failed to load visitor override", slog.Any("error", err))
		}
		return nil
	}
	override, err := webmaster.DecodeOverride(row.Fields)
	if err != nil {
		h.logger.Warn("Stored visitor override is not valid JSON", slog.Any("error", err))
		return nil
	}
	override.Name = row.Name
	return &override
}

// createVersion stores a version and the overrides that belong to it in one
// transaction. A version without its overrides would silently give everyone
// the normal page.
func (h *Handlers) createVersion(
	ctx context.Context,
	params sqlc.CreateSiteVersionParams,
	overrides map[string]webmaster.Override,
) (sqlc.SiteVersion, error) {
	tx, err := h.db.BeginTx(ctx, nil)
	if err != nil {
		return sqlc.SiteVersion{}, fmt.Errorf("begin version: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	queries := h.queries.WithTx(tx)
	version, err := queries.CreateSiteVersion(ctx, params)
	if err != nil {
		return sqlc.SiteVersion{}, fmt.Errorf("store site version: %w", err)
	}
	for key, override := range overrides {
		fields, err := webmaster.EncodeOverride(override)
		if err != nil {
			return sqlc.SiteVersion{}, err
		}
		if err := queries.CreateUserOverride(ctx, sqlc.CreateUserOverrideParams{
			VersionID: version.ID,
			Name:      override.Name,
			NameKey:   key,
			Fields:    fields,
		}); err != nil {
			return sqlc.SiteVersion{}, fmt.Errorf("store visitor override: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return sqlc.SiteVersion{}, fmt.Errorf("commit version: %w", err)
	}
	return version, nil
}

// carryVersion copies a version forward with its visitor overrides. Undo,
// restore and heal all use it, so history moves as one piece.
func (h *Handlers) carryVersion(
	ctx context.Context,
	source sqlc.SiteVersion,
	reason string,
) (sqlc.SiteVersion, error) {
	overrides, err := h.overridesFor(ctx, source.ID)
	if err != nil {
		return sqlc.SiteVersion{}, err
	}
	return h.createVersion(ctx, sqlc.CreateSiteVersionParams{
		Css:         source.Css,
		Note:        source.Note,
		Header:      source.Header,
		Board:       source.Board,
		Footer:      source.Footer,
		StateSchema: normalizeStateSchema(source.StateSchema),
		Reason:      reason,
	}, overrides)
}

// sortedOverrides is the admin page's view: stable order, no map in the
// template.
func sortedOverrides(overrides map[string]webmaster.Override) []webmaster.Override {
	keys := make([]string, 0, len(overrides))
	for key := range overrides {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	sorted := make([]webmaster.Override, 0, len(keys))
	for _, key := range keys {
		sorted = append(sorted, overrides[key])
	}
	return sorted
}
