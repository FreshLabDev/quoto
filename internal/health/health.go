// SPDX-License-Identifier: Apache-2.0

// Package health serves /healthz. It reports healthy only when every check
// passes: the database answers, Telegram polling is recent and the scheduler is
// ticking. Docker restarts a container that stays unhealthy.
package health

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/FreshLabDev/quoto/internal/buildinfo"
)

// Check is one named condition.
type Check struct {
	Name string
	Fn   func(ctx context.Context) error
}

// Handler answers 200 when every check passes and 503 otherwise, with the
// result of each check. Error text is the check's own and never carries
// secrets: checks return short fixed messages.
type Handler struct {
	checks []Check
}

// New returns the handler for the given checks.
func New(checks ...Check) *Handler { return &Handler{checks: checks} }

type response struct {
	OK      bool              `json:"ok"`
	Version string            `json:"version"`
	Commit  string            `json:"commit"`
	BuiltAt string            `json:"built_at"`
	Checks  map[string]string `json:"checks"`
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	res := response{
		OK:      true,
		Version: buildinfo.Version,
		Commit:  buildinfo.Commit,
		BuiltAt: buildinfo.Date,
		Checks:  make(map[string]string, len(h.checks)),
	}
	for _, c := range h.checks {
		if err := c.Fn(ctx); err != nil {
			res.OK = false
			res.Checks[c.Name] = err.Error()
			continue
		}
		res.Checks[c.Name] = "ok"
	}
	w.Header().Set("Content-Type", "application/json")
	if !res.OK {
		w.WriteHeader(http.StatusServiceUnavailable)
	}
	_ = json.NewEncoder(w).Encode(res)
}

// Fresh passes while last() is younger than maxAge. Before the first event
// (last() is zero) it passes for maxAge after startedAt, so a slow first poll
// does not fail the check that Docker runs right after the container starts.
func Fresh(name string, last func() time.Time, maxAge time.Duration, startedAt time.Time) Check {
	return Check{Name: name, Fn: func(context.Context) error {
		at := last()
		if at.IsZero() {
			at = startedAt
		}
		if age := time.Since(at); age > maxAge {
			return fmt.Errorf("stale: %s since the last event", age.Truncate(time.Second))
		}
		return nil
	}}
}

// Func adapts a plain error-returning function, such as a database ping.
func Func(name string, fn func(ctx context.Context) error) Check {
	return Check{Name: name, Fn: func(ctx context.Context) error {
		if err := fn(ctx); err != nil {
			if errors.Is(err, context.DeadlineExceeded) {
				return errors.New("timeout")
			}
			return errors.New("unavailable")
		}
		return nil
	}}
}
