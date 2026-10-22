// SPDX-License-Identifier: Apache-2.0

package domain

import (
	"fmt"
	"strconv"
	"strings"
	"time"
	// The runtime image has no zoneinfo, and every group's cutoff and day window
	// are computed in its own zone. Without the embedded database
	// time.LoadLocation fails, Location() falls back to UTC without a word, and
	// the zone picker refuses every name.
	_ "time/tzdata"
)

// Defaults for a group that has not chosen a value. NULL in group_settings
// means "use the default", resolved at read time, so changing a default here
// changes every group that never set one.
const (
	DefaultQuoteHour   = 21
	DefaultQuoteMinute = 0
	// QuoteMinuteJitter spreads new groups over the ten minutes after the
	// cutoff, so they do not all reach the AI at once. It shifts the minute
	// only, never the hour.
	QuoteMinuteJitter = 10

	DefaultTimezone = "Europe/Kyiv"

	DefaultMinMessages = 10
	MinMessagesLowest  = 1
	MinMessagesHighest = 200

	// MaxMessagesPerDailyEval caps what one group's day sends to the AI. The
	// newest messages are kept. Premium chats are exempt.
	MaxMessagesPerDailyEval = 1500
)

// Agreement versions. DocumentVersion is what an admin accepts today;
// MinAcceptedVersion is the oldest acceptance that still counts. Raise the
// second only for a change that needs fresh consent: every group below it
// publishes nothing until an admin accepts again.
const (
	DocumentVersion    = "1.2"
	MinAcceptedVersion = "1.2"
)

// GroupSettings is a row of quoto.group_settings. A nil pointer is NULL.
type GroupSettings struct {
	ChatID   int64
	IsActive bool

	QuoteHour     *int
	QuoteMinute   *int
	MinMessages   *int
	BoringNotice  *bool
	Pin           *bool
	QuoteContext  *bool
	IsPremium     *bool
	Timezone      *string
	MediaAnalysis *bool

	AgreementAcceptedAt *time.Time
	AgreementAcceptedBy *int64
	AgreementLanguage   *string
	AgreementVersion    *string

	// Filled by Groups.Get and Groups.ListActive from core, never persisted
	// here. Name is the chat title; Language is the language claim of any bot,
	// raw (it may be a code Quoto cannot render); LanguageSource says who set it.
	Name           string
	Language       string
	LanguageSource LangSource
}

// QuoteTime is the cutoff of the group, in its time zone.
func (g GroupSettings) QuoteTime() (hour, minute int) {
	h, m := DefaultQuoteHour, DefaultQuoteMinute
	if g.QuoteHour != nil {
		h = *g.QuoteHour
	}
	if g.QuoteMinute != nil {
		m = *g.QuoteMinute
	}
	return PyMod(h, 24), PyMod(m, 60)
}

// MinMessagesEffective is the smallest day that is worth sending to the AI.
func (g GroupSettings) MinMessagesEffective() int {
	if g.MinMessages == nil {
		return DefaultMinMessages
	}
	return Clamp(*g.MinMessages, MinMessagesLowest, MinMessagesHighest)
}

// BoringNoticeEnabled: NULL means on.
func (g GroupSettings) BoringNoticeEnabled() bool { return orTrue(g.BoringNotice) }

// PinEnabled: NULL means on.
func (g GroupSettings) PinEnabled() bool { return orTrue(g.Pin) }

// ContextEnabled: NULL means on.
func (g GroupSettings) ContextEnabled() bool { return orTrue(g.QuoteContext) }

// MediaAnalysisEnabled is the group's own switch. The global MEDIA_ANALYSIS_ENABLED
// is applied by the caller, which owns the config.
func (g GroupSettings) MediaAnalysisEnabled() bool { return orTrue(g.MediaAnalysis) }

func orTrue(b *bool) bool { return b == nil || *b }

// TimezoneName is the group's zone if it names a real one, else the default.
func (g GroupSettings) TimezoneName() string {
	if g.Timezone != nil && ValidTimezone(*g.Timezone) {
		return *g.Timezone
	}
	return DefaultTimezone
}

// Location is TimezoneName loaded.
func (g GroupSettings) Location() *time.Location {
	loc, err := time.LoadLocation(g.TimezoneName())
	if err != nil {
		return time.UTC
	}
	return loc
}

// CheckZones loads every zone the code names itself (the default and the
// per-language guesses). The bot calls it at start: a build that cannot load
// them would treat every group as UTC.
func CheckZones() error {
	names := []string{DefaultTimezone}
	for _, tz := range LanguageTimezones {
		names = append(names, tz)
	}
	for _, name := range names {
		if _, err := time.LoadLocation(name); err != nil {
			return fmt.Errorf("time zone %s is not available: %w", name, err)
		}
	}
	return nil
}

