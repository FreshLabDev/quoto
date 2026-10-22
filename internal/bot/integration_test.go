// SPDX-License-Identifier: Apache-2.0
//go:build integration

package bot

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/FreshLabDev/tg"

	"github.com/FreshLabDev/quoto/internal/db"
	"github.com/FreshLabDev/quoto/internal/db/dbtest"
	"github.com/FreshLabDev/quoto/internal/domain"
	"github.com/FreshLabDev/quoto/internal/i18n"
)

// realStores puts the handlers on a migrated database instead of the fakes. The
// Bot API stays fake: what these tests check is that what the handlers ask of the
// stores is what the stores take, and that it persists.
func realStores(t *testing.T) (*harness, *db.Store) {
	t.Helper()
	s := dbtest.New(t)
	h := newHarness(t)
	h.bot.d.Core, h.bot.d.Groups, h.bot.d.Messages = s, s, s
	h.bot.d.Reactions, h.bot.d.Quotes, h.bot.d.Stats = s, s, s
	return h, s
}

// window around the timestamp the tests give their messages (1700000000).
func testWindow() domain.Window {
	return domain.Window{
		QuoteDay: time.Date(2023, 11, 14, 0, 0, 0, 0, time.UTC),
		Start:    time.Date(2023, 11, 14, 0, 0, 0, 0, time.UTC),
		End:      time.Date(2023, 11, 15, 0, 0, 0, 0, time.UTC),
	}
}

