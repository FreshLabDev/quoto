// SPDX-License-Identifier: Apache-2.0

package config

import (
	"log/slog"
	"reflect"
	"strings"
	"testing"
)

func lookupFrom(env map[string]string) func(string) (string, bool) {
	return func(k string) (string, bool) { v, ok := env[k]; return v, ok }
}

func base() map[string]string {
	return map[string]string{
		"BOT_TOKEN":          "123:abc",
		"BOT_USERNAME":       "@quototbot",
		"DATABASE_URL":       "postgres://quoto_core:pw@core-postgres:5432/core",
		"OPENROUTER_API_KEY": "or-key",
	}
}

func TestLoadDefaults(t *testing.T) {
	c, err := load(lookupFrom(base()), nil)
	if err != nil {
		t.Fatal(err)
	}
	if c.BotUsername != "quototbot" {
		t.Errorf("username %q", c.BotUsername)
	}
	if c.AnthropicEvalModel != "claude-opus-5-5" || c.AnthropicMediaModel != "claude-sonnet-5-5" {
		t.Errorf("anthropic defaults %q %q", c.AnthropicEvalModel, c.AnthropicMediaModel)
	}
	if c.OpenRouterMediaModel != "google/gemini-3.5-flash-lite" || c.OpenRouterMediaFallback != "google/gemini-3.1-flash-lite" {
		t.Errorf("media models %q %q", c.OpenRouterMediaModel, c.OpenRouterMediaFallback)
	}
	if c.OpenRouterEvalModel != "openai/gpt-6-luna" || c.OpenRouterEvalEffort != "low" || c.OpenRouterMediaEffort != "medium" {
		t.Errorf("openrouter eval defaults %q %q %q", c.OpenRouterEvalModel, c.OpenRouterEvalEffort, c.OpenRouterMediaEffort)
	}
	if !c.MediaAnalysis || c.HTTPAddr != ":8080" || c.AuditLogDir != "logs" || c.LogLevel != slog.LevelInfo {
		t.Errorf("misc defaults %+v", c)
	}
}

func TestLoadRequired(t *testing.T) {
	for _, key := range []string{"BOT_TOKEN", "BOT_USERNAME", "DATABASE_URL"} {
		env := base()
		delete(env, key)
		if _, err := load(lookupFrom(env), nil); err == nil || !strings.Contains(err.Error(), key) {
			t.Errorf("without %s: %v", key, err)
		}
	}
}

func TestLoadRejectsPythonDatabaseURL(t *testing.T) {
	env := base()
	env["DATABASE_URL"] = "postgresql+asyncpg://u:p@h/db"
	if _, err := load(lookupFrom(env), nil); err == nil {
		t.Fatal("asyncpg URL accepted")
	}
}

func TestLoadNeedsOneAIKey(t *testing.T) {
	env := base()
	delete(env, "OPENROUTER_API_KEY")
	if _, err := load(lookupFrom(env), nil); err == nil || !strings.Contains(err.Error(), "ANTHROPIC_API_KEY") {
		t.Fatalf("no AI keys: %v", err)
	}
	env["ANTHROPIC_API_KEY"] = "sk-ant-x"
	if _, err := load(lookupFrom(env), nil); err != nil {
		t.Fatalf("anthropic only: %v", err)
	}
}

func TestLoadFilesDirRequiredWithCustomServer(t *testing.T) {
	env := base()
	env["TELEGRAM_BOT_API_BASE_URL"] = "http://telegram-bot-api:8081/"
	if _, err := load(lookupFrom(env), nil); err == nil || !strings.Contains(err.Error(), "BOT_API_FILES_DIR") {
		t.Fatalf("missing files dir: %v", err)
	}
	env["BOT_API_FILES_DIR"] = "/var/lib/telegram-bot-api/"
	c, err := load(lookupFrom(env), nil)
	if err != nil {
		t.Fatal(err)
	}
	if c.TelegramAPIBase != "http://telegram-bot-api:8081" || c.BotAPIFilesDir != "/var/lib/telegram-bot-api" {
		t.Errorf("not trimmed: %q %q", c.TelegramAPIBase, c.BotAPIFilesDir)
	}
}

