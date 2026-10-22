// SPDX-License-Identifier: Apache-2.0

package deeplink

import (
	"strconv"
	"testing"
)

// The signatures below were produced by the Python version
// (hmac.new(token, f"quote:{id}", sha256).hexdigest()[:16]).
func TestPayloadMatchesPython(t *testing.T) {
	s := NewSigner("123456:ABC-DEF_test")
	for id, sig := range map[int64]string{
		1:          "47213f630de957cd",
		7:          "d23dbc58327dd2e9",
		42:         "628c8196b67ff4d4",
		9999999999: "6170574553979eb3",
	} {
		want := "quote_" + strconv.FormatInt(id, 10) + "_" + sig
		if got := s.Payload(id); got != want {
			t.Errorf("payload(%d) = %s, want %s", id, got, want)
		}
		if got, ok := s.Parse(want); !ok || got != id {
			t.Errorf("parse(%s) = %d %t", want, got, ok)
		}
	}
	if got := s.URL("quototbot", 42); got != "https://t.me/quototbot?start=quote_42_628c8196b67ff4d4" {
		t.Errorf("url %s", got)
	}
}

func TestParseRejects(t *testing.T) {
	s := NewSigner("123456:ABC-DEF_test")
	for name, payload := range map[string]string{
		"empty":             "",
		"no signature":      "quote_42",
		"bad signature":     "quote_42_0000000000000000",
		"other id":          "quote_43_628c8196b67ff4d4",
		"uppercase":         "quote_42_628C8196B67FF4D4",
		"extra part":        "quote_42_628c8196b67ff4d4_x",
		"wrong prefix":      "post_42_628c8196b67ff4d4",
		"arabic-indic id":   "quote_٤٢_628c8196b67ff4d4",
		"superscript id":    "quote_4²_628c8196b67ff4d4",
		"signed id":         "quote_-42_628c8196b67ff4d4",
		"overflow":          "quote_99999999999999999999_628c8196b67ff4d4",
		"other token's key": NewSigner("999999:other").Payload(42),
	} {
		if id, ok := s.Parse(payload); ok {
			t.Errorf("%s: accepted as %d", name, id)
		}
	}
}

func TestLeadingZerosParseLikePython(t *testing.T) {
	s := NewSigner("123456:ABC-DEF_test")
	// int("007") is 7 in Python, and the signature is checked for 7.
	if id, ok := s.Parse("quote_007_d23dbc58327dd2e9"); !ok || id != 7 {
		t.Errorf("got %d %t", id, ok)
	}
}

func TestParseLegacy(t *testing.T) {
	for payload, want := range map[string]int64{"quote_5": 5, "quote_0012": 12} {
		if id, ok := ParseLegacy(payload); !ok || id != want {
			t.Errorf("%s: %d %t", payload, id, ok)
		}
	}
	for _, payload := range []string{"quote_", "quote_5_abc", "quote_x", "quote_٣", "post_5", ""} {
		if _, ok := ParseLegacy(payload); ok {
			t.Errorf("%q accepted", payload)
		}
	}
}