// The life of a group, handler by handler, on the real stores: the bot is added,
// a panel opens, messages and reactions are collected and edited, settings and
// language change and persist, the agreement is accepted, and the bot is
// removed. The test follows the order a group goes through.
func TestGroupLifecycleOnRealStores(t *testing.T) {
	h, s := realStores(t)
	ctx := t.Context()
	admin := person(ownerID, "ru")

	// The bot is added.
	h.deliver(tg.Update{UpdateID: 1, MyChatMember: &tg.ChatMemberUpdated{
		Chat: groupChat(), From: admin,
		OldChatMember: tg.ChatMember{Status: "left"}, NewChatMember: tg.ChatMember{Status: "administrator"},
	}})
	g, ok, err := s.Get(ctx, groupID)
	if err != nil || !ok || !g.IsActive || g.Name != "Test group" {
		t.Fatalf("group after joining: %+v ok=%v err=%v", g, ok, err)
	}
	if got := h.onlySent(groupID); got.Markup == nil {
		t.Fatal("the greeting has no agreement button")
	}

	// A panel opens, in the group's language once it has one.
	h.api.setMember(ownerID, "administrator")
	h.message(startInGroup(admin, 100, "/start"))
	if sent := h.adapter.sentTo(groupID); len(sent) != 2 {
		t.Fatalf("%d messages to the group", len(sent))
	}

	// Messages are collected, edited, and reacted to.
	h.message(groupText(admin, 5, "the first line"))
	h.deliver(tg.Update{UpdateID: 2, EditedMessage: &tg.Message{
		MessageID: 5, Date: 1700000000, From: &admin, Chat: groupChat(), Text: "the edited line",
	}})
	h.deliver(tg.Update{UpdateID: 3, MessageReaction: &tg.MessageReactionUpdated{
		Chat: groupChat(), MessageID: 5, NewReaction: []tg.ReactionType{emoji("🔥"), emoji("❤️")},
	}})
	h.deliver(tg.Update{UpdateID: 4, MessageReaction: &tg.MessageReactionUpdated{
		Chat: groupChat(), MessageID: 5, OldReaction: []tg.ReactionType{emoji("🔥"), emoji("❤️")}, NewReaction: []tg.ReactionType{emoji("🔥")},
	}})
	msgs, err := s.ListWindow(ctx, groupID, testWindow())
	if err != nil || len(msgs) != 1 {
		t.Fatalf("window: %+v err=%v", msgs, err)
	}
	m := msgs[0]
	if m.Text != "the edited line" || m.UserID != ownerID || m.ContentType != "text" || m.MessageID != 5 {
		t.Fatalf("stored message %+v", m)
	}
	if len(m.Reactions) != 1 || m.Reactions[0].Emoji != "🔥" || m.Reactions[0].Count != 1 {
		t.Fatalf("reactions %+v: a count that reaches zero must leave no row", m.Reactions)
	}

	// The absolute count replaces what was there (F1).
	h.deliver(tg.Update{UpdateID: 5, MessageReactionCount: &tg.MessageReactionCountUpdated{
		Chat: groupChat(), MessageID: 5, Reactions: []tg.ReactionCount{{Type: emoji("😂"), TotalCount: 4}},
	}})
	msgs, _ = s.ListWindow(ctx, groupID, testWindow())
	if len(msgs[0].Reactions) != 1 || msgs[0].Reactions[0].Emoji != "😂" || msgs[0].Reactions[0].Count != 4 {
		t.Fatalf("reactions after a sync %+v", msgs[0].Reactions)
	}

	// Settings.
	panel := panelMessage()
	press := func(data string) {
		h.clock.advance(time.Second) // past the throttle window
		h.press(admin, panel, data)
	}
	press("menu:777:g:toggle:pin")
	press("menu:777:g:setlang:uk")
	press("menu:777:g:settz:Europe/Berlin")
	press("menu:777:g:timeadj:60")
	press("menu:777:g:minadj:5")
	press("agree:a:uk:g:777")
	h.bot.wg.Wait()

	g, _, err = s.Get(ctx, groupID)
	if err != nil {
		t.Fatal(err)
	}
	if g.PinEnabled() {
		t.Error("the pin switch did not persist")
	}
	if g.Language != "uk" || g.LanguageSource != domain.SourceManual {
		t.Errorf("language %q from %q", g.Language, g.LanguageSource)
	}
	if g.TimezoneName() != "Europe/Berlin" {
		t.Errorf("time zone %q", g.TimezoneName())
	}
	if hour, _ := g.QuoteTime(); hour != 22 {
		t.Errorf("cutoff hour %d, want 22 after +1:00 from the default 21", hour)
	}
	if g.MinMessagesEffective() != domain.DefaultMinMessages+5 {
		t.Errorf("minimum %d", g.MinMessagesEffective())
	}
	if !g.AgreementAccepted() || g.AgreementLanguage == nil || *g.AgreementLanguage != "uk" || g.AgreementAcceptedBy == nil || *g.AgreementAcceptedBy != ownerID {
		t.Errorf("agreement not recorded: %+v", g)
	}
	if got := h.answers(); len(got) != 6 {
		t.Errorf("%d answers for 6 presses", len(got))
	}
	if keys := h.adapter.alertKeys(); len(keys) != 0 {
		t.Errorf("alerts %v", keys)
	}

	// The panel now speaks the language the group chose.
	h.message(startInGroup(admin, 101, "/start"))
	last := h.adapter.sentTo(groupID)
	if len(last) != 3 || !containsStr(labelsOf(last[2].Markup), i18n.T("uk", "menu.button.close")) {
		t.Errorf("the panel did not switch to the group's language: %v", labelsOf(last[len(last)-1].Markup))
	}

	// Statistics read what the quote store has (nothing yet).
	press("menu:777:g:chatstats")
	if text := h.lastEdit().text(); text == "" {
		t.Error("empty stats screen")
	}

	// The bot is removed.
	h.deliver(tg.Update{UpdateID: 9, MyChatMember: &tg.ChatMemberUpdated{
		Chat: groupChat(), From: admin,
		OldChatMember: tg.ChatMember{Status: "administrator"}, NewChatMember: tg.ChatMember{Status: "kicked"},
	}})
	g, _, _ = s.Get(ctx, groupID)
	if g.IsActive {
		t.Fatal("the group is still active after the bot was removed")
	}
	// Writing again wakes it up.
	h.message(groupText(admin, 6, "back again"))
	g, _, _ = s.Get(ctx, groupID)
	if !g.IsActive {
		t.Fatal("a new message did not wake the group")
	}
}

// A group that moves to a supergroup id keeps its settings and messages.
func TestMigrationOnRealStores(t *testing.T) {
	h, s := realStores(t)
	ctx := t.Context()
	admin := person(ownerID, "en")
	oldChat := tg.Chat{ID: -4444, Type: tg.ChatGroup, Title: "Old"}

	m := groupText(admin, 5, "kept")
	m.Chat = oldChat
	h.message(m)
	h.adapter.chats = map[int64]tg.Chat{groupID: {ID: groupID, Type: tg.ChatSupergroup, Title: "New"}}
	h.message(tg.Message{MessageID: 6, From: &admin, Chat: oldChat, MigrateToChatID: groupID})

	if keys := h.adapter.alertKeys(); len(keys) != 0 {
		t.Fatalf("alerts %v: %v", keys, h.adapter.alerts)
	}
	g, ok, err := s.Get(ctx, groupID)
	if err != nil || !ok || g.Name != "New" {
		t.Fatalf("new group %+v ok=%v err=%v", g, ok, err)
	}
	if _, ok, _ := s.Get(ctx, oldChat.ID); ok {
		t.Fatal("the old id still has settings")
	}
	msgs, err := s.ListWindow(ctx, groupID, testWindow())
	if err != nil || len(msgs) != 1 || msgs[0].Text != "kept" {
		t.Fatalf("messages after the move: %+v err=%v", msgs, err)
	}
}

