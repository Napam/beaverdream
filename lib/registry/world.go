package registry

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// World statuses. creating and deleting are recoverable: a start finishes or
// removes them.
const (
	WorldCreating = "creating"
	WorldReady    = "ready"
	WorldDeleting = "deleting"
)

// World is one row of the world index. The file path is derived from ID under
// WORLDS_DIR and is never stored, so a slug never touches the filesystem.
type World struct {
	ID        string
	Slug      string
	Status    string
	CreatedBy string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// CreateWorldRecord inserts a world in the creating state. The worlds package
// is the normal caller: it creates the file and flips the row to ready.
func (r *Registry) CreateWorldRecord(
	ctx context.Context,
	id, slug, createdBy string,
) (*World, error) {
	if id == "" || slug == "" {
		return nil, fmt.Errorf("%w: world id and slug are required", ErrInvalidInput)
	}
	nowTime := r.now().UTC()
	now := FormatTimestamp(nowTime)
	var creator any
	if createdBy != "" {
		creator = createdBy
	}
	if _, err := r.db.ExecContext(ctx,
		`INSERT INTO worlds (id, slug, status, created_by, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		id, slug, WorldCreating, creator, now, now,
	); err != nil {
		if isUniqueViolation(err) {
			return nil, ErrWorldExists
		}
		return nil, fmt.Errorf("insert world: %w", err)
	}
	return &World{
		ID:        id,
		Slug:      slug,
		Status:    WorldCreating,
		CreatedBy: createdBy,
		CreatedAt: nowTime,
		UpdatedAt: nowTime,
	}, nil
}

// SetWorldStatus moves one world row and touches updated_at.
func (r *Registry) SetWorldStatus(ctx context.Context, id, status string) error {
	switch status {
	case WorldCreating, WorldReady, WorldDeleting:
	default:
		return fmt.Errorf("%w: bad world status %q", ErrInvalidInput, status)
	}
	res, err := r.db.ExecContext(ctx,
		`UPDATE worlds SET status = ?, updated_at = ? WHERE id = ?`,
		status, r.timestamp(), id,
	)
	if err != nil {
		return fmt.Errorf("set world status: %w", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("set world status rows: %w", err)
	}
	if affected == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteWorldRecord removes the index row.
func (r *Registry) DeleteWorldRecord(ctx context.Context, id string) error {
	res, err := r.db.ExecContext(ctx, `DELETE FROM worlds WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete world record: %w", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("delete world record rows: %w", err)
	}
	if affected == 0 {
		return ErrNotFound
	}
	return nil
}

// GetWorld reads one world row.
func (r *Registry) GetWorld(ctx context.Context, id string) (*World, error) {
	world, err := scanWorld(r.db.QueryRowContext(ctx,
		`SELECT id, slug, status, COALESCE(created_by, ''), created_at, updated_at
		 FROM worlds WHERE id = ?`,
		id,
	))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("read world: %w", err)
	}
	return world, nil
}

// GetWorldBySlug reads one world row by its unique slug.
func (r *Registry) GetWorldBySlug(ctx context.Context, slug string) (*World, error) {
	world, err := scanWorld(r.db.QueryRowContext(ctx,
		`SELECT id, slug, status, COALESCE(created_by, ''), created_at, updated_at
		 FROM worlds WHERE slug = ?`,
		slug,
	))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("read world by slug: %w", err)
	}
	return world, nil
}

// ListWorlds returns every world, ready or not, newest first.
func (r *Registry) ListWorlds(ctx context.Context) ([]World, error) {
	return r.listWorlds(ctx,
		`SELECT id, slug, status, COALESCE(created_by, ''), created_at, updated_at
		 FROM worlds ORDER BY created_at DESC, id DESC`,
	)
}

// ListReadyWorlds is the lobby list.
func (r *Registry) ListReadyWorlds(ctx context.Context) ([]World, error) {
	return r.listWorlds(ctx,
		`SELECT id, slug, status, COALESCE(created_by, ''), created_at, updated_at
		 FROM worlds WHERE status = 'ready'
		 ORDER BY created_at ASC, id ASC`,
	)
}

// WorldsWithStatus is the recovery list.
func (r *Registry) WorldsWithStatus(ctx context.Context, status string) ([]World, error) {
	return r.listWorlds(ctx,
		`SELECT id, slug, status, COALESCE(created_by, ''), created_at, updated_at
		 FROM worlds WHERE status = ?
		 ORDER BY created_at ASC, id ASC`,
		status,
	)
}

func (r *Registry) listWorlds(
	ctx context.Context,
	query string,
	args ...any,
) ([]World, error) {
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list worlds: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var worlds []World
	for rows.Next() {
		world, err := scanWorld(rows)
		if err != nil {
			return nil, err
		}
		worlds = append(worlds, *world)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list worlds rows: %w", err)
	}
	return worlds, nil
}

func scanWorld(row rowScanner) (*World, error) {
	var (
		world     World
		createdAt string
		updatedAt string
	)
	if err := row.Scan(
		&world.ID, &world.Slug, &world.Status, &world.CreatedBy,
		&createdAt, &updatedAt,
	); err != nil {
		return nil, err
	}
	t, err := scanTime(createdAt)
	if err != nil {
		return nil, err
	}
	world.CreatedAt = t
	t, err = scanTime(updatedAt)
	if err != nil {
		return nil, err
	}
	world.UpdatedAt = t
	return &world, nil
}
