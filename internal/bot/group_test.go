// SPDX-License-Identifier: Apache-2.0

package bot

import (
	"errors"
	"strings"
	"testing"

	"github.com/FreshLabDev/tg"

	"github.com/FreshLabDev/quoto/internal/agreement"
	"github.com/FreshLabDev/quoto/internal/domain"
	"github.com/FreshLabDev/quoto/internal/i18n"
	"github.com/FreshLabDev/quoto/internal/menu"
)

func startInGroup(from tg.User, id int64, text string) tg.Message {
	m := groupText(from, id, text)
	return m
}

// HND-04 (test_handlers.py:145): /start in a group, from an admin, in a Russian
// group: the group's panel in the group's language, with the admin buttons.
func TestGroupStartAsAdmin(t *testing.T) {
	h := newHarness(t)
	h.store.claims[claimKey{domain.ScopeChat, groupID}] = domain.LangClaim{Language: "ru", Source: domain.SourceManual}
	h.api.setMember(ownerID, "administrator")

	h.message(startInGroup(person(ownerID, "en"), 100, "/start"))

	sent := h.onlySent(groupID)
	want := []string{"Статистика", "Язык", "Цитата дня", "Публикация", "Соглашение", "О боте", "Закрыть"}
	if got := labelsOf(sent.Markup); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("HND-04: buttons %v, want %v", got, want)
	}
	for _, d := range dataOf(sent.Markup) {
		if !strings.HasPrefix(d, "menu:777:g:") {
			t.Errorf("callback %q is not owned by the person who opened the panel", d)
		}
	}
	if sent.ThreadID != 0 {
		t.Errorf("thread id %d in a group without topics", sent.ThreadID)
	}
	if len(h.store.ensures) != 1 || h.store.ensures[0].Actor.ID != ownerID || h.store.ensures[0].Chat.ID != groupID ||
		h.store.ensures[0].Chat.Title != "Test group" || h.store.ensures[0].Chat.Type != tg.ChatSupergroup {
		t.Fatalf("group not recorded as expected: %+v", h.store.ensures)
	}
	if calls := h.api.named("getChatMember"); len(calls) != 1 || calls[0].Query.Get("user_id") != "777" {
		t.Fatalf("rights lookups: %+v", calls)
	}
}

// The same panel for a member leaves the admin rows out (menu_test covers the
// panel; this checks that the bot passes the admin flag it got from Telegram).
func TestGroupStartAsMember(t *testing.T) {
	for _, status := range []string{"member", "restricted", "left"} {
		t.Run(status, func(t *testing.T) {
			h := newHarness(t)
			h.api.setMember(ownerID, status)
			h.message(startInGroup(person(ownerID, "en"), 100, "/start"))
			g := h.store.group(groupID)
			want, _ := menu.GroupPanel(menu.GroupInputFor(ownerID, *g, false, "1.2.3"))
			got := h.onlySent(groupID)
			if got.Text != want {
				t.Fatalf("panel:\n%s\nwant\n%s", got.Text, want)
			}
			for _, l := range labelsOf(got.Markup) {
				if l == "Language" || l == "Daily quote" || l == "Publishing" {
					t.Errorf("a member got the admin button %q", l)
				}
			}
		})
	}
}

// Telegram cannot say who the person is: the panel is the member's. Python's
// _is_chat_admin returned False on any API error.
func TestGroupStartWhenTheRightsLookupFails(t *testing.T) {
	h := newHarness(t)
	h.api.memberFails = true
	h.message(startInGroup(person(ownerID, "en"), 100, "/start"))
	sent := h.onlySent(groupID)
	want, _ := menu.GroupPanel(menu.GroupInputFor(ownerID, *h.store.group(groupID), false, "1.2.3"))
	if sent.Text != want {
		t.Fatalf("panel:\n%s", sent.Text)
	}
	if keys := h.adapter.alertKeys(); len(keys) != 0 {
		t.Fatalf("a failed rights lookup alerted: %v", keys)
	}
}

