// Package worlds owns world files on disk and their registry rows. A world is
// one sqlite file at <WORLDS_DIR>/<id>.db. The path comes from the immutable
// random id, never from the slug, so a user-chosen name cannot touch the
// filesystem.
//
// The manager does not hold open databases, hubs, workers or mutators. A
// caller opens a Handle, uses it, and closes it, which keeps the runtime
// lifecycle (draining streams and workers) in the handler layer. Open runs the
// world migrations every time; they are idempotent, so a world that sat at an
// older version catches up the next time it opens.
package worlds

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"regexp"

	"github.com/Napam/beaverdream/db"
	"github.com/Napam/beaverdream/lib/registry"
	_ "modernc.org/sqlite"
)

// Errors callers can tell apart.
var (
	ErrInvalidSlug = errors.New("worlds: invalid slug")
	ErrNotFound    = errors.New("worlds: not found")
	ErrForbidden   = errors.New("worlds: forbidden")
	ErrNotReady    = registry.ErrWorldNotReady
)

var slugPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,47}$`)

// Manager creates, lists, opens, deletes and recovers world files.
type Manager struct {
	registry   *registry.Registry
	dir        string
	migrations fs.FS
	logger     *slog.Logger
	newID      func() (string, error)
	poolSize   int
}

type options struct {
	newID    func() (string, error)
	poolSize int
}

// Option changes NewManager's defaults.
type Option func(*options)

// WithIDGenerator replaces the random world id generator. Tests use it.
func WithIDGenerator(newID func() (string, error)) Option {
	return func(o *options) { o.newID = newID }
}

// WithPoolSize caps each world handle's open connections. Defaults to 8.
func WithPoolSize(size int) Option {
	return func(o *options) { o.poolSize = size }
}

// NewManager builds a manager. The migrations are the world set, never the
// registry set. A nil logger discards migration logs.
func NewManager(
	reg *registry.Registry,
	dir string,
	migrations fs.FS,
	logger *slog.Logger,
	opts ...Option,
) *Manager {
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	o := options{
		newID:    randomID,
		poolSize: 8,
	}
	for _, opt := range opts {
		opt(&o)
	}
	if o.newID == nil {
		o.newID = randomID
	}
	if o.poolSize <= 0 {
		o.poolSize = 8
	}
	return &Manager{
		registry:   reg,
		dir:        dir,
		migrations: migrations,
		logger:     logger,
		newID:      o.newID,
		poolSize:   o.poolSize,
	}
}

// Handle is one open world database. The caller owns it and must Close it
// after draining its workers and streams.
type Handle struct {
	Info registry.World
	DB   *sql.DB
	Path string
}

// Close closes the world database.
func (h *Handle) Close() error {
	if h == nil || h.DB == nil {
		return nil
	}
	return h.DB.Close()
}

// Dir is the configured world file directory.
func (m *Manager) Dir() string { return m.dir }

// Path returns the file a world id maps to.
func (m *Manager) Path(id string) string {
	return filepath.Join(m.dir, id+".db")
}

// Create makes a world file and its registry row. Only an admin may call it.
// The row is inserted as creating, the file is migrated, then the row becomes
// ready. A failure before ready removes both, so a returned error leaves no
// orphan. A crash leaves a creating row plus a file, which Recover finishes.
func (m *Manager) Create(
	ctx context.Context,
	actorID, slug string,
) (*registry.World, error) {
	if err := m.registry.RequireAdmin(ctx, actorID); err != nil {
		return nil, err
	}
	if err := ValidateSlug(slug); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(m.dir, 0o755); err != nil {
		return nil, fmt.Errorf("create worlds folder: %w", err)
	}
	id, err := m.newID()
	if err != nil {
		return nil, err
	}
	info, err := m.registry.CreateWorldRecord(ctx, id, slug, actorID)
	if err != nil {
		return nil, err
	}
	conn, err := m.openHandle(ctx, id, true)
	if err != nil {
		m.cleanupFailedCreate(ctx, id)
		return nil, err
	}
	if err := conn.Close(); err != nil {
		m.cleanupFailedCreate(ctx, id)
		return nil, fmt.Errorf("close new world %s: %w", slug, err)
	}
	if err := m.registry.SetWorldStatus(ctx, id, registry.WorldReady); err != nil {
		// The row and file are valid and recoverable as creating; a start
		// finishes them. Do not delete a world the admin may already see.
		return nil, fmt.Errorf("mark world %s ready: %w", slug, err)
	}
	info.Status = registry.WorldReady
	return info, nil
}

func (m *Manager) cleanupFailedCreate(ctx context.Context, id string) {
	if err := removeWorldFiles(m.Path(id)); err != nil {
		m.logger.Error("remove failed world files",
			slog.String("world_id", id), slog.Any("error", err))
	}
	if err := m.registry.DeleteWorldRecord(ctx, id); err != nil &&
		!errors.Is(err, registry.ErrNotFound) {
		m.logger.Error("remove failed world row",
			slog.String("world_id", id), slog.Any("error", err))
	}
}

// Open opens a ready world's file with mode=rw and runs its migrations. It
// never creates a missing file: after a delete, a stale request fails instead
// of rebuilding an orphan world.
func (m *Manager) Open(ctx context.Context, worldID string) (*Handle, error) {
	info, err := m.registry.GetWorld(ctx, worldID)
	if err != nil {
		return nil, err
	}
	if info.Status != registry.WorldReady {
		return nil, ErrNotReady
	}
	conn, err := m.openHandle(ctx, info.ID, false)
	if err != nil {
		return nil, err
	}
	return &Handle{Info: *info, DB: conn, Path: m.Path(info.ID)}, nil
}

// OpenBySlug resolves a lobby slug and opens the world.
func (m *Manager) OpenBySlug(ctx context.Context, slug string) (*Handle, error) {
	info, err := m.registry.GetWorldBySlug(ctx, slug)
	if err != nil {
		return nil, err
	}
	return m.Open(ctx, info.ID)
}

func (m *Manager) openHandle(ctx context.Context, id string, create bool) (*sql.DB, error) {
	path := m.Path(id)
	if !create {
		if _, err := os.Stat(path); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return nil, fmt.Errorf("open world %s: %w", id, ErrNotFound)
			}
			return nil, fmt.Errorf("stat world %s: %w", id, err)
		}
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve world path: %w", err)
	}
	dsn := worldDSN(absPath, create)
	conn, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open world %s: %w", id, err)
	}
	conn.SetMaxOpenConns(m.poolSize)
	conn.SetMaxIdleConns(m.poolSize)
	if _, err := db.RunMigrations(ctx, m.migrations, conn, m.logger); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("migrate world %s: %w", id, err)
	}
	return conn, nil
}

// Delete removes a world. Only an admin may call it. The row moves to
// deleting, the database, WAL, SHM and journal files are removed, then the row
// is deleted. The caller must close any Handle for the world before calling,
// and should cancel the world's turn and streams; that runtime draining
// belongs to the handler layer. A crash between the steps leaves a deleting
// row that Recover finishes.
func (m *Manager) Delete(ctx context.Context, actorID, worldID string) error {
	if err := m.registry.RequireAdmin(ctx, actorID); err != nil {
		return err
	}
	info, err := m.registry.GetWorld(ctx, worldID)
	if err != nil {
		return err
	}
	if info.Status != registry.WorldDeleting {
		if err := m.registry.SetWorldStatus(ctx, info.ID, registry.WorldDeleting); err != nil {
			return err
		}
	}
	if err := removeWorldFiles(m.Path(info.ID)); err != nil {
		return fmt.Errorf("remove world files: %w", err)
	}
	return m.registry.DeleteWorldRecord(ctx, info.ID)
}

// Recovery is what Recover did.
type Recovery struct {
	// Finished were creating rows whose file migrated and became ready.
	Finished []string
	// Removed were rows and files that could not be finished or were deleting.
	Removed []string
}

// Recover finishes creating rows and removes deleting rows. It must run before
// the app serves requests. A creating row with a usable file is migrated and
// marked ready; one with a missing or broken file is removed with its files.
// A deleting row is removed with its files.
func (m *Manager) Recover(ctx context.Context) (Recovery, error) {
	var result Recovery
	creating, err := m.registry.WorldsWithStatus(ctx, registry.WorldCreating)
	if err != nil {
		return result, err
	}
	for _, info := range creating {
		path := m.Path(info.ID)
		if _, statErr := os.Stat(path); errors.Is(statErr, os.ErrNotExist) {
			// A crash can remove the main file before its sidecars. Clean
			// them before dropping the row that names the world, or they are
			// orphaned forever.
			if err := removeWorldFiles(path); err != nil {
				return result, fmt.Errorf("remove missing world files: %w", err)
			}
			if err := m.registry.DeleteWorldRecord(ctx, info.ID); err != nil {
				return result, err
			}
			result.Removed = append(result.Removed, info.ID)
			continue
		} else if statErr != nil {
			return result, fmt.Errorf("stat world %s: %w", info.ID, statErr)
		}
		conn, openErr := m.openHandle(ctx, info.ID, false)
		if openErr != nil {
			m.logger.Warn("remove unfinished world",
				slog.String("world_id", info.ID), slog.Any("error", openErr))
			if err := removeWorldFiles(path); err != nil {
				return result, fmt.Errorf("remove broken world files: %w", err)
			}
			if err := m.registry.DeleteWorldRecord(ctx, info.ID); err != nil {
				return result, err
			}
			result.Removed = append(result.Removed, info.ID)
			continue
		}
		if err := conn.Close(); err != nil {
			return result, fmt.Errorf("close recovered world %s: %w", info.ID, err)
		}
		if err := m.registry.SetWorldStatus(ctx, info.ID, registry.WorldReady); err != nil {
			return result, err
		}
		result.Finished = append(result.Finished, info.ID)
	}

	deleting, err := m.registry.WorldsWithStatus(ctx, registry.WorldDeleting)
	if err != nil {
		return result, err
	}
	for _, info := range deleting {
		if err := removeWorldFiles(m.Path(info.ID)); err != nil {
			return result, fmt.Errorf("remove deleting world files: %w", err)
		}
		if err := m.registry.DeleteWorldRecord(ctx, info.ID); err != nil {
			return result, err
		}
		result.Removed = append(result.Removed, info.ID)
	}
	return result, nil
}

// ValidateSlug accepts 1 to 48 lowercase letters, digits and hyphens, starting
// with a letter or digit.
func ValidateSlug(slug string) error {
	if !slugPattern.MatchString(slug) {
		return fmt.Errorf("%w: use 1 to 48 lowercase letters, digits or hyphens", ErrInvalidSlug)
	}
	return nil
}

func worldDSN(path string, create bool) string {
	// A file: URI with an explicit mode is the only way to tell SQLite not to
	// create a missing file. Without mode=rw, opening a deleted world would
	// rebuild an empty database between the registry check and the query.
	mode := "rw"
	if create {
		mode = "rwc"
	}
	return (&url.URL{Scheme: "file", Path: path}).String() +
		"?mode=" + mode +
		"&_pragma=busy_timeout(5000)" +
		"&_pragma=foreign_keys(1)" +
		"&_pragma=journal_mode(WAL)" +
		"&_pragma=synchronous(NORMAL)"
}

func removeWorldFiles(path string) error {
	for _, suffix := range []string{"", "-wal", "-shm", "-journal"} {
		if err := os.Remove(path + suffix); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

func randomID() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("world id: %w", err)
	}
	return hex.EncodeToString(buf), nil
}
