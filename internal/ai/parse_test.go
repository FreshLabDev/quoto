// SPDX-License-Identifier: Apache-2.0

package ai

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/FreshLabDev/quoto/internal/domain"
)

func ids(ids ...int64) []domain.EvalMessage {
	out := make([]domain.EvalMessage, len(ids))
	for i, id := range ids {
		out[i] = domain.EvalMessage{ID: id, Author: "A", Text: "t"}
	}
	return out
}

func scoreMap(s []domain.Score) map[int64]float64 {
	m := map[int64]float64{}
	for _, sc := range s {
		m[sc.ID] = sc.Value
	}
	return m
}

const okDay = `{"day":{"should_publish":true,"reason_code":"worthy","reason_text":"fun"},` +
	`"quote":{"primary_id":1,"context_ids":[1,2],"context_needed":true},` +
	`"messages":[{"id":1,"score":7},{"id":2,"score":8}]}`

// tests/test_ai.py::test_parse_day_payload_* (AI-02 .. AI-06, AI-10).
func TestParseEvaluationHappyPaths(t *testing.T) {
	t.Run("full answer", func(t *testing.T) {
		ev, err := parseEvaluation(okDay, ids(1, 2), false)
		if err != nil {
			t.Fatal(err)
		}
		if got := scoreMap(ev.scores); got[1] != 0.7 || got[2] != 0.8 || len(ev.scores) != 2 {
			t.Errorf("scores = %v", ev.scores) // AI-10
		}
		if ev.scores[0].ID != 1 || ev.scores[1].ID != 2 {
			t.Errorf("the order of the reply is not kept: %v", ev.scores)
		}
		want := domain.DayVerdict{ShouldPublish: true, ReasonCode: "worthy", ReasonText: "fun"}
		if ev.verdict != want {
			t.Errorf("verdict = %+v", ev.verdict)
		}
		if q := ev.quote; q == nil || q.PrimaryID != 1 || !reflect.DeepEqual(q.ContextIDs, []int64{1, 2}) || !q.ContextNeeded {
			t.Errorf("quote = %+v", q) // AI-05
		}
		if ev.language != nil {
			t.Errorf("language = %+v", ev.language)
		}
	})

	t.Run("string false, default reason, no quote, no language", func(t *testing.T) { // AI-02
		ev, err := parseEvaluation(`{"day":{"should_publish":"false"},"messages":[{"id":1,"score":4}]}`, ids(1), false)
		if err != nil {
			t.Fatal(err)
		}
		if ev.verdict.ShouldPublish || ev.verdict.ReasonCode != domain.ReasonBoringDay || ev.verdict.ReasonText != "" {
			t.Errorf("verdict = %+v", ev.verdict)
		}
		if ev.quote != nil || ev.language != nil || scoreMap(ev.scores)[1] != 0.4 {
			t.Errorf("ev = %+v", ev)
		}
	})

	t.Run("default reason for a day worth publishing", func(t *testing.T) {
		ev, err := parseEvaluation(`{"day":{"should_publish":true,"reason_code":""}}`, ids(1), false)
		if err != nil || ev.verdict.ReasonCode != domain.ReasonWorthy || len(ev.scores) != 0 {
			t.Errorf("ev = %+v err = %v", ev, err)
		}
	})

	t.Run("reason code is not checked", func(t *testing.T) { // doc risk 5.D, kept as Python had it
		ev, err := parseEvaluation(`{"day":{"should_publish":true,"reason_code":"meh"}}`, ids(1), false)
		if err != nil || ev.verdict.ReasonCode != "meh" {
			t.Errorf("ev = %+v err = %v", ev, err)
		}
	})

	t.Run("reason text is stripped and cut in runes", func(t *testing.T) {
		long := strings.Repeat("я", 300)
		ev, err := parseEvaluation(`{"day":{"should_publish":true,"reason_text":"  `+long+`  "}}`, ids(1), false)
		if err != nil {
			t.Fatal(err)
		}
		if n := utf8.RuneCountInString(ev.verdict.ReasonText); n != maxReasonText {
			t.Errorf("reason text has %d runes", n)
		}
	})

	t.Run("language block when asked for", func(t *testing.T) { // AI-06
		in := `{"day":{"should_publish":true},"language":{"chat_language":" Ukrainian ","interface_language":"UK-ua"}}`
		ev, err := parseEvaluation(in, ids(1), true)
		if err != nil {
			t.Fatal(err)
		}
		if ev.language == nil || ev.language.ChatLanguage != "Ukrainian" || ev.language.InterfaceLanguage != "uk" {
			t.Errorf("language = %+v", ev.language)
		}
		// Not asked for: the block is ignored, so a chat that has a language is not moved.
		ev, err = parseEvaluation(in, ids(1), false)
		if err != nil || ev.language != nil {
			t.Errorf("language without a request = %+v err = %v", ev.language, err)
		}
	})

	t.Run("language block that cannot be used", func(t *testing.T) {
		for _, lang := range []string{
			`"x"`, `null`, `{"chat_language":"Klingon","interface_language":"tlh"}`, `{"chat_language":"x"}`,
			`{"chat_language":"x","interface_language":5}`, `{"chat_language":"x","interface_language":""}`,
		} {
			ev, err := parseEvaluation(`{"day":{"should_publish":true},"language":`+lang+`}`, ids(1), true)
			if err != nil || ev.language != nil {
				t.Errorf("language %s: %+v err = %v", lang, ev.language, err)
			}
		}
	})

	t.Run("language name is cut in runes", func(t *testing.T) {
		in := `{"day":{"should_publish":true},"language":{"chat_language":"` + strings.Repeat("ж", 200) + `","interface_language":"ru"}}`
		ev, _ := parseEvaluation(in, ids(1), true)
		if n := utf8.RuneCountInString(ev.language.ChatLanguage); n != maxChatLanguage {
			t.Errorf("chat language has %d runes", n)
		}
	})
}

