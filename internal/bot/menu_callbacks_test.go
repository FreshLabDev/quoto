// SPDX-License-Identifier: Apache-2.0

package bot

import (
	"strings"
	"testing"
	"time"

	"github.com/FreshLabDev/tg"

	"github.com/FreshLabDev/quoto/internal/domain"
	"github.com/FreshLabDev/quoto/internal/i18n"
	"github.com/FreshLabDev/quoto/internal/menu"
)

// ruGroup makes the group Russian and the owner (777) an administrator, the setup
// of most tests in test_handlers.py.
func ruGroup(h *harness) {
	h.store.claims[claimKey{domain.ScopeChat, groupID}] = domain.LangClaim{Language: "ru", Source: domain.SourceManual}
	h.api.setMember(ownerID, "administrator")
}

func owner(lang string) tg.User { return person(ownerID, lang) }

// requireEmptyAnswer: the press was acknowledged once, with no text and no dialog.
func (h *harness) requireEmptyAnswer() {
	h.t.Helper()
	got := h.answers()
	if len(got) != 1 {
		h.t.Fatalf("%d answers, want one", len(got))
	}
	if _, has := got[0].Body["text"]; has {
		h.t.Fatalf("the answer has a text: %v", got[0].Body)
	}
	if _, has := got[0].Body["show_alert"]; has {
		h.t.Fatalf("the answer is a dialog: %v", got[0].Body)
	}
}

func (h *harness) requireNoEdit() {
	h.t.Helper()
	if n := len(h.api.named("editMessageText")); n != 0 {
		h.t.Fatalf("%d edits, want none", n)
	}
}

// Everything the Python callback answered without doing anything: data that is
// not ours, not parseable, or an action this version no longer has. The press
// stops spinning (Python left it spinning for a prefix it did not know).
func TestMenuCallbackIgnoresWhatItDoesNotKnow(t *testing.T) {
	data := []string{
		"", "foo:bar", "menu", "menu:", "menu:777", "menu:777:g", "menu:777:g:", "menu:abc:g:home",
		"menu:777:g:retired", "menu:777:p:retired", "agree:zzz", "agree:v:en:g:notanumber",
		"agree:v", "menu:777:g:Home", "menu:7 77:g:home", "pay:777:g:home",
	}
	for _, d := range data {
		t.Run(d, func(t *testing.T) {
			h := newHarness(t)
			ruGroup(h)
			h.press(owner("ru"), panelMessage(), d)
			h.requireEmptyAnswer()
			h.requireNoEdit()
			if len(h.store.wrote()) != 0 || len(h.store.touched) != 0 || len(h.store.ensures) != 0 {
				t.Fatalf("something was written: %v %v", h.store.wrote(), h.store.ensures)
			}
			if keys := h.adapter.alertKeys(); len(keys) != 0 {
				t.Fatalf("alerts %v", keys)
			}
		})
	}
}

// Somebody else's button: a dialog in the language of the person who pressed, and
// nothing else, whatever the action; the owner check comes before everything,
// Close included (handlers.py start_menu_callback).
func TestMenuCallbackOfAnotherPerson(t *testing.T) {
	for _, action := range []string{"home", "close", "toggle:context", "setlang:de", "plang", "nonsense"} {
		for _, lang := range []string{"ru", "de", "ko", ""} {
			t.Run(action+"/"+lang, func(t *testing.T) {
				h := newHarness(t)
				ruGroup(h)
				h.bot.panels.register(msgRef{groupID, 50}, msgRef{groupID, 49}, h.clock.Now())
				h.press(person(888, lang), panelMessage(), "menu:777:g:"+action)

				text, alert := h.oneAnswer()
				if !alert || text != i18n.T(i18n.OrDefault(lang), "menu.other_user") {
					t.Fatalf("answer %q alert=%v", text, alert)
				}
				if len(h.api.named("deleteMessage")) != 0 || len(h.api.named("editMessageText")) != 0 {
					t.Fatalf("the panel was touched by somebody else: %v", h.api.methods())
				}
				if h.bot.panels.size() != 1 || len(h.store.wrote()) != 0 {
					t.Fatal("state changed")
				}
			})
		}
	}
}