// Doc 40-bot.md, item 6: message_thread_id is sent only for a message in a real
// topic. Telegram also sets it on a reply chain of an ordinary supergroup, where
// sending it back is a 400.
func TestGroupStartThread(t *testing.T) {
	tests := []struct {
		name  string
		topic bool
		want  int
	}{
		{"in a forum topic", true, 4242},
		{"a reply chain in an ordinary supergroup", false, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t)
			m := startInGroup(person(ownerID, "en"), 100, "/start")
			m.MessageThreadID, m.IsTopicMessage = 4242, tt.topic
			h.message(m)
			if got := h.onlySent(groupID).ThreadID; got != tt.want {
				t.Fatalf("thread id %d, want %d", got, tt.want)
			}
		})
	}
}

// The commands Python's Command("start") filter accepted in a group: /start and
// /start@thisbot; another bot's /start is not ours and, being a command, is not
// collected either.
func TestGroupStartRouting(t *testing.T) {
	tests := []struct {
		text  string
		panel bool
	}{
		{"/start", true},
		{"/start payload", true},
		{"/start@quoto_test_bot", true},
		{"/start@Quoto_Test_Bot", true},
		{"/start@otherbot", false},
		{"/Start", false},
		{"/help", false},
	}
	for _, tt := range tests {
		t.Run(tt.text, func(t *testing.T) {
			h := newHarness(t)
			h.message(startInGroup(person(ownerID, "en"), 100, tt.text))
			if got := len(h.adapter.sent) == 1; got != tt.panel {
				t.Fatalf("panel sent = %v, want %v", got, tt.panel)
			}
			if len(h.store.saved) != 0 {
				t.Fatalf("a command was collected: %+v", h.store.saved)
			}
		})
	}
}

// group_start_handler (handlers.py): no from_user means no panel (anonymous admins send as the
// group, which has no rights to check); a failure is an error of the update, not a silence.
func TestGroupStartWithoutASenderAndOnFailure(t *testing.T) {
	t.Run("no sender", func(t *testing.T) {
		h := newHarness(t)
		h.message(tg.Message{MessageID: 1, Chat: groupChat(), Text: "/start"})
		if len(h.adapter.sent) != 0 || len(h.store.ensures) != 0 {
			t.Fatal("the bot reacted to a message without a sender")
		}
	})
	t.Run("the group cannot be recorded", func(t *testing.T) {
		h := newHarness(t)
		h.store.errs["Ensure"] = errBoom()
		h.message(startInGroup(person(ownerID, "en"), 100, "/start"))
		if len(h.adapter.sent) != 0 {
			t.Fatal("answered after a failure")
		}
		if got := h.adapter.alertKeys(); len(got) != 1 || got[0] != "update:message" {
			t.Fatalf("alerts %v", got)
		}
	})
	t.Run("the panel cannot be sent", func(t *testing.T) {
		h := newHarness(t)
		h.adapter.sendErr = errBoom()
		h.message(startInGroup(person(ownerID, "en"), 100, "/start"))
		if got := h.adapter.alertKeys(); len(got) != 1 {
			t.Fatalf("alerts %v", got)
		}
		if h.bot.panels.size() != 0 {
			t.Fatal("a panel that was never sent was registered")
		}
	})
}

// HND-05 (test_handlers.py:177) needs the registry: the panel remembers the
// message that opened it. A /start in a caption (a photo) is a start, but its
// message is not one Close removes.
func TestGroupPanelIsRegisteredWithItsCommand(t *testing.T) {
	h := newHarness(t)
	h.message(startInGroup(person(ownerID, "en"), 100, "/start"))
	panel := msgRef{groupID, 9001}
	got, ok := h.bot.panels.take(panel, h.clock.Now())
	if !ok || got != (msgRef{groupID, 100}) {
		t.Fatalf("registry gave %+v, %v", got, ok)
	}

	h = newHarness(t)
	photo := startInGroup(person(ownerID, "en"), 101, "")
	photo.Caption = "/start"
	photo.Photo = []tg.Photo{{FileID: "f", FileUniqueID: "u"}}
	h.message(photo)
	if len(h.adapter.sent) != 1 {
		t.Fatalf("a /start caption did not open the panel")
	}
	if h.bot.panels.size() != 0 {
		t.Fatal("the panel of a caption command was registered")
	}
}

