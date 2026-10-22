// SPDX-License-Identifier: Apache-2.0

package openrouter

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func writeFile(t *testing.T, n int) (path string, content []byte) {
	t.Helper()
	content = make([]byte, n)
	for i := range content {
		content[i] = byte(i*7 + 13)
	}
	path = filepath.Join(t.TempDir(), "media.bin")
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
	return path, content
}

func imageRequest(path string) Request {
	return Request{
		Model: "google/gemini-3.5-flash-lite",
		Messages: []Message{{Role: "user", Parts: []Part{
			TextPart("describe"),
			ImagePart("image/jpeg"),
		}}},
		ReasoningEffort: "medium",
		File:            path,
	}
}

func mustBody(t *testing.T, r Request) Body {
	t.Helper()
	b, err := r.Body()
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// doc §2: Content-Length is prefix + 4*ceil(n/3) + suffix, and the stream
// delivers exactly that many bytes. Sizes cover n%3 = 0, 1, 2 and the empty file.
func TestOpenLengthMatchesStream(t *testing.T) {
	for _, n := range []int{0, 1, 2, 3, 4, 5, 6, 47, 48, 49, 4095, 4096, 4097, 65536 + 1} {
		t.Run(strconv.Itoa(n), func(t *testing.T) {
			path, content := writeFile(t, n)
			body := mustBody(t, imageRequest(path))
			rc, length, err := body.Open()
			if err != nil {
				t.Fatal(err)
			}
			defer rc.Close()
			got, err := io.ReadAll(rc)
			if err != nil {
				t.Fatal(err)
			}
			if int64(len(got)) != length {
				t.Fatalf("stream is %d bytes, announced %d", len(got), length)
			}
			want := int64(len(body.prefix)) + 4*int64((n+2)/3) + int64(len(body.suffix))
			if length != want {
				t.Fatalf("length = %d, formula says %d", length, want)
			}
			var doc struct {
				Messages []struct {
					Content []struct {
						ImageURL struct {
							URL string `json:"url"`
						} `json:"image_url"`
					} `json:"content"`
				} `json:"messages"`
			}
			if err := json.Unmarshal(got, &doc); err != nil {
				t.Fatalf("stream is not JSON: %v", err)
			}
			url := doc.Messages[0].Content[1].ImageURL.URL
			enc, ok := strings.CutPrefix(url, "data:image/jpeg;base64,")
			if !ok {
				t.Fatalf("url = %q", url)
			}
			dec, err := base64.StdEncoding.DecodeString(enc)
			if err != nil || !bytes.Equal(dec, content) {
				t.Fatalf("file did not survive the round trip (err %v)", err)
			}
		})
	}
}

// The wire body of a file request never goes through the audit path whole.
func TestBodyJSONOnlyWithoutFile(t *testing.T) {
	path, _ := writeFile(t, 10)
	if _, ok := mustBody(t, imageRequest(path)).JSON(); ok {
		t.Error("a streamed body must not offer a JSON document")
	}
	doc, ok := mustBody(t, Request{Model: "m", Messages: []Message{{Role: "user", Text: "hi"}}}).JSON()
	if !ok || !json.Valid(doc) {
		t.Errorf("text body: ok=%v doc=%s", ok, doc)
	}
}

// doc §6 "OpenRouter": provider is in every request, whatever the path. Also the
// fixed key order of the Python body.
func TestProviderAndKeyOrderInEveryBody(t *testing.T) {
	path, _ := writeFile(t, 3)
	const provider = `"provider":{"data_collection":"deny","zdr":true}`
	cases := []struct {
		name string
		req  Request
		want string // the keys of the top level, in order
	}{
		{"eval", Request{
			Model:           "openai/gpt-6-luna",
			Messages:        []Message{{Role: "system", Text: "S"}, {Role: "user", Text: "U"}},
			ResponseFormat:  json.RawMessage(`{"type":"json_schema"}`),
			IncludeUsage:    true,
			MaxTokens:       32000,
			ReasoningEffort: "low",
		}, "model messages response_format usage provider max_tokens reasoning"},
		{"eval without optional", Request{
			Model:    "m",
			Messages: []Message{{Role: "user", Text: "U"}},
		}, "model messages provider"},
		{"image", Request{Model: "m", File: path, ReasoningEffort: "medium",
			Messages: []Message{{Role: "user", Parts: []Part{TextPart("t"), ImagePart("image/jpeg")}}}},
			"model messages provider reasoning"},
		{"video", Request{Model: "m", File: path,
			Messages: []Message{{Role: "user", Parts: []Part{TextPart("t"), VideoPart("video/mp4")}}}},
			"model messages provider"},
		{"audio", Request{Model: "m", File: path,
			Messages: []Message{{Role: "user", Parts: []Part{TextPart("t"), AudioPart("mp3")}}}},
			"model messages provider"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rc, _, err := mustBody(t, tc.req).Open()
			if err != nil {
				t.Fatal(err)
			}
			defer rc.Close()
			raw, _ := io.ReadAll(rc)
			if !strings.Contains(string(raw), provider) {
				t.Errorf("no fixed provider block in %.300s", raw)
			}
			if got := topKeys(t, raw); got != tc.want {
				t.Errorf("keys = %q, want %q", got, tc.want)
			}
		})
	}
}

