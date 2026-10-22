// SPDX-License-Identifier: Apache-2.0

package ai

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/FreshLabDev/quoto/internal/i18n"
)

// doc section 6 "Python semantics" (key order in schemas): what OpenRouter gets
// as response_format and what Claude gets as the schema are the strings the
// Python code produced, compared byte for byte.
func TestSchemasMatchPython(t *testing.T) {
	cases := []struct {
		name       string
		schema     responseSchema
		formatFile string
		claudeFile string
		wantName   string
	}{
		{"day", daySchema, "response_format_day.json", "claude_schema_day.json", "quoto_day_verdict"},
		{"day with language", dayLanguageSchema, "response_format_day_language.json", "claude_schema_day_language.json", "quoto_day_verdict_language"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got, want := string(tc.schema.format), golden(t, tc.formatFile); got != want {
				t.Errorf("response_format differs\n got: %s\nwant: %s", got, want)
			}
			if got, want := string(tc.schema.claude), golden(t, tc.claudeFile); got != want {
				t.Errorf("claude schema differs\n got: %s\nwant: %s", got, want)
			}
			if tc.schema.name != tc.wantName {
				t.Errorf("name = %q", tc.schema.name)
			}
		})
	}
}

func TestSchemaFor(t *testing.T) {
	if schemaFor(false).name != "quoto_day_verdict" || schemaFor(true).name != "quoto_day_verdict_language" {
		t.Error("wrong schema picked")
	}
}

// tests/test_ai.py (AI-13) and tests/test_ai_claude.py::test_claude_schema_* (CLA-01):
// OpenRouter keeps the bounds, Claude's copy has none, the language block is
// required exactly when it is asked for, and the enum is the supported list.
func TestSchemaBoundsAndLanguage(t *testing.T) {
	full := string(dayLanguageSchema.format)
	for _, want := range []string{`"maxItems":10`, `"minimum":0`, `"maximum":10`, `"maxLength":200`, `"maxLength":80`} {
		if !strings.Contains(full, want) {
			t.Errorf("OpenRouter schema lost %s", want)
		}
	}
	claude := string(dayLanguageSchema.claude)
	for _, banned := range []string{"maxItems", "minimum", "maximum", "maxLength", "minLength", "exclusive", "multipleOf"} {
		if strings.Contains(claude, banned) {
			t.Errorf("Claude schema still has %s", banned)
		}
	}

	var doc struct {
		Required   []string `json:"required"`
		Properties map[string]struct {
			Properties map[string]struct {
				Enum []string `json:"enum"`
			} `json:"properties"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(dayLanguageSchema.claude, &doc); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(doc.Required, ","); got != "day,quote,messages,language" {
		t.Errorf("required = %s", got)
	}
	if got := strings.Join(doc.Properties["language"].Properties["interface_language"].Enum, ","); got != strings.Join(i18n.PromptCodes(), ",") {
		t.Errorf("enum = %s", got)
	}
	if strings.Contains(string(daySchema.claude), `"language"`) {
		t.Error("the plain day schema has a language block")
	}
}
