// SPDX-License-Identifier: Apache-2.0

package ai

import (
	"encoding/json"
	"testing"

	"github.com/FreshLabDev/quoto/internal/domain"
)

// tests/test_ai.py::test_extract_usage_* (AI-09): a usage that is missing or not
// an object is empty; a number that is not there is nil, never zero; the cost is
// reported only when it is a number.
func TestExtractUsage(t *testing.T) {
	i := func(n int) *int { return &n }
	f := func(x float64) *float64 { return &x }
	cases := []struct {
		name string
		raw  string // the "usage" value; empty means the key was absent
		want domain.TokenUsage
	}{
		{"absent", ``, domain.TokenUsage{}},
		{"null", `null`, domain.TokenUsage{}},
		{"empty object", `{}`, domain.TokenUsage{}},
		{"a string", `"nope"`, domain.TokenUsage{}},
		{"an array", `[1,2]`, domain.TokenUsage{}},
		{"only prompt tokens", `{"prompt_tokens":10}`, domain.TokenUsage{PromptTokens: i(10)}},
		{"everything", `{"prompt_tokens":1200,"completion_tokens":450,"total_tokens":1650,"completion_tokens_details":{"reasoning_tokens":300},"cost":0.00042}`,
			domain.TokenUsage{PromptTokens: i(1200), CompletionTokens: i(450), ReasoningTokens: i(300), TotalTokens: i(1650), CostUSD: f(0.00042)}},
		{"zero is a value", `{"prompt_tokens":0,"cost":0}`, domain.TokenUsage{PromptTokens: i(0), CostUSD: f(0)}},
		{"integer-like strings count, as int() reads them", `{"prompt_tokens":"12","completion_tokens":"x","total_tokens":7.9}`,
			domain.TokenUsage{PromptTokens: i(12), TotalTokens: i(7)}},
		{"cost in exponent form", `{"cost":4.2e-4}`, domain.TokenUsage{CostUSD: f(0.00042)}},
		{"cost as a string is not a number", `{"cost":"0.5"}`, domain.TokenUsage{}},
		{"cost null", `{"cost":null,"prompt_tokens":3}`, domain.TokenUsage{PromptTokens: i(3)}},
		{"details not an object", `{"completion_tokens_details":"x","prompt_tokens":3}`, domain.TokenUsage{PromptTokens: i(3)}},
		{"details null", `{"completion_tokens_details":null}`, domain.TokenUsage{}},
		{"reasoning missing", `{"completion_tokens_details":{}}`, domain.TokenUsage{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := extractUsage(json.RawMessage(tc.raw))
			if !sameUsage(got, tc.want) {
				t.Errorf("got %s, want %s", showUsage(got), showUsage(tc.want))
			}
		})
	}
}

func sameUsage(a, b domain.TokenUsage) bool {
	eqI := func(x, y *int) bool { return (x == nil) == (y == nil) && (x == nil || *x == *y) }
	eqF := func(x, y *float64) bool { return (x == nil) == (y == nil) && (x == nil || *x == *y) }
	return eqI(a.PromptTokens, b.PromptTokens) && eqI(a.CompletionTokens, b.CompletionTokens) &&
		eqI(a.ReasoningTokens, b.ReasoningTokens) && eqI(a.TotalTokens, b.TotalTokens) && eqF(a.CostUSD, b.CostUSD)
}

func showUsage(u domain.TokenUsage) string {
	b, _ := json.Marshal(u)
	return string(b)
}
