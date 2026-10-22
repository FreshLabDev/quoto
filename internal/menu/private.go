// SPDX-License-Identifier: Apache-2.0

package menu

import (
	"github.com/FreshLabDev/tg"

	"github.com/FreshLabDev/quoto/internal/domain"
	"github.com/FreshLabDev/quoto/internal/i18n"
)

// PrivateInput is what the private panel draws. Lang is the person's language
// as core resolved it, and LangSource says who chose it.
type PrivateInput struct {
	Owner       int64
	Lang        string
	LangSource  domain.LangSource
	BotUsername string
	// Version is the running build, for the About card.
	Version string
	Section Section
}

func privateSourceKey(source domain.LangSource) string {
	if source == domain.SourceManual {
		return "settings.private.language_source_manual"
	}
	return "settings.private.language_source_telegram"
}

// PrivatePanel draws one screen of the private hub: home, language or about.
// Any other section draws home.
func PrivatePanel(in PrivateInput) (string, *tg.InlineKeyboardMarkup) {
	lang := in.Lang
	source := i18n.T(lang, privateSourceKey(in.LangSource))

	switch in.Section {
	case SectionAbout:
		return AboutText(lang, in.Version), markup([][]tg.InlineKeyboardButton{NavRow(in.Owner, ScopePrivate, lang)})

	case SectionLanguage:
		// Which language is current is on the buttons, so the only thing left
		// to say is where that language came from.
		text := Panel(
			i18n.T(lang, "settings.private.language_title"),
			i18n.T(lang, "settings.private.language_hint"),
			source,
		)
		rows := languageButtons(in.Owner, ScopePrivate, ActionSetPrivateLanguage, lang)
		rows = append(rows,
			[]tg.InlineKeyboardButton{button(
				i18n.T(lang, "settings.private.telegram_language"),
				Data(in.Owner, ScopePrivate, ActionAutoPrivateLanguage, ""),
			)},
			NavRow(in.Owner, ScopePrivate, lang),
		)
		return text, markup(rows)
	}

	text := Panel(
		i18n.T(lang, "menu.private.title"),
		i18n.T(lang, "menu.private.body"),
		i18n.T(lang, "menu.private.language", "language_name", i18n.Name(lang)),
		source,
	)
	return text, markup([][]tg.InlineKeyboardButton{
		{button(i18n.T(lang, "settings.button.language"), Data(in.Owner, ScopePrivate, ActionPrivateLanguage, ""))},
		// Quoto does nothing in a direct chat, so the one thing this screen leads
		// to is putting it in a group.
		{{
			Text:  i18n.T(lang, "private.add_to_group"),
			URL:   "https://t.me/" + in.BotUsername + "?startgroup=new",
			Style: tg.StylePrimary,
		}},
		documentRow(in.Owner, ScopePrivate, lang),
	})
}
