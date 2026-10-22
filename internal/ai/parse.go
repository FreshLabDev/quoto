// SPDX-License-Identifier: Apache-2.0

package ai

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/FreshLabDev/quoto/internal/domain"
	"github.com/FreshLabDev/quoto/internal/i18n"
)

// parseError is every way a model's answer can fail to be usable: no JSON, the
// wrong shape, a missing day block, a score that is not a number. The caller
// turns it into the parse_failed status and tries the next candidate. Python
// let a few of these escape as TypeError; here none can.
type parseError struct{ msg string }

func (e *parseError) Error() string { return e.msg }

func parseErrorf(format string, args ...any) error {
	return &parseError{fmt.Sprintf(format, args...)}
}

// evaluation is a parsed answer.
type evaluation struct {
	scores   []domain.Score
	verdict  domain.DayVerdict
	quote    *domain.QuoteChoice
	language *domain.LanguageChoice
}

// parseEvaluation reads the model's text as the day-verdict reply: the day
// block, the quote pick, the scores and, when the request asked for it, the
// language. Ids the request did not contain are dropped.
func parseEvaluation(content string, msgs []domain.EvalMessage, detectLanguage bool) (evaluation, error) {
	doc, err := extractJSON(content)
	if err != nil {
		return evaluation{}, err
	}
	if doc[0] == '[' {
		return evaluation{}, parseErrorf("AI response must include a `day` verdict object.")
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(doc, &top); err != nil {
		return evaluation{}, parseErrorf("AI response is not a JSON object: %v", err)
	}

	var entries []json.RawMessage
	if raw, ok := top["messages"]; ok && !isNull(raw) {
		if err := json.Unmarshal(raw, &entries); err != nil {
			return evaluation{}, parseErrorf("AI response field `messages` must be a list.")
		}
	}

	day, ok := asObject(top["day"])
	if !ok {
		return evaluation{}, parseErrorf("AI response is missing a valid `day` verdict block.")
	}
	publish, err := parseBoolLike(day["should_publish"])
	if err != nil {
		return evaluation{}, parseErrorf("AI response field `day.should_publish` must be a strict boolean value.")
	}
	code := domain.ReasonBoringDay
	if publish {
		code = domain.ReasonWorthy
	}
	// The code is not checked against the two known ones: Python stored whatever
	// the model wrote, and the stats tolerate it.
	if s, ok := asString(day["reason_code"]); ok && s != "" {
		code = s
	}
	text, _ := asString(day["reason_text"])

	ev := evaluation{
		verdict: domain.DayVerdict{
			ShouldPublish: publish,
			ReasonCode:    code,
			ReasonText:    clip(strings.TrimSpace(text), maxReasonText),
		},
		quote: parseQuote(top["quote"]),
	}
	// Only a request that asked for the language gets one back. The model may
	// invent the block anyway (chat text is untrusted), and a chat that already
	// has a language must not be moved by it.
	if detectLanguage {
		ev.language = parseLanguage(top["language"])
	}
	if ev.scores, err = normalizeScores(msgs, entries); err != nil {
		return evaluation{}, err
	}
	return ev, nil
}

// normalizeScores keeps the entries whose id the request contained. Like the
// Python dict, an id that comes twice keeps the position of its first entry and
// the value of its last. A score is clamped to 0..10 and divided by 10. Python
// only read the score of a known id, and so does this.
func normalizeScores(msgs []domain.EvalMessage, entries []json.RawMessage) ([]domain.Score, error) {
	known := make(map[int64]bool, len(msgs))
	for _, m := range msgs {
		known[m.ID] = true
	}
	var out []domain.Score
	at := map[int64]int{}
	for i, raw := range entries {
		entry, ok := asObject(raw)
		if !ok {
			return nil, parseErrorf("messages[%d] is not an object", i)
		}
		idRaw, present := entry["id"]
		if !present {
			return nil, parseErrorf("messages[%d] has no id", i)
		}
		id, kind := parseIntLike(idRaw)
		switch kind {
		case intInvalid:
			return nil, parseErrorf("messages[%d].id is not an integer", i)
		case intOutOfRange:
			continue // cannot be an id the request had
		}
		if !known[id] {
			continue
		}
		scoreRaw, present := entry["score"]
		if !present {
			return nil, parseErrorf("messages[%d] has no score", i)
		}
		score, ok := parseFloatLike(scoreRaw)
		if !ok {
			return nil, parseErrorf("messages[%d].score is not a number", i)
		}
		value := math.Max(0, math.Min(10, score)) / 10
		if pos, seen := at[id]; seen {
			out[pos].Value = value
			continue
		}
		at[id] = len(out)
		out = append(out, domain.Score{ID: id, Value: value})
	}
	return out, nil
}

// parseQuote reads the quote block. A missing or malformed block is no pick;
// inside it, what cannot be read is dropped instead of failing the answer.
func parseQuote(raw json.RawMessage) *domain.QuoteChoice {
	q, ok := asObject(raw)
	if !ok {
		return nil
	}
	choice := &domain.QuoteChoice{ContextIDs: []int64{}}
	if id, kind := parseIntLike(q["primary_id"]); kind == intOK {
		choice.PrimaryID = id
	}
	var ids []json.RawMessage
	if json.Unmarshal(q["context_ids"], &ids) == nil {
		for _, item := range ids {
			if id, kind := parseIntLike(item); kind == intOK {
				choice.ContextIDs = append(choice.ContextIDs, id)
			}
		}
	}
	if needed, err := parseBoolLike(q["context_needed"]); err == nil {
		choice.ContextNeeded = needed
	}
	return choice
}

// parseLanguage reads the language block. Without a supported interface
// language there is no choice at all.
func parseLanguage(raw json.RawMessage) *domain.LanguageChoice {
	l, ok := asObject(raw)
	if !ok {
		return nil
	}
	iface, ok := asString(l["interface_language"])
	if !ok {
		return nil
	}
	code, ok := i18n.Normalize(iface)
	if !ok {
		return nil
	}
	name, _ := asString(l["chat_language"])
	return &domain.LanguageChoice{
		ChatLanguage:      clip(strings.TrimSpace(name), maxChatLanguage),
		InterfaceLanguage: code,
	}
}

// parseBoolLike accepts what Python's _parse_bool_like did: a boolean, the
// integers 0 and 1 (not 1.0), and the strings true, yes, 1, false, no, 0 in any
// case and with spaces around.
func parseBoolLike(raw json.RawMessage) (bool, error) {
	raw = bytes.TrimSpace(raw)
	switch string(raw) {
	case "true", "1":
		return true, nil
	case "false", "0", "-0":
		return false, nil
	}
	if s, ok := asString(raw); ok {
		switch strings.ToLower(strings.TrimSpace(s)) {
		case "true", "yes", "1":
			return true, nil
		case "false", "no", "0":
			return false, nil
		}
	}
	return false, errors.New("not a boolean")
}

type intKind int

const (
	intOK         intKind = iota
	intInvalid            // not a number at all (null, bool, text, object)
	intOutOfRange         // a number that does not fit an int64
)

// parseIntLike is Python's int(value) for what JSON can hold, narrowed where
// Python's answer is surprising: a number (a fraction is cut toward zero), or a
// string of ASCII digits with an optional sign and spaces around. Python also
// took true as 1, underscores and non-ASCII digits; those are invalid here.
func parseIntLike(raw json.RawMessage) (int64, intKind) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return 0, intInvalid
	}
	var text string
	switch c := raw[0]; {
	case c == '-' || (c >= '0' && c <= '9'):
		text = string(raw)
	case c == '"':
		s, ok := asString(raw)
		if !ok {
			return 0, intInvalid
		}
		text = strings.TrimSpace(s)
		if text == "" || !asciiInt(text) {
			return 0, intInvalid
		}
	default:
		return 0, intInvalid
	}
	if v, err := strconv.ParseInt(strings.TrimPrefix(text, "+"), 10, 64); err == nil {
		return v, intOK
	} else if errors.Is(err, strconv.ErrRange) {
		return 0, intOutOfRange
	}
	// A fraction or an exponent: JSON's 3.0, 1e3, 7.9.
	f, err := strconv.ParseFloat(text, 64)
	if err != nil && !errors.Is(err, strconv.ErrRange) {
		return 0, intInvalid
	}
	if math.IsNaN(f) || math.IsInf(f, 0) || math.Abs(f) >= 1<<63 {
		return 0, intOutOfRange
	}
	return int64(f), intOK
}

