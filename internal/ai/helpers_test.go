// SPDX-License-Identifier: Apache-2.0

package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/FreshLabDev/quoto/internal/claude"
	"github.com/FreshLabDev/quoto/internal/domain"
	"github.com/FreshLabDev/quoto/internal/openrouter"
)

// fakeClaude answers the n-th call with reply(n, request).
type fakeClaude struct {
	mu    sync.Mutex
	calls []claude.Request
	reply func(n int, req claude.Request) (*claude.Response, error)
}

func (f *fakeClaude) Message(ctx context.Context, req claude.Request) (*claude.Response, error) {
	f.mu.Lock()
	n := len(f.calls)
	f.calls = append(f.calls, req)
	f.mu.Unlock()
	return f.reply(n, req)
}

// fakeRouter answers the n-th call with reply(n, request document).
type fakeRouter struct {
	mu       sync.Mutex
	docs     [][]byte // each request body as it would go on the wire
	timeouts []time.Duration
	reply    func(n int, doc []byte) (*openrouter.Response, error)
}

func (f *fakeRouter) Do(ctx context.Context, body openrouter.Body, timeout time.Duration) (*openrouter.Response, error) {
	rc, _, err := body.Open()
	if err != nil {
		return nil, &openrouter.BodyError{Err: err}
	}
	doc, err := io.ReadAll(rc)
	rc.Close()
	if err != nil {
		return nil, &openrouter.BodyError{Err: err}
	}
	f.mu.Lock()
	n := len(f.docs)
	f.docs = append(f.docs, doc)
	f.timeouts = append(f.timeouts, timeout)
	f.mu.Unlock()
	return f.reply(n, doc)
}

func (f *fakeRouter) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.docs)
}

// models lists the model of every request, in order.
func (f *fakeRouter) models(t *testing.T) []string {
	t.Helper()
	var out []string
	for _, d := range f.docs {
		out = append(out, requestModel(t, d))
	}
	return out
}

func requestModel(t *testing.T, doc []byte) string {
	t.Helper()
	var m struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(doc, &m); err != nil {
		t.Fatalf("request is not JSON: %v\n%.200s", err, doc)
	}
	return m.Model
}

// memAudit keeps the audit records as the JSON they would have been written as.
type memAudit struct {
	mu   sync.Mutex
	recs [][]byte
	err  error
}

func (a *memAudit) Write(v any) error {
	if a.err != nil {
		return a.err
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return err
	}
	a.mu.Lock()
	a.recs = append(a.recs, bytes.TrimSpace(buf.Bytes()))
	a.mu.Unlock()
	return nil
}

// record decodes the i-th record into a generic map.
func (a *memAudit) record(t *testing.T, i int) map[string]any {
	t.Helper()
	if i >= len(a.recs) {
		t.Fatalf("only %d audit records, wanted #%d", len(a.recs), i)
	}
	var m map[string]any
	if err := json.Unmarshal(a.recs[i], &m); err != nil {
		t.Fatal(err)
	}
	return m
}

// keys lists the top-level keys of the i-th record in file order.
func (a *memAudit) keys(t *testing.T, i int) []string {
	t.Helper()
	return jsonKeys(t, a.recs[i])
}

func jsonKeys(t *testing.T, doc []byte) []string {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(doc))
	if _, err := dec.Token(); err != nil {
		t.Fatal(err)
	}
	var keys []string
	for dec.More() {
		k, err := dec.Token()
		if err != nil {
			t.Fatal(err)
		}
		keys = append(keys, k.(string))
		var skip json.RawMessage
		if err := dec.Decode(&skip); err != nil {
			t.Fatal(err)
		}
	}
	return keys
}

// clock is a settable time source.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func newClock() *clock { return &clock{t: time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)} }

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

const testRequestID = "0123456789abcdef0123456789abcdef"

// rig is a service wired to fakes. Nothing sleeps: pauses are recorded.
type rig struct {
	s      *Service
	claude *fakeClaude
	router *fakeRouter
	audit  *memAudit
	clock  *clock
	logs   *bytes.Buffer

	mu     sync.Mutex
	pauses []time.Duration
}

func (r *rig) slept() []time.Duration {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]time.Duration(nil), r.pauses...)
}

