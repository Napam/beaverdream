package registry

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

const SettingWorldCreationOpen = "world_creation_open"

// WorldCreationOpen reports whether members may create worlds. A missing row
// means open, matching the migration's default.
func (r *Registry) WorldCreationOpen(ctx context.Context) (bool, error) {
	var value string
	err := r.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, SettingWorldCreationOpen).
		Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return true, nil
	}
	if err != nil {
		return false, fmt.Errorf("read world creation setting: %w", err)
	}
	return value == "1", nil
}

// SetWorldCreationOpen changes the creation policy. Only an active admin may
// write it; existing worlds are unaffected.
func (r *Registry) SetWorldCreationOpen(ctx context.Context, actorID string, open bool) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin world creation setting: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := requireAdminTx(ctx, tx, actorID); err != nil {
		return err
	}
	value := "0"
	if open {
		value = "1"
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO settings (key, value, updated_at)
		VALUES (?, ?, ?) ON CONFLICT(key) DO UPDATE SET
		value = excluded.value, updated_at = excluded.updated_at
		WHERE settings.value <> excluded.value`, SettingWorldCreationOpen, value, r.timestamp()); err != nil {
		return fmt.Errorf("write world creation setting: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit world creation setting: %w", err)
	}
	return nil
}
