// SPDX-License-Identifier: Apache-2.0

// Package config reads the environment once at startup. Env holds secrets,
// addresses and what differs between the test bot and the production bot;
// everything nobody tunes is a constant next to the code that uses it.
package config

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
)

// Config is the validated process configuration. It is read-only after Load.
type Config struct {
	BotToken    string
	BotUsername string // without the leading @

	// DatabaseURL is a postgres:// URL. search_path is set in code.
	DatabaseURL string

	// TelegramAPIBase is a self-hosted Bot API server. Empty means
	// api.telegram.org. BotAPIFilesDir is the server's data directory as this
	// process sees it; required exactly when TelegramAPIBase is set.
	TelegramAPIBase string
	BotAPIFilesDir  string

	DeveloperIDs   []int64
	PremiumChatIDs map[int64]bool

	AnthropicKey         string
	AnthropicEvalModel   string // empty: Claude does not score the day
	AnthropicEvalEffort  string
	AnthropicMediaModel  string // empty: Claude does not describe images
	AnthropicMediaEffort string

	OpenRouterKey           string
	OpenRouterEvalModel     string
	OpenRouterEvalFallback  string
	OpenRouterMediaModel    string
	OpenRouterMediaFallback string
	OpenRouterEvalEffort    string
	OpenRouterMediaEffort   string

	MediaAnalysis bool

	// AuditLogDir holds ai_audit.jsonl and nothing else.
	AuditLogDir string
	HTTPAddr    string
	LogLevel    slog.Level

	// Obsolete lists variables that were read by the Python version, are set in
	// the environment and no longer have any effect. Names only, never values.
	Obsolete []string
}

// HasAI reports whether at least one provider key is set.
func (c Config) HasAI() bool { return c.AnthropicKey != "" || c.OpenRouterKey != "" }

// Secrets returns every value the logger must never print.
func (c Config) Secrets() []string {
	out := []string{c.BotToken, c.AnthropicKey, c.OpenRouterKey}
	if u, err := url.Parse(c.DatabaseURL); err == nil && u.User != nil {
		if pw, ok := u.User.Password(); ok {
			// The password also travels percent-encoded, as it was written in the URL.
			out = append(out, pw, url.QueryEscape(pw), url.PathEscape(pw), u.User.String())
		}
	}
	// A token in a URL has its colon encoded by some proxies.
	out = append(out, strings.ReplaceAll(c.BotToken, ":", "%3A"))
	return out
}

const (
	defaultOpenRouterEvalModel     = "openai/gpt-6-luna"
	defaultOpenRouterFallbackModel = "google/gemini-3.1-flash-lite"
	// The previous default, google/gemini-2.5-flash-lite, has no zero-data-
	// retention endpoint on OpenRouter, so every request carrying zdr=true was
	// rejected with 404 and fell through to the fallback.
	defaultOpenRouterMediaModel = "google/gemini-3.5-flash-lite"
)

var (
	claudeEfforts     = []string{"low", "medium", "high", "xhigh", "max"}
	openRouterEfforts = []string{"none", "minimal", "low", "medium", "high", "xhigh"}
)

