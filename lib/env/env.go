package env

import (
	"errors"
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
	// Usually supplied by .env.
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
	// .env is optional. Variables already in the environment win, so shell
	// exports and Taskfile env override the file.
	_ = LoadDotEnv(".env")

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

// loadAPIKey reads OPENAI_API_KEY, which .env can supply. The key never
// appears in logs.
func loadAPIKey() string {
	return strings.TrimSpace(os.Getenv("OPENAI_API_KEY"))
}

// LoadDotEnv applies KEY=value lines from path to the process environment.
// Variables already set win, so a real environment overrides the file. A
// missing file is not an error. Blank lines, comments, an optional "export "
// prefix, and single or double quotes are handled; everything else is left
// alone.
func LoadDotEnv(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}

	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")

		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		value = strings.TrimSpace(value)
		if len(value) >= 2 {
			first, last := value[0], value[len(value)-1]
			if (first == '"' && last == '"') || (first == '\'' && last == '\'') {
				value = value[1 : len(value)-1]
			}
		}

		if _, exists := os.LookupEnv(key); exists {
			continue
		}
		_ = os.Setenv(key, value)
	}
	return nil
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
