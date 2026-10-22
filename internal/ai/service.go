// SPDX-License-Identifier: Apache-2.0

// Package ai scores a day's messages and describes media with Claude first and
// OpenRouter after it. It implements domain.Evaluator and domain.MediaDescriber.
//
// The calls and their order are the Python version's (see doc section 6). What
// differs is on purpose: one redactor for every error text, a typed parser, an
// audit log that reports its failures, and a media request that streams its file
// instead of holding it in memory several times.
package ai

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"io"
	"log/slog"
	mathrand "math/rand/v2"
	"strings"
	"sync"
	"time"

	"github.com/FreshLabDev/quoto/internal/audit"
	"github.com/FreshLabDev/quoto/internal/claude"
	"github.com/FreshLabDev/quoto/internal/domain"
	"github.com/FreshLabDev/quoto/internal/openrouter"
)

// Config is what ai takes from the environment; cmd/bot fills it from
// config.Config.
type Config struct {
	AnthropicKey         string
	AnthropicEvalModel   string // empty switches Claude off for the day evaluation
	AnthropicEvalEffort  string
	AnthropicMediaModel  string // empty switches Claude off for media
	AnthropicMediaEffort string

	OpenRouterKey           string
	OpenRouterEvalModel     string
	OpenRouterEvalFallback  string // may be empty or equal to the primary
	OpenRouterMediaModel    string
	OpenRouterMediaFallback string
	OpenRouterEvalEffort    string // empty sends no reasoning block
	OpenRouterMediaEffort   string

	BotUsername string // HTTP-Referer is https://t.me/<BotUsername>
	AuditDir    string // holds ai_audit.jsonl; empty switches the audit log off
}

const (
	providerAnthropic  = "anthropic"
	providerOpenRouter = "openrouter"
)

// Limits and timings. They are variables so tests can shorten them; production
// code never assigns them. Max-token ceilings and the OpenRouter timeouts are
// the Python ones (doc section 4.2).
var (
	claudeEvalMaxTokens  = 32000
	claudeMediaMaxTokens = 8000
	orEvalMaxTokens      = 32000

	// claudeAttempts are our own tries of a Claude evaluation. The client already
	// retries a call that fails before its stream starts; this covers an error
	// that arrives in the middle of a stream.
	claudeAttempts = 2
	orEvalAttempts = 3
	orEvalTimeout  = 60 * time.Second

	orMediaAttempts = 3
	orMediaTimeout  = 180 * time.Second

	// A run of failed evaluations opens the circuit for a while, so a struggling
	// provider is not hit again by every group.
	breakerThreshold = 5
	breakerCooldown  = 120 * time.Second

	// mediaCooldown is how long OpenRouter media waits after every model said 402.
	mediaCooldown = 900 * time.Second

	// claudeMediaMaxBytes is the API's limit for one image.
	claudeMediaMaxBytes int64 = 5 << 20
)

type claudeAPI interface {
	Message(ctx context.Context, req claude.Request) (*claude.Response, error)
}

type routerAPI interface {
	Do(ctx context.Context, body openrouter.Body, timeout time.Duration) (*openrouter.Response, error)
}

type auditAPI interface {
	Write(v any) error
}

// Service implements domain.Evaluator and domain.MediaDescriber.
type Service struct {
	cfg    Config
	redact func(string) string
	log    *slog.Logger

	claude claudeAPI // nil without an Anthropic key
	router routerAPI // nil without an OpenRouter key
	orURL  string    // for the audit log
	audit  auditAPI  // nil when AuditDir is empty

	now   func() time.Time
	sleep func(ctx context.Context, d time.Duration) error
	rand  func() float64 // [0, 1), for backoff jitter
	newID func() string  // request ids

	// One breaker and one media cooldown for the whole process, as before: a
	// provider that is down is down for every group.
	mu               sync.Mutex
	failures         int
	breakerUntil     time.Time
	mediaPausedUntil time.Time
}

var (
	_ domain.Evaluator      = (*Service)(nil)
	_ domain.MediaDescriber = (*Service)(nil)
)

// New builds the service. redact scrubs secrets from every error text that is
// returned, stored or logged; it is log.Redactor.Scrub in production. Message
// text in the audit log is not redacted: the log is operator-only and holds the
// chat as it was sent to the models, on purpose.
func New(cfg Config, redact func(string) string, log *slog.Logger) *Service {
	s := newService(cfg, redact, log)
	if cfg.AnthropicKey != "" {
		s.claude = claude.NewClient(cfg.AnthropicKey)
	}
	if cfg.OpenRouterKey != "" {
		c := openrouter.NewClient(cfg.OpenRouterKey, referer(cfg.BotUsername))
		s.router, s.orURL = c, c.URL
	}
	if cfg.AuditDir != "" {
		log := audit.New(cfg.AuditDir, s.log)
		log.Prune()
		s.audit = log
	} else {
		s.log.Warn("ai audit log is off: no audit directory is configured")
	}
	return s
}

// newService fills in everything but the clients and the audit sink.
func newService(cfg Config, redact func(string) string, log *slog.Logger) *Service {
	if redact == nil {
		redact = func(s string) string { return s }
	}
	if log == nil {
		log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return &Service{
		cfg:    cfg,
		redact: redact,
		log:    log,
		orURL:  openrouter.DefaultURL,
		now:    time.Now,
		sleep:  sleep,
		rand:   mathrand.Float64,
		newID:  newRequestID,
	}
}

// referer is the HTTP-Referer OpenRouter shows in its dashboard.
func referer(username string) string {
	username = strings.TrimPrefix(strings.TrimSpace(username), "@")
	if username == "" {
		return ""
	}
	return "https://t.me/" + username
}

// newRequestID is 32 hex characters, shared by every attempt of one call.
func newRequestID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("ai: no randomness: " + err.Error())
	}
	return hex.EncodeToString(b[:])
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

// backoff is the pause before try attempt+1: min(60, 4*2^attempt) seconds plus
// up to two seconds of jitter, so 4..6 s, then 8..10 s, then 16..18 s.
func (s *Service) backoff(attempt int) time.Duration {
	secs := 4.0
	for range attempt {
		secs *= 2
	}
	secs = min(secs, 60) + s.rand()*2
	return time.Duration(secs * float64(time.Second))
}

// writeAudit appends one attempt to the audit log. A failure is logged, not
// returned: the evaluation went on regardless, as it did in Python, but unlike
// Python the operator can see that the record is missing.
func (s *Service) writeAudit(record any) {
	if s.audit == nil {
		return
	}
	if err := s.audit.Write(record); err != nil {
		s.log.Error("ai audit record not written", "err", s.redact(err.Error()))
	}
}

// breakerOpen reports whether evaluations are being skipped.
func (s *Service) breakerOpen() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.now().Before(s.breakerUntil)
}

func (s *Service) breakerSuccess() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failures = 0
	s.breakerUntil = time.Time{}
}

// breakerFailure counts a call whose every candidate failed. The fifth in a row
// opens the circuit.
func (s *Service) breakerFailure() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failures++
	if s.failures >= breakerThreshold {
		s.breakerUntil = s.now().Add(breakerCooldown)
		s.failures = 0
		s.log.Warn("ai circuit breaker opened", "for", breakerCooldown.String())
	}
}
