// SPDX-License-Identifier: Apache-2.0

package ai

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/FreshLabDev/quoto/internal/claude"
	"github.com/FreshLabDev/quoto/internal/domain"
	"github.com/FreshLabDev/quoto/internal/openrouter"
)

// claudeSeq answers the n-th call with replies[n] (a *claude.Response or an
// error); the last reply repeats.
func claudeSeq(replies ...any) func(int, claude.Request) (*claude.Response, error) {
	return func(n int, _ claude.Request) (*claude.Response, error) {
		r := replies[min(n, len(replies)-1)]
		if err, ok := r.(error); ok {
			return nil, err
		}
		return r.(*claude.Response), nil
	}
}

// orSeq is claudeSeq for OpenRouter.
func orSeq(replies ...any) func(int, []byte) (*openrouter.Response, error) {
	return func(n int, _ []byte) (*openrouter.Response, error) {
		r := replies[min(n, len(replies)-1)]
		if err, ok := r.(error); ok {
			return nil, err
		}
		return r.(*openrouter.Response), nil
	}
}

func decode(t *testing.T, doc []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(doc, &m); err != nil {
		t.Fatalf("not JSON: %v\n%.300s", err, doc)
	}
	return m
}

func sub(t *testing.T, m map[string]any, path ...string) any {
	t.Helper()
	var cur any = m
	for _, p := range path {
		obj, ok := cur.(map[string]any)
		if !ok {
			t.Fatalf("%v: %v is not an object", path, cur)
		}
		cur = obj[p]
	}
	return cur
}

func near(a, b float64) bool { return math.Abs(a-b) < 1e-12 }

// ---- Claude ----

// tests/test_ai_claude.py::test_evaluate_messages_with_claude* (CLA-05 .. CLA-08):
// the result, the request body Claude gets, the cost and the audit record.
func TestEvaluateWithClaude(t *testing.T) {
	cfg := fullConfig()
	cfg.AnthropicEvalModel = "claude-sonnet-5-5"
	r := newRig(t, cfg)
	think := 200
	r.claude.reply = func(n int, req claude.Request) (*claude.Response, error) {
		resp := claudeAnswer("claude-sonnet-5-5", dayAnswer)
		resp.Usage.ThinkingTokens = &think
		return resp, nil
	}

	res := r.s.Evaluate(context.Background(), twoMessages(), false)

	if res.Status != domain.RunParsed || res.ActualModel != "claude-sonnet-5-5" || res.RequestedModel != "claude-sonnet-5-5" {
		t.Fatalf("res = %+v", res)
	}
	if got := scoreMap(res.Scores); got[1] != 0.7 || got[2] != 0.8 { // CLA-05
		t.Errorf("scores = %v", res.Scores)
	}
	if res.RequestID != testRequestID || res.DayVerdict == nil || !res.DayVerdict.ShouldPublish || res.QuoteChoice == nil || res.QuoteChoice.PrimaryID != 1 {
		t.Errorf("res = %+v", res)
	}
	// CLA-07: 1000 in x $2 + 500 out x $10 per million = 0.007; thinking tokens kept.
	u := res.Usage
	if *u.PromptTokens != 1000 || *u.CompletionTokens != 500 || *u.TotalTokens != 1500 || *u.ReasoningTokens != 200 || u.CostUSD == nil || !near(*u.CostUSD, 0.007) {
		t.Errorf("usage = %+v cost %v", u, u.CostUSD)
	}

	if len(r.claude.calls) != 1 || r.router.count() != 0 {
		t.Fatalf("claude calls %d, openrouter calls %d", len(r.claude.calls), r.router.count())
	}
	// CLA-06: the request.
	req := r.claude.calls[0]
	if req.Model != "claude-sonnet-5-5" || req.MaxTokens != 32000 || req.Effort != "medium" {
		t.Errorf("req = %+v", req)
	}
	if !strings.Contains(req.System, "quote curator") || !strings.HasSuffix(req.System, injectionGuard) || req.System != dayPrompt+injectionGuard {
		t.Errorf("system = %.200q", req.System)
	}
	if string(req.Schema) != string(daySchema.claude) {
		t.Errorf("the schema is not the stripped day schema")
	}
	if len(req.Messages) != 1 || req.Messages[0].Role != "user" || req.Messages[0].Text != userPayload(twoMessages()) {
		t.Errorf("messages = %+v", req.Messages)
	}
	var payload []map[string]any
	if err := json.Unmarshal([]byte(req.Messages[0].Text), &payload); err != nil || payload[1]["t"] != "second" {
		t.Errorf("payload = %v %v", payload, err)
	}
	wire, err := req.Body()
	if err != nil {
		t.Fatal(err)
	}
	body := decode(t, wire)
	if sub(t, body, "fallbacks") != "default" || sub(t, body, "thinking", "type") != "adaptive" ||
		sub(t, body, "output_config", "format", "type") != "json_schema" {
		t.Errorf("body = %.300s", wire)
	}

	// CLA-08: the audit record.
	if len(r.audit.recs) != 1 {
		t.Fatalf("%d audit records", len(r.audit.recs))
	}
	rec := r.audit.record(t, 0)
	if rec["provider"] != "anthropic" || sub(t, rec, "result", "status") != "parsed" || sub(t, rec, "response", "stop_reason") != "end_turn" {
		t.Errorf("record = %s", r.audit.recs[0])
	}
	if sub(t, rec, "request", "url") != claude.MessagesURL || sub(t, rec, "request", "body", "model") != "claude-sonnet-5-5" {
		t.Errorf("request = %v", rec["request"])
	}
	if _, ok := rec["headers"]; ok {
		t.Error("headers in the audit record") // AI-16
	}
	if strings.Contains(string(r.audit.recs[0]), "sk-ant-test-key") {
		t.Error("the key is in the audit record")
	}
	want := []string{"request_id", "attempt", "provider", "include_day_verdict", "detect_interface_language", "message_count", "request", "usage", "response", "result"}
	if got := r.audit.keys(t, 0); !reflect.DeepEqual(got, want) {
		t.Errorf("keys = %v, want %v", got, want)
	}
	if rec["request_id"] != testRequestID || rec["attempt"] != float64(1) || rec["message_count"] != float64(2) || rec["include_day_verdict"] != true {
		t.Errorf("record = %s", r.audit.recs[0])
	}
}

