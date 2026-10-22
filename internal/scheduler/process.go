// SPDX-License-Identifier: Apache-2.0

package scheduler

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/FreshLabDev/quoto/internal/domain"
	"github.com/FreshLabDev/quoto/internal/i18n"
	"github.com/FreshLabDev/quoto/internal/scoring"
)

// staleError is the operation_error of a quote that sat in an in-progress status
// past staleAfter.
const staleError = "In-progress quote timed out before final status confirmation."

// processGroup is one group's run for one window. done says the day is finished:
// nothing more will be done for it in this process. A day that is not done, and
// an error, make the tick wait retryAfter before trying again.
//
// A day is finished when it was published or skipped on purpose (too few
// messages, a boring day), when its quote row says so, and when there is nothing
// to try again: no eligible message (doc §7.2 F6). It is not finished while it
// can still be published: the AI failed, the send failed with a known outcome,
// the agreement reminder did not go out (F8).
func (s *Scheduler) processGroup(ctx context.Context, g domain.GroupSettings, w domain.Window) (done bool, err error) {
	log := s.log.With("chat_id", g.ChatID, "quote_day", w.QuoteDay.Format(time.DateOnly))
	lang := i18n.OrDefault(g.Language)

	if !g.AgreementAccepted() {
		log.Info("day skipped: the agreement is not accepted")
		return s.remindAgreement(ctx, g, w, lang), nil
	}

	// Stale rows are written off before the day is looked at, so that a quote
	// stuck in publishing is seen as publish_unknown, not as work in progress.
	if err := s.recoverStale(ctx, g.ChatID); err != nil {
		return false, fmt.Errorf("recover stale quotes: %w", err)
	}

	existing, err := s.d.Quotes.ForDay(ctx, g.ChatID, w.QuoteDay)
	if err != nil {
		return false, fmt.Errorf("load the day's quote: %w", err)
	}
	if existing != nil {
		recompute, retried, err := s.retryExisting(ctx, g, *existing, lang)
		if err != nil {
			return false, err
		}
		if !recompute {
			log.Info("day already handled", "status", existing.DecisionStatus)
			return retried || existingIsTerminal(*existing), nil
		}
		log.Info("day will be recomputed after a technical failure", "status", existing.DecisionStatus)
	}

	minMessages := g.MinMessagesEffective()
	count, err := s.d.Messages.CountWindow(ctx, g.ChatID, w)
	if err != nil {
		return false, fmt.Errorf("count window messages: %w", err)
	}
	if count == 0 {
		log.Info("day is empty")
		return true, nil
	}
	if count < minMessages {
		return true, s.skipSmallDay(ctx, g.ChatID, w, count, minMessages)
	}

	source, err := s.d.Messages.ListWindow(ctx, g.ChatID, w)
	if err != nil {
		return false, fmt.Errorf("list window messages: %w", err)
	}
	maxMessages := g.MessageCap(s.cfg.PremiumChats)
	day := scoring.Prepare(source, maxMessages)
	if day.Eligible == 0 {
		// Everything carried the tag (or the window emptied since the count).
		if day.SourceCount > 0 {
			deleted, err := s.d.Messages.DeleteWindow(ctx, g.ChatID, w)
			if err != nil {
				return false, fmt.Errorf("clear window: %w", err)
			}
			log.Info("day is empty after the #quoto filter", "deleted", deleted)
			return true, nil
		}
		log.Info("day is empty")
		return true, nil
	}
	if day.Eligible < minMessages {
		return true, s.skipSmallDay(ctx, g.ChatID, w, day.Eligible, minMessages)
	}

	names, err := s.d.Core.DisplayNames(ctx, userIDs(day.Messages))
	if err != nil {
		return false, fmt.Errorf("resolve author names: %w", err)
	}

	// The model is asked once per day. Asking for the chat's language is part of
	// the same request, and only a chat nobody has claimed a language for needs it.
	res := s.d.Evaluator.Evaluate(ctx, day.Payload(names), g.Language == "")
	if err := ctx.Err(); err != nil {
		// A shutdown cancelled the call: its answer is a failure by the
		// evaluator's own contract, not a verdict. Nothing is recorded; the next
		// start asks again while the window is open.
		return false, err
	}
	ev := scoring.Pick(day, res)

	// From here on the run is committed: the report, the row and the post are
	// written even if a shutdown arrives meanwhile.
	cctx, cancel := commitContext(ctx)
	defer cancel()

	var selected int64
	if ev.Best != nil {
		selected = ev.Best.ID
	}
	s.d.Reports.SaveEvaluationReport(cctx, domain.EvaluationReport{
		GroupID:     g.ChatID,
		Window:      w,
		SourceCount: day.SourceCount,
		Scored:      day.Messages,
		Result:      res,
		SelectedID:  selected,
		AuthorNames: names,
	})

	g, lang = s.applyDetectedLanguage(cctx, g, lang, ev)

	if ev.Best == nil {
		// Nothing can be quoted: the day holds only stickers, or the model scored
		// nothing and picked nothing. Asking again would pay again for the same
		// answer, so the day is closed without a row.
		log.Warn("no message could be picked as the quote")
		return true, nil
	}

	if ev.VerdictError != "" {
		// No usable verdict: neither a quote nor a boring notice. The row records
		// the failure and the day is tried again after retryAfter.
		_, created, err := s.d.Quotes.Create(cctx, s.newQuote(g, w, ev, names, lang, domain.StatusPublishFailed, ev.VerdictError))
		if err != nil {
			return false, fmt.Errorf("create quote: %w", err)
		}
		if created {
			log.Error("the day's verdict is invalid", "error", ev.VerdictError)
		} else {
			log.Info("day already taken by another worker")
		}
		return false, nil
	}

	status := domain.StatusPublishing
	if !ev.ShouldPublish {
		status = domain.StatusSkippedBoring
		if g.BoringNoticeEnabled() {
			status = domain.StatusNotifyingBoring
		}
	}
	quote, created, err := s.d.Quotes.Create(cctx, s.newQuote(g, w, ev, names, lang, status, ""))
	if err != nil {
		return false, fmt.Errorf("create quote: %w", err)
	}
	if !created {
		log.Info("day already taken by another worker")
		return false, nil
	}

	if ev.ShouldPublish {
		author, ok := names[ev.Best.UserID]
		if !ok {
			author = i18n.T(lang, "common.anonymous")
		}
		note := ""
		if day.Truncated && maxMessages > 0 {
			note = i18n.T(lang, "quote_post.context_truncated",
				"shown", strconv.Itoa(maxMessages), "total", strconv.Itoa(day.Eligible))
		}
		return s.publishQuote(cctx, g, lang, quote, author, ev.Breakdown, note)
	}

	if !g.BoringNoticeEnabled() {
		deleted, err := s.d.Messages.DeleteWindow(cctx, g.ChatID, w)
		if err != nil {
			return false, fmt.Errorf("clear window: %w", err)
		}
		log.Info("day marked boring without a notice", "deleted", deleted)
		return true, nil
	}
	return s.sendBoringNotice(cctx, lang, quote)
}

