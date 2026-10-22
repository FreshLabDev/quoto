// SPDX-License-Identifier: Apache-2.0

package agreement

import (
	"fmt"
	"strings"
	"testing"

	"github.com/FreshLabDev/tg"

	"github.com/FreshLabDev/quoto/internal/buildinfo"
	"github.com/FreshLabDev/quoto/internal/domain"
	"github.com/FreshLabDev/quoto/internal/i18n"
	"github.com/FreshLabDev/quoto/internal/menu"
)

func flat(m *tg.InlineKeyboardMarkup) []string {
	var out []string
	for _, row := range m.InlineKeyboard {
		for _, b := range row {
			out = append(out, b.CallbackData)
		}
	}
	return out
}

func contains(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

// AGR-01 (test_agreement.py:19): the round trip of the plain form, and the golden
// string the panels already in chats carry.
func TestCallbackRoundTrip(t *testing.T) {
	data := Data(ActionAccept, "ru", Ref{})
	if data != "agree:a:ru" {
		t.Fatalf("data = %q", data)
	}
	got, ok := Parse(data)
	if !ok || got != (Callback{Action: ActionAccept, Lang: "ru"}) {
		t.Fatalf("Parse = %+v, %v", got, ok)
	}
	if _, has := got.Ref(); has {
		t.Fatal("a plain callback names a panel")
	}
}

// AGR-02 (:26): the form that keeps the panel the tab was opened from.
func TestCallbackKeepsThePanel(t *testing.T) {
	data := Data(ActionView, "de", Ref{Scope: menu.ScopeGroup, Owner: 777})
	if data != "agree:v:de:g:777" {
		t.Fatalf("data = %q", data)
	}
	got, ok := Parse(data)
	want := Callback{Action: ActionView, Lang: "de", Scope: "g", Owner: 777, HasOwner: true}
	if !ok || got != want {
		t.Fatalf("Parse = %+v, %v", got, ok)
	}
	if ref, has := got.Ref(); !has || ref != (Ref{Scope: "g", Owner: 777}) {
		t.Fatalf("Ref = %+v, %v", ref, has)
	}
}

// AGR-03 (:35): foreign data and malformed data are rejected. The shape is exactly
// three or exactly five parts, and the owner is a number.
func TestParseRejects(t *testing.T) {
	for _, data := range []string{
		"menu:1:p:home", "", "agree", "agree:", "agree:v", "agree:v:en:g", "agree:v:en:g:not-a-number",
		"agree:v:en:g:", "agree:v:en:g:1:extra", "agreement:v:en", "Agree:v:en", "agree:v:en:g:1.5",
	} {
		if got, ok := Parse(data); ok {
			t.Errorf("Parse(%q) = %+v, want rejection", data, got)
		}
	}
	// The five-part form takes a negative owner and any scope text, as Python did.
	if got, ok := Parse("agree:v:en:x:-5"); !ok || got.Owner != -5 || got.Scope != "x" {
		t.Errorf("Parse = %+v, %v", got, ok)
	}
	// A three-part form carries whatever action and language text it has; the handler decides.
	if got, ok := Parse("agree:zzz:xx"); !ok || got.Action != "zzz" || got.Lang != "xx" {
		t.Errorf("Parse = %+v, %v", got, ok)
	}
	// A callback without a scope has no panel even with an owner.
	if _, has := (Callback{HasOwner: true, Owner: 1}).Ref(); has {
		t.Error("Ref without a scope")
	}
}

// AGR-04 (:42, :51, :57, :96): Accept (painted primary) is offered only to someone
// who can accept, in a document not accepted yet, and it is the one primary button.
func TestAcceptButton(t *testing.T) {
	tests := []struct {
		name string
		opts Options
		want bool
	}{
		{"can accept", Options{CanAccept: true}, true},
		{"already accepted", Options{CanAccept: true, Accepted: true}, false},
		{"not allowed", Options{}, false},
		{"accepted and not allowed", Options{Accepted: true}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			doc := Build("en", tt.opts)
			if got := contains(flat(doc.Keyboard), Data(ActionAccept, "en", Ref{})); got != tt.want {
				t.Fatalf("Accept offered = %v, want %v", got, tt.want)
			}
		})
	}

	doc := Build("en", Options{CanAccept: true})
	var primary []string
	for _, row := range doc.Keyboard.InlineKeyboard {
		for _, b := range row {
			if b.Style == tg.StylePrimary {
				primary = append(primary, b.Text)
			}
		}
	}
	if len(primary) != 1 || primary[0] != i18n.T("en", "agreement.accept_button") {
		t.Fatalf("primary buttons %v: Accept must be the one thing the document leads to", primary)
	}
}

