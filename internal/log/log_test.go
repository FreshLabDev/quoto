// SPDX-License-Identifier: Apache-2.0

package log

import (
	"bytes"
	"errors"
	"log/slog"
	"strings"
	"testing"
)

func TestScrubKnownAndShapedSecrets(t *testing.T) {
	r := NewRedactor([]string{"hunter2-db-password", "", "ab"})
	cases := map[string]string{
		"connect postgres://u:hunter2-db-password@h/db":                      "connect postgres://u:[redacted]@h/db",
		"key sk-ant-api03-AAAA_bbb-CCC failed":                               "key [redacted] failed",
		"Authorization: Bearer eyJhbGciOi.payload.sig":                       "Authorization: Bearer [redacted]",
		"url /bot123456789:AAHdqTcvCH1vGWJxfSeofSAs0K5PALDsaw0/getMe failed": "url /bot[redacted]/getMe failed",
		"ab stays because short secrets are ignored":                         "ab stays because short secrets are ignored",
		"sk-or-v1-0123456789abcdef is an OpenRouter key":                     "[redacted] is an OpenRouter key",
	}
	for in, want := range cases {
		if got := r.Scrub(in); got != want {
			t.Errorf("Scrub(%q)\n got %q\nwant %q", in, got, want)
		}
	}
}

func TestLongestSecretWins(t *testing.T) {
	r := NewRedactor([]string{"secret", "secret-with-suffix"})
	if got := r.Scrub("x secret-with-suffix y"); got != "x [redacted] y" {
		t.Errorf("got %q", got)
	}
}

func TestLoggerScrubsMessageAttrsErrorsAndGroups(t *testing.T) {
	var buf bytes.Buffer
	r := NewRedactor([]string{"s3cr3t-token-value"})
	log := New(&buf, slog.LevelInfo, r).With("pre", "s3cr3t-token-value")
	log.Info("hello s3cr3t-token-value",
		"err", errors.New("dial https://api/bot s3cr3t-token-value"),
		slog.Group("g", slog.String("k", "v s3cr3t-token-value")),
		"n", 7)
	out := buf.String()
	if strings.Contains(out, "s3cr3t-token-value") {
		t.Fatalf("secret leaked: %s", out)
	}
	for _, want := range []string{`"n":7`, `"msg":"hello [redacted]"`, `"pre":"[redacted]"`} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %s in %s", want, out)
		}
	}
}

func TestLevelFilter(t *testing.T) {
	var buf bytes.Buffer
	log := New(&buf, slog.LevelWarn, nil)
	log.Info("quiet")
	if buf.Len() != 0 {
		t.Errorf("info logged at warn level: %s", buf.String())
	}
	log.Warn("loud")
	if !strings.Contains(buf.String(), "loud") {
		t.Errorf("warn lost: %s", buf.String())
	}
}

// Secrets that travel percent-encoded, and whole connection URLs, are removed
// as well (review SEC-6).
func TestScrubEncodedFormsAndDatabaseURLs(t *testing.T) {
	r := NewRedactor([]string{"p@ss/word:1", "p%40ss%2Fword%3A1"})
	for _, in := range []string{
		"connect postgres://quoto_core:p@ss/word:1@core-postgres:5432/core failed",
		"connect postgres://quoto_core:p%40ss%2Fword%3A1@core-postgres:5432/core failed",
		"connect postgresql://quoto_core:anything-at-all@core-postgres/core failed",
	} {
		out := r.Scrub(in)
		if strings.Contains(out, "word") || strings.Contains(out, "anything-at-all") || strings.Contains(out, "p@ss") {
			t.Errorf("password survived: %q", out)
		}
		if !strings.Contains(out, "core-postgres") {
			t.Errorf("the host should stay: %q", out)
		}
	}
}