// The greeting when the bot joins (bot_added_to_chat_event, handlers.py:350-398)
// with doc 7.2 F2 (groups only) and F9 (the time of the group's own row).
func TestMyChatMember(t *testing.T) {
	member := func(status string) tg.ChatMember { return tg.ChatMember{Status: status} }
	update := func(chat tg.Chat, from tg.User, oldStatus, newStatus string) tg.Update {
		return tg.Update{UpdateID: 1, MyChatMember: &tg.ChatMemberUpdated{
			Chat: chat, From: from, OldChatMember: member(oldStatus), NewChatMember: member(newStatus),
		}}
	}
	welcome := func(lang string, chatTitle string, admin bool) string {
		text := i18n.T(lang, "chat_member.added", "chat_title", chatTitle, "time", "21:07")
		if !admin {
			text += "\n\n" + i18n.T(lang, "chat_member.need_admin")
		}
		return text + "\n\n" + i18n.T(lang, "agreement.welcome_note")
	}

	added := []struct {
		name      string
		old, next string
		admin     bool
	}{
		{"added as a member", "left", "member", false},
		{"added as an administrator", "left", "administrator", true},
		{"added again after a kick", "kicked", "member", false},
		{"added with no previous status", "", "administrator", true},
	}
	for _, tt := range added {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t)
			chat := tg.Chat{ID: groupID, Type: tg.ChatSupergroup, Title: "Team <3 & Co"}
			h.deliver(update(chat, person(ownerID, "ru"), tt.old, tt.next))

			sent := h.onlySent(groupID)
			if want := welcome("ru", "Team &lt;3 &amp; Co", tt.admin); sent.Text != want {
				t.Fatalf("greeting:\n%s\nwant\n%s", sent.Text, want)
			}
			wantKeys := labelsOf(agreement.WelcomeKeyboard("ru"))
			if strings.Join(labelsOf(sent.Markup), "|") != strings.Join(wantKeys, "|") {
				t.Fatalf("keyboard %v, want %v", labelsOf(sent.Markup), wantKeys)
			}
			if sent.ThreadID != 0 {
				t.Errorf("a greeting went to thread %d", sent.ThreadID)
			}
			if len(h.store.ensures) != 1 || h.store.ensures[0].Actor.ID != ownerID {
				t.Fatalf("group not recorded for the person who added the bot: %+v", h.store.ensures)
			}
		})
	}

	t.Run("a group is greeted in the language of whoever added the bot", func(t *testing.T) {
		h := newHarness(t)
		h.deliver(update(groupChat(), person(ownerID, "de"), "left", "member"))
		if got := h.onlySent(groupID).Text; got != welcome("de", "Test group", false) {
			t.Fatalf("greeting:\n%s", got)
		}
	})

	t.Run("F9: the cutoff shown is the one in the group's row", func(t *testing.T) {
		h := newHarness(t)
		minute, hour := 42, 18
		g := h.store.group(groupID)
		g.QuoteMinute, g.QuoteHour = &minute, &hour
		h.deliver(update(groupChat(), person(ownerID, "en"), "left", "member"))
		if got := h.onlySent(groupID).Text; !strings.Contains(got, "18:42") {
			t.Fatalf("greeting does not show the group's own time:\n%s", got)
		}
	})

	t.Run("a group with no title is named by its handle, then by its number", func(t *testing.T) {
		h := newHarness(t)
		h.deliver(update(tg.Chat{ID: groupID, Type: tg.ChatGroup, Username: "team_chat"}, person(ownerID, "en"), "left", "member"))
		if got := h.onlySent(groupID).Text; !strings.Contains(got, "@team_chat") {
			t.Fatalf("greeting:\n%s", got)
		}
		h = newHarness(t)
		h.deliver(update(tg.Chat{ID: groupID, Type: tg.ChatGroup}, person(ownerID, "en"), "left", "member"))
		if got := h.onlySent(groupID).Text; !strings.Contains(got, "-1001234567890") {
			t.Fatalf("greeting:\n%s", got)
		}
	})

	t.Run("made an administrator", func(t *testing.T) {
		for _, old := range []string{"member", "restricted"} {
			h := newHarness(t)
			h.deliver(update(groupChat(), person(ownerID, "en"), old, "administrator"))
			sent := h.onlySent(groupID)
			if sent.Text != i18n.T("en", "chat_member.admin_granted") || sent.Markup != nil {
				t.Fatalf("message %+v", sent)
			}
			if len(h.store.ensures) != 0 {
				t.Fatal("a promotion recorded the group")
			}
		}
	})

	t.Run("lost the administrator rights", func(t *testing.T) {
		for _, next := range []string{"member", "restricted"} {
			h := newHarness(t)
			h.deliver(update(groupChat(), person(ownerID, "en"), "administrator", next))
			if got := h.onlySent(groupID).Text; got != i18n.T("en", "chat_member.need_admin") {
				t.Fatalf("message %q", got)
			}
		}
	})

	t.Run("HND-26: removed from the group", func(t *testing.T) {
		for _, next := range []string{"left", "kicked"} {
			h := newHarness(t)
			h.store.group(groupID)
			h.deliver(update(groupChat(), person(ownerID, "en"), "administrator", next))
			if len(h.adapter.sent) != 0 {
				t.Fatalf("%s: the bot said something after being removed: %+v", next, h.adapter.sent)
			}
			if got := h.store.wrote(); len(got) != 1 || got[0] != "SetActive -1001234567890 false" {
				t.Fatalf("%s: writes %v", next, got)
			}
		}
	})

	t.Run("changes that mean nothing", func(t *testing.T) {
		for _, c := range [][2]string{
			{"member", "member"}, {"administrator", "administrator"}, {"restricted", "member"},
			{"member", "restricted"}, {"left", "kicked"}, {"kicked", "left"},
		} {
			h := newHarness(t)
			h.store.group(groupID)
			h.deliver(update(groupChat(), person(ownerID, "en"), c[0], c[1]))
			// left -> kicked and kicked -> left still pause the group; every other pair is silent.
			if len(h.adapter.sent) != 0 {
				t.Errorf("%v: sent %+v", c, h.adapter.sent)
			}
			if len(h.store.ensures) != 0 {
				t.Errorf("%v: the group was recorded", c)
			}
		}
	})

	t.Run("F2: private chats and channels are ignored", func(t *testing.T) {
		for _, kind := range []string{tg.ChatPrivate, tg.ChatChannel} {
			h := newHarness(t)
			chat := tg.Chat{ID: ownerID, Type: kind, Title: "x"}
			h.deliver(update(chat, person(ownerID, "en"), "left", "member"))
			h.deliver(update(chat, person(ownerID, "en"), "member", "kicked"))
			if len(h.adapter.sent) != 0 || len(h.store.ensures) != 0 || len(h.store.wrote()) != 0 {
				t.Fatalf("%s: something happened: sent %+v writes %v", kind, h.adapter.sent, h.store.wrote())
			}
		}
	})

	t.Run("a greeting that cannot be delivered is only logged", func(t *testing.T) {
		h := newHarness(t)
		h.adapter.sendErr = errBoom()
		h.deliver(update(groupChat(), person(ownerID, "en"), "left", "member"))
		if keys := h.adapter.alertKeys(); len(keys) != 0 {
			t.Fatalf("alerts %v", keys)
		}
		if len(h.store.ensures) != 1 {
			t.Fatal("the group was not recorded")
		}
	})

	t.Run("a group that cannot be recorded is an error", func(t *testing.T) {
		h := newHarness(t)
		h.store.errs["Ensure"] = errBoom()
		h.deliver(update(groupChat(), person(ownerID, "en"), "left", "member"))
		if got := h.adapter.alertKeys(); len(got) != 1 || got[0] != "update:my_chat_member" {
			t.Fatalf("alerts %v", got)
		}
		if len(h.adapter.sent) != 0 {
			t.Fatal("greeted a group that was not recorded")
		}
	})

	t.Run("pausing a group that cannot be written is an error", func(t *testing.T) {
		h := newHarness(t)
		h.store.errs["SetActive"] = errBoom()
		h.deliver(update(groupChat(), person(ownerID, "en"), "member", "kicked"))
		if got := h.adapter.alertKeys(); len(got) != 1 || got[0] != "update:my_chat_member" {
			t.Fatalf("alerts %v", got)
		}
	})
}

