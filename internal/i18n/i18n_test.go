// SPDX-License-Identifier: Apache-2.0

package i18n

import (
	"regexp"
	"testing"
	"time"
)

var placeholder = regexp.MustCompile(`\{[a-z_]+\}`)

// Every key exists in every language, and a string carries the same
// placeholders everywhere (tests/test_i18n.py parity).
func TestLocaleParity(t *testing.T) {
	for _, key := range Keys() {
		for _, code := range Codes() {
			if _, ok := translations[key][code]; !ok {
				t.Errorf("%s: missing %s", key, code)
			}
		}
		en, isString := translations[key]["en"].(string)
		if !isString {
			continue
		}
		want := map[string]bool{}
		for _, p := range placeholder.FindAllString(en, -1) {
			want[p] = true
		}
		for _, code := range Codes() {
			s, _ := translations[key][code].(string)
			got := map[string]bool{}
			for _, p := range placeholder.FindAllString(s, -1) {
				got[p] = true
			}
			if len(got) != len(want) {
				t.Errorf("%s/%s: placeholders %v, English has %v", key, code, got, want)
				continue
			}
			for p := range want {
				if !got[p] {
					t.Errorf("%s/%s: lacks %s", key, code, p)
				}
			}
		}
	}
}

func TestNormalize(t *testing.T) {
	cases := map[string]string{
		"en": "en", "EN-us": "en", "en_GB": "en", " ru ": "ru", "ukr": "uk", "ua": "uk", "ук": "uk",
		"uk-UA": "uk", "Deutsch": "de", "german": "de", "zh-Hans": "zh", "pt": "", "": "", "xx-yy": "", "fr_CA": "fr",
	}
	for in, want := range cases {
		got, ok := Normalize(in)
		if got != want || ok != (want != "") {
			t.Errorf("Normalize(%q) = %q, %v; want %q", in, got, ok, want)
		}
	}
	if OrDefault("pt") != "en" || OrDefault("ja") != "ja" {
		t.Error("OrDefault")
	}
}

func TestTFallbackAndInterpolation(t *testing.T) {
	if got := T("xx", "about.tagline"); got != T("en", "about.tagline") {
		t.Errorf("unknown language did not fall back: %q", got)
	}
	if got := T("en", "no.such.key"); got != "no.such.key" {
		t.Errorf("missing key: %q", got)
	}
	got := T("en", "stats.author_row", "medal", "🥇", "name", "Ann", "wins", "3", "score", "8.5")
	want := "🥇 <b>Ann</b> — 3 · 8.5/10"
	if got != want {
		t.Errorf("got %q want %q", got, want)
	}
}

func TestFormatDate(t *testing.T) {
	d := time.Date(2026, time.March, 27, 0, 0, 0, 0, time.UTC)
	if got := FormatDate("en", d, false); got != "27 March" {
		t.Errorf("en: %q", got)
	}
	if got := FormatDate("ru", d, false); got != "27 марта" {
		t.Errorf("ru: %q", got)
	}
	if got := FormatDate("en", d, true); got == "" {
		t.Error("empty year form")
	}
}

func TestPromptListOrder(t *testing.T) {
	want := "ru=Русский, uk=Українська, en=English, de=Deutsch, es=Español, fr=Français, it=Italiano, pl=Polski, " +
		"cs=Čeština, tr=Türkçe, sv=Svenska, be=Беларуская, ca=Català, zh=中文, ja=日本語, ar=العربية"
	if got := PromptList(); got != want {
		t.Errorf("got  %q\nwant %q", got, want)
	}
}

func TestStructuredValues(t *testing.T) {
	sections, ok := Value("de", "agreement.sections").([]any)
	if !ok || len(sections) != 7 {
		t.Fatalf("agreement.sections: %T len %d", Value("de", "agreement.sections"), len(sections))
	}
	if MonthName("en", time.October) != "October" || MonthName("en", 13) != "13" {
		t.Error("MonthName")
	}
}

func TestDroppedKeys(t *testing.T) {
	for _, key := range []string{"private.hello", "start.group"} {
		if _, ok := translations[key]; ok {
			t.Errorf("%s should be gone", key)
		}
	}
}
