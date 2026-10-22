// SPDX-License-Identifier: Apache-2.0

package ai

import (
	"context"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/FreshLabDev/quoto/internal/claude"
	"github.com/FreshLabDev/quoto/internal/domain"
	"github.com/FreshLabDev/quoto/internal/openrouter"
)

// mediaFile writes a file of n bytes that stands in for a normalised one.
func mediaFile(t *testing.T, n int) (path string, content []byte) {
	t.Helper()
	content = make([]byte, n)
	for i := range content {
		content[i] = byte(i*31 + 7)
	}
	path = filepath.Join(t.TempDir(), "normalized.bin")
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
	return path, content
}

func describeReq(path, kind, mime string) domain.MediaDescribeRequest {
	return domain.MediaDescribeRequest{Path: path, MimeType: mime, Kind: kind}
}

// mediaCompletion is OpenRouter's answer to a media request: a description and
// the three token counts media keeps.
func mediaCompletion(model, text string) *openrouter.Response {
	return &openrouter.Response{Status: 200, Body: []byte(
		`{"model":"` + model + `","choices":[{"message":{"content":` + quote(text) + `}}],"usage":{"prompt_tokens":321,"completion_tokens":45,"total_tokens":366}}`)}
}

func quote(s string) string { return string(appendString(nil, s)) }

func claudeDescription(model, text string) *claude.Response {
	r := claudeAnswer(model, text)
	r.Usage.Counts = claude.Counts{Input: 800, Output: 60, CacheRead: 100}
	return r
}

// tests/test_ai_claude.py::test_describe_media_* (CLA-16, CLA-17): an image goes
// to Claude as a base64 image block and then the prompt; the body carries only
// the effort in output_config; OpenRouter is not called.
func TestDescribeMediaWithClaude(t *testing.T) {
	cases := []struct {
		kind   string
		prompt string
	}{
		{"photo", imageDescriptionPrompt},
		{"image", imageDescriptionPrompt},
		{"sticker", stickerDescriptionPrompt}, // CLA-17
	}
	for _, tc := range cases {
		t.Run(tc.kind, func(t *testing.T) {
			r := newRig(t, fullConfig())
			path, content := mediaFile(t, 3000)
			r.claude.reply = claudeSeq(claudeDescription("claude-sonnet-5-5", "  A cat on a windowsill.\n"))

			got, err := r.s.DescribeMedia(context.Background(), describeReq(path, tc.kind, "image/jpeg"))
			if err != nil {
				t.Fatal(err)
			}
			want := domain.MediaDescription{Text: "A cat on a windowsill.", ActualModel: "claude-sonnet-5-5", PromptTokens: 900, CompletionTokens: 60, TotalTokens: 960}
			if got != want {
				t.Errorf("got %+v, want %+v", got, want)
			}
			if r.router.count() != 0 || len(r.claude.calls) != 1 {
				t.Fatalf("claude %d, openrouter %d", len(r.claude.calls), r.router.count())
			}
			wire, err := r.claude.calls[0].Body()
			if err != nil {
				t.Fatal(err)
			}
			body := decode(t, wire)
			content0 := sub(t, body, "messages").([]any)[0].(map[string]any)["content"].([]any)
			img, txt := content0[0].(map[string]any), content0[1].(map[string]any)
			if img["type"] != "image" || sub(t, img, "source", "type") != "base64" || sub(t, img, "source", "media_type") != "image/jpeg" ||
				sub(t, img, "source", "data") != base64.StdEncoding.EncodeToString(content) {
				t.Errorf("image block = %.200v", img)
			}
			if txt["type"] != "text" || txt["text"] != tc.prompt {
				t.Errorf("text block = %v", txt)
			}
			if !reflect.DeepEqual(body["output_config"], map[string]any{"effort": "medium"}) || body["fallbacks"] != "default" ||
				sub(t, body, "thinking", "type") != "adaptive" || body["max_tokens"] != float64(8000) || body["model"] != "claude-sonnet-5-5" {
				t.Errorf("body = %.300s", wire)
			}
			if _, ok := body["system"]; ok {
				t.Error("media has no system prompt")
			}
			if len(r.audit.recs) != 0 {
				t.Error("media is not audited")
			}
		})
	}
}

