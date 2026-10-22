// SPDX-License-Identifier: Apache-2.0

// Package openrouter calls OpenRouter's chat completions endpoint over
// net/http.
//
// Two things are not up to the caller. Every request carries
// provider {data_collection: "deny", zdr: true}: the user agreement promises
// that chat text and media only reach endpoints that neither keep nor train on
// it, so there is no setting for it and no request without it (decision E4).
// And a request that carries a file never holds the file in memory: its body is
// written through a pipe and a base64 encoder, reopened for every attempt, with
// Content-Length computed from the file size so the request is not chunked.
package openrouter

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

const (
	// DefaultURL is the chat completions endpoint (the full URL, not a base).
	DefaultURL = "https://openrouter.ai/api/v1/chat/completions"
	// Title is the X-OpenRouter-Title header: the app's name in OpenRouter's
	// dashboard.
	Title = "Quoto"

	// filePlaceholder stands in the marshalled body where the file's base64 goes.
	filePlaceholder = "@@QUOTO-FILE-DATA@@"
)

// Request is one chat completion. Its JSON form is fixed: model, messages,
// response_format, usage, provider, max_tokens, reasoning, with the optional
// ones left out when unset.
type Request struct {
	Model    string
	Messages []Message
	// ResponseFormat is the response_format object, already encoded.
	ResponseFormat json.RawMessage
	// IncludeUsage asks for token counts and the dollar cost: usage {include: true}.
	IncludeUsage bool
	// MaxTokens is sent when positive.
	MaxTokens int
	// ReasoningEffort is sent as reasoning {enabled: true, effort, exclude: true}
	// when not empty.
	ReasoningEffort string
	// File is the path of the media to embed. Exactly one part of the messages
	// (ImagePart, VideoPart or AudioPart) says where it goes.
	File string
}

// Message is one turn: a plain string or a list of parts.
type Message struct {
	Role  string
	Text  string
	Parts []Part
}

// MarshalJSON writes content as a string for a text message and as an array
// for one with parts.
func (m Message) MarshalJSON() ([]byte, error) {
	if m.Parts == nil {
		return marshal(struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		}{m.Role, m.Text})
	}
	return marshal(struct {
		Role    string `json:"role"`
		Content []Part `json:"content"`
	}{m.Role, m.Parts})
}

// Part is a piece of a message's content.
type Part struct {
	kind   string // text, image_url, video_url, input_audio
	text   string
	mime   string
	format string
}

// TextPart is a text content part.
func TextPart(text string) Part { return Part{kind: "text", text: text} }

// ImagePart embeds the request's File as an image_url data URI.
func ImagePart(mime string) Part { return Part{kind: "image_url", mime: mime} }

// VideoPart embeds the request's File as a video_url data URI.
func VideoPart(mime string) Part { return Part{kind: "video_url", mime: mime} }

// AudioPart embeds the request's File as input_audio in the given format
// (mp3, wav, ...).
func AudioPart(format string) Part { return Part{kind: "input_audio", format: format} }

// MarshalJSON writes the part in the shapes OpenRouter takes. A file part holds
// the placeholder where the data goes; Body splits the document there.
func (p Part) MarshalJSON() ([]byte, error) {
	switch p.kind {
	case "text":
		return marshal(struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}{"text", p.text})
	case "image_url", "video_url":
		type ref struct {
			URL string `json:"url"`
		}
		url := ref{"data:" + p.mime + ";base64," + filePlaceholder}
		if p.kind == "image_url" {
			return marshal(struct {
				Type     string `json:"type"`
				ImageURL ref    `json:"image_url"`
			}{p.kind, url})
		}
		return marshal(struct {
			Type     string `json:"type"`
			VideoURL ref    `json:"video_url"`
		}{p.kind, url})
	case "input_audio":
		type audio struct {
			Data   string `json:"data"`
			Format string `json:"format"`
		}
		return marshal(struct {
			Type       string `json:"type"`
			InputAudio audio  `json:"input_audio"`
		}{p.kind, audio{filePlaceholder, p.format}})
	}
	return nil, fmt.Errorf("openrouter: unknown part kind %q", p.kind)
}

// routing is the provider block. A struct with fixed values, not a map and not
// a setting: see the package comment.
type routing struct {
	DataCollection string `json:"data_collection"`
	ZDR            bool   `json:"zdr"`
}

