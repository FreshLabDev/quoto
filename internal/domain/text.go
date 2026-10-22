// SPDX-License-Identifier: Apache-2.0

package domain

import (
	"strings"
	"unicode"
	"unicode/utf16"
	"unicode/utf8"
)

// EscapeHTML escapes like Python's html.escape(s, quote=True). Go's
// html.EscapeString writes &#34; and &#39; where Python writes &quot; and
// &#x27;, and posts are compared byte for byte.
func EscapeHTML(s string) string {
	if !strings.ContainsAny(s, `&<>"'`) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s) + 16)
	for _, r := range s {
		switch r {
		case '&':
			b.WriteString("&amp;")
		case '<':
			b.WriteString("&lt;")
		case '>':
			b.WriteString("&gt;")
		case '"':
			b.WriteString("&quot;")
		case '\'':
			b.WriteString("&#x27;")
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// isPySpace matches str.isspace(): Unicode White_Space plus the four ASCII
// separator controls U+001C to U+001F, which unicode.IsSpace leaves out.
func isPySpace(r rune) bool { return unicode.IsSpace(r) || (r >= 0x1c && r <= 0x1f) }

// PyStrip is Python's str.strip() with no argument.
func PyStrip(s string) string { return strings.TrimFunc(s, isPySpace) }

// PyRStrip is Python's str.rstrip() with no argument.
func PyRStrip(s string) string { return strings.TrimRightFunc(s, isPySpace) }

// RuneLen is Python's len(str): code points, not bytes.
func RuneLen(s string) int { return utf8.RuneCountInString(s) }

// UTF16Len is the length Telegram counts entity offsets and lengths in.
func UTF16Len(s string) int {
	n := 0
	for _, r := range s {
		n += len(utf16.Encode([]rune{r}))
	}
	return n
}

// Truncate cuts s to at most limit code points, ending in an ellipsis when it
// had to cut: strip, keep if it fits, else s[:limit-1].rstrip() + "…".
func Truncate(s string, limit int) string {
	s = PyStrip(s)
	if limit <= 0 {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= limit {
		return s
	}
	return PyRStrip(string(runes[:limit-1])) + "…"
}

// Clamp bounds v to [lo, hi].
func Clamp(v, lo, hi int) int { return max(lo, min(hi, v)) }
