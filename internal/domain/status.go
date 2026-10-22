// SPDX-License-Identifier: Apache-2.0

// Package domain holds the types and string constants shared by every other
// package, plus the narrow store interfaces that internal/db implements. It
// does no I/O.
//
// Every status below is stored as plain text in a VARCHAR column, so the values
// are a contract with the existing data: do not rename one.
package domain

// Quote decision statuses (quotes.decision_status).
const (
	StatusPublishing          = "publishing"
	StatusPublished           = "published"
	StatusNotifyingBoring     = "notifying_boring"
	StatusSkippedBoring       = "skipped_boring"
	StatusPublishFailed       = "publish_failed"
	StatusBoringNoticeFailed  = "boring_notice_failed"
	StatusPublishUnknown      = "publish_unknown"
	StatusBoringNoticeUnknown = "boring_notice_unknown"
)

// InProgress reports whether a quote row is between "created" and "confirmed".
// A row stuck there is turned into an *_unknown status by the scheduler.
func InProgress(status string) bool {
	return status == StatusPublishing || status == StatusNotifyingBoring
}

// Day verdict reason codes (ai_evaluation_runs.day_reason_code).
const (
	ReasonWorthy    = "worthy"
	ReasonBoringDay = "boring_day"
)

// Evaluation run statuses (ai_evaluation_runs.status).
const (
	RunParsed      = "parsed"
	RunEmpty       = "empty"
	RunAIFailed    = "ai_failed"
	RunParseFailed = "parse_failed"
)

// Media statuses (messages.media_status, message_media.analysis_status).
// processing exists only on message_media: it is the lease.
const (
	MediaPending     = "pending"
	MediaProcessing  = "processing"
	MediaDisabled    = "disabled"
	MediaUnsupported = "unsupported"
	MediaCached      = "cached"
	MediaAnalyzed    = "analyzed"
	MediaFailed      = "failed"
)

// Content types and media kinds (messages.content_type, message_media.media_kind).
const (
	ContentText      = "text"
	ContentPhoto     = "photo"
	ContentVideo     = "video"
	ContentAnimation = "animation"
	ContentVideoNote = "video_note"
	ContentVoice     = "voice"
	ContentAudio     = "audio"
	ContentSticker   = "sticker"
	ContentImage     = "image"
	ContentDocument  = "document"
	ContentUnknown   = "unknown"
)

// App is the key Quoto passes to core.touch, core.set_language and
// core.clear_language. It also ranks Quoto's language claims above the other
// bots' on a tie, so it is not a free choice.
const App = "quoto"

// LangScope is the subject of a language claim in core.
type LangScope string

const (
	ScopeUser LangScope = "user"
	ScopeChat LangScope = "chat"
)

// LangSource says who decided a language. Quoto writes manual and auto; it reads
// back client and default as well.
type LangSource string

const (
	SourceManual  LangSource = "manual"
	SourceAuto    LangSource = "auto"
	SourceClient  LangSource = "client"
	SourceDefault LangSource = "default"
)

// LangClaim is the language core resolved for a subject across all bots.
// The zero value means nobody has claimed one.
type LangClaim struct {
	Language string
	Source   LangSource
}

// Set reports whether a language is claimed at all.
func (c LangClaim) Set() bool { return c.Language != "" }