// The route of every analysis kind the media package can send (coordinator:
// photo, image, sticker, video, animation, video_note, voice, audio), the part
// OpenRouter gets, and the prompt. tests/test_ai_claude.py (CLA-19: video never
// goes to Claude) and doc section 6 "OpenRouter".
func TestDescribeMediaRoutes(t *testing.T) {
	cases := []struct {
		kind, mime string
		claude     bool
		part       string // the key of the media part in the OpenRouter request
		prompt     string
	}{
		{"photo", "image/jpeg", true, "image_url", imageDescriptionPrompt},
		{"image", "image/jpeg", true, "image_url", imageDescriptionPrompt},
		{"sticker", "image/jpeg", true, "image_url", stickerDescriptionPrompt},
		{"video", "video/mp4", false, "video_url", videoDescriptionPrompt},
		{"animation", "video/mp4", false, "video_url", videoDescriptionPrompt},
		{"video_note", "video/mp4", false, "video_url", videoDescriptionPrompt},
		{"voice", "audio/mpeg", false, "input_audio", audioDescriptionPrompt},
		{"audio", "audio/mpeg", false, "input_audio", audioDescriptionPrompt},
	}
	for _, tc := range cases {
		t.Run(tc.kind, func(t *testing.T) {
			path, content := mediaFile(t, 1000)
			enc := base64.StdEncoding.EncodeToString(content)

			// With Claude configured: images go there, the rest do not.
			r := newRig(t, fullConfig())
			r.claude.reply = claudeSeq(claudeDescription("claude-sonnet-5-5", "from claude"))
			r.router.reply = orSeq(mediaCompletion("google/gemini-3.5-flash-lite", "from openrouter"))
			got, err := r.s.DescribeMedia(context.Background(), describeReq(path, tc.kind, tc.mime))
			if err != nil {
				t.Fatal(err)
			}
			if tc.claude {
				if got.Text != "from claude" || r.router.count() != 0 {
					t.Errorf("got %+v, openrouter calls %d", got, r.router.count())
				}
				return
			}
			if got.Text != "from openrouter" || len(r.claude.calls) != 0 {
				t.Errorf("got %+v, claude calls %d", got, len(r.claude.calls))
			}

			doc := r.router.docs[0]
			body := decode(t, doc)
			if want := []string{"model", "messages", "provider", "reasoning"}; !reflect.DeepEqual(jsonKeys(t, doc), want) {
				t.Errorf("keys = %v, want %v (no usage, max_tokens or response_format for media)", jsonKeys(t, doc), want)
			}
			if !reflect.DeepEqual(body["provider"], map[string]any{"data_collection": "deny", "zdr": true}) ||
				!reflect.DeepEqual(body["reasoning"], map[string]any{"enabled": true, "effort": "medium", "exclude": true}) {
				t.Errorf("body = %.300s", doc)
			}
			parts := sub(t, body, "messages").([]any)[0].(map[string]any)["content"].([]any)
			if len(parts) != 2 || !reflect.DeepEqual(parts[0], map[string]any{"type": "text", "text": tc.prompt}) { // text first, then the media
				t.Fatalf("parts = %.300v", parts)
			}
			media := parts[1].(map[string]any)
			if media["type"] != tc.part {
				t.Errorf("part = %.100v", media)
			}
			switch tc.part {
			case "input_audio":
				if sub(t, media, "input_audio", "format") != "mp3" || sub(t, media, "input_audio", "data") != enc {
					t.Errorf("audio = %.100v", media)
				}
			default:
				if sub(t, media, tc.part, "url") != "data:"+tc.mime+";base64,"+enc {
					t.Errorf("part = %.100v", media)
				}
			}
		})
	}
}

