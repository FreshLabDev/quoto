// SPDX-License-Identifier: Apache-2.0

package domain

import (
	"reflect"
	"testing"
	"time"
)

func ptr[T any](v T) *T { return &v }

func TestVersionCounts(t *testing.T) {
	cases := []struct {
		v    string
		want bool
	}{
		{"", false}, {"abc", false}, {"1", false}, {"1.1", false}, {"1.x", false},
		{"1.2", true}, {"1.2.0", true}, {"1.10", true}, {"2", true}, {"2.0", true},
	}
	for _, tc := range cases {
		if got := VersionCounts(ptr(tc.v)); got != tc.want {
			t.Errorf("VersionCounts(%q) = %v, want %v", tc.v, got, tc.want)
		}
	}
	if VersionCounts(nil) {
		t.Error("a NULL version does not count")
	}
}

func TestAgreementGate(t *testing.T) {
	now := time.Now()
	accepted := GroupSettings{AgreementAcceptedAt: &now, AgreementVersion: ptr("1.2")}
	if !accepted.AgreementAccepted() || accepted.AgreementOutdated() {
		t.Error("1.2 accepted")
	}
	old := GroupSettings{AgreementAcceptedAt: &now}
	if old.AgreementAccepted() || !old.AgreementOutdated() {
		t.Error("NULL version is outdated, not accepted")
	}
	never := GroupSettings{}
	if never.AgreementAccepted() || never.AgreementOutdated() {
		t.Error("never accepted is neither")
	}
}

func TestGroupDefaults(t *testing.T) {
	g := GroupSettings{ChatID: -1001}
	if h, m := g.QuoteTime(); h != 21 || m != 0 {
		t.Errorf("default cutoff %d:%d", h, m)
	}
	if g.MinMessagesEffective() != 10 || !g.BoringNoticeEnabled() || !g.PinEnabled() || !g.ContextEnabled() || !g.MediaAnalysisEnabled() {
		t.Error("NULL means default")
	}
	if g.TimezoneName() != "Europe/Kyiv" {
		t.Error("default zone")
	}
	g.Timezone = ptr("Mars/Olympus")
	if g.TimezoneName() != "Europe/Kyiv" {
		t.Error("invalid zone falls back")
	}
	g.Timezone = ptr("Europe/Berlin")
	g.QuoteHour, g.QuoteMinute, g.MinMessages = ptr(27), ptr(-5), ptr(999)
	if h, m := g.QuoteTime(); h != 3 || m != 55 {
		t.Errorf("wrapped cutoff %d:%d", h, m)
	}
	if g.MinMessagesEffective() != 200 {
		t.Error("clamped to 200")
	}
	g.BoringNotice = ptr(false)
	if g.BoringNoticeEnabled() || g.Enabled(ToggleBoring) {
		t.Error("explicit off")
	}
}

func TestPremiumAndCap(t *testing.T) {
	g := GroupSettings{ChatID: -1001}
	if g.MessageCap(nil) != 1500 {
		t.Error("cap")
	}
	if g.MessageCap(map[int64]bool{-1001: true}) != 0 {
		t.Error("env premium has no cap")
	}
	g.IsPremium = ptr(true)
	if g.MessageCap(nil) != 0 {
		t.Error("row premium has no cap")
	}
}

func TestQuoteTimeArithmetic(t *testing.T) {
	cases := []struct{ h, m, wantH, wantM int }{
		{21, 0 - 60, 20, 0}, {0, -15, 23, 45}, {23, 59 + 2, 0, 1}, {21, 0, 21, 0},
	}
	for _, tc := range cases {
		h, m := NormalizeQuoteTime(tc.h, tc.m)
		if h != tc.wantH || m != tc.wantM {
			t.Errorf("NormalizeQuoteTime(%d,%d) = %d:%d, want %d:%d", tc.h, tc.m, h, m, tc.wantH, tc.wantM)
		}
	}
}

func TestDefaultTimezoneForLanguage(t *testing.T) {
	want := map[string]string{"ru": "Europe/Moscow", "uk": "Europe/Kyiv", "en": "UTC", "de": "Europe/Berlin", "fr": "Europe/Kyiv", "ja": "Europe/Kyiv"}
	for lang, tz := range want {
		if got := DefaultTimezoneForLanguage(lang); got != tz {
			t.Errorf("%s -> %s, want %s", lang, got, tz)
		}
	}
}

func TestContextCodecRoundTripAndTolerance(t *testing.T) {
	entries := []ContextEntry{
		{MessageID: 11, Author: "Аня", Text: "привет <b> & \"q\"", IsPrimary: false},
		{MessageID: 12, Author: "Bob", Text: "main", IsPrimary: true},
	}
	snapshot, ids := EncodeContext(entries)
	if ids != "[11,12]" {
		t.Errorf("ids %q", ids)
	}
	if got := DecodeContext(snapshot); !reflect.DeepEqual(got, entries) {
		t.Errorf("round trip: %+v", got)
	}
	if s, i := EncodeContext(entries[:1]); s != "" || i != "" {
		t.Error("a single message stores no context")
	}
	for _, bad := range []string{"", "not json", `{"a":1}`, `[1,2]`, `[{"message_id":1,"text":"  "}]`} {
		if got := DecodeContext(bad); len(got) != 0 {
			t.Errorf("DecodeContext(%q) = %+v", bad, got)
		}
	}
	got := DecodeContext(`[{"message_id":5,"text":"x"},{"message_id":6,"author":"A","text":"y","is_primary":true}]`)
	if len(got) != 2 || got[0].Author != "" || !got[1].IsPrimary {
		t.Errorf("tolerant read: %+v", got)
	}
}
