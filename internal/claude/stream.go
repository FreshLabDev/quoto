// SPDX-License-Identifier: Apache-2.0

package claude

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// Response is a finished message, assembled from the event stream.
type Response struct {
	ID    string
	Model string // the model that answered; differs from the requested one after a fallback
	// StopReason is end_turn, max_tokens or refusal, as the API wrote it.
	StopReason  string
	StopDetails *StopDetails
	// Text is the text blocks joined in order. Thinking blocks and the fallback
	// marker are not part of it. It is not trimmed.
	Text      string
	Usage     Usage
	RequestID string // the request-id response header
}

// StopDetails explains a stop, in particular a refusal.
type StopDetails struct {
	Category    *string
	Explanation *string
}

// maxEventLine caps one line of the event stream. A text delta is a few
// hundred bytes; this only stops a broken server from filling memory.
const maxEventLine = 8 << 20

type sseEvent struct {
	name string // the "event:" line, if any
	data []byte // the "data:" lines joined by \n
}

// readSSE feeds handle every event of a text/event-stream body. Lines may end
// in \n or \r\n; comment lines (": ping") and the id and retry fields are
// skipped. As the SSE spec says, what a cut stream leaves at its end is not an
// event: a last line without its newline is dropped. An event whose lines all
// arrived but whose closing blank line did not is still delivered.
func readSSE(r io.Reader, handle func(sseEvent) error) error {
	br := bufio.NewReaderSize(r, 64<<10)
	var ev sseEvent
	var data [][]byte
	flush := func() error {
		if len(data) == 0 { // per the spec an event with no data is not dispatched
			ev = sseEvent{}
			return nil
		}
		ev.data = bytes.Join(data, []byte{'\n'})
		out := ev
		ev, data = sseEvent{}, nil
		return handle(out)
	}
	for {
		line, err := readEventLine(br)
		if err != nil {
			if errors.Is(err, io.EOF) {
				return flush() // line, if any, had no newline: it was cut off
			}
			return err
		}
		line = bytes.TrimRight(line, "\r\n")
		switch {
		case len(line) == 0:
			if ferr := flush(); ferr != nil {
				return ferr
			}
		case line[0] == ':':
		default:
			field, value, _ := bytes.Cut(line, []byte{':'})
			value = bytes.TrimPrefix(value, []byte{' '})
			switch string(field) {
			case "event":
				ev.name = string(value)
			case "data":
				data = append(data, append([]byte(nil), value...))
			}
		}
	}
}

func readEventLine(br *bufio.Reader) ([]byte, error) {
	var line []byte
	for {
		chunk, err := br.ReadSlice('\n')
		if len(line)+len(chunk) > maxEventLine {
			return nil, fmt.Errorf("event line longer than %d bytes", maxEventLine)
		}
		line = append(line, chunk...)
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		return line, err
	}
}

// optInt reads a JSON number leniently: a fraction is cut off, and anything
// that is not a number (null, a string, an object) counts as absent.
type optInt struct {
	v  int
	ok bool
}

func (o *optInt) UnmarshalJSON(b []byte) error {
	if f, err := strconv.ParseFloat(strings.TrimSpace(string(b)), 64); err == nil {
		o.v, o.ok = int(f), true
	}
	return nil
}

type wireUsage struct {
	InputTokens              optInt          `json:"input_tokens"`
	OutputTokens             optInt          `json:"output_tokens"`
	CacheCreationInputTokens optInt          `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     optInt          `json:"cache_read_input_tokens"`
	OutputTokensDetails      json.RawMessage `json:"output_tokens_details"`
	Iterations               json.RawMessage `json:"iterations"`
}

type wireIteration struct {
	Type                     string `json:"type"`
	Model                    string `json:"model"`
	InputTokens              optInt `json:"input_tokens"`
	OutputTokens             optInt `json:"output_tokens"`
	CacheCreationInputTokens optInt `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     optInt `json:"cache_read_input_tokens"`
}

type wireStopDetails struct {
	Category    *string `json:"category"`
	Explanation *string `json:"explanation"`
}

type wireMessage struct {
	ID          string           `json:"id"`
	Model       string           `json:"model"`
	StopReason  *string          `json:"stop_reason"`
	StopDetails *json.RawMessage `json:"stop_details"`
	Usage       *wireUsage       `json:"usage"`
}

// event is the union of the fields the parser reads. Fields of a type the
// parser does not know stay raw, so a new event shape cannot break decoding.
type event struct {
	Type         string          `json:"type"`
	Index        *int            `json:"index"`
	Message      *wireMessage    `json:"message"`
	ContentBlock json.RawMessage `json:"content_block"`
	Delta        json.RawMessage `json:"delta"`
	Usage        *wireUsage      `json:"usage"`
	Error        *struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}

type block struct {
	typ  string
	text []byte
}

// accumulator rebuilds the message from events, the way the SDK's
// get_final_message does: message_start opens (and, if it comes again,
// replaces) the snapshot, deltas fill it in, message_delta sets the stop reason
// and the final usage.
type accumulator struct {
	httpStatus int
	started    bool
	stopped    bool
	msg        Response
	blocks     []block
}