// Claude only takes a JPEG that fits the API's limit, with a key and a model.
func TestDescribeMediaSkipsClaude(t *testing.T) {
	big := func(t *testing.T) string {
		path, _ := mediaFile(t, 10)
		if err := os.Truncate(path, claudeMediaMaxBytes+1); err != nil {
			t.Fatal(err)
		}
		return path
	}
	cases := []struct {
		name string
		edit func(*Config)
		mime string
		path func(*testing.T) string
	}{
		{"png", nil, "image/png", nil},
		{"over 5 MiB", nil, "image/jpeg", big},
		{"no Anthropic key", func(c *Config) { c.AnthropicKey = "" }, "image/jpeg", nil},
		{"no Claude media model", func(c *Config) { c.AnthropicMediaModel = "" }, "image/jpeg", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := fullConfig()
			if tc.edit != nil {
				tc.edit(&cfg)
			}
			r := newRig(t, cfg)
			r.router.reply = orSeq(mediaCompletion("google/gemini-3.5-flash-lite", "from openrouter"))
			path, _ := mediaFile(t, 100)
			if tc.path != nil {
				path = tc.path(t)
			}
			got, err := r.s.DescribeMedia(context.Background(), describeReq(path, "photo", tc.mime))
			if err != nil || got.Text != "from openrouter" || len(r.claude.calls) != 0 {
				t.Errorf("got %+v, err %v, claude calls %d", got, err, len(r.claude.calls))
			}
		})
	}

	t.Run("exactly 5 MiB goes to Claude", func(t *testing.T) {
		r := newRig(t, fullConfig())
		r.claude.reply = claudeSeq(claudeDescription("claude-sonnet-5-5", "ok"))
		path, _ := mediaFile(t, 10)
		if err := os.Truncate(path, claudeMediaMaxBytes); err != nil {
			t.Fatal(err)
		}
		if _, err := r.s.DescribeMedia(context.Background(), describeReq(path, "photo", "image/jpeg")); err != nil || len(r.claude.calls) != 1 {
			t.Errorf("err %v, claude calls %d", err, len(r.claude.calls))
		}
	})
}

// tests/test_ai_claude.py::test_media_falls_back_to_openrouter_on_claude_error
// (CLA-18).
func TestDescribeMediaFallsBackFromClaude(t *testing.T) {
	path, _ := mediaFile(t, 500)
	for name, claudeErr := range map[string]error{
		"500":        &claude.APIError{Status: 500, Type: "api_error"},
		"connection": &claude.ConnError{Err: claude.ErrIdle},
		"refusal":    errors.New("refusal"), // replaced below by a refusal response
		"stream":     &claude.StreamError{Err: errors.New("bad event")},
	} {
		t.Run(name, func(t *testing.T) {
			r := newRig(t, fullConfig())
			if name == "refusal" {
				cat := "cyber"
				r.claude.reply = func(int, claude.Request) (*claude.Response, error) {
					resp := claudeAnswer("claude-sonnet-5-5", "")
					resp.StopReason, resp.StopDetails = "refusal", &claude.StopDetails{Category: &cat}
					return resp, nil
				}
			} else {
				r.claude.reply = claudeSeq(claudeErr)
			}
			r.router.reply = orSeq(mediaCompletion("google/gemini-2.5-flash-lite", "from openrouter"))
			got, err := r.s.DescribeMedia(context.Background(), describeReq(path, "photo", "image/jpeg"))
			if err != nil || got.Text != "from openrouter" || got.ActualModel != "google/gemini-2.5-flash-lite" {
				t.Errorf("got %+v, err %v", got, err)
			}
			if got.PromptTokens != 321 || got.CompletionTokens != 45 || got.TotalTokens != 366 {
				t.Errorf("tokens = %+v", got)
			}
			if len(r.claude.calls) != 1 || r.router.count() != 1 {
				t.Errorf("claude %d, openrouter %d", len(r.claude.calls), r.router.count())
			}
		})
	}
}

// tests/test_ai_claude.py::test_media_cooldown_does_not_block_claude (CLA-21).
func TestOpenRouterCooldownDoesNotBlockClaude(t *testing.T) {
	r := newRig(t, fullConfig())
	r.s.mediaPauseStart()
	r.claude.reply = claudeSeq(claudeDescription("claude-sonnet-5-5", "described"))
	path, _ := mediaFile(t, 100)
	got, err := r.s.DescribeMedia(context.Background(), describeReq(path, "photo", "image/jpeg"))
	if err != nil || got.Text != "described" {
		t.Errorf("got %+v, err %v", got, err)
	}
}