// The first test of AgreementDocumentTests (:42): every language can be switched
// to, and the document names the processors it discloses.
func TestDocumentOffersEveryLanguage(t *testing.T) {
	doc := Build("en", Options{CanAccept: true})
	data := flat(doc.Keyboard)
	for _, code := range i18n.Codes() {
		if !contains(data, Data(ActionView, code, Ref{})) {
			t.Errorf("no switch to %s", code)
		}
	}
}

// AGR-13 (:42, :63): the English text discloses the processors and the extended
// service records. Python pinned "OpenRouter"; the agreement now names Anthropic
// first, and OpenRouter as the fallback (docs/go-rewrite.md, AGENTS.md).
func TestEnglishDocumentDisclosures(t *testing.T) {
	doc := Build("en", Options{})
	for name, rendering := range map[string]string{"markdown": doc.Markdown, "html": doc.HTML} {
		for _, want := range []string{"OpenRouter", "Anthropic", "service records", "quality checks"} {
			if !strings.Contains(rendering, want) {
				t.Errorf("%s rendering does not mention %q", name, want)
			}
		}
	}
}

// AGR-06 (:69): the Markdown has real headings, a list and quotes.
func TestMarkdownRendering(t *testing.T) {
	doc := Build("en", Options{})
	lines := strings.Split(doc.Markdown, "\n")
	if !strings.HasPrefix(lines[0], "# ") {
		t.Fatalf("first line %q", lines[0])
	}
	headings, lists, quotes := 0, 0, 0
	for _, l := range lines {
		switch {
		case strings.HasPrefix(l, "## "):
			headings++
		case strings.HasPrefix(l, "- "):
			lists++
		case strings.HasPrefix(l, "> "):
			quotes++
		}
	}
	sections, _ := i18n.Value("en", "agreement.sections").([]any)
	if headings != len(sections)+1 {
		t.Errorf("%d level-2 headings, want %d sections plus the signature", headings, len(sections))
	}
	if lists == 0 || quotes == 0 {
		t.Errorf("list lines %d, quote lines %d", lists, quotes)
	}
}

// AGR-07 (:80): the HTML fallback has no Markdown syntax and folds its body.
func TestHTMLRendering(t *testing.T) {
	doc := Build("en", Options{CanAccept: true})
	for _, l := range strings.Split(doc.HTML, "\n") {
		if strings.HasPrefix(l, "# ") || strings.HasPrefix(l, "## ") {
			t.Fatalf("Markdown heading in the HTML: %q", l)
		}
	}
	if !strings.Contains(doc.HTML, "<blockquote expandable>") {
		t.Fatal("the body is not folded")
	}
}

// AGR-08 (:85): on every language the HTML opens with the shape of every screen,
// title in bold, one italic line, then the folded body.
func TestHTMLOpensLikeEveryScreen(t *testing.T) {
	for _, lang := range i18n.Codes() {
		t.Run(lang, func(t *testing.T) {
			doc := Build(lang, Options{})
			head, rest, _ := strings.Cut(doc.HTML, "\n\n")
			want := "<b>" + domain.EscapeHTML(i18n.T(lang, "agreement.title")) + "</b>\n" +
				"<i>" + domain.EscapeHTML(i18n.T(lang, "agreement.summary")) + "</i>"
			if head != want {
				t.Fatalf("head\n%s\nwant\n%s", head, want)
			}
			if !strings.HasPrefix(rest, "<blockquote expandable>") {
				t.Fatalf("body opens with %.60q", rest)
			}
		})
	}
}

