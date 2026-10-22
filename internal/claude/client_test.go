// SPDX-License-Identifier: Apache-2.0

package claude

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// recorder keeps what a test server saw.
type recorder struct {
	mu      sync.Mutex
	bodies  [][]byte
	headers []http.Header
	paths   []string
}

func (r *recorder) record(req *http.Request) {
	body, _ := io.ReadAll(req.Body)
	r.mu.Lock()
	defer r.mu.Unlock()
	r.bodies = append(r.bodies, body)
	r.headers = append(r.headers, req.Header.Clone())
	r.paths = append(r.paths, req.Method+" "+req.URL.Path)
}

func (r *recorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.bodies)
}

// newTestClient returns a client for srv with sleeping replaced by a log of the
// delays it was asked for.
func newTestClient(srv *httptest.Server) (*Client, *[]time.Duration) {
	c := NewClient("sk-ant-test-key")
	c.BaseURL = srv.URL
	c.HTTP = srv.Client()
	var slept []time.Duration
	c.Sleep = func(_ context.Context, d time.Duration) error {
		slept = append(slept, d)
		return nil
	}
	c.Rand = func() float64 { return 0 }
	return c, &slept
}

func streamHandler(rec *recorder, stream string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rec.record(r)
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("request-id", "req_abc")
		io.WriteString(w, stream)
	}
}

func errorHandler(rec *recorder, status int, typ, msg string, hdr ...string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rec.record(r)
		for i := 0; i+1 < len(hdr); i += 2 {
			w.Header().Set(hdr[i], hdr[i+1])
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		io.WriteString(w, `{"type":"error","error":{"type":"`+typ+`","message":"`+msg+`"}}`)
	}
}

func evalRequest() Request {
	return Request{
		Model:     "claude-sonnet-5-5",
		MaxTokens: 32000,
		System:    "You are a quote curator <b> & more",
		Messages:  []Message{{Role: "user", Text: `[{"i": 1, "a": "Алиса"}]`}},
		Effort:    "medium",
		Schema:    json.RawMessage(`{"type":"object","properties":{"day":{"type":"object"}},"required":["day"]}`),
	}
}

// doc §6 Claude: "fallbacks: "default", beta server-side-fallback-2026-07-01,
// adaptive thinking, output_config.effort, structured output ...; httptest body
// assertions." tests/test_ai_claude.py::test_claude_scores_the_day_with_structured_output
func TestMessageRequestBodyAndHeaders(t *testing.T) {
	rec := &recorder{}
	srv := httptest.NewServer(streamHandler(rec, textStream("ok")))
	defer srv.Close()
	c, _ := newTestClient(srv)

	resp, err := c.Message(context.Background(), evalRequest())
	if err != nil {
		t.Fatal(err)
	}
	if resp.Text != "ok" || resp.RequestID != "req_abc" {
		t.Fatalf("response = %+v", resp)
	}
	if rec.count() != 1 || rec.paths[0] != "POST /v1/messages" {
		t.Fatalf("requests = %v", rec.paths)
	}

	h := rec.headers[0]
	for name, want := range map[string]string{
		"X-Api-Key":         "sk-ant-test-key",
		"Anthropic-Version": APIVersion,
		"Anthropic-Beta":    "server-side-fallback-2026-07-01",
		"Content-Type":      "application/json",
		"Accept":            "text/event-stream",
	} {
		if got := h.Get(name); got != want {
			t.Errorf("header %s = %q, want %q", name, got, want)
		}
	}

	wire := string(rec.bodies[0])
	want := `{"model":"claude-sonnet-5-5","max_tokens":32000,"system":"You are a quote curator <b> & more",` +
		`"messages":[{"role":"user","content":"[{\"i\": 1, \"a\": \"Алиса\"}]"}],` +
		`"thinking":{"type":"adaptive"},` +
		`"output_config":{"effort":"medium","format":{"type":"json_schema","schema":{"type":"object","properties":{"day":{"type":"object"}},"required":["day"]}}},` +
		`"fallbacks":"default","stream":true}`
	if wire != want {
		t.Fatalf("wire body =\n%s\nwant\n%s", wire, want)
	}

	// The audit body is the same without the transport's stream flag.
	audit, err := evalRequest().Body()
	if err != nil {
		t.Fatal(err)
	}
	if string(audit) != strings.Replace(want, `,"stream":true`, "", 1) {
		t.Fatalf("audit body =\n%s", audit)
	}
	for _, banned := range []string{"temperature", "cache_control", "batch", "files", "\\u003c"} {
		if strings.Contains(wire, banned) {
			t.Errorf("body mentions %q", banned)
		}
	}
	if strings.Contains(string(audit), "sk-ant") {
		t.Error("the audit body must not carry the key")
	}
}

