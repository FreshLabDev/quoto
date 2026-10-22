// SPDX-License-Identifier: Apache-2.0

package bot

import (
	"strconv"
	"strings"
	"testing"

	"github.com/FreshLabDev/quoto/internal/telegram"
)

// wired replaces the adapter fake with the real internal/telegram client over the
// same fake Bot API, which is how cmd/bot builds it.
func wired(t *testing.T, redact func(string) string) *harness {
	t.Helper()
	h := newHarness(t)
	h.bot.d.Adapter = telegram.New(h.bot.d.TG, telegram.Config{DeveloperIDs: []int64{4001}, Redact: redact, Now: h.clock.Now, Sleep: h.clock.Sleep}, nil)
	h.api.on("sendMessage", func(c apiCall, n int) reply {
		chat := strconv.FormatInt(int64(c.Body["chat_id"].(float64)), 10)
		return ok(`{"message_id":900,"date":1700000000,"chat":{"id":` + chat + `,"type":"supergroup"}}`)
	})
	return h
}

// The whole path for one /start in a forum topic, with the real adapter: the
// panel goes out silently, as HTML, without a link preview, into the topic.
func TestWiringGroupStartInATopic(t *testing.T) {
	h := wired(t, nil)
	h.api.setMember(ownerID, "administrator")
	m := startInGroup(person(ownerID, "ru"), 100, "/start")
	m.MessageThreadID, m.IsTopicMessage = 4242, true
	h.message(m)

	sends := h.api.named("sendMessage")
	if len(sends) != 1 {
		t.Fatalf("%d sendMessage calls", len(sends))
	}
	b := sends[0].Body
	if b["chat_id"].(float64) != float64(groupID) || b["message_thread_id"].(float64) != 4242 {
		t.Errorf("addressed to %v / %v", b["chat_id"], b["message_thread_id"])
	}
	if b["parse_mode"] != "HTML" || b["disable_notification"] != true {
		t.Errorf("parse_mode=%v disable_notification=%v", b["parse_mode"], b["disable_notification"])
	}
	if lp, _ := b["link_preview_options"].(map[string]any); lp["is_disabled"] != true {
		t.Errorf("link preview not disabled: %v", b["link_preview_options"])
	}
	if _, has := b["reply_to_message_id"]; has {
		t.Error("the panel is a reply")
	}
	if sends[0].keyboardData() == "" {
		t.Error("no keyboard")
	}
	// The panel is registered under the id Telegram returned, so Close finds it.
	if got, ok := h.bot.panels.take(msgRef{groupID, 900}, h.clock.Now()); !ok || got.message != 100 {
		t.Errorf("registry %+v %v", got, ok)
	}
}

func (c apiCall) keyboardData() string {
	_, data := c.keyboard()
	return strings.Join(data, ",")
}

// A failing handler reaches the developers as a plain-text message with the secret
// scrubbed, once per kind of update.
func TestWiringAlertForAFailedUpdate(t *testing.T) {
	h := wired(t, func(s string) string { return strings.ReplaceAll(s, "secret-token", "***") })
	h.store.errs["Ensure"] = errBoomWith("db password is secret-token")
	h.message(startInGroup(person(ownerID, "en"), 100, "/start"))
	h.message(startInGroup(person(ownerID, "en"), 101, "/start"))

	sends := h.api.named("sendMessage")
	if len(sends) != 1 {
		t.Fatalf("%d alerts, want one (deduplicated)", len(sends))
	}
	b := sends[0].Body
	if b["chat_id"].(float64) != 4001 {
		t.Errorf("sent to %v", b["chat_id"])
	}
	if _, has := b["parse_mode"]; has {
		t.Errorf("an alert with a parse mode: %v", b["parse_mode"])
	}
	text := b["text"].(string)
	if strings.Contains(text, "secret-token") || !strings.Contains(text, "***") || !strings.Contains(text, "message") {
		t.Errorf("alert text %q", text)
	}
}

// The quote card through the real adapter: silent HTML, in the private chat.
func TestWiringQuoteCard(t *testing.T) {
	h := wired(t, nil)
	h.store.quotes[42] = publishedQuote()
	h.startQuote(person(ownerID, "en"), h.signer.Payload(42))
	sends := h.api.named("sendMessage")
	if len(sends) != 1 || sends[0].str("text") != cardEN || sends[0].Body["disable_notification"] != true {
		t.Fatalf("sends %+v", sends)
	}
	if _, has := sends[0].Body["reply_markup"]; has {
		t.Fatal("the card has a keyboard")
	}
}