// tests/test_ai_claude.py::test_server_side_fallback_* (CLA-09): the fallback
// model answered, the cost covers the declined attempt too, and OpenRouter is
// not called.
func TestEvaluateClaudeServerSideFallback(t *testing.T) {
	cfg := fullConfig()
	cfg.AnthropicEvalModel = "claude-sonnet-5-5"
	r := newRig(t, cfg)
	r.claude.reply = func(int, claude.Request) (*claude.Response, error) {
		resp := claudeAnswer("claude-sonnet-5", dayAnswer)
		resp.Usage.Iterations = []claude.Iteration{
			{Type: "message", Model: "claude-sonnet-5-5", Counts: claude.Counts{Input: 1000}},
			{Type: "message", Model: "claude-sonnet-5", Counts: claude.Counts{Input: 1000, Output: 500}},
		}
		return resp, nil
	}
	res := r.s.Evaluate(context.Background(), twoMessages(), false)
	if res.Status != domain.RunParsed || res.ActualModel != "claude-sonnet-5" || res.RequestedModel != "claude-sonnet-5-5" {
		t.Fatalf("res = %+v", res)
	}
	// 1000 x $2 + (1000 x $2 + 500 x $10) = 0.009; tokens are the answering attempt's.
	if c := res.Usage.CostUSD; c == nil || !near(*c, 0.009) {
		t.Errorf("cost = %v", c)
	}
	if *res.Usage.PromptTokens != 1000 || *res.Usage.CompletionTokens != 500 {
		t.Errorf("usage = %+v", res.Usage)
	}
	if r.router.count() != 0 {
		t.Error("OpenRouter was called")
	}
	if !strings.Contains(r.logs.String(), "fallback model answered") {
		t.Error("the fallback is not logged")
	}
}

// tests/test_ai_claude.py::test_claude_refusal_falls_back_to_openrouter (CLA-10).
func TestEvaluateClaudeRefusalGoesToOpenRouter(t *testing.T) {
	cfg := fullConfig()
	cfg.AnthropicEvalModel = "claude-sonnet-5-5"
	r := newRig(t, cfg)
	cat := "general_harms"
	r.claude.reply = func(int, claude.Request) (*claude.Response, error) {
		resp := claudeAnswer("claude-sonnet-5-5", "")
		resp.StopReason = "refusal"
		resp.StopDetails = &claude.StopDetails{Category: &cat}
		return resp, nil
	}
	r.router.reply = orSeq(chatCompletion("openai/gpt-6-luna", dayAnswer))

	res := r.s.Evaluate(context.Background(), twoMessages(), false)

	if len(r.claude.calls) != 1 { // a refusal is not retried
		t.Errorf("%d Claude calls", len(r.claude.calls))
	}
	if res.Status != domain.RunParsed || res.ActualModel != "openai/gpt-6-luna" {
		t.Errorf("res = %+v", res)
	}
	if len(r.audit.recs) != 2 {
		t.Fatalf("%d audit records", len(r.audit.recs))
	}
	raw, _ := json.Marshal(r.audit.record(t, 0)["result"])
	if string(raw) != `{"actual_model":"claude-sonnet-5-5","category":"general_harms","status":"refused"}` {
		t.Errorf("result = %s", raw)
	}
	if got := string(r.audit.recs[0]); !strings.Contains(got, `"result":{"status":"refused","actual_model":"claude-sonnet-5-5","category":"general_harms"}`) {
		t.Errorf("record = %s", got)
	}
	if r.audit.record(t, 1)["provider"] != "openrouter" {
		t.Errorf("second record = %s", r.audit.recs[1])
	}
}

// tests/test_ai_claude.py::test_claude_billing_error_* (CLA-11).
func TestEvaluateClaudeBillingErrorIsNotRetried(t *testing.T) {
	r := newRig(t, fullConfig())
	r.claude.reply = claudeSeq(&claude.APIError{Status: 402, Type: "billing_error", Message: "credit balance is too low"})
	r.router.reply = orSeq(chatCompletion("openai/gpt-6-luna", dayAnswer))

	res := r.s.Evaluate(context.Background(), twoMessages(), false)

	if len(r.claude.calls) != 1 || res.Status != domain.RunParsed || res.ActualModel != "openai/gpt-6-luna" {
		t.Errorf("calls %d res %+v", len(r.claude.calls), res)
	}
	rec := r.audit.record(t, 0)
	if sub(t, rec, "error", "status_code") != float64(402) || sub(t, rec, "error", "error_type") != "billing_error" ||
		sub(t, rec, "error", "type") != "APIError" {
		t.Errorf("error = %v", rec["error"])
	}
	if _, ok := rec["result"]; ok {
		t.Error("a failed call has no result")
	}
	if got := r.audit.keys(t, 0); got[len(got)-1] != "error" || len(got) != 8 {
		t.Errorf("keys = %v", got)
	}
	if len(r.slept()) != 0 {
		t.Errorf("slept %v", r.slept())
	}
}