// obsoleteNames are variables the Python version read. They are not an error:
// an old .env keeps working, but the operator is told once that they do nothing.
// POSTGRES_*, QUOTO_* and TZ are compose or build plumbing and are not listed.
var obsoleteNames = []string{
	"COLORS", "DB_URL", "EMOJIS", "ENABLE_DEVELOPERS_NOTIFY", "HEARTBEAT_FILE", "LENGTH_OPTIMAL_MAX",
	"LENGTH_OPTIMAL_MIN", "LOGS_PATH", "MAX_MESSAGES_PER_DAILY_EVAL", "MEDIA_AUDIO_BITRATE",
	"MEDIA_AUDIO_SAMPLE_RATE", "MEDIA_CACHE_PROMPT_VERSION", "MEDIA_COMMAND_TIMEOUT_SECONDS",
	"MEDIA_IMAGE_MAX_SIDE", "MEDIA_IMAGE_QUALITY", "MEDIA_PENDING_ITEM_TIMEOUT_SECONDS",
	"MEDIA_PENDING_RETRY_BATCH_SIZE", "MEDIA_PENDING_RETRY_INTERVAL_SECONDS", "MEDIA_PHASH_DISTANCE",
	"MEDIA_PROVIDER_COOLDOWN_SECONDS", "MEDIA_RETRY_BASE_DELAY_SECONDS", "MEDIA_RETRY_MAX_ATTEMPTS",
	"MEDIA_RETRY_MAX_DELAY_SECONDS", "MEDIA_VIDEO_CRF", "MEDIA_VIDEO_FPS", "MEDIA_VIDEO_LOW_RES_MAX_SECONDS",
	"MEDIA_VIDEO_MAX_HEIGHT", "MEDIA_VIDEO_MAX_SECONDS", "MIN_MESSAGES_FOR_AUTO_REVIEW",
	"ANTHROPIC_EVAL_MAX_TOKENS", "ANTHROPIC_MEDIA_MAX_TOKENS", "OPENROUTER_APP_TITLE", "OPENROUTER_BASE_URL",
	"OPENROUTER_DATA_COLLECTION", "OPENROUTER_EVAL_MAX_TOKENS", "OPENROUTER_HTTP_REFERER",
	"OPENROUTER_MEDIA_AUDIO_MODEL", "OPENROUTER_MEDIA_IMAGE_MODEL", "OPENROUTER_MEDIA_VIDEO_MODEL",
	"OPENROUTER_MODEL", "OPENROUTER_REASONING_EFFORT", "OPENROUTER_ZDR", "QUOTE_HOUR", "QUOTE_MINUTE",
	"QUOTE_MINUTE_JITTER", "SCHEDULER_CONCURRENCY", "TIMEZONE", "WEIGHT_AI", "WEIGHT_LENGTH", "WEIGHT_REACTIONS",
}

// Load reads and validates the process environment.
func Load() (Config, error) {
	return load(os.LookupEnv, os.Environ())
}