// HND-05 (test_handlers.py:177): Close removes the panel and the message that
// opened it, and forgets the pair; the spinner stops first.
func TestMenuClose(t *testing.T) {
	t.Run("removes both messages", func(t *testing.T) {
		h := newHarness(t)
		h.message(startInGroup(owner("en"), 100, "/start"))
		panel := h.adapter.sentTo(groupID)
		if len(panel) != 1 {
			t.Fatal("no panel")
		}
		// The adapter numbered the panel 9001.
		h.press(owner("en"), tg.Message{MessageID: 9001, Date: 1700000000, Chat: groupChat()}, "menu:777:g:close")

		h.requireEmptyAnswer()
		deletes := h.api.named("deleteMessage")
		if len(deletes) != 2 {
			t.Fatalf("%d deletes", len(deletes))
		}
		ids := []float64{deletes[0].Body["message_id"].(float64), deletes[1].Body["message_id"].(float64)}
		if ids[0] != 9001 || ids[1] != 100 {
			t.Fatalf("deleted %v, want the panel then the command", ids)
		}
		// The answer comes before the first delete.
		order := h.api.methods()
		if order[len(order)-3] != "answerCallbackQuery" {
			t.Fatalf("call order %v", order)
		}
		if h.bot.panels.size() != 0 {
			t.Fatal("the registry still holds the panel")
		}
	})

	t.Run("a panel the bot no longer remembers loses only itself", func(t *testing.T) {
		h := newHarness(t)
		h.press(owner("en"), panelMessage(), "menu:777:g:close")
		h.requireEmptyAnswer()
		if n := len(h.api.named("deleteMessage")); n != 1 {
			t.Fatalf("%d deletes", n)
		}
	})

	t.Run("a registration older than two days is not used", func(t *testing.T) {
		h := newHarness(t)
		h.bot.panels.register(msgRef{groupID, 50}, msgRef{groupID, 49}, h.clock.Now())
		h.clock.advance(49 * time.Hour)
		h.press(owner("en"), panelMessage(), "menu:777:g:close")
		if n := len(h.api.named("deleteMessage")); n != 1 {
			t.Fatalf("%d deletes", n)
		}
	})

	t.Run("a refused delete does not fail the press", func(t *testing.T) {
		h := newHarness(t)
		h.api.answerWith("deleteMessage", fail(400, "Bad Request: message can't be deleted"))
		h.bot.panels.register(msgRef{groupID, 50}, msgRef{groupID, 49}, h.clock.Now())
		h.press(owner("en"), panelMessage(), "menu:777:g:close")
		if len(h.answers()) != 1 || len(h.adapter.alertKeys()) != 0 {
			t.Fatalf("answers %d, alerts %v", len(h.answers()), h.adapter.alertKeys())
		}
		if n := len(h.api.named("deleteMessage")); n != 2 {
			t.Fatalf("%d deletes: the second must be tried after the first fails", n)
		}
	})

	t.Run("Close is not throttled and works in a private panel too", func(t *testing.T) {
		h := newHarness(t)
		h.press(owner("en"), privatePanel(), "menu:777:p:home")
		h.press(owner("en"), privatePanel(), "menu:777:p:close")
		if n := len(h.api.named("deleteMessage")); n != 1 {
			t.Fatalf("%d deletes", n)
		}
	})

	t.Run("a press with no message answers and does nothing", func(t *testing.T) {
		h := newHarness(t)
		h.press(owner("en"), tg.Message{}, "menu:777:g:close")
		h.requireEmptyAnswer()
		if len(h.api.named("deleteMessage")) != 0 {
			t.Fatal("deleted something")
		}
	})
}

// HND-16 (test_handlers.py:499): a second press of the same person on the same
// panel within 0.7 s is dropped with an empty answer; both are answered.
func TestMenuThrottle(t *testing.T) {
	h := newHarness(t)
	ruGroup(h)
	h.press(owner("ru"), panelMessage(), "menu:777:g:sched")
	h.press(owner("ru"), panelMessage(), "menu:777:g:tz")

	if got := len(h.answers()); got != 2 {
		t.Fatalf("%d answers, want 2", got)
	}
	if got := len(h.api.named("editMessageText")); got != 1 {
		t.Fatalf("%d edits, want 1: the second press must be dropped", got)
	}

	h.clock.advance(699 * time.Millisecond)
	h.press(owner("ru"), panelMessage(), "menu:777:g:tz")
	if got := len(h.api.named("editMessageText")); got != 1 {
		t.Fatalf("a press at 699 ms got through (%d edits)", got)
	}

	h.clock.advance(2 * time.Millisecond) // 701 ms since the last accepted press
	h.press(owner("ru"), panelMessage(), "menu:777:g:tz")
	if got := len(h.api.named("editMessageText")); got != 2 {
		t.Fatalf("a press after the window was dropped (%d edits)", got)
	}
}

// The window belongs to a (chat, message, person) triple.
func TestMenuThrottleKeys(t *testing.T) {
	h := newHarness(t)
	ruGroup(h)
	other := tg.Message{MessageID: 51, Date: 1700000000, Chat: groupChat()}
	h.press(owner("ru"), panelMessage(), "menu:777:g:sched")
	h.press(owner("ru"), other, "menu:777:g:sched") // another panel
	if got := len(h.api.named("editMessageText")); got != 2 {
		t.Fatalf("%d edits: another panel shares the window", got)
	}
}

// The throttle comes before the action is looked at: a retired action pressed
// twice is still an empty answer twice.
func TestMenuThrottleBeforeAction(t *testing.T) {
	h := newHarness(t)
	h.press(owner("ru"), panelMessage(), "menu:777:g:retired")
	h.press(owner("ru"), panelMessage(), "menu:777:g:retired")
	if got := len(h.answers()); got != 2 {
		t.Fatalf("%d answers", got)
	}
}

// HND-16's bookkeeping, _is_menu_callback_throttled (handlers.py:175): a press without a chat,
// a message or a person is never throttled, and the map is pruned past 1024 entries (60 s);
// the Go version also caps it under a flood of fresh presses.
func TestCallbackThrottleBookkeeping(t *testing.T) {
	var th callbackThrottle
	th.init()
	now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)

	if th.hit(0, 1, 1, now) || th.hit(0, 1, 1, now) {
		t.Fatal("a press with no chat was throttled")
	}
	if th.hit(1, 0, 1, now) || th.hit(1, 0, 1, now) {
		t.Fatal("a press with no message was throttled")
	}
	if th.hit(1, 1, 0, now) || th.hit(1, 1, 0, now) {
		t.Fatal("a press with no person was throttled")
	}

	// Old entries are dropped once the map is large.
	for i := int64(1); i <= throttlePruneAt+1; i++ {
		th.hit(1, i, 1, now)
	}
	later := now.Add(2 * throttleStale)
	th.hit(2, 1, 1, later)
	if len(th.seen) > 2 {
		t.Fatalf("%d entries after pruning stale ones", len(th.seen))
	}

	// A flood of fresh presses cannot make the map grow without bound.
	var flood callbackThrottle
	flood.init()
	for i := int64(1); i <= 3*throttlePruneAt; i++ {
		flood.hit(1, i, 1, now)
	}
	if len(flood.seen) > 2*throttlePruneAt+1 {
		t.Fatalf("%d entries under a flood", len(flood.seen))
	}
}

