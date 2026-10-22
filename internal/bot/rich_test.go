// SPDX-License-Identifier: Apache-2.0

package bot

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/FreshLabDev/tg"

	"github.com/FreshLabDev/quoto/internal/agreement"
)

func selfHosted(c *Config) { c.SelfHosted = true }

// The probe at startup (richmd.py preflight, test_richmd.py:41-65 RMD-01..04). The
// verdicts are Telegram's two answers about the method; everything else says
// nothing about it and leaves the bot on HTML.
func TestProbeRich(t *testing.T) {
	tests := []struct {
		name      string
		hosted    bool
		reply     *reply
		state     int32
		alertKeys []string
	}{
		{"a cloud server has no rich Markdown and is not asked", false, nil, richOff, nil},
		{"RMD-01: a 400 on the empty probe means the method exists", true, &reply{400, `{"ok":false,"error_code":400,"description":"Bad Request: rich_message is empty"}`}, richOn, nil},
		{"the method answers an empty body", true, &reply{200, `{"ok":true,"result":true}`}, richOn, nil},
		{"RMD-02: method not found", true, &reply{404, `{"ok":false,"error_code":404,"description":"Not Found: method not found"}`}, richOff, []string{"richmd:unsupported"}},
		{"a 404 from something in front of the server", true, &reply{404, `{"ok":false,"error_code":404,"description":"Not Found"}`}, richUnknown, nil},
		{"RMD-03: unauthorized", true, &reply{401, `{"ok":false,"error_code":401,"description":"Unauthorized"}`}, richUnknown, nil},
		{"RMD-03: forbidden", true, &reply{403, `{"ok":false,"error_code":403,"description":"Forbidden: bot was blocked"}`}, richUnknown, nil},
		{"a conflict", true, &reply{409, `{"ok":false,"error_code":409,"description":"Conflict: terminated by other getUpdates request"}`}, richUnknown, nil},
		{"a flood limit", true, &reply{429, `{"ok":false,"error_code":429,"description":"Too Many Requests"}`}, richUnknown, nil},
		{"a server error", true, &reply{502, `{"ok":false,"error_code":502,"description":"Bad Gateway"}`}, richUnknown, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t, func(c *Config) { c.SelfHosted = tt.hosted })
			if tt.reply != nil {
				h.api.answerWith("sendRichMessage", *tt.reply)
			}
			h.bot.probeRich(context.Background())

			if got := h.bot.rich.v.Load(); got != tt.state {
				t.Fatalf("state %d, want %d", got, tt.state)
			}
			if got := h.bot.rich.available(); got != (tt.state == richOn) {
				t.Fatalf("available = %v", got)
			}
			keys := h.adapter.alertKeys()
			if strings.Join(keys, ",") != strings.Join(tt.alertKeys, ",") {
				t.Fatalf("alerts %v, want %v", keys, tt.alertKeys)
			}
			calls := h.api.named("sendRichMessage")
			if !tt.hosted && len(calls) != 0 {
				t.Fatal("a cloud server was probed")
			}
			if tt.hosted && len(calls) == 0 {
				t.Fatal("the server was not probed")
			}
		})
	}
}

// RMD-04 (test_richmd.py:65): a server that cannot be reached is not an answer
// about the method: HTML for now, and a developer is told. The probe retries a
// dropped connection itself, with a wait of up to two seconds, so the test is
// skipped in -short runs.
func TestProbeRichUnreachable(t *testing.T) {
	if testing.Short() {
		t.Skip("the probe waits between its own retries")
	}
	h := newHarness(t, selfHosted)
	h.api.answerWith("sendRichMessage", reply{status: -1})
	h.bot.probeRich(context.Background())
	if h.bot.rich.v.Load() != richUnknown {
		t.Fatalf("state %d", h.bot.rich.v.Load())
	}
	if keys := h.adapter.alertKeys(); len(keys) != 1 || keys[0] != "richmd:preflight-failed" {
		t.Fatalf("alerts %v", keys)
	}
}

