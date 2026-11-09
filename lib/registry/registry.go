// Package registry owns the instance-wide sqlite file: identities, logins,
// sessions, invites, roles, daily usage and the world index. World files hold
// their own world data and live in WORLDS_DIR; no transaction spans the two.
//
// Every id in this file is 128 random bits written as lowercase hex. Times are
// the same UTC text form the world migrations use
// (strftime('%Y-%m-%dT%H:%M:%fZ')), so an expiry comparison is a plain text
// comparison and the exact expiry instant is refused.
package registry

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Napam/beaverdream/db"
	_ "modernc.org/sqlite"
)

// TimestampLayout is the one timestamp form in the registry and in world
// files. It matches strftime('%Y-%m-%dT%H:%M:%fZ').
const TimestampLayout = "2006-01-02T15:04:05.000Z"

// DayLayout is the UTC date stored in usage.day.
const DayLayout = "2006-01-02"

const (
	// DefaultAppSessionTTL is seven days.
	DefaultAppSessionTTL = 7 * 24 * time.Hour
	// DefaultPlaySessionTTL is zero: play sessions do not expire by default.
	DefaultPlaySessionTTL time.Duration = 0
	// DefaultHandoffTTL is how long a play handoff code lives.
	DefaultHandoffTTL = 60 * time.Second
)

// Errors callers can tell apart. Invite failures share one error on purpose:
// a redeemer must not learn whether a code was unknown, expired, used up or
// revoked.
var (
	ErrNotFound           = errors.New("registry: not found")
	ErrInvalidCredentials = errors.New("registry: invalid credentials")
	ErrSessionInvalid     = errors.New("registry: session invalid")
	ErrInviteInvalid      = errors.New("registry: invite invalid")
	ErrResetInvalid       = errors.New("registry: reset invalid")
	ErrUsernameTaken      = errors.New("registry: username taken")
	ErrForbidden          = errors.New("registry: forbidden")
	ErrBudget             = errors.New("registry: budget exhausted")
	ErrHandoffInvalid     = errors.New("registry: handoff invalid")
	ErrWorldExists        = errors.New("registry: world slug exists")
	ErrWorldNotReady      = errors.New("registry: world not ready")
	ErrInvalidInput       = errors.New("registry: invalid input")
)

// Registry is the instance-wide store. It is safe for concurrent use; sqlite
// serializes writes and the driver waits on its busy timeout.
type Registry struct {
	db       *sql.DB
	hasher   Hasher
	now      func() time.Time
	random   io.Reader
	csrfKey  []byte
	appTTL   time.Duration
	playTTL  time.Duration
	handoff  time.Duration
	closeErr error
}

type options struct {
	hasher  Hasher
	now     func() time.Time
	random  io.Reader
	csrfKey []byte
	appTTL  time.Duration
	playTTL time.Duration
	handoff time.Duration
}

// Option changes Open's defaults.
type Option func(*options)

// WithHasher replaces argon2id. Tests use low memory parameters.
func WithHasher(h Hasher) Option {
	return func(o *options) { o.hasher = h }
}

// WithClock replaces time.Now. Tests use it for expiry boundaries.
func WithClock(now func() time.Time) Option {
	return func(o *options) { o.now = now }
}

// WithRandom replaces crypto/rand.Reader for deterministic ids and tokens.
func WithRandom(r io.Reader) Option {
	return func(o *options) { o.random = r }
}

// WithCSRFKey sets the HMAC key for app-form CSRF tokens. Open generates a
// random key when it is empty, which invalidates outstanding form tokens on a
// restart but not the sessions themselves.
func WithCSRFKey(key []byte) Option {
	return func(o *options) { o.csrfKey = append([]byte(nil), key...) }
}

// WithSessionTTLs sets the expiry used by NewSession for app and play
// sessions. A zero or negative TTL means no expiry.
func WithSessionTTLs(app, play time.Duration) Option {
	return func(o *options) {
		o.appTTL = app
		o.playTTL = play
	}
}

// WithHandoffTTL sets how long a play handoff code is accepted.
func WithHandoffTTL(ttl time.Duration) Option {
	return func(o *options) { o.handoff = ttl }
}