// HND-05's registry: handlers.py's _PANEL_COMMAND_MESSAGES grew with every panel and was lost
// on restart; here it is bounded (2048), expires, keeps the newest, and a panel can be taken
// once (doc 40-bot.md item 2).
func TestPanelRegistryBounds(t *testing.T) {
	var r panelRegistry
	r.init()
	now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	for i := int64(1); i <= panelMax+500; i++ {
		r.register(msgRef{1, i}, msgRef{1, -i}, now.Add(time.Duration(i)*time.Second))
	}
	if r.size() > panelMax {
		t.Fatalf("%d panels remembered, limit %d", r.size(), panelMax)
	}
	// The newest survive.
	if _, ok := r.take(msgRef{1, panelMax + 500}, now.Add(24*time.Hour)); !ok {
		t.Fatal("the newest panel was dropped")
	}
	if _, ok := r.take(msgRef{1, 1}, now.Add(24*time.Hour)); ok {
		t.Fatal("the oldest panel survived")
	}
	// Taking forgets.
	if _, ok := r.take(msgRef{1, panelMax + 500}, now); ok {
		t.Fatal("a panel can be taken twice")
	}
}

// Every admin action by a person who is not an administrator: a dialog in the
// group's language, nothing written, nothing redrawn (handlers.py, the admin
// check of start_menu_callback).
func TestMenuAdminGate(t *testing.T) {
	actions := []string{
		"lang", "setlang:de", "autolang", "sched", "timeadj:60", "minadj:5", "tz", "settz:UTC", "behavior", "toggle:pin",
	}
	for _, a := range actions {
		cb := "menu:777:g:" + a
		t.Run(a, func(t *testing.T) {
			if !menu.AdminOnly(strings.SplitN(a, ":", 2)[0]) {
				t.Fatalf("%s is not an admin action", a)
			}
			h := newHarness(t)
			ruGroup(h)
			h.api.setMember(ownerID, "member")
			h.press(owner("en"), panelMessage(), cb)
			text, alert := h.oneAnswer()
			if !alert || text != i18n.T("ru", "admin.admin_only") {
				t.Fatalf("answer %q alert=%v", text, alert)
			}
			h.requireNoEdit()
			if len(h.store.wrote()) != 0 {
				t.Fatalf("writes %v", h.store.wrote())
			}
		})
	}
}

// Doc 7.2 F5: when Telegram cannot say whether the person is an admin, the press
// gets a message instead of spinning. The actions that never look at the rights
// are not affected.
func TestMenuRightsLookupFailure(t *testing.T) {
	for _, a := range []string{"home", "toggle:pin", "sched", "settz:UTC", "doc"} {
		t.Run(a, func(t *testing.T) {
			h := newHarness(t)
			ruGroup(h)
			h.api.memberFails = true
			h.press(owner("en"), panelMessage(), "menu:777:g:"+a)
			text, alert := h.oneAnswer()
			if !alert || text != i18n.T("ru", "settings.invalid") {
				t.Fatalf("answer %q alert=%v", text, alert)
			}
			h.requireNoEdit()
			if len(h.store.wrote()) != 0 {
				t.Fatalf("writes %v", h.store.wrote())
			}
		})
	}
	for _, a := range []string{"about", "chatstats", "userstats"} {
		t.Run(a+" does not need the rights", func(t *testing.T) {
			h := newHarness(t)
			ruGroup(h)
			h.api.memberFails = true
			h.press(owner("en"), panelMessage(), "menu:777:g:"+a)
			if len(h.api.named("editMessageText")) != 1 {
				t.Fatal("not shown")
			}
			if len(h.api.named("getChatMember")) != 0 {
				t.Fatal("asked Telegram needlessly")
			}
		})
	}
}

// A group action pressed on a panel that is not in a group (or not readable)
// says so (menu.group_context_required), instead of doing nothing.
func TestMenuGroupContextRequired(t *testing.T) {
	tests := []struct {
		name  string
		panel tg.Message
	}{
		{"a private panel", privatePanel()},
		{"a channel", tg.Message{MessageID: 50, Date: 1, Chat: tg.Chat{ID: -100999, Type: tg.ChatChannel}}},
		{"a message the bot cannot see", tg.Message{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t)
			h.press(owner("de"), tt.panel, "menu:777:g:home")
			text, alert := h.oneAnswer()
			if !alert || text != i18n.T("de", "menu.group_context_required") {
				t.Fatalf("answer %q alert=%v", text, alert)
			}
			if len(h.store.ensures) != 0 {
				t.Fatal("recorded a group")
			}
		})
	}
}

// An unknown scope is a group, as in Python (any scope that is not "p").
func TestMenuUnknownScopeIsAGroup(t *testing.T) {
	h := newHarness(t)
	ruGroup(h)
	h.press(owner("ru"), panelMessage(), "menu:777:x:about")
	if len(h.api.named("editMessageText")) != 1 || len(h.store.ensures) != 1 {
		t.Fatalf("calls %v", h.api.methods())
	}
}

