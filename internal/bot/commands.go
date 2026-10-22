// SPDX-License-Identifier: Apache-2.0

package bot

import (
	"context"

	"github.com/FreshLabDev/tg"

	"github.com/FreshLabDev/quoto/internal/i18n"
)

// registerCommands publishes /start, the only command, per chat type and per
// interface language. It opens a different screen in a private chat than in a
// group, so the two scopes describe it differently. Telegram picks the list that
// matches the client's language; the lists without a language are the English
// fallback for every other client, and the default-scope list overwrites what an
// older version left behind for chat types the scoped lists do not cover.
//
// That is 1 + 2 x 17 = 35 calls. One failing call is logged and the rest go on.
func (b *Bot) registerCommands(ctx context.Context) {
	languages := append([]string{""}, i18n.Codes()...)
	for _, lang := range languages {
		text := lang
		if text == "" {
			text = i18n.DefaultLang
		}
		private := []tg.BotCommand{{Command: "start", Description: i18n.T(text, "command.start_private")}}
		group := []tg.BotCommand{{Command: "start", Description: i18n.T(text, "command.start_group")}}

		if lang == "" {
			b.setCommands(ctx, private, &tg.BotCommandScope{Type: "default"})
		}
		b.setCommands(ctx, private, &tg.BotCommandScope{Type: "all_private_chats", LanguageCode: lang})
		b.setCommands(ctx, group, &tg.BotCommandScope{Type: "all_group_chats", LanguageCode: lang})
		if ctx.Err() != nil {
			return
		}
	}
}

func (b *Bot) setCommands(ctx context.Context, commands []tg.BotCommand, scope *tg.BotCommandScope) {
	if err := b.d.TG.SetMyCommandsForScope(ctx, commands, scope); err != nil && ctx.Err() == nil {
		language := scope.LanguageCode
		if language == "" {
			language = "default"
		}
		b.log.Warn("could not publish commands", "scope", scope.Type, "language", language, "error", err)
	}
}
