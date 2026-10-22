// SPDX-License-Identifier: Apache-2.0

// Package claude talks to the Anthropic Messages API over net/http. There is no
// SDK: the request needs `fallbacks` and a beta header that an SDK may not know,
// and the agreement allows exactly the features used here (no Batch or Files
// API, no Fable or Mythos model).
//
// Every call is streamed and every request carries `fallbacks: "default"` with
// the server-side-fallback beta: when Claude's safety classifiers decline a
// request, the API re-runs it inside the same call on the model Anthropic
// recommends, and Response.Model names the one that answered. Group banter
// trips the classifiers now and then; without this the day would go to the next
// provider or get no quote.
//
// The wire formats of the beta (the fallback content block, usage.iterations,
// stop_details, output_tokens_details) are read from the documented event
// stream and from the Python version's use of the SDK, and checked by tests
// only against that reading. Unknown events, blocks and fields are skipped.
package claude

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
)

const (
	// DefaultBaseURL is the API host.
	DefaultBaseURL = "https://api.anthropic.com"
	// MessagesPath is the endpoint, relative to the base URL.
	MessagesPath = "/v1/messages"
	// MessagesURL is what the audit log records as the request URL, even when
	// the client was pointed elsewhere (a test server).
	MessagesURL = DefaultBaseURL + MessagesPath
	// APIVersion is the anthropic-version header.
	APIVersion = "2023-06-01"
	// FallbackBeta is the anthropic-beta value that enables `fallbacks`.
	FallbackBeta = "server-side-fallback-2026-07-01"
)

// Request is one Messages call. Its JSON form is fixed: model, max_tokens,
// system, messages, thinking, output_config, fallbacks (and stream on the wire).
type Request struct {
	Model     string
	MaxTokens int
	// System is omitted when empty.
	System   string
	Messages []Message
	// Effort is output_config.effort (low, medium, high, xhigh, max); omitted
	// when empty.
	Effort string
	// Schema, when set, asks for structured output: output_config.format with
	// type json_schema. It must already be free of the keywords Claude's
	// structured outputs reject (see StripUnsupported).
	Schema json.RawMessage
}

// Message is one turn: either a plain string or a list of blocks.
type Message struct {
	Role   string
	Text   string
	Blocks []Block
}

// MarshalJSON writes content as a string for a text message and as an array
// for one with blocks.
func (m Message) MarshalJSON() ([]byte, error) {
	if m.Blocks == nil {
		return marshal(struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		}{m.Role, m.Text})
	}
	return marshal(struct {
		Role    string  `json:"role"`
		Content []Block `json:"content"`
	}{m.Role, m.Blocks})
}

// Block is a piece of a message's content: text or a base64 image.
type Block struct {
	text      string
	mediaType string
	data      string
	image     bool
}

// TextBlock is a text content block.
func TextBlock(text string) Block { return Block{text: text} }

// ImageBlock is a base64 image content block. The API takes JPEG, PNG, GIF and
// WebP up to 5 MB.
func ImageBlock(mediaType string, data []byte) Block {
	return Block{image: true, mediaType: mediaType, data: base64.StdEncoding.EncodeToString(data)}
}

// MarshalJSON writes {"type":"text",...} or {"type":"image","source":{...}}.
func (b Block) MarshalJSON() ([]byte, error) {
	if !b.image {
		return marshal(struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}{"text", b.text})
	}
	type source struct {
		Type      string `json:"type"`
		MediaType string `json:"media_type"`
		Data      string `json:"data"`
	}
	return marshal(struct {
		Type   string `json:"type"`
		Source source `json:"source"`
	}{"image", source{"base64", b.mediaType, b.data}})
}

type requestBody struct {
	Model        string       `json:"model"`
	MaxTokens    int          `json:"max_tokens"`
	System       string       `json:"system,omitempty"`
	Messages     []Message    `json:"messages"`
	Thinking     thinking     `json:"thinking"`
	OutputConfig outputConfig `json:"output_config"`
	Fallbacks    string       `json:"fallbacks"`
	Stream       bool         `json:"stream,omitempty"`
}

// thinking is spelled out although adaptive thinking is the default: the body
// then says that the model thinks and that effort sets how much.
type thinking struct {
	Type string `json:"type"`
}

type outputConfig struct {
	Effort string  `json:"effort,omitempty"`
	Format *format `json:"format,omitempty"`
}

type format struct {
	Type   string          `json:"type"`
	Schema json.RawMessage `json:"schema"`
}

func (r Request) body(stream bool) requestBody {
	b := requestBody{
		Model:        r.Model,
		MaxTokens:    r.MaxTokens,
		System:       r.System,
		Messages:     r.Messages,
		Thinking:     thinking{Type: "adaptive"},
		OutputConfig: outputConfig{Effort: r.Effort},
		Fallbacks:    "default",
		Stream:       stream,
	}
	if len(r.Schema) > 0 {
		b.OutputConfig.Format = &format{Type: "json_schema", Schema: r.Schema}
	}
	return b
}

// Body is the request as the audit log records it: the JSON body without the
// transport's stream flag.
func (r Request) Body() ([]byte, error) { return marshal(r.body(false)) }

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