// A private action on a group panel and a group action on a private panel are
// answered and ignored.
func TestMenuActionOfTheOtherScope(t *testing.T) {
	t.Run("private action on a group panel", func(t *testing.T) {
		h := newHarness(t)
		ruGroup(h)
		for _, a := range []string{"plang", "setplang:de", "autoplang"} {
			h.press(person(ownerID, "ru"), tg.Message{MessageID: 60 + int64(len(a)), Date: 1, Chat: groupChat()}, "menu:777:g:"+a)
		}
		h.requireNoEdit()
		if len(h.store.wrote()) != 0 {
			t.Fatalf("writes %v", h.store.wrote())
		}
	})
	t.Run("group action on a private panel", func(t *testing.T) {
		h := newHarness(t)
		for _, a := range []string{"toggle:pin", "sched", "settz:UTC", "chatstats", "autolang", "setlang:de"} {
			h.press(owner("ru"), tg.Message{MessageID: 60 + int64(len(a)), Date: 1, Chat: tg.Chat{ID: ownerID, Type: tg.ChatPrivate}}, "menu:777:p:"+a)
		}
		h.requireNoEdit()
		if len(h.store.wrote()) != 0 || len(h.store.ensures) != 0 {
			t.Fatalf("writes %v", h.store.wrote())
		}
		if len(h.answers()) != 6 {
			t.Fatalf("%d answers", len(h.answers()))
		}
	})
}

// HND-04 (test_handlers.py:145), through the Home button: the panel is redrawn
// in the group's language with the admin rows for an admin.
func TestMenuHome(t *testing.T) {
	h := newHarness(t)
	ruGroup(h)
	h.press(owner("en"), panelMessage(), "menu:777:g:home")
	want := []string{"Статистика", "Язык", "Цитата дня", "Публикация", "Соглашение", "О боте", "Закрыть"}
	if got := h.lastEdit().labels(); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("buttons %v, want %v", got, want)
	}
	if h.lastEdit().Body["chat_id"].(float64) != float64(groupID) || h.lastEdit().Body["message_id"].(float64) != 50 {
		t.Fatalf("edited %v", h.lastEdit().Body)
	}
	h.requireEmptyAnswer()
	if h.lastEdit().str("parse_mode") != "HTML" {
		t.Fatal("the screen is not HTML")
	}
}

// HND-07 (test_handlers.py:232): the schedule screen names the daily quote, has
// the step buttons and ends with Back and Close.
func TestMenuSchedule(t *testing.T) {
	h := newHarness(t)
	ruGroup(h)
	h.press(owner("ru"), panelMessage(), "menu:777:g:sched")
	e := h.lastEdit()
	if !strings.Contains(e.text(), "Цитата дня") {
		t.Fatalf("text %q", e.text())
	}
	labels := e.labels()
	has := func(want string) bool {
		for _, l := range labels {
			if l == want {
				return true
			}
		}
		return false
	}
	if !has("+1:00") || !has("+5") {
		t.Fatalf("buttons %v", labels)
	}
	if tail := labels[len(labels)-2:]; tail[0] != "Назад" || tail[1] != "Закрыть" {
		t.Fatalf("last buttons %v", tail)
	}
}

// HND-08: the step buttons' payloads (-60 +60 -15 +15 for the time, -5 +5 -1 +1
// for the minimum) arrive as plain integers and move the setting.
func TestMenuScheduleAdjust(t *testing.T) {
	tests := []struct {
		action  string
		payload string
		write   string
	}{
		{"timeadj", "-60", "AdjustQuoteTime -1001234567890 -60"},
		{"timeadj", "60", "AdjustQuoteTime -1001234567890 60"},
		{"timeadj", "-15", "AdjustQuoteTime -1001234567890 -15"},
		{"timeadj", "15", "AdjustQuoteTime -1001234567890 15"},
		{"minadj", "-5", "AdjustMinMessages -1001234567890 -5"},
		{"minadj", "5", "AdjustMinMessages -1001234567890 5"},
		{"minadj", "-1", "AdjustMinMessages -1001234567890 -1"},
		{"minadj", "1", "AdjustMinMessages -1001234567890 1"},
		{"timeadj", "+30", "AdjustQuoteTime -1001234567890 30"},
		{"timeadj", " 15 ", "AdjustQuoteTime -1001234567890 15"},
		{"minadj", "", "AdjustMinMessages -1001234567890 0"},
	}
	for _, tt := range tests {
		t.Run(tt.action+tt.payload, func(t *testing.T) {
			h := newHarness(t)
			ruGroup(h)
			data := "menu:777:g:" + tt.action
			if tt.payload != "" {
				data += ":" + tt.payload
			}
			h.press(owner("ru"), panelMessage(), data)
			if got := h.store.wrote(); len(got) != 1 || got[0] != tt.write {
				t.Fatalf("writes %v, want %q", got, tt.write)
			}
			text, alert := h.oneAnswer()
			if alert || text != i18n.T("ru", "settings.updated") {
				t.Fatalf("answer %q alert=%v", text, alert)
			}
			if len(h.api.named("editMessageText")) != 1 {
				t.Fatal("the screen was not redrawn")
			}
		})
	}

	for _, payload := range []string{"abc", "1.5", "--1", "1e3", "٣"} {
		t.Run("invalid "+payload, func(t *testing.T) {
			h := newHarness(t)
			ruGroup(h)
			h.press(owner("ru"), panelMessage(), "menu:777:g:timeadj:"+payload)
			text, alert := h.oneAnswer()
			if !alert || text != i18n.T("ru", "settings.invalid") {
				t.Fatalf("answer %q alert=%v", text, alert)
			}
			if len(h.store.wrote()) != 0 {
				t.Fatalf("writes %v", h.store.wrote())
			}
		})
	}

	t.Run("the redrawn screen shows the new time", func(t *testing.T) {
		h := newHarness(t)
		ruGroup(h)
		hour, minute := 21, 7
		g := h.store.group(groupID)
		g.QuoteHour, g.QuoteMinute = &hour, &minute
		h.press(owner("ru"), panelMessage(), "menu:777:g:timeadj:60")
		if !strings.Contains(h.lastEdit().text(), "22:07") {
			t.Fatalf("text %q", h.lastEdit().text())
		}
	})
}

