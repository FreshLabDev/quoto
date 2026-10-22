// SPDX-License-Identifier: Apache-2.0

package claude

import (
	"errors"
	"io"
	"strings"
	"testing"
	"testing/iotest"
)

// ev builds one server-sent event the way the API writes it.
func ev(name, data string) string { return "event: " + name + "\ndata: " + data + "\n\n" }

const startEvent = `{"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"claude-sonnet-5-5","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":1000,"output_tokens":1,"cache_creation_input_tokens":0,"cache_read_input_tokens":0}}}`

func textStream(parts ...string) string {
	s := ev("message_start", startEvent) +
		ev("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`)
	for _, p := range parts {
		s += ev("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":`+quote(p)+`}}`)
	}
	return s + ev("content_block_stop", `{"type":"content_block_stop","index":0}`) +
		ev("message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":500}}`) +
		ev("message_stop", `{"type":"message_stop"}`)
}

func quote(s string) string {
	b, _ := marshal(s)
	return string(b)
}

// Assumption A1 (documented SSE format): message_start, content_block_*,
// message_delta, message_stop, with the text in text_delta events.
func TestParseStreamHappyPath(t *testing.T) {
	resp, err := parseStream(strings.NewReader(textStream(`{"a":`, ` 1}`)), 200)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Text != `{"a": 1}` || resp.ID != "msg_1" || resp.Model != "claude-sonnet-5-5" || resp.StopReason != "end_turn" {
		t.Fatalf("response = %+v", resp)
	}
	want := Usage{Counts: Counts{Input: 1000, Output: 500}}
	if resp.Usage.Counts != want.Counts || resp.Usage.ThinkingTokens != nil || resp.Usage.Iterations != nil {
		t.Fatalf("usage = %+v", resp.Usage)
	}
}

