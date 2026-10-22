// SPDX-License-Identifier: Apache-2.0

package bot

import (
	"context"
	"fmt"
	"regexp"
	"time"

	"github.com/FreshLabDev/tg"

	"github.com/FreshLabDev/quoto/internal/domain"
)

// pySpace is Python's \s for str patterns: Go's \s is ASCII only and leaves out
// \v and the Unicode spaces that Python counts.
const pySpace = `\t\n\v\f\r \x{1c}-\x{1f}\x{85}\x{a0}\x{1680}\x{2000}-\x{200a}\x{2028}\x{2029}\x{202f}\x{205f}\x{3000}`

// linkOnly matches a text that is nothing but a link: a URL with a scheme or
// "www.", or a bare domain with an optional path. Ported from Python's
// _LINK_ONLY_RE. It also matches "readme.md" and "hi.there", as the original did,
// and those are not collected either. İ and ı are in the letter classes because
// Python's IGNORECASE lets [a-z] match them; Go's folding already adds ſ and K.
// The second branch of the Python check (a lone link entity) never matched and
// is not ported.
var linkOnly = regexp.MustCompile(`(?i)^(?:(?:https?://|tg://|www\.)[^` + pySpace + `]+|(?:[a-z0-9\x{130}\x{131}-]+\.)+[a-z\x{130}\x{131}]{2,}(?:/[^` + pySpace + `]*)?)$`)

// isLinkOnly reports a text message that is only a link. A message with a file
// never is.
func isLinkOnly(m *tg.Message) bool {
	if sourceFromMessage(m) != nil {
		return false
	}
	text := domain.PyStrip(m.Text)
	return text != "" && linkOnly.MatchString(text)
}

// collectable applies the filters every collected message passes: a person (not a
// bot, which includes the anonymous-admin and linked-channel stand-ins), not a
// command, not a bare link.
func collectable(m *tg.Message) bool {
	if m.From == nil || m.From.IsBot {
		return false
	}
	return !isCommandMessage(m) && !isLinkOnly(m)
}

const (
	saveAttempts  = 3
	saveRetryBase = 500 * time.Millisecond
)

// onGroupMessage stores a message of the day. Everything that passes the filters
// and has text, a caption or a file is stored, whether or not the group has
// accepted the agreement. The group is recorded either way, as a join or a pin
// is a sign of life too.
func (b *Bot) onGroupMessage(ctx context.Context, m *tg.Message) error {
	if !collectable(m) {
		return nil
	}
	g, err := b.d.Groups.Ensure(ctx, personFrom(m.From), chatInfo(m.Chat))
	if err != nil {
		return fmt.Errorf("record group %d: %w", m.Chat.ID, err)
	}
	if !hasContent(m, sourceFromMessage(m)) {
		return nil
	}
	nm := newMessage(m, b.now().UTC())
	id, inserted, ok := b.save(ctx, nm)
	if !ok || !inserted || nm.Media == nil {
		return nil
	}
	if b.cfg.MediaAnalysisEnabled && g.MediaAnalysisEnabled() && b.d.Media != nil {
		b.d.Media.Submit(id)
	}
	return nil
}

// save stores a message, trying three times with 0.5 s and 1 s between them. A
// message that cannot be stored is lost for the day, so after the last failure a
// developer is told, once per message. inserted is false for a message Telegram
// delivered twice.
func (b *Bot) save(ctx context.Context, nm domain.NewMessage) (id int64, inserted, ok bool) {
	var err error
	for attempt := 0; attempt < saveAttempts; attempt++ {
		id, inserted, err = b.d.Messages.Save(ctx, nm)
		if err == nil {
			return id, inserted, true
		}
		if attempt == saveAttempts-1 {
			break
		}
		delay := saveRetryBase << attempt
		b.log.Warn("message not saved, retrying", "chat", nm.ChatID, "message", nm.MessageID, "retry_in", delay.String(), "error", err)
		if b.sleep(ctx, delay) != nil {
			return 0, false, false
		}
	}
	b.log.Error("message not saved", "chat", nm.ChatID, "message", nm.MessageID, "attempts", saveAttempts, "error", err)
	b.d.Adapter.Alert(ctx, fmt.Sprintf("message-save:%d:%d", nm.ChatID, nm.MessageID),
		fmt.Sprintf("Message save failed for chat %d, message %d: %v", nm.ChatID, nm.MessageID, err))
	return 0, false, false
}

// onEditedMessage applies an edit to a stored message. An edit that makes a
// message a bare link is ignored, so the stored version stays.
func (b *Bot) onEditedMessage(ctx context.Context, m *tg.Message) error {
	if !isGroupChat(m.Chat) || !collectable(m) || !hasContent(m, sourceFromMessage(m)) {
		return nil
	}
	if err := b.d.Messages.Edit(ctx, messageEdit(m)); err != nil {
		return fmt.Errorf("edit message %d in chat %d: %w", m.MessageID, m.Chat.ID, err)
	}
	return nil
}