// ValidTimezone reports whether name is an IANA zone this build knows.
func ValidTimezone(name string) bool {
	if name == "" || name == "Local" {
		return false
	}
	_, err := time.LoadLocation(name)
	return err == nil
}

// Premium: the row says so, or the chat is in PREMIUM_CHAT_IDS.
func (g GroupSettings) Premium(premiumChats map[int64]bool) bool {
	return (g.IsPremium != nil && *g.IsPremium) || premiumChats[g.ChatID]
}

// MessageCap is the most messages one day sends to the AI; 0 means no cap.
func (g GroupSettings) MessageCap(premiumChats map[int64]bool) int {
	if g.Premium(premiumChats) {
		return 0
	}
	return MaxMessagesPerDailyEval
}

// AgreementAccepted: accepted at some time, in a version that still counts. A
// NULL version is an acceptance before 1.2 and does not count.
func (g GroupSettings) AgreementAccepted() bool {
	return g.AgreementAcceptedAt != nil && VersionCounts(g.AgreementVersion)
}

// AgreementOutdated: accepted once, but in a version that no longer counts.
func (g GroupSettings) AgreementOutdated() bool {
	return g.AgreementAcceptedAt != nil && !g.AgreementAccepted()
}

// VersionCounts reports whether an accepted version is at least MinAcceptedVersion,
// compared as integer tuples ("1.10" > "1.2"). NULL, empty and non-numeric
// versions do not count.
func VersionCounts(version *string) bool {
	if version == nil {
		return false
	}
	have, ok := versionKey(*version)
	if !ok {
		return false
	}
	need, _ := versionKey(MinAcceptedVersion)
	return compareKeys(have, need) >= 0
}

func versionKey(v string) ([]int, bool) {
	v = strings.TrimSpace(v)
	if v == "" {
		return nil, false
	}
	parts := strings.Split(v, ".")
	key := make([]int, len(parts))
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return nil, false
		}
		key[i] = n
	}
	return key, true
}

// compareKeys orders version tuples like Python orders tuples: element by
// element, and a prefix is smaller than the longer tuple ((1,2) < (1,2,0)).
func compareKeys(a, b []int) int {
	for i := 0; i < len(a) && i < len(b); i++ {
		if a[i] != b[i] {
			if a[i] < b[i] {
				return -1
			}
			return 1
		}
	}
	switch {
	case len(a) < len(b):
		return -1
	case len(a) > len(b):
		return 1
	}
	return 0
}

// PyMod is Python's % for a positive modulus: the result is never negative.
func PyMod(a, n int) int { return ((a % n) + n) % n }

// ClampMinMessages bounds a requested minimum to what the panel allows.
func ClampMinMessages(v int) int { return Clamp(v, MinMessagesLowest, MinMessagesHighest) }

// NormalizeQuoteTime wraps a time of day given in minutes into one day.
func NormalizeQuoteTime(hour, minute int) (int, int) {
	total := PyMod(hour*60+minute, 24*60)
	return total / 60, total % 60
}

// LanguageTimezones maps the language an AI run detected for a new group to the
// time zone it most likely lives in; any other language gets DefaultTimezone.
// The mapping is a guess kept from the Python version.
var LanguageTimezones = map[string]string{
	"ru": "Europe/Moscow",
	"uk": "Europe/Kyiv",
	"en": "UTC",
	"de": "Europe/Berlin",
}

// DefaultTimezoneForLanguage returns the guessed zone for a detected language.
func DefaultTimezoneForLanguage(lang string) string {
	if tz, ok := LanguageTimezones[lang]; ok {
		return tz
	}
	return DefaultTimezone
}

// ToggleKey names a boolean group setting on the panel.
type ToggleKey string

const (
	ToggleContext ToggleKey = "context"
	ToggleBoring  ToggleKey = "boring"
	TogglePin     ToggleKey = "pin"
	ToggleMedia   ToggleKey = "media"
)

// ParseToggle returns the key for the callback payload, or false for an
// unknown one (which the panel ignores without an error).
func ParseToggle(s string) (ToggleKey, bool) {
	switch k := ToggleKey(s); k {
	case ToggleContext, ToggleBoring, TogglePin, ToggleMedia:
		return k, true
	}
	return "", false
}

// Enabled reads the effective value of the setting k.
func (g GroupSettings) Enabled(k ToggleKey) bool {
	switch k {
	case ToggleContext:
		return g.ContextEnabled()
	case ToggleBoring:
		return g.BoringNoticeEnabled()
	case TogglePin:
		return g.PinEnabled()
	case ToggleMedia:
		return g.MediaAnalysisEnabled()
	}
	return false
}