// Time zones: any zone the runtime can load works, not only the thirteen on the
// buttons, so a button from an older panel keeps working; a name that is not a
// zone, or a group that does not exist, says so.
func TestMenuTimezone(t *testing.T) {
	t.Run("the screen", func(t *testing.T) {
		h := newHarness(t)
		ruGroup(h)
		h.press(owner("ru"), panelMessage(), "menu:777:g:tz")
		if got := h.lastEdit().data(); len(got) < len(menu.TimezoneChoices) {
			t.Fatalf("%d buttons", len(got))
		}
	})
	for _, zone := range []string{"Europe/Berlin", "Europe/Paris", "UTC", "Pacific/Auckland"} {
		t.Run("set "+zone, func(t *testing.T) {
			h := newHarness(t)
			ruGroup(h)
			h.press(owner("ru"), panelMessage(), "menu:777:g:settz:"+zone)
			if got := h.store.wrote(); len(got) != 1 || got[0] != "SetTimezone -1001234567890 "+zone {
				t.Fatalf("writes %v", got)
			}
			text, alert := h.oneAnswer()
			if alert || text != i18n.T("ru", "settings.updated") {
				t.Fatalf("answer %q alert=%v", text, alert)
			}
		})
	}
	for _, zone := range []string{"Mars/Base", "", "Local", "../etc/passwd", "europe/berlin "} {
		t.Run("invalid "+zone, func(t *testing.T) {
			h := newHarness(t)
			ruGroup(h)
			h.press(owner("ru"), panelMessage(), "menu:777:g:settz:"+zone)
			text, alert := h.oneAnswer()
			if !alert || text != i18n.T("ru", "settings.invalid") {
				t.Fatalf("answer %q alert=%v", text, alert)
			}
			if len(h.store.wrote()) != 0 {
				t.Fatalf("writes %v", h.store.wrote())
			}
		})
	}
	t.Run("a group that is gone", func(t *testing.T) {
		h := newHarness(t)
		ruGroup(h)
		h.store.noGroup = true
		h.press(owner("ru"), panelMessage(), "menu:777:g:settz:UTC")
		text, alert := h.oneAnswer()
		if !alert || text != i18n.T("ru", "settings.invalid") {
			t.Fatalf("answer %q alert=%v", text, alert)
		}
	})
}

// HND-09 (test_handlers.py:295): the behaviour screen explains the context and
// labels it with the glyph; each switch flips its own setting and the screen
// shows the result.
func TestMenuBehaviorAndToggle(t *testing.T) {
	h := newHarness(t)
	ruGroup(h)
	h.press(owner("ru"), panelMessage(), "menu:777:g:behavior")
	e := h.lastEdit()
	if !strings.Contains(e.text(), "сообщения, которые ИИ выбрал") {
		t.Fatalf("text %q", e.text())
	}
	found := false
	for _, l := range e.labels() {
		found = found || l == "◉ Контекст"
	}
	if !found {
		t.Fatalf("buttons %v", e.labels())
	}

	for _, key := range []string{"context", "boring", "pin", "media"} {
		t.Run(key, func(t *testing.T) {
			h := newHarness(t)
			ruGroup(h)
			before := h.store.group(groupID).Enabled(domain.ToggleKey(key))
			h.press(owner("ru"), panelMessage(), "menu:777:g:toggle:"+key)
			after := h.store.group(groupID).Enabled(domain.ToggleKey(key))
			if before == after {
				t.Fatalf("%s was not flipped", key)
			}
			if got := h.store.wrote(); len(got) != 1 || got[0] != "Toggle -1001234567890 "+key {
				t.Fatalf("writes %v", got)
			}
			text, _ := h.oneAnswer()
			if text != i18n.T("ru", "settings.updated") {
				t.Fatalf("answer %q", text)
			}
			if len(h.api.named("editMessageText")) != 1 {
				t.Fatal("not redrawn")
			}
		})
	}

	t.Run("the redrawn screen shows the new state", func(t *testing.T) {
		h := newHarness(t)
		ruGroup(h)
		h.press(owner("ru"), panelMessage(), "menu:777:g:toggle:context")
		found := false
		for _, l := range h.lastEdit().labels() {
			found = found || l == "◎ Контекст"
		}
		if !found {
			t.Fatalf("buttons %v", h.lastEdit().labels())
		}
	})

	// An unknown key changes nothing and still reports success, as it did.
	for _, key := range []string{"zzz", "", "CONTEXT"} {
		t.Run("unknown key "+key, func(t *testing.T) {
			h := newHarness(t)
			ruGroup(h)
			data := "menu:777:g:toggle"
			if key != "" {
				data += ":" + key
			}
			h.press(owner("ru"), panelMessage(), data)
			if len(h.store.wrote()) != 0 {
				t.Fatalf("writes %v", h.store.wrote())
			}
			text, alert := h.oneAnswer()
			if alert || text != i18n.T("ru", "settings.updated") {
				t.Fatalf("answer %q alert=%v", text, alert)
			}
		})
	}
}

