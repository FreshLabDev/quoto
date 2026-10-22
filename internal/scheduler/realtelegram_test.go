// SPDX-License-Identifier: Apache-2.0

package scheduler

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/FreshLabDev/tg"

	"github.com/FreshLabDev/quoto/internal/domain"
	"github.com/FreshLabDev/quoto/internal/telegram"
)

// The scheduler tests classify a failed send with fakeSender.OutcomeUnknown,
// which looks for a sentinel error. These tests put the real telegram.Client
// (and the real tg client) under the scheduler, over an HTTP server that answers
// like the Bot API, so the status written for a failure comes from what the
// network really returned.

type botReply struct {
	status int
	body   string
}

func apiError(code int, desc string) botReply {
	return botReply{code, `{"ok":false,"error_code":` + itoa(code) + `,"description":"` + desc + `"}`}
}

func itoa(n int) string { return strconv.Itoa(n) }

var sentOK = botReply{200, `{"ok":true,"result":{"message_id":900,"date":1,"chat":{"id":-1001234567890,"type":"supergroup"}}}`}
var copiedOK = botReply{200, `{"ok":true,"result":{"message_id":901}}`}
var trueOK = botReply{200, `{"ok":true,"result":true}`}

type botAPI struct {
	mu     sync.Mutex
	script map[string][]botReply
	seen   map[string]int
}

func (a *botAPI) count(method string) int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.seen[method]
}

func realTelegramHarness(t *testing.T, script map[string][]botReply) (*harness, *botAPI) {
	t.Helper()
	api := &botAPI{script: script, seen: map[string]int{}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
		_, _ = io.Copy(io.Discard, r.Body)
		api.mu.Lock()
		n := api.seen[method]
		api.seen[method]++
		answers := api.script[method]
		rep := trueOK
		if len(answers) > 0 {
			rep = answers[min(n, len(answers)-1)]
		}
		api.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(rep.status)
		_, _ = io.WriteString(w, rep.body)
	}))
	t.Cleanup(srv.Close)

	h := newHarness(t)
	client := tg.New("123456:TESTTOKEN", tg.WithAPIBase(srv.URL))
	h.sched.d.Sender = telegram.New(client, telegram.Config{
		Sleep: func(context.Context, time.Duration) error { return nil },
	}, nil)
	h.addGroup(chatA)
	h.fill(chatA, 10)
	return h, api
}

func photoDay(h *harness) {
	h.setMessage(chatA, 10, func(m *domain.WindowMessage) {
		m.ContentType, m.MessageID = domain.ContentPhoto, 57
		m.Media = &domain.MediaSnapshot{Description: "кот"}
	})
}

func TestRealTelegramMediaCopyServerErrorIsUnknownAndNotRetried(t *testing.T) {
	h, api := realTelegramHarness(t, map[string][]botReply{"copyMessage": {apiError(502, "Bad Gateway")}})
	photoDay(h)
	h.run()
	if got := h.status(chatA); got != domain.StatusPublishUnknown {
		t.Fatalf("status %s", got)
	}
	if api.count("copyMessage") != 1 || api.count("sendMessage") != 0 {
		t.Errorf("copyMessage x%d, sendMessage x%d: a text fallback after a 502 may double the post", api.count("copyMessage"), api.count("sendMessage"))
	}
	h.clock.Advance(20 * time.Minute)
	h.run()
	if api.count("copyMessage") != 1 || api.count("sendMessage") != 0 {
		t.Errorf("an unknown outcome was retried: copyMessage x%d, sendMessage x%d", api.count("copyMessage"), api.count("sendMessage"))
	}
}

func TestRealTelegramMediaCopyRefusedFallsBackToText(t *testing.T) {
	h, api := realTelegramHarness(t, map[string][]botReply{
		"copyMessage": {apiError(400, "Bad Request: message can't be copied")},
		"sendMessage": {sentOK},
	})
	photoDay(h)
	h.run()
	q := h.store.quote(t, chatA, day27)
	if q.DecisionStatus != domain.StatusPublished || !strings.HasPrefix(q.OperationError, "Media copy failed, text fallback used: ") {
		t.Errorf("quote %s / %q", q.DecisionStatus, q.OperationError)
	}
	if api.count("sendMessage") != 1 || q.BotMessageID != 900 {
		t.Errorf("sendMessage x%d, bot message %d", api.count("sendMessage"), q.BotMessageID)
	}
}

func TestRealTelegramSendServerErrorIsUnknown(t *testing.T) {
	h, api := realTelegramHarness(t, map[string][]botReply{"sendMessage": {apiError(500, "Internal Server Error"), sentOK}})
	h.run()
	if got := h.status(chatA); got != domain.StatusPublishUnknown {
		t.Fatalf("status %s", got)
	}
	h.clock.Advance(20 * time.Minute)
	h.run()
	if api.count("sendMessage") != 1 {
		t.Errorf("sendMessage x%d: an unknown outcome was sent again", api.count("sendMessage"))
	}
}

func TestRealTelegramSendRefusedIsFailedAndRetriedAfterFifteenMinutes(t *testing.T) {
	h, api := realTelegramHarness(t, map[string][]botReply{"sendMessage": {apiError(403, "Forbidden: bot is not a member of the supergroup chat"), sentOK}})
	h.run()
	if got := h.status(chatA); got != domain.StatusPublishFailed {
		t.Fatalf("status %s", got)
	}
	h.clock.Advance(10 * time.Minute)
	h.run()
	if api.count("sendMessage") != 1 {
		t.Errorf("retried after 10 minutes: sendMessage x%d", api.count("sendMessage"))
	}
	h.clock.Advance(6 * time.Minute)
	h.run()
	if api.count("sendMessage") != 2 || h.status(chatA) != domain.StatusPublished {
		t.Errorf("after 16 minutes: sendMessage x%d, status %s", api.count("sendMessage"), h.status(chatA))
	}
}

func TestRealTelegramPinRefusedKeepsTheQuotePublished(t *testing.T) {
	h, _ := realTelegramHarness(t, map[string][]botReply{
		"sendMessage":    {sentOK},
		"pinChatMessage": {apiError(400, "Bad Request: not enough rights to pin a message")},
	})
	h.run()
	q := h.store.quote(t, chatA, day27)
	if q.DecisionStatus != domain.StatusPublished || !strings.HasPrefix(q.OperationError, "Pin failed: ") {
		t.Errorf("quote %s / %q", q.DecisionStatus, q.OperationError)
	}
	if h.store.clearedCount(chatA) != 1 {
		t.Errorf("the window was cleared %d times", h.store.clearedCount(chatA))
	}
}
