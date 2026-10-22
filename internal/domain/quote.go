// SPDX-License-Identifier: Apache-2.0

package domain

import (
	"bytes"
	"encoding/json"
	"time"
)

// ContextEntry is one message of a quote's context. IsPrimary marks the quoted
// one. The snapshot is stored in quotes.context_snapshot as JSON and read back
// by the details card and by the published post.
type ContextEntry struct {
	MessageID int64  `json:"message_id"`
	Author    string `json:"author"`
	Text      string `json:"text"`
	IsPrimary bool   `json:"is_primary"`
}

// Quote is a row of quoto.quotes. Optional fields: zero means NULL.
type Quote struct {
	ID       int64
	GroupID  int64
	AuthorID int64
	Text     string

	Score         float64
	ReactionScore float64
	AIScore       float64
	LengthScore   float64
	ReactionCount int

	MessageID       int64 // Telegram id of the quoted message
	ContentType     string
	BotMessageID    int64
	NoticeMessageID int64
	AIModel         string
	AIBestText      string

	// Context is the decoded context_snapshot; empty when the quote had none.
	Context []ContextEntry

	QuoteDay    time.Time
	WindowStart time.Time
	WindowEnd   time.Time

	DecisionStatus  string
	StatusChangedAt time.Time
	DecisionReason  string
	OperationError  string
	CreatedAt       time.Time
}

// NewQuote is a quote row about to be created. The store stamps created_at and
// status_changed_at. Quoto never forces a quote, so forced_by_admin stays false.
type NewQuote struct {
	GroupID  int64
	AuthorID int64
	Text     string

	Score         float64
	ReactionScore float64
	AIScore       float64
	LengthScore   float64
	ReactionCount int

	MessageID   int64
	ContentType string
	AIModel     string
	AIBestText  string
	// Context is stored only when it has more than one message; a single
	// message is the quote itself.
	Context []ContextEntry

	QuoteDay    time.Time
	WindowStart time.Time
	WindowEnd   time.Time

	DecisionStatus string
	DecisionReason string
	OperationError string
}

// QuoteDetail is a quote with the names the details card prints. An empty name
// means core does not know the person or the chat; the caller writes the
// placeholder in the reader's language.
type QuoteDetail struct {
	Quote
	AuthorName string
	GroupName  string
}

// TextPatch is one nullable text column in an update: leave it, set it, or
// write NULL.
type TextPatch struct {
	Set   bool
	Value *string
}

// SetText writes s.
func SetText(s string) TextPatch { return TextPatch{Set: true, Value: &s} }

// SetNull writes NULL.
func SetNull() TextPatch { return TextPatch{Set: true} }

// EncodeContext serialises the context for quotes.context_snapshot and
// quotes.context_message_ids. Fewer than two entries store nothing (both NULL,
// returned as empty strings). The output is valid JSON that Python's json.loads
// reads, without the spaces after separators Python's default adds.
func EncodeContext(entries []ContextEntry) (snapshot, ids string) {
	if len(entries) <= 1 {
		return "", ""
	}
	idList := make([]int64, len(entries))
	for i, e := range entries {
		idList[i] = e.MessageID
	}
	return marshalNoEscape(entries), marshalNoEscape(idList)
}

// DecodeContext reads context_snapshot tolerantly, like the Python reader: bad
// JSON, a non-list, non-object items and entries without text all give nothing.
func DecodeContext(raw string) []ContextEntry {
	if raw == "" {
		return nil
	}
	var items []json.RawMessage
	if err := json.Unmarshal([]byte(raw), &items); err != nil {
		return nil
	}
	var out []ContextEntry
	for _, item := range items {
		var e struct {
			MessageID json.Number `json:"message_id"`
			Author    *string     `json:"author"`
			Text      *string     `json:"text"`
			IsPrimary bool        `json:"is_primary"`
		}
		dec := json.NewDecoder(bytes.NewReader(item))
		dec.UseNumber()
		if err := dec.Decode(&e); err != nil || e.Text == nil || PyStrip(*e.Text) == "" {
			continue
		}
		id, _ := e.MessageID.Int64()
		author := ""
		if e.Author != nil {
			author = *e.Author
		}
		out = append(out, ContextEntry{MessageID: id, Author: author, Text: *e.Text, IsPrimary: e.IsPrimary})
	}
	return out
}

func marshalNoEscape(v any) string {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
	return string(bytes.TrimRight(buf.Bytes(), "\n"))
}
