// SPDX-License-Identifier: Apache-2.0

package menu

import (
	"fmt"
	"strconv"

	"github.com/FreshLabDev/tg"

	"github.com/FreshLabDev/quoto/internal/domain"
	"github.com/FreshLabDev/quoto/internal/i18n"
)

// Stats tabs.
const (
	StatsUser = "user"
	StatsChat = "chat"
)

// GroupInput is what the group panel draws. The panel speaks the group's
// language, so Lang and GroupLang are the same in practice; they stay apart
// because one picks the words and the other picks the highlighted grid cell.
type GroupInput struct {
	Owner           int64
	Lang            string
	GroupLang       string
	GroupLangSource domain.LangSource
	IsAdmin         bool

	QuoteTime    string // "21:00", in the group's zone
	MinMessages  int
	Timezone     string
	BoringNotice bool
	Pin          bool
	QuoteContext bool
	// MediaAnalysis is the group's own switch, the one the toggle changes. The
	// global MEDIA_ANALYSIS_ENABLED is not folded in: with it off the toggle
	// still reads as what the admins chose.
	MediaAnalysis bool

	Section Section
	// StatsText is the body of the stats screen, built by ChatStatsText or
	// UserStatsText; StatsView says which tab is open.
	StatsText string
	StatsView string
	// Version is the running build, for the About card.
	Version string
}

// GroupInputFor reads a group's row into the panel input. g comes from
// Groups.Get, so Language is the raw claim of any bot; the panel language is
// that claim made renderable.
func GroupInputFor(owner int64, g domain.GroupSettings, isAdmin bool, version string) GroupInput {
	lang := i18n.OrDefault(g.Language)
	hour, minute := g.QuoteTime()
	return GroupInput{
		Owner:           owner,
		Lang:            lang,
		GroupLang:       lang,
		GroupLangSource: g.LanguageSource,
		IsAdmin:         isAdmin,
		QuoteTime:       fmt.Sprintf("%02d:%02d", hour, minute),
		MinMessages:     g.MinMessagesEffective(),
		Timezone:        g.TimezoneName(),
		BoringNotice:    g.BoringNoticeEnabled(),
		Pin:             g.PinEnabled(),
		QuoteContext:    g.ContextEnabled(),
		MediaAnalysis:   g.MediaAnalysisEnabled(),
		Version:         version,
	}
}

func groupSourceKey(source domain.LangSource) string {
	switch source {
	case domain.SourceManual:
		return "menu.language.source_manual"
	case domain.SourceAuto:
		return "menu.language.source_auto"
	}
	return "menu.language.source_default"
}

// dashboard is what the home screen's own buttons cannot say: how Quoto is set
// up here. The buttons open sections and do not carry these values, so reading
// them off is not the body repeating the keyboard.
func dashboard(in GroupInput) []string {
	lang := in.Lang
	return []string{
		i18n.T(lang, "menu.group.language_line", "language_name", i18n.Name(in.GroupLang)),
		i18n.T(lang, "menu.group.schedule_line", "time", in.QuoteTime, "min_messages", strconv.Itoa(in.MinMessages)),
		toggleLine(i18n.T(lang, "settings.group.context.short"), in.QuoteContext),
		toggleLine(i18n.T(lang, "settings.group.boring_notice.short"), in.BoringNotice),
		toggleLine(i18n.T(lang, "settings.group.pin.short"), in.Pin),
		toggleLine(i18n.T(lang, "settings.group.media.short"), in.MediaAnalysis),
	}
}

func adjustButton(owner int64, action, payload, label string) tg.InlineKeyboardButton {
	return button(label, Data(owner, ScopeGroup, action, payload))
}

func toggleButton(owner int64, label string, enabled bool, key domain.ToggleKey) tg.InlineKeyboardButton {
	return button(toggleLine(label, enabled), Data(owner, ScopeGroup, ActionToggleGroupSetting, string(key)))
}

