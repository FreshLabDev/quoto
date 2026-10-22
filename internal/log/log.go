// SPDX-License-Identifier: Apache-2.0

// Package log builds the process logger: JSON lines on stdout, with every
// secret and every token-shaped string removed before it is written. One
// redactor knows all secrets; the Python version had two, and each missed one
// of the keys the other knew.
package log

import (
	"context"
	"io"
	"log/slog"
	"regexp"
	"sort"
	"strings"
)

const placeholder = "[redacted]"

var (
	// Anthropic keys, bearer headers and Telegram bot tokens have a shape of
	// their own, so they are caught even when the configured value was not
	// handed to the redactor (a key pasted into a message, say).
	anthropicKey = regexp.MustCompile(`sk-ant-[A-Za-z0-9_\-]+`)
	openRouter   = regexp.MustCompile(`sk-or-[A-Za-z0-9_\-]+`)
	bearer       = regexp.MustCompile(`(?i)bearer\s+[A-Za-z0-9._\-]+`)
	botToken     = regexp.MustCompile(`\d{6,}:[A-Za-z0-9_\-]{30,}`)
	// A connection URL carries its password; the host after the @ stays.
	dbURL = regexp.MustCompile(`(postgres(?:ql)?://[^:@/\s]*):[^@\s]+@`)
)

// Redactor removes secrets from strings. The zero value removes only
// token-shaped strings.
type Redactor struct {
	secrets []string
}

// NewRedactor returns a Redactor for the given secret values. Empty and very
// short values are ignored: replacing "a" everywhere would only destroy logs.
func NewRedactor(secrets []string) *Redactor {
	r := &Redactor{}
	seen := map[string]bool{}
	for _, s := range secrets {
		s = strings.TrimSpace(s)
		if len(s) < 4 || seen[s] {
			continue
		}
		seen[s] = true
		r.secrets = append(r.secrets, s)
	}
	// Longest first, so a secret that contains another is removed whole.
	sort.Slice(r.secrets, func(i, j int) bool { return len(r.secrets[i]) > len(r.secrets[j]) })
	return r
}

// Scrub returns s with every known secret and token-shaped string replaced.
func (r *Redactor) Scrub(s string) string {
	if s == "" {
		return s
	}
	if r != nil {
		for _, secret := range r.secrets {
			s = strings.ReplaceAll(s, secret, placeholder)
		}
	}
	s = anthropicKey.ReplaceAllString(s, placeholder)
	s = openRouter.ReplaceAllString(s, placeholder)
	s = bearer.ReplaceAllString(s, "Bearer "+placeholder)
	s = dbURL.ReplaceAllString(s, "${1}:"+placeholder+"@")
	return botToken.ReplaceAllString(s, placeholder)
}

// New returns a JSON logger on w that scrubs every record through r.
func New(w io.Writer, level slog.Level, r *Redactor) *slog.Logger {
	inner := slog.NewJSONHandler(w, &slog.HandlerOptions{Level: level})
	return slog.New(&handler{inner: inner, r: r})
}

type handler struct {
	inner slog.Handler
	r     *Redactor
}

func (h *handler) Enabled(ctx context.Context, l slog.Level) bool { return h.inner.Enabled(ctx, l) }

func (h *handler) Handle(ctx context.Context, rec slog.Record) error {
	clean := slog.NewRecord(rec.Time, rec.Level, h.r.Scrub(rec.Message), rec.PC)
	rec.Attrs(func(a slog.Attr) bool {
		clean.AddAttrs(h.attr(a))
		return true
	})
	return h.inner.Handle(ctx, clean)
}

func (h *handler) WithAttrs(attrs []slog.Attr) slog.Handler {
	cleaned := make([]slog.Attr, len(attrs))
	for i, a := range attrs {
		cleaned[i] = h.attr(a)
	}
	return &handler{inner: h.inner.WithAttrs(cleaned), r: h.r}
}

func (h *handler) WithGroup(name string) slog.Handler {
	return &handler{inner: h.inner.WithGroup(name), r: h.r}
}

func (h *handler) attr(a slog.Attr) slog.Attr {
	v := a.Value.Resolve()
	switch v.Kind() {
	case slog.KindString:
		return slog.String(a.Key, h.r.Scrub(v.String()))
	case slog.KindGroup:
		group := v.Group()
		cleaned := make([]slog.Attr, len(group))
		for i, g := range group {
			cleaned[i] = h.attr(g)
		}
		return slog.Attr{Key: a.Key, Value: slog.GroupValue(cleaned...)}
	case slog.KindAny:
		// Errors and Stringers can carry a URL with a token in it.
		if err, ok := v.Any().(error); ok {
			return slog.String(a.Key, h.r.Scrub(err.Error()))
		}
		if s, ok := v.Any().(interface{ String() string }); ok {
			return slog.String(a.Key, h.r.Scrub(s.String()))
		}
	}
	return slog.Attr{Key: a.Key, Value: v}
}
