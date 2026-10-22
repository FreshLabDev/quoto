// SPDX-License-Identifier: Apache-2.0

package agreement

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/FreshLabDev/tg"

	"github.com/FreshLabDev/quoto/internal/buildinfo"
	"github.com/FreshLabDev/quoto/internal/domain"
	"github.com/FreshLabDev/quoto/internal/i18n"
	"github.com/FreshLabDev/quoto/internal/menu"
)

// EffectiveDate is when this text started applying. ISO 8601, because a date
// has to read the same in every locale. Change it together with
// domain.DocumentVersion.
const EffectiveDate = "2026-10-18"

// Document is the agreement in both renderings with the keyboard under it.
type Document struct {
	Markdown string
	HTML     string
	Keyboard *tg.InlineKeyboardMarkup
}

// Options say who is looking and where the tab came from.
type Options struct {
	// CanAccept shows the Accept button, to an admin of a group that has not
	// accepted yet. Accepted is the group's state; the two never both show it.
	CanAccept bool
	Accepted  bool
	// Ref is carried through every button, so a language switch stays inside the
	// panel the tab was opened from.
	Ref Ref
	// Nav is the panel's own navigation row (Back, Close), appended last.
	Nav []tg.InlineKeyboardButton
}

// WelcomeKeyboard is the single button under the welcome message and the
// reminder: open the agreement. Until somebody reads and accepts it Quoto
// publishes nothing, so there is nothing else to do from there.
func WelcomeKeyboard(lang string) *tg.InlineKeyboardMarkup {
	return &tg.InlineKeyboardMarkup{InlineKeyboard: [][]tg.InlineKeyboardButton{{{
		Text:         i18n.T(lang, "agreement.view_button"),
		CallbackData: Data(ActionView, lang, Ref{}),
		Style:        tg.StylePrimary,
	}}}}
}

// AcceptedText is the confirmation shown in place of the document after an
// admin accepts. It is HTML on purpose, even on a rich server: it is two short
// lines.
func AcceptedText(lang string) string {
	var parts []string
	for _, key := range []string{"agreement.accepted", "agreement.accepted_admin_hint"} {
		if s := i18n.T(lang, key); s != "" {
			parts = append(parts, s)
		}
	}
	return strings.Join(parts, "\n\n")
}

// Build renders the agreement in language lang, with a language switcher and
// the navigation.
func Build(lang string, o Options) Document {
	rows := menu.LanguageRows(lang, func(code string) string { return Data(ActionView, code, o.Ref) })
	if o.CanAccept && !o.Accepted {
		// An admin who opened this screen came to accept; everything else on it
		// is reading material.
		rows = append(rows, []tg.InlineKeyboardButton{{
			Text:         i18n.T(lang, "agreement.accept_button"),
			CallbackData: Data(ActionAccept, lang, o.Ref),
			Style:        tg.StylePrimary,
		}})
	}
	if len(o.Nav) > 0 {
		rows = append(rows, o.Nav)
	}
	return Document{
		Markdown: renderMarkdown(lang, o),
		HTML:     renderHTML(lang, o),
		Keyboard: &tg.InlineKeyboardMarkup{InlineKeyboard: rows},
	}
}

// section is one numbered part of the document.
type section struct {
	Heading string
	Text    string
	Items   []string
	Note    string
}

// sections reads the document's structure from the locale. A language without
// the key gets the English one whole (i18n.Value falls back as a unit), and a
// malformed entry is skipped rather than failing the screen.
func sections(lang string) []section {
	raw, _ := i18n.Value(lang, "agreement.sections").([]any)
	var out []section
	for _, item := range raw {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		s := section{
			Heading: str(m["heading"]),
			Text:    strings.ReplaceAll(str(m["text"]), "{contact}", buildinfo.Contact),
			Note:    str(m["note"]),
		}
		if items, ok := m["items"].([]any); ok {
			for _, it := range items {
				s.Items = append(s.Items, str(it))
			}
		}
		out = append(out, s)
	}
	return out
}

func str(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	}
	return fmt.Sprint(v)
}