// skipSmallDay clears the window of a day with fewer messages than the group's
// minimum. It is silent: the group is told nothing.
func (s *Scheduler) skipSmallDay(ctx context.Context, chatID int64, w domain.Window, count, minMessages int) error {
	deleted, err := s.d.Messages.DeleteWindow(ctx, chatID, w)
	if err != nil {
		return fmt.Errorf("clear window: %w", err)
	}
	s.log.Info("day skipped: too few messages", "chat_id", chatID, "count", count, "min", minMessages, "deleted", deleted)
	return nil
}

// applyDetectedLanguage records the interface language the model detected for a
// chat nobody has claimed a language for, and the time zone that goes with it,
// and returns the group with the new language so that the post comes out in it.
// A failure here is logged and ignored: the AI has been paid for, and the post
// in the default language is better than no post.
func (s *Scheduler) applyDetectedLanguage(ctx context.Context, g domain.GroupSettings, lang string, ev scoring.Evaluation) (domain.GroupSettings, string) {
	if ev.InterfaceLanguage == "" {
		return g, lang
	}
	code, known := i18n.Normalize(ev.InterfaceLanguage)

	if g.Language == "" && known {
		claimed, err := s.d.Core.ClaimChatLanguageAuto(ctx, g.ChatID, code)
		switch {
		case err != nil:
			s.log.Warn("claim chat language", "chat_id", g.ChatID, "error", err)
		case claimed:
			g.Language, g.LanguageSource = code, domain.SourceAuto
			lang = i18n.OrDefault(code)
			s.log.Info("interface language chosen automatically", "chat_id", g.ChatID, "language", code, "chat_language", ev.ChatLanguage)
		}
	}

	if g.Timezone == nil {
		tz := domain.DefaultTimezoneForLanguage(code)
		set, err := s.d.Groups.SetTimezoneAuto(ctx, g.ChatID, tz)
		switch {
		case err != nil:
			s.log.Warn("set default time zone", "chat_id", g.ChatID, "error", err)
		case set:
			g.Timezone = &tz
			s.log.Info("default time zone chosen", "chat_id", g.ChatID, "timezone", tz)
		}
	}
	return g, lang
}

