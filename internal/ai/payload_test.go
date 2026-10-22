// SPDX-License-Identifier: Apache-2.0

package ai

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/FreshLabDev/quoto/internal/domain"
)

// payloadCase is one entry of testdata/payload_cases.json: the dict shape
// scoring.py builds, and what json.dumps(ensure_ascii=False) gave for it.
type payloadCase struct {
	Name     string `json:"name"`
	Messages []struct {
		ID        int64           `json:"id"`
		Author    string          `json:"author"`
		Text      string          `json:"text"`
		Caption   string          `json:"caption"`
		Kind      string          `json:"kind"`
		Desc      string          `json:"desc"`
		ReplyToID int64           `json:"reply_to_id"`
		Reactions json.RawMessage `json:"reactions"`
	} `json:"messages"`
	Expected string `json:"expected"`
}

// orderedReactions reads {"emoji": count, ...} keeping the order of the keys.
func orderedReactions(t *testing.T, raw json.RawMessage) []domain.Reaction {
	t.Helper()
	if len(raw) == 0 {
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	if _, err := dec.Token(); err != nil {
		t.Fatal(err)
	}
	var out []domain.Reaction
	for dec.More() {
		key, err := dec.Token()
		if err != nil {
			t.Fatal(err)
		}
		var n int
		if err := dec.Decode(&n); err != nil {
			t.Fatal(err)
		}
		out = append(out, domain.Reaction{Emoji: key.(string), Count: n})
	}
	return out
}

// doc section 6 "Python semantics" and "JSON key order in payloads": the payload
// is the string Python's json.dumps(list, ensure_ascii=False) wrote, for the
// cases in testdata/payload_cases.json. Replaces tests/test_ai.py::
// test_message_payload_* (AI-12, AI-25) and pins the escaping on top of them.
func TestUserPayloadMatchesPython(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "payload_cases.json"))
	if err != nil {
		t.Fatal(err)
	}
	var cases []payloadCase
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	if len(cases) < 9 {
		t.Fatalf("only %d cases", len(cases))
	}
	for _, tc := range cases {
		t.Run(tc.Name, func(t *testing.T) {
			var msgs []domain.EvalMessage
			for _, m := range tc.Messages {
				msgs = append(msgs, domain.EvalMessage{
					ID: m.ID, Author: m.Author, Text: m.Text, Caption: m.Caption, Kind: m.Kind, Desc: m.Desc,
					ReplyToID: m.ReplyToID, Reactions: orderedReactions(t, m.Reactions),
				})
			}
			if got := userPayload(msgs); got != tc.Expected {
				t.Errorf("payload differs\n got: %s\nwant: %s", got, tc.Expected)
			}
		})
	}
}

// tests/test_ai.py::test_message_payload_truncates_long_fields (AI-25): the text
// is cut to 2000 and the author to 128, counted in runes.
func TestPayloadClipsInRunes(t *testing.T) {
	got := userPayload([]domain.EvalMessage{{ID: 1, Author: strings.Repeat("a", 500), Text: strings.Repeat("x", 5000), Caption: strings.Repeat("c", 5000), Desc: strings.Repeat("d", 5000)}})
	var arr []map[string]any
	if err := json.Unmarshal([]byte(got), &arr); err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]int{"a": maxAuthorName, "t": maxMessageText, "caption": maxMessageText, "desc": maxMediaDesc} {
		if n := utf8.RuneCountInString(arr[0][key].(string)); n != want {
			t.Errorf("%s has %d runes, want %d", key, n, want)
		}
	}
}

// Edges of clip: shorter than the limit, exactly the limit, multi-byte runes,
// and bytes that are not UTF-8.
func TestClip(t *testing.T) {
	bad := "ab\xffcd"
	cases := []struct {
		in   string
		n    int
		want string
	}{
		{"", 5, ""},
		{"abc", 5, "abc"},
		{"abcde", 5, "abcde"},
		{"abcdef", 5, "abcde"},
		{"яяяяяя", 5, "яяяяя"},
		{"😀😀😀", 2, "😀😀"},
		{"日本語", 3, "日本語"},
		{bad, 3, "ab" + string(utf8.RuneError)},
		{bad, 10, "ab" + string(utf8.RuneError) + "cd"},
	}
	for _, tc := range cases {
		if got := clip(tc.in, tc.n); got != tc.want {
			t.Errorf("clip(%q, %d) = %q, want %q", tc.in, tc.n, got, tc.want)
		}
	}
}

