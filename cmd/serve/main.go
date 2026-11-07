package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/Napam/beaverdream"
	"github.com/Napam/beaverdream/cmd/serve/handlers"
	"github.com/Napam/beaverdream/lib/env"
	"github.com/Napam/beaverdream/lib/registry"
	"github.com/Napam/beaverdream/lib/webmaster"
	"github.com/Napam/beaverdream/lib/worlds"
	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	_ "modernc.org/sqlite"
)

func main() {
	handler := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})
	logger := slog.New(handler)

	if err := run(logger); err != nil {
		logger.Error("Server exited with error", slog.Any("error", err))
		os.Exit(1)
	}
}

// run wires up dependencies and starts the app and play listeners, returning
// any fatal startup or serving error so main stays a one-liner.
func run(logger *slog.Logger) error {
	if cwd, err := os.Getwd(); err == nil {
		logger.Info("Initializing beaverdream", slog.String("cwd", cwd))
	}
	if (env.Vars.TLSCertFile == "") != (env.Vars.TLSKeyFile == "") {
		return fmt.Errorf("TLS_CERT_FILE and TLS_KEY_FILE must be set together")
	}

	ctx := context.Background()
	reg, err := registry.Open(
		ctx,
		env.Vars.RegistryPath,
		beaverdream.RegistryMigrationsFS,
		logger,
		registry.WithSessionTTLs(env.Vars.AppSessionTTL, env.Vars.PlaySessionTTL),
		registry.WithHandoffTTL(env.Vars.HandoffTTL),
	)
	if err != nil {
		return fmt.Errorf("open the registry: %w", err)
	}
	defer func() {
		if err := reg.Close(); err != nil {
			logger.Error("Failed to close the registry", slog.Any("error", err))
		}
	}()

	bootstrap, err := reg.EnsureBootstrapAdmin(ctx, env.Vars.AdminPassword)
	if err != nil {
		return fmt.Errorf("bootstrap the admin: %w", err)
	}
	if bootstrap.Generated {
		logger.Warn("ADMIN_PASSWORD is not set, generated one for this run",
			slog.String("admin_username", registry.BootstrapUsername),
			slog.String("admin_password", bootstrap.Password),
		)
	}

	manager := worlds.NewManager(
		reg,
		env.Vars.WorldsDir,
		beaverdream.WorldMigrationsFS,
		logger,
	)
	recovery, err := manager.Recover(ctx)
	if err != nil {
		return fmt.Errorf("recover worlds: %w", err)
	}
	for _, id := range recovery.Finished {
		logger.Info("Finished a world left creating", slog.String("world_id", id))
	}
	for _, id := range recovery.Removed {
		logger.Info("Removed a world left deleting or broken", slog.String("world_id", id))
	}

	sessionID := resolveSessionID(env.Vars.OpenAIBaseURL, env.Vars.OpencodeSessionID)
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
	newRunner := func() *webmaster.ChangesetRunner {
		return webmaster.New(
			client,
			env.Vars.OpenAIModel,
			env.Vars.ChangesetRounds,
			env.Vars.OpenAIReasoningEffort,
		)
	}

	cfg := handlers.Config{
		AppPort:        env.Vars.AppPort,
		AppURL:         env.Vars.AppURL,
		PlayPort:       env.Vars.PlayPort,
		PlayURL:        env.Vars.PlayURL,
		AppSessionTTL:  env.Vars.AppSessionTTL,
		PlaySessionTTL: env.Vars.PlaySessionTTL,
		DatastarSrc:    beaverdream.StaticRootPath("static/datastar.js"),
		ThreeSrc:       beaverdream.StaticRootPath("static/three.module.js"),
		ThreeAddonsSrc: beaverdream.StaticRootPath("static/three-addons.js"),
	}
	app := handlers.NewApp(logger, reg, manager, newRunner, handlers.AppConfig{
		Config: cfg,
		Budget: registry.Budget{
			Identity: env.Vars.DailyTokensIdentity,
			Global:   env.Vars.DailyTokensGlobal,
		},
		ModelConcurrency: env.Vars.ModelConcurrency,
		WorldIdleTimeout: env.Vars.WorldIdleTimeout,
	})
	defer app.Close()

	appRouter := app.AppRouter()
	playRouter := app.PlayRouter()

	logAttrs := []slog.Attr{
		slog.String("app_address", env.Vars.Host+":"+env.Vars.AppPort),
		slog.String("play_address", env.Vars.Host+":"+env.Vars.PlayPort),
		slog.String("registry_path", env.Vars.RegistryPath),
		slog.String("worlds_dir", env.Vars.WorldsDir),
		slog.String("openai_base_url", env.Vars.OpenAIBaseURL),
		slog.String("openai_model", env.Vars.OpenAIModel),
		slog.Bool("openai_api_key_set", env.Vars.OpenAIAPIKey != ""),
		slog.String("openai_reasoning_effort", env.Vars.OpenAIReasoningEffort),
		slog.Bool("tls", env.Vars.TLSCertFile != "" && env.Vars.TLSKeyFile != ""),
	}
	if env.Vars.AppURL != "" {
		logAttrs = append(logAttrs, slog.String("app_url", env.Vars.AppURL))
	}
	if env.Vars.PlayURL != "" {
		logAttrs = append(logAttrs, slog.String("play_url", env.Vars.PlayURL))
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

	errs := make(chan error, 2)
	go func() { errs <- serve(env.Vars.Host+":"+env.Vars.AppPort, appRouter, "app") }()
	go func() { errs <- serve(env.Vars.Host+":"+env.Vars.PlayPort, playRouter, "play") }()
	return <-errs
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