// With thinking on, the reply opens with a thinking block (empty text under the
// default display) and, after a server-side fallback, a fallback marker. Only
// text blocks are the answer.
// tests/test_ai_claude.py::_message (thinking block) and ::test_server_side_fallback_answer_is_used_and_every_attempt_is_billed
func TestParseStreamIgnoresThinkingAndFallbackBlocks(t *testing.T) {
	stream := ev("message_start", startEvent) +
		ev("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"fallback","from":{"model":"claude-sonnet-5-5"},"to":{"model":"claude-sonnet-5"}}}`) +
		ev("content_block_stop", `{"type":"content_block_stop","index":0}`) +
		ev("content_block_start", `{"type":"content_block_start","index":1,"content_block":{"type":"thinking","thinking":""}}`) +
		ev("content_block_delta", `{"type":"content_block_delta","index":1,"delta":{"type":"thinking_delta","thinking":"hmm"}}`) +
		ev("content_block_delta", `{"type":"content_block_delta","index":1,"delta":{"type":"signature_delta","signature":"sig"}}`) +
		ev("content_block_stop", `{"type":"content_block_stop","index":1}`) +
		ev("content_block_start", `{"type":"content_block_start","index":2,"content_block":{"type":"text","text":"start-"}}`) +
		ev("content_block_delta", `{"type":"content_block_delta","index":2,"delta":{"type":"text_delta","text":"end"}}`) +
		ev("content_block_delta", `{"type":"content_block_delta","index":2,"delta":{"type":"citations_delta","citation":{}}}`) +
		ev("message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":9}}`) +
		ev("message_stop", `{"type":"message_stop"}`)
	resp, err := parseStream(strings.NewReader(stream), 200)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Text != "start-end" {
		t.Fatalf("text = %q", resp.Text)
	}
}

// Assumption A2: the final usage, with iterations and thinking tokens, arrives
// in message_delta. The same fields in message_start are read just as well.
// tests/test_ai_claude.py::test_server_side_fallback_answer_is_used_and_every_attempt_is_billed
func TestParseStreamUsageIterationsAndThinking(t *testing.T) {
	for name, where := range map[string]string{"in message_delta": "delta", "in message_start": "start"} {
		t.Run(name, func(t *testing.T) {
			usage := `{"input_tokens":1000,"output_tokens":500,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,` +
				`"output_tokens_details":{"thinking_tokens":200},"iterations":[` +
				`{"type":"message","model":"claude-sonnet-5-5","input_tokens":1000,"output_tokens":0,"cache_creation_input_tokens":0,"cache_read_input_tokens":0},` +
				`{"type":"fallback_message","model":"claude-sonnet-5","input_tokens":1000,"output_tokens":500,"cache_creation_input_tokens":0,"cache_read_input_tokens":0}]}`
			start := startEvent
			delta := `{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":500}}`
			if where == "start" {
				start = strings.Replace(startEvent, `"usage":{"input_tokens":1000,"output_tokens":1,"cache_creation_input_tokens":0,"cache_read_input_tokens":0}`, `"usage":`+usage, 1)
			} else {
				delta = `{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":` + usage + `}`
			}
			stream := ev("message_start", start) +
				ev("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":"ok"}}`) +
				ev("message_delta", delta) + ev("message_stop", `{"type":"message_stop"}`)
			resp, err := parseStream(strings.NewReader(stream), 200)
			if err != nil {
				t.Fatal(err)
			}
			u := resp.Usage
			if u.Input != 1000 || u.Output != 500 || u.ThinkingTokens == nil || *u.ThinkingTokens != 200 {
				t.Fatalf("usage = %+v", u)
			}
			if len(u.Iterations) != 2 || u.Iterations[0].Model != "claude-sonnet-5-5" || u.Iterations[1].Model != "claude-sonnet-5" || u.Iterations[1].Output != 500 {
				t.Fatalf("iterations = %+v", u.Iterations)
			}
			cost, ok := u.Cost("claude-sonnet-5-5")
			if !ok || cost < 0.008999 || cost > 0.009001 {
				t.Fatalf("cost = %v, %v", cost, ok)
			}
		})
	}
}

// message_delta carries the cumulative output count; a count that is absent
// there must not wipe the one from message_start.
func TestParseStreamUsageMergeKeepsEarlierCounts(t *testing.T) {
	resp, err := parseStream(strings.NewReader(textStream("x")), 200)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Usage.Input != 1000 || resp.Usage.Output != 500 {
		t.Fatalf("usage = %+v", resp.Usage)
	}
}

// Assumption A3: a refusal is stop_reason "refusal" with stop_details
// {category, explanation} in message_delta.
// tests/test_ai_claude.py::test_refusal_falls_back_to_openrouter_without_retrying_claude
func TestParseStreamRefusal(t *testing.T) {
	stream := ev("message_start", startEvent) +
		ev("message_delta", `{"type":"message_delta","delta":{"stop_reason":"refusal","stop_details":{"type":"refusal","category":"general_harms","explanation":"declined"}},"usage":{"output_tokens":0}}`) +
		ev("message_stop", `{"type":"message_stop"}`)
	resp, err := parseStream(strings.NewReader(stream), 200)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StopReason != "refusal" || resp.Text != "" || resp.StopDetails == nil ||
		resp.StopDetails.Category == nil || *resp.StopDetails.Category != "general_harms" ||
		resp.StopDetails.Explanation == nil || *resp.StopDetails.Explanation != "declined" {
		t.Fatalf("response = %+v", resp)
	}
}

func TestParseStreamStopDetailsNullAndMissing(t *testing.T) {
	stream := ev("message_start", strings.Replace(startEvent, `"stop_sequence":null`, `"stop_sequence":null,"stop_details":null`, 1)) +
		ev("message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn","stop_details":null},"usage":{"output_tokens":1}}`) +
		ev("message_stop", `{"type":"message_stop"}`)
	resp, err := parseStream(strings.NewReader(stream), 200)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StopDetails != nil {
		t.Fatalf("stop details = %+v, want none", resp.StopDetails)
	}
}