// tests/test_ai.py::test_parse_day_payload_requires_day_block (AI-03) and
// ..._rejects_legacy_array (AI-04), plus every other way an answer can be wrong.
// None of them may escape as a panic or a half result: they are errors, and the
// caller moves on to the next model.
func TestParseEvaluationErrors(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		wantErr string
	}{
		{"no day block", `{"quote":null,"messages":[{"id":1,"score":4}]}`, "missing a valid `day`"},
		{"day is not an object", `{"day":"worthy","messages":[]}`, "missing a valid `day`"},
		{"day is null", `{"day":null}`, "missing a valid `day`"},
		{"legacy array", `[{"id":1,"score":4}]`, "must include a `day`"},
		{"no JSON at all", `not json at all`, "no JSON payload"},
		{"empty", ``, "no JSON payload"},
		{"only prose with a stray bracket", `see [this and {that`, "no JSON payload"},
		{"messages is not a list", `{"day":{"should_publish":true},"messages":{"id":1}}`, "`messages` must be a list"},
		{"messages is a string", `{"day":{"should_publish":true},"messages":"none"}`, "`messages` must be a list"},
		{"should_publish missing", `{"day":{},"messages":[]}`, "should_publish"},
		{"should_publish null", `{"day":{"should_publish":null}}`, "should_publish"},
		{"should_publish maybe", `{"day":{"should_publish":"maybe"}}`, "should_publish"},
		{"should_publish 2", `{"day":{"should_publish":2}}`, "should_publish"},
		{"should_publish 1.0", `{"day":{"should_publish":1.0}}`, "should_publish"},
		{"should_publish object", `{"day":{"should_publish":{}}}`, "should_publish"},
		{"entry is a number", `{"day":{"should_publish":true},"messages":[5]}`, "not an object"},
		{"entry is a string", `{"day":{"should_publish":true},"messages":["a"]}`, "not an object"},
		{"entry is null", `{"day":{"should_publish":true},"messages":[null]}`, "not an object"},
		{"entry without id", `{"day":{"should_publish":true},"messages":[{"score":3}]}`, "no id"},
		{"id is text", `{"day":{"should_publish":true},"messages":[{"id":"abc","score":3}]}`, "not an integer"},
		{"id is null", `{"day":{"should_publish":true},"messages":[{"id":null,"score":3}]}`, "not an integer"},
		{"id is true", `{"day":{"should_publish":true},"messages":[{"id":true,"score":3}]}`, "not an integer"},
		{"entry without score", `{"day":{"should_publish":true},"messages":[{"id":1}]}`, "no score"},
		{"score is null", `{"day":{"should_publish":true},"messages":[{"id":1,"score":null}]}`, "not a number"},
		{"score is text", `{"day":{"should_publish":true},"messages":[{"id":1,"score":"high"}]}`, "not a number"},
		{"score is nan", `{"day":{"should_publish":true},"messages":[{"id":1,"score":"nan"}]}`, "not a number"},
		{"score is a bool", `{"day":{"should_publish":true},"messages":[{"id":1,"score":true}]}`, "not a number"},
		{"score is an object", `{"day":{"should_publish":true},"messages":[{"id":1,"score":{}}]}`, "not a number"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseEvaluation(tc.in, ids(1), false)
			var pe *parseError
			if !errors.As(err, &pe) {
				t.Fatalf("err = %T %v, want *parseError", err, err)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("err = %q, want it to mention %q", err, tc.wantErr)
			}
		})
	}
}

