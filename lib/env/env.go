package env

import (
	"os"
	"strconv"
	"strings"
)

type Env = struct {
	// Set DB_MIGRATE_ON_START=true (or 1, t, etc.) to enable. Defaults to false.
	DbMigrateOnStart bool
	Host             string
	Port             string
	// For logging purposes only. Not needed in production, so they're optional.
	LiveReloadHost string
	// For logging purposes only. Not needed in production, so they're optional.
	LiveReloadPort string
	// Model used to rewrite the site. Defaults to the mock's model id, so
	// `task dev` plus `task dev.mock-llm` works with no configuration.
	OpenAIModel string
	// Base URL of the OpenAI-compatible chat completions API. Defaults to the
	// bundled mock on "http://localhost:6701" (`task dev.mock-llm`).
	OpenAIBaseURL string
	// API key for the model provider. Empty means no Authorization header.
	// Usually supplied by the invoking task or shell.
	OpenAIAPIKey string
	// Stable session id sent as x-opencode-session. The OpenCode gateway wants
	// one per conversation; empty means the server generates one at startup.
	OpencodeSessionID string
	// Reasoning effort for models that support it: minimal, low, medium,
	// high, xhigh. Empty leaves the provider default.
	OpenAIReasoningEffort string
	// Path to the sqlite database file. Defaults to "./data/sqlite.db".
	DBPath string
	// Admin password. Empty means the server generates a random one at
	// startup and logs the URL.
	AdminPassword string
	// Optional public URL of the admin panel, such as
	// "https://admin.example.com". The board redirects /admin there when set.
	// Trailing slashes are trimmed.
	AdminURL string
	// Port that serves the admin pages. The board's port redirects /admin
	// here. A separate origin keeps the board's scripts, and the model's, off
	// the origin that holds the admin login cookie. Defaults to the port after
	// PORT, skipping the live reload proxy.
	AdminPort string
	// Tool rounds per mutation before the loop gives up.
	MutationRounds int
	// TLS certificate and key. With both set the server speaks TLS, so
	// browsers get HTTP/2 and every live stream shares one connection.
	// Without them it serves HTTP/1.1 and h2c for a proxy in front.
	TLSCertFile string
	TLSKeyFile  string
}

var Vars *Env

func init() {
	// The process environment is the only input. The Taskfile loads .env
	// before it runs the server, so a binary started by hand does not read it.
	migrate, _ := strconv.ParseBool(os.Getenv("DB_MIGRATE_ON_START"))
	Vars = &Env{
		DbMigrateOnStart:      migrate,
		Host:                  GetEnv("HOST", "localhost"),
		Port:                  GetEnv("PORT", "6930"),
		LiveReloadHost:        os.Getenv("LIVE_RELOAD_PROXY_HOST"),
		LiveReloadPort:        os.Getenv("LIVE_RELOAD_PROXY_PORT"),
		OpenAIModel:           GetEnv("OPENAI_MODEL", "mock-model"),
		OpenAIBaseURL:         GetEnv("OPENAI_BASE_URL", "http://localhost:6701"),
		OpenAIAPIKey:          loadAPIKey(),
		OpencodeSessionID:     os.Getenv("OPENCODE_SESSION_ID"),
		OpenAIReasoningEffort: os.Getenv("OPENAI_REASONING_EFFORT"),
		DBPath:                GetEnv("DB_PATH", "./data/sqlite.db"),
		AdminPassword:         os.Getenv("ADMIN_PASSWORD"),
		AdminURL:              strings.TrimRight(os.Getenv("ADMIN_URL"), "/"),
		AdminPort:             adminPort(GetEnv("PORT", "6930")),
		MutationRounds:        GetEnvInt("MUTATION_ROUNDS", 3),
		TLSCertFile:           os.Getenv("TLS_CERT_FILE"),
		TLSKeyFile:            os.Getenv("TLS_KEY_FILE"),
	}
}

// adminPort is the port the admin listener uses: the one after the board's,
// skipping the live reload proxy so the two never collide in dev.
func adminPort(port string) string {
	if configured := os.Getenv("ADMIN_PORT"); configured != "" {
		return configured
	}
	board, err := strconv.Atoi(port)
	if err != nil {
		return "6932"
	}
	next := board + 1
	if strconv.Itoa(next) == os.Getenv("LIVE_RELOAD_PROXY_PORT") {
		next++
	}
	return strconv.Itoa(next)
}

// loadAPIKey reads OPENAI_API_KEY, which the task or shell can supply. The key
// never appears in logs.
func loadAPIKey() string {
	return strings.TrimSpace(os.Getenv("OPENAI_API_KEY"))
}

func GetEnv(name string, fallback string) string {
	if val := os.Getenv(name); val != "" {
		return val
	}
	return fallback
}

func GetEnvInt(name string, fallback int) int {
	val, err := strconv.Atoi(os.Getenv(name))
	if err != nil {
		return fallback
	}
	return val
}
