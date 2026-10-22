// SPDX-License-Identifier: Apache-2.0

package ai

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/FreshLabDev/quoto/internal/audit"
	"github.com/FreshLabDev/quoto/internal/claude"
	"github.com/FreshLabDev/quoto/internal/domain"
	"github.com/FreshLabDev/quoto/internal/openrouter"
)

// These tests build the service with New and the real clients and point them at
// httptest servers, so the pieces are checked together: the wire format of both
// providers, the headers, the audit file.

type seen struct {
	header        http.Header
	body          []byte
	contentLength int64
	chunked       bool
}

type recorderSrv struct {
	*httptest.Server
	mu   sync.Mutex
	reqs []seen
}

func newRecorderSrv(t *testing.T, h func(n int, w http.ResponseWriter, r *http.Request, body []byte)) *recorderSrv {
	t.Helper()
	rs := &recorderSrv{}
	rs.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		rs.mu.Lock()
		n := len(rs.reqs)
		rs.reqs = append(rs.reqs, seen{r.Header.Clone(), body, r.ContentLength, len(r.TransferEncoding) > 0})
		rs.mu.Unlock()
		h(n, w, r, body)
	}))
	t.Cleanup(rs.Close)
	return rs
}

// claudeSSE is a finished Claude stream with one text block.
func claudeSSE(model, text string, in, out int) string {
	t, _ := json.Marshal(text)
	ev := func(name, data string) string { return "event: " + name + "\ndata: " + data + "\n\n" }
	return ev("message_start", fmt.Sprintf(`{"type":"message_start","message":{"id":"msg_1","model":%q,"usage":{"input_tokens":%d,"output_tokens":1}}}`, model, in)) +
		ev("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`) +
		ev("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":`+string(t)+`}}`) +
		ev("content_block_stop", `{"type":"content_block_stop","index":0}`) +
		ev("message_delta", fmt.Sprintf(`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":%d}}`, out)) +
		ev("message_stop", `{"type":"message_stop"}`)
}

func sseHandler(stream string) func(int, http.ResponseWriter, *http.Request, []byte) {
	return func(_ int, w http.ResponseWriter, _ *http.Request, _ []byte) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("request-id", "req_e2e")
		io.WriteString(w, stream)
	}
}

func jsonHandler(body string) func(int, http.ResponseWriter, *http.Request, []byte) {
	return func(_ int, w http.ResponseWriter, _ *http.Request, _ []byte) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, body)
	}
}

func completionJSON(model, content string) string {
	c, _ := json.Marshal(content)
	return `{"model":"` + model + `","choices":[{"message":{"role":"assistant","content":` + string(c) + `}}],` +
		`"usage":{"prompt_tokens":1200,"completion_tokens":450,"total_tokens":1650,"completion_tokens_details":{"reasoning_tokens":300},"cost":0.00042}}`
}

// newE2E builds the service the way cmd/bot does and redirects both clients.
func newE2E(t *testing.T, cfg Config, claudeSrv, orSrv *recorderSrv) *Service {
	t.Helper()
	s := New(cfg, func(s string) string {
		return strings.ReplaceAll(strings.ReplaceAll(s, "sk-ant-test-key", "***"), "or-test-key", "***")
	}, nil)
	if c, ok := s.claude.(*claude.Client); ok {
		c.BaseURL = claudeSrv.URL
		c.HTTP = claudeSrv.Client()
		c.Sleep = func(ctx context.Context, _ time.Duration) error { return ctx.Err() }
		c.Rand = func() float64 { return 0 }
	}
	if c, ok := s.router.(*openrouter.Client); ok {
		c.URL = orSrv.URL
		s.orURL = orSrv.URL
	}
	s.sleep = func(ctx context.Context, _ time.Duration) error { return ctx.Err() }
	return s
}

func readAudit(t *testing.T, dir string) []map[string]any {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, audit.FileName))
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(dir, audit.FileName))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("audit file mode = %v, err %v", info.Mode(), err)
	}
	var out []map[string]any
	for _, line := range bytes.Split(bytes.TrimSpace(raw), []byte("\n")) {
		out = append(out, decode(t, line))
	}
	return out
}

