package env

import (
	"os"
	"strconv"
	"strings"
	"time"
)

// Env is the process configuration. The process environment is the only
// input; the Taskfile loads .env before it runs the server.
type Env struct {
	Host string
	// AppPort is the management plane: login, account, API tokens, invites,
	// the lobby and every admin page.
	AppPort string
	// PlayPort serves the worlds under /worlds/{slug}. It never serves login,
	// invites or admin.
	PlayPort string
	// AppURL and PlayURL are the public origins when a proxy or two hosts
	// front the server. Empty derives the origin from the request.
	AppURL  string
	PlayURL string

	// TLS certificate and key. With both set the server speaks TLS, so
	// browsers get HTTP/2 and every live stream shares one connection.
	// Without them it serves HTTP/1.1 and h2c for a proxy in front.
	TLSCertFile string
	TLSKeyFile  string

	// RegistryPath is the registry SQLite file. It holds identities, logins,
	// sessions, invites, roles, usage and the worlds table. WorldsDir holds
	// one <world-id>.db per world; the slug never reaches the path.
	RegistryPath string
	WorldsDir    string

	// AdminPassword bootstraps the admin identity on every start. Empty makes
	// the server generate one and log it.
	AdminPassword string

	// AppSessionTTL bounds an app session. PlaySessionTTL is zero by default,
	// so a play session does not expire.
	AppSessionTTL  time.Duration
	PlaySessionTTL time.Duration
	// HandoffTTL bounds the one-time code that moves a browser from the app
	// port to the play port.
	HandoffTTL time.Duration

	// Daily token budgets, per UTC day. Zero means unlimited.
	DailyTokensIdentity int64
	DailyTokensGlobal   int64

	// ModelConcurrency caps model turns running at once across every loaded
	// world. WorldIdleTimeout unloads a world with no subscribers, queued
	// posts or running turn; zero never unloads.
	ModelConcurrency int
	WorldIdleTimeout time.Duration

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
	// Tool rounds per changeset before the loop gives up.
	ChangesetRounds int

	// For logging purposes only. Not needed in production, so they're optional.
	LiveReloadHost string
	// For logging purposes only. Not needed in production, so they're optional.
	LiveReloadPort string
}

var Vars *Env

func init() {
	Vars = Load()
}

// Load reads the process environment into a fresh Env.
func Load() *Env {
	return &Env{
		Host:                  GetEnv("HOST", "localhost"),
		AppPort:               GetEnv("APP_PORT", "6930"),
		PlayPort:              GetEnv("PLAY_PORT", "6932"),
		AppURL:                trimURL(os.Getenv("APP_URL")),
		PlayURL:               trimURL(os.Getenv("PLAY_URL")),
		TLSCertFile:           os.Getenv("TLS_CERT_FILE"),
		TLSKeyFile:            os.Getenv("TLS_KEY_FILE"),
		RegistryPath:          GetEnv("REGISTRY_PATH", "./data/registry.db"),
		WorldsDir:             GetEnv("WORLDS_DIR", "./data/worlds"),
		AdminPassword:         os.Getenv("ADMIN_PASSWORD"),
		AppSessionTTL:         GetEnvPositiveDuration("APP_SESSION_TTL", 168*time.Hour),
		PlaySessionTTL:        GetEnvNonNegativeDuration("PLAY_SESSION_TTL", 0),
		HandoffTTL:            GetEnvPositiveDuration("HANDOFF_TTL", time.Minute),
		DailyTokensIdentity:   GetEnvNonNegativeInt64("DAILY_TOKENS_IDENTITY", 1_000_000),
		DailyTokensGlobal:     GetEnvNonNegativeInt64("DAILY_TOKENS_GLOBAL", 5_000_000),
		ModelConcurrency:      GetEnvPositiveInt("MODEL_CONCURRENCY", 1),
		WorldIdleTimeout:      GetEnvNonNegativeDuration("WORLD_IDLE_TIMEOUT", 15*time.Minute),
		OpenAIModel:           GetEnv("OPENAI_MODEL", "mock-model"),
		OpenAIBaseURL:         GetEnv("OPENAI_BASE_URL", "http://localhost:6701"),
		OpenAIAPIKey:          loadAPIKey(),
		OpencodeSessionID:     os.Getenv("OPENCODE_SESSION_ID"),
		OpenAIReasoningEffort: os.Getenv("OPENAI_REASONING_EFFORT"),
		ChangesetRounds:       GetEnvInt("TURN_MAX_ROUNDS", 25),
		LiveReloadHost:        os.Getenv("LIVE_RELOAD_PROXY_HOST"),
		LiveReloadPort:        os.Getenv("LIVE_RELOAD_PROXY_PORT"),
	}
}

// loadAPIKey reads OPENAI_API_KEY, which the task or shell can supply. The key
// never appears in logs.
func loadAPIKey() string {
	return strings.TrimSpace(os.Getenv("OPENAI_API_KEY"))
}

func trimURL(value string) string {
	return strings.TrimRight(value, "/")
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

// GetEnvPositiveInt is GetEnvInt for a value that must be greater than zero. A
// missing, unparseable, zero or negative value uses fallback.
func GetEnvPositiveInt(name string, fallback int) int {
	val, err := strconv.Atoi(os.Getenv(name))
	if err != nil || val <= 0 {
		return fallback
	}
	return val
}

// GetEnvNonNegativeInt64 reads a whole number that may be zero. A missing,
// unparseable or negative value uses fallback.
func GetEnvNonNegativeInt64(name string, fallback int64) int64 {
	val, err := strconv.ParseInt(os.Getenv(name), 10, 64)
	if err != nil || val < 0 {
		return fallback
	}
	return val
}

// GetEnvDuration reads a Go duration such as "2s" or "1h". A missing or
// unparseable value uses fallback. Zero is allowed.
func GetEnvDuration(name string, fallback time.Duration) time.Duration {
	val, err := time.ParseDuration(os.Getenv(name))
	if err != nil {
		return fallback
	}
	return val
}

// GetEnvNonNegativeDuration is GetEnvDuration for a value that may be zero but
// not negative. A missing, unparseable or negative value uses fallback.
func GetEnvNonNegativeDuration(name string, fallback time.Duration) time.Duration {
	val, err := time.ParseDuration(os.Getenv(name))
	if err != nil || val < 0 {
		return fallback
	}
	return val
}

// GetEnvPositiveDuration is GetEnvDuration for a timeout that must be greater
// than zero. A zero or negative value uses fallback.
func GetEnvPositiveDuration(name string, fallback time.Duration) time.Duration {
	val, err := time.ParseDuration(os.Getenv(name))
	if err != nil || val <= 0 {
		return fallback
	}
	return val
}