// tests/test_ai_claude.py (CLA-22, CLA-23): Claude is the only provider and it
// fails. A billing error and a refusal both end as a retryable error that names
// the model and the cause.
func TestDescribeMediaClaudeOnly(t *testing.T) {
	cfg := fullConfig()
	cfg.OpenRouterKey = ""
	path, _ := mediaFile(t, 100)

	var re *domain.MediaRetryableError
	var ex *modelsExhaustedError

	r := newRig(t, cfg)
	r.claude.reply = claudeSeq(&claude.APIError{Status: 402, Type: "billing_error", Message: "credit balance is too low"})
	_, err := r.s.DescribeMedia(context.Background(), describeReq(path, "photo", "image/jpeg"))
	if !errors.As(err, &re) || !errors.As(err, &ex) || !re.CountsAsAttempt || re.RetryAfter != 0 { // CLA-22
		t.Fatalf("err = %T %v", err, err)
	}
	if !strings.Contains(err.Error(), "claude-sonnet-5-5") || !strings.Contains(err.Error(), "402") || !strings.Contains(err.Error(), "credit balance") {
		t.Errorf("text = %q", err)
	}

	r = newRig(t, cfg)
	cat := "general_harms"
	r.claude.reply = func(int, claude.Request) (*claude.Response, error) {
		resp := claudeAnswer("claude-sonnet-5-5", "")
		resp.StopReason, resp.StopDetails = "refusal", &claude.StopDetails{Category: &cat}
		return resp, nil
	}
	_, err = r.s.DescribeMedia(context.Background(), describeReq(path, "photo", "image/jpeg"))
	if !errors.As(err, &re) || !strings.Contains(err.Error(), "declined") || !strings.Contains(err.Error(), "general_harms") { // CLA-23
		t.Errorf("err = %T %v", err, err)
	}

	// A request Anthropic called wrong will not be better later.
	r = newRig(t, cfg)
	r.claude.reply = claudeSeq(&claude.APIError{Status: 400, Type: "invalid_request_error"})
	_, err = r.s.DescribeMedia(context.Background(), describeReq(path, "photo", "image/jpeg"))
	if errors.As(err, &re) || !errors.As(err, &ex) {
		t.Errorf("a 400 must be final: %T %v", err, err)
	}
}

// tests/test_ai_claude.py::test_is_retryable_media_error (CLA-24), extended to
// every error the package produces.
func TestMediaRetryable(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"claude connection", &claude.ConnError{Err: claude.ErrTruncated}, true},
		{"claude 400", &claude.APIError{Status: 400, Type: "invalid_request_error"}, false},
		{"claude 401", &claude.APIError{Status: 401}, false},
		{"claude 402", &claude.APIError{Status: 402, Type: "billing_error"}, true},
		{"claude 429", &claude.APIError{Status: 429}, true},
		{"claude 500", &claude.APIError{Status: 500}, true},
		{"claude overloaded in a stream", &claude.APIError{Status: 200, Type: "overloaded_error"}, true},
		{"claude bad stream", &claude.StreamError{Err: errors.New("x")}, false},
		{"unusable answer", &responseError{"empty"}, true},
		{"request error", &openrouter.RequestError{Err: errors.New("dial")}, true},
		{"openrouter 400", statusErr(400), false},
		{"openrouter 401", statusErr(401), false},
		{"openrouter 402", statusErr(402), true},
		{"openrouter 403", statusErr(403), false},
		{"openrouter 404", statusErr(404), false},
		{"openrouter 408", statusErr(408), true},
		{"openrouter 409", statusErr(409), true},
		{"openrouter 425", statusErr(425), true},
		{"openrouter 429", statusErr(429), true},
		{"openrouter 500", statusErr(500), true},
		{"openrouter 503", statusErr(503), true},
		{"something else", errors.New("boom"), false},
		{"wrapped", errors.Join(errors.New("ctx"), statusErr(503)), true},
	}
	for _, tc := range cases {
		if got := mediaRetryable(tc.err); got != tc.want {
			t.Errorf("%s: %v, want %v", tc.name, got, tc.want)
		}
	}
}