// doc section 6 "Claude" and "AI audit": the whole day evaluation through the real
// Claude client and the real audit file.
func TestEndToEndEvaluateWithClaude(t *testing.T) {
	cfg := fullConfig()
	cfg.AuditDir = t.TempDir()
	claudeSrv := newRecorderSrv(t, sseHandler(claudeSSE("claude-opus-5-5", dayAnswer, 1000, 500)))
	orSrv := newRecorderSrv(t, func(n int, _ http.ResponseWriter, _ *http.Request, _ []byte) {
		t.Errorf("OpenRouter was called (#%d)", n)
	})
	s := newE2E(t, cfg, claudeSrv, orSrv)

	res := s.Evaluate(context.Background(), twoMessages(), false)
	if res.Status != domain.RunParsed || res.ActualModel != "claude-opus-5-5" || len(res.Scores) != 2 || res.Scores[0].Value != 0.7 ||
		res.DayVerdict == nil || !res.DayVerdict.ShouldPublish {
		t.Fatalf("result = %+v", res)
	}
	if res.Usage == nil || *res.Usage.PromptTokens != 1000 || *res.Usage.CompletionTokens != 500 {
		t.Errorf("usage = %+v", res.Usage)
	}

	req := claudeSrv.reqs[0]
	for name, want := range map[string]string{
		"X-Api-Key":         "sk-ant-test-key",
		"Anthropic-Version": claude.APIVersion,
		"Anthropic-Beta":    claude.FallbackBeta,
		"Content-Type":      "application/json",
	} {
		if got := req.header.Get(name); got != want {
			t.Errorf("header %s = %q, want %q", name, got, want)
		}
	}
	body := decode(t, req.body)
	if body["model"] != "claude-opus-5-5" || body["fallbacks"] != "default" || body["stream"] != true ||
		sub(t, body, "thinking", "type") != "adaptive" || sub(t, body, "output_config", "effort") != "medium" ||
		sub(t, body, "output_config", "format", "type") != "json_schema" {
		t.Errorf("body = %.400s", req.body)
	}

	recs := readAudit(t, cfg.AuditDir)
	if len(recs) != 1 || recs[0]["provider"] != "anthropic" || recs[0]["request_id"] != res.RequestID {
		t.Fatalf("audit = %v", recs)
	}
	if strings.Contains(string(req.body), "or-test-key") {
		t.Error("the OpenRouter key went to Anthropic")
	}
}

// Claude says no (400 is final for it), OpenRouter answers: both providers are
// in the audit file under one request id, and OpenRouter gets its headers and the
// provider block.
func TestEndToEndEvaluateFallsToOpenRouter(t *testing.T) {
	cfg := fullConfig()
	cfg.AuditDir = t.TempDir()
	claudeSrv := newRecorderSrv(t, func(_ int, w http.ResponseWriter, _ *http.Request, _ []byte) {
		w.WriteHeader(400)
		io.WriteString(w, `{"type":"error","error":{"type":"invalid_request_error","message":"bad sk-ant-test-key"}}`)
	})
	orSrv := newRecorderSrv(t, jsonHandler(completionJSON("openai/gpt-6-luna", dayAnswer)))
	s := newE2E(t, cfg, claudeSrv, orSrv)

	res := s.Evaluate(context.Background(), twoMessages(), true)
	if res.Status != domain.RunParsed || res.ActualModel != "openai/gpt-6-luna" || res.Usage == nil || *res.Usage.CostUSD != 0.00042 || *res.Usage.ReasoningTokens != 300 {
		t.Fatalf("result = %+v (usage %+v)", res, res.Usage)
	}

	h := orSrv.reqs[0].header
	for name, want := range map[string]string{
		"Authorization":      "Bearer or-test-key",
		"Http-Referer":       "https://t.me/quototbot",
		"X-Openrouter-Title": "Quoto",
		"Content-Type":       "application/json",
	} {
		if got := h.Get(name); got != want {
			t.Errorf("header %s = %q, want %q", name, got, want)
		}
	}
	if orSrv.reqs[0].chunked || orSrv.reqs[0].contentLength != int64(len(orSrv.reqs[0].body)) {
		t.Errorf("content length %d, body %d, chunked %v", orSrv.reqs[0].contentLength, len(orSrv.reqs[0].body), orSrv.reqs[0].chunked)
	}
	body := decode(t, orSrv.reqs[0].body)
	if p := sub(t, body, "provider"); fmt.Sprint(p) != "map[data_collection:deny zdr:true]" {
		t.Errorf("provider = %v", p)
	}
	if want := []string{"model", "messages", "response_format", "usage", "provider", "max_tokens", "reasoning"}; strings.Join(jsonKeys(t, orSrv.reqs[0].body), ",") != strings.Join(want, ",") {
		t.Errorf("keys = %v", jsonKeys(t, orSrv.reqs[0].body))
	}
	if sub(t, body, "response_format", "type") != "json_schema" {
		t.Errorf("response_format = %v", body["response_format"])
	}

	recs := readAudit(t, cfg.AuditDir)
	if len(recs) != 2 || recs[0]["provider"] != "anthropic" || recs[1]["provider"] != "openrouter" || recs[0]["request_id"] != recs[1]["request_id"] {
		t.Fatalf("audit = %v", recs)
	}
	if strings.Contains(res.DayVerdictError, "sk-ant-test-key") {
		t.Errorf("a key is in the result: %q", res.DayVerdictError)
	}
}