// tests/test_ai_claude.py::test_image_is_described_by_claude
func TestMessageImageRequest(t *testing.T) {
	rec := &recorder{}
	srv := httptest.NewServer(streamHandler(rec, textStream("Кот сидит на подоконнике.")))
	defer srv.Close()
	c, _ := newTestClient(srv)

	_, err := c.Message(context.Background(), Request{
		Model:     "claude-sonnet-5-5",
		MaxTokens: 8000,
		Messages:  []Message{{Role: "user", Blocks: []Block{ImageBlock("image/jpeg", []byte("\xff\xd8\xff fake jpeg")), TextBlock("Опиши.")}}},
		Effort:    "medium",
	})
	if err != nil {
		t.Fatal(err)
	}
	var body struct {
		System       *string `json:"system"`
		Fallbacks    string  `json:"fallbacks"`
		Thinking     map[string]string
		OutputConfig map[string]any `json:"output_config"`
		Messages     []struct {
			Role    string `json:"role"`
			Content []struct {
				Type   string `json:"type"`
				Text   string `json:"text"`
				Source *struct {
					Type      string `json:"type"`
					MediaType string `json:"media_type"`
					Data      string `json:"data"`
				} `json:"source"`
			} `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(rec.bodies[0], &body); err != nil {
		t.Fatal(err)
	}
	content := body.Messages[0].Content
	if body.System != nil || body.Fallbacks != "default" || len(content) != 2 {
		t.Fatalf("body = %s", rec.bodies[0])
	}
	if content[0].Type != "image" || content[0].Source.Type != "base64" || content[0].Source.MediaType != "image/jpeg" ||
		content[0].Source.Data != "/9j/IGZha2UganBlZw==" {
		t.Fatalf("image block = %+v", content[0])
	}
	if content[1].Type != "text" || content[1].Text != "Опиши." {
		t.Fatalf("text block = %+v", content[1])
	}
	if len(body.OutputConfig) != 1 || body.OutputConfig["effort"] != "medium" {
		t.Fatalf("output_config = %v, want only effort for a plain-text answer", body.OutputConfig)
	}
	if got := rec.headers[0].Get("Anthropic-Beta"); got != FallbackBeta {
		t.Fatalf("beta = %q", got)
	}
}

// tests/test_ai_claude.py::test_empty_balance_falls_back_without_retrying_claude
func TestMessageDoesNotRetryBillingOrBadRequests(t *testing.T) {
	for _, tc := range []struct {
		status int
		typ    string
	}{{402, "billing_error"}, {400, "invalid_request_error"}, {401, "authentication_error"}, {404, "not_found_error"}} {
		rec := &recorder{}
		srv := httptest.NewServer(errorHandler(rec, tc.status, tc.typ, "nope"))
		c, slept := newTestClient(srv)
		_, err := c.Message(context.Background(), evalRequest())
		srv.Close()

		var api *APIError
		if !errors.As(err, &api) || api.Status != tc.status || api.Type != tc.typ || api.Message != "nope" {
			t.Fatalf("%d: err = %#v", tc.status, err)
		}
		if rec.count() != 1 || len(*slept) != 0 {
			t.Errorf("%d: %d requests, %d sleeps; want one request and no wait", tc.status, rec.count(), len(*slept))
		}
		if IsRetryable(err) {
			t.Errorf("%d must not be retryable", tc.status)
		}
	}
}

// The SDK retries a call that failed before its stream: 408, 409, 429, 5xx.
func TestMessageRetriesLikeTheSDK(t *testing.T) {
	for _, status := range []int{408, 409, 429, 500, 502, 503, 529} {
		var calls int
		rec := &recorder{}
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls++
			if calls == 1 {
				errorHandler(rec, status, "overloaded_error", "busy")(w, r)
				return
			}
			streamHandler(rec, textStream("fine"))(w, r)
		}))
		c, slept := newTestClient(srv)
		resp, err := c.Message(context.Background(), evalRequest())
		srv.Close()
		if err != nil || resp.Text != "fine" {
			t.Fatalf("%d: resp = %+v, err = %v", status, resp, err)
		}
		if rec.count() != 2 {
			t.Errorf("%d: %d requests, want 2", status, rec.count())
		}
		// No Retry-After: min(0.5 * 2^0, 8) seconds, jitter 0 here.
		if len(*slept) != 1 || (*slept)[0] != 500*time.Millisecond {
			t.Errorf("%d: waited %v, want [500ms]", status, *slept)
		}
	}
}

func TestMessageGivesUpAfterMaxRetries(t *testing.T) {
	rec := &recorder{}
	srv := httptest.NewServer(errorHandler(rec, 529, "overloaded_error", "busy"))
	defer srv.Close()
	c, slept := newTestClient(srv)
	_, err := c.Message(context.Background(), evalRequest())

	var api *APIError
	if !errors.As(err, &api) || api.Status != 529 {
		t.Fatalf("err = %#v", err)
	}
	if rec.count() != 1+MaxRetries {
		t.Fatalf("%d requests, want %d", rec.count(), 1+MaxRetries)
	}
	if len(*slept) != 2 || (*slept)[0] != 500*time.Millisecond || (*slept)[1] != time.Second {
		t.Fatalf("waits = %v, want 0.5 s then 1 s", *slept)
	}
}

func TestMessageHonoursRetryAfterAndShouldRetry(t *testing.T) {
	t.Run("retry-after seconds", func(t *testing.T) {
		var calls int
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls++
			if calls == 1 {
				errorHandler(&recorder{}, 429, "rate_limit_error", "slow down", "retry-after", "7")(w, r)
				return
			}
			streamHandler(&recorder{}, textStream("ok"))(w, r)
		}))
		defer srv.Close()
		c, slept := newTestClient(srv)
		if _, err := c.Message(context.Background(), evalRequest()); err != nil {
			t.Fatal(err)
		}
		if len(*slept) != 1 || (*slept)[0] != 7*time.Second {
			t.Fatalf("waits = %v", *slept)
		}
	})
	t.Run("retry-after-ms wins, an absurd value is ignored", func(t *testing.T) {
		got := func(h http.Header) time.Duration { return retryAfter(h) }
		if d := got(http.Header{"Retry-After-Ms": {"1500"}, "Retry-After": {"9"}}); d != 1500*time.Millisecond {
			t.Errorf("ms = %v", d)
		}
		c := NewClient("k")
		c.Rand = func() float64 { return 0 }
		if d := c.retryDelay(0, 10*time.Minute); d != 500*time.Millisecond {
			t.Errorf("an hour-long Retry-After must fall back to backoff, got %v", d)
		}
		c.Rand = func() float64 { return 0.999999 }
		if d := c.retryDelay(5, 0); d < 6*time.Second || d > 8*time.Second {
			t.Errorf("capped backoff = %v", d)
		}
	})
	t.Run("x-should-retry false stops a 500", func(t *testing.T) {
		rec := &recorder{}
		srv := httptest.NewServer(errorHandler(rec, 500, "api_error", "boom", "x-should-retry", "false"))
		defer srv.Close()
		c, _ := newTestClient(srv)
		if _, err := c.Message(context.Background(), evalRequest()); err == nil || rec.count() != 1 {
			t.Fatalf("err = %v after %d requests", err, rec.count())
		}
	})
	t.Run("x-should-retry true retries a 400", func(t *testing.T) {
		rec := &recorder{}
		srv := httptest.NewServer(errorHandler(rec, 400, "invalid_request_error", "hm", "x-should-retry", "true"))
		defer srv.Close()
		c, _ := newTestClient(srv)
		_, _ = c.Message(context.Background(), evalRequest())
		if rec.count() != 1+MaxRetries {
			t.Fatalf("%d requests", rec.count())
		}
	})
}

// A proxy's HTML error page must not end up in the error text.
func TestMessageErrorBodyIsNotEchoed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(502)
		io.WriteString(w, "<html>secret-token-in-page "+strings.Repeat("x", 100000)+"</html>")
	}))
	defer srv.Close()
	c, _ := newTestClient(srv)
	_, err := c.Message(context.Background(), evalRequest())
	var api *APIError
	if !errors.As(err, &api) || api.Status != 502 || api.Message != "Bad Gateway" || api.Type != "" {
		t.Fatalf("err = %#v", err)
	}
	if strings.Contains(err.Error(), "secret-token") {
		t.Fatal("body leaked into the error")
	}
}

func TestMessageLongErrorMessageIsClipped(t *testing.T) {
	srv := httptest.NewServer(errorHandler(&recorder{}, 400, "invalid_request_error", strings.Repeat("я", 5000)))
	defer srv.Close()
	c, _ := newTestClient(srv)
	_, err := c.Message(context.Background(), evalRequest())
	var api *APIError
	if !errors.As(err, &api) || len([]rune(api.Message)) != maxErrorMessage {
		t.Fatalf("err = %#v", err)
	}
}

// An error event on a 200 response is returned as it is; the SDK does not retry
// it and neither does the client (the caller's attempt loop does).
// tests/test_ai_claude.py::test_overload_mid_stream_is_retried
func TestMessageMidStreamErrorIsNotRetriedHere(t *testing.T) {
	rec := &recorder{}
	stream := ev("message_start", startEvent) + ev("error", `{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`)
	srv := httptest.NewServer(streamHandler(rec, stream))
	defer srv.Close()
	c, slept := newTestClient(srv)
	_, err := c.Message(context.Background(), evalRequest())

	var api *APIError
	if !errors.As(err, &api) || api.Status != 200 || api.Type != "overloaded_error" || api.RequestID != "req_abc" {
		t.Fatalf("err = %#v", err)
	}
	if !IsRetryable(err) || rec.count() != 1 || len(*slept) != 0 {
		t.Fatalf("retryable=%v requests=%d sleeps=%d", IsRetryable(err), rec.count(), len(*slept))
	}
}

func TestMessageTruncatedStreamIsAConnError(t *testing.T) {
	rec := &recorder{}
	full := textStream("a long answer")
	srv := httptest.NewServer(streamHandler(rec, full[:len(full)/2]))
	defer srv.Close()
	c, _ := newTestClient(srv)
	_, err := c.Message(context.Background(), evalRequest())
	var conn *ConnError
	if !errors.As(err, &conn) || !IsRetryable(err) {
		t.Fatalf("err = %#v", err)
	}
	if rec.count() != 1 {
		t.Fatalf("a broken stream is not retried by the client; %d requests", rec.count())
	}
}

// The server cuts the connection in the middle of the answer.
func TestMessageConnectionDroppedMidStream(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, ev("message_start", startEvent))
		w.(http.Flusher).Flush()
		conn, _, _ := w.(http.Hijacker).Hijack()
		conn.Close()
	}))
	defer srv.Close()
	c, _ := newTestClient(srv)
	_, err := c.Message(context.Background(), evalRequest())
	var conn *ConnError
	if !errors.As(err, &conn) || !IsRetryable(err) {
		t.Fatalf("err = %#v", err)
	}
}

// No connection at all is retried like the SDK does, then reported.
func TestMessageConnectionRefused(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	c, slept := newTestClient(srv)
	srv.Close()
	_, err := c.Message(context.Background(), evalRequest())
	var conn *ConnError
	if !errors.As(err, &conn) || !IsRetryable(err) {
		t.Fatalf("err = %#v", err)
	}
	if len(*slept) != MaxRetries {
		t.Fatalf("waits = %v, want %d", *slept, MaxRetries)
	}
}

// The API stays silent after the headers: the idle timeout ends the call.
func TestMessageIdleTimeoutMidStream(t *testing.T) {
	old := IdleTimeout
	IdleTimeout = 40 * time.Millisecond
	defer func() { IdleTimeout = old }()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, ev("message_start", startEvent))
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer srv.Close()
	c, _ := newTestClient(srv)
	_, err := c.Message(context.Background(), evalRequest())
	if !errors.Is(err, ErrIdle) || !IsRetryable(err) {
		t.Fatalf("err = %#v", err)
	}
}

// ... and the same before any header arrives, which the SDK also retries.
func TestMessageIdleTimeoutBeforeHeaders(t *testing.T) {
	old := IdleTimeout
	IdleTimeout = 40 * time.Millisecond
	defer func() { IdleTimeout = old }()

	var calls int
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls++
		n := calls
		mu.Unlock()
		io.ReadAll(r.Body) // the server notices a closed connection only once the body is read
		if n == 1 {
			<-r.Context().Done()
			return
		}
		streamHandler(&recorder{}, textStream("late but fine"))(w, r)
	}))
	defer srv.Close()
	c, slept := newTestClient(srv)
	resp, err := c.Message(context.Background(), evalRequest())
	if err != nil || resp.Text != "late but fine" || len(*slept) != 1 {
		t.Fatalf("resp = %+v, err = %v, waits = %v", resp, err, *slept)
	}
}

// A slow but steady stream never trips the idle timeout.
func TestMessageIdleTimeoutIsPerQuietPeriod(t *testing.T) {
	old := IdleTimeout
	IdleTimeout = 150 * time.Millisecond
	defer func() { IdleTimeout = old }()

	ticks := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		events := strings.SplitAfter(textStream("a", "b", "c", "d"), "\n\n")
		for _, e := range events {
			io.WriteString(w, e)
			w.(http.Flusher).Flush()
			select {
			case <-ticks:
			case <-time.After(30 * time.Millisecond):
			}
		}
	}))
	defer srv.Close()
	c, _ := newTestClient(srv)
	resp, err := c.Message(context.Background(), evalRequest())
	if err != nil || resp.Text != "abcd" {
		t.Fatalf("resp = %+v, err = %v", resp, err)
	}
}

// Shutdown: the caller's cancellation comes back as it is, never as a
// retryable connection error.
func TestMessageContextCancelled(t *testing.T) {
	started := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, ev("message_start", startEvent))
		w.(http.Flusher).Flush()
		close(started)
		<-r.Context().Done()
	}))
	defer srv.Close()
	c, _ := newTestClient(srv)

	ctx, cancel := context.WithCancel(context.Background())
	go func() { <-started; cancel() }()
	_, err := c.Message(ctx, evalRequest())
	if !errors.Is(err, context.Canceled) || IsRetryable(err) {
		t.Fatalf("err = %#v", err)
	}
}

func TestMessageSleepInterruptedByContext(t *testing.T) {
	srv := httptest.NewServer(errorHandler(&recorder{}, 503, "api_error", "down"))
	defer srv.Close()
	c, _ := newTestClient(srv)
	boom := errors.New("context ended while waiting")
	c.Sleep = func(context.Context, time.Duration) error { return boom }
	if _, err := c.Message(context.Background(), evalRequest()); !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
}

func TestIsRetryable(t *testing.T) {
	cases := []struct {
		err  error
		want bool
	}{
		{&ConnError{io.ErrUnexpectedEOF}, true},
		{&APIError{Status: 429}, true},
		{&APIError{Status: 500}, true},
		{&APIError{Status: 529}, true},
		{&APIError{Status: 200, Type: "overloaded_error"}, true},
		{&APIError{Status: 200, Type: "api_error"}, true},
		{&APIError{Status: 200, Type: "rate_limit_error"}, true},
		{&APIError{Status: 200, Type: "timeout_error"}, true},
		{&APIError{Status: 402, Type: "billing_error"}, false},
		{&APIError{Status: 400, Type: "invalid_request_error"}, false},
		{&APIError{Status: 401}, false},
		{&APIError{Status: 200, Type: "invalid_request_error"}, false},
		{&StreamError{io.EOF}, false},
		{context.Canceled, false},
		{errors.New("other"), false},
		{nil, false},
	}
	for _, tc := range cases {
		if got := IsRetryable(tc.err); got != tc.want {
			t.Errorf("IsRetryable(%v) = %v, want %v", tc.err, got, tc.want)
		}
	}
}

func TestAPIErrorText(t *testing.T) {
	e := &APIError{Status: 402, Type: "billing_error", Message: "Your credit balance is too low"}
	if got := e.Error(); got != "anthropic: HTTP 402 billing_error: Your credit balance is too low" {
		t.Fatalf("Error() = %q", got)
	}
	if got := (&APIError{Status: 500}).Error(); got != "anthropic: HTTP 500" {
		t.Fatalf("Error() = %q", got)
	}
}