// The beta's events are known only from the documentation, so anything the
// parser does not recognise must be skipped, not fatal.
func TestParseStreamToleratesUnknownEventsAndFields(t *testing.T) {
	stream := ": keep-alive comment\n\n" +
		ev("ping", `{"type":"ping"}`) +
		ev("message_start", strings.Replace(startEvent, `"type":"message","role"`, `"type":"message","new_field":{"x":[1,2]},"role"`, 1)) +
		ev("server_tool_progress", `{"type":"server_tool_progress","message":"a string, not an object","index":"x"}`) +
		ev("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":"","extra":true}}`) +
		ev("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"a","extra":1}}`) +
		ev("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"mystery_delta","blob":"zz"}}`) +
		ev("content_block_delta", `{"type":"content_block_delta","index":-1,"delta":{"type":"text_delta","text":"negative index"}}`) +
		ev("content_block_delta", `{"type":"content_block_delta","index":99999,"delta":{"type":"text_delta","text":"far index"}}`) +
		ev("message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null,"future":1},"usage":{"output_tokens":3,"server_tool_use":{"web_search_requests":0}}}`) +
		"id: 7\nretry: 100\n\n" +
		ev("message_stop", `{"type":"message_stop","extra":1}`)
	resp, err := parseStream(strings.NewReader(stream), 200)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Text != "a" || resp.StopReason != "end_turn" || resp.Usage.Output != 3 {
		t.Fatalf("response = %+v", resp)
	}
}

// Assumption A4: the type of an event may be missing from its data; then the
// event: line names it.
func TestParseStreamEventNameWithoutTypeInData(t *testing.T) {
	stream := ev("message_start", `{"message":{"id":"m","model":"claude-sonnet-5-5","usage":{"input_tokens":5}}}`) +
		ev("content_block_start", `{"index":0,"content_block":{"type":"text","text":"hi"}}`) +
		ev("message_delta", `{"delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":2}}`) +
		ev("message_stop", `{}`)
	resp, err := parseStream(strings.NewReader(stream), 200)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Text != "hi" || resp.Usage.Input != 5 || resp.Usage.Output != 2 {
		t.Fatalf("response = %+v", resp)
	}
}

// The wire may use \r\n, split a line across reads, and lead data with no space.
func TestParseStreamLineEndingsAndChunking(t *testing.T) {
	raw := strings.ReplaceAll(textStream("héllo ", "wörld"), "\n", "\r\n")
	raw = strings.ReplaceAll(raw, "data: ", "data:")
	for name, r := range map[string]io.Reader{
		"whole":       strings.NewReader(raw),
		"one byte":    iotest.OneByteReader(strings.NewReader(raw)),
		"data errors": iotest.DataErrReader(strings.NewReader(raw)),
	} {
		t.Run(name, func(t *testing.T) {
			resp, err := parseStream(r, 200)
			if err != nil {
				t.Fatal(err)
			}
			if resp.Text != "héllo wörld" {
				t.Fatalf("text = %q", resp.Text)
			}
		})
	}
}

func TestParseStreamLongDataLine(t *testing.T) {
	big := strings.Repeat("ж", 300_000) // ~600 KB in one data line, past bufio's default
	resp, err := parseStream(strings.NewReader(textStream(big)), 200)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Text != big {
		t.Fatalf("text length %d, want %d", len(resp.Text), len(big))
	}
}

func TestParseStreamLastEventWithoutBlankLine(t *testing.T) {
	stream := strings.TrimSuffix(textStream("ok"), "\n")
	resp, err := parseStream(strings.NewReader(stream), 200)
	if err != nil || resp.Text != "ok" {
		t.Fatalf("resp = %+v, err = %v", resp, err)
	}
}

// A second message_start replaces the snapshot, as the SDK's accumulator does.
// This is how a re-run on the fallback model would read if it were announced
// that way (assumption A5).
func TestParseStreamSecondMessageStartReplacesSnapshot(t *testing.T) {
	stream := ev("message_start", startEvent) +
		ev("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":"declined draft"}}`) +
		ev("message_start", strings.Replace(startEvent, "claude-sonnet-5-5", "claude-sonnet-5", 1)) +
		ev("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":"real answer"}}`) +
		ev("message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":4}}`) +
		ev("message_stop", `{"type":"message_stop"}`)
	resp, err := parseStream(strings.NewReader(stream), 200)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Text != "real answer" || resp.Model != "claude-sonnet-5" {
		t.Fatalf("response = %+v", resp)
	}
}

// Without message_stop the stop reason still proves the message is complete.
func TestParseStreamAcceptsStopReasonWithoutMessageStop(t *testing.T) {
	stream := strings.TrimSuffix(textStream("ok"), ev("message_stop", `{"type":"message_stop"}`))
	resp, err := parseStream(strings.NewReader(stream), 200)
	if err != nil || resp.Text != "ok" {
		t.Fatalf("resp = %+v, err = %v", resp, err)
	}
}

// tests/test_ai_claude.py::test_overload_mid_stream_is_retried (the error side;
// the retry is the caller's).
func TestParseStreamErrorEvent(t *testing.T) {
	for _, tc := range []struct {
		typ       string
		retryable bool
	}{
		{"overloaded_error", true}, {"api_error", true}, {"rate_limit_error", true}, {"timeout_error", true},
		{"invalid_request_error", false}, {"billing_error", false}, {"", false},
	} {
		stream := ev("message_start", startEvent) +
			ev("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":"par"}}`) +
			ev("error", `{"type":"error","error":{"type":"`+tc.typ+`","message":"Overloaded"}}`)
		_, err := parseStream(strings.NewReader(stream), 200)
		var api *APIError
		if !errors.As(err, &api) || api.Type != tc.typ || api.Status != 200 || api.Message != "Overloaded" {
			t.Fatalf("%q: err = %#v", tc.typ, err)
		}
		if IsRetryable(err) != tc.retryable {
			t.Errorf("%q: IsRetryable = %v, want %v", tc.typ, IsRetryable(err), tc.retryable)
		}
	}
}