// HND-06 (test_handlers.py:194): choosing a language for the group writes a
// manual claim and redraws the screen in the new language, with the toast in it
// too. A code that is not a language is a dialog.
func TestMenuGroupLanguage(t *testing.T) {
	t.Run("HND-06", func(t *testing.T) {
		h := newHarness(t)
		ruGroup(h)
		h.press(owner("ru"), panelMessage(), "menu:777:g:setlang:uk")
		if got := h.store.wrote(); len(got) != 1 || got[0] != "SetLanguage chat -1001234567890 uk manual" {
			t.Fatalf("writes %v", got)
		}
		if !strings.Contains(h.lastEdit().text(), "Мова групи") {
			t.Fatalf("text %q", h.lastEdit().text())
		}
		text, alert := h.oneAnswer()
		if alert || text != i18n.T("uk", "menu.language.updated", "language_name", i18n.Name("uk")) {
			t.Fatalf("answer %q alert=%v", text, alert)
		}
	})
	t.Run("a code with a region and capitals", func(t *testing.T) {
		h := newHarness(t)
		ruGroup(h)
		h.press(owner("ru"), panelMessage(), "menu:777:g:setlang:DE")
		if got := h.store.wrote(); len(got) != 1 || got[0] != "SetLanguage chat -1001234567890 de manual" {
			t.Fatalf("writes %v", got)
		}
	})
	for _, code := range []string{"xx", "", "ruu"} {
		t.Run("invalid "+code, func(t *testing.T) {
			h := newHarness(t)
			ruGroup(h)
			data := "menu:777:g:setlang"
			if code != "" {
				data += ":" + code
			}
			h.press(owner("ru"), panelMessage(), data)
			text, alert := h.oneAnswer()
			if !alert || text != i18n.T("ru", "menu.language.invalid") {
				t.Fatalf("answer %q alert=%v", text, alert)
			}
			if len(h.store.wrote()) != 0 {
				t.Fatalf("writes %v", h.store.wrote())
			}
		})
	}
	t.Run("the automatic choice", func(t *testing.T) {
		h := newHarness(t)
		ruGroup(h)
		h.press(owner("ru"), panelMessage(), "menu:777:g:autolang")
		if got := h.store.wrote(); len(got) != 1 || got[0] != "ClearLanguage chat -1001234567890" {
			t.Fatalf("writes %v", got)
		}
		// With no claim left the group speaks the default, and so does the reply.
		text, _ := h.oneAnswer()
		if text != i18n.T("en", "settings.group.language_auto_updated") {
			t.Fatalf("answer %q", text)
		}
	})
	t.Run("a store failure is an error and the press is still answered", func(t *testing.T) {
		h := newHarness(t)
		ruGroup(h)
		h.store.errs["SetLanguage"] = errBoom()
		h.press(owner("ru"), panelMessage(), "menu:777:g:setlang:de")
		if len(h.answers()) != 1 {
			t.Fatalf("%d answers", len(h.answers()))
		}
		if got := h.adapter.alertKeys(); len(got) != 1 || got[0] != "update:callback_query" {
			t.Fatalf("alerts %v", got)
		}
	})
}

// HND-10 (test_handlers.py:333): a member opening their own statistics gets the
// two tabs with the personal one selected.
func TestMenuStats(t *testing.T) {
	t.Run("HND-10: userstats for a member", func(t *testing.T) {
		h := newHarness(t)
		ruGroup(h)
		h.api.setMember(ownerID, "member")
		h.press(owner("ru"), panelMessage(), "menu:777:g:userstats")
		e := h.lastEdit()
		if !strings.Contains(e.text(), "Твоя статистика") {
			t.Fatalf("text %q", e.text())
		}
		want := []string{"◉ Моя статистика", "◎ Статистика чата", "Назад", "Закрыть"}
		if got := e.labels(); strings.Join(got, "|") != strings.Join(want, "|") {
			t.Fatalf("buttons %v, want %v", got, want)
		}
	})
	statsScreen := func(h *harness, text, view string) (string, []string) {
		in := menu.GroupInputFor(ownerID, *h.store.group(groupID), false, "1.2.3")
		in.Lang = "ru"
		in.Section, in.StatsText, in.StatsView = menu.SectionStats, text, view
		screen, markup := menu.GroupPanel(in)
		return screen, labelsOf(markup)
	}
	t.Run("the chat tab reads the chat's numbers", func(t *testing.T) {
		h := newHarness(t)
		ruGroup(h)
		stats := &domain.ChatStats{TotalQuotes: 3, UniqueAuthors: 2, AvgScore: 0.75}
		h.store.chatStats = stats
		h.press(owner("ru"), panelMessage(), "menu:777:g:chatstats")
		want, labels := statsScreen(h, menu.ChatStatsText("ru", stats), menu.StatsChat)
		if got := h.lastEdit().text(); got != want {
			t.Fatalf("screen:\n%s\nwant\n%s", got, want)
		}
		if got := h.lastEdit().labels(); strings.Join(got, "|") != strings.Join(labels, "|") {
			t.Fatalf("buttons %v, want %v", got, labels)
		}
		if !strings.Contains(h.lastEdit().text(), "7,5") && !strings.Contains(h.lastEdit().text(), "7.5") {
			t.Fatalf("the average (7.5 of 10) is not in\n%s", h.lastEdit().text())
		}
	})
	t.Run("the person's tab reads the person's numbers", func(t *testing.T) {
		h := newHarness(t)
		ruGroup(h)
		stats := &domain.UserStats{}
		h.store.userStats = stats
		h.press(owner("ru"), panelMessage(), "menu:777:g:userstats")
		want, _ := statsScreen(h, menu.UserStatsText("ru", stats), menu.StatsUser)
		if got := h.lastEdit().text(); got != want {
			t.Fatalf("screen:\n%s\nwant\n%s", got, want)
		}
	})
	t.Run("no statistics yet", func(t *testing.T) {
		h := newHarness(t)
		ruGroup(h)
		h.press(owner("ru"), panelMessage(), "menu:777:g:chatstats")
		want, _ := statsScreen(h, menu.ChatStatsText("ru", nil), menu.StatsChat)
		if got := h.lastEdit().text(); got != want {
			t.Fatalf("screen:\n%s\nwant\n%s", got, want)
		}
	})
	t.Run("a store failure is an error and the press is still answered", func(t *testing.T) {
		h := newHarness(t)
		ruGroup(h)
		h.store.errs["Chat"] = errBoom()
		h.press(owner("ru"), panelMessage(), "menu:777:g:chatstats")
		if len(h.answers()) != 1 || len(h.adapter.alertKeys()) != 1 {
			t.Fatalf("answers %d alerts %v", len(h.answers()), h.adapter.alertKeys())
		}
	})
}