// A photo through Claude and a video through OpenRouter, end to end, from a file
// on disk to the description.
func TestEndToEndDescribeMedia(t *testing.T) {
	cfg := fullConfig()
	claudeSrv := newRecorderSrv(t, sseHandler(claudeSSE("claude-sonnet-5-5", "A cat.", 800, 20)))
	orSrv := newRecorderSrv(t, jsonHandler(completionJSON("google/gemini-3.5-flash-lite", "A talk about cats.")))
	s := newE2E(t, cfg, claudeSrv, orSrv)

	photo, photoBytes := mediaFile(t, 4000)
	got, err := s.DescribeMedia(context.Background(), describeReq(photo, "photo", "image/jpeg"))
	if err != nil || got.Text != "A cat." || got.PromptTokens != 800 || got.CompletionTokens != 20 {
		t.Fatalf("photo: %+v %v", got, err)
	}
	body := decode(t, claudeSrv.reqs[0].body)
	blocks := sub(t, body, "messages").([]any)[0].(map[string]any)["content"].([]any)
	if sub(t, blocks[0].(map[string]any), "source", "data") != base64.StdEncoding.EncodeToString(photoBytes) {
		t.Error("the image sent to Claude is not the file")
	}

	video, videoBytes := mediaFile(t, 10_000)
	got, err = s.DescribeMedia(context.Background(), describeReq(video, "video_note", "video/mp4"))
	if err != nil || got.Text != "A talk about cats." || got.TotalTokens != 1650 {
		t.Fatalf("video: %+v %v", got, err)
	}
	if len(claudeSrv.reqs) != 1 {
		t.Errorf("Claude got %d requests, want 1 (video does not go there)", len(claudeSrv.reqs))
	}
	r := orSrv.reqs[0]
	if r.chunked || r.contentLength != int64(len(r.body)) {
		t.Errorf("content length %d, body %d, chunked %v", r.contentLength, len(r.body), r.chunked)
	}
	parts := sub(t, decode(t, r.body), "messages").([]any)[0].(map[string]any)["content"].([]any)
	if sub(t, parts[1].(map[string]any), "video_url", "url") != "data:video/mp4;base64,"+base64.StdEncoding.EncodeToString(videoBytes) {
		t.Error("the video sent to OpenRouter is not the file")
	}
	if r.header.Get("HTTP-Referer") != "https://t.me/quototbot" || r.header.Get("Authorization") != "Bearer or-test-key" {
		t.Errorf("headers = %v", r.header)
	}
}

// OpenRouter answers 402 on both models over the real client: the cooldown opens
// and a call during it is not sent.
func TestEndToEndMediaCooldown(t *testing.T) {
	cfg := fullConfig()
	cfg.AnthropicKey = ""
	orSrv := newRecorderSrv(t, func(_ int, w http.ResponseWriter, _ *http.Request, _ []byte) {
		w.WriteHeader(402)
		io.WriteString(w, `{"error":{"message":"insufficient credits"}}`)
	})
	s := newE2E(t, cfg, nil, orSrv)
	clk := newClock()
	s.now = clk.now

	path, _ := mediaFile(t, 100)
	req := describeReq(path, "voice", "audio/mpeg")
	var re *domain.MediaRetryableError
	_, err := s.DescribeMedia(context.Background(), req)
	if !asRetryable(err, &re) || re.RetryAfter != 900*time.Second || !re.CountsAsAttempt {
		t.Fatalf("err = %T %v", err, err)
	}
	if len(orSrv.reqs) != 2 {
		t.Fatalf("requests = %d, want one per model", len(orSrv.reqs))
	}
	_, err = s.DescribeMedia(context.Background(), req)
	if !asRetryable(err, &re) || re.CountsAsAttempt || len(orSrv.reqs) != 2 {
		t.Errorf("during the pause: %T %v, requests %d", err, err, len(orSrv.reqs))
	}
}