// A group that became a supergroup (handlers.py, the migration handler): settings,
// messages and quotes follow to the new id; a failure is told to the developers,
// because the group would silently stop being served.
func TestMigration(t *testing.T) {
	migrate := func(h *harness) {
		old := tg.Chat{ID: -4444, Type: tg.ChatGroup, Title: "Old"}
		from := person(ownerID, "en")
		h.message(tg.Message{MessageID: 1, From: &from, Chat: old, MigrateToChatID: groupID})
	}

	t.Run("moves the group", func(t *testing.T) {
		h := newHarness(t)
		h.adapter.chats = map[int64]tg.Chat{groupID: {ID: groupID, Type: tg.ChatSupergroup, Title: "New <title>"}}
		migrate(h)
		if len(h.store.migrations) != 1 {
			t.Fatalf("migrations: %+v", h.store.migrations)
		}
		m := h.store.migrations[0]
		if m.Old != -4444 || m.New != groupID || m.Actor.ID != ownerID || m.Chat.ID != groupID || m.Chat.Title != "New <title>" || m.Chat.Type != tg.ChatSupergroup {
			t.Fatalf("migration %+v", m)
		}
		if keys := h.adapter.alertKeys(); len(keys) != 0 {
			t.Fatalf("alerts %v", keys)
		}
		if len(h.store.ensures) != 0 || len(h.store.saved) != 0 {
			t.Fatal("the service message was handled as an ordinary one")
		}
	})

	t.Run("moved=false is only logged", func(t *testing.T) {
		h := newHarness(t)
		h.store.migrated = false
		migrate(h)
		if keys := h.adapter.alertKeys(); len(keys) != 0 {
			t.Fatalf("alerts %v", keys)
		}
	})

	t.Run("the new chat cannot be read", func(t *testing.T) {
		h := newHarness(t)
		h.adapter.chatErr = errors.New("chat not found")
		migrate(h)
		if len(h.store.migrations) != 0 {
			t.Fatal("moved a group it knew nothing about")
		}
		keys := h.adapter.alertKeys()
		if len(keys) != 1 || keys[0] != "chat-migration:-4444:-1001234567890" {
			t.Fatalf("alerts %v", keys)
		}
		if text := h.adapter.alerts[0].Text; !strings.Contains(text, "getChat") {
			t.Fatalf("the alert does not say which step failed: %q", text)
		}
	})

	t.Run("the move fails", func(t *testing.T) {
		h := newHarness(t)
		h.store.errs["MigrateChatID"] = errBoom()
		migrate(h)
		keys := h.adapter.alertKeys()
		if len(keys) != 1 || keys[0] != "chat-migration:-4444:-1001234567890" {
			t.Fatalf("alerts %v", keys)
		}
		if text := h.adapter.alerts[0].Text; !strings.Contains(text, "move") {
			t.Fatalf("the alert does not say which step failed: %q", text)
		}
	})

	t.Run("a /start that also carries the notice is a /start", func(t *testing.T) {
		h := newHarness(t)
		m := startInGroup(person(ownerID, "en"), 100, "/start")
		m.MigrateToChatID = groupID
		h.message(m)
		if len(h.adapter.sent) != 1 || len(h.store.migrations) != 0 {
			t.Fatal("the order of the handlers changed: /start comes before the migration")
		}
	})
}