// Python read the score only of an id it knew. A bad score for an id that was
// not in the request does not fail the answer; an id out of int64 range cannot
// be a known one either.
func TestParseEvaluationUnknownIDs(t *testing.T) {
	in := `{"day":{"should_publish":true},"messages":[` +
		`{"id":99,"score":"garbage"},{"id":1,"score":6},{"id":123456789012345678901234567890,"score":1},{"id":98,"score":null}]}`
	ev, err := parseEvaluation(in, ids(1), false)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(ev.scores, []domain.Score{{ID: 1, Value: 0.6}}) {
		t.Errorf("scores = %v", ev.scores)
	}
}

// tests/test_ai.py (AI-10): the score is clamped to 0..10 and divided by 10.
func TestScoreClampAndForms(t *testing.T) {
	cases := []struct {
		score string
		want  float64
	}{
		{"7", 0.7}, {"8", 0.8}, {"0", 0}, {"10", 1}, {"11", 1}, {"1000", 1}, {"-3", 0}, {"-0.0", 0},
		{"7.5", 0.75}, {"1e1", 1}, {"1e999", 1}, {"-1e999", 0},
		{`"7"`, 0.7}, {`" 7.5 "`, 0.75}, {`"inf"`, 1}, {`"-inf"`, 0},
	}
	for _, tc := range cases {
		ev, err := parseEvaluation(`{"day":{"should_publish":true},"messages":[{"id":1,"score":`+tc.score+`}]}`, ids(1), false)
		if err != nil {
			t.Errorf("score %s: %v", tc.score, err)
			continue
		}
		if got := ev.scores[0].Value; got != tc.want {
			t.Errorf("score %s = %v, want %v", tc.score, got, tc.want)
		}
	}
}

// A repeated id keeps the position of its first entry and the value of its last,
// as the Python dict did; the scheduler breaks ties by this order.
func TestScoresKeepReplyOrderAndDictSemantics(t *testing.T) {
	in := `{"day":{"should_publish":true},"messages":[` +
		`{"id":3,"score":9},{"id":1,"score":5},{"id":3,"score":2},{"id":2,"score":9}]}`
	ev, err := parseEvaluation(in, ids(1, 2, 3), false)
	if err != nil {
		t.Fatal(err)
	}
	want := []domain.Score{{ID: 3, Value: 0.2}, {ID: 1, Value: 0.5}, {ID: 2, Value: 0.9}}
	if !reflect.DeepEqual(ev.scores, want) {
		t.Errorf("scores = %v, want %v", ev.scores, want)
	}
}

// Ids that arrive as text or as numbers with a fraction are read the way Python's
// int() read them.
func TestParseIntLike(t *testing.T) {
	const big = "123456789012345678901234567890"
	cases := []struct {
		in   string
		want int64
		kind intKind
	}{
		{`5`, 5, intOK}, {`-5`, -5, intOK}, {`0`, 0, intOK}, {`-0`, 0, intOK},
		{`7.9`, 7, intOK}, {`-7.9`, -7, intOK}, {`3.0`, 3, intOK}, {`1e3`, 1000, intOK},
		{`"12"`, 12, intOK}, {`" 12 "`, 12, intOK}, {`"+12"`, 12, intOK}, {`"-12"`, -12, intOK},
		{`9223372036854775807`, 9223372036854775807, intOK},
		{`9223372036854775808`, 0, intOutOfRange}, {big, 0, intOutOfRange}, {`1e30`, 0, intOutOfRange}, {`-1e30`, 0, intOutOfRange},
		{`"7.5"`, 0, intInvalid}, {`"abc"`, 0, intInvalid}, {`""`, 0, intInvalid}, {`"1_000"`, 0, intInvalid}, {`"--5"`, 0, intInvalid},
		{`"٣"`, 0, intInvalid}, // an Arabic-Indic digit: Python accepted it, an id never is one
		{`"+"`, 0, intInvalid}, {`true`, 0, intInvalid}, {`false`, 0, intInvalid}, {`null`, 0, intInvalid},
		{`[]`, 0, intInvalid}, {`{}`, 0, intInvalid}, {``, 0, intInvalid},
	}
	for _, tc := range cases {
		got, kind := parseIntLike(json.RawMessage(tc.in))
		if got != tc.want || kind != tc.kind {
			t.Errorf("parseIntLike(%s) = %d, %d; want %d, %d", tc.in, got, kind, tc.want, tc.kind)
		}
	}
}

// Python's _parse_bool_like.
func TestParseBoolLike(t *testing.T) {
	yes := []string{`true`, `1`, `"true"`, `"TRUE"`, `" Yes "`, `"1"`, `"yes"`}
	no := []string{`false`, `0`, `-0`, `"false"`, `"No"`, `"0"`, `" FALSE "`}
	bad := []string{`2`, `-1`, `1.0`, `0.0`, `1e0`, `"maybe"`, `""`, `null`, `[]`, `{}`, ``, `"truee"`, `"on"`}
	for _, in := range yes {
		if v, err := parseBoolLike(json.RawMessage(in)); err != nil || !v {
			t.Errorf("%s = %v, %v", in, v, err)
		}
	}
	for _, in := range no {
		if v, err := parseBoolLike(json.RawMessage(in)); err != nil || v {
			t.Errorf("%s = %v, %v", in, v, err)
		}
	}
	for _, in := range bad {
		if _, err := parseBoolLike(json.RawMessage(in)); err == nil {
			t.Errorf("%s was accepted", in)
		}
	}
}

