package main

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net"
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
	if (env.Vars.TLSCertFile == "") != (env.Vars.TLSKeyFile == "") {
		return fmt.Errorf("TLS_CERT_FILE and TLS_KEY_FILE must be set together")
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

	if env.Vars.AdminPort == env.Vars.Port {
		return fmt.Errorf("ADMIN_PORT must differ from PORT: the admin needs its own origin")
	}

	h := handlers.New(logger, dbConn, queries, mutator, handlers.Config{
		AdminToken:  adminToken,
		AdminPort:   env.Vars.AdminPort,
		Port:        env.Vars.Port,
		DatastarSrc: beaverdream.StaticRootPath("static/datastar.js"),
	})
	defer h.Close()

	router := SetupRouter(logger, h)
	adminRouter := SetupAdminRouter(logger, h)

	logAttrs := []slog.Attr{
		slog.String("address", env.Vars.Host+":"+env.Vars.Port),
		slog.String("admin_address", env.Vars.Host+":"+env.Vars.AdminPort),
		slog.String("db_path", env.Vars.DBPath),
		slog.String("openai_base_url", env.Vars.OpenAIBaseURL),
		slog.String("openai_model", env.Vars.OpenAIModel),
		slog.Bool("openai_api_key_set", env.Vars.OpenAIAPIKey != ""),
		slog.String("openai_reasoning_effort", env.Vars.OpenAIReasoningEffort),
		slog.Bool("tls", env.Vars.TLSCertFile != "" && env.Vars.TLSKeyFile != ""),
	}
	if env.Vars.LiveReloadHost != "" && env.Vars.LiveReloadPort != "" {
		logAttrs = append(logAttrs, slog.String(
			"live_reload_address",
			env.Vars.LiveReloadHost+":"+env.Vars.LiveReloadPort+" (you have to use 'task dev' for this to work)",
		))
	}
	logger.LogAttrs(context.Background(), slog.LevelInfo, "Server running", logAttrs...)

	// Every open tab holds one live stream. Over HTTP/1.1 a browser opens at
	// most six connections per host, so the seventh tab would stall; HTTP/2
	// multiplexes them over one. Browsers only speak HTTP/2 over TLS, so
	// plain mode also accepts h2c from a proxy that terminates TLS. There is
	// no write timeout: a stream stays open for as long as the tab does.
	var protocols http.Protocols
	protocols.SetHTTP1(true)
	protocols.SetHTTP2(true)
	protocols.SetUnencryptedHTTP2(true)
	serve := func(addr string, handler http.Handler, name string) error {
		srv := &http.Server{
			Addr:              addr,
			Handler:           handler,
			ReadHeaderTimeout: 10 * time.Second,
			Protocols:         &protocols,
		}
		if env.Vars.TLSCertFile != "" && env.Vars.TLSKeyFile != "" {
			if err := srv.ListenAndServeTLS(env.Vars.TLSCertFile, env.Vars.TLSKeyFile); err != nil {
				return fmt.Errorf("%s: listen and serve tls: %w", name, err)
			}
			return nil
		}
		if err := srv.ListenAndServe(); err != nil {
			return fmt.Errorf("%s: listen and serve: %w", name, err)
		}
		return nil
	}

	// The admin gets its own listener, so no board is ever served on the
	// origin that holds the login cookie.
	errs := make(chan error, 2)
	go func() { errs <- serve(env.Vars.Host+":"+env.Vars.Port, router, "board") }()
	go func() { errs <- serve(env.Vars.Host+":"+env.Vars.AdminPort, adminRouter, "admin") }()
	if err := <-errs; err != nil {
		return err
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
	switch host {
	case "", "0.0.0.0", "::", "[::]":
		host = "localhost"
	}
	scheme := "http"
	if env.Vars.TLSCertFile != "" && env.Vars.TLSKeyFile != "" {
		scheme = "https"
	}
	logger.Warn(
		"ADMIN_TOKEN is not set, generated one for this run",
		slog.String("admin_url", scheme+"://"+net.JoinHostPort(host, env.Vars.AdminPort)+"/admin"),
		slog.String("admin_password", token),
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

// favicon sends the browser's implicit icon request to the beaver the
// code-owned pages already link.
func favicon(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, webmaster.FaviconPath, http.StatusMovedPermanently)
}

// SetupRouter registers every route. Writes are commands that answer with a
// redirect or 204; /live is the one stream an open page reads from.
func SetupRouter(logger *slog.Logger, h *handlers.Handlers) *chi.Mux {
	router := chi.NewRouter()
	router.Use(middleware.Recoverer)
	// The board's port has no admin routes. It sends /admin to the admin
	// port, whose origin the board's scripts cannot reach.
	router.Use(h.RedirectAdmin)

	beaverdream.SetupStatic(router)

	// A browser that ignores the page's icon link still asks for this.
	router.Get("/favicon.ico", favicon)

	router.Get("/", h.Index)
	router.Get("/identity", h.Identity)
	router.Post("/identity", h.SetIdentity)
	router.Post("/posts", h.CreatePost)
	router.Get("/live", h.Live)
	router.Get("/version", h.Version)
	router.Get("/x/state", h.State)
	router.Post("/x/state/{key}", h.SetState)
	router.Get("/j/{key}", h.JournalRead)
	router.Post("/j/{key}", h.JournalAppend)
	router.Post("/x/err", h.ClientError)

	logger.Debug("Routes registered")
	return router
}

// SetupAdminRouter serves the panel and the assets it loads, and nothing else.
// No board route is mounted here: the model's page never runs on the origin
// that holds the admin login cookie.
func SetupAdminRouter(logger *slog.Logger, h *handlers.Handlers) *chi.Mux {
	router := chi.NewRouter()
	router.Use(middleware.Recoverer)

	beaverdream.SetupStatic(router)

	// A browser that ignores the page's icon link still asks for this.
	router.Get("/favicon.ico", favicon)

	router.Get("/admin", h.Admin)
	router.Post("/admin", h.Admin)
	router.Get("/admin/", h.Admin)
	router.Get("/admin/turns", h.AdminTurns)
	router.Post("/admin/turns", h.AdminTurns)
	router.Get("/admin/turns/{id}", h.AdminTurn)
	router.Post("/admin/turns/{id}", h.AdminTurn)
	router.Get("/admin/site", h.AdminSite)
	router.Post("/admin/site", h.AdminSite)
	router.Get("/admin/live", h.AdminLive)
	router.Post("/admin/mutate", h.AdminMutate)
	router.Post("/admin/retry/{id}", h.RetryTurn)
	router.Post("/admin/stop/{id}", h.StopTurn)
	router.Post("/admin/undo", h.UndoLast)
	router.Post("/admin/heal", h.HealBoard)
	router.Post("/admin/versions/{id}/restore", h.RestoreVersion)
	router.Post("/admin/reset", h.ResetBoard)
	router.Post("/admin/logout", h.AdminLogout)

	logger.Debug("Admin routes registered")
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
	// Every live stream reads on every publish, so several readers are always
	// in flight. Go keeps two idle connections by default and opens a new one
	// when they are busy; each new one reruns the pragmas, and closing it again
	// costs a worker thread. Under 20 players that churn was thousands of
	// connections a second and most of the CPU. Keep one small fixed pool.
	dbConn.SetMaxOpenConns(16)
	dbConn.SetMaxIdleConns(16)
	return dbConn, nil
}
