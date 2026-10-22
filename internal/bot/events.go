// SPDX-License-Identifier: Apache-2.0

package bot

import (
	"context"
	"fmt"
	"strconv"

	"github.com/FreshLabDev/tg"

	"github.com/FreshLabDev/quoto/internal/agreement"
	"github.com/FreshLabDev/quoto/internal/domain"
	"github.com/FreshLabDev/quoto/internal/i18n"
)

// reactionKey is the key a reaction is stored under: the emoji, or
// "custom:<id>". A paid reaction has none and is not counted.
func reactionKey(r tg.ReactionType) string {
	switch r.Type {
	case tg.ReactionEmoji:
		return r.Emoji
	case tg.ReactionCustomEmoji:
		if r.CustomEmojiID != "" {
			return "custom:" + r.CustomEmojiID
		}
	}
	return ""
}

// onReaction applies one person's change. Telegram sends the whole list before
// and after, so the change is each emoji's count in the new list minus its count
// in the old one. Who reacted is not recorded, so anonymous reactions count too.
func (b *Bot) onReaction(ctx context.Context, ev *tg.MessageReactionUpdated) error {
	deltas := map[string]int{}
	for _, r := range ev.OldReaction {
		if key := reactionKey(r); key != "" {
			deltas[key]--
		}
	}
	for _, r := range ev.NewReaction {
		if key := reactionKey(r); key != "" {
			deltas[key]++
		}
	}
	for key, delta := range deltas {
		if delta == 0 {
			delete(deltas, key)
		}
	}
	if len(deltas) == 0 {
		return nil
	}
	if err := b.d.Reactions.ApplyDelta(ctx, ev.Chat.ID, ev.MessageID, deltas); err != nil {
		return fmt.Errorf("apply reactions to message %d in chat %d: %w", ev.MessageID, ev.Chat.ID, err)
	}
	return nil
}

// onReactionCount replaces a message's reactions with the totals Telegram sends
// for a chat whose reactions are anonymous (doc 7.2 F1: the count is total_count).
func (b *Bot) onReactionCount(ctx context.Context, ev *tg.MessageReactionCountUpdated) error {
	counts := map[string]int{}
	for _, r := range ev.Reactions {
		if key := reactionKey(r.Type); key != "" {
			counts[key] = r.TotalCount
		}
	}
	if err := b.d.Reactions.SyncAbsolute(ctx, ev.Chat.ID, ev.MessageID, counts); err != nil {
		return fmt.Errorf("sync reactions of message %d in chat %d: %w", ev.MessageID, ev.Chat.ID, err)
	}
	return nil
}

// chatTitle is the group's name for running text. A group has a title unless
// Telegram withheld it; then the handle, then the number.
func chatTitle(c tg.Chat) string {
	switch {
	case c.Title != "":
		return c.Title
	case c.Username != "":
		return "@" + c.Username
	}
	return strconv.FormatInt(c.ID, 10)
}

// onMyChatMember reacts to the bot's own status in a chat changing (doc 7.2 F2: in
// groups and supergroups only). Added: record the group and greet it with the
// agreement. Promoted or demoted: say what that means. Removed: stop posting.
func (b *Bot) onMyChatMember(ctx context.Context, ev *tg.ChatMemberUpdated) error {
	if !isGroupChat(ev.Chat) {
		return nil
	}
	oldStatus, newStatus := ev.OldChatMember.Status, ev.NewChatMember.Status
	// The greeting speaks the language of whoever changed the bot's status, not
	// of the group, which has none yet when the bot is new to it.
	lang := i18n.OrDefault(ev.From.LanguageCode)

	var text string
	var markup *tg.InlineKeyboardMarkup
	switch {
	case !oneOf(oldStatus, "member", "administrator", "restricted") && oneOf(newStatus, "member", "administrator"):
		actor := personFrom(&ev.From)
		g, err := b.d.Groups.Ensure(ctx, actor, chatInfo(ev.Chat))
		if err != nil {
			return fmt.Errorf("record group %d: %w", ev.Chat.ID, err)
		}
		// The time shown is the one this group will really get (doc 7.2 F9): its
		// row has just been created, with a minute spread over the next ten.
		hour, minute := g.QuoteTime()
		text = i18n.T(lang, "chat_member.added",
			"chat_title", domain.EscapeHTML(chatTitle(ev.Chat)),
			"time", fmt.Sprintf("%02d:%02d", hour, minute))
		if newStatus == "member" {
			text += "\n\n" + i18n.T(lang, "chat_member.need_admin")
		}
		text += "\n\n" + i18n.T(lang, "agreement.welcome_note")
		markup = agreement.WelcomeKeyboard(lang)
		b.log.Info("bot added to a group", "chat", ev.Chat.ID, "status", newStatus)

	case newStatus == "administrator" && oneOf(oldStatus, "member", "restricted"):
		text = i18n.T(lang, "chat_member.admin_granted")
		b.log.Info("bot made administrator", "chat", ev.Chat.ID)

	case oneOf(newStatus, "member", "restricted") && oldStatus == "administrator":
		text = i18n.T(lang, "chat_member.need_admin")
		b.log.Info("bot lost administrator rights", "chat", ev.Chat.ID)

	case oneOf(newStatus, "left", "kicked"):
		if _, err := b.d.Groups.SetActive(ctx, ev.Chat.ID, false); err != nil {
			return fmt.Errorf("pause group %d: %w", ev.Chat.ID, err)
		}
		b.log.Info("group paused: the bot was removed", "chat", ev.Chat.ID, "status", newStatus)
		return nil
	}

	if text == "" {
		return nil
	}
	// A greeting that cannot be delivered is not worth an alert: the bot may
	// have been added and removed again before it could speak.
	if _, err := b.d.Adapter.Send(ctx, sendTo(ev.Chat.ID, 0, text, markup)); err != nil {
		b.log.Warn("could not send the status message", "chat", ev.Chat.ID, "error", err)
	}
	return nil
}

func oneOf(s string, options ...string) bool {
	for _, o := range options {
		if s == o {
			return true
		}
	}
	return false
}

// onMigrated follows a group that became a supergroup: its settings, messages and
// quotes move to the new chat id. A failure is logged and sent to the developers,
// because the group would otherwise silently stop being served.
func (b *Bot) onMigrated(ctx context.Context, m *tg.Message) error {
	oldID, newID := m.Chat.ID, m.MigrateToChatID
	key := fmt.Sprintf("chat-migration:%d:%d", oldID, newID)
	fail := func(step string, err error) error {
		b.log.Error("chat migration failed", "from", oldID, "to", newID, "step", step, "error", err)
		b.d.Adapter.Alert(ctx, key, fmt.Sprintf("Chat migration failed: %d -> %d: %s: %v", oldID, newID, step, err))
		return nil
	}
	chat, err := b.d.Adapter.GetChat(ctx, newID)
	if err != nil {
		return fail("getChat", err)
	}
	moved, err := b.d.Groups.MigrateChatID(ctx, oldID, newID, personFrom(m.From), chatInfo(chat))
	if err != nil {
		return fail("move", err)
	}
	b.log.Info("group migrated to a supergroup", "from", oldID, "to", newID, "moved", moved)
	return nil
}