// doc section 6 "Queue claim": the cooldown does not count as an attempt. It
// opens when every OpenRouter model said 402, lasts 900 s, and a call during it
// sends nothing.
func TestOpenRouterMediaCooldown(t *testing.T) {
	cfg := fullConfig()
	cfg.AnthropicKey = ""
	path, _ := mediaFile(t, 100)
	req := describeReq(path, "video", "video/mp4")

	r := newRig(t, cfg)
	r.router.reply = orSeq(statusErr(402))
	_, err := r.s.DescribeMedia(context.Background(), req)
	var re *domain.MediaRetryableError
	if !errors.As(err, &re) || !re.CountsAsAttempt || re.RetryAfter != 900*time.Second {
		t.Fatalf("first call: %T %v %+v", err, err, re)
	}
	if got := r.router.models(t); !reflect.DeepEqual(got, []string{"google/gemini-3.5-flash-lite", "google/gemini-3.1-flash-lite"}) {
		t.Errorf("models = %v", got) // a 402 is not retried on the same model
	}

	// During the pause nothing is sent and the attempt is not counted.
	r.clock.advance(100 * time.Second)
	before := r.router.count()
	_, err = r.s.DescribeMedia(context.Background(), req)
	if !errors.As(err, &re) || re.CountsAsAttempt || re.RetryAfter != 800*time.Second || r.router.count() != before {
		t.Fatalf("during the pause: %T %v %+v, %d new calls", err, err, re, r.router.count()-before)
	}
	if !strings.Contains(err.Error(), "cooldown") {
		t.Errorf("text = %q", err)
	}

	// After it, requests go out again, and a success clears the state.
	r.clock.advance(801 * time.Second)
	r.router.reply = orSeq(mediaCompletion("google/gemini-3.5-flash-lite", "ok"))
	if got, err := r.s.DescribeMedia(context.Background(), req); err != nil || got.Text != "ok" {
		t.Fatalf("after the pause: %+v %v", got, err)
	}
	if !r.s.mediaPausedUntil.IsZero() {
		t.Error("the pause was not cleared")
	}

	t.Run("after a Claude failure the pause makes the error retryable with its time", func(t *testing.T) {
		r := newRig(t, fullConfig())
		r.s.mediaPauseStart()
		r.claude.reply = claudeSeq(&claude.APIError{Status: 400})
		path, _ := mediaFile(t, 100)
		_, err := r.s.DescribeMedia(context.Background(), describeReq(path, "photo", "image/jpeg"))
		if !errors.As(err, &re) || !re.CountsAsAttempt || re.RetryAfter != 900*time.Second || r.router.count() != 0 {
			t.Errorf("err = %T %v %+v", err, err, re)
		}
	})

	t.Run("a mix of 402 and another error opens no pause", func(t *testing.T) {
		for _, second := range []error{statusErr(500), statusErr(404), timeoutErr()} {
			r := newRig(t, cfg)
			r.router.reply = orSeq(statusErr(402), second)
			// 500 and a timeout are retried three times on their own model.
			_, err := r.s.DescribeMedia(context.Background(), req)
			if !errors.As(err, &re) || re.RetryAfter != 0 || r.s.mediaPauseRemaining() != 0 {
				t.Errorf("second error %v: %T %v %+v", second, err, err, re)
			}
		}
	})

	t.Run("404 on every model is final and says so", func(t *testing.T) {
		r := newRig(t, cfg)
		r.router.reply = orSeq(&openrouter.StatusError{Status: 404})
		_, err := r.s.DescribeMedia(context.Background(), req)
		var ex *modelsExhaustedError
		if errors.As(err, &re) || !errors.As(err, &ex) || r.s.mediaPauseRemaining() != 0 {
			t.Fatalf("err = %T %v", err, err)
		}
		for _, want := range []string{"google/gemini-3.5-flash-lite", "google/gemini-3.1-flash-lite", "HTTP 404"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("text %q lacks %q", err, want)
			}
		}
	})
}

