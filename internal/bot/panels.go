// SPDX-License-Identifier: Apache-2.0

package bot

import (
	"context"
	"fmt"
	"strings"
	"unicode"

	"github.com/FreshLabDev/tg"

	"github.com/FreshLabDev/quoto/internal/deeplink"
	"github.com/FreshLabDev/quoto/internal/domain"
	"github.com/FreshLabDev/quoto/internal/i18n"
	"github.com/FreshLabDev/quoto/internal/menu"
	"github.com/FreshLabDev/quoto/internal/telegram"
)

func sendTo(chatID int64, thread int, text string, markup *tg.InlineKeyboardMarkup) telegram.SendParams {
	return telegram.SendParams{ChatID: chatID, ThreadID: thread, Text: text, Markup: markup}
}

// threadOf is the message_thread_id to answer in. Telegram also sets the field on
// a reply chain in an ordinary supergroup, where sending it back is a 400, so it
// is used only for a message that really is in a forum topic.
func threadOf(m *tg.Message) int {
	if m.IsTopicMessage {
		return m.MessageThreadID
	}
	return 0
}

// say posts text in the chat of m, as a plain message and not as a reply.
func (b *Bot) say(ctx context.Context, m *tg.Message, text string, markup *tg.InlineKeyboardMarkup) (tg.Message, error) {
	return b.d.Adapter.Send(ctx, sendTo(m.Chat.ID, threadOf(m), text, markup))
}

// userLanguage is the language to speak to a person in private, and who chose it:
// core's claim if any bot made one, else the language of their Telegram client
// with no source. A claim that cannot be rendered shows as English. A failed read
// falls back to the client's language; a panel in the wrong language is better
// than none.
func (b *Bot) userLanguage(ctx context.Context, userID int64, clientLang string) (string, domain.LangSource) {
	claim, err := b.d.Core.LanguageClaim(ctx, domain.ScopeUser, userID)
	if err != nil {
		b.log.Warn("language claim not read", "user", userID, "error", err)
		return i18n.OrDefault(clientLang), ""
	}
	if claim.Set() {
		return i18n.OrDefault(claim.Language), claim.Source
	}
	return i18n.OrDefault(clientLang), ""
}

// isAdmin asks Telegram, on every use: rights change, and nothing is cached.
func (b *Bot) isAdmin(ctx context.Context, chatID, userID int64) (bool, error) {
	member, err := b.d.TG.GetChatMember(ctx, chatID, userID)
	if err != nil {
		return false, err
	}
	return member.Status == "administrator" || member.Status == "creator", nil
}

// group reads a group for a panel: records the person and the chat, then reads the
// row with the group's name and language from core.
func (b *Bot) group(ctx context.Context, actor *tg.User, chat tg.Chat) (domain.GroupSettings, error) {
	if _, err := b.d.Groups.Ensure(ctx, personFrom(actor), chatInfo(chat)); err != nil {
		return domain.GroupSettings{}, fmt.Errorf("record group %d: %w", chat.ID, err)
	}
	return b.groupByID(ctx, chat.ID)
}

func (b *Bot) groupByID(ctx context.Context, chatID int64) (domain.GroupSettings, error) {
	g, ok, err := b.d.Groups.Get(ctx, chatID)
	if err != nil {
		return domain.GroupSettings{}, fmt.Errorf("read group %d: %w", chatID, err)
	}
	if !ok {
		return domain.GroupSettings{}, fmt.Errorf("group %d has no settings row", chatID)
	}
	return g, nil
}

// onPrivate answers any message in a private chat: with the card of a quote if
// the person came through a quote link, else with the panel.
func (b *Bot) onPrivate(ctx context.Context, m *tg.Message) error {
	if m.From == nil {
		return nil
	}
	if err := b.d.Core.TouchUser(ctx, personFrom(m.From)); err != nil {
		return fmt.Errorf("record user %d: %w", m.From.ID, err)
	}
	lang, source := b.userLanguage(ctx, m.From.ID, m.From.LanguageCode)

	if cmd, ok := b.isStart(m); ok && strings.HasPrefix(cmd.args, "quote_") {
		return b.sendQuoteCard(ctx, m, lang, cmd.args)
	}
	// Any other payload, "settings" among them, and every other message, opens the panel.
	text, markup := menu.PrivatePanel(menu.PrivateInput{
		Owner: m.From.ID, Lang: lang, LangSource: source,
		BotUsername: b.cfg.BotUsername, Version: b.cfg.Version, Section: menu.SectionHome,
	})
	if _, err := b.say(ctx, m, text, markup); err != nil {
		return fmt.Errorf("send the panel to %d: %w", m.Chat.ID, err)
	}
	return nil
}