// tests/test_ai_claude.py::test_claude_retries_* (CLA-12): which errors earn a
// second try, and that there are only two.
func TestEvaluateClaudeRetryRules(t *testing.T) {
	cases := []struct {
		name  string
		err   error
		retry bool
	}{
		{"overloaded in a stream on a 200", &claude.APIError{Status: 200, Type: "overloaded_error"}, true},
		{"api_error", &claude.APIError{Status: 200, Type: "api_error"}, true},
		{"rate_limit_error", &claude.APIError{Status: 200, Type: "rate_limit_error"}, true},
		{"timeout_error", &claude.APIError{Status: 200, Type: "timeout_error"}, true},
		{"429", &claude.APIError{Status: 429}, true},
		{"500", &claude.APIError{Status: 500}, true},
		{"529", &claude.APIError{Status: 529, Type: "overloaded_error"}, true},
		{"connection", &claude.ConnError{Err: claude.ErrTruncated}, true},
		{"idle", &claude.ConnError{Err: claude.ErrIdle}, true},
		{"400", &claude.APIError{Status: 400, Type: "invalid_request_error"}, false},
		{"401", &claude.APIError{Status: 401, Type: "authentication_error"}, false},
		{"402", &claude.APIError{Status: 402, Type: "billing_error"}, false},
		{"403", &claude.APIError{Status: 403}, false},
		{"404", &claude.APIError{Status: 404}, false},
		{"a stream we cannot read", &claude.StreamError{Err: errors.New("bad event")}, false},
		{"an error that is none of ours", errors.New("boom"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(t, fullConfig())
			r.claude.reply = claudeSeq(tc.err, claudeAnswer("claude-opus-5-5", dayAnswer))
			r.router.reply = orSeq(chatCompletion("openai/gpt-6-luna", dayAnswer))
			res := r.s.Evaluate(context.Background(), twoMessages(), false)
			if res.Status != domain.RunParsed {
				t.Fatalf("res = %+v", res)
			}
			if tc.retry {
				if len(r.claude.calls) != 2 || r.router.count() != 0 || res.ActualModel != "claude-opus-5-5" {
					t.Errorf("claude %d, openrouter %d, model %s", len(r.claude.calls), r.router.count(), res.ActualModel)
				}
				if got := r.slept(); !reflect.DeepEqual(got, []time.Duration{5 * time.Second}) { // 4 s + the 1 s test jitter
					t.Errorf("slept %v", got)
				}
			} else if len(r.claude.calls) != 1 || r.router.count() != 1 || len(r.slept()) != 0 {
				t.Errorf("claude %d, openrouter %d, slept %v", len(r.claude.calls), r.router.count(), r.slept())
			}
		})
	}

	t.Run("two attempts and no more", func(t *testing.T) {
		r := newRig(t, fullConfig())
		r.claude.reply = claudeSeq(&claude.APIError{Status: 200, Type: "overloaded_error"})
		r.router.reply = orSeq(chatCompletion("openai/gpt-6-luna", dayAnswer))
		res := r.s.Evaluate(context.Background(), twoMessages(), false)
		if len(r.claude.calls) != 2 || res.ActualModel != "openai/gpt-6-luna" {
			t.Errorf("claude %d, res %+v", len(r.claude.calls), res)
		}
		if got := r.audit.record(t, 1)["attempt"]; got != float64(2) {
			t.Errorf("second attempt is numbered %v", got)
		}
	})
}

// tests/test_ai_claude.py (CLA-13): a refusal with no OpenRouter key fails the
// run, and the text says why.
func TestEvaluateClaudeRefusalWithoutOpenRouter(t *testing.T) {
	cfg := fullConfig()
	cfg.OpenRouterKey = ""
	r := newRig(t, cfg)
	cat := "cyber"
	r.claude.reply = func(int, claude.Request) (*claude.Response, error) {
		resp := claudeAnswer("claude-opus-4-8", "")
		resp.StopReason, resp.StopDetails = "refusal", &claude.StopDetails{Category: &cat}
		return resp, nil
	}
	res := r.s.Evaluate(context.Background(), twoMessages(), false)
	if res.Status != domain.RunAIFailed || res.DayVerdict != nil || !strings.Contains(res.DayVerdictError, "declined") ||
		!strings.Contains(res.DayVerdictError, "cyber") || res.ActualModel != "claude-opus-4-8" {
		t.Errorf("res = %+v", res)
	}
	r2 := newRig(t, cfg)
	r2.claude.reply = func(int, claude.Request) (*claude.Response, error) {
		resp := claudeAnswer("claude-opus-5-5", "")
		resp.StopReason = "refusal"
		return resp, nil
	}
	if res := r2.s.Evaluate(context.Background(), twoMessages(), false); !strings.Contains(res.DayVerdictError, "(no category)") {
		t.Errorf("error = %q", res.DayVerdictError)
	}
}

// tests/test_ai_claude.py (CLA-14): no key, no call, neutral scores.
func TestEvaluateWithoutKeys(t *testing.T) {
	r := newRig(t, Config{OpenRouterEvalModel: "openai/gpt-6-luna"})
	msgs := []domain.EvalMessage{{ID: 5, Author: "A"}, {ID: 3, Author: "B"}, {ID: 9, Author: "C"}}
	res := r.s.Evaluate(context.Background(), msgs, true)
	if res.Status != domain.RunAIFailed || res.DayVerdict != nil || res.DayVerdictError != errNoKeys ||
		res.ActualModel != "openai/gpt-6-luna" || res.RequestedModel != "openai/gpt-6-luna" || res.RequestID != "" {
		t.Errorf("res = %+v", res)
	}
	// The order of the input, not sorted: the scheduler's fallback pick relies on it.
	want := []domain.Score{{ID: 5, Value: 0.5}, {ID: 3, Value: 0.5}, {ID: 9, Value: 0.5}}
	if !reflect.DeepEqual(res.Scores, want) {
		t.Errorf("scores = %v", res.Scores)
	}
	if res.Usage == nil {
		t.Error("usage is nil")
	}
}

// tests/test_ai_claude.py (CLA-15): an empty Claude model switches Claude off.
func TestEvaluateClaudeSwitchedOff(t *testing.T) {
	cfg := fullConfig()
	cfg.AnthropicEvalModel = ""
	r := newRig(t, cfg)
	r.router.reply = orSeq(chatCompletion("openai/gpt-6-luna", dayAnswer))
	res := r.s.Evaluate(context.Background(), twoMessages(), false)
	if res.Status != domain.RunParsed || res.RequestedModel != "openai/gpt-6-luna" || len(r.claude.calls) != 0 {
		t.Errorf("res = %+v, claude calls %d", res, len(r.claude.calls))
	}
}