// tests/test_ai.py::test_*media*_retries: 429 and 5xx and broken connections are
// tried again on the same model, three times; the next model comes after.
func TestOpenRouterMediaRetries(t *testing.T) {
	cfg := fullConfig()
	cfg.AnthropicKey = ""
	path, _ := mediaFile(t, 100)
	req := describeReq(path, "audio", "audio/mpeg")
	const primary, fallback = "google/gemini-3.5-flash-lite", "google/gemini-3.1-flash-lite"

	t.Run("429 with Retry-After, then success", func(t *testing.T) {
		r := newRig(t, cfg)
		r.router.reply = orSeq(&openrouter.StatusError{Status: 429, RetryAfter: 3 * time.Second}, statusErr(503), mediaCompletion(primary, "ok"))
		got, err := r.s.DescribeMedia(context.Background(), req)
		if err != nil || got.Text != "ok" || r.router.count() != 3 {
			t.Fatalf("got %+v err %v calls %d", got, err, r.router.count())
		}
		if s := r.slept(); !reflect.DeepEqual(s, []time.Duration{3 * time.Second, 9 * time.Second}) {
			t.Errorf("slept %v", s)
		}
		if r.router.timeouts[0] != 180*time.Second {
			t.Errorf("timeout = %v", r.router.timeouts[0])
		}
	})

	t.Run("three tries and then the fallback", func(t *testing.T) {
		r := newRig(t, cfg)
		r.router.reply = func(n int, doc []byte) (*openrouter.Response, error) {
			if requestModel(t, doc) == primary {
				return nil, timeoutErr()
			}
			return mediaCompletion(fallback, "from the fallback"), nil
		}
		got, err := r.s.DescribeMedia(context.Background(), req)
		if err != nil || got.ActualModel != fallback {
			t.Fatalf("got %+v err %v", got, err)
		}
		if m := r.router.models(t); !reflect.DeepEqual(m, []string{primary, primary, primary, fallback}) {
			t.Errorf("models = %v", m)
		}
	})

	t.Run("a bad request is not retried", func(t *testing.T) {
		r := newRig(t, cfg)
		r.router.reply = orSeq(statusErr(400), mediaCompletion(fallback, "ok"))
		got, err := r.s.DescribeMedia(context.Background(), req)
		if err != nil || got.ActualModel != fallback || r.router.count() != 2 || len(r.slept()) != 0 {
			t.Errorf("got %+v err %v calls %d", got, err, r.router.count())
		}
	})

	t.Run("an unusable answer moves to the next model", func(t *testing.T) {
		for name, resp := range map[string]*openrouter.Response{
			"empty content":    mediaCompletion(primary, "   "),
			"no choices":       {Status: 200, Body: []byte(`{"model":"` + primary + `","choices":[]}`)},
			"not JSON":         {Status: 200, Body: []byte(`<html>`)},
			"not an object":    {Status: 200, Body: []byte(`[1]`)},
			"content is a map": {Status: 200, Body: []byte(`{"choices":[{"message":{"content":{"a":1}}}]}`)},
		} {
			r := newRig(t, cfg)
			r.router.reply = orSeq(resp, mediaCompletion(fallback, "second"))
			got, err := r.s.DescribeMedia(context.Background(), req)
			if err != nil || got.Text != "second" || r.router.count() != 2 {
				t.Errorf("%s: got %+v err %v calls %d", name, got, err, r.router.count())
			}
		}
	})

	t.Run("a model missing from the answer is the requested one, tokens may be absent", func(t *testing.T) {
		r := newRig(t, cfg)
		r.router.reply = orSeq(&openrouter.Response{Status: 200, Body: []byte(`{"choices":[{"message":{"content":"hi"}}]}`)})
		got, err := r.s.DescribeMedia(context.Background(), req)
		if err != nil || got != (domain.MediaDescription{Text: "hi", ActualModel: primary}) {
			t.Errorf("got %+v err %v", got, err)
		}
	})
}

// doc 10-ai: the fallback must not repeat the primary, and a chain with no model
// is an error instead of a request for "".
func TestMediaModelChain(t *testing.T) {
	path, _ := mediaFile(t, 100)
	req := describeReq(path, "video", "video/mp4")

	cfg := fullConfig()
	cfg.AnthropicKey = ""
	cfg.OpenRouterMediaFallback = cfg.OpenRouterMediaModel
	r := newRig(t, cfg)
	r.router.reply = orSeq(statusErr(400))
	r.s.DescribeMedia(context.Background(), req)
	if r.router.count() != 1 {
		t.Errorf("the same model was asked %d times", r.router.count())
	}

	cfg.OpenRouterMediaModel, cfg.OpenRouterMediaFallback = "", ""
	r = newRig(t, cfg)
	if _, err := r.s.DescribeMedia(context.Background(), req); err == nil || r.router.count() != 0 {
		t.Errorf("err = %v", err)
	}
}

