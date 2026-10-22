// SPDX-License-Identifier: Apache-2.0

package bot

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/FreshLabDev/tg"

	"github.com/FreshLabDev/quoto/internal/domain"
	"github.com/FreshLabDev/quoto/internal/i18n"
)

// A store that fails in the middle of a press: the person is still answered
// (once), nothing is redrawn, and the developers are told, once per kind of
// update (doc 40-bot.md, errors in handlers).
func TestPressWithAFailingStore(t *testing.T) {
	tests := []struct {
		name  string
		data  string
		op    string
		panel func() tg.Message
	}{
		{"schedule: time", "menu:777:g:timeadj:15", "AdjustQuoteTime", panelMessage},
		{"schedule: minimum", "menu:777:g:minadj:1", "AdjustMinMessages", panelMessage},
		{"time zone", "menu:777:g:settz:UTC", "SetTimezone", panelMessage},
		{"switch", "menu:777:g:toggle:pin", "Toggle", panelMessage},
		{"group language: automatic", "menu:777:g:autolang", "ClearLanguage", panelMessage},
		{"private language: automatic", "menu:777:p:autoplang", "ClearLanguage", privatePanel},
		{"person's statistics", "menu:777:g:userstats", "User", panelMessage},
		{"agreement tab", "agree:v:ru:g:777", "Ensure", panelMessage},
		{"acceptance: group", "agree:a:ru:g:777", "Ensure", panelMessage},
		{"menu: group", "menu:777:g:home", "Ensure", panelMessage},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t)
			ruGroup(h)
			h.store.errs[tt.op] = errBoom()
			h.press(owner("ru"), tt.panel(), tt.data)
			if len(h.answers()) != 1 {
				t.Fatalf("%d answers, want one", len(h.answers()))
			}
			h.requireNoEdit()
			if keys := h.adapter.alertKeys(); len(keys) != 1 || keys[0] != "update:callback_query" {
				t.Fatalf("alerts %v", keys)
			}
		})
	}
}

// The group is read again after a change so the screen speaks its new state; a
// failing read is an error like any other.
func TestPressWhenTheGroupVanishes(t *testing.T) {
	h := newHarness(t)
	ruGroup(h)
	h.store.getMissing = true
	h.press(owner("ru"), panelMessage(), "menu:777:g:about")
	if len(h.answers()) != 1 || len(h.adapter.alertKeys()) != 1 {
		t.Fatalf("answers %d alerts %v", len(h.answers()), h.adapter.alertKeys())
	}
	if text := h.adapter.alerts[0].Text; !strings.Contains(text, "no settings row") {
		t.Fatalf("alert %q", text)
	}
	h.requireNoEdit()
}

// Every screen of the group panel, reached by its button, draws into the panel.
func TestMenuScreens(t *testing.T) {
	for _, action := range []string{"home", "lang", "sched", "tz", "behavior", "about", "doc", "chatstats", "userstats"} {
		t.Run(action, func(t *testing.T) {
			h := newHarness(t)
			ruGroup(h)
			h.press(owner("ru"), panelMessage(), "menu:777:g:"+action)
			e := h.lastEdit()
			if e.text() == "" || len(e.data()) == 0 {
				t.Fatalf("empty screen %q %v", e.text(), e.data())
			}
			for _, d := range e.data() {
				if !strings.HasPrefix(d, "menu:777:") && !strings.HasPrefix(d, "agree:") {
					t.Errorf("callback %q is not for this panel", d)
				}
			}
			h.requireEmptyAnswer()
		})
	}
}

// A private panel that cannot be answered (the chat is gone) is an error to
// alert, and a private panel with the same text is fine to resend.
func TestPrivateSendFailure(t *testing.T) {
	h := newHarness(t)
	h.adapter.sendErr = errBoom()
	h.message(privateMessage(person(ownerID, "en"), "/start"))
	if keys := h.adapter.alertKeys(); len(keys) != 1 || keys[0] != "update:message" {
		t.Fatalf("alerts %v", keys)
	}

	h = newHarness(t)
	h.store.quotes[42] = publishedQuote()
	h.adapter.sendErr = errBoom()
	h.startQuote(person(ownerID, "en"), h.signer.Payload(42))
	if keys := h.adapter.alertKeys(); len(keys) != 1 {
		t.Fatalf("alerts %v", keys)
	}
}