func asRetryable(err error, target **domain.MediaRetryableError) bool {
	for err != nil {
		if r, ok := err.(*domain.MediaRetryableError); ok {
			*target = r
			return true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}

// Brief 10-ai, "the file is never held in memory": a 20 MiB file goes through
// the real client and its peak heap stays far below the 27 MiB the base64 text
// would take. The server reads the body in chunks and samples the heap as it
// does, so the numbers do not depend on a timer.
func TestDescribeMediaDoesNotHoldTheFile(t *testing.T) {
	if testing.Short() {
		t.Skip("writes and uploads 20 MiB")
	}
	const size = 20 << 20
	path := filepath.Join(t.TempDir(), "big.mp4")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	chunk := make([]byte, 1<<20)
	for i := range chunk {
		chunk[i] = byte(i*131 + i>>8)
	}
	for i := 0; i < size>>20; i++ {
		chunk[0] = byte(i) // make the chunks differ
		if _, err := f.Write(chunk); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	var (
		head, tail []byte
		total      int64
		sum        = sha256.New()
		peak       uint64
		baseline   uint64
	)
	sample := func() {
		var m runtime.MemStats
		runtime.ReadMemStats(&m)
		peak = max(peak, m.HeapAlloc)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, 256<<10)
		next := int64(1 << 20)
		for {
			n, err := r.Body.Read(buf)
			if n > 0 {
				if len(head) < 8192 {
					head = append(head, buf[:min(n, 8192-len(head))]...)
				}
				tail = append(tail, buf[:n]...)
				if len(tail) > 512 {
					tail = append(tail[:0], tail[len(tail)-512:]...)
				}
				sum.Write(buf[:n])
				total += int64(n)
				if total >= next {
					sample()
					next += 1 << 20
				}
			}
			if err != nil {
				break
			}
		}
		if r.ContentLength != total || len(r.TransferEncoding) > 0 {
			t.Errorf("content length %d, received %d, transfer encoding %v", r.ContentLength, total, r.TransferEncoding)
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, completionJSON("google/gemini-3.5-flash-lite", "described"))
	}))
	t.Cleanup(srv.Close)
	orSrv := &recorderSrv{Server: srv}

	cfg := fullConfig()
	cfg.AnthropicKey = ""
	s := newE2E(t, cfg, nil, orSrv)

	runtime.GC()
	var before runtime.MemStats
	runtime.ReadMemStats(&before)
	baseline = before.HeapAlloc

	got, err := s.DescribeMedia(context.Background(), describeReq(path, "video", "video/mp4"))
	sample()
	if err != nil || got.Text != "described" {
		t.Fatalf("got %+v, err %v", got, err)
	}
	var after runtime.MemStats
	runtime.ReadMemStats(&after)

	peakGrowth := int64(peak) - int64(baseline)
	allocated := after.TotalAlloc - before.TotalAlloc
	t.Logf("20 MiB file: body %d bytes, peak heap growth %.2f MiB, total allocated %.2f MiB",
		total, float64(peakGrowth)/(1<<20), float64(allocated)/(1<<20))
	if peakGrowth > 4<<20 {
		t.Errorf("peak heap grew by %d bytes, want well below the file size", peakGrowth)
	}
	if allocated > 4<<20 {
		t.Errorf("%d bytes allocated for a 20 MiB file", allocated)
	}

	// The body is the JSON around the file's base64, byte for byte.
	const marker = "base64,"
	i := bytes.Index(head, []byte(marker))
	if i < 0 {
		t.Fatalf("no data URI in the head: %.200s", head)
	}
	prefix := head[:i+len(marker)]
	j := bytes.IndexByte(tail, '"')
	if j < 0 {
		t.Fatalf("no end of the data URI in the tail: %.200s", tail)
	}
	suffix := tail[j:]
	want := sha256.New()
	want.Write(prefix)
	enc := base64.NewEncoder(base64.StdEncoding, want)
	src, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	if _, err := io.Copy(enc, src); err != nil {
		t.Fatal(err)
	}
	enc.Close()
	want.Write(suffix)
	if !bytes.Equal(sum.Sum(nil), want.Sum(nil)) {
		t.Error("the body is not prefix + base64(file) + suffix")
	}
	if wantLen := int64(len(prefix)) + 4*((size+2)/3) + int64(len(suffix)); total != wantLen {
		t.Errorf("body length %d, want %d", total, wantLen)
	}
}
