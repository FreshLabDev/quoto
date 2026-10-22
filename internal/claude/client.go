// SPDX-License-Identifier: Apache-2.0

package claude

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// Variables rather than constants so that tests can shorten them.
var (
	// IdleTimeout is how long the API may stay silent, before the headers and
	// between events. It is the SDK's 300 s per network operation: a long streamed
	// answer keeps the connection busy and never trips it.
	IdleTimeout = 300 * time.Second
	// MaxRetries is the number of extra tries for a call that failed before any
	// of its stream was read (connection error, 408, 409, 429, 5xx), as the
	// SDK's max_retries. A failure in the middle of a stream is not retried here;
	// the caller decides.
	MaxRetries = 2
	// maxRetryAfter is the longest Retry-After the client waits for.
	maxRetryAfter = 60 * time.Second
)

const (
	// maxErrorBody bounds what is read of a non-2xx answer.
	maxErrorBody = 64 << 10
	// maxErrorMessage bounds the message kept from an error body.
	maxErrorMessage = 500
)

// Client calls the Messages API. The zero value is not usable; use NewClient.
type Client struct {
	APIKey string
	// BaseURL defaults to DefaultBaseURL; tests point it at an httptest server.
	BaseURL string
	// HTTP has no overall timeout: the client applies IdleTimeout itself.
	HTTP *http.Client
	// Sleep waits d or until ctx ends. Tests replace it.
	Sleep func(ctx context.Context, d time.Duration) error
	// Rand returns a number in [0, 1) for retry jitter.
	Rand func() float64
}