// AGR-09 (:118): the signature block carries the title, the version, the effective
// date, the operator and the contact, in both renderings and on every language.
func TestSignature(t *testing.T) {
	if EffectiveDate != "2026-10-18" {
		t.Errorf("effective date %q", EffectiveDate)
	}
	for _, lang := range i18n.Codes() {
		t.Run(lang, func(t *testing.T) {
			doc := Build(lang, Options{})
			for name, rendering := range map[string]string{"markdown": doc.Markdown, "html": doc.HTML} {
				for _, want := range []string{
					i18n.T(lang, "agreement.signature.title"), domain.DocumentVersion, EffectiveDate,
					buildinfo.Operator, buildinfo.Contact,
				} {
					if !strings.Contains(rendering, want) {
						t.Errorf("%s lacks %q", name, want)
					}
				}
			}
		})
	}
}

// AGR-10 (:129): the contact is the real handle, and no placeholder survives.
func TestContactHandle(t *testing.T) {
	for _, lang := range i18n.Codes() {
		doc := Build(lang, Options{})
		for _, rendering := range []string{doc.Markdown, doc.HTML} {
			if !strings.Contains(rendering, "@amtiyo") || strings.Contains(rendering, "@amti_yo") || strings.Contains(rendering, "{contact}") {
				t.Errorf("%s: contact handling is wrong", lang)
			}
		}
	}
}

// AGR-05 (:106): the switcher is the family grid: sixteen buttons, two to a row,
// "<glyph> <flag> <name>", with the current language marked.
func TestLanguageSwitcherIsTheFamilyGrid(t *testing.T) {
	doc := Build("de", Options{})
	codes := i18n.Codes()
	if len(codes) != 16 {
		t.Fatalf("%d languages", len(codes))
	}
	grid := doc.Keyboard.InlineKeyboard[:(len(codes)+1)/2]
	var got []string
	for _, row := range grid {
		if len(row) > 2 {
			t.Fatalf("row of %d buttons", len(row))
		}
		for _, b := range row {
			got = append(got, b.Text)
		}
	}
	for i, code := range codes {
		glyph := menu.ToggleOff
		if code == "de" {
			glyph = menu.ToggleOn
		}
		if want := glyph + " " + i18n.Label(code); got[i] != want {
			t.Errorf("button %d = %q, want %q", i, got[i], want)
		}
	}
}

// AGR-11 (:137): the panel's navigation row comes last and the switcher carries
// the panel (scope and owner), so Back keeps working after a language change.
func TestNavigationAndPanelAreCarried(t *testing.T) {
	nav := menu.NavRow(777, menu.ScopeGroup, "en")
	doc := Build("en", Options{Ref: Ref{Scope: menu.ScopeGroup, Owner: 777}, Nav: nav})
	rows := doc.Keyboard.InlineKeyboard
	last := rows[len(rows)-1]
	if len(last) != len(nav) {
		t.Fatalf("last row %+v, want %+v", last, nav)
	}
	for i := range nav {
		if last[i] != nav[i] {
			t.Fatalf("last row %+v, want %+v", last, nav)
		}
	}
	if !contains(flat(doc.Keyboard), Data(ActionView, "ru", Ref{Scope: "g", Owner: 777})) {
		t.Fatal("the switcher lost the panel")
	}

	// With an Accept button, Nav still comes after it.
	doc = Build("en", Options{CanAccept: true, Nav: nav})
	rows = doc.Keyboard.InlineKeyboard
	if rows[len(rows)-1][0] != nav[0] || rows[len(rows)-2][0].CallbackData != Data(ActionAccept, "en", Ref{}) {
		t.Fatal("order: switcher, Accept, navigation")
	}

	// No Nav, no extra row.
	doc = Build("en", Options{})
	if got := len(doc.Keyboard.InlineKeyboard); got != 8 {
		t.Fatalf("%d rows without navigation, want the 8 of the grid", got)
	}
}

