// SPDX-License-Identifier: Apache-2.0

package claude

import (
	"errors"
	"fmt"
)

// APIError is an error the API reported: a non-2xx answer, or an `error` event
// in the middle of a stream (then Status is the 200 of the stream's own
// response and Type says what happened).
type APIError struct {
	Status    int
	Type      string // error.type from the body, e.g. overloaded_error; may be empty
	Message   string
	RequestID string
}

func (e *APIError) Error() string {
	s := fmt.Sprintf("anthropic: HTTP %d", e.Status)
	if e.Type != "" {
		s += " " + e.Type
	}
	if e.Message != "" {
		s += ": " + e.Message
	}
	return s
}

// ConnError is a failure of the connection itself: dial, TLS, a timeout, a
// read that broke, or a stream that ended before the message was complete.
type ConnError struct{ Err error }

func (e *ConnError) Error() string { return "anthropic: connection: " + e.Err.Error() }
func (e *ConnError) Unwrap() error { return e.Err }

// StreamError is an event stream that cannot be read: not JSON, or not the
// shape the parser needs. It is not retried; a second try would meet the same
// format.
type StreamError struct{ Err error }

func (e *StreamError) Error() string { return "anthropic: stream: " + e.Err.Error() }
func (e *StreamError) Unwrap() error { return e.Err }

// Errors a ConnError can carry.
var (
	// ErrIdle means nothing arrived for IdleTimeout.
	ErrIdle = errors.New("no data from the API within the idle timeout")
	// ErrTruncated means the stream ended before message_stop.
	ErrTruncated = errors.New("stream ended before the message was complete")
)

// retryableErrorTypes are the error.type values worth another try. An error
// event in a stream arrives on a 200 response, so the type, not the status,
// says whether it is worth it.
var retryableErrorTypes = map[string]bool{
	"api_error": true, "overloaded_error": true, "rate_limit_error": true, "timeout_error": true,
}

// IsRetryable reports whether another call may succeed: a broken connection, a
// 429 or 5xx, or one of the transient error types. A 402 (empty balance), a
// 400 or a refusal is not.
func IsRetryable(err error) bool {
	var conn *ConnError
	if errors.As(err, &conn) {
		return true
	}
	var api *APIError
	if errors.As(err, &api) {
		return api.Status == 429 || (api.Status >= 500 && api.Status < 600) || retryableErrorTypes[api.Type]
	}
	return false
}