// newQuote is the quotes row for the day's best message. Reaction and length
// scores are written as 0: they no longer exist (doc §7.5 S1), the columns stay
// for the old rows and for rolling back to Python.
func (s *Scheduler) newQuote(g domain.GroupSettings, w domain.Window, ev scoring.Evaluation, names map[int64]string, lang, status, operationError string) domain.NewQuote {
	best := ev.Best
	n := domain.NewQuote{
		GroupID:        g.ChatID,
		AuthorID:       best.UserID,
		Text:           scoring.ResolvedText(*best),
		Score:          ev.Breakdown.Total(),
		AIScore:        ev.Breakdown.AI,
		ReactionCount:  ev.Breakdown.ReactionCount,
		MessageID:      best.MessageID,
		ContentType:    contentTypeOf(*best),
		AIModel:        ev.Breakdown.Model,
		AIBestText:     ev.Breakdown.BestText,
		QuoteDay:       w.QuoteDay,
		WindowStart:    w.Start,
		WindowEnd:      w.End,
		DecisionStatus: status,
		DecisionReason: ev.ReasonText,
		OperationError: operationError,
	}
	if g.ContextEnabled() {
		n.Context = contextEntries(ev, names, lang)
	}
	return n
}

// contextEntries is the snapshot of the context messages as the post will show
// them. A person core has no name for is "Anonymous" in the group's language.
func contextEntries(ev scoring.Evaluation, names map[int64]string, lang string) []domain.ContextEntry {
	entries := make([]domain.ContextEntry, 0, len(ev.Context))
	for _, m := range ev.Context {
		author, ok := names[m.UserID]
		if !ok {
			author = i18n.T(lang, "common.anonymous")
		}
		entries = append(entries, domain.ContextEntry{
			MessageID: m.MessageID,
			Author:    author,
			Text:      scoring.ResolvedText(m),
			IsPrimary: m.ID == ev.Best.ID,
		})
	}
	return entries
}

func contentTypeOf(m domain.WindowMessage) string {
	if m.ContentType == "" {
		return domain.ContentText
	}
	return m.ContentType
}

func userIDs(ms []domain.WindowMessage) []int64 {
	seen := make(map[int64]bool, len(ms))
	out := make([]int64, 0, len(ms))
	for _, m := range ms {
		if !seen[m.UserID] {
			seen[m.UserID] = true
			out = append(out, m.UserID)
		}
	}
	return out
}

// remindAgreement nudges the group to accept the user agreement, once per quote
// day, and reports whether the day is finished. It is finished when the reminder
// went out (now or earlier today), and when the chat is gone: the group is then
// marked inactive. A failed send leaves the day open for another try (doc §7.2
// F8).
func (s *Scheduler) remindAgreement(ctx context.Context, g domain.GroupSettings, w domain.Window, lang string) bool {
	s.mu.Lock()
	already := s.stateLocked(g.ChatID).reminded.Equal(w.QuoteDay)
	s.mu.Unlock()
	if already {
		return true
	}

	// A group that accepted an older version is told what changed, not asked as
	// if it had never seen the document.
	err := s.d.Sender.SendAgreementReminder(ctx, g.ChatID, lang, g.AgreementOutdated())
	switch {
	case err == nil:
		s.mu.Lock()
		s.stateLocked(g.ChatID).reminded = w.QuoteDay
		s.mu.Unlock()
		s.log.Info("agreement reminder sent", "chat_id", g.ChatID)
		return true
	case s.d.Sender.ChatNotFound(err):
		if _, setErr := s.d.Groups.SetActive(ctx, g.ChatID, false); setErr != nil {
			s.log.Error("pause group", "chat_id", g.ChatID, "error", setErr)
			return false
		}
		s.log.Warn("group paused: Telegram says the chat is not found", "chat_id", g.ChatID)
		return true
	default:
		s.log.Warn("agreement reminder not sent", "chat_id", g.ChatID, "error", err)
		return false
	}
}

