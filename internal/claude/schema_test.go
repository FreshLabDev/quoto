// SPDX-License-Identifier: Apache-2.0

package claude

import (
	"strings"
	"testing"
)

// tests/test_ai_claude.py::test_schema_drops_only_keywords_claude_rejects
func TestStripUnsupportedDropsOnlyRejectedKeywords(t *testing.T) {
	in := `{"type":"object","additionalProperties":false,"properties":{` +
		`"day":{"type":"object","additionalProperties":false,"properties":{"reason_text":{"type":"string","maxLength":200,"minLength":1}},"required":["reason_text"]},` +
		`"messages":{"type":"array","maxItems":10,"items":{"type":"object","properties":{"id":{"type":"integer","minimum":0,"exclusiveMinimum":0},` +
		`"score":{"type":"number","minimum":0,"maximum":10,"exclusiveMaximum":11,"multipleOf":0.5}},"required":["id","score"]}},` +
		`"language":{"type":"string","enum":["ru","uk","en"]},"primary_id":{"type":["integer","null"]}},"required":["day","messages"]}`
	want := `{"type":"object","additionalProperties":false,"properties":{` +
		`"day":{"type":"object","additionalProperties":false,"properties":{"reason_text":{"type":"string"}},"required":["reason_text"]},` +
		`"messages":{"type":"array","items":{"type":"object","properties":{"id":{"type":"integer"},"score":{"type":"number"}},"required":["id","score"]}},` +
		`"language":{"type":"string","enum":["ru","uk","en"]},"primary_id":{"type":["integer","null"]}},"required":["day","messages"]}`
	got, err := StripUnsupported([]byte(in))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}
	// The input is untouched (the OpenRouter copy keeps its bounds).
	if !strings.Contains(in, `"maxItems":10`) {
		t.Fatal("test bug")
	}
	again, err := StripUnsupported(got)
	if err != nil || string(again) != want {
		t.Fatalf("not idempotent: %s, %v", again, err)
	}
}

// tests/test_ai_claude.py::test_property_named_like_a_keyword_survives
func TestStripUnsupportedKeepsFieldNamedLikeAKeyword(t *testing.T) {
	in := `{"type":"object","properties":{"maximum":{"type":"integer","maximum":3},"maxItems":{"type":"array","maxItems":2,"items":{"type":"string"}}}}`
	want := `{"type":"object","properties":{"maximum":{"type":"integer"},"maxItems":{"type":"array","items":{"type":"string"}}}}`
	got, err := StripUnsupported([]byte(in))
	if err != nil || string(got) != want {
		t.Fatalf("got %s, %v", got, err)
	}
}

// Outside "properties" a key is a keyword wherever it is, as in the Python
// recursion: this is what keeps the port equal to it.
func TestStripUnsupportedRecursesIntoEveryValue(t *testing.T) {
	in := `{"anyOf":[{"type":"integer","minimum":1},{"type":"string","maxLength":4}],"$defs":{"x":{"maximum":5,"type":"number"}},"default":{"maximum":1,"keep":true}}`
	want := `{"anyOf":[{"type":"integer"},{"type":"string"}],"$defs":{"x":{"type":"number"}},"default":{"keep":true}}`
	got, err := StripUnsupported([]byte(in))
	if err != nil || string(got) != want {
		t.Fatalf("got %s, %v", got, err)
	}
}

func TestStripUnsupportedKeepsScalarsExactly(t *testing.T) {
	in := `{"const":"Привет <b> & \"q\"","enum":[1,2.50,1e3,true,null,"x"],"properties":"not an object","maximum":9}`
	want := `{"const":"Привет <b> & \"q\"","enum":[1,2.50,1e3,true,null,"x"],"properties":"not an object"}`
	got, err := StripUnsupported([]byte(in))
	if err != nil || string(got) != want {
		t.Fatalf("got %s, %v", got, err)
	}
}

func TestStripUnsupportedRejectsBadJSON(t *testing.T) {
	for _, in := range []string{``, `{`, `{"a":}`, `{"a":1} x`, `[1,2`, `nope`} {
		if got, err := StripUnsupported([]byte(in)); err == nil {
			t.Errorf("%q: accepted as %s", in, got)
		}
	}
}