// sendQuoteCard answers /start quote_<id>_<signature>. A link from before 0.10.2
// has no signature and shows the quote only to a member of its group. A missing
// quote and one the reader may not see get the same answer, so the reply does not
// tell whether a quote exists.
func (b *Bot) sendQuoteCard(ctx context.Context, m *tg.Message, lang, payload string) error {
	id, ok := b.d.Signer.Parse(payload)
	legacy := false
	if !ok {
		id, ok = deeplink.ParseLegacy(payload)
		legacy = ok
	}
	if !ok {
		// Python's isdigit accepts digits of other scripts. Those payloads have the
		// shape of a quote link but name no quote we could have issued, so they get
		// the answer of a quote that does not exist, not that of a broken link.
		key := "private.invalid_quote_link"
		if quoteIDShape(payload) {
			key = "private.quote_not_found"
		}
		_, err := b.say(ctx, m, i18n.T(lang, key), nil)
		return err
	}
	detail, err := b.d.Quotes.Detail(ctx, id, true)
	if err != nil {
		return fmt.Errorf("read quote %d: %w", id, err)
	}
	if detail != nil && legacy && !b.canViewLegacy(ctx, detail.GroupID, m.From.ID) {
		detail = nil
	}
	if detail == nil {
		_, err := b.say(ctx, m, i18n.T(lang, "private.quote_not_found"), nil)
		return err
	}
	if _, err := b.say(ctx, m, quoteCard(lang, detail), nil); err != nil {
		return fmt.Errorf("send the card of quote %d: %w", id, err)
	}
	return nil
}

// quoteIDShape reports whether a payload that failed to parse still has the shape
// of a quote link whose id the bot could not have issued: quote_<id> with digits
// of any script, or a signed form with at least one non-ASCII digit. A signed
// link with plain digits and a wrong signature is not of this shape; it is forged
// or damaged.
func quoteIDShape(payload string) bool {
	parts := strings.Split(payload, "_")
	if (len(parts) != 2 && len(parts) != 3) || parts[0] != "quote" || parts[1] == "" {
		return false
	}
	foreign := false
	for _, r := range parts[1] {
		if !unicode.IsDigit(r) && !unicode.Is(unicode.No, r) {
			return false
		}
		if r > unicode.MaxASCII {
			foreign = true
		}
	}
	return len(parts) == 2 || foreign
}

// canViewLegacy: is the person in the quote's group? Anything but a clear yes is a
// no, a failed lookup included.
func (b *Bot) canViewLegacy(ctx context.Context, chatID, userID int64) bool {
	if chatID == 0 || userID == 0 {
		return false
	}
	member, err := b.d.TG.GetChatMember(ctx, chatID, userID)
	if err != nil {
		return false
	}
	switch member.Status {
	case "creator", "administrator", "member":
		return true
	case "restricted":
		return member.IsMember
	}
	return false
}

// sendGroupPanel answers /start in a group with the group's panel, in the group's
// language, and remembers which message opened it so Close can remove both. If
// Telegram cannot say whether the person is an admin, the panel is the member's.
func (b *Bot) sendGroupPanel(ctx context.Context, m *tg.Message) error {
	if m.From == nil {
		return nil
	}
	g, err := b.group(ctx, m.From, m.Chat)
	if err != nil {
		return err
	}
	admin, err := b.isAdmin(ctx, m.Chat.ID, m.From.ID)
	if err != nil {
		b.log.Warn("could not check the rights of the person who opened the panel", "chat", m.Chat.ID, "user", m.From.ID, "error", err)
	}
	text, markup := menu.GroupPanel(menu.GroupInputFor(m.From.ID, g, admin, b.cfg.Version))
	sent, err := b.say(ctx, m, text, markup)
	if err != nil {
		return fmt.Errorf("send the panel to %d: %w", m.Chat.ID, err)
	}
	if isCommandMessage(m) {
		b.panels.register(msgRef{m.Chat.ID, sent.MessageID}, msgRef{m.Chat.ID, m.MessageID}, b.now())
	}
	return nil
}

// onMessage routes a message in the order the handlers were registered: a private
// chat gets the panel, /start in a group gets the group's panel, a migration
// notice moves the group, anything else in a group is collected.
func (b *Bot) onMessage(ctx context.Context, m *tg.Message) error {
	if m.Chat.Type == tg.ChatPrivate {
		return b.onPrivate(ctx, m)
	}
	if isGroupChat(m.Chat) {
		if _, ok := b.isStart(m); ok {
			return b.sendGroupPanel(ctx, m)
		}
	}
	if m.MigrateToChatID != 0 {
		return b.onMigrated(ctx, m)
	}
	if isGroupChat(m.Chat) {
		return b.onGroupMessage(ctx, m)
	}
	return nil
}