// asciiInt reports whether s is an optional sign and then ASCII digits only.
func asciiInt(s string) bool {
	if s != "" && (s[0] == '+' || s[0] == '-') {
		s = s[1:]
	}
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// parseFloatLike is Python's float(value) for a number or a numeric string.
// NaN is refused: Python's min/max turned it into a 10.
func parseFloatLike(raw json.RawMessage) (float64, bool) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return 0, false
	}
	text := string(raw)
	if raw[0] == '"' {
		s, ok := asString(raw)
		if !ok {
			return 0, false
		}
		text = strings.TrimSpace(s)
	} else if raw[0] != '-' && (raw[0] < '0' || raw[0] > '9') {
		return 0, false
	}
	f, err := strconv.ParseFloat(text, 64)
	if err != nil && !errors.Is(err, strconv.ErrRange) {
		return 0, false
	}
	return f, !math.IsNaN(f)
}

func isNull(raw json.RawMessage) bool { return string(bytes.TrimSpace(raw)) == "null" }

func asObject(raw json.RawMessage) (map[string]json.RawMessage, bool) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || raw[0] != '{' {
		return nil, false
	}
	var m map[string]json.RawMessage
	if json.Unmarshal(raw, &m) != nil {
		return nil, false
	}
	return m, true
}