// Open opens the registry, creates its folder when needed, and runs the
// registry migrations. It always migrates: the app cannot start without its
// own schema. A nil logger discards goose output.
func Open(
	ctx context.Context,
	path string,
	migrationsFS fs.FS,
	logger *slog.Logger,
	opts ...Option,
) (*Registry, error) {
	if migrationsFS == nil {
		return nil, errors.New("registry: migrations filesystem is nil")
	}
	if path == "" {
		return nil, errors.New("registry: empty path")
	}
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}

	o := options{
		hasher:  DefaultArgon2id(),
		now:     time.Now,
		random:  rand.Reader,
		appTTL:  DefaultAppSessionTTL,
		playTTL: DefaultPlaySessionTTL,
		handoff: DefaultHandoffTTL,
	}
	for _, opt := range opts {
		opt(&o)
	}
	if o.hasher == nil {
		o.hasher = DefaultArgon2id()
	}
	if o.now == nil {
		o.now = time.Now
	}
	if o.random == nil {
		o.random = rand.Reader
	}
	if len(o.csrfKey) == 0 {
		key := make([]byte, 32)
		if _, err := io.ReadFull(o.random, key); err != nil {
			return nil, fmt.Errorf("registry csrf key: %w", err)
		}
		o.csrfKey = key
	}

	dsn, err := registryDSN(path)
	if err != nil {
		return nil, err
	}
	dbConn, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open registry: %w", err)
	}
	// The registry is a small file with short writes. A handful of
	// connections is enough and keeps the process from opening one per
	// request.
	dbConn.SetMaxOpenConns(8)
	dbConn.SetMaxIdleConns(8)

	r := &Registry{
		db:      dbConn,
		hasher:  o.hasher,
		now:     o.now,
		random:  o.random,
		csrfKey: o.csrfKey,
		appTTL:  o.appTTL,
		playTTL: o.playTTL,
		handoff: o.handoff,
	}
	if _, err := db.RunMigrations(ctx, migrationsFS, dbConn, logger); err != nil {
		_ = dbConn.Close()
		return nil, fmt.Errorf("migrate registry: %w", err)
	}
	return r, nil
}

// Close closes the registry database.
func (r *Registry) Close() error {
	if r == nil || r.db == nil {
		return nil
	}
	r.closeErr = r.db.Close()
	return r.closeErr
}

// DB exposes the underlying handle for read-only tests and migrations. Normal
// code uses the methods on Registry.
func (r *Registry) DB() *sql.DB { return r.db }

func registryDSN(path string) (string, error) {
	file, _, _ := strings.Cut(path, "?")
	if file != "" && !strings.HasPrefix(file, "file:") && !strings.HasPrefix(file, ":") {
		if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
			return "", fmt.Errorf("create registry folder: %w", err)
		}
	}
	separator := "?"
	if strings.Contains(path, "?") {
		separator = "&"
	}
	// _txlock=immediate makes every BeginTx take the write lock up front, so
	// a read-then-write transaction can never fail with SQLITE_BUSY_SNAPSHOT.
	return path + separator +
		"_txlock=immediate" +
		"&_pragma=busy_timeout(5000)" +
		"&_pragma=foreign_keys(1)" +
		"&_pragma=journal_mode(WAL)" +
		"&_pragma=synchronous(NORMAL)", nil
}

func (r *Registry) newID() (string, error) {
	buf := make([]byte, 16)
	if _, err := io.ReadFull(r.random, buf); err != nil {
		return "", fmt.Errorf("registry id: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

func (r *Registry) newToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := io.ReadFull(r.random, buf); err != nil {
		return "", fmt.Errorf("registry token: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

// FormatTimestamp writes a time in the registry's text form.
func FormatTimestamp(t time.Time) string {
	return t.UTC().Format(TimestampLayout)
}

// ParseTimestamp reads the registry's text form.
func ParseTimestamp(value string) (time.Time, error) {
	t, err := time.Parse(TimestampLayout, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse timestamp %q: %w", value, err)
	}
	return t.UTC(), nil
}

func (r *Registry) timestamp() string {
	return FormatTimestamp(r.now())
}

// day returns the UTC day key for a time.
func day(t time.Time) string {
	return t.UTC().Format(DayLayout)
}

func scanTime(value string) (time.Time, error) {
	return ParseTimestamp(value)
}

func nullableTime(value sql.NullString) (*time.Time, error) {
	if !value.Valid || value.String == "" {
		return nil, nil
	}
	t, err := ParseTimestamp(value.String)
	if err != nil {
		return nil, err
	}
	return &t, nil
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	var sqliteErr interface{ Code() int }
	if errors.As(err, &sqliteErr) {
		// SQLITE_CONSTRAINT_PRIMARYKEY (1555) and SQLITE_CONSTRAINT_UNIQUE
		// (2067) both mean a duplicate key.
		switch sqliteErr.Code() {
		case 1555, 2067:
			return true
		}
	}
	return strings.Contains(err.Error(), "UNIQUE constraint failed")
}