// The quote block is lenient inside: what cannot be read is dropped, the answer
// stands. doc section 1.6 (_parse_quote_choice).
func TestParseQuote(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want *domain.QuoteChoice
	}{
		{"absent", ``, nil},
		{"null", `null`, nil},
		{"not an object", `[1]`, nil},
		{"full", `{"primary_id":5,"context_ids":[4,5],"context_needed":true}`,
			&domain.QuoteChoice{PrimaryID: 5, ContextIDs: []int64{4, 5}, ContextNeeded: true}},
		{"null primary", `{"primary_id":null,"context_ids":[],"context_needed":false}`,
			&domain.QuoteChoice{ContextIDs: []int64{}}},
		{"text primary", `{"primary_id":"7"}`, &domain.QuoteChoice{PrimaryID: 7, ContextIDs: []int64{}}},
		{"junk primary", `{"primary_id":"seven"}`, &domain.QuoteChoice{ContextIDs: []int64{}}},
		{"junk context ids are dropped", `{"primary_id":1,"context_ids":[1,"x",null,2.0,"3",true,{}]}`,
			&domain.QuoteChoice{PrimaryID: 1, ContextIDs: []int64{1, 2, 3}}},
		{"context ids not a list", `{"primary_id":1,"context_ids":"1,2"}`, &domain.QuoteChoice{PrimaryID: 1, ContextIDs: []int64{}}},
		{"context needed junk", `{"primary_id":1,"context_needed":"maybe"}`, &domain.QuoteChoice{PrimaryID: 1, ContextIDs: []int64{}}},
		{"context needed text", `{"primary_id":1,"context_needed":"yes"}`, &domain.QuoteChoice{PrimaryID: 1, ContextIDs: []int64{}, ContextNeeded: true}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := parseQuote(json.RawMessage(tc.in))
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}

// tests/test_ai.py::test_extract_json_* (AI-05): prose, braces, code fences and
// think blocks around the JSON; the longest decodable value wins.
func TestExtractJSON(t *testing.T) {
	const obj = `{"a": [1, 2, {"b": "}"}], "c": null}`
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"bare", obj, obj},
		{"prose around", "Sure! Here you go: " + obj + "\nHope that helps {really}.", obj},
		{"fenced", "```json\n" + obj + "\n```", obj},
		{"fenced without language", "```\n" + obj + "\n```", obj},
		{"fence glued to the value", "```json" + obj + "```", obj},
		{"think block", "<think>maybe {\"x\": 1} and [2]</think>\n" + obj, obj},
		{"think block over several lines", "<think>\nline {\n</think>" + obj, obj},
		{"longest wins over an earlier short one", `{"x":1} then ` + obj, obj},
		{"first of equals", `{"a":1} {"b":2}`, `{"a":1}`},
		{"nested is not a candidate of its own", `[{"a":1},{"b":2}]`, `[{"a":1},{"b":2}]`},
		{"truncated first, whole second", `{"a": [1, 2 ` + obj, obj},
		{"array", `[1, 2, 3]`, `[1, 2, 3]`},
		{"leading and trailing space", "  \n" + obj + "  \n", obj},
		{"longest by characters, not by bytes", `{"a":"яяяяяя"} {"bb":"ccccccc"}`, `{"bb":"ccccccc"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := extractJSON(tc.in)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != tc.want {
				t.Errorf("got  %s\nwant %s", got, tc.want)
			}
		})
	}

	for _, in := range []string{"", "   ", "no braces here", "{broken", "[1, 2", "<think>{\"a\":1}</think>", "```json\n```"} {
		if _, err := extractJSON(in); err == nil {
			t.Errorf("extractJSON(%q) found something", in)
		}
	}
}

// Python had no bound on the scan and went quadratic on a long run of
// brackets; the budget stops it instead of taking seconds.
func TestExtractJSONStopsOnAHostileAnswer(t *testing.T) {
	hostile := strings.Repeat("[", 1<<17)
	if _, err := extractJSON(hostile); err == nil {
		t.Error("found JSON in a run of brackets")
	}
	// A valid object after a modest amount of noise is still found.
	noisy := strings.Repeat("[", 2000) + ` {"ok": true}`
	got, err := extractJSON(noisy)
	if err != nil || string(got) != `{"ok": true}` {
		t.Errorf("got %s, %v", got, err)
	}
}