func asString(raw json.RawMessage) (string, bool) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || raw[0] != '"' {
		return "", false
	}
	var s string
	if json.Unmarshal(raw, &s) != nil {
		return "", false
	}
	return s, true
}

var (
	thinkBlock = regexp.MustCompile(`(?s)<think>.*?</think>`)
	// Python's \s is Unicode-aware; RE2's is ASCII only.
	codeFence = regexp.MustCompile("```(?:json)?[\\s\\p{Z}\\x{0b}\\x{1c}-\\x{1f}\\x{85}]*")
)

// scanBudget bounds the bytes the JSON scan may spend on starting points that
// turn out not to be JSON. Python had no bound and a quadratic scan; an answer
// is a few kilobytes, and this only stops a hostile run of brackets.
const scanBudget = 2 << 20

// extractJSON finds the JSON in a model's text. Models that ignore the schema
// wrap it in prose, a code fence or a <think> block, so this drops the think
// blocks and the fences, tries every [ and { as the start of a JSON value, and
// returns the longest value that decodes (longest in characters, the first of
// equals). A value that starts inside a longer one is not tried: it is shorter.
func extractJSON(content string) (json.RawMessage, error) {
	cleaned := strings.TrimSpace(content)
	cleaned = strings.TrimSpace(thinkBlock.ReplaceAllString(cleaned, ""))
	cleaned = codeFence.ReplaceAllString(cleaned, "")
	cleaned = strings.TrimSpace(strings.ReplaceAll(cleaned, "```", ""))

	var best string
	bestLen := -1
	budget := scanBudget
	for i := 0; i < len(cleaned); {
		if c := cleaned[i]; c != '[' && c != '{' {
			i++
			continue
		}
		end, cost := decodePrefix(cleaned[i:])
		if end == 0 {
			if budget -= cost; budget < 0 {
				break
			}
			i++
			continue
		}
		if n := utf8.RuneCountInString(cleaned[i : i+end]); n > bestLen {
			best, bestLen = cleaned[i:i+end], n
		}
		i += end
	}
	if bestLen < 0 {
		return nil, parseErrorf("no JSON payload found in the AI response")
	}
	return json.RawMessage(best), nil
}

// decodePrefix returns the length of the JSON value at the start of s, or 0 and
// what the failed attempt cost.
func decodePrefix(s string) (end, cost int) {
	dec := json.NewDecoder(strings.NewReader(s))
	var v json.RawMessage
	if err := dec.Decode(&v); err != nil {
		var syn *json.SyntaxError
		if errors.As(err, &syn) {
			return 0, int(syn.Offset) + 1
		}
		return 0, len(s)
	}
	return int(dec.InputOffset()), 0
}
