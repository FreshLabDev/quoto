// SPDX-License-Identifier: Apache-2.0

package claude

import (
	"math"
	"testing"
)

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

// tests/test_ai_claude.py::test_cache_reads_are_priced_per_model
func TestCostCacheReadPrices(t *testing.T) {
	c := Counts{CacheRead: 1_000_000}
	for model, want := range map[string]float64{
		"claude-sonnet-5-5": 0.2, // $2 x 0.1
		"claude-opus-5-5":   0.2, // $4 x 0.05
	} {
		if got, ok := Cost(model, c); !ok || !near(got, want) {
			t.Errorf("%s = %v, %v; want %v", model, got, ok, want)
		}
	}
	if _, ok := Cost("claude-unknown", c); ok {
		t.Error("an unknown model has no cost")
	}
}

// tests/test_ai_claude.py::test_claude_scores_the_day_with_structured_output
func TestCostPlainCall(t *testing.T) {
	// 1000 in x $2 + 500 out x $10, per million tokens.
	got, ok := Cost("claude-sonnet-5-5", Counts{Input: 1000, Output: 500})
	if !ok || !near(got, 0.007) {
		t.Fatalf("cost = %v, %v", got, ok)
	}
}

func TestCostCacheWrites(t *testing.T) {
	got, ok := Cost("claude-opus-5-5", Counts{CacheCreation: 1_000_000, Input: 1_000_000, Output: 1_000_000})
	// 4 + 4 x 1.25 + 20
	if !ok || !near(got, 29) {
		t.Fatalf("cost = %v, %v", got, ok)
	}
}

func TestPromptCountsEveryInputToken(t *testing.T) {
	if got := (Counts{Input: 10, CacheCreation: 20, CacheRead: 30, Output: 99}).Prompt(); got != 60 {
		t.Fatalf("prompt = %d", got)
	}
}

// tests/test_ai_claude.py::test_server_side_fallback_answer_is_used_and_every_attempt_is_billed
func TestUsageCostBillsEveryIteration(t *testing.T) {
	u := Usage{
		Counts: Counts{Input: 1000, Output: 500},
		Iterations: []Iteration{
			{Type: "message", Model: "claude-sonnet-5-5", Counts: Counts{Input: 1000}},
			{Type: "fallback_message", Model: "claude-sonnet-5", Counts: Counts{Input: 1000, Output: 500}},
		},
	}
	// Declined attempt: 1000 in x $2. Answer: 1000 in x $2 + 500 out x $10.
	got, ok := u.Cost("claude-sonnet-5-5")
	if !ok || !near(got, 0.009) {
		t.Fatalf("cost = %v, %v", got, ok)
	}
}

func TestUsageCostUnpricedIterationMakesItUnknown(t *testing.T) {
	u := Usage{Iterations: []Iteration{
		{Model: "claude-sonnet-5-5", Counts: Counts{Input: 1000}},
		{Model: "claude-future-9", Counts: Counts{Input: 1000}},
	}}
	if got, ok := u.Cost("claude-sonnet-5-5"); ok {
		t.Fatalf("cost = %v, want unknown", got)
	}
}

func TestUsageCostWithoutIterationsUsesRequestedModel(t *testing.T) {
	u := Usage{Counts: Counts{Input: 1000, Output: 500}}
	if got, ok := u.Cost("claude-sonnet-5-5"); !ok || !near(got, 0.007) {
		t.Fatalf("cost = %v, %v", got, ok)
	}
	if _, ok := u.Cost("claude-future-9"); ok {
		t.Fatal("unknown model priced")
	}
}

func TestUsageCostIterationWithoutModelFallsBackToRequested(t *testing.T) {
	u := Usage{Iterations: []Iteration{{Counts: Counts{Input: 1000, Output: 500}}}}
	if got, ok := u.Cost("claude-sonnet-5-5"); !ok || !near(got, 0.007) {
		t.Fatalf("cost = %v, %v", got, ok)
	}
}