// Shutting down during the probe says nothing and alerts nobody.
func TestProbeRichCanceled(t *testing.T) {
	h := newHarness(t, selfHosted)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	h.bot.probeRich(ctx)
	if h.bot.rich.v.Load() != richUnknown || len(h.adapter.alertKeys()) != 0 {
		t.Fatalf("state %d alerts %v", h.bot.rich.v.Load(), h.adapter.alertKeys())
	}
}

func testDocument() agreement.Document {
	return agreement.Build("en", agreement.Options{CanAccept: true, Ref: agreement.Ref{Scope: "g", Owner: ownerID}})
}

// RMD-05 (test_richmd.py:82): the rich edit is editMessageText with a
// rich_message and nothing else for the text; entity detection stays on.
func TestEditDocumentRich(t *testing.T) {
	h := newHarness(t, selfHosted)
	h.bot.rich.set(richOn)
	doc := testDocument()
	if err := h.bot.editDocument(context.Background(), groupID, 50, doc); err != nil {
		t.Fatal(err)
	}
	calls := h.api.named("editMessageText")
	if len(calls) != 1 {
		t.Fatalf("%d edits", len(calls))
	}
	body := calls[0].Body
	rich, _ := body["rich_message"].(map[string]any)
	if rich["markdown"] != doc.Markdown {
		t.Fatalf("rich_message %v", rich)
	}
	if _, has := body["text"]; has {
		t.Fatal("text next to the rich message")
	}
	if body["chat_id"].(float64) != float64(groupID) || body["message_id"].(float64) != 50 {
		t.Fatalf("addressed to %v", body)
	}
	if len(edit{calls[0]}.data()) == 0 {
		t.Fatal("the keyboard was dropped")
	}
	// Nothing else in rich_message: entity detection is on unless a key turns it off.
	if len(rich) != 1 {
		t.Fatalf("rich_message %v has more than the markdown", rich)
	}
}

// richmd.py edit path, rich unavailable (RMD-05's counterpart): the HTML rendering is sent as
// ordinary editMessageText with parse_mode HTML, whether rich is off or only unknown.
func TestEditDocumentHTMLWhenRichIsOff(t *testing.T) {
	for _, state := range []int32{richOff, richUnknown} {
		h := newHarness(t, selfHosted)
		h.bot.rich.set(state)
		doc := testDocument()
		if err := h.bot.editDocument(context.Background(), groupID, 50, doc); err != nil {
			t.Fatal(err)
		}
		calls := h.api.named("editMessageText")
		if len(calls) != 1 || calls[0].str("text") != doc.HTML || calls[0].str("parse_mode") != "HTML" {
			t.Fatalf("state %d: %+v", state, calls)
		}
		if _, has := calls[0].Body["rich_message"]; has {
			t.Fatalf("state %d: rich message sent", state)
		}
	}
}

// "Message is not modified" means the panel already shows it, in both paths.
func TestEditDocumentNotModified(t *testing.T) {
	notModified := fail(400, "Bad Request: message is not modified: specified new message content and reply markup are exactly the same")
	for _, rich := range []bool{true, false} {
		h := newHarness(t, selfHosted)
		if rich {
			h.bot.rich.set(richOn)
		}
		h.api.answerWith("editMessageText", notModified)
		if err := h.bot.editDocument(context.Background(), groupID, 50, testDocument()); err != nil {
			t.Fatalf("rich=%v: %v", rich, err)
		}
		if n := len(h.api.named("editMessageText")); n != 1 {
			t.Fatalf("rich=%v: %d edits, no fallback is needed", rich, n)
		}
	}
}