// doc 10-ai: when one key is empty the chain runs on the other.
func TestEvaluateCandidates(t *testing.T) {
	cases := []struct {
		name string
		edit func(*Config)
		want []candidate
	}{
		{"all", func(*Config) {}, []candidate{
			{providerAnthropic, "claude-opus-5-5"}, {providerOpenRouter, "openai/gpt-6-luna"}, {providerOpenRouter, "google/gemini-3.1-flash-lite"}}},
		{"no Anthropic key", func(c *Config) { c.AnthropicKey = "" }, []candidate{
			{providerOpenRouter, "openai/gpt-6-luna"}, {providerOpenRouter, "google/gemini-3.1-flash-lite"}}},
		{"no OpenRouter key", func(c *Config) { c.OpenRouterKey = "" }, []candidate{{providerAnthropic, "claude-opus-5-5"}}},
		{"no Claude model", func(c *Config) { c.AnthropicEvalModel = "" }, []candidate{
			{providerOpenRouter, "openai/gpt-6-luna"}, {providerOpenRouter, "google/gemini-3.1-flash-lite"}}},
		{"fallback equals primary", func(c *Config) { c.OpenRouterEvalFallback = c.OpenRouterEvalModel }, []candidate{
			{providerAnthropic, "claude-opus-5-5"}, {providerOpenRouter, "openai/gpt-6-luna"}}},
		{"no fallback", func(c *Config) { c.OpenRouterEvalFallback = "" }, []candidate{
			{providerAnthropic, "claude-opus-5-5"}, {providerOpenRouter, "openai/gpt-6-luna"}}},
		{"nothing", func(c *Config) { c.AnthropicKey, c.OpenRouterKey = "", "" }, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := fullConfig()
			tc.edit(&cfg)
			if got := newService(cfg, nil, nil).evalCandidates(); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("got %v\nwant %v", got, tc.want)
			}
		})
	}
}

// Claude's answer that cannot be used: no retry on Claude, down the chain.
func TestEvaluateClaudeBadAnswers(t *testing.T) {
	cases := []struct {
		name       string
		resp       func() *claude.Response
		wantRecord map[string]string // result.status or error.type
	}{
		{"empty text", func() *claude.Response { return claudeAnswer("claude-opus-5-5", "  \n") }, map[string]string{"result.status": "empty_response"}},
		{"not JSON", func() *claude.Response { return claudeAnswer("claude-opus-5-5", "I cannot help with that") }, map[string]string{"error.type": "ParseError"}},
		{"no day block", func() *claude.Response {
			return claudeAnswer("claude-opus-5-5", `{"quote":null,"messages":[{"id":1,"score":4}]}`)
		}, map[string]string{"error.type": "ParseError"}},
		{"entry that is not an object", func() *claude.Response {
			return claudeAnswer("claude-opus-5-5", `{"day":{"should_publish":true},"messages":[5]}`)
		}, map[string]string{"error.type": "ParseError"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(t, fullConfig())
			r.claude.reply = claudeSeq(tc.resp())
			r.router.reply = orSeq(chatCompletion("openai/gpt-6-luna", dayAnswer))
			res := r.s.Evaluate(context.Background(), twoMessages(), false)
			if len(r.claude.calls) != 1 || res.Status != domain.RunParsed || res.ActualModel != "openai/gpt-6-luna" {
				t.Errorf("claude %d res %+v", len(r.claude.calls), res)
			}
			rec := r.audit.record(t, 0)
			for path, want := range tc.wantRecord {
				parts := strings.Split(path, ".")
				if got := sub(t, rec, parts...); got != want {
					t.Errorf("%s = %v, want %s", path, got, want)
				}
			}
		})
	}

	t.Run("max_tokens is only a warning", func(t *testing.T) {
		r := newRig(t, fullConfig())
		resp := claudeAnswer("claude-opus-5-5", dayAnswer)
		resp.StopReason = "max_tokens"
		r.claude.reply = claudeSeq(resp)
		if res := r.s.Evaluate(context.Background(), twoMessages(), false); res.Status != domain.RunParsed {
			t.Errorf("res = %+v", res)
		}
		if !strings.Contains(r.logs.String(), "max_tokens") {
			t.Error("not logged")
		}
	})

	t.Run("a parse failure on the last candidate", func(t *testing.T) {
		cfg := fullConfig()
		cfg.OpenRouterKey = ""
		r := newRig(t, cfg)
		r.claude.reply = claudeSeq(claudeAnswer("claude-opus-5-5", "plain words"))
		res := r.s.Evaluate(context.Background(), twoMessages(), false)
		if res.Status != domain.RunParseFailed || res.DayVerdict != nil || res.DayVerdictError != errEvalFailed || len(res.Scores) != 2 {
			t.Errorf("res = %+v", res)
		}
		if res.Scores[0].Value != 0.5 || res.Usage == nil || *res.Usage.PromptTokens != 1000 {
			t.Errorf("scores %v usage %+v", res.Scores, res.Usage)
		}
	})
}

// ---- OpenRouter ----

// tests/test_ai.py::test_openrouter_request_* (AI-07, AI-08, AI-11): the request
// body, its key order, the usage that comes back.
func TestEvaluateOpenRouterRequestAndUsage(t *testing.T) {
	cfg := fullConfig()
	cfg.AnthropicKey = ""
	r := newRig(t, cfg)
	r.router.reply = orSeq(chatCompletion("openai/gpt-6-luna", dayAnswer))

	res := r.s.Evaluate(context.Background(), twoMessages(), false)
	if res.Status != domain.RunParsed || res.ActualModel != "openai/gpt-6-luna" || res.RequestedModel != "openai/gpt-6-luna" {
		t.Fatalf("res = %+v", res)
	}
	u := res.Usage // AI-08
	if *u.PromptTokens != 1200 || *u.CompletionTokens != 450 || *u.ReasoningTokens != 300 || *u.TotalTokens != 1650 || *u.CostUSD != 0.00042 {
		t.Errorf("usage = %+v", u)
	}

	doc := r.router.docs[0]
	body := decode(t, doc)
	if got := jsonKeys(t, doc); !reflect.DeepEqual(got, []string{"model", "messages", "response_format", "usage", "provider", "max_tokens", "reasoning"}) {
		t.Errorf("keys = %v", got)
	}
	if sub(t, body, "usage", "include") != true || // AI-07
		!reflect.DeepEqual(sub(t, body, "provider"), map[string]any{"data_collection": "deny", "zdr": true}) {
		t.Errorf("body = %.400s", doc)
	}
	if !reflect.DeepEqual(sub(t, body, "reasoning"), map[string]any{"enabled": true, "effort": "low", "exclude": true}) || sub(t, body, "max_tokens") != float64(32000) { // AI-11
		t.Errorf("reasoning/max_tokens: %.400s", doc)
	}
	msgs := body["messages"].([]any)
	system, user := msgs[0].(map[string]any), msgs[1].(map[string]any)
	if system["role"] != "system" || system["content"] != dayPrompt+injectionGuard || user["role"] != "user" || user["content"] != userPayload(twoMessages()) {
		t.Errorf("messages = %.300v", msgs)
	}
	if !strings.Contains(string(doc), string(daySchema.format)) {
		t.Error("response_format is not the day schema with its bounds")
	}
	if r.router.timeouts[0] != 60*time.Second {
		t.Errorf("timeout = %v", r.router.timeouts[0])
	}

	t.Run("no reasoning effort sends no reasoning block", func(t *testing.T) {
		cfg := cfg
		cfg.OpenRouterEvalEffort = ""
		r := newRig(t, cfg)
		r.router.reply = orSeq(chatCompletion("openai/gpt-6-luna", dayAnswer))
		r.s.Evaluate(context.Background(), twoMessages(), false)
		if strings.Contains(string(r.router.docs[0]), `"reasoning"`) {
			t.Error("reasoning sent")
		}
	})
}