// Private panels on the real core: the language a person chooses persists, the
// Telegram-language button withdraws only Quoto's claim, and the card of a
// published quote is the one the store returns, visible only when published.
func TestPrivateAndQuoteLinksOnRealStores(t *testing.T) {
	h, s := realStores(t)
	ctx := t.Context()
	admin := person(ownerID, "ru")

	// A person is recorded when they open the bot, in the client's language.
	h.message(privateMessage(admin, "/start"))
	if claim, err := s.LanguageClaim(ctx, domain.ScopeUser, ownerID); err != nil {
		t.Fatal(err)
	} else if claim.Language != "ru" {
		t.Fatalf("claim after touching: %+v", claim)
	}

	h.press(admin, privatePanel(), "menu:777:p:setplang:de")
	if claim, _ := s.LanguageClaim(ctx, domain.ScopeUser, ownerID); claim.Language != "de" || claim.Source != domain.SourceManual {
		t.Fatalf("claim after choosing: %+v", claim)
	}
	h.clock.advance(time.Second)
	h.press(admin, privatePanel(), "menu:777:p:autoplang")
	if claim, _ := s.LanguageClaim(ctx, domain.ScopeUser, ownerID); claim.Source == domain.SourceManual {
		t.Fatalf("the manual choice survived the Telegram-language button: %+v", claim)
	}

	// Quote cards.
	if _, err := s.Ensure(ctx, personFrom(&admin), groupChat2()); err != nil {
		t.Fatal(err)
	}
	day := time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)
	newQuote := func(status string, d time.Time) domain.Quote {
		q, _, err := s.Create(ctx, domain.NewQuote{
			GroupID: groupID, AuthorID: ownerID, Text: "A <quote>", Score: 0.87, AIScore: 0.9, MessageID: 321,
			ContentType: "text", AIModel: "anthropic/claude-sonnet-5-5", QuoteDay: d,
			WindowStart: d.Add(-24 * time.Hour), WindowEnd: d, DecisionStatus: status, DecisionReason: "Funny",
		})
		if err != nil {
			t.Fatal(err)
		}
		return q
	}
	published := newQuote(domain.StatusPublished, day)
	hidden := newQuote(domain.StatusSkippedBoring, day.Add(24*time.Hour))

	h.api.setMember(ownerID, "member")
	reader := person(ownerID, "en")
	if err := s.ClearLanguage(ctx, domain.ScopeUser, ownerID); err != nil {
		t.Fatal(err)
	}
	h.adapter.sent = nil
	h.startQuote(reader, h.signer.Payload(published.ID))
	h.startQuote(reader, h.signer.Payload(hidden.ID))
	h.startQuote(reader, "quote_"+strconv.FormatInt(published.ID, 10)) // legacy, and the reader is a member
	h.api.setMember(ownerID, "left")
	h.startQuote(reader, "quote_"+strconv.FormatInt(published.ID, 10)) // legacy, and the reader is not
	sent := h.adapter.sentTo(ownerID)
	if len(sent) != 4 {
		t.Fatalf("%d replies", len(sent))
	}
	notFound := i18n.T("en", "private.quote_not_found")
	wantCard := func(i int) {
		t.Helper()
		text := sent[i].Text
		if !strings.Contains(text, "Day #"+strconv.FormatInt(published.ID, 10)) || !strings.Contains(text, "A &lt;quote&gt;") || !strings.Contains(text, "Test group") {
			t.Errorf("reply %d is not the card:\n%s", i, text)
		}
	}
	wantCard(0)
	if sent[1].Text != notFound {
		t.Errorf("an unpublished quote: %q", sent[1].Text)
	}
	wantCard(2)
	if sent[3].Text != notFound {
		t.Errorf("a legacy link for a non-member: %q", sent[3].Text)
	}
}

func groupChat2() domain.ChatInfo {
	return domain.ChatInfo{ID: groupID, Type: tg.ChatSupergroup, Title: "Test group"}
}
