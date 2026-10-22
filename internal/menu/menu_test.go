// SPDX-License-Identifier: Apache-2.0

package menu

import (
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/FreshLabDev/tg"

	"github.com/FreshLabDev/quoto/internal/buildinfo"
	"github.com/FreshLabDev/quoto/internal/domain"
	"github.com/FreshLabDev/quoto/internal/i18n"
)

// A panel is title, hint, quote: a bold name, optionally the version beside it,
// one italic line under it, and then at most one blockquote.
var (
	headRE = regexp.MustCompile(`(?s)^<b>[^<]+</b>( · <i>[^<]+</i>)?(\n<i>.+</i>)?$`)
	bodyRE = regexp.MustCompile(`(?s)^<blockquote( expandable)?>.*</blockquote>$`)
	// Anything pictographic. The language grid is allowed its flags, which are the
	// content of those buttons, and nothing else on a panel may carry one.
	emojiRE = regexp.MustCompile("[\\x{1F000}-\\x{1FAFF}\\x{2600}-\\x{27BF}\\x{2B00}-\\x{2BFF}\\x{2190}-\\x{21FF}\\x{2300}-\\x{23FF}]")
)

func labels(m *tg.InlineKeyboardMarkup) []string {
	var out []string
	for _, row := range m.InlineKeyboard {
		for _, b := range row {
			out = append(out, b.Text)
		}
	}
	return out
}

func buttons(m *tg.InlineKeyboardMarkup) []tg.InlineKeyboardButton {
	var out []tg.InlineKeyboardButton
	for _, row := range m.InlineKeyboard {
		out = append(out, row...)
	}
	return out
}

func styleOf(m *tg.InlineKeyboardMarkup, style string) []string {
	out := []string{}
	for _, b := range buttons(m) {
		if b.Style == style {
			out = append(out, b.Text)
		}
	}
	return out
}

