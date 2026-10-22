// SPDX-License-Identifier: Apache-2.0

package ai

import (
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/FreshLabDev/quoto/internal/domain"
)

// Caps on what a member of the chat can put in front of the model (cost and
// abuse safety), in runes. The reason text and the language name are capped
// again on the way back, because Claude's schema cannot carry maxLength.
const (
	maxMessageText  = 2000
	maxAuthorName   = 128
	maxMediaDesc    = 1500
	maxReasonText   = 200
	maxChatLanguage = 80
)

// userPayload is the user turn: a JSON array of compact message objects, with
// the bytes Python's json.dumps(list, ensure_ascii=False) wrote. The separators
// are ", " and ": ", the key order is i, a, t, caption, kind, desc, rp, re, and
// a field with nothing to say is left out. Any change moves the token count of
// every request, so this is hand-written instead of going through encoding/json
// (a map loses the order, and json.Marshal escapes U+2028 and writes other
// control bytes differently).
func userPayload(msgs []domain.EvalMessage) string {
	var b []byte
	b = append(b, '[')
	for i, m := range msgs {
		if i > 0 {
			b = append(b, ", "...)
		}
		b = appendMessage(b, m)
	}
	b = append(b, ']')
	return string(b)
}

func appendMessage(b []byte, m domain.EvalMessage) []byte {
	b = append(b, `{"i": `...)
	b = strconv.AppendInt(b, m.ID, 10)
	b = append(b, `, "a": `...)
	b = appendString(b, clip(m.Author, maxAuthorName))
	if m.Text != "" {
		b = append(b, `, "t": `...)
		b = appendString(b, clip(m.Text, maxMessageText))
	}
	if m.Caption != "" {
		b = append(b, `, "caption": `...)
		b = appendString(b, clip(m.Caption, maxMessageText))
	}
	if m.Kind != "" && m.Kind != domain.ContentText {
		b = append(b, `, "kind": `...)
		b = appendString(b, m.Kind)
	}
	if m.Desc != "" {
		b = append(b, `, "desc": `...)
		b = appendString(b, clip(m.Desc, maxMediaDesc))
	}
	if m.ReplyToID != 0 {
		b = append(b, `, "rp": `...)
		b = strconv.AppendInt(b, m.ReplyToID, 10)
	}
	if re := positiveReactions(m.Reactions); len(re) > 0 {
		b = append(b, `, "re": {`...)
		for i, r := range re {
			if i > 0 {
				b = append(b, ", "...)
			}
			b = appendString(b, r.Emoji)
			b = append(b, ": "...)
			b = strconv.AppendInt(b, int64(r.Count), 10)
		}
		b = append(b, '}')
	}
	return append(b, '}')
}

// positiveReactions drops zero counts. The caller already does, so this only
// guards the contract of domain.EvalMessage.
func positiveReactions(in []domain.Reaction) []domain.Reaction {
	for _, r := range in {
		if r.Count <= 0 {
			out := make([]domain.Reaction, 0, len(in))
			for _, r := range in {
				if r.Count > 0 {
					out = append(out, r)
				}
			}
			return out
		}
	}
	return in
}

// clip cuts s to n runes, as Python's text[:n] cuts code points. Bytes that are
// not UTF-8 become U+FFFD first, so the cut and the JSON are both well defined.
func clip(s string, n int) string {
	if !utf8.ValidString(s) {
		s = strings.ToValidUTF8(s, string(utf8.RuneError))
	}
	if len(s) <= n { // at most one byte per rune: nothing to cut
		return s
	}
	count := 0
	for i := range s {
		if count == n {
			return s[:i]
		}
		count++
	}
	return s
}

const hexDigits = "0123456789abcdef"

// appendString writes s as Python's json.dumps(ensure_ascii=False) does: only
// the quote, the backslash and the control characters below U+0020 are escaped.
// \b, \f, \n, \r and \t have short forms, the rest are \u00xx in lower case.
// DEL, U+2028 and U+2029 and the HTML characters stay as they are.
func appendString(b []byte, s string) []byte {
	if !utf8.ValidString(s) {
		s = strings.ToValidUTF8(s, string(utf8.RuneError))
	}
	b = append(b, '"')
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '"':
			b = append(b, '\\', '"')
		case c == '\\':
			b = append(b, '\\', '\\')
		case c == '\n':
			b = append(b, '\\', 'n')
		case c == '\r':
			b = append(b, '\\', 'r')
		case c == '\t':
			b = append(b, '\\', 't')
		case c == '\b':
			b = append(b, '\\', 'b')
		case c == '\f':
			b = append(b, '\\', 'f')
		case c < 0x20:
			b = append(b, '\\', 'u', '0', '0', hexDigits[c>>4], hexDigits[c&0xf])
		default:
			b = append(b, c)
		}
	}
	return append(b, '"')
}