func load(lookup func(string) (string, bool), environ []string) (Config, error) {
	var errs []string
	fail := func(format string, args ...any) { errs = append(errs, fmt.Sprintf(format, args...)) }

	get := func(key string) string {
		v, _ := lookup(key)
		return strings.TrimSpace(v)
	}
	// withDefault treats an unset or blank value as "use the default".
	withDefault := func(key, def string) string {
		if v := get(key); v != "" {
			return v
		}
		return def
	}
	// keepEmpty distinguishes unset (default) from set-but-empty. For the
	// Anthropic models an empty value is how Claude is switched off for a job.
	keepEmpty := func(key, def string) string {
		if v, ok := lookup(key); ok {
			return strings.TrimSpace(v)
		}
		return def
	}

	c := Config{
		BotToken:    get("BOT_TOKEN"),
		BotUsername: strings.TrimPrefix(get("BOT_USERNAME"), "@"),
		DatabaseURL: get("DATABASE_URL"),

		TelegramAPIBase: strings.TrimRight(get("TELEGRAM_BOT_API_BASE_URL"), "/"),
		BotAPIFilesDir:  strings.TrimRight(get("BOT_API_FILES_DIR"), "/"),

		AnthropicKey:         get("ANTHROPIC_API_KEY"),
		AnthropicEvalModel:   keepEmpty("ANTHROPIC_EVAL_MODEL", "claude-opus-5-5"),
		AnthropicEvalEffort:  withDefault("ANTHROPIC_EVAL_EFFORT", "medium"),
		AnthropicMediaModel:  keepEmpty("ANTHROPIC_MEDIA_MODEL", "claude-sonnet-5-5"),
		AnthropicMediaEffort: withDefault("ANTHROPIC_MEDIA_EFFORT", "medium"),

		OpenRouterKey:           get("OPENROUTER_API_KEY"),
		OpenRouterEvalModel:     withDefault("OPENROUTER_EVAL_MODEL", defaultOpenRouterEvalModel),
		OpenRouterEvalFallback:  withDefault("OPENROUTER_EVAL_FALLBACK_MODEL", defaultOpenRouterFallbackModel),
		OpenRouterMediaModel:    withDefault("OPENROUTER_MEDIA_MODEL", defaultOpenRouterMediaModel),
		OpenRouterMediaFallback: withDefault("OPENROUTER_MEDIA_FALLBACK_MODEL", defaultOpenRouterFallbackModel),
		OpenRouterEvalEffort:    withDefault("OPENROUTER_EVAL_REASONING_EFFORT", "low"),
		OpenRouterMediaEffort:   withDefault("OPENROUTER_MEDIA_REASONING_EFFORT", "medium"),

		AuditLogDir: withDefault("AUDIT_LOG_DIR", "logs"),
		HTTPAddr:    withDefault("HTTP_ADDR", ":8080"),
	}

	if c.BotToken == "" {
		fail("BOT_TOKEN is required")
	}
	if c.BotUsername == "" {
		fail("BOT_USERNAME is required (the bot's public username)")
	}
	switch {
	case c.DatabaseURL == "":
		fail("DATABASE_URL is required")
	case !strings.HasPrefix(c.DatabaseURL, "postgres://") && !strings.HasPrefix(c.DatabaseURL, "postgresql://"):
		fail("DATABASE_URL must be a postgres:// URL (the Python form postgresql+asyncpg:// is not accepted)")
	}

	if c.TelegramAPIBase != "" {
		if u, err := url.Parse(c.TelegramAPIBase); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			fail("TELEGRAM_BOT_API_BASE_URL must be an http(s) URL")
		}
		if c.BotAPIFilesDir == "" {
			fail("BOT_API_FILES_DIR is required when TELEGRAM_BOT_API_BASE_URL is set")
		}
	}

	var err error
	if c.DeveloperIDs, err = parseIDList(get("DEVELOPER_IDS")); err != nil {
		fail("DEVELOPER_IDS: %v", err)
	}
	premium, err := parseIDList(get("PREMIUM_CHAT_IDS"))
	if err != nil {
		fail("PREMIUM_CHAT_IDS: %v", err)
	}
	c.PremiumChatIDs = make(map[int64]bool, len(premium))
	for _, id := range premium {
		c.PremiumChatIDs[id] = true
	}

	checkChoice := func(key, value string, allowed []string) {
		for _, a := range allowed {
			if value == a {
				return
			}
		}
		fail("%s must be one of %s, got %q", key, strings.Join(allowed, ", "), value)
	}
	checkChoice("ANTHROPIC_EVAL_EFFORT", c.AnthropicEvalEffort, claudeEfforts)
	checkChoice("ANTHROPIC_MEDIA_EFFORT", c.AnthropicMediaEffort, claudeEfforts)
	checkChoice("OPENROUTER_EVAL_REASONING_EFFORT", c.OpenRouterEvalEffort, openRouterEfforts)
	checkChoice("OPENROUTER_MEDIA_REASONING_EFFORT", c.OpenRouterMediaEffort, openRouterEfforts)

	if c.MediaAnalysis, err = parseBool(withDefault("MEDIA_ANALYSIS_ENABLED", "true")); err != nil {
		fail("MEDIA_ANALYSIS_ENABLED: %v", err)
	}

	if c.LogLevel, err = parseLevel(withDefault("LOG_LEVEL", "info")); err != nil {
		fail("LOG_LEVEL: %v", err)
	}

	// A bot with no provider key can collect messages and publish nothing.
	// Python logged a critical line and kept running; that hid the problem.
	if !c.HasAI() {
		fail("ANTHROPIC_API_KEY and OPENROUTER_API_KEY are both empty: set at least one")
	}

	for _, name := range obsoleteNames {
		if _, ok := lookup(name); ok {
			c.Obsolete = append(c.Obsolete, name)
		}
	}
	sort.Strings(c.Obsolete)

	if len(errs) > 0 {
		return Config{}, fmt.Errorf("configuration: %s", strings.Join(errs, "; "))
	}
	return c, nil
}

// parseIDList accepts a JSON array ([1, 2], the form the Python settings
// required, so existing .env files keep parsing) or a comma list (1,2). Blank
// means empty.
func parseIDList(raw string) ([]int64, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	if strings.HasPrefix(raw, "[") {
		var ids []int64
		if err := json.Unmarshal([]byte(raw), &ids); err != nil {
			return nil, fmt.Errorf("not a JSON array of integers")
		}
		return ids, nil
	}
	var ids []int64
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		id, err := strconv.ParseInt(part, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("%q is not an integer", part)
		}
		ids = append(ids, id)
	}
	return ids, nil
}

// parseBool accepts the spellings pydantic accepted, so an existing .env
// (MEDIA_ANALYSIS_ENABLED=True) keeps working.
func parseBool(raw string) (bool, error) {
	switch strings.ToLower(raw) {
	case "1", "true", "t", "yes", "y", "on":
		return true, nil
	case "0", "false", "f", "no", "n", "off":
		return false, nil
	}
	return false, fmt.Errorf("%q is not a boolean", raw)
}

func parseLevel(raw string) (slog.Level, error) {
	switch strings.ToLower(raw) {
	case "debug":
		return slog.LevelDebug, nil
	case "info":
		return slog.LevelInfo, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	}
	return slog.LevelInfo, fmt.Errorf("must be debug, info, warn or error, got %q", raw)
}