// A message that is a /start in a group, but whose chat has no row after the
// write, cannot be answered.
func TestGroupStartWithoutARow(t *testing.T) {
	h := newHarness(t)
	h.store.getMissing = true
	h.message(startInGroup(person(ownerID, "en"), 100, "/start"))
	if len(h.adapter.sent) != 0 || len(h.adapter.alertKeys()) != 1 {
		t.Fatalf("sent %v alerts %v", h.adapter.sent, h.adapter.alertKeys())
	}
}

// A migration notice has no sender when an anonymous admin made the change; the
// group still moves.
func TestMigrationWithoutASender(t *testing.T) {
	h := newHarness(t)
	h.message(tg.Message{MessageID: 1, Chat: tg.Chat{ID: -4444, Type: tg.ChatGroup}, MigrateToChatID: groupID})
	if len(h.store.migrations) != 1 || h.store.migrations[0].Actor != (domain.Person{}) {
		t.Fatalf("migrations %+v", h.store.migrations)
	}
}

// Telegram refuses to confirm the last updates: the bot still stops.
func TestRunWhenTheConfirmationFails(t *testing.T) {
	h := newHarness(t)
	stop := make(chan struct{})
	t.Cleanup(func() { close(stop) })
	arrived := make(chan int, 16)
	h.api.on("getUpdates", func(c apiCall, n int) reply {
		if c.Query.Get("timeout") == "0" {
			return fail(409, "Conflict")
		}
		arrived <- n
		if n == 0 {
			return ok("[" + privateStartJSON(5, ownerID) + "]")
		}
		<-stop
		return ok("[]")
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := h.runAsync(ctx)
	waitFor(t, arrived, 1)
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

// A backoff that is cut short ends the loop without another poll.
func TestRunStopsWhenTheBackoffIsCut(t *testing.T) {
	h := newHarness(t)
	h.api.answerWith("getUpdates", fail(409, "Conflict"))
	h.bot.sleep = func(context.Context, time.Duration) error { return context.Canceled }
	if err := h.bot.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := len(getUpdatesCalls(h)); n != 1 {
		t.Fatalf("%d polls after the backoff was cut", n)
	}
}

// No Python counterpart: the injectable wait of the update loop and the save retries ends at
// once when its context does.
func TestSleepContext(t *testing.T) {
	if err := sleepContext(context.Background(), time.Nanosecond); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := sleepContext(ctx, time.Hour); err != context.Canceled {
		t.Fatalf("err = %v", err)
	}
}

// No Python counterpart: the conversion of tg types into domain.Person and domain.ChatInfo that
// core.user_getOrCreate and group_getOrCreate did on aiogram objects.
func TestPersonFromAndChatInfo(t *testing.T) {
	if p := personFrom(nil); p != (domain.Person{}) {
		t.Fatalf("personFrom(nil) = %+v", p)
	}
	u := tg.User{ID: 1, IsBot: true, FirstName: "A", LastName: "B", Username: "u", LanguageCode: "de"}
	want := domain.Person{ID: 1, IsBot: true, FirstName: "A", LastName: "B", Username: "u", LanguageCode: "de"}
	if got := personFrom(&u); got != want {
		t.Fatalf("personFrom = %+v", got)
	}
	c := tg.Chat{ID: -5, Type: tg.ChatGroup, Title: "T", Username: "t"}
	if got := chatInfo(c); got != (domain.ChatInfo{ID: -5, Type: tg.ChatGroup, Title: "T", Username: "t"}) {
		t.Fatalf("chatInfo = %+v", got)
	}
}

// Expired registrations are dropped when the registry fills up.
func TestPanelRegistryExpiry(t *testing.T) {
	var r panelRegistry
	r.init()
	now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	for i := int64(1); i <= panelMax; i++ {
		r.register(msgRef{1, i}, msgRef{1, -i}, now)
	}
	later := now.Add(panelTTL + time.Hour)
	r.register(msgRef{2, 1}, msgRef{2, -1}, later)
	if r.size() != 1 {
		t.Fatalf("%d panels after everything expired, want only the new one", r.size())
	}
}

// The language grids render in the viewer's panel language; an unknown language
// on an agreement button is English (checked above) and one on a menu button
// cannot be set.
func TestMenuLanguageCodesAreRenderable(t *testing.T) {
	for _, code := range i18n.Codes() {
		h := newHarness(t)
		ruGroup(h)
		h.press(owner("ru"), panelMessage(), "menu:777:g:setlang:"+code)
		if got := h.store.wrote(); len(got) != 1 || got[0] != "SetLanguage chat -1001234567890 "+code+" manual" {
			t.Fatalf("%s: writes %v", code, got)
		}
	}
}