func topKeys(t *testing.T, doc []byte) string {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(doc))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		t.Fatalf("not an object: %v %v", tok, err)
	}
	var keys []string
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			t.Fatal(err)
		}
		keys = append(keys, tok.(string))
		var skip json.RawMessage
		if err := dec.Decode(&skip); err != nil {
			t.Fatal(err)
		}
	}
	return strings.Join(keys, " ")
}

// doc §6 "OpenRouter": the shapes of the media parts, and reasoning as Python
// sent it. No HTML escaping: the prompts carry < and &.
func TestPartShapes(t *testing.T) {
	path, _ := writeFile(t, 3) // "\r\x14\x1b" -> base64 "DRQb"
	enc := base64.StdEncoding.EncodeToString([]byte{13, 20, 27})
	cases := []struct {
		name string
		part Part
		want string
	}{
		{"text", TextPart(`a <b> & "c"`), `{"type":"text","text":"a <b> & \"c\""}`},
		{"image", ImagePart("image/jpeg"), `{"type":"image_url","image_url":{"url":"data:image/jpeg;base64,` + enc + `"}}`},
		{"video", VideoPart("video/mp4"), `{"type":"video_url","video_url":{"url":"data:video/mp4;base64,` + enc + `"}}`},
		{"audio", AudioPart("mp3"), `{"type":"input_audio","input_audio":{"data":"` + enc + `","format":"mp3"}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := Request{Model: "m", Messages: []Message{{Role: "user", Parts: []Part{tc.part}}}}
			if tc.name != "text" {
				req.File = path
			}
			rc, _, err := mustBody(t, req).Open()
			if err != nil {
				t.Fatal(err)
			}
			defer rc.Close()
			raw, _ := io.ReadAll(rc)
			if !strings.Contains(string(raw), `"content":[`+tc.want+`]`) {
				t.Errorf("part not found in %s", raw)
			}
		})
	}

	rc, _, err := mustBody(t, Request{Model: "m", ReasoningEffort: "high",
		Messages: []Message{{Role: "user", Text: "x"}}}).Open()
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(rc)
	rc.Close()
	if !strings.Contains(string(raw), `"reasoning":{"enabled":true,"effort":"high","exclude":true}`) {
		t.Errorf("reasoning block missing in %s", raw)
	}
}

func TestBodyRejectsWrongFileUse(t *testing.T) {
	path, _ := writeFile(t, 3)
	cases := []struct {
		name string
		req  Request
	}{
		{"file part without file", Request{Model: "m", Messages: []Message{{Role: "user", Parts: []Part{ImagePart("image/jpeg")}}}}},
		{"two file parts", Request{Model: "m", File: path, Messages: []Message{{Role: "user", Parts: []Part{ImagePart("image/jpeg"), VideoPart("video/mp4")}}}}},
		{"file without part", Request{Model: "m", File: path, Messages: []Message{{Role: "user", Text: "x"}}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := tc.req.Body(); err == nil {
				t.Error("want an error")
			}
		})
	}
}

// A file that is shorter than measured must fail the request, not deliver a
// body shorter than its Content-Length.
func TestWriteFailsWhenFileShrinks(t *testing.T) {
	b := mustBody(t, imageRequest("unused"))
	var out bytes.Buffer
	err := b.write(&out, strings.NewReader("ab"), 10)
	if err == nil || !strings.Contains(err.Error(), "shorter") {
		t.Fatalf("err = %v", err)
	}
}

func TestOpenErrors(t *testing.T) {
	b := mustBody(t, imageRequest(filepath.Join(t.TempDir(), "missing")))
	if _, _, err := b.Open(); err == nil {
		t.Error("missing file: want an error")
	}
	dir := t.TempDir()
	if _, _, err := mustBody(t, imageRequest(dir)).Open(); err == nil {
		t.Error("directory: want an error")
	}

	c := NewClient("k", "")
	c.URL = "http://127.0.0.1:1/"
	_, err := c.Do(context.Background(), mustBody(t, imageRequest(filepath.Join(t.TempDir(), "missing"))), time.Second)
	var be *BodyError
	if !errors.As(err, &be) {
		t.Errorf("Do with a missing file: %T %v, want *BodyError", err, err)
	}
}

type captured struct {
	header        http.Header
	contentLength int64
	chunked       bool
	body          []byte
}

func server(t *testing.T, status int, reply string, hdr http.Header, got *[]captured) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		if got != nil {
			*got = append(*got, captured{r.Header.Clone(), r.ContentLength, len(r.TransferEncoding) > 0, raw})
		}
		for k, v := range hdr {
			w.Header()[k] = v
		}
		w.WriteHeader(status)
		io.WriteString(w, reply)
	}))
	t.Cleanup(srv.Close)
	c := NewClient("sk-or-test-key", "https://t.me/quotobot")
	c.URL = srv.URL
	return c
}

// doc §2 and 10-ai: the request is not chunked, its Content-Length is the
// computed one, and every attempt sends the whole file again.
func TestDoSendsExactContentLengthEveryAttempt(t *testing.T) {
	for _, n := range []int{0, 1, 2, 3, 1000, 70000} {
		t.Run(strconv.Itoa(n), func(t *testing.T) {
			path, content := writeFile(t, n)
			body := mustBody(t, imageRequest(path))
			var got []captured
			c := server(t, 200, `{"ok":true}`, nil, &got)
			for range 2 { // two attempts on the same Body: the file is reopened
				resp, err := c.Do(context.Background(), body, 5*time.Second)
				if err != nil {
					t.Fatal(err)
				}
				if string(resp.Body) != `{"ok":true}` || resp.Status != 200 {
					t.Fatalf("resp = %+v", resp)
				}
			}
			if len(got) != 2 {
				t.Fatalf("%d requests", len(got))
			}
			want := int64(len(body.prefix)) + 4*int64((n+2)/3) + int64(len(body.suffix))
			for i, g := range got {
				if g.chunked {
					t.Errorf("attempt %d: chunked", i)
				}
				if g.contentLength != want || int64(len(g.body)) != want {
					t.Errorf("attempt %d: Content-Length %d, body %d, want %d", i, g.contentLength, len(g.body), want)
				}
				if !bytes.Contains(g.body, []byte(base64.StdEncoding.EncodeToString(content))) {
					t.Errorf("attempt %d: file data missing", i)
				}
			}
		})
	}
}

// doc §6 "OpenRouter": headers.
func TestHeaders(t *testing.T) {
	var got []captured
	c := server(t, 200, `{}`, nil, &got)
	body := mustBody(t, Request{Model: "m", Messages: []Message{{Role: "user", Text: "x"}}})
	if _, err := c.Do(context.Background(), body, time.Second); err != nil {
		t.Fatal(err)
	}
	h := got[0].header
	for k, want := range map[string]string{
		"Authorization":      "Bearer sk-or-test-key",
		"Content-Type":       "application/json",
		"Http-Referer":       "https://t.me/quotobot",
		"X-Openrouter-Title": "Quoto",
	} {
		if h.Get(k) != want {
			t.Errorf("%s = %q, want %q", k, h.Get(k), want)
		}
	}
	if _, ok := h["X-Title"]; ok {
		t.Error("X-Title must not be sent")
	}

	got = nil
	c.Referer = ""
	if _, err := c.Do(context.Background(), body, time.Second); err != nil {
		t.Fatal(err)
	}
	if _, ok := got[0].header["Http-Referer"]; ok {
		t.Error("HTTP-Referer sent although empty")
	}
}

// Python raised on every non-2xx; the callers need the status, the Retry-After
// and a short piece of the body.
func TestStatusErrors(t *testing.T) {
	long := strings.Repeat("x", 5000)
	cases := []struct {
		name        string
		status      int
		body        string
		hdr         http.Header
		wantAfter   time.Duration
		wantSnippet string
	}{
		{"429 with retry-after", 429, `{"error":"slow down"}`, http.Header{"Retry-After": {"7"}}, 7 * time.Second, `{"error":"slow down"}`},
		{"500", 500, "boom", nil, 0, "boom"},
		{"402 keeps the text", 402, `{"error":{"message":"insufficient credits"}}`, nil, 0, `{"error":{"message":"insufficient credits"}}`},
		{"snippet is cut", 400, long, nil, 0, long[:snippetLimit]},
		{"invalid utf-8 is replaced", 400, "ok\xff\xfeend", nil, 0, "ok\ufffdend"},
		// 10-ai: the body of a 404 is neither read nor logged.
		{"404 body is not kept", 404, `{"error":"no endpoints found that support zdr"}`, nil, 0, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := server(t, tc.status, tc.body, tc.hdr, nil)
			_, err := c.Do(context.Background(), mustBody(t, Request{Model: "m", Messages: []Message{{Role: "user", Text: "x"}}}), time.Second)
			var se *StatusError
			if !errors.As(err, &se) {
				t.Fatalf("err = %T %v", err, err)
			}
			if se.Status != tc.status || se.RetryAfter != tc.wantAfter || se.Snippet != tc.wantSnippet {
				t.Errorf("got status %d, retry-after %v, snippet %q", se.Status, se.RetryAfter, se.Snippet)
			}
			if strings.Contains(err.Error(), "sk-or") || strings.Contains(err.Error(), tc.body) && tc.body != "" {
				t.Errorf("error text leaks: %q", err.Error())
			}
		})
	}
}

func TestRetryableStatus(t *testing.T) {
	for code, want := range map[int]bool{200: false, 400: false, 402: false, 404: false, 408: false, 429: true, 499: false, 500: true, 503: true, 599: true, 600: false} {
		if got := RetryableStatus(code); got != want {
			t.Errorf("RetryableStatus(%d) = %v", code, got)
		}
	}
}

func TestRetryAfter(t *testing.T) {
	cases := map[string]time.Duration{
		"":                              0,
		"0":                             0,
		"-3":                            0,
		"abc":                           0,
		"7":                             7 * time.Second,
		" 7 ":                           7 * time.Second,
		"7.5":                           7500 * time.Millisecond,
		"120":                           maxRetryAfter,
		"Wed, 21 Oct 2026 07:28:00 GMT": 0,
	}
	for in, want := range cases {
		h := http.Header{}
		if in != "" {
			h.Set("Retry-After", in)
		}
		if got := RetryAfter(h); got != want {
			t.Errorf("RetryAfter(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestRequestErrors(t *testing.T) {
	body := mustBody(t, Request{Model: "m", Messages: []Message{{Role: "user", Text: "x"}}})

	t.Run("connection refused", func(t *testing.T) {
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		addr := l.Addr().String()
		l.Close()
		c := NewClient("k", "")
		c.URL = "http://" + addr
		_, err = c.Do(context.Background(), body, time.Second)
		var re *RequestError
		if !errors.As(err, &re) {
			t.Errorf("err = %T %v", err, err)
		}
	})

	t.Run("timeout", func(t *testing.T) {
		release := make(chan struct{})
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			io.Copy(io.Discard, r.Body)
			select {
			case <-release:
			case <-r.Context().Done():
			}
		}))
		defer srv.Close()
		defer close(release)
		c := NewClient("k", "")
		c.URL = srv.URL
		_, err := c.Do(context.Background(), body, 50*time.Millisecond)
		var re *RequestError
		if !errors.As(err, &re) {
			t.Errorf("err = %T %v", err, err)
		}
	})

	t.Run("caller cancels", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			io.Copy(io.Discard, r.Body)
			<-r.Context().Done()
		}))
		defer srv.Close()
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		c := NewClient("k", "")
		c.URL = srv.URL
		_, err := c.Do(ctx, body, time.Second)
		if !errors.Is(err, context.Canceled) {
			t.Errorf("err = %T %v, want context.Canceled", err, err)
		}
	})

	t.Run("answer cut in the middle", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			io.Copy(io.Discard, r.Body)
			w.Header().Set("Content-Length", "100")
			w.WriteHeader(200)
			io.WriteString(w, "short")
		}))
		defer srv.Close()
		c := NewClient("k", "")
		c.URL = srv.URL
		_, err := c.Do(context.Background(), body, time.Second)
		var re *RequestError
		if !errors.As(err, &re) {
			t.Errorf("err = %T %v", err, err)
		}
	})

	t.Run("answer too long", func(t *testing.T) {
		chunk := make([]byte, 1<<20)
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			io.Copy(io.Discard, r.Body)
			for range 17 {
				if _, err := w.Write(chunk); err != nil {
					return
				}
			}
		}))
		defer srv.Close()
		c := NewClient("k", "")
		c.URL = srv.URL
		_, err := c.Do(context.Background(), body, 10*time.Second)
		var re *RequestError
		if !errors.As(err, &re) || !strings.Contains(err.Error(), "longer") {
			t.Errorf("err = %T %v", err, err)
		}
	})
}

// A request error text never carries the key: Go's url.Error prints the URL, and
// the key is in a header, so this pins that it stays there.
func TestErrorsDoNotCarryTheKey(t *testing.T) {
	c := NewClient("sk-or-secret-value", "")
	c.URL = "http://127.0.0.1:1/chat"
	_, err := c.Do(context.Background(), mustBody(t, Request{Model: "m", Messages: []Message{{Role: "user", Text: "x"}}}), time.Second)
	if err == nil || strings.Contains(err.Error(), "secret-value") {
		t.Errorf("err = %v", err)
	}
}