// tests/test_ai.py::test_compute_backoff* (AI-26): min(60, 4 * 2^n) plus up to two
// seconds.
func TestBackoff(t *testing.T) {
	s := newService(Config{}, nil, nil)
	cases := []struct {
		attempt int
		base    float64
	}{{0, 4}, {1, 8}, {2, 16}, {3, 32}, {4, 60}, {10, 60}, {40, 60}}
	for _, tc := range cases {
		for _, jitter := range []float64{0, 0.5, 0.999} {
			s.rand = func() float64 { return jitter }
			got := s.backoff(tc.attempt).Seconds()
			if want := tc.base + 2*jitter; math.Abs(got-want) > 1e-6 { // a Duration is whole nanoseconds
				t.Errorf("backoff(%d) with jitter %v = %v, want %v", tc.attempt, jitter, got, want)
			}
		}
	}
	s.rand = func() float64 { return 0.999999 }
	if got := s.backoff(10); got > 62*time.Second {
		t.Errorf("backoff(10) = %v", got)
	}
}

// tests/test_ai.py::test_evaluate_messages_retries_* (AI-15, AI-18, AI-21,
// AI-22): what is retried on one model and how the chain moves on.
func TestEvaluateOpenRouterRetriesAndChain(t *testing.T) {
	cfg := fullConfig()
	cfg.AnthropicKey = ""
	const primary, fallback = "openai/gpt-6-luna", "google/gemini-3.1-flash-lite"
	byModel := func(f func(model string, n int) (*openrouter.Response, error)) func(int, []byte) (*openrouter.Response, error) {
		perModel := map[string]int{}
		return func(_ int, doc []byte) (*openrouter.Response, error) {
			m := requestModel(t, doc)
			n := perModel[m]
			perModel[m]++
			return f(m, n)
		}
	}

	t.Run("a timeout once, then success", func(t *testing.T) { // AI-15
		r := newRig(t, cfg)
		r.router.reply = orSeq(timeoutErr(), chatCompletion(primary, dayAnswer))
		res := r.s.Evaluate(context.Background(), twoMessages(), false)
		if res.Status != domain.RunParsed || r.router.count() != 2 {
			t.Fatalf("res %+v, %d calls", res, r.router.count())
		}
		if got := r.slept(); len(got) != 1 || got[0] < 4*time.Second || got[0] >= 6*time.Second {
			t.Errorf("slept %v", got)
		}
	})

	t.Run("three tries on the primary, then the fallback answers", func(t *testing.T) { // AI-18
		r := newRig(t, cfg)
		r.router.reply = byModel(func(m string, n int) (*openrouter.Response, error) {
			if m == primary {
				return nil, timeoutErr()
			}
			return chatCompletion(fallback, `{"day":{"should_publish":true},"messages":[{"id":1,"score":7}]}`), nil
		})
		res := r.s.Evaluate(context.Background(), twoMessages(), false)
		if got := r.router.models(t); !reflect.DeepEqual(got, []string{primary, primary, primary, fallback}) {
			t.Errorf("models = %v", got)
		}
		if res.Status != domain.RunParsed || res.ActualModel != fallback || res.RequestedModel != primary || scoreMap(res.Scores)[1] != 0.7 {
			t.Errorf("res = %+v", res)
		}
		if got := r.slept(); !reflect.DeepEqual(got, []time.Duration{5 * time.Second, 9 * time.Second}) {
			t.Errorf("slept %v", got)
		}
		if r.s.breakerOpen() || r.s.failures != 0 {
			t.Error("the breaker moved")
		}
	})

	t.Run("every model fails", func(t *testing.T) { // AI-21
		r := newRig(t, cfg)
		r.router.reply = orSeq(timeoutErr())
		msgs := []domain.EvalMessage{{ID: 1, Author: "Alice", Text: "hello"}}
		res := r.s.Evaluate(context.Background(), msgs, false)
		if res.Status != domain.RunAIFailed || res.ActualModel != fallback || res.RequestedModel != primary ||
			!reflect.DeepEqual(res.Scores, []domain.Score{{ID: 1, Value: 0.5}}) || res.DayVerdictError != errEvalFailed || res.DayVerdict != nil {
			t.Errorf("res = %+v", res)
		}
		if r.router.count() != 6 {
			t.Errorf("%d calls", r.router.count())
		}
	})

	t.Run("no fallback configured", func(t *testing.T) { // AI-22
		cfg := cfg
		cfg.OpenRouterEvalFallback = ""
		r := newRig(t, cfg)
		r.router.reply = orSeq(timeoutErr())
		res := r.s.Evaluate(context.Background(), twoMessages(), false)
		if r.router.count() != 3 || res.Status != domain.RunAIFailed || res.ActualModel != primary {
			t.Errorf("calls %d res %+v", r.router.count(), res)
		}
	})

	t.Run("statuses", func(t *testing.T) {
		cases := []struct {
			status int
			retry  bool
		}{{429, true}, {500, true}, {502, true}, {503, true}, {599, true}, {400, false}, {401, false}, {402, false}, {404, false}, {408, false}, {422, false}}
		for _, tc := range cases {
			cfg := cfg
			cfg.OpenRouterEvalFallback = ""
			r := newRig(t, cfg)
			r.router.reply = orSeq(statusErr(tc.status))
			res := r.s.Evaluate(context.Background(), twoMessages(), false)
			want := 1
			if tc.retry {
				want = 3
			}
			if r.router.count() != want || res.Status != domain.RunAIFailed {
				t.Errorf("status %d: %d calls (want %d), %s", tc.status, r.router.count(), want, res.Status)
			}
		}
	})

	t.Run("Retry-After is honoured", func(t *testing.T) {
		r := newRig(t, cfg)
		r.router.reply = orSeq(&openrouter.StatusError{Status: 429, RetryAfter: 7 * time.Second}, chatCompletion(primary, dayAnswer))
		r.s.Evaluate(context.Background(), twoMessages(), false)
		if got := r.slept(); !reflect.DeepEqual(got, []time.Duration{7 * time.Second}) {
			t.Errorf("slept %v", got)
		}
	})

	t.Run("an empty answer is retried and then fails", func(t *testing.T) {
		cfg := cfg
		cfg.OpenRouterEvalFallback = ""
		r := newRig(t, cfg)
		r.router.reply = orSeq(chatCompletion(primary, "   "))
		res := r.s.Evaluate(context.Background(), twoMessages(), false)
		if r.router.count() != 3 || res.Status != domain.RunAIFailed || res.DayVerdictError != errEmptyResponse {
			t.Errorf("calls %d res %+v", r.router.count(), res)
		}
		if len(r.slept()) != 2 {
			t.Errorf("slept %v", r.slept())
		}
		if got := sub(t, r.audit.record(t, 0), "result", "status"); got != "empty_response" {
			t.Errorf("result = %v", got)
		}
	})

	t.Run("an empty answer once, then success", func(t *testing.T) {
		r := newRig(t, cfg)
		empty := &openrouter.Response{Status: 200, Body: []byte(`{"model":"` + primary + `","choices":[]}`)}
		r.router.reply = orSeq(empty, chatCompletion(primary, dayAnswer))
		if res := r.s.Evaluate(context.Background(), twoMessages(), false); res.Status != domain.RunParsed || r.router.count() != 2 {
			t.Errorf("res %+v calls %d", res, r.router.count())
		}
	})
}