// GroupPanel draws one screen of the group hub.
func GroupPanel(in GroupInput) (string, *tg.InlineKeyboardMarkup) {
	lang, owner := in.Lang, in.Owner
	nav := NavRow(owner, ScopeGroup, lang)

	switch in.Section {
	case SectionAbout:
		return AboutText(lang, in.Version), markup([][]tg.InlineKeyboardButton{nav})

	case SectionLanguage:
		// Which language is current is on the buttons; where it came from is not.
		text := Panel(
			i18n.T(lang, "menu.language.title"),
			i18n.T(lang, "menu.language.hint"),
			i18n.T(lang, groupSourceKey(in.GroupLangSource)),
		)
		rows := languageButtons(owner, ScopeGroup, ActionSetGroupLanguage, in.GroupLang)
		rows = append(rows,
			[]tg.InlineKeyboardButton{button(
				i18n.T(lang, "settings.group.language_auto"),
				Data(owner, ScopeGroup, ActionAutoGroupLanguage, ""),
			)},
			nav,
		)
		return text, markup(rows)

	case SectionSchedule:
		// The buttons here are nudges, not values: nothing on the keyboard says
		// what the time and the threshold currently are.
		text := Panel(
			i18n.T(lang, "settings.group.quote_day.title"),
			i18n.T(lang, "settings.group.quote_day.hint"),
			i18n.T(lang, "settings.group.time.line", "time", in.QuoteTime),
			i18n.T(lang, "settings.group.min_messages.line", "count", strconv.Itoa(in.MinMessages)),
			i18n.T(lang, "settings.group.time.timezone", "timezone", in.Timezone),
		)
		// A clock delta reads the same in every language, and the value it moves
		// is on the line right above it, so the labels carry no units. The minus
		// is U+2212.
		return text, markup([][]tg.InlineKeyboardButton{
			{
				adjustButton(owner, ActionGroupTimeAdjust, "-60", "−1:00"),
				adjustButton(owner, ActionGroupTimeAdjust, "60", "+1:00"),
			},
			{
				adjustButton(owner, ActionGroupTimeAdjust, "-15", "−0:15"),
				adjustButton(owner, ActionGroupTimeAdjust, "15", "+0:15"),
			},
			{
				adjustButton(owner, ActionGroupMinAdjust, "-5", "−5"),
				adjustButton(owner, ActionGroupMinAdjust, "5", "+5"),
				adjustButton(owner, ActionGroupMinAdjust, "-1", "−1"),
				adjustButton(owner, ActionGroupMinAdjust, "1", "+1"),
			},
			{button(i18n.T(lang, "settings.button.timezone"), Data(owner, ScopeGroup, ActionGroupTimezone, ""))},
			nav,
		})

	case SectionTimezone:
		// The chosen zone is marked on its own button, so repeating it in the
		// body would be the body saying what the keyboard already says.
		text := Panel(i18n.T(lang, "settings.group.tz.title"), i18n.T(lang, "settings.group.tz.hint"))
		rows := timezoneButtons(owner, in.Timezone)
		return text, markup(append(rows, nav))

	case SectionBehavior:
		// Every switch and its state is on a button below. What the buttons
		// cannot fit is what "context" means, so that is the body.
		text := Panel(
			i18n.T(lang, "settings.group.publication.title"),
			i18n.T(lang, "settings.group.publication.hint"),
			i18n.T(lang, "settings.group.context.explain"),
		)
		return text, markup([][]tg.InlineKeyboardButton{
			{toggleButton(owner, i18n.T(lang, "settings.button.context"), in.QuoteContext, domain.ToggleContext)},
			{
				toggleButton(owner, i18n.T(lang, "settings.button.boring_notice"), in.BoringNotice, domain.ToggleBoring),
				toggleButton(owner, i18n.T(lang, "settings.button.pin"), in.Pin, domain.TogglePin),
			},
			{toggleButton(owner, i18n.T(lang, "settings.button.media"), in.MediaAnalysis, domain.ToggleMedia)},
			nav,
		})

	case SectionStats:
		text := in.StatsText
		if text == "" {
			text = Panel(
				i18n.T(lang, "user_stats.title"),
				i18n.T(lang, "user_stats.hint"),
				i18n.T(lang, "user_stats.missing"),
			)
		}
		// Two tabs over one set of numbers: the open one is the state the screen
		// is in, not an action.
		return text, markup([][]tg.InlineKeyboardButton{
			{
				Selectable(i18n.T(lang, "menu.button.user_stats"), Data(owner, ScopeGroup, ActionUserStats, ""), in.StatsView != StatsChat),
				Selectable(i18n.T(lang, "menu.button.chat_stats"), Data(owner, ScopeGroup, ActionChatStats, ""), in.StatsView == StatsChat),
			},
			nav,
		})
	}

	// Home: the dashboard.
	bodyKey := "menu.group.user_body"
	if in.IsAdmin {
		bodyKey = "menu.group.admin_body"
	}
	text := Panel(i18n.T(lang, "menu.group.title"), i18n.T(lang, bodyKey), dashboard(in)...)

	rows := [][]tg.InlineKeyboardButton{
		// Settings are set once; the standings are what the panel is reopened
		// for, and for a non-admin they are the only thing here.
		{styled(i18n.T(lang, "menu.button.stats"), Data(owner, ScopeGroup, ActionUserStats, ""), tg.StylePrimary)},
	}
	if in.IsAdmin {
		rows = append(rows, []tg.InlineKeyboardButton{
			button(i18n.T(lang, "settings.button.language"), Data(owner, ScopeGroup, ActionGroupLanguage, "")),
			button(i18n.T(lang, "settings.button.quote_day"), Data(owner, ScopeGroup, ActionGroupSchedule, "")),
			button(i18n.T(lang, "settings.button.publication"), Data(owner, ScopeGroup, ActionGroupBehavior, "")),
		})
	}
	rows = append(rows, documentRow(owner, ScopeGroup, lang))
	// Home is the top of the panel, so it is the one screen with nothing to go
	// back to. It still gets Close, because in a group the panel is a message in
	// somebody else's feed.
	rows = append(rows, []tg.InlineKeyboardButton{CloseButton(owner, ScopeGroup, lang)})
	return text, markup(rows)
}
