// SPDX-License-Identifier: Apache-2.0

package domain

import "context"

// Sender is how the scheduler talks to Telegram; internal/telegram implements
// it. Everything it sends is HTML, silent (no notification sound) and without
// link previews, except Alert.
type Sender interface {
	// SendHTML posts text to the chat. A flood wait is waited out and retried:
	// three attempts, min(retry_after, 60)+1 seconds between them. Any other
	// error is returned at once.
	SendHTML(ctx context.Context, chatID int64, html string) (messageID int64, err error)
	// CopyWithCaption copies message messageID of the same chat as a new message
	// with the given HTML caption. Same retry rule as SendHTML.
	CopyWithCaption(ctx context.Context, chatID, messageID int64, captionHTML string) (newMessageID int64, err error)
	// Pin pins a message without notifying. No retry.
	Pin(ctx context.Context, chatID, messageID int64) error
	// SendAgreementReminder posts the "agreement not accepted" notice in lang
	// with the read-and-accept keyboard; outdated selects the "agreement
	// changed" wording. No retry.
	SendAgreementReminder(ctx context.Context, chatID int64, lang string, outdated bool) error
	// Alert sends a plain text message to every developer id, with sound.
	// Secrets are scrubbed first and the same key alerts at most once per ten
	// minutes. It never fails the caller.
	Alert(ctx context.Context, key, text string)

	// OutcomeUnknown reports whether a failed send may nevertheless have been
	// delivered: a timeout, a network error or a server error. The scheduler
	// then records publish_unknown instead of publish_failed and never retries.
	OutcomeUnknown(err error) bool
	// ChatNotFound reports Telegram's "chat not found" answer; the scheduler
	// then marks the group inactive.
	ChatNotFound(err error) bool
}
