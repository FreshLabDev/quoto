// SPDX-License-Identifier: Apache-2.0

package menu

import (
	"strings"

	"github.com/FreshLabDev/tg"

	"github.com/FreshLabDev/quoto/internal/domain"
	"github.com/FreshLabDev/quoto/internal/i18n"
)

// Section is a screen of a panel.
type Section string

const (
	SectionHome     Section = "home"
	SectionLanguage Section = "lang"
	SectionSchedule Section = "sched"
	SectionTimezone Section = "tz"
	SectionBehavior Section = "behavior"
	SectionStats    Section = "stats"
	SectionAbout    Section = "about"
)

// The glyphs of a choice: the family uses the same pair in every bot.
const (
	ToggleOn  = "◉"
	ToggleOff = "◎"
)

// TimezoneChoices are the zones the timezone screen offers, in its order. The
// callback accepts any zone Go can load, so a button from an older panel keeps
// working when a zone is added or dropped here.
var TimezoneChoices = []string{
	"UTC",
	"Europe/London",
	"Europe/Berlin",
	"Europe/Kyiv",
	"Europe/Moscow",
	"America/New_York",
	"America/Chicago",
	"America/Los_Angeles",
	"America/Sao_Paulo",
	"Asia/Dubai",
	"Asia/Kolkata",
	"Asia/Tokyo",
	"Australia/Sydney",
}

// Screen is the shape every screen of Quoto has: the name in bold, one italic
// line saying what it is for, and what the buttons cannot say themselves inside
// one blockquote. Titles and hints arrive as plain text and get their tags here,
// so a screen cannot grow a shape of its own by forgetting a tag in a locale file.
type Screen struct {
	Title string
	Hint  string
	// Lines are the body, one per line. A blank entry is kept: a caller uses one
	// to group rows inside the quote. Only a body with nothing in it vanishes.
	Lines []string
	// Version is the About card's one addition: the version beside the name.
	Version string
	// Expandable folds the quote; the agreement is long enough to need it.
	Expandable bool
}

// HTML renders the screen as Telegram HTML.
func (s Screen) HTML() string {
	head := "<b>" + s.Title + "</b>"
	if s.Version != "" {
		head += " · <i>" + s.Version + "</i>"
	}
	if s.Hint != "" {
		head += "\n<i>" + s.Hint + "</i>"
	}
	body := strings.Join(s.Lines, "\n")
	if domain.PyStrip(body) == "" {
		return head
	}
	tag := "<blockquote>"
	if s.Expandable {
		tag = "<blockquote expandable>"
	}
	return head + "\n\n" + tag + body + "</blockquote>"
}

// Panel is the common case of Screen: title, hint, body lines.
func Panel(title, hint string, lines ...string) string {
	return Screen{Title: title, Hint: hint, Lines: lines}.HTML()
}

func button(text, data string) tg.InlineKeyboardButton {
	return tg.InlineKeyboardButton{Text: text, CallbackData: data}
}

func styled(text, data, style string) tg.InlineKeyboardButton {
	return tg.InlineKeyboardButton{Text: text, CallbackData: data, Style: style}
}

func toggleIcon(enabled bool) string {
	if enabled {
		return ToggleOn
	}
	return ToggleOff
}

func toggleLine(label string, enabled bool) string { return toggleIcon(enabled) + " " + label }

// Selectable is one option of a set the person picks exactly one of: the glyph
// says chosen or not on every option, and the chosen one is also painted
// Success, because Success means "this is the state you are in".
func Selectable(text, data string, selected bool) tg.InlineKeyboardButton {
	b := button(toggleIcon(selected)+" "+text, data)
	if selected {
		b.Style = tg.StyleSuccess
	}
	return b
}

// CloseButton dismisses the panel. It is the one destructive button Quoto has,
// so it is always painted.
func CloseButton(owner int64, scope, lang string) tg.InlineKeyboardButton {
	return styled(i18n.T(lang, "menu.button.close"), Data(owner, scope, ActionClose, ""), tg.StyleDanger)
}

func backButton(owner int64, scope, lang string) tg.InlineKeyboardButton {
	return button(i18n.T(lang, "menu.button.back"), Data(owner, scope, ActionHome, ""))
}

// NavRow is Back, and Close only in a group: a private panel has nothing to
// close. Back always leads to the home screen; there is no deeper hierarchy.
func NavRow(owner int64, scope, lang string) []tg.InlineKeyboardButton {
	row := []tg.InlineKeyboardButton{backButton(owner, scope, lang)}
	if scope == ScopeGroup {
		row = append(row, CloseButton(owner, scope, lang))
	}
	return row
}

// documentRow holds the two read-only tabs both panels carry.
func documentRow(owner int64, scope, lang string) []tg.InlineKeyboardButton {
	return []tg.InlineKeyboardButton{
		button(i18n.T(lang, "menu.button.agreement"), Data(owner, scope, ActionAgreement, "")),
		button(i18n.T(lang, "menu.button.about"), Data(owner, scope, ActionAbout, "")),
	}
}

// LanguageRows is the family language grid: every language Quoto speaks, in the
// family order, flag and native name, two to a row. The panel's picker and the
// agreement's both come through here, so neither can drift into an order or a
// label format of its own, and adding a locale adds a button.
func LanguageRows(current string, callbackFor func(code string) string) [][]tg.InlineKeyboardButton {
	codes := i18n.Codes()
	buttons := make([]tg.InlineKeyboardButton, len(codes))
	for i, code := range codes {
		buttons[i] = Selectable(i18n.Label(code), callbackFor(code), code == current)
	}
	return pairs(buttons)
}

// pairs lays buttons out two to a row.
func pairs(buttons []tg.InlineKeyboardButton) [][]tg.InlineKeyboardButton {
	var rows [][]tg.InlineKeyboardButton
	for i := 0; i < len(buttons); i += 2 {
		end := min(i+2, len(buttons))
		// The capped slice keeps a row from growing into its neighbour.
		rows = append(rows, buttons[i:end:end])
	}
	return rows
}

func languageButtons(owner int64, scope, action, current string) [][]tg.InlineKeyboardButton {
	return LanguageRows(current, func(code string) string { return Data(owner, scope, action, code) })
}

func timezoneButtons(owner int64, current string) [][]tg.InlineKeyboardButton {
	buttons := make([]tg.InlineKeyboardButton, len(TimezoneChoices))
	for i, name := range TimezoneChoices {
		buttons[i] = Selectable(name, Data(owner, ScopeGroup, ActionSetGroupTimezone, name), name == current)
	}
	return pairs(buttons)
}

func markup(rows [][]tg.InlineKeyboardButton) *tg.InlineKeyboardMarkup {
	return &tg.InlineKeyboardMarkup{InlineKeyboard: rows}
}