// fullConfig has both providers, a Claude model for each job and an OpenRouter
// primary with a different fallback.
func fullConfig() Config {
	return Config{
		AnthropicKey: "sk-ant-test-key", AnthropicEvalModel: "claude-opus-5-5", AnthropicEvalEffort: "medium",
		AnthropicMediaModel: "claude-sonnet-5-5", AnthropicMediaEffort: "medium",
		OpenRouterKey: "or-test-key", OpenRouterEvalModel: "openai/gpt-6-luna", OpenRouterEvalFallback: "google/gemini-3.1-flash-lite",
		OpenRouterMediaModel: "google/gemini-3.5-flash-lite", OpenRouterMediaFallback: "google/gemini-3.1-flash-lite",
		OpenRouterEvalEffort: "low", OpenRouterMediaEffort: "medium",
		BotUsername: "quototbot",
	}
}

// newRig builds a service for cfg. The fakes fail the test if they are called
// without a reply being set.
func newRig(t *testing.T, cfg Config) *rig {
	t.Helper()
	r := &rig{
		claude: &fakeClaude{reply: func(n int, _ claude.Request) (*claude.Response, error) {
			t.Errorf("unexpected Claude call #%d", n)
			return nil, context.Canceled
		}},
		router: &fakeRouter{reply: func(n int, doc []byte) (*openrouter.Response, error) {
			t.Errorf("unexpected OpenRouter call #%d: %.100s", n, doc)
			return nil, context.Canceled
		}},
		audit: &memAudit{},
		clock: newClock(),
		logs:  &bytes.Buffer{},
	}
	log := slog.New(slog.NewTextHandler(r.logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	s := newService(cfg, func(s string) string {
		return strings.ReplaceAll(strings.ReplaceAll(s, "sk-ant-test-key", "***"), "or-test-key", "***")
	}, log)
	if cfg.AnthropicKey != "" {
		s.claude = r.claude
	}
	if cfg.OpenRouterKey != "" {
		s.router = r.router
	}
	s.audit = r.audit
	s.now = r.clock.now
	s.rand = func() float64 { return 0.5 } // jitter of exactly one second
	s.newID = func() string { return testRequestID }
	s.sleep = func(ctx context.Context, d time.Duration) error {
		r.mu.Lock()
		r.pauses = append(r.pauses, d)
		r.mu.Unlock()
		return ctx.Err()
	}
	r.s = s
	return r
}

// ---- canned answers ----

// dayAnswer is a model's text for the messages 1 and 2.
const dayAnswer = `{"day":{"should_publish":true,"reason_code":"worthy","reason_text":"fun"},` +
	`"quote":{"primary_id":1,"context_ids":[1,2],"context_needed":true},` +
	`"messages":[{"id":1,"score":7},{"id":2,"score":8}]}`

func twoMessages() []domain.EvalMessage {
	return []domain.EvalMessage{
		{ID: 1, Author: "Alice", Text: "first"},
		{ID: 2, Author: "Bob", Text: "second", ReplyToID: 1},
	}
}

// claudeAnswer is a finished Claude message with the usage of the Python tests:
// 1000 tokens in, 500 out.
func claudeAnswer(model, text string) *claude.Response {
	return &claude.Response{
		ID: "msg_1", Model: model, StopReason: "end_turn", Text: text, RequestID: "req_1",
		Usage: claude.Usage{Counts: claude.Counts{Input: 1000, Output: 500}},
	}
}

// chatCompletion is an OpenRouter chat completion as JSON.
func chatCompletion(model, content string) *openrouter.Response {
	body, _ := json.Marshal(map[string]any{
		"model": model,
		"choices": []any{map[string]any{
			"message":       map[string]any{"role": "assistant", "content": content},
			"finish_reason": "stop",
		}},
		"usage": map[string]any{
			"prompt_tokens": 1200, "completion_tokens": 450, "total_tokens": 1650,
			"completion_tokens_details": map[string]any{"reasoning_tokens": 300},
			"cost":                      0.00042,
		},
	})
	return &openrouter.Response{Status: 200, Body: body}
}

func statusErr(code int) error { return &openrouter.StatusError{Status: code} }

func timeoutErr() error { return &openrouter.RequestError{Err: context.DeadlineExceeded} }
