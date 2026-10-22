// SPDX-License-Identifier: Apache-2.0

package scheduler

import (
	"context"
	"fmt"

	"github.com/FreshLabDev/quoto/internal/domain"
	"github.com/FreshLabDev/quoto/internal/scoring"
)

// publishQuote posts the quote and records what happened. It reports whether the
// quote went out and was recorded as published; the window is cleared after that.
//
// The send is the one step that cannot be undone, so the outcome is recorded by
// what is known. A failure that certainly did not deliver is publish_failed and
// the day is tried again later. A failure that may have delivered (a timeout,
// a network or server error) is publish_unknown and is never retried: a missed
// quote is better than a duplicate. A copied media message that failed on
// anything else falls back to text.
//
// The error is returned only when a status could not be written to the database
// after a send, the one state the operator should hear about.
func (s *Scheduler) publishQuote(ctx context.Context, g domain.GroupSettings, lang string, q domain.Quote, author string, b scoring.Breakdown, note string) (bool, error) {
	view := postView{
		Lang:          lang,
		Quote:         q,
		Author:        author,
		Stars:         b.Stars(),
		ReactionCount: b.ReactionCount,
		MsgLink:       messageLink(g.ChatID, q.MessageID),
		DetailsLink:   s.d.Signer.URL(s.cfg.BotUsername, q.ID),
		Note:          note,
	}
	log := s.log.With("chat_id", g.ChatID, "quote_id", q.ID)

	var (
		sentID  int64
		sendErr error
		copyErr string
	)
	if canCopyMedia(q) {
		sentID, sendErr = s.d.Sender.CopyWithCaption(ctx, g.ChatID, q.MessageID, postText(view, true))
		if sendErr != nil {
			if s.d.Sender.OutcomeUnknown(sendErr) {
				log.Error("media quote delivery outcome unknown", "error", sendErr)
				return false, s.setStatus(ctx, q.ID, domain.StatusPublishUnknown,
					"Telegram media delivery outcome unknown: "+cutRunes(sendErr.Error(), 200))
			}
			log.Warn("media copy failed, posting text", "error", sendErr)
			copyErr = cutRunes(sendErr.Error(), 200)
			sentID, sendErr = s.d.Sender.SendHTML(ctx, g.ChatID, postText(view, false))
		}
	} else {
		sentID, sendErr = s.d.Sender.SendHTML(ctx, g.ChatID, postText(view, false))
	}
	if sendErr != nil {
		status := domain.StatusPublishFailed
		if s.d.Sender.OutcomeUnknown(sendErr) {
			status = domain.StatusPublishUnknown
		}
		log.Error("sending the quote failed", "status", status, "error", sendErr)
		return false, s.setStatus(ctx, q.ID, status, cutRunes(sendErr.Error(), 250))
	}

	if err := s.d.Quotes.MarkPublished(ctx, q.ID, sentID, q.DecisionReason); err != nil {
		log.Error("quote was sent but the database write failed", "error", err)
		return false, s.setStatus(ctx, q.ID, domain.StatusPublishUnknown,
			"Telegram message was sent, but DB finalization failed: "+cutRunes(err.Error(), 200))
	}

	if copyErr != "" {
		s.appendError(ctx, q.ID, "Media copy failed, text fallback used: "+copyErr)
	}
	if g.PinEnabled() {
		// A quote that could not be pinned is still published.
		if err := s.d.Sender.Pin(ctx, g.ChatID, sentID); err != nil {
			s.appendError(ctx, q.ID, "Pin failed: "+cutRunes(err.Error(), 200))
			log.Warn("pin failed", "error", err)
		}
	}
	s.clearWindow(ctx, q)

	log.Info("quote published", "bot_message_id", sentID)
	return true, nil
}

// sendBoringNotice tells the group the day had no quote and records it. Statuses
// follow publishQuote's rule, with the notice's own names.
func (s *Scheduler) sendBoringNotice(ctx context.Context, lang string, q domain.Quote) (bool, error) {
	log := s.log.With("chat_id", q.GroupID, "quote_id", q.ID)
	text := boringText(lang, s.d.Signer.URL(s.cfg.BotUsername, q.ID), q.DecisionReason)

	sentID, err := s.d.Sender.SendHTML(ctx, q.GroupID, text)
	if err != nil {
		status := domain.StatusBoringNoticeFailed
		if s.d.Sender.OutcomeUnknown(err) {
			status = domain.StatusBoringNoticeUnknown
		}
		log.Error("sending the boring-day notice failed", "status", status, "error", err)
		return false, s.setStatus(ctx, q.ID, status, cutRunes(err.Error(), 250))
	}
	if err := s.d.Quotes.MarkNotified(ctx, q.ID, sentID); err != nil {
		log.Error("notice was sent but the database write failed", "error", err)
		return false, s.setStatus(ctx, q.ID, domain.StatusBoringNoticeUnknown,
			"Telegram notice was sent, but DB finalization failed: "+cutRunes(err.Error(), 200))
	}
	s.clearWindow(ctx, q)
	log.Info("day marked boring", "notice_message_id", sentID)
	return true, nil
}

// clearWindow deletes the quote's window of messages. Whatever fails is noted on
// the quote and not retried: the quote is already out, and old messages are
// deleted by the retention job anyway.
func (s *Scheduler) clearWindow(ctx context.Context, q domain.Quote) {
	w := domain.Window{QuoteDay: q.QuoteDay, Start: q.WindowStart, End: q.WindowEnd}
	deleted, err := s.d.Messages.DeleteWindow(ctx, q.GroupID, w)
	if err != nil {
		s.appendError(ctx, q.ID, "Cleanup failed: "+cutRunes(err.Error(), 200))
		s.log.Warn("clearing the window failed", "chat_id", q.GroupID, "quote_id", q.ID, "error", err)
		return
	}
	s.log.Debug("window cleared", "chat_id", q.GroupID, "quote_id", q.ID, "deleted", deleted)
}

// setStatus moves a quote to status and records message as its operation error.
func (s *Scheduler) setStatus(ctx context.Context, id int64, status, message string) error {
	if err := s.d.Quotes.SetStatus(ctx, id, status, domain.TextPatch{}, domain.SetText(message)); err != nil {
		return fmt.Errorf("set quote %d to %s: %w", id, status, err)
	}
	return nil
}

// appendError adds a note to the quote's operation error. A note that cannot be
// written is only logged.
func (s *Scheduler) appendError(ctx context.Context, id int64, message string) {
	if err := s.d.Quotes.AppendError(ctx, id, message); err != nil {
		s.log.Warn("could not note the error on the quote", "quote_id", id, "error", err)
	}
}

// cutRunes is Python's s[:n]: at most n code points, with no ellipsis.
func cutRunes(s string, n int) string {
	if n <= 0 {
		return ""
	}
	i := 0
	for pos := range s {
		if i == n {
			return s[:pos]
		}
		i++
	}
	return s
}