// recoverStale writes off the group's quotes that have been publishing or
// notifying for longer than staleAfter. Telegram is not asked what became of the
// message and nothing is sent again: a duplicate quote is worse than a missing
// one.
func (s *Scheduler) recoverStale(ctx context.Context, chatID int64) error {
	stale, err := s.d.Quotes.Stale(ctx, chatID, s.now().Add(-staleAfter))
	if err != nil {
		return err
	}
	for _, q := range stale {
		next := domain.StatusPublishUnknown
		switch q.DecisionStatus {
		case domain.StatusPublishing:
		case domain.StatusNotifyingBoring:
			next = domain.StatusBoringNoticeUnknown
		default:
			continue
		}
		if err := s.d.Quotes.SetStatus(ctx, q.ID, next, domain.TextPatch{}, domain.SetText(staleError)); err != nil {
			return fmt.Errorf("quote %d: %w", q.ID, err)
		}
		s.log.Warn("stale quote written off", "chat_id", chatID, "quote_id", q.ID, "status", next)
	}
	return nil
}

// existingIsTerminal says the day's row needs no more work: it was published or
// skipped, its outcome is unknown (an unknown outcome is never retried), or it
// failed after the message had been sent.
func existingIsTerminal(q domain.Quote) bool {
	switch q.DecisionStatus {
	case domain.StatusPublished, domain.StatusSkippedBoring, domain.StatusPublishUnknown, domain.StatusBoringNoticeUnknown:
		return true
	case domain.StatusPublishFailed:
		return q.BotMessageID != 0
	case domain.StatusBoringNoticeFailed:
		return q.NoticeMessageID != 0
	}
	return false
}

// retryExisting handles a day that already has a quote row. A publish_failed
// row with no message behind it is deleted once it is retryAfter old, and the
// day is then computed again from scratch (recompute). A boring_notice_failed
// row gets its notice sent again, or is settled if notices have been switched
// off since (done). Everything else is left to existingIsTerminal.
func (s *Scheduler) retryExisting(ctx context.Context, g domain.GroupSettings, q domain.Quote, lang string) (recompute, done bool, err error) {
	if q.DecisionStatus != domain.StatusPublishFailed && q.DecisionStatus != domain.StatusBoringNoticeFailed {
		return false, false, nil
	}
	if !s.retryDue(q) {
		return false, false, nil
	}

	switch q.DecisionStatus {
	case domain.StatusPublishFailed:
		if q.BotMessageID != 0 {
			return false, false, nil
		}
		deleted, err := s.d.Quotes.Delete(ctx, q.ID)
		if err != nil {
			return false, false, fmt.Errorf("delete failed quote %d: %w", q.ID, err)
		}
		return deleted, false, nil

	case domain.StatusBoringNoticeFailed:
		if q.NoticeMessageID != 0 {
			return false, false, nil
		}
		cctx, cancel := commitContext(ctx)
		defer cancel()
		if g.BoringNoticeEnabled() {
			sent, err := s.sendBoringNotice(cctx, lang, q)
			return false, sent, err
		}
		if err := s.d.Quotes.SetStatus(cctx, q.ID, domain.StatusSkippedBoring, domain.TextPatch{}, domain.SetNull()); err != nil {
			return false, false, fmt.Errorf("settle quote %d: %w", q.ID, err)
		}
		s.clearWindow(cctx, q)
		return false, true, nil
	}
	return false, false, nil
}

// retryDue says a failed quote is old enough to be tried again.
func (s *Scheduler) retryDue(q domain.Quote) bool {
	changed := q.StatusChangedAt
	if changed.IsZero() {
		changed = q.CreatedAt
	}
	if changed.IsZero() {
		return true
	}
	return s.now().Sub(changed) >= retryAfter
}