// Errors that are the caller's, not the models': a bad kind, a missing file, no
// keys. They are final, and nothing is sent.
func TestDescribeMediaFinalErrors(t *testing.T) {
	path, _ := mediaFile(t, 100)
	dir := t.TempDir()
	cases := []struct {
		name string
		cfg  func() Config
		req  domain.MediaDescribeRequest
		want string
	}{
		{"unsupported kind", fullConfig, describeReq(path, "document", "application/pdf"), "unsupported media kind"},
		{"missing file", fullConfig, describeReq(filepath.Join(dir, "gone"), "video", "video/mp4"), "media file"},
		{"a directory", fullConfig, describeReq(dir, "video", "video/mp4"), "not a regular file"},
		{"no keys", func() Config { return Config{} }, describeReq(path, "photo", "image/jpeg"), "no AI provider key"},
		{"video with only an Anthropic key", func() Config {
			c := fullConfig()
			c.OpenRouterKey = ""
			return c
		}, describeReq(path, "video", "video/mp4"), "no AI provider key"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(t, tc.cfg())
			_, err := r.s.DescribeMedia(context.Background(), tc.req)
			var re *domain.MediaRetryableError
			if err == nil || errors.As(err, &re) || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %T %v, want a final error with %q", err, err, tc.want)
			}
			if len(r.claude.calls) != 0 || r.router.count() != 0 {
				t.Error("something was sent")
			}
		})
	}
}

func TestDescribeMediaCancelled(t *testing.T) {
	path, _ := mediaFile(t, 100)
	ctx, cancel := context.WithCancel(context.Background())
	r := newRig(t, fullConfig())
	r.claude.reply = func(int, claude.Request) (*claude.Response, error) {
		cancel()
		return nil, ctx.Err()
	}
	_, err := r.s.DescribeMedia(ctx, describeReq(path, "photo", "image/jpeg"))
	if !errors.Is(err, context.Canceled) || r.router.count() != 0 {
		t.Errorf("err = %v, openrouter calls %d", err, r.router.count())
	}

	ctx, cancel = context.WithCancel(context.Background())
	cfg := fullConfig()
	cfg.AnthropicKey = ""
	r = newRig(t, cfg)
	r.router.reply = func(int, []byte) (*openrouter.Response, error) {
		cancel()
		return nil, ctx.Err()
	}
	_, err = r.s.DescribeMedia(ctx, describeReq(path, "video", "video/mp4"))
	if !errors.Is(err, context.Canceled) || r.router.count() != 1 {
		t.Errorf("err = %v, calls %d", err, r.router.count())
	}
}

// doc 7.0 "one redactor": the text that ends up in the database has no secret in
// it, whichever provider echoed it.
func TestExhaustedErrorIsRedacted(t *testing.T) {
	cfg := fullConfig()
	path, _ := mediaFile(t, 100)
	r := newRig(t, cfg)
	r.claude.reply = claudeSeq(&claude.APIError{Status: 401, Message: "bad x-api-key sk-ant-test-key"})
	r.router.reply = orSeq(&openrouter.StatusError{Status: 401, Snippet: `{"error":"invalid key or-test-key"}`})
	_, err := r.s.DescribeMedia(context.Background(), describeReq(path, "photo", "image/jpeg"))
	if err == nil {
		t.Fatal("no error")
	}
	if strings.Contains(err.Error(), "sk-ant-test-key") || strings.Contains(err.Error(), "or-test-key") || !strings.Contains(err.Error(), "***") {
		t.Errorf("text = %q", err)
	}
	if strings.Contains(r.logs.String(), "sk-ant-test-key") || strings.Contains(r.logs.String(), "or-test-key") {
		t.Errorf("a key is in the log:\n%s", r.logs)
	}
}
