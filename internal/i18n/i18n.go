// SPDX-License-Identifier: Apache-2.0

// Package i18n holds Quoto's user-facing text. Strings live in
// translations.json (key -> {lang: value}) embedded at build time, so no text
// sits inline in Go. A value is a string, or structured data where the text is
// a document (the agreement sections, the month names).
//
// T(lang, key, pairs...) returns the localized string with {placeholder}
// interpolation, falling back to English. The language set is the family's: a
// person who picked a language in one bot is understood by the rest.
package i18n

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

const DefaultLang = "en"

//go:embed translations.json
var translationsJSON []byte

// translations[key][lang] = string, []any or map[string]any
var translations map[string]map[string]any

func init() {
	if err := json.Unmarshal(translationsJSON, &translations); err != nil {
		panic("i18n: invalid translations.json: " + err.Error())
	}
}

// Option is a selectable language for a picker.
type Option struct {
	Code  string
	Label string // flag + native name, e.g. "🇬🇧 English"
}

// Options is the picker order, the same in every bot of the family.
var Options = []Option{
	{"en", "🇬🇧 English"},
	{"ru", "🇷🇺 Русский"},
	{"uk", "🇺🇦 Українська"},
	{"es", "🇪🇸 Español"},
	{"fr", "🇫🇷 Français"},
	{"de", "🇩🇪 Deutsch"},
	{"it", "🇮🇹 Italiano"},
	{"pl", "🇵🇱 Polski"},
	{"cs", "🇨🇿 Čeština"},
	{"tr", "🇹🇷 Türkçe"},
	{"sv", "🇸🇪 Svenska"},
	{"be", "🇧🇾 Беларуская"},
	{"ca", "🇦🇩 Català"},
	{"zh", "🇨🇳 中文"},
	{"ja", "🇯🇵 日本語"},
	{"ar", "🇦🇪 العربية"},
}

// promptOrder is the order the day-evaluation prompt lists languages in. It is
// not the picker order and must not change: the prompt text is compared
// byte for byte against the Python version.
var promptOrder = []string{"ru", "uk", "en", "de", "es", "fr", "it", "pl", "cs", "tr", "sv", "be", "ca", "zh", "ja", "ar"}

var supported = func() map[string]bool {
	m := make(map[string]bool, len(Options))
	for _, o := range Options {
		m[o.Code] = true
	}
	return m
}()

var aliases = map[string]string{
	"rus": "ru", "ru-ru": "ru", "russian": "ru",
	"ук": "uk", "ukr": "uk", "ua": "uk", "uk-ua": "uk", "ukrainian": "uk",
	"eng": "en", "en-us": "en", "en-gb": "en", "english": "en",
	"ger": "de", "de-de": "de", "deutsch": "de", "german": "de",
}

// Supported reports whether code is one of the sixteen languages, as is.
func Supported(code string) bool { return supported[code] }

// Codes lists every supported code in picker order.
func Codes() []string {
	out := make([]string, len(Options))
	for i, o := range Options {
		out[i] = o.Code
	}
	return out
}

// Normalize maps what a person, Telegram or another bot wrote ("en-US", "uk_UA",
// "Deutsch") to one of the supported codes. ok is false for anything else, and
// then nothing may be written to the shared language store.
func Normalize(value string) (code string, ok bool) {
	v := strings.ToLower(strings.TrimSpace(value))
	v = strings.ReplaceAll(v, "_", "-")
	if v == "" {
		return "", false
	}
	if alias, found := aliases[v]; found {
		v = alias
	} else if i := strings.Index(v, "-"); i >= 0 {
		v = v[:i]
	}
	if supported[v] {
		return v, true
	}
	return "", false
}

// OrDefault is Normalize with English for what cannot be rendered.
func OrDefault(value string) string {
	if code, ok := Normalize(value); ok {
		return code
	}
	return DefaultLang
}

// Name is the bare native name, for running text.
func Name(code string) string {
	lang := OrDefault(code)
	if s, ok := translations["language_name"][lang].(string); ok && s != "" {
		return s
	}
	return lang
}

// Label is the button label: flag plus native name.
func Label(code string) string {
	lang := OrDefault(code)
	for _, o := range Options {
		if o.Code == lang {
			return o.Label
		}
	}
	return Name(lang)
}

// PromptList renders "ru=Русский, uk=Українська, ..." for the language
// detection prompt.
func PromptList() string {
	parts := make([]string, len(promptOrder))
	for i, code := range promptOrder {
		parts[i] = code + "=" + Name(code)
	}
	return strings.Join(parts, ", ")
}

// PromptCodes lists the codes in the order PromptList uses.
func PromptCodes() []string { return append([]string(nil), promptOrder...) }

// T returns the localized string for key in lang, interpolating {name}
// placeholders from pairs (name, value, name, value, ...). A missing language
// falls back to English; a missing key returns the key itself, as Python did,
// so a gap is visible instead of blank.
func T(lang, key string, pairs ...string) string {
	text, ok := Value(lang, key).(string)
	if !ok {
		return key
	}
	if len(pairs) >= 2 {
		repl := make([]string, 0, len(pairs))
		for i := 0; i+1 < len(pairs); i += 2 {
			repl = append(repl, "{"+pairs[i]+"}", pairs[i+1])
		}
		text = strings.NewReplacer(repl...).Replace(text)
	}
	return text
}

// Value returns the entry for key in lang with the same English fallback as T.
// Strings come back as string, lists as []any, objects as map[string]any; nil
// means the key does not exist at all.
func Value(lang, key string) any {
	byLang, ok := translations[key]
	if !ok {
		return nil
	}
	code := OrDefault(lang)
	if v, ok := byLang[code]; ok && v != nil && v != "" {
		return v
	}
	if v, ok := byLang[DefaultLang]; ok {
		return v
	}
	return nil
}

// Keys lists every translation key, sorted. Tests use it to assert coverage.
func Keys() []string {
	out := make([]string, 0, len(translations))
	for k := range translations {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Languages reports which languages have an entry for key.
func Languages(key string) []string {
	var out []string
	for code := range translations[key] {
		out = append(out, code)
	}
	sort.Strings(out)
	return out
}

// MonthName returns the month's name in lang; the number if the table is gone.
func MonthName(lang string, month time.Month) string {
	if months, ok := Value(lang, "months").([]any); ok && month >= 1 && int(month) <= len(months) {
		if s, ok := months[month-1].(string); ok {
			return s
		}
	}
	return strconv.Itoa(int(month))
}

// FormatDate writes a date the way the language writes one. The order of day,
// month and year lives in the locale next to the month names: Spanish needs
// prepositions, German puts a period after the day, Chinese starts with the
// year.
func FormatDate(lang string, day time.Time, withYear bool) string {
	key := "date.day_month"
	if withYear {
		key = "date.day_month_year"
	}
	return T(lang, key,
		"day", strconv.Itoa(day.Day()),
		"month", MonthName(lang, day.Month()),
		"year", strconv.Itoa(day.Year()))
}

// Score formats a rating out of ten the way every card shows it: one decimal.
func Score(v float64) string { return fmt.Sprintf("%.1f", v) }