// AGR-12 (:155): the first button of the welcome keyboard opens the document in
// the language of the greeting; it is the only button.
func TestWelcomeKeyboard(t *testing.T) {
	kb := WelcomeKeyboard("de")
	if len(kb.InlineKeyboard) != 1 || len(kb.InlineKeyboard[0]) != 1 {
		t.Fatalf("keyboard %+v", kb)
	}
	b := kb.InlineKeyboard[0][0]
	if b.CallbackData != "agree:v:de" || b.Text != i18n.T("de", "agreement.view_button") {
		t.Fatalf("button %+v", b)
	}
	if b.Style != tg.StylePrimary {
		t.Errorf("style %q", b.Style)
	}
}

// The state notes under the document: an accepted one says so, one that can be
// accepted says how, a plain reader sees neither.
func TestStateNotes(t *testing.T) {
	already := i18n.T("en", "agreement.already")
	hint := i18n.T("en", "agreement.accept_hint")
	tests := []struct {
		name         string
		opts         Options
		already, tip bool
	}{
		{"reader", Options{}, false, false},
		{"accepted", Options{Accepted: true}, true, false},
		{"admin who can accept", Options{CanAccept: true}, false, true},
		{"accepted, even for an admin", Options{CanAccept: true, Accepted: true}, true, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			doc := Build("en", tt.opts)
			renderings := []struct {
				name, text   string
				already, tip string
			}{
				{"markdown", doc.Markdown, already, hint},
				{"html", doc.HTML, domain.EscapeHTML(already), domain.EscapeHTML(hint)},
			}
			for _, r := range renderings {
				if got := strings.Contains(r.text, r.already); got != tt.already {
					t.Errorf("%s: already-note = %v, want %v", r.name, got, tt.already)
				}
				if got := strings.Contains(r.text, r.tip); got != tt.tip {
					t.Errorf("%s: accept hint = %v, want %v", r.name, got, tt.tip)
				}
			}
		})
	}
}

// The confirmation after an admin accepts is the two locale lines and nothing
// else, in every language, with nothing left empty.
func TestAcceptedText(t *testing.T) {
	for _, lang := range i18n.Codes() {
		got := AcceptedText(lang)
		want := i18n.T(lang, "agreement.accepted") + "\n\n" + i18n.T(lang, "agreement.accepted_admin_hint")
		if got != want || strings.HasPrefix(got, "\n") || strings.HasSuffix(got, "\n") {
			t.Errorf("%s: %q", lang, got)
		}
	}
}

// Every section of every language renders: a numbered bold heading, in order,
// and the Markdown has as many level-2 headings.
func TestSectionsAreNumbered(t *testing.T) {
	for _, lang := range i18n.Codes() {
		secs := sections(lang)
		if len(secs) == 0 {
			t.Errorf("%s: no sections", lang)
			continue
		}
		doc := Build(lang, Options{})
		for i := range secs {
			if !strings.Contains(doc.Markdown, fmt.Sprintf("## %d. ", i+1)) {
				t.Errorf("%s: section %d missing from the Markdown", lang, i+1)
			}
			if !strings.Contains(doc.HTML, fmt.Sprintf("<b>%d. ", i+1)) {
				t.Errorf("%s: section %d missing from the HTML", lang, i+1)
			}
		}
	}
}

// No Python counterpart: str is the Go reading of the str(value) calls in agreement.py's
// section reader, which tolerated numbers and missing keys in the locale JSON.
func TestStr(t *testing.T) {
	if str(nil) != "" || str("a") != "a" || str(3) != "3" || str(1.5) != "1.5" {
		t.Fatal("str")
	}
}
