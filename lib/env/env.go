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
	// Base URL of the OpenAI-compatible chat completions API. Defaults to
	// "http://localhost:6700"; `task dev` points it at the bundled mock on
	// 6701 instead.
	OpenAIBaseURL string
	// API key for the model provider. Empty means no Authorization header.
	// OPENAI_API_KEY takes precedence; OPENAI_API_KEY_FILE is read if it is
	// not set.
	OpenAIAPIKey string
	// Stable session id sent as x-opencode-session. OpenCode Go wants one per
	// conversation; empty means the server generates one at startup.
	OpenAISessionID string
	// Reasoning effort for models that support it: minimal, low, medium,
	// high, xhigh. Empty leaves the provider default.
	OpenAIReasoningEffort string
	// Path to the sqlite database file. Defaults to "./data/sqlite.db".
	DBPath string
	// Required token for /admin. Empty means the server generates a random
	// one at startup and logs the URL.
	AdminToken string
	// Cost kill switch: mutations allowed per UTC day. Zero disables the cap.
	MaxMutationsPerDay int
	// Tool rounds per mutation before the loop gives up.
	MutationRounds int
}

var Vars *Env

func init() {
	migrate, _ := strconv.ParseBool(os.Getenv("DB_MIGRATE_ON_START"))
	Vars = &Env{
		DbMigrateOnStart:      migrate,
		Host:                  GetEnv("HOST", "localhost"),
		Port:                  GetEnv("PORT", "6930"),
		LiveReloadHost:        os.Getenv("LIVE_RELOAD_PROXY_HOST"),
		LiveReloadPort:        os.Getenv("LIVE_RELOAD_PROXY_PORT"),
		OpenAIModel:           GetEnv("OPENAI_MODEL", "mock-model"),
		OpenAIBaseURL:         GetEnv("OPENAI_BASE_URL", "http://localhost:6700"),
		OpenAIAPIKey:          loadAPIKey(),
		OpenAISessionID:       os.Getenv("OPENAI_SESSION_ID"),
		OpenAIReasoningEffort: os.Getenv("OPENAI_REASONING_EFFORT"),
		DBPath:                GetEnv("DB_PATH", "./data/sqlite.db"),
		AdminToken:            os.Getenv("ADMIN_TOKEN"),
		MaxMutationsPerDay:    GetEnvInt("MAX_MUTATIONS_PER_DAY", 500),
		MutationRounds:        GetEnvInt("MUTATION_ROUNDS", 3),
	}
}

// loadAPIKey reads OPENAI_API_KEY, falling back to the file named by
// OPENAI_API_KEY_FILE. The key never appears in logs.
func loadAPIKey() string {
	if value := os.Getenv("OPENAI_API_KEY"); value != "" {
		return strings.TrimSpace(value)
	}
	path := os.Getenv("OPENAI_API_KEY_FILE")
	if path == "" {
		return ""
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
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