func (a *accumulator) handle(ev sseEvent) error {
	// The type is read first and alone, so an event of a kind the parser does
	// not know is skipped even if its other fields have an unexpected shape.
	var head struct {
		Type string `json:"type"`
	}
	typ := ev.name
	if len(ev.data) > 0 {
		if err := json.Unmarshal(ev.data, &head); err != nil {
			return &StreamError{fmt.Errorf("event %q is not JSON: %w", ev.name, err)}
		}
		if head.Type != "" {
			typ = head.Type
		}
	}
	switch typ {
	case "message_start", "content_block_start", "content_block_delta", "message_delta", "error":
	case "message_stop":
		a.stopped = true
		return nil
	default: // ping, content_block_stop, anything new
		return nil
	}

	var e event
	if err := json.Unmarshal(ev.data, &e); err != nil {
		return &StreamError{fmt.Errorf("%s event: %w", typ, err)}
	}
	switch typ {
	case "error":
		apiErr := &APIError{Status: a.httpStatus}
		if e.Error != nil {
			apiErr.Type, apiErr.Message = e.Error.Type, e.Error.Message
		}
		return apiErr

	case "message_start":
		if e.Message == nil {
			return &StreamError{errors.New("message_start without a message")}
		}
		a.started = true
		a.msg = Response{ID: e.Message.ID, Model: e.Message.Model}
		a.blocks = nil
		a.setStop(e.Message.StopReason, e.Message.StopDetails)
		a.mergeUsage(e.Message.Usage)

	case "content_block_start":
		i, ok := a.index(e.Index)
		if !ok {
			return nil
		}
		var b struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if json.Unmarshal(e.ContentBlock, &b) == nil {
			a.blocks[i].typ = b.Type
			if b.Type == "text" {
				a.blocks[i].text = append(a.blocks[i].text, b.Text...)
			}
		}

	case "content_block_delta":
		i, ok := a.index(e.Index)
		if !ok {
			return nil
		}
		var d struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if json.Unmarshal(e.Delta, &d) == nil && d.Type == "text_delta" {
			if a.blocks[i].typ == "" {
				a.blocks[i].typ = "text"
			}
			if a.blocks[i].typ == "text" {
				a.blocks[i].text = append(a.blocks[i].text, d.Text...)
			}
		}

	case "message_delta":
		var d struct {
			StopReason  *string          `json:"stop_reason"`
			StopDetails *json.RawMessage `json:"stop_details"`
		}
		if json.Unmarshal(e.Delta, &d) == nil {
			a.setStop(d.StopReason, d.StopDetails)
		}
		a.mergeUsage(e.Usage)
	}
	return nil
}

// index makes room for block i. A negative or absurd index is ignored.
func (a *accumulator) index(p *int) (int, bool) {
	if p == nil || *p < 0 || *p > 4096 {
		return 0, false
	}
	for len(a.blocks) <= *p {
		a.blocks = append(a.blocks, block{})
	}
	return *p, true
}

func (a *accumulator) setStop(reason *string, details *json.RawMessage) {
	if reason != nil {
		a.msg.StopReason = *reason
	}
	if details != nil && len(*details) > 0 && string(*details) != "null" {
		var d wireStopDetails
		if json.Unmarshal(*details, &d) == nil {
			a.msg.StopDetails = &StopDetails{Category: d.Category, Explanation: d.Explanation}
		}
	}
}

// mergeUsage lays a usage object over the snapshot: a number that is present
// replaces the old one, an absent one leaves it. The SDK does the same for the
// token counts; iterations and thinking tokens follow the same rule here
// because the beta's events do not say which event carries them.
func (a *accumulator) mergeUsage(u *wireUsage) {
	if u == nil {
		return
	}
	set := func(dst *int, v optInt) {
		if v.ok {
			*dst = v.v
		}
	}
	set(&a.msg.Usage.Input, u.InputTokens)
	set(&a.msg.Usage.Output, u.OutputTokens)
	set(&a.msg.Usage.CacheCreation, u.CacheCreationInputTokens)
	set(&a.msg.Usage.CacheRead, u.CacheReadInputTokens)

	var details struct {
		ThinkingTokens optInt `json:"thinking_tokens"`
	}
	if len(u.OutputTokensDetails) > 0 && json.Unmarshal(u.OutputTokensDetails, &details) == nil && details.ThinkingTokens.ok {
		v := details.ThinkingTokens.v
		a.msg.Usage.ThinkingTokens = &v
	}

	var iterations []wireIteration
	if len(u.Iterations) > 0 && json.Unmarshal(u.Iterations, &iterations) == nil && len(iterations) > 0 {
		out := make([]Iteration, len(iterations))
		for i, it := range iterations {
			out[i] = Iteration{Type: it.Type, Model: it.Model}
			set(&out[i].Input, it.InputTokens)
			set(&out[i].Output, it.OutputTokens)
			set(&out[i].CacheCreation, it.CacheCreationInputTokens)
			set(&out[i].CacheRead, it.CacheReadInputTokens)
		}
		a.msg.Usage.Iterations = out
	}
}

// finish returns the message, or a ConnError when the stream stopped short.
func (a *accumulator) finish() (*Response, error) {
	if !a.started || (!a.stopped && a.msg.StopReason == "") {
		return nil, &ConnError{ErrTruncated}
	}
	var text []byte
	for _, b := range a.blocks {
		if b.typ == "text" {
			text = append(text, b.text...)
		}
	}
	a.msg.Text = string(text)
	return &a.msg, nil
}

// parseStream reads a whole event stream into a Response. httpStatus is the
// status line of the stream's own response, reported by an error event.
func parseStream(r io.Reader, httpStatus int) (*Response, error) {
	acc := &accumulator{httpStatus: httpStatus}
	if err := readSSE(r, acc.handle); err != nil {
		var api *APIError
		var stream *StreamError
		if errors.As(err, &api) || errors.As(err, &stream) {
			return nil, err
		}
		return nil, &ConnError{err}
	}
	return acc.finish()
}
