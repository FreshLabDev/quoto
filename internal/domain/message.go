// SPDX-License-Identifier: Apache-2.0

package domain

import "time"

// A zero value of an optional field below means NULL in the database: an empty
// string, 0 for an id, size or dimension. Telegram never sends a real id, size
// or dimension of 0, and Python treated a 0 duration the same way.

// Person is a Telegram user as it appears in an update.
type Person struct {
	ID           int64
	Username     string
	FirstName    string
	LastName     string
	LanguageCode string
	IsBot        bool
}

// ChatInfo is a Telegram chat as it appears in an update.
type ChatInfo struct {
	ID       int64
	Type     string
	Title    string
	Username string
}

// Window is one day's span of messages: [Start, End) in UTC. QuoteDay is the
// local date of the closing cutoff, stored as midnight UTC of that date.
type Window struct {
	QuoteDay time.Time
	Start    time.Time
	End      time.Time
}

// MediaSource is the file a message carries, as far as Telegram describes it.
type MediaSource struct {
	// Kind is the raw kind: photo, video, animation, video_note, voice, audio,
	// sticker, image (a document that is an image) or document.
	Kind         string
	FileID       string
	FileUniqueID string
	MimeType     string
	FileName     string
	FileSize     int64
	Width        int
	Height       int
	Duration     float64
	// SupportsAnalysis is false for what no model can read (a tgs sticker, a
	// document that is not an image).
	SupportsAnalysis bool
}

// NewMessage is a message to store. With Media set, the row also gets
// media_status pending and a message_media row in the same transaction.
type NewMessage struct {
	MessageID   int64
	ChatID      int64
	UserID      int64
	Text        string
	ContentType string
	Caption     string
	ReplyTo     int64
	CreatedAt   time.Time
	Media       *MediaSource
}

// MessageEdit is an edited_message update for a stored message.
type MessageEdit struct {
	MessageID   int64
	ChatID      int64
	Text        string
	ContentType string
	Caption     string
	ReplyTo     int64
	// KeepText leaves messages.text alone. A media message's text holds the
	// description the media pipeline wrote, and an edit carries only the
	// placeholder.
	KeepText bool
}

// Reaction is one emoji on one message. Custom emoji are "custom:<id>".
type Reaction struct {
	Emoji string
	Count int
}

// MediaSnapshot is a message's first message_media row, which is the only one
// anybody reads.
type MediaSnapshot struct {
	ID           int64
	Status       string
	Description  string // description_snapshot
	Kind         string
	FileID       string
	FileUniqueID string
	MimeType     string
	FileName     string
	FileSize     int64
	Width        int
	Height       int
	Duration     float64
	SHA256       string
	Phash        string
	CacheID      int64
}

// WindowMessage is a stored message with what scoring needs to read it.
type WindowMessage struct {
	ID          int64 // messages.id, the key the AI payload uses
	MessageID   int64 // Telegram's id
	UserID      int64
	Text        string
	ContentType string
	Caption     string
	MediaStatus string
	ReplyTo     int64
	CreatedAt   time.Time
	Reactions   []Reaction
	Media       *MediaSnapshot
}

// ReactionTotal is the number of reactions on the message, of any emoji.
func (m WindowMessage) ReactionTotal() int {
	n := 0
	for _, r := range m.Reactions {
		n += r.Count
	}
	return n
}