// tests/test_ai.py::test_*_parse_failure_* (AI-19, AI-20): an answer that cannot
// be read is not asked for again; the next model is.
func TestEvaluateOpenRouterParseFailures(t *testing.T) {
	cfg := fullConfig()
	cfg.AnthropicKey = ""
	const primary, fallback = "openai/gpt-6-luna", "google/gemini-3.1-flash-lite"

	garbage := []struct {
		name string
		resp *openrouter.Response
	}{
		{"not JSON content", chatCompletion("actual/served-model", "not json at all")},
		{"no day block", chatCompletion("actual/served-model", `{"messages":[{"id":1,"score":7}]}`)},
		{"the body is not a chat completion", &openrouter.Response{Status: 200, Body: []byte(`<html>gateway</html>`)}},
		{"content is a list", &openrouter.Response{Status: 200, Body: []byte(`{"model":"actual/served-model","choices":[{"message":{"content":[{"type":"text"}]}}]}`)}},
	}
	for _, g := range garbage {
		t.Run(g.name+" falls back at once", func(t *testing.T) { // AI-19
			r := newRig(t, cfg)
			r.router.reply = func(n int, doc []byte) (*openrouter.Response, error) {
				if requestModel(t, doc) == primary {
					return g.resp, nil
				}
				return chatCompletion(fallback, dayAnswer), nil
			}
			res := r.s.Evaluate(context.Background(), twoMessages(), false)
			if got := r.router.models(t); !reflect.DeepEqual(got, []string{primary, fallback}) || res.Status != domain.RunParsed || res.ActualModel != fallback {
				t.Errorf("models %v res %+v", got, res)
			}
			if len(r.slept()) != 0 {
				t.Errorf("slept %v", r.slept())
			}
		})
	}

	t.Run("without a fallback the status is parse_failed and the model is the one that answered", func(t *testing.T) { // AI-20
		cfg := cfg
		cfg.OpenRouterEvalFallback = ""
		r := newRig(t, cfg)
		r.router.reply = orSeq(garbage[0].resp)
		res := r.s.Evaluate(context.Background(), twoMessages(), false)
		if res.Status != domain.RunParseFailed || res.ActualModel != "actual/served-model" || res.RequestedModel != primary || r.router.count() != 1 {
			t.Errorf("res %+v calls %d", res, r.router.count())
		}
		if len(res.Scores) != 2 || res.Scores[0].Value != 0.5 || res.DayVerdictError != errEvalFailed {
			t.Errorf("res = %+v", res)
		}
	})
}

