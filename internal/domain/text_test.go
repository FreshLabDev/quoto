// SPDX-License-Identifier: Apache-2.0

package domain

import "testing"

func TestEscapeHTMLMatchesPython(t *testing.T) {
	in := `<a href="x">Tom & 'Jerry'</a>`
	want := `&lt;a href=&quot;x&quot;&gt;Tom &amp; &#x27;Jerry&#x27;&lt;/a&gt;`
	if got := EscapeHTML(in); got != want {
		t.Errorf("got %q want %q", got, want)
	}
	if EscapeHTML("plain текст") != "plain текст" {
		t.Error("plain text changed")
	}
}

func TestTruncate(t *testing.T) {
	cases := []struct {
		in    string
		limit int
		want  string
	}{
		{"  short  ", 10, "short"},
		{"exactly ten", 11, "exactly ten"},
		{"abcdefghij", 5, "abcd…"},
		{"ab cdefghij", 4, "ab…"}, // rstrip after the cut: "ab " -> "ab"
		{"привет мир как дела", 8, "привет …"[:len("привет")] + "…"},
		{"😀😀😀😀😀", 3, "😀😀…"},
	}
	for _, tc := range cases {
		if got := Truncate(tc.in, tc.limit); got != tc.want {
			t.Errorf("Truncate(%q, %d) = %q, want %q", tc.in, tc.limit, got, tc.want)
		}
	}
}

func TestPyStripCoversSeparatorControls(t *testing.T) {
	if got := PyStrip("\x1c a \x1f "); got != "a" {
		t.Errorf("got %q", got)
	}
}

func TestLengths(t *testing.T) {
	if RuneLen("a😀я") != 3 {
		t.Error("RuneLen")
	}
	if UTF16Len("a😀я") != 4 {
		t.Errorf("UTF16Len = %d", UTF16Len("a😀я"))
	}
}
