// SPDX-License-Identifier: Apache-2.0

package domain

import (
	"context"
	"time"
)

// The interfaces below are what internal/db implements for the stores more than
// one package uses. A package that needs only part of one declares the smaller
// interface itself; these exist so the parts can be written against one
// contract. Stores owned by one package (media, evaluation reports, statistics)
// are declared by that package.

// Core is Telegram identity, chats and language, which belong to the shared
// core database. Quoto reaches it only through core.touch, core.set_language,
// core.clear_language and core.effective_language, plus three direct reads
// (person names, chat titles, language claims). It never writes core tables.
type Core interface {
	// TouchUser records a person in core (a private chat, or a user who is not
	// in a group). Groups.Ensure is the call for anything that carries a chat.
	TouchUser(ctx context.Context, p Person) error

	// SetLanguage claims a language for a user or a chat. code must already be
	// one i18n.Normalize accepted: core.set_language silently drops the rest.
	SetLanguage(ctx context.Context, scope LangScope, subject int64, code string, source LangSource) error
	// ClearLanguage withdraws Quoto's own claim. The next touch brings back the
	// client hint, so a person who picks "Telegram language" ends up with it.
	ClearLanguage(ctx context.Context, scope LangScope, subject int64) error
	// LanguageClaim is the language core resolved across all bots, and who set
	// it. The zero value means none.
	LanguageClaim(ctx context.Context, scope LangScope, subject int64) (LangClaim, error)
	// ClaimChatLanguageAuto claims code for the chat with source auto, unless any
	// bot already holds a claim for it. It reports whether it wrote.
	ClaimChatLanguageAuto(ctx context.Context, chatID int64, code string) (bool, error)

	// DisplayNames maps person ids to "first last", else the username. Ids core
	// does not know, or that have no name at all, are absent.
	DisplayNames(ctx context.Context, ids []int64) (map[int64]string, error)
}

// Groups is quoto.group_settings.
type Groups interface {
	// Ensure records the person and the chat in core, makes sure the group has a
	// settings row, and marks it active, in one transaction. A new row gets the
	// jittered cutoff minute and nothing else. It does not hydrate the core
	// fields (Name, Language).
	Ensure(ctx context.Context, actor Person, chat ChatInfo) (GroupSettings, error)
	// Get returns the group with Name, Language and LanguageSource filled in.
	Get(ctx context.Context, chatID int64) (GroupSettings, bool, error)
	// ListActive is every group with is_active, hydrated like Get.
	ListActive(ctx context.Context) ([]GroupSettings, error)
	// SetActive reports false when there is no such group.
	SetActive(ctx context.Context, chatID int64, active bool) (bool, error)

	// SetTimezone writes a zone the caller validated with ValidTimezone.
	SetTimezone(ctx context.Context, chatID int64, tz string) (bool, error)
	// SetTimezoneAuto writes the zone only if the group has none yet.
	SetTimezoneAuto(ctx context.Context, chatID int64, tz string) (bool, error)
	// AdjustQuoteTime moves the cutoff by deltaMinutes, wrapping within the day,
	// and writes hour and minute together.
	AdjustQuoteTime(ctx context.Context, chatID int64, deltaMinutes int) (bool, error)
	// AdjustMinMessages moves the minimum, clamped to its bounds.
	AdjustMinMessages(ctx context.Context, chatID int64, delta int) (bool, error)
	// Toggle writes the negation of the effective value; NULL counts as on.
	Toggle(ctx context.Context, chatID int64, key ToggleKey) (bool, error)
	// AcceptAgreement records the acceptance of DocumentVersion. It writes
	// nothing when the group already accepted a version that counts, and reports
	// false only when there is no such group. language must be normalised.
	AcceptAgreement(ctx context.Context, chatID, userID int64, language string) (bool, error)
	// MigrateChatID moves every table that carries the chat id to the new id of a
	// group that became a supergroup, in one transaction. It reports false when
	// there is nothing to move or the new id is already in use.
	MigrateChatID(ctx context.Context, oldID, newID int64, actor Person, newChat ChatInfo) (bool, error)
}

// Messages is quoto.messages and the message_media row that goes with a media
// message.
type Messages interface {
	// Save inserts the message (and its media row). inserted is false when the
	// message was already stored: Telegram delivered it twice.
	Save(ctx context.Context, m NewMessage) (id int64, inserted bool, err error)
	// Edit applies an edited_message. A message that was never stored is ignored.
	Edit(ctx context.Context, e MessageEdit) error

	CountWindow(ctx context.Context, chatID int64, w Window) (int, error)
	// DeleteWindow removes the window's messages; reactions and media rows go
	// with them.
	DeleteWindow(ctx context.Context, chatID int64, w Window) (int, error)
	// ListWindow returns the window's messages oldest first (ties by id), each
	// with its reactions and first media row.
	ListWindow(ctx context.Context, chatID int64, w Window) ([]WindowMessage, error)
	// DeleteOlderThan removes messages created before t, in batches, and returns
	// how many.
	DeleteOlderThan(ctx context.Context, t time.Time) (int64, error)
}

// Reactions is quoto.reactions.
type Reactions interface {
	// SyncAbsolute replaces a message's reactions with counts (from
	// message_reaction_count). A message that is not stored is ignored.
	SyncAbsolute(ctx context.Context, chatID, messageID int64, counts map[string]int) error
	// ApplyDelta adds deltas (from message_reaction): a count that reaches zero
	// deletes its row.
	ApplyDelta(ctx context.Context, chatID, messageID int64, deltas map[string]int) error
}

// Quotes is quoto.quotes.
type Quotes interface {
	// ForDay returns the group's quote for a day, if any.
	ForDay(ctx context.Context, chatID int64, day time.Time) (*Quote, error)
	// Create inserts the day's row. created is false when the group already had
	// one (another worker, or a restart) and then the existing row is returned.
	Create(ctx context.Context, q NewQuote) (quote Quote, created bool, err error)
	// MarkPublished sets the bot's message id, status published and clears the
	// error. reason is written only when non-empty.
	MarkPublished(ctx context.Context, id, botMessageID int64, reason string) error
	// MarkNotified is MarkPublished for the boring-day notice: status
	// skipped_boring.
	MarkNotified(ctx context.Context, id, noticeMessageID int64) error
	// SetStatus changes the status and status_changed_at, and the two text
	// columns only where a patch says so.
	SetStatus(ctx context.Context, id int64, status string, reason, operationError TextPatch) error
	// AppendError adds msg (trimmed, at most 250 characters) to operation_error,
	// separated by " | ".
	AppendError(ctx context.Context, id int64, msg string) error
	Delete(ctx context.Context, id int64) (bool, error)
	// Stale returns the group's in-progress quotes whose status has not changed
	// since olderThan, newest window first.
	Stale(ctx context.Context, chatID int64, olderThan time.Time) ([]Quote, error)
	// Detail returns the quote with names for the details card. publicOnly hides
	// everything that is not published, and the operation error.
	Detail(ctx context.Context, id int64, publicOnly bool) (*QuoteDetail, error)
}