// The About tab of a group panel and of a private one (HND-13, test_handlers.py:431).
func TestMenuAbout(t *testing.T) {
	t.Run("private", func(t *testing.T) {
		h := newHarness(t)
		h.press(owner("ru"), privatePanel(), "menu:777:p:about")
		e := h.lastEdit()
		want, _ := menu.PrivatePanel(menu.PrivateInput{Owner: ownerID, Lang: "ru", BotUsername: testUsername, Version: "1.2.3", Section: menu.SectionAbout})
		if e.text() != want {
			t.Fatalf("text:\n%s\nwant\n%s", e.text(), want)
		}
		if got := e.labels(); len(got) != 1 || got[0] != "Назад" {
			t.Fatalf("HND-13: buttons %v, want only Back", got)
		}
		if len(h.store.touched) != 1 {
			t.Fatal("the person was not recorded")
		}
	})
	t.Run("group", func(t *testing.T) {
		h := newHarness(t)
		ruGroup(h)
		h.press(owner("ru"), panelMessage(), "menu:777:g:about")
		if got := h.lastEdit().labels(); got[len(got)-1] != "Закрыть" {
			t.Fatalf("buttons %v", got)
		}
	})
}

// HND-18 (test_handlers.py:555): the person's own language in private.
func TestMenuPrivateLanguage(t *testing.T) {
	t.Run("the screen", func(t *testing.T) {
		h := newHarness(t)
		h.press(owner("ru"), privatePanel(), "menu:777:p:plang")
		want, markup := menu.PrivatePanel(menu.PrivateInput{Owner: ownerID, Lang: "ru", BotUsername: testUsername, Version: "1.2.3", Section: menu.SectionLanguage})
		if got := h.lastEdit().text(); got != want {
			t.Fatalf("screen:\n%s\nwant\n%s", got, want)
		}
		if got := h.lastEdit().data(); strings.Join(got, "|") != strings.Join(dataOf(markup), "|") {
			t.Fatalf("callbacks %v", got)
		}
	})
	t.Run("HND-18", func(t *testing.T) {
		h := newHarness(t)
		h.press(owner("ru"), privatePanel(), "menu:777:p:setplang:de")
		if got := h.store.wrote(); len(got) != 1 || got[0] != "SetLanguage user 777 de manual" {
			t.Fatalf("writes %v", got)
		}
		if !strings.Contains(h.lastEdit().text(), "Sprache im privaten Chat") {
			t.Fatalf("text %q", h.lastEdit().text())
		}
		text, alert := h.oneAnswer()
		if alert || text != i18n.T("de", "menu.language.updated", "language_name", i18n.Name("de")) {
			t.Fatalf("answer %q alert=%v", text, alert)
		}
	})
	t.Run("an invalid code is a dialog", func(t *testing.T) {
		h := newHarness(t)
		h.press(owner("ru"), privatePanel(), "menu:777:p:setplang:xx")
		text, alert := h.oneAnswer()
		if !alert || text != i18n.T("ru", "menu.language.invalid") {
			t.Fatalf("answer %q alert=%v", text, alert)
		}
		if len(h.store.wrote()) != 0 {
			t.Fatalf("writes %v", h.store.wrote())
		}
	})
	t.Run("a failing store is an error", func(t *testing.T) {
		h := newHarness(t)
		h.store.errs["TouchUser"] = errBoom()
		h.press(owner("ru"), privatePanel(), "menu:777:p:home")
		if len(h.answers()) != 1 || len(h.adapter.alertKeys()) != 1 {
			t.Fatalf("answers %d alerts %v", len(h.answers()), h.adapter.alertKeys())
		}
	})
}