func TestLoadIDLists(t *testing.T) {
	cases := []struct {
		raw  string
		want []int64
		bad  bool
	}{
		{"", nil, false},
		{"[5195134444]", []int64{5195134444}, false},
		{"[1, -1002, 3]", []int64{1, -1002, 3}, false},
		{"1,2, 3", []int64{1, 2, 3}, false},
		{"1,x", nil, true},
		{"[1,", nil, true},
	}
	for _, tc := range cases {
		env := base()
		env["DEVELOPER_IDS"] = tc.raw
		c, err := load(lookupFrom(env), nil)
		if tc.bad {
			if err == nil {
				t.Errorf("%q accepted", tc.raw)
			}
			continue
		}
		if err != nil || !reflect.DeepEqual(c.DeveloperIDs, tc.want) {
			t.Errorf("%q: %v %v", tc.raw, c.DeveloperIDs, err)
		}
	}
}

func TestEmptyAnthropicModelSwitchesClaudeOff(t *testing.T) {
	env := base()
	env["ANTHROPIC_API_KEY"] = "k"
	env["ANTHROPIC_EVAL_MODEL"] = ""
	c, err := load(lookupFrom(env), nil)
	if err != nil {
		t.Fatal(err)
	}
	if c.AnthropicEvalModel != "" || c.AnthropicMediaModel == "" {
		t.Errorf("eval %q media %q", c.AnthropicEvalModel, c.AnthropicMediaModel)
	}
}

func TestBlankOpenRouterModelUsesDefault(t *testing.T) {
	env := base()
	env["OPENROUTER_EVAL_MODEL"] = "  "
	c, err := load(lookupFrom(env), nil)
	if err != nil || c.OpenRouterEvalModel != "openai/gpt-6-luna" {
		t.Fatalf("%q %v", c.OpenRouterEvalModel, err)
	}
}

func TestInvalidChoices(t *testing.T) {
	for key, value := range map[string]string{
		"ANTHROPIC_EVAL_EFFORT":             "turbo",
		"OPENROUTER_MEDIA_REASONING_EFFORT": "max",
		"MEDIA_ANALYSIS_ENABLED":            "maybe",
		"LOG_LEVEL":                         "loud",
	} {
		env := base()
		env[key] = value
		if _, err := load(lookupFrom(env), nil); err == nil || !strings.Contains(err.Error(), key) {
			t.Errorf("%s=%s: %v", key, value, err)
		}
	}
}

func TestBoolSpellings(t *testing.T) {
	for raw, want := range map[string]bool{"True": true, "false": false, "1": true, "off": false, "YES": true} {
		got, err := parseBool(raw)
		if err != nil || got != want {
			t.Errorf("%q -> %v %v", raw, got, err)
		}
	}
}

func TestObsoleteNamesAreListedWithoutValues(t *testing.T) {
	env := base()
	env["QUOTE_HOUR"] = "21"
	env["DB_URL"] = "postgresql+asyncpg://secret"
	env["POSTGRES_PASSWORD"] = "secret"
	env["TZ"] = "Europe/Kyiv"
	env["QUOTO_IMAGE"] = "ghcr.io/x"
	c, err := load(lookupFrom(env), nil)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"DB_URL", "QUOTE_HOUR"}
	if !reflect.DeepEqual(c.Obsolete, want) {
		t.Errorf("obsolete %v, want %v", c.Obsolete, want)
	}
}

func TestSecretsIncludeDatabasePassword(t *testing.T) {
	c, err := load(lookupFrom(base()), nil)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, s := range c.Secrets() {
		if s == "pw" {
			found = true
		}
	}
	if !found {
		t.Errorf("secrets %v lack the database password", c.Secrets())
	}
}