// signatureRows are the four facts printed under the document. The operator and
// the contact come from the project's identity, and the contact is substituted
// into the section that mentions it mid-sentence, so the whole document has one
// source for it rather than sixteen locale copies.
func signatureRows(lang string) [][2]string {
	return [][2]string{
		{i18n.T(lang, "agreement.signature.version_label"), domain.DocumentVersion},
		{i18n.T(lang, "agreement.signature.effective_label"), EffectiveDate},
		{i18n.T(lang, "agreement.signature.operator_label"), buildinfo.Operator},
		{i18n.T(lang, "agreement.signature.contact_label"), buildinfo.Contact},
	}
}

// renderMarkdown is the document as Markdown. The locale text is ours and
// carries no markup, so nothing here is escaped: it is prose, not user input.
func renderMarkdown(lang string, o Options) string {
	blocks := []string{
		"# " + i18n.T(lang, "agreement.title"),
		i18n.T(lang, "agreement.summary"),
	}
	for i, s := range sections(lang) {
		blocks = append(blocks, "## "+strconv.Itoa(i+1)+". "+s.Heading)
		if s.Text != "" {
			blocks = append(blocks, s.Text)
		}
		if len(s.Items) > 0 {
			items := make([]string, len(s.Items))
			for j, it := range s.Items {
				items[j] = "- " + it
			}
			blocks = append(blocks, strings.Join(items, "\n"))
		}
		if s.Note != "" {
			blocks = append(blocks, "> "+s.Note)
		}
	}
	if o.Accepted {
		blocks = append(blocks, "> "+i18n.T(lang, "agreement.already"))
	}
	if o.CanAccept && !o.Accepted {
		blocks = append(blocks, i18n.T(lang, "agreement.accept_hint"))
	}
	blocks = append(blocks, "## "+i18n.T(lang, "agreement.signature.title"))
	sig := signatureRows(lang)
	lines := make([]string, len(sig))
	for i, row := range sig {
		lines[i] = "- **" + row[0] + "** · " + row[1]
	}
	blocks = append(blocks, strings.Join(lines, "\n"))
	return strings.Join(blocks, "\n\n")
}

// renderHTML is the same document in Telegram HTML, for a server without rich
// Markdown.
func renderHTML(lang string, o Options) string {
	esc := domain.EscapeHTML
	var body []string
	for i, s := range sections(lang) {
		lines := []string{"<b>" + strconv.Itoa(i+1) + ". " + esc(s.Heading) + "</b>"}
		if s.Text != "" {
			lines = append(lines, esc(s.Text))
		}
		for _, it := range s.Items {
			lines = append(lines, "— "+esc(it))
		}
		if s.Note != "" {
			lines = append(lines, "<i>"+esc(s.Note)+"</i>")
		}
		body = append(body, strings.Join(lines, "\n"))
	}

	sig := signatureRows(lang)
	sigLines := make([]string, len(sig))
	for i, row := range sig {
		sigLines[i] = esc(row[0]) + " · " + esc(row[1])
	}

	// The document's head is a panel like every other screen: title, one line
	// saying what it is, then the substance quoted. Only the substance is long
	// enough that Telegram should fold it.
	parts := []string{menu.Screen{
		Title:      esc(i18n.T(lang, "agreement.title")),
		Hint:       esc(i18n.T(lang, "agreement.summary")),
		Lines:      []string{strings.Join(body, "\n\n")},
		Expandable: true,
	}.HTML()}
	if o.Accepted {
		parts = append(parts, "<i>"+esc(i18n.T(lang, "agreement.already"))+"</i>")
	}
	if o.CanAccept && !o.Accepted {
		parts = append(parts, "<i>"+esc(i18n.T(lang, "agreement.accept_hint"))+"</i>")
	}
	parts = append(parts,
		"<b>"+esc(i18n.T(lang, "agreement.signature.title"))+"</b>\n"+
			"<blockquote>"+strings.Join(sigLines, "\n")+"</blockquote>")
	return strings.Join(parts, "\n\n")
}