// tests/test_ai.py::test_circuit_breaker_* (AI-27): five calls that failed as a
// whole open the circuit for two minutes, a success closes it, and while it is
// open nothing is sent.
func TestBreaker(t *testing.T) {
	cfg := fullConfig()
	cfg.AnthropicKey = ""
	cfg.OpenRouterEvalFallback = ""

	fail := func(r *rig) domain.EvalResult { return r.s.Evaluate(context.Background(), twoMessages(), false) }

	t.Run("five failures open it for two minutes", func(t *testing.T) {
		r := newRig(t, cfg)
		r.router.reply = orSeq(statusErr(400))
		for i := range 4 {
			if res := fail(r); res.Status != domain.RunAIFailed || r.s.breakerOpen() {
				t.Fatalf("call %d: %s, open %v", i, res.Status, r.s.breakerOpen())
			}
		}
		fail(r) // the fifth
		if !r.s.breakerOpen() {
			t.Fatal("the breaker is still closed")
		}
		before := r.router.count()
		res := fail(r)
		if r.router.count() != before {
			t.Error("a request went out while the breaker was open")
		}
		if res.Status != domain.RunAIFailed || res.DayVerdictError != errBreakerOpen || res.DayVerdict != nil ||
			!reflect.DeepEqual(res.Scores, []domain.Score{{ID: 1, Value: 0.5}, {ID: 2, Value: 0.5}}) {
			t.Errorf("res = %+v", res)
		}

		r.clock.advance(119 * time.Second)
		if !r.s.breakerOpen() {
			t.Error("closed after 119 s")
		}
		r.clock.advance(2 * time.Second)
		if r.s.breakerOpen() {
			t.Error("still open after 121 s")
		}
		r.router.reply = orSeq(chatCompletion("openai/gpt-6-luna", dayAnswer))
		if res := fail(r); res.Status != domain.RunParsed {
			t.Errorf("after the cooldown: %+v", res)
		}
	})

	t.Run("a success resets the count", func(t *testing.T) {
		r := newRig(t, cfg)
		r.router.reply = orSeq(statusErr(400))
		for range 4 {
			fail(r)
		}
		r.router.reply = orSeq(chatCompletion("openai/gpt-6-luna", dayAnswer))
		fail(r)
		r.router.reply = orSeq(statusErr(400))
		for range 4 {
			fail(r)
		}
		if r.s.breakerOpen() {
			t.Error("the count survived a success")
		}
	})

	t.Run("one call counts once however many models it tried", func(t *testing.T) {
		cfg := fullConfig() // three candidates
		r := newRig(t, cfg)
		r.claude.reply = claudeSeq(&claude.APIError{Status: 400})
		r.router.reply = orSeq(statusErr(400))
		for range 4 {
			fail(r)
		}
		if r.s.breakerOpen() || r.s.failures != 4 {
			t.Errorf("open %v, failures %d", r.s.breakerOpen(), r.s.failures)
		}
	})

	t.Run("a cancelled call is not a failure", func(t *testing.T) {
		r := newRig(t, cfg)
		ctx, cancel := context.WithCancel(context.Background())
		r.router.reply = func(int, []byte) (*openrouter.Response, error) {
			cancel()
			return nil, ctx.Err()
		}
		for range 6 {
			r.s.Evaluate(ctx, twoMessages(), false)
		}
		if r.s.breakerOpen() || r.s.failures != 0 {
			t.Errorf("open %v, failures %d", r.s.breakerOpen(), r.s.failures)
		}
	})
}

// A cancelled context stops the chain where it is.
func TestEvaluateStopsWhenCancelled(t *testing.T) {
	r := newRig(t, fullConfig())
	ctx, cancel := context.WithCancel(context.Background())
	r.claude.reply = func(int, claude.Request) (*claude.Response, error) {
		cancel()
		return nil, ctx.Err()
	}
	res := r.s.Evaluate(ctx, twoMessages(), false)
	if res.Status != domain.RunAIFailed || r.router.count() != 0 || len(r.claude.calls) != 1 {
		t.Errorf("res %+v, claude %d, openrouter %d", res, len(r.claude.calls), r.router.count())
	}
	if len(r.audit.recs) != 0 {
		t.Error("an audit record for a cancelled call")
	}
}

func TestEvaluateEmptyDay(t *testing.T) {
	r := newRig(t, Config{})
	res := r.s.Evaluate(context.Background(), nil, false)
	if res.Status != domain.RunEmpty || len(res.Scores) != 0 || res.Usage == nil {
		t.Errorf("res = %+v", res)
	}
}

// doc 10-ai: the id is 32 hex characters and the same on every record of a call,
// whichever provider wrote it.
func TestRequestIDIsSharedByEveryAttempt(t *testing.T) {
	r := newRig(t, fullConfig())
	r.claude.reply = claudeSeq(&claude.APIError{Status: 200, Type: "overloaded_error"})
	r.router.reply = orSeq(timeoutErr(), chatCompletion("openai/gpt-6-luna", dayAnswer))
	res := r.s.Evaluate(context.Background(), twoMessages(), false)
	if res.RequestID != testRequestID || len(r.audit.recs) != 4 {
		t.Fatalf("id %q, %d records", res.RequestID, len(r.audit.recs))
	}
	for i := range r.audit.recs {
		if r.audit.record(t, i)["request_id"] != testRequestID {
			t.Errorf("record %d has another id", i)
		}
	}
	attempts := []float64{1, 2, 1, 2} // numbered per candidate
	for i, want := range attempts {
		if got := r.audit.record(t, i)["attempt"]; got != want {
			t.Errorf("record %d attempt = %v, want %v", i, got, want)
		}
	}

	a, b := newRequestID(), newRequestID()
	if len(a) != 32 || a == b || strings.Trim(a, "0123456789abcdef") != "" {
		t.Errorf("ids %q %q", a, b)
	}
}

// doc section 6 "Prompts": the language question is part of the same request
// when the chat has no language yet.
func TestEvaluateDetectsLanguage(t *testing.T) {
	cfg := fullConfig()
	cfg.AnthropicKey = ""
	answer := `{"day":{"should_publish":true},"quote":null,"messages":[],"language":{"chat_language":"Ukrainian","interface_language":"uk"}}`

	r := newRig(t, cfg)
	r.router.reply = orSeq(chatCompletion("openai/gpt-6-luna", answer))
	res := r.s.Evaluate(context.Background(), twoMessages(), true)
	if res.LanguageChoice == nil || *res.LanguageChoice != (domain.LanguageChoice{ChatLanguage: "Ukrainian", InterfaceLanguage: "uk"}) {
		t.Errorf("language = %+v", res.LanguageChoice)
	}
	body := decode(t, r.router.docs[0])
	system := sub(t, body, "messages").([]any)[0].(map[string]any)["content"]
	if system != dayPrompt+injectionGuard+languageDetectionPrompt {
		t.Error("the language prompt is not appended after the guard")
	}
	if !strings.Contains(string(r.router.docs[0]), string(dayLanguageSchema.format)) || sub(t, body, "response_format", "json_schema", "name") != "quoto_day_verdict_language" {
		t.Error("the schema has no language block")
	}
	if rec := r.audit.record(t, 0); rec["detect_interface_language"] != true || sub(t, rec, "result", "language_choice", "interface_language") != "uk" {
		t.Errorf("record = %s", r.audit.recs[0])
	}

	r = newRig(t, cfg)
	r.router.reply = orSeq(chatCompletion("openai/gpt-6-luna", answer))
	res = r.s.Evaluate(context.Background(), twoMessages(), false)
	if res.LanguageChoice != nil || strings.Contains(string(r.router.docs[0]), "interface_language") {
		t.Errorf("language asked for or returned without a request: %+v", res.LanguageChoice)
	}
}