// Python wrote U+2028 and U+2029 and DEL as they are, and the short escapes for
// \b and \f; encoding/json does neither.
func TestAppendStringIsPythonsEncoding(t *testing.T) {
	ls, ps := string(rune(0x2028)), string(rune(0x2029))
	cases := []struct{ in, want string }{
		{"plain", `"plain"`},
		{"", `""`},
		{`q"b\s`, `"q\"b\\s"`},
		{"a\nb\rc\td\be\ff", `"a\nb\rc\td\be\ff"`},
		{"\x00\x01\x1f", `"\u0000\u0001\u001f"`},
		{"\x7f", "\"\x7f\""},
		{"<a href='x'>&amp;</a>", `"<a href='x'>&amp;</a>"`},
		{ls + ps, `"` + ls + ps + `"`},
		{"ab\xffcd", `"ab` + string(utf8.RuneError) + `cd"`},
		{"Карл 😀", `"Карл 😀"`},
	}
	for _, tc := range cases {
		if got := string(appendString(nil, tc.in)); got != tc.want {
			t.Errorf("appendString(%q) = %s, want %s", tc.in, got, tc.want)
		}
	}
}

// The payload is JSON whatever the content, and reads back to the input.
func TestPayloadIsValidJSON(t *testing.T) {
	msgs := []domain.EvalMessage{
		{ID: 1, Author: "a\x00b", Text: "t\x1f\x7f" + string(rune(0x2028)), Reactions: []domain.Reaction{{Emoji: "👍", Count: 2}, {Emoji: "custom:5", Count: 1}}},
		{ID: 2, Author: "", ReplyToID: 1, Kind: "sticker", Desc: "d"},
	}
	var back []map[string]any
	if err := json.Unmarshal([]byte(userPayload(msgs)), &back); err != nil {
		t.Fatal(err)
	}
	if len(back) != 2 || back[0]["a"] != "a\x00b" || back[1]["kind"] != "sticker" || back[1]["rp"] != float64(1) {
		t.Errorf("back = %v", back)
	}
	if userPayload(nil) != "[]" {
		t.Errorf("empty payload = %q", userPayload(nil))
	}
}

// tests/test_ai.py (AI-12): the optional keys appear only when there is
// something to say, and there is no message_id.
func TestPayloadOmitsEmptyFields(t *testing.T) {
	cases := []struct {
		name string
		msg  domain.EvalMessage
		want string
	}{
		{"bare", domain.EvalMessage{ID: 1, Author: "A"}, `[{"i": 1, "a": "A"}]`},
		{"text kind is plain", domain.EvalMessage{ID: 1, Author: "A", Text: "x", Kind: "text"}, `[{"i": 1, "a": "A", "t": "x"}]`},
		{"reply", domain.EvalMessage{ID: 1, Author: "A", ReplyToID: 7}, `[{"i": 1, "a": "A", "rp": 7}]`},
		{"no reactions", domain.EvalMessage{ID: 1, Author: "A", Reactions: []domain.Reaction{}}, `[{"i": 1, "a": "A"}]`},
		{"zero counts are dropped", domain.EvalMessage{ID: 1, Author: "A", Reactions: []domain.Reaction{{Emoji: "👍", Count: 0}}}, `[{"i": 1, "a": "A"}]`},
		{"zero count among others", domain.EvalMessage{ID: 1, Author: "A", Reactions: []domain.Reaction{{Emoji: "👍", Count: 0}, {Emoji: "😂", Count: 2}}}, `[{"i": 1, "a": "A", "re": {"😂": 2}}]`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := userPayload([]domain.EvalMessage{tc.msg}); got != tc.want {
				t.Errorf("got  %s\nwant %s", got, tc.want)
			}
		})
	}
}
