// SPDX-License-Identifier: Apache-2.0

package domain

import (
	"context"
	"time"
)

// EvalMessage is one message of the day as the model sees it. Ids are
// messages.id, not Telegram's: the model answers with them and the scheduler
// maps them back. Text and Caption are the full stored strings; the ai package
// clips them (author 128, text and caption 2000, description 1500 runes).
type EvalMessage struct {
	ID      int64
	Author  string
	Text    string // empty for non-text messages, and then left out of the payload
	Caption string
	// Kind is the content type. "text" and "" are left out of the payload.
	Kind string
	// Desc is the media description. For a non-text message without one, the
	// caller passes the stored placeholder text here, as Python did.
	Desc string
	// ReplyToID is the messages.id of the message this one replies to, when
	// that message is part of the same payload; 0 otherwise.
	ReplyToID int64
	// Reactions holds the emoji with a positive count, in stored order.
	Reactions []Reaction
}

// DayVerdict is the model's answer to "is this day worth a quote".
type DayVerdict struct {
	ShouldPublish bool
	ReasonCode    string // ReasonWorthy or ReasonBoringDay
	ReasonText    string // at most 200 runes
}

// QuoteChoice is the model's pick of the quote and of its context. Ids are
// messages.id. PrimaryID 0 means no pick.
type QuoteChoice struct {
	PrimaryID     int64
	ContextIDs    []int64
	ContextNeeded bool
}

// LanguageChoice is the language the model detected for the chat.
type LanguageChoice struct {
	ChatLanguage      string
	InterfaceLanguage string
}

// Score is one message's score from 0 to 1 (the model's 0..10 divided by 10).
type Score struct {
	ID    int64
	Value float64
}

// TokenUsage is what one evaluation cost. A nil field was not reported.
type TokenUsage struct {
	PromptTokens     *int
	CompletionTokens *int
	ReasoningTokens  *int
	TotalTokens      *int
	CostUSD          *float64
}

// EvalResult is the outcome of the daily evaluation. It is always returned,
// never an error: a failed run is Status RunAIFailed or RunParseFailed with
// every message scored 0.5 and DayVerdictError set.
type EvalResult struct {
	// Scores is in the order of the model's reply. The fallback pick relies on
	// that order for ties; on failure it is the input order.
	Scores         []Score
	ActualModel    string
	RequestedModel string
	Status         string // RunParsed, RunEmpty, RunAIFailed or RunParseFailed
	RequestID      string // 32 hex characters, shared by every attempt of one call

	DayVerdict      *DayVerdict
	DayVerdictError string
	QuoteChoice     *QuoteChoice
	LanguageChoice  *LanguageChoice
	Usage           *TokenUsage
}

// Evaluator scores a day's messages. One call per group per day.
type Evaluator interface {
	// Evaluate asks the models, Claude first and OpenRouter after it, for scores,
	// a day verdict and a quote choice. detectLanguage adds the chat-language
	// question to the same request.
	Evaluate(ctx context.Context, msgs []EvalMessage, detectLanguage bool) EvalResult
}

// MediaDescribeRequest is a normalised file ready for a model.
type MediaDescribeRequest struct {
	Path     string // file on disk; the ai package streams it, never loads it whole
	MimeType string
	// Kind is the analysis kind: image, video, audio (or photo, as the cache
	// stores it). Photos, images and stickers may go to Claude; video and audio
	// only to OpenRouter.
	Kind string
}

// MediaDescription is a model's description of a file.
type MediaDescription struct {
	Text             string
	ActualModel      string
	PromptTokens     int
	CompletionTokens int
	TotalTokens      int
}

// MediaDescriber describes one file. Every error it returns is final unless it
// is a *MediaRetryableError.
type MediaDescriber interface {
	DescribeMedia(ctx context.Context, req MediaDescribeRequest) (MediaDescription, error)
}

// MediaRetryableError says the file should stay pending and be tried again
// later: a rate limit, a provider outage, a cooldown after a billing error.
type MediaRetryableError struct {
	Err error
	// RetryAfter is the provider's hint; 0 means the caller picks its own delay.
	RetryAfter time.Duration
	// CountsAsAttempt is false while a provider cooldown is active: nothing was
	// sent, so the attempt budget is untouched.
	CountsAsAttempt bool
}

func (e *MediaRetryableError) Error() string { return e.Err.Error() }
func (e *MediaRetryableError) Unwrap() error { return e.Err }