// A stream that stops short is a connection failure, not a result.
func TestParseStreamTruncated(t *testing.T) {
	full := textStream("one", "two")
	for name, cut := range map[string]string{
		"nothing":            "",
		"cut inside a line":  full[:len(full)-100],
		"only message_start": ev("message_start", startEvent),
		"mid text":           full[:strings.Index(full, ev("content_block_stop", `{"type":"content_block_stop","index":0}`))],
	} {
		t.Run(name, func(t *testing.T) {
			_, err := parseStream(strings.NewReader(cut), 200)
			var conn *ConnError
			if !errors.As(err, &conn) || !errors.Is(err, ErrTruncated) {
				t.Fatalf("err = %#v", err)
			}
			if !IsRetryable(err) {
				t.Error("a broken stream is retryable")
			}
		})
	}
}

func TestParseStreamReadErrorIsConnError(t *testing.T) {
	boom := errors.New("connection reset by peer")
	r := io.MultiReader(strings.NewReader(ev("message_start", startEvent)), iotest.ErrReader(boom))
	_, err := parseStream(r, 200)
	var conn *ConnError
	if !errors.As(err, &conn) || !errors.Is(err, boom) || !IsRetryable(err) {
		t.Fatalf("err = %#v", err)
	}
}

func TestParseStreamMalformedEvents(t *testing.T) {
	for name, stream := range map[string]string{
		"data is not JSON":         ev("message_start", `{oops`),
		"unknown event not JSON":   ev("weird", `{oops`),
		"message_start no message": ev("message_start", `{"type":"message_start"}`),
		"delta wrong shape":        ev("message_start", startEvent) + ev("content_block_delta", `{"type":"content_block_delta","index":"zero","delta":{}}`),
	} {
		t.Run(name, func(t *testing.T) {
			_, err := parseStream(strings.NewReader(stream), 200)
			var se *StreamError
			if !errors.As(err, &se) {
				t.Fatalf("err = %#v, want *StreamError", err)
			}
			if IsRetryable(err) {
				t.Error("a stream the parser cannot read is not retryable")
			}
		})
	}
}