type requestBody struct {
	Model          string          `json:"model"`
	Messages       []Message       `json:"messages"`
	ResponseFormat json.RawMessage `json:"response_format,omitempty"`
	Usage          *usageOption    `json:"usage,omitempty"`
	Provider       routing         `json:"provider"`
	MaxTokens      int             `json:"max_tokens,omitempty"`
	Reasoning      *reasoning      `json:"reasoning,omitempty"`
}

type usageOption struct {
	Include bool `json:"include"`
}

type reasoning struct {
	Enabled bool   `json:"enabled"`
	Effort  string `json:"effort"`
	Exclude bool   `json:"exclude"`
}

// Body is an encoded request. For a request with a file it is two pieces of
// JSON and the path of the file between them.
type Body struct {
	prefix, suffix []byte
	path           string
}

// Body encodes the request.
func (r Request) Body() (Body, error) {
	b := requestBody{
		Model:          r.Model,
		Messages:       r.Messages,
		ResponseFormat: r.ResponseFormat,
		Provider:       routing{DataCollection: "deny", ZDR: true},
	}
	if r.IncludeUsage {
		b.Usage = &usageOption{Include: true}
	}
	if r.MaxTokens > 0 {
		b.MaxTokens = r.MaxTokens
	}
	if r.ReasoningEffort != "" {
		b.Reasoning = &reasoning{Enabled: true, Effort: r.ReasoningEffort, Exclude: true}
	}
	doc, err := marshal(b)
	if err != nil {
		return Body{}, fmt.Errorf("openrouter: encode request: %w", err)
	}
	slots := bytes.Count(doc, []byte(filePlaceholder))
	switch {
	case r.File == "" && slots == 0:
		return Body{prefix: doc}, nil
	case r.File == "":
		return Body{}, errors.New("openrouter: a file part without a file")
	case slots != 1:
		return Body{}, fmt.Errorf("openrouter: a file needs exactly one file part, got %d", slots)
	}
	prefix, suffix, _ := bytes.Cut(doc, []byte(filePlaceholder))
	return Body{prefix: prefix, suffix: suffix, path: r.File}, nil
}

// JSON is the request document, for the audit log. It is only available for a
// request without a file; a streamed body is never held whole.
func (b Body) JSON() (json.RawMessage, bool) {
	if b.path != "" {
		return nil, false
	}
	return b.prefix, true
}

// Open starts one attempt's body: the reader to hand to the HTTP request and
// the exact number of bytes it will deliver. A file is opened here and closed
// when the reader is, so every attempt reads the file again from the start.
//
// The length is len(prefix) + 4*ceil(n/3) + len(suffix) for a file of n bytes.
// If the file changes size after it is measured the reader fails instead of
// delivering a body of the wrong length.
func (b Body) Open() (io.ReadCloser, int64, error) {
	if b.path == "" {
		return io.NopCloser(bytes.NewReader(b.prefix)), int64(len(b.prefix)), nil
	}
	f, err := os.Open(b.path)
	if err != nil {
		return nil, 0, fmt.Errorf("openrouter: open file: %w", err)
	}
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		f.Close()
		if err == nil {
			err = errors.New("not a regular file")
		}
		return nil, 0, fmt.Errorf("openrouter: measure file: %w", err)
	}
	n := info.Size()
	length := int64(len(b.prefix)) + 4*((n+2)/3) + int64(len(b.suffix))

	pr, pw := io.Pipe()
	go func() {
		defer f.Close()
		pw.CloseWithError(b.write(pw, f, n)) // a nil error closes the pipe with EOF
	}()
	return pr, length, nil
}

func (b Body) write(w io.Writer, f io.Reader, n int64) error {
	if _, err := w.Write(b.prefix); err != nil {
		return err
	}
	enc := base64.NewEncoder(base64.StdEncoding, w)
	if _, err := io.CopyN(enc, f, n); err != nil {
		if errors.Is(err, io.EOF) {
			err = fmt.Errorf("openrouter: file is shorter than the %d bytes measured", n)
		}
		return err
	}
	if err := enc.Close(); err != nil {
		return err
	}
	_, err := w.Write(b.suffix)
	return err
}

// marshal is json.Marshal without HTML escaping, which would turn the < and >
// in the prompts into \u003c and \u003e.
func marshal(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

// toValidUTF8 makes a byte snippet safe to log and to store as text.
func toValidUTF8(b []byte) string { return strings.ToValidUTF8(string(b), "\ufffd") }
