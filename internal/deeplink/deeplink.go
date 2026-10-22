// SPDX-License-Identifier: Apache-2.0

// Package deeplink builds and checks the signed links that open a quote's
// details card: https://t.me/<bot>?start=quote_<id>_<signature>. The signature
// is the first 16 hex characters of HMAC-SHA256(BOT_TOKEN, "quote:<id>"), so a
// link cannot be forged for another quote, and rotating the bot token
// invalidates every link already posted. The format is shared with the Python
// version: links in published posts keep working.
package deeplink

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"strconv"
	"strings"
)

// Signer signs and verifies quote payloads for one bot token.
type Signer struct {
	key []byte
}

// NewSigner returns a Signer keyed by the bot token.
func NewSigner(botToken string) Signer { return Signer{key: []byte(botToken)} }

func (s Signer) signature(quoteID int64) string {
	mac := hmac.New(sha256.New, s.key)
	mac.Write([]byte("quote:" + strconv.FormatInt(quoteID, 10)))
	return hex.EncodeToString(mac.Sum(nil))[:16]
}

// Payload is the start parameter for a quote: quote_<id>_<signature>.
func (s Signer) Payload(quoteID int64) string {
	return "quote_" + strconv.FormatInt(quoteID, 10) + "_" + s.signature(quoteID)
}

// URL is the link a post carries.
func (s Signer) URL(botUsername string, quoteID int64) string {
	return "https://t.me/" + botUsername + "?start=" + s.Payload(quoteID)
}

// Parse returns the quote id of a signed payload. ok is false for anything
// else: a wrong shape, a non-ASCII digit, a bad signature.
func (s Signer) Parse(payload string) (id int64, ok bool) {
	parts := strings.Split(payload, "_")
	if len(parts) != 3 || parts[0] != "quote" {
		return 0, false
	}
	id, ok = asciiID(parts[1])
	if !ok {
		return 0, false
	}
	if subtle.ConstantTimeCompare([]byte(parts[2]), []byte(s.signature(id))) != 1 {
		return 0, false
	}
	return id, true
}

// ParseLegacy reads a link from before 0.10.2, which carried no signature
// (quote_<id>). The caller must check chat membership before showing anything.
func ParseLegacy(payload string) (id int64, ok bool) {
	parts := strings.Split(payload, "_")
	if len(parts) != 2 || parts[0] != "quote" {
		return 0, false
	}
	return asciiID(parts[1])
}

// asciiID accepts only ASCII digits. Python's str.isdigit also accepts
// superscripts and other scripts' digits, and then crashes in int(); here those
// are simply not a quote.
func asciiID(s string) (int64, bool) {
	if s == "" {
		return 0, false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return 0, false
		}
	}
	id, err := strconv.ParseInt(s, 10, 64)
	return id, err == nil
}