// RMD-06 (test_richmd.py:93): when the method disappears at runtime the bot drops
// to HTML for good, tells a developer once, and edits this very message as HTML.
func TestEditDocumentMethodLost(t *testing.T) {
	h := newHarness(t, selfHosted)
	h.bot.rich.set(richOn)
	h.api.on("editMessageText", func(c apiCall, n int) reply {
		if _, rich := c.Body["rich_message"]; rich {
			return fail(404, "Not Found: method not found")
		}
		return ok("true")
	})
	doc := testDocument()
	if err := h.bot.editDocument(context.Background(), groupID, 50, doc); err != nil {
		t.Fatal(err)
	}
	calls := h.api.named("editMessageText")
	if len(calls) != 2 || calls[1].str("text") != doc.HTML {
		t.Fatalf("calls %+v", calls)
	}
	if h.bot.rich.v.Load() != richOff {
		t.Fatalf("state %d", h.bot.rich.v.Load())
	}
	if keys := h.adapter.alertKeys(); len(keys) != 1 || keys[0] != "richmd:lost" {
		t.Fatalf("alerts %v", keys)
	}

	// Degraded once: the next edit goes straight to HTML and says nothing more.
	if err := h.bot.editDocument(context.Background(), groupID, 50, doc); err != nil {
		t.Fatal(err)
	}
	if got := len(h.api.named("editMessageText")); got != 3 {
		t.Fatalf("%d edits, want 3 (rich is not tried again)", got)
	}
	if keys := h.adapter.alertKeys(); len(keys) != 1 {
		t.Fatalf("alerts %v", keys)
	}
}

// A 400 on a rich edit (a deleted panel, a message that cannot be edited) says
// nothing about the server: this edit falls back to HTML, the next one tries rich
// again, nobody is alerted.
func TestEditDocumentRefusedFallsBackOnce(t *testing.T) {
	h := newHarness(t, selfHosted)
	h.bot.rich.set(richOn)
	h.api.on("editMessageText", func(c apiCall, n int) reply {
		if _, rich := c.Body["rich_message"]; rich {
			return fail(400, "Bad Request: can't parse rich message")
		}
		return ok("true")
	})
	if err := h.bot.editDocument(context.Background(), groupID, 50, testDocument()); err != nil {
		t.Fatal(err)
	}
	if h.bot.rich.v.Load() != richOn {
		t.Fatal("one refused edit turned rich off")
	}
	if n := len(h.api.named("editMessageText")); n != 2 {
		t.Fatalf("%d edits, want rich then HTML", n)
	}
	if len(h.adapter.alertKeys()) != 0 {
		t.Fatalf("alerts %v", h.adapter.alertKeys())
	}
}

// A flood limit or a server error would meet the HTML edit too: the error is
// returned and nothing else is sent.
func TestEditDocumentTransientErrors(t *testing.T) {
	for name, rep := range map[string]reply{
		"flood limit":  fail(429, "Too Many Requests: retry later"),
		"server error": fail(500, "Internal Server Error"),
	} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t, selfHosted)
			h.bot.rich.set(richOn)
			h.api.answerWith("editMessageText", rep)
			err := h.bot.editDocument(context.Background(), groupID, 50, testDocument())
			if err == nil {
				t.Fatal("no error")
			}
			if n := len(h.api.named("editMessageText")); n != 1 {
				t.Fatalf("%d edits, want 1", n)
			}
			if h.bot.rich.v.Load() != richOn {
				t.Fatal("rich was switched off by a transient error")
			}
			var api *tg.APIError
			if !errors.As(err, &api) {
				t.Fatalf("error %v is not an API error", err)
			}
		})
	}
}

// Doc 7.2 F4: only a 400 or a 404 is a refusal of one edit; a flood limit, a server error and a
// network error are not.
func TestRejected(t *testing.T) {
	tests := []struct {
		err  error
		want bool
	}{
		{&tg.APIError{ErrorCode: 400}, true},
		{&tg.APIError{ErrorCode: 404}, true},
		{&tg.APIError{StatusCode: 400}, true},
		{&tg.APIError{ErrorCode: 429}, false},
		{&tg.APIError{ErrorCode: 500}, false},
		{errors.New("eof"), false},
		{nil, false},
	}
	for _, tt := range tests {
		if got := rejected(tt.err); got != tt.want {
			t.Errorf("rejected(%v) = %v, want %v", tt.err, got, tt.want)
		}
	}
}