// NewClient returns a client for apiKey.
func NewClient(apiKey string) *Client {
	return &Client{
		APIKey:  apiKey,
		BaseURL: DefaultBaseURL,
		HTTP: &http.Client{Transport: &http.Transport{
			Proxy:                 http.ProxyFromEnvironment,
			DialContext:           (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
			ForceAttemptHTTP2:     true,
			MaxIdleConns:          16,
			MaxIdleConnsPerHost:   4,
			IdleConnTimeout:       90 * time.Second,
			TLSHandshakeTimeout:   10 * time.Second,
			ExpectContinueTimeout: time.Second,
		}},
		Sleep: sleep,
		Rand:  rand.Float64,
	}
}

func sleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Message sends req as a streamed call and returns the finished message.
//
// A call that fails before its stream starts (no connection, 408, 409, 429,
// 5xx, or an x-should-retry: true) is tried again up to MaxRetries times,
// waiting for Retry-After when the API sent one. Errors are *APIError,
// *ConnError or *StreamError; a cancelled ctx returns ctx's own error.
func (c *Client) Message(ctx context.Context, req Request) (*Response, error) {
	body, err := marshal(req.body(true))
	if err != nil {
		return nil, fmt.Errorf("claude: encode request: %w", err)
	}
	for try := 0; ; try++ {
		resp, retry, err := c.once(ctx, body)
		if err == nil {
			return resp, nil
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if !retry.ok || try >= MaxRetries {
			return nil, err
		}
		if serr := c.sleep(ctx, c.retryDelay(try, retry.after)); serr != nil {
			return nil, serr
		}
	}
}

func (c *Client) sleep(ctx context.Context, d time.Duration) error {
	if c.Sleep != nil {
		return c.Sleep(ctx, d)
	}
	return sleep(ctx, d)
}

// retryDelay is the SDK's: the API's Retry-After when it is sane, else
// min(0.5 * 2^try, 8) seconds less up to a quarter of that as jitter.
func (c *Client) retryDelay(try int, after time.Duration) time.Duration {
	if after > 0 && after <= maxRetryAfter {
		return after
	}
	d := min(0.5*float64(uint(1)<<uint(try)), 8.0)
	r := rand.Float64
	if c.Rand != nil {
		r = c.Rand
	}
	return time.Duration(d * (1 - 0.25*r()) * float64(time.Second))
}

type retryInfo struct {
	ok    bool
	after time.Duration
}

// once makes one HTTP request and reads its stream. retry says whether the SDK
// would try the call again.
func (c *Client) once(parent context.Context, body []byte) (*Response, retryInfo, error) {
	ctx, cancel := context.WithCancelCause(parent)
	defer cancel(nil)
	wd := newWatchdog(IdleTimeout, func() { cancel(ErrIdle) })
	defer wd.stop()

	base := c.BaseURL
	if base == "" {
		base = DefaultBaseURL
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(base, "/")+MessagesPath, bytes.NewReader(body))
	if err != nil {
		return nil, retryInfo{}, fmt.Errorf("claude: build request: %w", err)
	}
	httpReq.Header.Set("x-api-key", c.APIKey)
	httpReq.Header.Set("anthropic-version", APIVersion)
	httpReq.Header.Set("anthropic-beta", FallbackBeta)
	httpReq.Header.Set("content-type", "application/json")
	httpReq.Header.Set("accept", "text/event-stream")
	httpReq.Header.Set("user-agent", "quoto")

	hc := c.HTTP
	if hc == nil {
		hc = http.DefaultClient
	}
	resp, err := hc.Do(httpReq)
	if err != nil {
		return nil, retryInfo{ok: true}, connFailure(parent, ctx, err)
	}
	defer resp.Body.Close()
	wd.reset()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		apiErr := readAPIError(resp)
		return nil, shouldRetry(resp), apiErr
	}

	out, err := parseStream(&idleReader{r: resp.Body, wd: wd}, resp.StatusCode)
	if err != nil {
		var conn *ConnError
		if errors.As(err, &conn) {
			return nil, retryInfo{}, connFailure(parent, ctx, conn.Err)
		}
		var api *APIError
		if errors.As(err, &api) {
			api.RequestID = resp.Header.Get("request-id")
		}
		return nil, retryInfo{}, err
	}
	out.RequestID = resp.Header.Get("request-id")
	return out, retryInfo{}, nil
}

// connFailure turns a transport error into the error the caller sees: its own
// cancellation as it is, the idle timeout as ErrIdle, the rest as ConnError.
func connFailure(parent, ctx context.Context, err error) error {
	if parent.Err() != nil {
		return parent.Err()
	}
	if cause := context.Cause(ctx); errors.Is(cause, ErrIdle) {
		return &ConnError{ErrIdle}
	}
	var conn *ConnError
	if errors.As(err, &conn) {
		return err
	}
	return &ConnError{err}
}

// shouldRetry follows the SDK: x-should-retry decides when present, otherwise
// 408, 409, 429 and 5xx are retried.
func shouldRetry(resp *http.Response) retryInfo {
	info := retryInfo{after: retryAfter(resp.Header)}
	switch strings.ToLower(resp.Header.Get("x-should-retry")) {
	case "true":
		info.ok = true
	case "false":
	default:
		s := resp.StatusCode
		info.ok = s == 408 || s == 409 || s == 429 || s >= 500
	}
	return info
}

// retryAfter reads retry-after-ms, then retry-after as seconds or an HTTP date.
func retryAfter(h http.Header) time.Duration {
	if ms, err := strconv.ParseFloat(strings.TrimSpace(h.Get("retry-after-ms")), 64); err == nil && ms > 0 {
		return time.Duration(ms * float64(time.Millisecond))
	}
	v := strings.TrimSpace(h.Get("retry-after"))
	if secs, err := strconv.ParseFloat(v, 64); err == nil && secs > 0 {
		return time.Duration(secs * float64(time.Second))
	}
	if t, err := http.ParseTime(v); err == nil {
		if d := time.Until(t); d > 0 {
			return d
		}
	}
	return 0
}

// readAPIError builds the error of a non-2xx answer from its JSON body. The
// body is read in part and only its error object is kept: an HTML error page
// from a proxy ends up as the status text, not as log noise.
func readAPIError(resp *http.Response) *APIError {
	e := &APIError{Status: resp.StatusCode, RequestID: resp.Header.Get("request-id")}
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
	var body struct {
		Error *struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(raw, &body) == nil && body.Error != nil {
		e.Type, e.Message = body.Error.Type, clip(body.Error.Message, maxErrorMessage)
	}
	if e.Message == "" {
		e.Message = http.StatusText(resp.StatusCode)
	}
	return e
}

func clip(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}

// watchdog cancels a request when nothing has happened for d.
type watchdog struct {
	mu sync.Mutex
	d  time.Duration
	t  *time.Timer
}

func newWatchdog(d time.Duration, onIdle func()) *watchdog {
	return &watchdog{d: d, t: time.AfterFunc(d, onIdle)}
}

func (w *watchdog) reset() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.t.Reset(w.d)
}

func (w *watchdog) stop() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.t.Stop()
}

// idleReader restarts the watchdog on every read that brings data.
type idleReader struct {
	r  io.Reader
	wd *watchdog
}

func (r *idleReader) Read(p []byte) (int, error) {
	n, err := r.r.Read(p)
	if n > 0 {
		r.wd.reset()
	}
	return n, err
}
