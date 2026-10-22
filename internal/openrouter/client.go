// SPDX-License-Identifier: Apache-2.0

package openrouter

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	// maxResponse bounds a successful answer. A chat completion is kilobytes.
	maxResponse = 16 << 20
	// snippetLimit bounds what is kept of an error answer.
	snippetLimit = 2048
	// maxRetryAfter is the longest Retry-After honoured.
	maxRetryAfter = 60 * time.Second
)

// Client posts chat completions. The zero value is not usable; use NewClient.
type Client struct {
	Key string
	URL string
	// Referer is the HTTP-Referer header (the bot's t.me link); omitted when empty.
	Referer string
	// HTTP has no overall timeout: Do applies the timeout it is given.
	HTTP *http.Client
}

// NewClient returns a client for key.
func NewClient(key, referer string) *Client {
	return &Client{
		Key:     key,
		URL:     DefaultURL,
		Referer: referer,
		HTTP: &http.Client{Transport: &http.Transport{
			Proxy:                 http.ProxyFromEnvironment,
			DialContext:           (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
			ForceAttemptHTTP2:     true,
			MaxIdleConns:          16,
			MaxIdleConnsPerHost:   4,
			IdleConnTimeout:       90 * time.Second,
			TLSHandshakeTimeout:   10 * time.Second,
			ExpectContinueTimeout: time.Second,
		}, CheckRedirect: noRedirect},
	}
}

// noRedirect refuses to follow a redirect: the request body is the day's chat
// text, and the API has no legitimate redirects.
func noRedirect(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

// Response is a successful (2xx) answer.
type Response struct {
	Status int
	Body   []byte
}

// StatusError is a non-2xx answer. Snippet is the start of its body, kept for
// the audit log; it is empty for a 404, whose body is neither read nor logged.
type StatusError struct {
	Status     int
	RetryAfter time.Duration // from the Retry-After header; 0 when absent or unusable
	Snippet    string
}

func (e *StatusError) Error() string { return fmt.Sprintf("openrouter: HTTP %d", e.Status) }

// RequestError is a failure to get an answer: dial, TLS, timeout, a connection
// that broke while the answer was read.
type RequestError struct{ Err error }

func (e *RequestError) Error() string { return "openrouter: request: " + e.Err.Error() }
func (e *RequestError) Unwrap() error { return e.Err }

// BodyError is a request body that could not be produced: the media file is
// gone, or it changed while it was sent. Retrying cannot help.
type BodyError struct{ Err error }

func (e *BodyError) Error() string { return e.Err.Error() }
func (e *BodyError) Unwrap() error { return e.Err }

// RetryableStatus reports whether another try may succeed: 429 or any 5xx.
// A 402 (empty balance) or any other 4xx will not change by itself.
func RetryableStatus(code int) bool { return code == 429 || (code >= 500 && code < 600) }

// Do makes one attempt. A non-2xx answer is a *StatusError, a failure to get
// an answer a *RequestError, and a cancelled ctx its own error. The attempt as a
// whole, upload and answer, ends after timeout.
func (c *Client) Do(ctx context.Context, body Body, timeout time.Duration) (*Response, error) {
	rc, length, err := body.Open()
	if err != nil {
		return nil, &BodyError{err}
	}
	defer rc.Close()

	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodPost, c.URL, rc)
	if err != nil {
		return nil, fmt.Errorf("openrouter: build request: %w", err)
	}
	// Known length, so the request is not sent chunked.
	req.ContentLength = length
	req.Header.Set("Authorization", "Bearer "+c.Key)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "quoto")
	req.Header.Set("X-OpenRouter-Title", Title)
	if c.Referer != "" {
		req.Header.Set("HTTP-Referer", c.Referer)
	}

	hc := c.HTTP
	if hc == nil {
		hc = http.DefaultClient
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, wrap(ctx, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		e := &StatusError{Status: resp.StatusCode, RetryAfter: RetryAfter(resp.Header)}
		// The body of a 404 is not read at all. The rest are read in part: enough
		// to tell what the provider objected to, not enough to fill a log.
		if resp.StatusCode != http.StatusNotFound {
			raw, _ := io.ReadAll(io.LimitReader(resp.Body, snippetLimit))
			e.Snippet = toValidUTF8(raw)
		}
		return nil, e
	}

	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponse+1))
	if err != nil {
		return nil, wrap(ctx, err)
	}
	if len(raw) > maxResponse {
		return nil, &RequestError{fmt.Errorf("answer longer than %d bytes", maxResponse)}
	}
	return &Response{Status: resp.StatusCode, Body: raw}, nil
}

// wrap turns a transport error into a RequestError, except when the caller
// itself cancelled: then it is the caller's error to see.
func wrap(parent context.Context, err error) error {
	if parent.Err() != nil {
		return parent.Err()
	}
	var body *BodyError
	if errors.As(err, &body) {
		return body
	}
	return &RequestError{err}
}

// RetryAfter reads a Retry-After header in seconds, at most a minute. A date,
// zero, a negative number or garbage is 0, and the caller backs off by itself.
func RetryAfter(h http.Header) time.Duration {
	secs, err := strconv.ParseFloat(strings.TrimSpace(h.Get("Retry-After")), 64)
	if err != nil || secs <= 0 {
		return 0
	}
	return min(time.Duration(secs*float64(time.Second)), maxRetryAfter)
}