// ---- the audit log ----

// tests/test_ai.py::test_audit_log_* (AI-16): the OpenRouter record.
func TestAuditRecordsOfOpenRouter(t *testing.T) {
	cfg := fullConfig()
	cfg.AnthropicKey = ""
	cfg.OpenRouterEvalFallback = ""
	r := newRig(t, cfg)
	r.router.reply = orSeq(&openrouter.StatusError{Status: 429, Snippet: `{"error":"slow down"}`}, chatCompletion("openai/gpt-6-luna", dayAnswer))
	r.s.Evaluate(context.Background(), twoMessages(), false)

	if len(r.audit.recs) != 2 {
		t.Fatalf("%d records", len(r.audit.recs))
	}
	// The failed attempt: response before error, and no usage or result.
	wantKeys := []string{"request_id", "attempt", "provider", "include_day_verdict", "detect_interface_language", "message_count", "request", "response", "error"}
	if got := r.audit.keys(t, 0); !reflect.DeepEqual(got, wantKeys) {
		t.Errorf("keys = %v", got)
	}
	fail := r.audit.record(t, 0)
	if sub(t, fail, "response", "status_code") != float64(429) || sub(t, fail, "response", "text") != `{"error":"slow down"}` ||
		sub(t, fail, "error", "type") != "HTTPStatusError" || sub(t, fail, "error", "status_code") != float64(429) {
		t.Errorf("failed record = %s", r.audit.recs[0])
	}

	// The good one: response, usage, result.
	wantKeys = []string{"request_id", "attempt", "provider", "include_day_verdict", "detect_interface_language", "message_count", "request", "response", "usage", "result"}
	if got := r.audit.keys(t, 1); !reflect.DeepEqual(got, wantKeys) {
		t.Errorf("keys = %v", got)
	}
	rec := r.audit.record(t, 1)
	if rec["provider"] != "openrouter" || sub(t, rec, "request", "url") != openrouter.DefaultURL ||
		!reflect.DeepEqual(sub(t, rec, "request", "body", "provider"), map[string]any{"data_collection": "deny", "zdr": true}) {
		t.Errorf("request = %v", rec["request"])
	}
	resp := rec["response"].(map[string]any)
	if resp["status_code"] != float64(200) || resp["model"] != "openai/gpt-6-luna" || resp["content"] != dayAnswer ||
		resp["finish_reason"] != "stop" || !strings.Contains(resp["text"].(string), `"choices"`) {
		t.Errorf("response = %v", resp)
	}
	if sub(t, rec, "usage", "prompt_tokens") != float64(1200) || sub(t, rec, "usage", "cost_usd") != 0.00042 || sub(t, rec, "usage", "reasoning_tokens") != float64(300) {
		t.Errorf("usage = %v", rec["usage"])
	}
	// AI-16: the quote choice of the result, and the system prompt in the body.
	ctx := sub(t, rec, "result", "quote_choice", "context_ids")
	if !reflect.DeepEqual(ctx, []any{float64(1), float64(2)}) || sub(t, rec, "result", "score_count") != float64(2) {
		t.Errorf("result = %v", rec["result"])
	}
	system := sub(t, rec, "request", "body", "messages").([]any)[0].(map[string]any)["content"].(string)
	if !strings.Contains(system, "use ONLY `i`") {
		t.Error("the system prompt is not in the audit body")
	}
	if got := string(r.audit.recs[1]); !strings.Contains(got, `"result":{"status":"parsed","actual_model":"openai/gpt-6-luna","score_count":2,"quote_choice":{"primary_id":1,"context_ids":[1,2],"context_needed":true},"day_verdict":{"should_publish":true,"reason_code":"worthy","reason_text":"fun"},"language_choice":null,"day_verdict_error":null}`) {
		t.Errorf("result record = %s", got)
	}
}

// doc 7.1: the audit writer's errors are logged instead of swallowed, and an
// evaluation does not depend on the log.
func TestAuditFailureIsLoggedNotFatal(t *testing.T) {
	cfg := fullConfig()
	cfg.AnthropicKey = ""
	r := newRig(t, cfg)
	r.audit.err = errors.New("disk full")
	r.router.reply = orSeq(chatCompletion("openai/gpt-6-luna", dayAnswer))
	if res := r.s.Evaluate(context.Background(), twoMessages(), false); res.Status != domain.RunParsed {
		t.Errorf("res = %+v", res)
	}
	if !strings.Contains(r.logs.String(), "audit record not written") || !strings.Contains(r.logs.String(), "disk full") {
		t.Errorf("log = %s", r.logs)
	}
}

// doc 7.1 "Redaction: one redactor knows every secret": whatever goes to the log
// or the audit record passes through it.
func TestErrorTextsAreRedacted(t *testing.T) {
	r := newRig(t, fullConfig())
	r.claude.reply = claudeSeq(&claude.APIError{Status: 401, Type: "authentication_error", Message: "invalid x-api-key sk-ant-test-key"})
	r.router.reply = orSeq(&openrouter.StatusError{Status: 400, Snippet: "bad key or-test-key"}, &openrouter.StatusError{Status: 400, Snippet: "bad key or-test-key"})
	r.s.Evaluate(context.Background(), twoMessages(), false)
	for i, rec := range r.audit.recs {
		if strings.Contains(string(rec), "sk-ant-test-key") {
			t.Errorf("record %d has the Anthropic key", i)
		}
	}
	if got := sub(t, r.audit.record(t, 0), "error", "message"); !strings.Contains(got.(string), "***") {
		t.Errorf("message = %v", got)
	}
	if strings.Contains(r.logs.String(), "sk-ant-test-key") || strings.Contains(r.logs.String(), "or-test-key") {
		t.Errorf("a key is in the log:\n%s", r.logs)
	}
}