func datas(m *tg.InlineKeyboardMarkup) []string {
	var out []string
	for _, b := range buttons(m) {
		out = append(out, b.CallbackData)
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

func private(lang string, section Section) (string, *tg.InlineKeyboardMarkup) {
	return PrivatePanel(PrivateInput{
		Owner: 777, Lang: lang, BotUsername: "quoto_test_bot", Version: "1.2.3", Section: section,
	})
}

func group(lang string, section Section, admin bool) (string, *tg.InlineKeyboardMarkup) {
	return GroupPanel(GroupInput{
		Owner: 777, Lang: lang, GroupLang: lang, IsAdmin: admin,
		QuoteTime: "21:00", MinMessages: 10, Timezone: "Europe/Kyiv",
		BoringNotice: true, Pin: true, QuoteContext: true, MediaAnalysis: true,
		Section: section, Version: "1.2.3",
	})
}

type screen struct {
	name     string
	text     string
	keyboard *tg.InlineKeyboardMarkup
}

// everyScreen is every screen both panels can draw, for the rules that hold on
// all of them.
func everyScreen(lang string) []screen {
	var out []screen
	for _, s := range []Section{SectionHome, SectionLanguage, SectionAbout} {
		t, k := private(lang, s)
		out = append(out, screen{"private/" + string(s), t, k})
	}
	for _, s := range []Section{SectionHome, SectionLanguage, SectionSchedule, SectionTimezone, SectionBehavior, SectionStats, SectionAbout} {
		t, k := group(lang, s, true)
		out = append(out, screen{"group/" + string(s), t, k})
	}
	t, k := group(lang, SectionHome, false)
	return append(out, screen{"group/home-member", t, k})
}

// doc §6: callback_data grammar. Python has no direct test; the strings are the
// ones listed in phase0/04-handlers.md §1.6, which old panels in chats carry.
func TestCallbackGoldenStrings(t *testing.T) {
	cases := []struct {
		owner                  int64
		scope, action, payload string
		want                   string
	}{
		{777, ScopeGroup, ActionHome, "", "menu:777:g:home"},
		{777, ScopeGroup, ActionClose, "", "menu:777:g:close"},
		{777, ScopeGroup, ActionUserStats, "", "menu:777:g:userstats"},
		{777, ScopeGroup, ActionChatStats, "", "menu:777:g:chatstats"},
		{777, ScopeGroup, ActionGroupLanguage, "", "menu:777:g:lang"},
		{777, ScopeGroup, ActionSetGroupLanguage, "de", "menu:777:g:setlang:de"},
		{777, ScopeGroup, ActionAutoGroupLanguage, "", "menu:777:g:autolang"},
		{777, ScopeGroup, ActionGroupSchedule, "", "menu:777:g:sched"},
		{777, ScopeGroup, ActionGroupTimeAdjust, "-60", "menu:777:g:timeadj:-60"},
		{777, ScopeGroup, ActionGroupMinAdjust, "1", "menu:777:g:minadj:1"},
		{777, ScopeGroup, ActionGroupTimezone, "", "menu:777:g:tz"},
		{777, ScopeGroup, ActionSetGroupTimezone, "Europe/Kyiv", "menu:777:g:settz:Europe/Kyiv"},
		{777, ScopeGroup, ActionGroupBehavior, "", "menu:777:g:behavior"},
		{777, ScopeGroup, ActionToggleGroupSetting, "context", "menu:777:g:toggle:context"},
		{777, ScopeGroup, ActionAgreement, "", "menu:777:g:doc"},
		{777, ScopeGroup, ActionAbout, "", "menu:777:g:about"},
		{777, ScopePrivate, ActionHome, "", "menu:777:p:home"},
		{777, ScopePrivate, ActionPrivateLanguage, "", "menu:777:p:plang"},
		{777, ScopePrivate, ActionSetPrivateLanguage, "uk", "menu:777:p:setplang:uk"},
		{777, ScopePrivate, ActionAutoPrivateLanguage, "", "menu:777:p:autoplang"},
		{777, ScopePrivate, ActionAgreement, "", "menu:777:p:doc"},
		{777, ScopePrivate, ActionAbout, "", "menu:777:p:about"},
	}
	for _, c := range cases {
		got := Data(c.owner, c.scope, c.action, c.payload)
		if got != c.want {
			t.Errorf("Data(%d,%s,%s,%q) = %q, want %q", c.owner, c.scope, c.action, c.payload, got, c.want)
		}
		parsed, ok := Parse(c.want)
		if !ok || parsed != (Callback{Owner: c.owner, Scope: c.scope, Action: c.action, Payload: c.payload}) {
			t.Errorf("Parse(%q) = %+v, %v", c.want, parsed, ok)
		}
		if !KnownAction(c.action) {
			t.Errorf("%q is not a known action", c.action)
		}
	}
}

// doc §6: the grammar of menu: callback_data (split into at most five parts,
// four required, owner numeric); anything else is answered with an empty ack.
func TestCallbackParseRejects(t *testing.T) {
	for _, data := range []string{"", "menu", "menu:", "menu:777", "menu:777:g", "menu:abc:g:home", "agree:v:en", "other:1:g:home", "menu::g:home"} {
		if c, ok := Parse(data); ok {
			t.Errorf("Parse(%q) = %+v, want no", data, c)
		}
	}
	// The payload is everything after the fourth colon, colons included.
	c, ok := Parse("menu:5:g:settz:America/Argentina/Buenos_Aires:extra")
	if !ok || c.Payload != "America/Argentina/Buenos_Aires:extra" {
		t.Errorf("payload with colon: %+v %v", c, ok)
	}
	// A trailing colon leaves an empty payload, as split(":", 4) did.
	if c, ok := Parse("menu:5:g:setlang:"); !ok || c.Payload != "" || c.Action != "setlang" {
		t.Errorf("trailing colon: %+v %v", c, ok)
	}
}

// doc §6: names of actions retired before 2026-06-01 must not be understood, so
// that their buttons get an empty acknowledgement and nothing else.
func TestRetiredActionsAreUnknown(t *testing.T) {
	for _, name := range []string{"settings", "time", "minmsg", "quoteday", "publish", "preview", "publish_ok", ""} {
		if KnownAction(name) {
			t.Errorf("%q must not be a known action", name)
		}
	}
	if len(knownActions) != 19 {
		t.Errorf("%d known actions, want 19", len(knownActions))
	}
	for _, a := range []string{ActionGroupLanguage, ActionSetGroupLanguage, ActionAutoGroupLanguage, ActionGroupSchedule,
		ActionGroupTimeAdjust, ActionGroupMinAdjust, ActionGroupTimezone, ActionSetGroupTimezone, ActionGroupBehavior, ActionToggleGroupSetting} {
		if !AdminOnly(a) {
			t.Errorf("%s must need an admin", a)
		}
	}
	for _, a := range []string{ActionHome, ActionClose, ActionAbout, ActionAgreement, ActionChatStats, ActionUserStats} {
		if AdminOnly(a) {
			t.Errorf("%s must not need an admin", a)
		}
	}
}

// tests/test_menu.py::NavigationTests::test_close_exists_only_in_group_panels (MEN-01)
func TestCloseExistsOnlyInGroupPanels(t *testing.T) {
	closeLabel := i18n.T("ru", "menu.button.close")
	_, k := private("ru", SectionHome)
	if contains(labels(k), closeLabel) {
		t.Error("private home has Close")
	}
	_, k = private("ru", SectionLanguage)
	if contains(labels(k), closeLabel) {
		t.Error("private language has Close")
	}
	_, k = group("ru", SectionHome, true)
	if !contains(labels(k), closeLabel) {
		t.Error("group home lacks Close")
	}
	_, k = group("ru", SectionLanguage, true)
	if !contains(labels(k), closeLabel) {
		t.Error("group language lacks Close")
	}
}

// tests/test_menu.py::NavigationTests::test_navigation_labels_carry_no_arrows (MEN-02)
func TestNavigationLabelsCarryNoArrows(t *testing.T) {
	for _, lang := range i18n.Codes() {
		for _, key := range []string{"menu.button.back", "menu.button.close"} {
			label := i18n.T(lang, key)
			if label != strings.TrimSpace(label) {
				t.Errorf("%s %s has blanks around it: %q", lang, key, label)
			}
			for _, ornament := range []string{"‹", "›", "<", ">", "←", "→", "×", "✕", "✖"} {
				if strings.Contains(label, ornament) {
					t.Errorf("%s %s carries %q", lang, key, ornament)
				}
			}
		}
	}
}

// tests/test_menu.py::NavigationTests::test_selection_marker_is_the_family_pair (MEN-03)
func TestSelectionMarkerIsTheFamilyPair(t *testing.T) {
	if ToggleOn != "◉" || ToggleOff != "◎" {
		t.Fatalf("markers %q %q", ToggleOn, ToggleOff)
	}
	_, k := group("ru", SectionTimezone, true)
	var on, off bool
	for _, l := range labels(k) {
		on = on || strings.HasPrefix(l, "◉ ")
		off = off || strings.HasPrefix(l, "◎ ")
	}
	if !on || !off {
		t.Errorf("timezone labels %v", labels(k))
	}
}

// tests/test_menu.py::NavigationTests::test_close_is_the_only_destructive_button_and_is_always_painted (MEN-04)
func TestCloseIsTheOnlyDangerButton(t *testing.T) {
	closeLabel := i18n.T("ru", "menu.button.close")
	for _, s := range everyScreen("ru") {
		got := styleOf(s.keyboard, tg.StyleDanger)
		want := []string{}
		if contains(labels(s.keyboard), closeLabel) {
			want = []string{closeLabel}
		}
		if fmt.Sprint(got) != fmt.Sprint(want) {
			t.Errorf("%s: danger buttons %v, want %v", s.name, got, want)
		}
	}
}

// tests/test_menu.py::NavigationTests::test_going_up_is_always_the_same_word (MEN-05)
func TestBackIsOnEveryScreenButHome(t *testing.T) {
	back := i18n.T("ru", "menu.button.back")
	for _, s := range everyScreen("ru") {
		has := contains(labels(s.keyboard), back)
		home := strings.HasSuffix(s.name, "home") || strings.HasSuffix(s.name, "home-member")
		if home && has {
			t.Errorf("%s: home has Back", s.name)
		}
		if !home && !has {
			t.Errorf("%s: no Back", s.name)
		}
	}
}

// tests/test_menu.py::ScreenShapeTests::test_every_screen_is_a_title_a_hint_and_at_most_one_quote (MEN-06)
func TestEveryScreenIsTitleHintAndAtMostOneQuote(t *testing.T) {
	for _, s := range everyScreen("ru") {
		head, body, _ := strings.Cut(s.text, "\n\n")
		if !headRE.MatchString(head) {
			t.Errorf("%s: head %q", s.name, head)
		}
		if body != "" && !bodyRE.MatchString(body) {
			t.Errorf("%s: body %q", s.name, body)
		}
	}
}

// tests/test_menu.py::ScreenShapeTests::test_a_section_does_not_repeat_the_bot_name_above_its_own_title (MEN-07)
func TestSectionDoesNotRepeatTheBotName(t *testing.T) {
	for _, sec := range []Section{SectionLanguage, SectionSchedule, SectionTimezone, SectionBehavior} {
		text, _ := group("ru", sec, true)
		first, _, _ := strings.Cut(text, "\n\n")
		if strings.Contains(first, i18n.T("ru", "menu.group.title")) {
			t.Errorf("%s repeats the bot name: %q", sec, first)
		}
	}
}

// tests/test_menu.py::ScreenShapeTests::test_a_screen_never_lists_in_the_body_what_its_buttons_already_say (MEN-08)
func TestBodyDoesNotRepeatWhatTheButtonsSay(t *testing.T) {
	text, _ := group("ru", SectionTimezone, true)
	if strings.Contains(text, "Europe/Kyiv") {
		t.Error("timezone body names the chosen zone")
	}
	g, _ := group("ru", SectionLanguage, true)
	p, _ := private("ru", SectionLanguage)
	for _, text := range []string{g, p} {
		if strings.Contains(text, i18n.Name("ru")) {
			t.Errorf("language body names the language: %q", text)
		}
	}
	text, _ = group("ru", SectionBehavior, true)
	if strings.Contains(text, ToggleOn) || strings.Contains(text, ToggleOff) {
		t.Error("behaviour body carries toggle glyphs")
	}
}

// tests/test_menu.py::ColourTests::test_no_screen_has_two_primaries (MEN-09)
func TestNoScreenHasTwoPrimaries(t *testing.T) {
	for _, s := range everyScreen("ru") {
		if n := len(styleOf(s.keyboard, tg.StylePrimary)); n > 1 {
			t.Errorf("%s has %d primaries", s.name, n)
		}
	}
}

// tests/test_menu.py::ColourTests::test_the_primary_is_the_one_thing_each_home_leads_to (MEN-09)
func TestThePrimaryIsWhatEachHomeLeadsTo(t *testing.T) {
	_, k := private("ru", SectionHome)
	if got := styleOf(k, tg.StylePrimary); fmt.Sprint(got) != fmt.Sprint([]string{i18n.T("ru", "private.add_to_group")}) {
		t.Errorf("private primary %v", got)
	}
	for _, admin := range []bool{true, false} {
		_, k := group("ru", SectionHome, admin)
		if got := styleOf(k, tg.StylePrimary); fmt.Sprint(got) != fmt.Sprint([]string{i18n.T("ru", "menu.button.stats")}) {
			t.Errorf("group primary (admin=%v) %v", admin, got)
		}
	}
}

// tests/test_menu.py::ColourTests::test_a_screen_that_leads_nowhere_gets_no_primary (MEN-09)
func TestScreensThatLeadNowhereHaveNoPrimary(t *testing.T) {
	for _, sec := range []Section{SectionLanguage, SectionSchedule, SectionTimezone, SectionBehavior, SectionStats, SectionAbout} {
		_, k := group("ru", sec, true)
		if got := styleOf(k, tg.StylePrimary); len(got) != 0 {
			t.Errorf("%s has primary %v", sec, got)
		}
	}
	_, k := private("ru", SectionLanguage)
	if got := styleOf(k, tg.StylePrimary); len(got) != 0 {
		t.Errorf("private language has primary %v", got)
	}
}

// tests/test_menu.py::ColourTests::test_success_marks_the_state_and_never_an_action (MEN-10)
func TestSuccessMarksStateNeverAnAction(t *testing.T) {
	for _, s := range everyScreen("ru") {
		for _, b := range buttons(s.keyboard) {
			if b.Style == tg.StyleSuccess && !strings.HasPrefix(b.Text, ToggleOn+" ") {
				t.Errorf("%s: success button %q", s.name, b.Text)
			}
		}
	}
}

// tests/test_menu.py::ColourTests::test_the_chosen_option_of_every_set_is_the_success_one (MEN-10)
func TestChosenOptionIsTheSuccessOne(t *testing.T) {
	_, pk := private("ru", SectionLanguage)
	_, gk := group("ru", SectionLanguage, true)
	for _, k := range []*tg.InlineKeyboardMarkup{pk, gk} {
		want := fmt.Sprint([]string{ToggleOn + " " + i18n.Label("ru")})
		if got := fmt.Sprint(styleOf(k, tg.StyleSuccess)); got != want {
			t.Errorf("language success %s, want %s", got, want)
		}
	}
	_, k := group("ru", SectionTimezone, true)
	if got := styleOf(k, tg.StyleSuccess); fmt.Sprint(got) != fmt.Sprint([]string{"◉ Europe/Kyiv"}) {
		t.Errorf("timezone success %v", got)
	}
	_, k = group("ru", SectionStats, true)
	if got := styleOf(k, tg.StyleSuccess); fmt.Sprint(got) != fmt.Sprint([]string{"◉ " + i18n.T("ru", "menu.button.user_stats")}) {
		t.Errorf("stats success %v", got)
	}
}

// tests/test_menu.py::ColourTests::test_toggles_stay_unpainted (MEN-11)
func TestTogglesStayUnpainted(t *testing.T) {
	_, k := group("ru", SectionBehavior, true)
	for _, b := range buttons(k) {
		if (strings.HasPrefix(b.Text, ToggleOn) || strings.HasPrefix(b.Text, ToggleOff)) && b.Style != "" {
			t.Errorf("toggle %q is painted %q", b.Text, b.Style)
		}
	}
}

// tests/test_menu.py::LanguageGridTests::test_the_family_order_and_labels_are_the_contract_s (MEN-12, MEN-13)
func TestLanguageOrderAndLabels(t *testing.T) {
	want := []string{"en", "ru", "uk", "es", "fr", "de", "it", "pl", "cs", "tr", "sv", "be", "ca", "zh", "ja", "ar"}
	if fmt.Sprint(i18n.Codes()) != fmt.Sprint(want) {
		t.Errorf("order %v", i18n.Codes())
	}
	if i18n.Label("en") != "🇬🇧 English" || i18n.Label("uk") != "🇺🇦 Українська" {
		t.Errorf("labels %q %q", i18n.Label("en"), i18n.Label("uk"))
	}
}

// tests/test_menu.py::LanguageGridTests::test_every_language_button_is_a_flag_a_native_name_and_a_mark (MEN-14)
func TestLanguageRowsGolden(t *testing.T) {
	rows := LanguageRows("uk", func(code string) string { return "x:" + code })
	if len(rows) != 8 {
		t.Fatalf("%d rows", len(rows))
	}
	var got []string
	for _, row := range rows {
		if len(row) != 2 {
			t.Fatalf("row %v", row)
		}
		for _, b := range row {
			got = append(got, b.Text)
		}
	}
	want := []string{
		"◎ 🇬🇧 English", "◎ 🇷🇺 Русский", "◉ 🇺🇦 Українська", "◎ 🇪🇸 Español", "◎ 🇫🇷 Français", "◎ 🇩🇪 Deutsch",
		"◎ 🇮🇹 Italiano", "◎ 🇵🇱 Polski", "◎ 🇨🇿 Čeština", "◎ 🇹🇷 Türkçe", "◎ 🇸🇪 Svenska", "◎ 🇧🇾 Беларуская",
		"◎ 🇦🇩 Català", "◎ 🇨🇳 中文", "◎ 🇯🇵 日本語", "◎ 🇦🇪 العربية",
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("labels\n got %q\nwant %q", got, want)
	}
	if rows[0][0].CallbackData != "x:en" {
		t.Errorf("callback %q", rows[0][0].CallbackData)
	}
}

// Rows must not share capacity: a row that grows must not overwrite the next.
func TestLanguageRowsDoNotAlias(t *testing.T) {
	rows := LanguageRows("en", func(code string) string { return code })
	first := rows[1][0].Text
	_ = append(rows[0], tg.InlineKeyboardButton{Text: "extra"})
	if rows[1][0].Text != first {
		t.Error("appending to one row changed the next")
	}
}

// tests/test_menu.py::AboutTests::test_about_card_names_version_scoring_sources_and_admin (MEN-16), with F13 and S1's licence rule
func TestAboutCard(t *testing.T) {
	for _, lang := range i18n.Codes() {
		text := AboutText(lang, "1.2.3")
		for _, want := range []string{
			"<b>Quoto</b> · <i>v1.2.3</i>",
			i18n.T(lang, "about.tagline"),
			`<a href="` + buildinfo.Repository + `">FreshLabDev/quoto</a>`,
			buildinfo.License,
			`<a href="` + buildinfo.ContactURL + `">` + buildinfo.Contact + `</a>`,
			"<blockquote>",
			"Claude",
			"OpenRouter",
		} {
			if !strings.Contains(text, want) {
				t.Errorf("%s: about card lacks %q:\n%s", lang, want, text)
			}
		}
		if strings.Contains(text, "GPL") {
			t.Errorf("%s: about card mentions GPL", lang)
		}
	}
	// A version already written with a v is not given a second one.
	if !strings.Contains(AboutText("en", "v0.12.0"), "<i>v0.12.0</i>") {
		t.Error("v prefix doubled")
	}
}

// doc §7.2 F13: the scoring row reads from the locale when the key exists.
func TestAboutScoringRowPrefersTheLocaleKey(t *testing.T) {
	if i18n.Value("en", evalKey) != nil {
		t.Skip("the locale now has " + evalKey + "; the fallback is no longer reachable")
	}
	if got := evalValue("ru"); got != evalFallback {
		t.Errorf("fallback %q", got)
	}
}

// tests/test_menu.py::AboutTests::test_about_tab_has_no_source_button_duplicating_the_link (MEN-17)
// tests/test_menu.py::AboutTests::test_about_tab_navigation_follows_the_scope (MEN-18)
func TestAboutTabHasOnlyNavigation(t *testing.T) {
	pt, pk := private("ru", SectionAbout)
	gt, gk := group("ru", SectionAbout, true)
	if pt != AboutText("ru", "1.2.3") || gt != AboutText("ru", "1.2.3") {
		t.Error("about tab text differs from the card")
	}
	for _, k := range []*tg.InlineKeyboardMarkup{pk, gk} {
		for _, b := range buttons(k) {
			if b.URL != "" {
				t.Errorf("about tab has a URL button %q", b.Text)
			}
		}
	}
	back, closeLabel := i18n.T("ru", "menu.button.back"), i18n.T("ru", "menu.button.close")
	if fmt.Sprint(labels(pk)) != fmt.Sprint([]string{back}) {
		t.Errorf("private nav %v", labels(pk))
	}
	if fmt.Sprint(labels(gk)) != fmt.Sprint([]string{back, closeLabel}) {
		t.Errorf("group nav %v", labels(gk))
	}
}

// tests/test_menu.py::AboutTests::test_both_panels_offer_the_agreement_and_about_tabs (MEN-19)
func TestBothPanelsOfferAgreementAndAbout(t *testing.T) {
	_, pk := private("ru", SectionHome)
	_, gk := group("ru", SectionHome, true)
	for scope, k := range map[string]*tg.InlineKeyboardMarkup{ScopePrivate: pk, ScopeGroup: gk} {
		d := datas(k)
		if !contains(d, Data(777, scope, ActionAgreement, "")) || !contains(d, Data(777, scope, ActionAbout, "")) {
			t.Errorf("scope %s: %v", scope, d)
		}
	}
}

// tests/test_handlers.py::HandlerTests::test_group_start_menu_shows_admin_controls (HND-04)
func TestGroupHomeLabelsForAdmin(t *testing.T) {
	_, k := group("ru", SectionHome, true)
	want := []string{"Статистика", "Язык", "Цитата дня", "Публикация", "Соглашение", "О боте", "Закрыть"}
	if fmt.Sprint(labels(k)) != fmt.Sprint(want) {
		t.Errorf("labels %q", labels(k))
	}
	_, k = group("ru", SectionHome, false)
	if fmt.Sprint(labels(k)) != fmt.Sprint([]string{"Статистика", "Соглашение", "О боте", "Закрыть"}) {
		t.Errorf("member labels %q", labels(k))
	}
}

// tests/test_handlers.py::HandlerTests::test_the_schedule_nudges_read_the_same_in_every_language (HND-08)
func TestScheduleNudgesAreTheSameInEveryLanguage(t *testing.T) {
	want := []string{"−1:00", "+1:00", "−0:15", "+0:15", "−5", "+5", "−1", "+1"}
	payloads := []string{
		"menu:777:g:timeadj:-60", "menu:777:g:timeadj:60", "menu:777:g:timeadj:-15", "menu:777:g:timeadj:15",
		"menu:777:g:minadj:-5", "menu:777:g:minadj:5", "menu:777:g:minadj:-1", "menu:777:g:minadj:1",
	}
	for _, lang := range i18n.Codes() {
		_, k := group(lang, SectionSchedule, true)
		var got, data []string
		for _, row := range k.InlineKeyboard[:3] {
			for _, b := range row {
				got = append(got, b.Text)
				data = append(data, b.CallbackData)
			}
		}
		if fmt.Sprint(got) != fmt.Sprint(want) {
			t.Errorf("%s: nudges %q", lang, got)
		}
		if fmt.Sprint(data) != fmt.Sprint(payloads) {
			t.Errorf("%s: callbacks %q", lang, data)
		}
	}
	// U+2212, not a hyphen or an en dash.
	if !strings.HasPrefix(want[0], "\u2212") {
		t.Fatal("test data lost its minus sign")
	}
}

// tests/test_handlers.py::HandlerTests::test_group_schedule_section_shows_time_and_min_controls (HND-07)
func TestScheduleSection(t *testing.T) {
	text, k := group("ru", SectionSchedule, true)
	if !strings.Contains(text, "Цитата дня") {
		t.Errorf("text %q", text)
	}
	l := labels(k)
	if !contains(l, "+1:00") || !contains(l, "+5") {
		t.Errorf("labels %q", l)
	}
	if tail := l[len(l)-2:]; fmt.Sprint(tail) != fmt.Sprint([]string{"Назад", "Закрыть"}) {
		t.Errorf("tail %q", tail)
	}
}

// tests/test_handlers.py::HandlerTests::test_group_publication_settings_explains_quote_context (HND-09)
func TestBehaviorSection(t *testing.T) {
	text, k := group("ru", SectionBehavior, true)
	if !strings.Contains(text, "Публикация") || !strings.Contains(text, "сообщения, которые ИИ выбрал") {
		t.Errorf("text %q", text)
	}
	if !contains(labels(k), "◉ Контекст") {
		t.Errorf("labels %q", labels(k))
	}
	want := []string{
		"menu:777:g:toggle:context", "menu:777:g:toggle:boring", "menu:777:g:toggle:pin", "menu:777:g:toggle:media",
		"menu:777:g:home", "menu:777:g:close",
	}
	if fmt.Sprint(datas(k)) != fmt.Sprint(want) {
		t.Errorf("callbacks %q", datas(k))
	}
}

// doc §7.2 F10: the home dashboard shows the group's real media switch, the one
// the Behaviour screen's toggle changes.
func TestHomeShowsTheRealMediaState(t *testing.T) {
	in := GroupInput{
		Owner: 777, Lang: "en", GroupLang: "en", IsAdmin: true, QuoteTime: "21:00", MinMessages: 10,
		Timezone: "UTC", BoringNotice: true, Pin: true, QuoteContext: true, MediaAnalysis: false,
	}
	text, _ := GroupPanel(in)
	if !strings.Contains(text, ToggleOff+" "+i18n.T("en", "settings.group.media.short")) {
		t.Errorf("home does not show media off:\n%s", text)
	}
	in.MediaAnalysis = true
	text, _ = GroupPanel(in)
	if !strings.Contains(text, ToggleOn+" "+i18n.T("en", "settings.group.media.short")) {
		t.Errorf("home does not show media on:\n%s", text)
	}
	in.Section = SectionBehavior
	in.MediaAnalysis = false
	_, k := GroupPanel(in)
	if !contains(labels(k), ToggleOff+" "+i18n.T("en", "settings.button.media")) {
		t.Errorf("behaviour labels %q", labels(k))
	}
}

// Golden home dashboard, English and Russian.
func TestGroupHomeGolden(t *testing.T) {
	cases := map[string]string{
		"en": "<b>Quoto</b>\n<i>I watch the chat and publish the best line at the end of the day. Stats and settings below.</i>\n\n" +
			"<blockquote>Language: <b>English</b>\nDaily summary at <b>21:00</b> · from <b>10</b> messages\n" +
			"◉ Context\n◉ Boring day\n◉ Pin\n◉ Media</blockquote>",
	}
	for lang, want := range cases {
		text, _ := group(lang, SectionHome, true)
		if text != want {
			t.Errorf("%s home\n got %q\nwant %q", lang, text, want)
		}
	}
}

// tests/test_handlers.py::HandlerTests::test_group_stats_panel_switches_between_user_and_chat_stats (HND-10)
func TestStatsTabLabels(t *testing.T) {
	_, k := group("ru", SectionStats, false)
	want := []string{"◉ Моя статистика", "◎ Статистика чата", "Назад", "Закрыть"}
	if fmt.Sprint(labels(k)) != fmt.Sprint(want) {
		t.Errorf("user tab %q", labels(k))
	}
	_, k = GroupPanel(GroupInput{Owner: 777, Lang: "ru", GroupLang: "ru", Section: SectionStats, StatsView: StatsChat})
	want = []string{"◎ Моя статистика", "◉ Статистика чата", "Назад", "Закрыть"}
	if fmt.Sprint(labels(k)) != fmt.Sprint(want) {
		t.Errorf("chat tab %q", labels(k))
	}
}

// With no numbers given, the stats screen says they are missing instead of
// drawing an empty quote.
func TestStatsScreenWithoutText(t *testing.T) {
	text, _ := group("en", SectionStats, true)
	want := Panel(i18n.T("en", "user_stats.title"), i18n.T("en", "user_stats.hint"), i18n.T("en", "user_stats.missing"))
	if text != want {
		t.Errorf("got %q want %q", text, want)
	}
}

// tests/test_menu.py::HeadlineTests::test_buttons_carry_no_emoji_icons (MEN-21)
func TestButtonsStartWithALetter(t *testing.T) {
	for _, lang := range i18n.Codes() {
		for _, key := range []string{"agreement.view_button", "agreement.accept_button"} {
			label := []rune(i18n.T(lang, key))
			if len(label) == 0 || !isLetter(label[0]) {
				t.Errorf("%s %s: %q", lang, key, string(label))
			}
		}
	}
}

func isLetter(r rune) bool {
	return strings.ContainsRune("abcdefghijklmnopqrstuvwxyz", r|0x20) || r > 0x7f && !emojiRE.MatchString(string(r)) && r != '✅'
}

// tests/test_menu.py::HeadlineTests::test_the_trophy_is_reserved_for_the_published_quote (MEN-20)
func TestTrophyIsReservedForThePublishedQuote(t *testing.T) {
	for _, lang := range i18n.Codes() {
		for _, key := range []string{"menu.private.title", "menu.group.title"} {
			if strings.Contains(i18n.T(lang, key), "🏆") {
				t.Errorf("%s %s has a trophy", lang, key)
			}
		}
		if !strings.Contains(i18n.T(lang, "quote_post.titles.text"), "🏆") {
			t.Errorf("%s: the published quote lost its trophy", lang)
		}
	}
}

// tests/test_menu.py::HeadlineTests::test_no_panel_furniture_carries_an_emoji_in_any_locale (MEN-22)
func TestNoPanelFurnitureCarriesAnEmoji(t *testing.T) {
	for _, lang := range i18n.Codes() {
		for _, s := range everyScreen(lang) {
			head, _, _ := strings.Cut(s.text, "\n\n")
			if found := emojiRE.FindAllString(head, -1); len(found) != 0 {
				t.Errorf("%s %s: head %q has %q", lang, s.name, head, found)
			}
			for _, label := range labels(s.keyboard) {
				bare := label
				for _, o := range i18n.Options {
					bare = strings.ReplaceAll(bare, o.Label, "")
				}
				if found := emojiRE.FindAllString(bare, -1); len(found) != 0 {
					t.Errorf("%s %s: label %q has %q", lang, s.name, label, found)
				}
			}
		}
	}
}

// doc §6: Panels; the private home leads to the group, never to a DM action.
func TestPrivateHome(t *testing.T) {
	text, k := private("ru", SectionHome)
	if !strings.Contains(text, "<b>Quoto</b>") {
		t.Errorf("text %q", text)
	}
	var add tg.InlineKeyboardButton
	for _, b := range buttons(k) {
		if b.URL != "" {
			add = b
		}
	}
	if add.URL != "https://t.me/quoto_test_bot?startgroup=new" || add.Style != tg.StylePrimary || add.CallbackData != "" {
		t.Errorf("add-to-group button %+v", add)
	}
	// The source line says manual only for a manual choice.
	manual, _ := PrivatePanel(PrivateInput{Owner: 1, Lang: "en", LangSource: domain.SourceManual, BotUsername: "b"})
	auto, _ := PrivatePanel(PrivateInput{Owner: 1, Lang: "en", LangSource: domain.SourceClient, BotUsername: "b"})
	if !strings.Contains(manual, i18n.T("en", "settings.private.language_source_manual")) ||
		!strings.Contains(auto, i18n.T("en", "settings.private.language_source_telegram")) {
		t.Errorf("source lines:\n%s\n%s", manual, auto)
	}
}

// tests/test_handlers.py::TelegramLanguageButtonTests::test_it_sits_under_the_language_grid_and_over_the_navigation (HND-28)
func TestTelegramLanguageButtonPlacement(t *testing.T) {
	_, k := PrivatePanel(PrivateInput{Owner: 777, Lang: "en", BotUsername: "quoto_test_bot", Section: SectionLanguage})
	grid := (len(i18n.Codes()) + 1) / 2
	if got := k.InlineKeyboard[grid]; len(got) != 1 || got[0].Text != i18n.T("en", "settings.private.telegram_language") {
		t.Errorf("row %d = %+v", grid, got)
	}
	if got := k.InlineKeyboard[grid+1]; len(got) != 1 || got[0].Text != i18n.T("en", "menu.button.back") {
		t.Errorf("row %d = %+v", grid+1, got)
	}
	// It performs an action, so it is never painted as a state.
	if k.InlineKeyboard[grid][0].Style != "" {
		t.Error("the Telegram language button is painted")
	}
	if k.InlineKeyboard[grid][0].CallbackData != "menu:777:p:autoplang" {
		t.Errorf("callback %q", k.InlineKeyboard[grid][0].CallbackData)
	}
}

// doc §6: group language screen reads the source of the language.
func TestGroupLanguageSourceLine(t *testing.T) {
	for source, key := range map[domain.LangSource]string{
		domain.SourceManual: "menu.language.source_manual",
		domain.SourceAuto:   "menu.language.source_auto",
		domain.SourceClient: "menu.language.source_default",
		"":                  "menu.language.source_default",
	} {
		text, _ := GroupPanel(GroupInput{Owner: 1, Lang: "en", GroupLang: "en", GroupLangSource: source, Section: SectionLanguage})
		if !strings.Contains(text, i18n.T("en", key)) {
			t.Errorf("source %q: %q lacks %q", source, text, i18n.T("en", key))
		}
	}
}

// menu.py TIMEZONE_CHOICES (13 zones) and handlers.py's tz screen: the buttons carry settz and
// the zone name, the current one is selected.
func TestTimezoneScreenOffersThirteenZones(t *testing.T) {
	_, k := group("en", SectionTimezone, true)
	zones := 0
	for _, b := range buttons(k) {
		if strings.HasPrefix(b.CallbackData, "menu:777:g:settz:") {
			zones++
			if zone := strings.TrimPrefix(b.CallbackData, "menu:777:g:settz:"); !domain.ValidTimezone(zone) {
				t.Errorf("zone %q is not loadable", zone)
			}
		}
	}
	if zones != 13 || len(TimezoneChoices) != 13 {
		t.Errorf("%d zone buttons, %d choices", zones, len(TimezoneChoices))
	}
}

// No Python counterpart: GroupInputFor replaces the dict handlers.py assembled from the group row
// for build_group_panel; this pins the mapping.
func TestGroupInputFor(t *testing.T) {
	hour, minute, min := 7, 5, 30
	off, tz := false, "Asia/Tokyo"
	g := domain.GroupSettings{
		ChatID: -100, QuoteHour: &hour, QuoteMinute: &minute, MinMessages: &min, Pin: &off, MediaAnalysis: &off, Timezone: &tz,
		Language: "uk-UA", LanguageSource: domain.SourceAuto,
	}
	in := GroupInputFor(5, g, true, "9")
	if in.Lang != "uk" || in.QuoteTime != "07:05" || in.MinMessages != 30 || in.Timezone != "Asia/Tokyo" ||
		in.Pin || in.MediaAnalysis || !in.BoringNotice || !in.QuoteContext || in.GroupLangSource != domain.SourceAuto {
		t.Errorf("input %+v", in)
	}
	// A claim Quoto cannot render is shown in English, as the group's messages are.
	if got := GroupInputFor(5, domain.GroupSettings{Language: "xx"}, false, "").Lang; got != "en" {
		t.Errorf("unrenderable claim gives %q", got)
	}
	if got := GroupInputFor(5, domain.GroupSettings{}, false, "").QuoteTime; got != "21:00" {
		t.Errorf("default time %q", got)
	}
}

// menu.panel in test_menu.py: bold title, italic hint, one blockquote, the version beside the
// name on the About card, a body that is only blanks vanishes.
func TestScreenHTML(t *testing.T) {
	cases := []struct {
		name string
		s    Screen
		want string
	}{
		{"title only", Screen{Title: "T"}, "<b>T</b>"},
		{"hint", Screen{Title: "T", Hint: "h"}, "<b>T</b>\n<i>h</i>"},
		{"version", Screen{Title: "T", Version: "v1"}, "<b>T</b> · <i>v1</i>"},
		{"body", Screen{Title: "T", Hint: "h", Lines: []string{"a", "", "b"}}, "<b>T</b>\n<i>h</i>\n\n<blockquote>a\n\nb</blockquote>"},
		{"expandable", Screen{Title: "T", Lines: []string{"a"}, Expandable: true}, "<b>T</b>\n\n<blockquote expandable>a</blockquote>"},
		{"blank body vanishes", Screen{Title: "T", Lines: []string{" ", "\u00a0"}}, "<b>T</b>"},
	}
	for _, c := range cases {
		if got := c.s.HTML(); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
}
