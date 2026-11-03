package main

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/Napam/beaverdream"
	"github.com/Napam/beaverdream/cmd/serve/handlers"
	"github.com/Napam/beaverdream/db"
	"github.com/Napam/beaverdream/db/generated/sqlc"
	"github.com/Napam/beaverdream/lib/env"
	"github.com/Napam/beaverdream/lib/logging"
	"github.com/Napam/beaverdream/lib/webmaster"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	_ "modernc.org/sqlite"
)

func main() {
	handler := logging.NewHandler(&slog.HandlerOptions{
		Level: slog.LevelInfo,
	})
	logger := slog.New(handler)

	if err := run(logger); err != nil {
		logger.Error("Server exited with error", slog.Any("error", err))
		os.Exit(1)
	}
}

// run wires up dependencies and starts the HTTP server, returning any fatal
// startup or serving error so main stays a one-liner.
func run(logger *slog.Logger) error {
	if cwd, err := os.Getwd(); err == nil {
		logger.Info("Initializing beaverdream", slog.String("cwd", cwd))
	}

	dbConn, err := openSQLite(env.Vars.DBPath)
	if err != nil {
		return err
	}
	defer func() {
		if err := dbConn.Close(); err != nil {
			logger.Error("Failed to close database", slog.Any("error", err))
		}
	}()

	if env.Vars.DbMigrateOnStart {
		if _, err := db.RunMigrations(
			context.Background(),
			beaverdream.MigrationsFS,
			dbConn,
			logger,
		); err != nil {
			return fmt.Errorf("run migrations: %w", err)
		}
	}

	queries := sqlc.New(dbConn)

	sessionID := resolveSessionID(env.Vars.OpenAIBaseURL, env.Vars.OpenAISessionID)
	clientOpts := []option.RequestOption{
		option.WithBaseURL(env.Vars.OpenAIBaseURL),
		option.WithHeader("User-Agent", "beaverdream/0.1"),
	}
	if env.Vars.OpenAIAPIKey != "" {
		clientOpts = append(clientOpts, option.WithAPIKey(env.Vars.OpenAIAPIKey))
	}
	if sessionID != "" {
		clientOpts = append(clientOpts, option.WithHeader("x-opencode-session", sessionID))
	}
	client := openai.NewClient(clientOpts...)
	mutator := webmaster.New(
		client,
		env.Vars.OpenAIModel,
		env.Vars.MutationRounds,
		env.Vars.OpenAIReasoningEffort,
	)

	adminToken, err := resolveAdminToken(logger, env.Vars.AdminToken)
	if err != nil {
		return err
	}

	h := handlers.New(logger, dbConn, queries, mutator, handlers.Config{
		AdminToken:         adminToken,
		MaxMutationsPerDay: env.Vars.MaxMutationsPerDay,
	})
	defer h.Close()

	router := SetupRouter(logger, h)

	logAttrs := []slog.Attr{
		slog.String("address", env.Vars.Host+":"+env.Vars.Port),
		slog.String("db_path", env.Vars.DBPath),
		slog.String("openai_base_url", env.Vars.OpenAIBaseURL),
		slog.String("openai_model", env.Vars.OpenAIModel),
		slog.Bool("openai_api_key_set", env.Vars.OpenAIAPIKey != ""),
		slog.String("openai_reasoning_effort", env.Vars.OpenAIReasoningEffort),
	}
	if env.Vars.LiveReloadHost != "" && env.Vars.LiveReloadPort != "" {
		logAttrs = append(logAttrs, slog.String(
			"live_reload_address",
			env.Vars.LiveReloadHost+":"+env.Vars.LiveReloadPort+" (you have to use 'task dev' for this to work)",
		))
	}
	logger.LogAttrs(context.Background(), slog.LevelInfo, "Server running", logAttrs...)

	srv := &http.Server{
		Addr:              env.Vars.Host + ":" + env.Vars.Port,
		Handler:           router,
		ReadHeaderTimeout: 10 * time.Second,
	}
	if err := srv.ListenAndServe(); err != nil {
		return fmt.Errorf("listen and serve: %w", err)
	}
	return nil
}

// resolveAdminToken returns the configured token, or generates a random one
// so the admin panel is never open by default. The generated token only lives
// for this process; set ADMIN_TOKEN for a stable one.
func resolveAdminToken(logger *slog.Logger, configured string) (string, error) {
	if configured != "" {
		return configured, nil
	}
	token, err := randomHex(16)
	if err != nil {
		return "", fmt.Errorf("generate admin token: %w", err)
	}
	host := env.Vars.Host
	if host == "0.0.0.0" || host == "" {
		host = "localhost"
	}
	logger.Warn(
		"ADMIN_TOKEN is not set, generated one for this run",
		slog.String("admin_url", "http://"+host+":"+env.Vars.Port+"/admin?token="+token),
	)
	return token, nil
}

// resolveSessionID returns a stable id for OpenCode Go routing. Other providers
// neither need nor understand the header, so it stays empty for them.
func resolveSessionID(baseURL, configured string) string {
	if configured != "" {
		return configured
	}
	if !strings.Contains(baseURL, "opencode.ai") {
		return ""
	}
	id, err := randomHex(8)
	if err != nil {
		return "beaverdream"
	}
	return "beaverdream-" + id
}

func randomHex(size int) (string, error) {
	buf := make([]byte, size)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

// SetupRouter registers every route. The board is three handlers; the admin
// panel is the fourth.
func SetupRouter(logger *slog.Logger, h *handlers.Handlers) *chi.Mux {
	router := chi.NewRouter()
	router.Use(middleware.Recoverer)

	beaverdream.SetupStatic(router)

	router.Get("/", h.Index)
	router.Get("/identity", h.Identity)
	router.Post("/identity", h.SetIdentity)
	router.Post("/posts", h.CreatePost)
	router.Get("/version", h.Version)
	router.Get("/version/{id}/css", h.VersionCSS)
	router.Get("/x/state", h.State)
	router.Post("/x/state/{key}", h.SetState)

	router.Get("/admin", h.Admin)
	router.Get("/admin/", h.Admin)
	router.Get("/admin/status", h.AdminStatus)
	router.Post("/admin/mutate", h.AdminMutate)
	router.Post("/admin/undo", h.UndoLast)
	router.Post("/admin/heal", h.HealBoard)
	router.Post("/admin/versions/{id}/restore", h.RestoreVersion)
	router.Post("/admin/reset", h.ResetBoard)

	logger.Debug("Routes registered")
	return router
}

// sqlitePragmas configure every connection: foreign keys (off by default in
// SQLite), a busy timeout so concurrent writers wait instead of failing
// immediately, and WAL plus NORMAL sync so readers don't block the writer.
const sqlitePragmas = "_pragma=busy_timeout(5000)" +
	"&_pragma=foreign_keys(1)" +
	"&_pragma=journal_mode(WAL)" +
	"&_pragma=synchronous(NORMAL)"

// openSQLite opens the database. The pragmas travel in the DSN so they apply
// to every pooled connection, not just the first.
func openSQLite(path string) (*sql.DB, error) {
	separator := "?"
	if strings.Contains(path, "?") {
		separator = "&"
	}
	dbConn, err := sql.Open("sqlite", path+separator+sqlitePragmas)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	return dbConn, nil
}