// HND-28 (test_handlers.py:~520): the "Telegram language" button clears Quoto's
// claim and then asks core again, so a choice another bot made (ru, manual)
// survives and the screen and the toast speak it, not the German client's hint.
func TestMenuAutoPrivateLanguage(t *testing.T) {
	t.Run("another bot's choice survives", func(t *testing.T) {
		h := newHarness(t)
		survivor := domain.LangClaim{Language: "ru", Source: domain.SourceManual}
		h.store.claims[claimKey{domain.ScopeUser, ownerID}] = domain.LangClaim{Language: "de", Source: domain.SourceManual}
		h.store.clearLeaves = map[claimKey]domain.LangClaim{{domain.ScopeUser, ownerID}: survivor}

		h.press(owner("de"), privatePanel(), "menu:777:p:autoplang")

		if got := h.store.wrote(); len(got) != 1 || got[0] != "ClearLanguage user 777" {
			t.Fatalf("writes %v", got)
		}
		want, _ := menu.PrivatePanel(menu.PrivateInput{
			Owner: ownerID, Lang: "ru", LangSource: domain.SourceManual,
			BotUsername: testUsername, Version: "1.2.3", Section: menu.SectionLanguage,
		})
		if got := h.lastEdit().text(); got != want {
			t.Fatalf("screen:\n%s\nwant\n%s", got, want)
		}
		text, _ := h.oneAnswer()
		if text != i18n.T("ru", "settings.updated") {
			t.Fatalf("toast %q", text)
		}
	})
	t.Run("with nothing left the client language answers", func(t *testing.T) {
		h := newHarness(t)
		h.store.claims[claimKey{domain.ScopeUser, ownerID}] = domain.LangClaim{Language: "ru", Source: domain.SourceManual}
		h.press(owner("de"), privatePanel(), "menu:777:p:autoplang")
		want, _ := menu.PrivatePanel(menu.PrivateInput{
			Owner: ownerID, Lang: "de", BotUsername: testUsername, Version: "1.2.3", Section: menu.SectionLanguage,
		})
		if got := h.lastEdit().text(); got != want {
			t.Fatalf("screen:\n%s\nwant\n%s", got, want)
		}
		text, _ := h.oneAnswer()
		if text != i18n.T("de", "settings.updated") {
			t.Fatalf("toast %q", text)
		}
	})
}

// HND-17 (test_handlers.py:535): an edit Telegram refuses (flood limit, a message
// too old, a deleted panel) is logged; the setting is already saved, so the press
// is answered with its toast and no second answer or alert follows.
func TestMenuEditFailure(t *testing.T) {
	failures := map[string]reply{
		"flood limit":     fail(429, "Too Many Requests: retry later"),
		"not modifiable":  fail(400, "Bad Request: message can't be edited"),
		"message missing": fail(400, "Bad Request: message to edit not found"),
		"server error":    fail(502, "Bad Gateway"),
	}
	for name, rep := range failures {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			ruGroup(h)
			h.api.answerWith("editMessageText", rep)
			h.press(owner("ru"), panelMessage(), "menu:777:g:toggle:pin")

			if got := h.store.wrote(); len(got) != 1 {
				t.Fatalf("the setting was not saved: %v", got)
			}
			text, alert := h.oneAnswer()
			if alert || text != i18n.T("ru", "settings.updated") {
				t.Fatalf("answer %q alert=%v", text, alert)
			}
			if keys := h.adapter.alertKeys(); len(keys) != 0 {
				t.Fatalf("alerts %v", keys)
			}
		})
	}
}

// A message the bot can no longer see (older than Telegram keeps) cannot be
// edited; the change is saved and the press answered.
func TestMenuInaccessiblePanel(t *testing.T) {
	h := newHarness(t)
	ruGroup(h)
	gone := tg.Message{MessageID: 50, Chat: groupChat()} // date 0: inaccessible
	h.press(owner("ru"), gone, "menu:777:g:toggle:pin")
	h.requireNoEdit()
	if len(h.store.wrote()) != 1 {
		t.Fatalf("writes %v", h.store.wrote())
	}
	text, _ := h.oneAnswer()
	if text != i18n.T("ru", "settings.updated") {
		t.Fatalf("answer %q", text)
	}
}

// The answer to a press that fails in the handler is still sent (a button that
// spins for half a minute is worse than one that does nothing), and Telegram
// refusing the answer is not an error of the update.
func TestMenuAnswerFailures(t *testing.T) {
	t.Run("the group cannot be read", func(t *testing.T) {
		h := newHarness(t)
		h.store.errs["Get"] = errBoom()
		h.press(owner("ru"), panelMessage(), "menu:777:g:about")
		if len(h.answers()) != 1 || len(h.adapter.alertKeys()) != 1 {
			t.Fatalf("answers %d alerts %v", len(h.answers()), h.adapter.alertKeys())
		}
	})
	t.Run("an answer that arrives too late", func(t *testing.T) {
		h := newHarness(t)
		ruGroup(h)
		h.api.answerWith("answerCallbackQuery", fail(400, "Bad Request: query is too old and response timeout expired or query ID is invalid"))
		h.press(owner("ru"), panelMessage(), "menu:777:g:about")
		if len(h.adapter.alertKeys()) != 0 {
			t.Fatalf("alerts %v", h.adapter.alertKeys())
		}
		if len(h.api.named("editMessageText")) != 1 {
			t.Fatal("the screen was not drawn")
		}
	})
}

// The language of the group reaches the screen and the toast; the group is the
// one the panel is in, whoever the viewer is.
func TestMenuUsesTheGroupsLanguage(t *testing.T) {
	h := newHarness(t)
	ruGroup(h)
	h.press(owner("de"), panelMessage(), "menu:777:g:toggle:pin")
	text, _ := h.oneAnswer()
	if text != i18n.T("ru", "settings.updated") {
		t.Fatalf("toast %q is not in the group's language", text)
	}
}
