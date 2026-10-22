// SPDX-License-Identifier: Apache-2.0

package scheduler

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/FreshLabDev/quoto/internal/domain"
)

var (
	defaultStart = time.Date(2026, 3, 26, 19, 0, 0, 0, time.UTC)
	defaultEnd   = time.Date(2026, 3, 27, 19, 0, 0, 0, time.UTC)
)

func (h *harness) detailsLink(quoteID int64) string {
	return h.sched.d.Signer.URL(botUsername, quoteID)
}

// onlySent is the one message the chat received.
func (h *harness) onlySent(chat int64) sentMessage {
	h.t.Helper()
	sent := h.sender.sentTo(chat)
	if len(sent) != 1 {
		h.t.Fatalf("chat %d received %d messages, want 1: %+v", chat, len(sent), sent)
	}
	return sent[0]
}

func (h *harness) status(chat int64) string {
	h.t.Helper()
	return h.store.quote(h.t, chat, day27).DecisionStatus
}

// doc inventory 03 §1.4 step 10, §1.6: the whole path of a day that gets a quote.
// tests/test_scheduler.py::test_quote_job_catches_up_after_scheduled_minute (SCH-05)
// tests/test_scheduler.py::test_process_group_calls_ai_for_publishable_window (SCH-17)
// tests/test_scoring.py::test_pick_best_quote_persists_daily_evaluation_report (SCO-11)
func TestTickPublishesTheDay(t *testing.T) {
	h := newHarness(t)
	h.addGroup(chatA)
	h.fill(chatA, 10)
	h.setMessage(chatA, 10, func(m *domain.WindowMessage) {
		m.Reactions = []domain.Reaction{{Emoji: "😂", Count: 3}, {Emoji: "❤️", Count: 1}}
	})
	h.eval.set(parsedResult(10, domain.ReasonWorthy))

	h.run()

	// One call to the model with the whole day, and no language question for a
	// chat that already has a language.
	if h.eval.callCount() != 1 {
		t.Fatalf("%d AI calls, want 1", h.eval.callCount())
	}
	call := h.eval.call(0)
	if len(call.msgs) != 10 || call.detect {
		t.Errorf("payload %d messages, detect=%t", len(call.msgs), call.detect)
	}
	if got := call.msgs[9]; got.ID != 10 || got.Author != "User 10" || got.Text != "line 10" || got.Kind != "text" ||
		!reflect.DeepEqual(got.Reactions, []domain.Reaction{{Emoji: "😂", Count: 3}, {Emoji: "❤️", Count: 1}}) {
		t.Errorf("last payload message = %+v", got)
	}

	// The report the day produced.
	reports := h.reports.list()
	if len(reports) != 1 {
		t.Fatalf("%d reports", len(reports))
	}
	rep := reports[0]
	if rep.GroupID != chatA || rep.SourceCount != 10 || len(rep.Scored) != 10 || rep.SelectedID != 10 ||
		rep.Window.QuoteDay != day27 || !rep.Window.Start.Equal(defaultStart) || !rep.Window.End.Equal(defaultEnd) {
		t.Errorf("report = group %d source %d scored %d selected %d window %+v", rep.GroupID, rep.SourceCount, len(rep.Scored), rep.SelectedID, rep.Window)
	}
	if rep.AuthorNames[1010] != "User 10" {
		t.Errorf("report names = %v", rep.AuthorNames)
	}

	// The row.
	q := h.store.quote(t, chatA, day27)
	want := domain.Quote{
		ID: 1, GroupID: chatA, AuthorID: 1010, Text: "line 10", Score: 1, AIScore: 1, ReactionScore: 0, LengthScore: 0,
		ReactionCount: 4, MessageID: 110, ContentType: "text", BotMessageID: 101, AIModel: "claude-actual",
		QuoteDay: day27, WindowStart: defaultStart, WindowEnd: defaultEnd,
		DecisionStatus: domain.StatusPublished, DecisionReason: "because",
	}
	q.StatusChangedAt, q.CreatedAt = time.Time{}, time.Time{}
	if !reflect.DeepEqual(q, want) {
		t.Errorf("quote:\n got %+v\nwant %+v", q, want)
	}

	// The post, pinned, and the day's messages gone.
	post := h.onlySent(chatA)
	wantText := "🏆 <b>Quote of the Day</b>\n\n" +
		"<blockquote><i>«line 10»</i>\n— <b>User 10</b></blockquote>\n\n" +
		"<i>★★★★★</i> · 4 ❤️ · 27 March\n\n" +
		"<a href='https://t.me/c/1234567890/110'>Original</a> · <a href='" + h.detailsLink(1) + "'>Details</a> · #quoto"
	if post.text != wantText || post.copyOf != 0 {
		t.Errorf("post:\n got %q\nwant %q", post.text, wantText)
	}
	if !reflect.DeepEqual(h.sender.pins, []int64{101}) {
		t.Errorf("pins = %v", h.sender.pins)
	}
	if h.store.messageCount(chatA) != 0 || h.store.clearedCount(chatA) != 1 {
		t.Errorf("messages left %d, windows cleared %d", h.store.messageCount(chatA), h.store.clearedCount(chatA))
	}

	// The day is finished: later ticks inside the catch-up window do nothing.
	h.clock.Advance(time.Minute)
	h.run()
	if h.eval.callCount() != 1 || len(h.sender.sentTo(chatA)) != 1 || h.store.count("ForDay") != 1 {
		t.Errorf("a finished day was run again: %d AI calls, %d messages, %d lookups",
			h.eval.callCount(), len(h.sender.sentTo(chatA)), h.store.count("ForDay"))
	}
}

// tests/test_scheduler.py::test_quote_job_catches_up_after_scheduled_minute (SCH-05)
// tests/test_scheduler.py::test_quote_job_catches_up_after_midnight_for_late_cutoff (SCH-06)
// tests/test_scheduler.py::test_quote_job_ignores_stale_unprocessed_cutoff (SCH-09)
func TestTickPicksTheDueWindow(t *testing.T) {
	tests := []struct {
		name     string
		hour     int
		minute   int
		now      time.Time
		wantRun  bool
		wantDay  time.Time
		wantEnd  time.Time
		wantFrom time.Time
	}{
		{"three minutes after 21:00 Kyiv", 21, 0, t1903, true, day27, defaultEnd, defaultStart},
		{"the exact minute", 21, 0, defaultEnd, true, day27, defaultEnd, defaultStart},
		{"a minute early", 21, 0, defaultEnd.Add(-time.Minute), false, time.Time{}, time.Time{}, time.Time{}},
		{"cutoff 23:30, 00:15 the next day", 23, 30, time.Date(2026, 3, 27, 22, 15, 0, 0, time.UTC), true, day27,
			time.Date(2026, 3, 27, 21, 30, 0, 0, time.UTC), time.Date(2026, 3, 26, 21, 30, 0, 0, time.UTC)},
		{"two hours and a minute late", 21, 0, time.Date(2026, 3, 27, 21, 1, 0, 0, time.UTC), false, time.Time{}, time.Time{}, time.Time{}},
		{"the last minute of the catch-up", 21, 0, time.Date(2026, 3, 27, 20, 59, 0, 0, time.UTC), true, day27, defaultEnd, defaultStart},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t)
			h.addGroup(chatA, func(g *domain.GroupSettings) { g.QuoteHour, g.QuoteMinute = ptr(tt.hour), ptr(tt.minute) })
			h.fill(chatA, 10)
			h.clock.Set(tt.now)

			h.run()

			h.store.mu.Lock()
			windows := slices.Clone(h.store.windows)
			h.store.mu.Unlock()
			if !tt.wantRun {
				if len(windows) != 0 {
					t.Fatalf("the group was run for %+v", windows)
				}
				return
			}
			if len(windows) == 0 {
				t.Fatal("the group was not run")
			}
			if w := windows[0]; w.QuoteDay != tt.wantDay || !w.End.Equal(tt.wantEnd) || !w.Start.Equal(tt.wantFrom) {
				t.Errorf("window = %+v", w)
			}
		})
	}
}

// tests/test_windows.py::test_resolve_timezone_falls_back_on_bad_value (WIN-04): windows are in
// the group's own zone, and a group without one is on Kyiv time.
func TestTickUsesTheGroupsTimeZone(t *testing.T) {
	h := newHarness(t)
	h.addGroup(chatA, func(g *domain.GroupSettings) { g.Timezone = ptr("Asia/Tokyo") })
	h.addGroup(chatA - 1) // no zone: Kyiv
	h.fill(chatA, 10)
	h.fill(chatA-1, 10)
	// 12:03 UTC is 21:03 in Tokyo and 14:03 in Kyiv.
	h.clock.Set(time.Date(2026, 3, 27, 12, 3, 0, 0, time.UTC))

	h.run()

	if h.eval.callCount() != 1 || len(h.sender.sentTo(chatA)) != 1 || len(h.sender.sentTo(chatA-1)) != 0 {
		t.Fatalf("AI calls %d; sent to Tokyo %d, to the Kyiv group %d", h.eval.callCount(), len(h.sender.sentTo(chatA)), len(h.sender.sentTo(chatA-1)))
	}
}

// tests/test_windows.py::test_resolve_timezone_falls_back_on_bad_value (WIN-05), seen
// from the tick: a zone that cannot be loaded means Europe/Kyiv, not UTC.
func TestTickFallsBackToKyivForAnUnknownZone(t *testing.T) {
	h := newHarness(t) // 19:03 UTC is 21:03 in Kyiv and 19:03 in UTC
	h.addGroup(chatA, func(g *domain.GroupSettings) { g.Timezone = ptr("Not/AZone") })
	h.fill(chatA, 10)
	h.run()
	if h.eval.callCount() != 1 || h.status(chatA) != domain.StatusPublished {
		t.Errorf("AI calls %d", h.eval.callCount())
	}
}

// tests/test_scheduler.py::test_quote_job_processes_multiple_due_groups (SCH-07)
func TestTickRunsEveryDueGroup(t *testing.T) {
	h := newHarness(t)
	for i := int64(0); i < 3; i++ {
		h.addGroup(chatA - i)
		h.fill(chatA-i, 10)
	}
	h.run()
	for i := int64(0); i < 3; i++ {
		if got := h.store.quote(t, chatA-i, day27).DecisionStatus; got != domain.StatusPublished {
			t.Errorf("group %d: %s", chatA-i, got)
		}
	}
	if h.eval.callCount() != 3 {
		t.Errorf("%d AI calls", h.eval.callCount())
	}
}

// tests/test_scheduler.py::test_quote_job_isolates_one_failing_group (SCH-08)
// doc §7.2 F6: a failing run waits 15 minutes.
func TestFailingGroupDoesNotStopOthersAndWaits(t *testing.T) {
	h := newHarness(t)
	for i := int64(0); i < 3; i++ {
		h.addGroup(chatA - i)
		h.fill(chatA-i, 10)
	}
	failing := chatA - 1
	boom := errors.New("boom")
	h.store.failChat("CountWindow", failing, boom)

	h.run()

	for _, chat := range []int64{chatA, chatA - 2} {
		if got := h.store.quote(t, chat, day27).DecisionStatus; got != domain.StatusPublished {
			t.Errorf("group %d: %s", chat, got)
		}
	}
	if h.store.hasQuote(failing, day27) {
		t.Error("the failing group got a quote")
	}
	// The developers hear about it, once, under the key Python used.
	alerts := h.sender.alertList()
	wantKey := fmt.Sprintf("scheduler:%d:2026-03-27", failing)
	wantText := fmt.Sprintf("Scheduler group run failed for chat %d: count window messages: boom", failing)
	if len(alerts) != 1 || alerts[0].key != wantKey || alerts[0].text != wantText {
		t.Fatalf("alerts = %+v, want key %q text %q", alerts, wantKey, wantText)
	}

	// Not again on the next minute, nor at 14 minutes; again at 15.
	countCalls := func() int {
		n := 0
		for _, w := range h.store.callLog() {
			if w == "CountWindow" {
				n++
			}
		}
		return n
	}
	before := countCalls()
	h.clock.Advance(time.Minute)
	h.run()
	h.clock.Advance(13 * time.Minute)
	h.run()
	if got := countCalls(); got != before {
		t.Fatalf("the failed group was tried %d more times inside 15 minutes", got-before)
	}
	h.store.mu.Lock()
	delete(h.store.chatErrs, fmt.Sprintf("CountWindow/%d", failing))
	h.store.mu.Unlock()
	h.clock.Advance(time.Minute)
	h.run()
	if got := h.store.quote(t, failing, day27).DecisionStatus; got != domain.StatusPublished {
		t.Errorf("after 15 minutes: %s", got)
	}
}

// tests/test_scheduler.py::test_process_group_skips_and_reminds_when_agreement_not_accepted (SCH-01)
// tests/test_scheduler.py::test_process_group_asks_again_when_accepted_version_is_outdated (SCH-02)
func TestAgreementMissingRemindsOncePerDay(t *testing.T) {
	for _, tt := range []struct {
		name     string
		tweak    func(*domain.GroupSettings)
		outdated bool
	}{
		{"never accepted", func(g *domain.GroupSettings) { g.AgreementAcceptedAt, g.AgreementVersion = nil, nil }, false},
		{"accepted an older version", func(g *domain.GroupSettings) { g.AgreementVersion = nil }, true},
		{"accepted a version below the minimum", func(g *domain.GroupSettings) { g.AgreementVersion = ptr("1.1") }, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t)
			h.addGroup(chatA, tt.tweak)
			h.fill(chatA, 10)

			h.run()

			want := []reminderCall{{chatA, "en", tt.outdated}}
			if !reflect.DeepEqual(h.sender.reminders, want) {
				t.Fatalf("reminders = %+v, want %+v", h.sender.reminders, want)
			}
			// Nothing else happens: no recovery, no count, no AI, the window stays.
			for _, call := range h.store.callLog() {
				if call == "Stale" || call == "ForDay" || call == "CountWindow" {
					t.Errorf("unexpected call %s for a group without the agreement", call)
				}
			}
			if h.eval.callCount() != 0 || h.store.messageCount(chatA) != 10 {
				t.Errorf("AI calls %d, messages left %d", h.eval.callCount(), h.store.messageCount(chatA))
			}

			// The day is finished, so no second reminder on the next ticks.
			h.clock.Advance(time.Minute)
			h.run()
			if len(h.sender.reminders) != 1 {
				t.Errorf("%d reminders in one day", len(h.sender.reminders))
			}
			// The next quote day brings the next reminder.
			h.clock.Set(t1903.Add(24 * time.Hour))
			h.run()
			if len(h.sender.reminders) != 2 {
				t.Errorf("%d reminders after two days, want 2", len(h.sender.reminders))
			}
		})
	}
}

func TestAgreementReminderUsesTheGroupsLanguage(t *testing.T) {
	h := newHarness(t)
	h.addGroup(chatA, func(g *domain.GroupSettings) { g.AgreementAcceptedAt, g.AgreementVersion, g.Language = nil, nil, "uk" })
	h.run()
	if len(h.sender.reminders) != 1 || h.sender.reminders[0].lang != "uk" {
		t.Errorf("reminders = %+v", h.sender.reminders)
	}
	// A language Quoto cannot render reads in English.
	h2 := newHarness(t)
	h2.addGroup(chatA, func(g *domain.GroupSettings) { g.AgreementAcceptedAt, g.AgreementVersion, g.Language = nil, nil, "xx" })
	h2.run()
	if len(h2.sender.reminders) != 1 || h2.sender.reminders[0].lang != "en" {
		t.Errorf("reminders = %+v", h2.sender.reminders)
	}
}

// tests/test_scheduler.py::test_agreement_reminder_deactivates_missing_chat (SCH-03)
func TestAgreementReminderPausesAGoneChat(t *testing.T) {
	h := newHarness(t)
	g := h.addGroup(chatA, func(g *domain.GroupSettings) { g.AgreementAcceptedAt, g.AgreementVersion = nil, nil })
	h.sender.setErrors(nil, nil, nil, fmt.Errorf("send: %w", errChatGone))

	h.run()

	if !slices.Contains(h.store.callLog(), "SetActive(-1001234567890,false)") || g.IsActive {
		t.Errorf("the group was not paused: calls %v, active %t", h.store.callLog(), g.IsActive)
	}
	if len(h.sender.reminders) != 0 || len(h.sender.alertList()) != 0 {
		t.Errorf("reminders %v alerts %v", h.sender.reminders, h.sender.alertList())
	}
	// A paused group is not listed again.
	h.clock.Advance(time.Minute)
	h.run()
	if n := h.store.count("SetActive(-1001234567890,false)"); n != 1 {
		t.Errorf("paused %d times", n)
	}
}

// doc §7.2 F8: a reminder that did not go out is tried again, not counted as sent.
func TestAgreementReminderFailureIsRetried(t *testing.T) {
	h := newHarness(t)
	h.addGroup(chatA, func(g *domain.GroupSettings) { g.AgreementAcceptedAt, g.AgreementVersion = nil, nil })
	h.sender.setErrors(nil, nil, nil, errors.New("telegram is down"))

	h.run()
	if len(h.sender.reminders) != 0 {
		t.Fatal("the reminder cannot have gone out")
	}
	if len(h.sender.alertList()) != 0 {
		t.Errorf("a failed reminder is a warning, not an alert: %v", h.sender.alertList())
	}

	// Not every minute: fifteen minutes later.
	h.sender.setErrors(nil, nil, nil, nil)
	h.clock.Advance(14 * time.Minute)
	h.run()
	if len(h.sender.reminders) != 0 {
		t.Fatal("retried too early")
	}
	h.clock.Advance(time.Minute)
	h.run()
	if len(h.sender.reminders) != 1 {
		t.Fatalf("%d reminders after the retry", len(h.sender.reminders))
	}
	// Sent now, so the day is finished.
	h.clock.Advance(time.Minute)
	h.run()
	if len(h.sender.reminders) != 1 {
		t.Errorf("%d reminders", len(h.sender.reminders))
	}
}

// tests/test_scheduler.py::test_process_group_recovers_stale_quotes_before_day_lookup (SCH-04)
// doc inventory 03 §1.8: stale recovery.
func TestStaleQuotesAreWrittenOffBeforeTheDayIsLooked(t *testing.T) {
	h := newHarness(t)
	h.addGroup(chatA)
	h.fill(chatA, 10)
	old := h.clock.Now().Add(-16 * time.Minute)
	fresh := h.clock.Now().Add(-14 * time.Minute)
	mk := func(day int, status string, at time.Time) int64 {
		return h.store.addQuote(domain.Quote{
			GroupID: chatA, QuoteDay: time.Date(2026, 3, day, 0, 0, 0, 0, time.UTC), DecisionStatus: status,
			StatusChangedAt: at, CreatedAt: at,
			WindowStart: defaultStart.AddDate(0, 0, day-27), WindowEnd: defaultEnd.AddDate(0, 0, day-27),
		}).ID
	}
	publishing := mk(24, domain.StatusPublishing, old)
	notifying := mk(25, domain.StatusNotifyingBoring, old)
	young := mk(26, domain.StatusPublishing, fresh)
	finished := mk(23, domain.StatusPublished, old)

	h.run()

	calls := h.store.callLog()
	if slices.Index(calls, "Stale") < 0 || slices.Index(calls, "Stale") > slices.Index(calls, "ForDay") {
		t.Errorf("recovery must come before the lookup: %v", calls)
	}
	status := func(id int64) domain.Quote {
		h.store.mu.Lock()
		defer h.store.mu.Unlock()
		return *h.store.find(id)
	}
	if q := status(publishing); q.DecisionStatus != domain.StatusPublishUnknown || q.OperationError != staleError {
		t.Errorf("stale publishing = %s / %q", q.DecisionStatus, q.OperationError)
	}
	if q := status(notifying); q.DecisionStatus != domain.StatusBoringNoticeUnknown || q.OperationError != staleError {
		t.Errorf("stale notifying = %s / %q", q.DecisionStatus, q.OperationError)
	}
	if q := status(young); q.DecisionStatus != domain.StatusPublishing {
		t.Errorf("a 14-minute-old publishing quote was touched: %s", q.DecisionStatus)
	}
	if q := status(finished); q.DecisionStatus != domain.StatusPublished {
		t.Errorf("a published quote was touched: %s", q.DecisionStatus)
	}
	// Nothing was resent for the written-off ones: only today's quote was posted.
	if len(h.sender.sentTo(chatA)) != 1 {
		t.Errorf("%d messages sent", len(h.sender.sentTo(chatA)))
	}
}

// doc inventory 03 §1.8 and the outcome table of §1.4: a quote of the day that is
// stuck is written off and the day is finished; it is never resent.
func TestStaleQuoteOfTheDayFinishesTheDay(t *testing.T) {
	h := newHarness(t)
	h.addGroup(chatA)
	h.fill(chatA, 10)
	at := h.clock.Now().Add(-20 * time.Minute)
	h.store.addQuote(domain.Quote{GroupID: chatA, QuoteDay: day27, DecisionStatus: domain.StatusPublishing, StatusChangedAt: at, CreatedAt: at,
		WindowStart: defaultStart, WindowEnd: defaultEnd})

	h.run()

	if got := h.status(chatA); got != domain.StatusPublishUnknown {
		t.Fatalf("status = %s", got)
	}
	if h.eval.callCount() != 0 || len(h.sender.sentTo(chatA)) != 0 || h.store.messageCount(chatA) != 10 {
		t.Errorf("AI %d, sent %d, messages %d", h.eval.callCount(), len(h.sender.sentTo(chatA)), h.store.messageCount(chatA))
	}
	// Finished: the next tick does not even look.
	lookups := h.store.count("ForDay")
	h.clock.Advance(time.Minute)
	h.run()
	if h.store.count("ForDay") != lookups {
		t.Error("a finished day was looked at again")
	}
}

// A quote that is in progress and young is somebody's work: nothing is done, and
// the day is not finished.
func TestInProgressQuoteIsLeftAlone(t *testing.T) {
	h := newHarness(t)
	h.addGroup(chatA)
	h.fill(chatA, 10)
	at := h.clock.Now().Add(-2 * time.Minute)
	h.store.addQuote(domain.Quote{GroupID: chatA, QuoteDay: day27, DecisionStatus: domain.StatusPublishing, StatusChangedAt: at, CreatedAt: at,
		WindowStart: defaultStart, WindowEnd: defaultEnd})

	h.run()

	if h.eval.callCount() != 0 || h.status(chatA) != domain.StatusPublishing {
		t.Errorf("AI calls %d, status %s", h.eval.callCount(), h.status(chatA))
	}
}

// tests/test_scheduler.py::test_process_group_retries_publish_failed_after_cooldown (SCH-10)
// tests/test_scheduler.py::test_process_group_leaves_recent_publish_failed_for_cooldown (SCH-11)
func TestPublishFailedIsRetriedAfterFifteenMinutes(t *testing.T) {
	for _, tt := range []struct {
		name      string
		age       time.Duration
		botID     int64
		wantRerun bool
	}{
		{"16 minutes old", 16 * time.Minute, 0, true},
		{"exactly 15 minutes old", 15 * time.Minute, 0, true},
		{"11 minutes old", 11 * time.Minute, 0, false},
		{"old, but a message was sent", 16 * time.Minute, 555, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t)
			h.addGroup(chatA)
			h.fill(chatA, 10)
			at := h.clock.Now().Add(-tt.age)
			h.store.addQuote(domain.Quote{GroupID: chatA, QuoteDay: day27, DecisionStatus: domain.StatusPublishFailed,
				StatusChangedAt: at, CreatedAt: at.Add(-time.Hour), BotMessageID: tt.botID, WindowStart: defaultStart, WindowEnd: defaultEnd})

			h.run()

			if tt.wantRerun {
				if h.eval.callCount() != 1 || h.status(chatA) != domain.StatusPublished || len(h.sender.sentTo(chatA)) != 1 {
					t.Fatalf("AI %d, status %s, sent %d", h.eval.callCount(), h.status(chatA), len(h.sender.sentTo(chatA)))
				}
				return
			}
			if h.eval.callCount() != 0 || len(h.sender.sentTo(chatA)) != 0 || h.store.count("Delete") != 0 {
				t.Errorf("the failed quote was retried: AI %d sent %d deletes %d", h.eval.callCount(), len(h.sender.sentTo(chatA)), h.store.count("Delete"))
			}
		})
	}
}

// tests/test_scheduler.py::test_process_group_retries_failed_boring_notice_after_cooldown (SCH-12)
// tests/test_scheduler.py::test_process_group_finalizes_failed_boring_notice_when_notice_disabled (SCH-13)
func TestBoringNoticeFailedIsRetried(t *testing.T) {
	setup := func(t *testing.T, notices bool, noticeID int64) *harness {
		h := newHarness(t)
		h.addGroup(chatA, func(g *domain.GroupSettings) { g.BoringNotice = ptr(notices) })
		h.fill(chatA, 10)
		at := h.clock.Now().Add(-16 * time.Minute)
		h.store.addQuote(domain.Quote{GroupID: chatA, QuoteDay: day27, DecisionStatus: domain.StatusBoringNoticeFailed,
			StatusChangedAt: at, CreatedAt: at, OperationError: "flood", DecisionReason: "flat day", NoticeMessageID: noticeID,
			WindowStart: defaultStart, WindowEnd: defaultEnd})
		return h
	}

	t.Run("notices on: sent again, window cleared", func(t *testing.T) {
		h := setup(t, true, 0)
		h.run()
		q := h.store.quote(t, chatA, day27)
		if q.DecisionStatus != domain.StatusSkippedBoring || q.NoticeMessageID != 101 || q.OperationError != "" {
			t.Errorf("quote = %s notice %d error %q", q.DecisionStatus, q.NoticeMessageID, q.OperationError)
		}
		want := boringText("en", h.detailsLink(1), "flat day")
		if got := h.onlySent(chatA).text; got != want {
			t.Errorf("notice:\n got %q\nwant %q", got, want)
		}
		if h.store.clearedCount(chatA) != 1 || h.eval.callCount() != 0 {
			t.Errorf("cleared %d, AI calls %d", h.store.clearedCount(chatA), h.eval.callCount())
		}
		// Settled, so finished.
		looks := h.store.count("ForDay")
		h.clock.Advance(time.Minute)
		h.run()
		if h.store.count("ForDay") != looks {
			t.Error("the settled day was looked at again")
		}
	})

	t.Run("notices off: settled without a message", func(t *testing.T) {
		h := setup(t, false, 0)
		h.run()
		q := h.store.quote(t, chatA, day27)
		if q.DecisionStatus != domain.StatusSkippedBoring || q.OperationError != "" {
			t.Errorf("quote = %s error %q", q.DecisionStatus, q.OperationError)
		}
		if len(h.sender.sentTo(chatA)) != 0 || h.store.clearedCount(chatA) != 1 {
			t.Errorf("sent %d, cleared %d", len(h.sender.sentTo(chatA)), h.store.clearedCount(chatA))
		}
	})

	t.Run("a notice id means it was sent: nothing to do", func(t *testing.T) {
		h := setup(t, true, 77)
		h.run()
		if len(h.sender.sentTo(chatA)) != 0 || h.status(chatA) != domain.StatusBoringNoticeFailed {
			t.Errorf("sent %d, status %s", len(h.sender.sentTo(chatA)), h.status(chatA))
		}
	})

	t.Run("still failing: stays failed, tried again later", func(t *testing.T) {
		h := setup(t, true, 0)
		h.sender.setErrors(errors.New("flood wait"), nil, nil, nil)
		h.run()
		q := h.store.quote(t, chatA, day27)
		if q.DecisionStatus != domain.StatusBoringNoticeFailed || q.OperationError != "flood wait" {
			t.Errorf("quote = %s / %q", q.DecisionStatus, q.OperationError)
		}
		if h.store.clearedCount(chatA) != 0 {
			t.Error("the window was cleared without a notice")
		}
	})
}

// tests/test_scheduler.py::test_process_group_does_not_auto_retry_unknown_statuses (SCH-14)
func TestUnknownOutcomesAreNeverRetried(t *testing.T) {
	for _, status := range []string{domain.StatusPublishUnknown, domain.StatusBoringNoticeUnknown, domain.StatusPublished, domain.StatusSkippedBoring} {
		t.Run(status, func(t *testing.T) {
			h := newHarness(t)
			h.addGroup(chatA)
			h.fill(chatA, 10)
			at := h.clock.Now().Add(-3 * time.Hour)
			h.store.addQuote(domain.Quote{GroupID: chatA, QuoteDay: day27, DecisionStatus: status, StatusChangedAt: at, CreatedAt: at})

			h.run()
			h.clock.Advance(20 * time.Minute)
			h.sched.tick(context.Background()) // outside the window by now; the run above decided
			h.sched.wg.Wait()

			if h.eval.callCount() != 0 || len(h.sender.sentTo(chatA)) != 0 || h.store.count("Delete") != 0 {
				t.Errorf("AI %d, sent %d, deletes %d", h.eval.callCount(), len(h.sender.sentTo(chatA)), h.store.count("Delete"))
			}
			if h.status(chatA) != status {
				t.Errorf("status moved to %s", h.status(chatA))
			}
		})
	}
}

func TestExistingIsTerminal(t *testing.T) {
	tests := []struct {
		q    domain.Quote
		want bool
	}{
		{domain.Quote{DecisionStatus: domain.StatusPublished}, true},
		{domain.Quote{DecisionStatus: domain.StatusSkippedBoring}, true},
		{domain.Quote{DecisionStatus: domain.StatusPublishUnknown}, true},
		{domain.Quote{DecisionStatus: domain.StatusBoringNoticeUnknown}, true},
		{domain.Quote{DecisionStatus: domain.StatusPublishFailed}, false},
		{domain.Quote{DecisionStatus: domain.StatusPublishFailed, BotMessageID: 1}, true},
		{domain.Quote{DecisionStatus: domain.StatusBoringNoticeFailed}, false},
		{domain.Quote{DecisionStatus: domain.StatusBoringNoticeFailed, NoticeMessageID: 1}, true},
		{domain.Quote{DecisionStatus: domain.StatusPublishing}, false},
		{domain.Quote{DecisionStatus: domain.StatusNotifyingBoring}, false},
	}
	for _, tt := range tests {
		if got := existingIsTerminal(tt.q); got != tt.want {
			t.Errorf("%s (bot %d, notice %d): %t, want %t", tt.q.DecisionStatus, tt.q.BotMessageID, tt.q.NoticeMessageID, got, tt.want)
		}
	}
}

// tests/test_scheduler.py::test_process_group_skips_empty_window_before_ai (SCH-15)
func TestEmptyDayIsFinishedWithoutAI(t *testing.T) {
	h := newHarness(t)
	h.addGroup(chatA)
	h.run()
	if h.eval.callCount() != 0 || h.store.clearedCount(chatA) != 0 || len(h.sender.sentTo(chatA)) != 0 {
		t.Errorf("AI %d cleared %d sent %d", h.eval.callCount(), h.store.clearedCount(chatA), len(h.sender.sentTo(chatA)))
	}
	counts := h.store.count("CountWindow")
	h.clock.Advance(time.Minute)
	h.run()
	if h.store.count("CountWindow") != counts {
		t.Error("an empty day was counted again")
	}
}

// tests/test_scheduler.py::test_process_group_skips_subthreshold_window_before_ai (SCH-16)
// tests/test_scheduler.py::test_process_group_calls_ai_for_publishable_window (SCH-17)
// tests/test_scheduler.py::test_process_group_uses_group_min_messages_setting (SCH-18)
func TestMinimumMessages(t *testing.T) {
	tests := []struct {
		name    string
		min     *int
		count   int
		wantRun bool
	}{
		{"nine of ten", nil, 9, false},
		{"exactly ten", nil, 10, true},
		{"eleven", nil, 11, true},
		{"the group's own minimum, one short", ptr(15), 14, false},
		{"the group's own minimum, exact", ptr(15), 15, true},
		{"a low minimum", ptr(3), 3, true},
		{"a minimum above the panel's range is cut to 200", ptr(500), 199, false},
		{"a minimum below the range is cut to 1", ptr(-4), 1, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t)
			h.addGroup(chatA, func(g *domain.GroupSettings) { g.MinMessages = tt.min })
			h.fill(chatA, tt.count)
			h.eval.set(parsedResult(tt.count, domain.ReasonWorthy))

			h.run()

			if !tt.wantRun {
				if h.eval.callCount() != 0 || len(h.sender.sentTo(chatA)) != 0 {
					t.Errorf("AI %d, sent %d", h.eval.callCount(), len(h.sender.sentTo(chatA)))
				}
				// Cleared in silence; the day is over; no row.
				if h.store.clearedCount(chatA) != 1 || h.store.messageCount(chatA) != 0 || h.store.hasQuote(chatA, day27) {
					t.Errorf("cleared %d, left %d, quote %t", h.store.clearedCount(chatA), h.store.messageCount(chatA), h.store.hasQuote(chatA, day27))
				}
				return
			}
			if h.eval.callCount() != 1 || h.status(chatA) != domain.StatusPublished {
				t.Errorf("AI %d, status %s", h.eval.callCount(), h.status(chatA))
			}
			// The window is cleared by the publication, not before the AI.
			if h.store.clearedCount(chatA) != 1 {
				t.Errorf("cleared %d times", h.store.clearedCount(chatA))
			}
		})
	}
}

// tests/test_scheduler.py::test_process_group_clears_window_when_all_messages_filtered_before_ai (SCH-23)
// tests/test_scoring.py::test_pick_best_quote_skips_ai_when_all_messages_filtered (SCO-09)
func TestQuotoTaggedMessagesDoNotCountTowardTheMinimum(t *testing.T) {
	t.Run("everything tagged", func(t *testing.T) {
		h := newHarness(t)
		h.addGroup(chatA)
		h.fill(chatA, 10)
		for i := int64(1); i <= 10; i++ {
			h.setMessage(chatA, i, func(m *domain.WindowMessage) { m.Text += " #quoto" })
		}
		h.run()
		if h.eval.callCount() != 0 || h.store.clearedCount(chatA) != 1 || len(h.sender.sentTo(chatA)) != 0 || h.store.hasQuote(chatA, day27) {
			t.Errorf("AI %d cleared %d sent %d", h.eval.callCount(), h.store.clearedCount(chatA), len(h.sender.sentTo(chatA)))
		}
		// Finished.
		lookups := h.store.count("CountWindow")
		h.clock.Advance(time.Minute)
		h.run()
		if h.store.count("CountWindow") != lookups {
			t.Error("a finished day was counted again")
		}
	})

	t.Run("ten messages, two tagged: eight is below the minimum", func(t *testing.T) {
		h := newHarness(t)
		h.addGroup(chatA)
		h.fill(chatA, 10)
		h.setMessage(chatA, 3, func(m *domain.WindowMessage) { m.Text = "report #quoto" })
		h.setMessage(chatA, 4, func(m *domain.WindowMessage) { m.Text = "#QUOTO" })
		h.run()
		if h.eval.callCount() != 0 || h.store.clearedCount(chatA) != 1 {
			t.Errorf("AI %d cleared %d", h.eval.callCount(), h.store.clearedCount(chatA))
		}
	})

	t.Run("eleven messages, one tagged: the model sees ten", func(t *testing.T) {
		h := newHarness(t)
		h.addGroup(chatA)
		h.fill(chatA, 11)
		h.setMessage(chatA, 3, func(m *domain.WindowMessage) { m.Text = "report #quoto" })
		h.eval.set(parsedResult(11, domain.ReasonWorthy))
		h.run()
		if h.eval.callCount() != 1 || len(h.eval.call(0).msgs) != 10 {
			t.Fatalf("AI calls %d", h.eval.callCount())
		}
		rep := h.reports.list()[0]
		if rep.SourceCount != 11 || len(rep.Scored) != 10 {
			t.Errorf("report source %d scored %d", rep.SourceCount, len(rep.Scored))
		}
	})
}

// tests/test_scheduler.py::test_process_group_does_not_detect_language_when_already_set (SCH-19)
// tests/test_scheduler.py::test_process_group_persists_detected_language_before_publish (SCH-20)
func TestLanguageDetection(t *testing.T) {
	uk := func() domain.EvalResult {
		r := parsedResult(10, domain.ReasonWorthy)
		r.LanguageChoice = &domain.LanguageChoice{ChatLanguage: "Ukrainian", InterfaceLanguage: "uk"}
		return r
	}

	t.Run("a chat with a language is not asked", func(t *testing.T) {
		h := newHarness(t)
		h.addGroup(chatA) // en
		h.fill(chatA, 10)
		h.run()
		if h.eval.call(0).detect {
			t.Error("asked for the language of a chat that has one")
		}
	})

	t.Run("a language Quoto has no locale for is not asked again", func(t *testing.T) {
		h := newHarness(t)
		h.addGroup(chatA, func(g *domain.GroupSettings) { g.Language = "xx" })
		h.fill(chatA, 10)
		h.eval.set(uk())
		h.run()
		if h.eval.call(0).detect || h.store.count("ClaimChatLanguageAuto") != 0 {
			t.Errorf("detect %t, claims %d", h.eval.call(0).detect, h.store.count("ClaimChatLanguageAuto"))
		}
		if !strings.HasPrefix(h.onlySent(chatA).text, "🏆 <b>Quote of the Day</b>") {
			t.Errorf("an unrenderable language falls back to English: %q", h.onlySent(chatA).text)
		}
	})

	t.Run("a chat without one is asked, and the post comes out in the answer", func(t *testing.T) {
		h := newHarness(t)
		g := h.addGroup(chatA, func(g *domain.GroupSettings) { g.Language = "" })
		h.fill(chatA, 10)
		h.eval.set(uk())
		h.run()

		if !h.eval.call(0).detect {
			t.Error("did not ask for the language")
		}
		if !reflect.DeepEqual(h.store.claims, []string{"uk"}) {
			t.Errorf("claims = %v", h.store.claims)
		}
		if g.Language != "uk" || g.LanguageSource != domain.SourceAuto {
			t.Errorf("group = %q %q", g.Language, g.LanguageSource)
		}
		// The time zone follows the language, once.
		if g.Timezone == nil || *g.Timezone != "Europe/Kyiv" {
			t.Errorf("timezone = %v", g.Timezone)
		}
		post := h.onlySent(chatA).text
		if !strings.HasPrefix(post, "🏆 <b>Цитата дня</b>") {
			t.Errorf("post is not in Ukrainian: %q", post)
		}
		if !strings.Contains(post, "27 березня") {
			t.Errorf("date is not in Ukrainian: %q", post)
		}
		// Claimed before the quote was created, in order.
		calls := h.store.callLog()
		if slices.Index(calls, "ClaimChatLanguageAuto") > slices.Index(calls, "Create") {
			t.Errorf("the language must be saved before the post: %v", calls)
		}
	})

	t.Run("somebody else claimed first: the post stays in the default language", func(t *testing.T) {
		h := newHarness(t)
		h.addGroup(chatA, func(g *domain.GroupSettings) { g.Language = "" })
		h.store.claimResult = false
		h.fill(chatA, 10)
		h.eval.set(uk())
		h.run()
		if !strings.HasPrefix(h.onlySent(chatA).text, "🏆 <b>Quote of the Day</b>") {
			t.Errorf("post = %q", h.onlySent(chatA).text)
		}
		if !slices.Contains(h.store.callLog(), "SetTimezoneAuto(Europe/Kyiv)") {
			t.Error("the zone is set whether or not the claim went through")
		}
	})

	t.Run("a failing claim does not lose the day", func(t *testing.T) {
		h := newHarness(t)
		h.addGroup(chatA, func(g *domain.GroupSettings) { g.Language = "" })
		h.store.fail("ClaimChatLanguageAuto", errors.New("core is down"))
		h.fill(chatA, 10)
		h.eval.set(uk())
		h.run()
		if h.status(chatA) != domain.StatusPublished {
			t.Errorf("status = %s", h.status(chatA))
		}
	})

	t.Run("zone by language", func(t *testing.T) {
		for lang, want := range map[string]string{"ru": "Europe/Moscow", "en": "UTC", "de": "Europe/Berlin", "uk": "Europe/Kyiv", "pl": "Europe/Kyiv"} {
			h := newHarness(t)
			g := h.addGroup(chatA, func(g *domain.GroupSettings) { g.Language = "" })
			h.fill(chatA, 10)
			r := parsedResult(10, domain.ReasonWorthy)
			r.LanguageChoice = &domain.LanguageChoice{InterfaceLanguage: lang}
			h.eval.set(r)
			h.run()
			if g.Timezone == nil || *g.Timezone != want {
				t.Errorf("%s: timezone %v, want %s", lang, g.Timezone, want)
			}
		}
	})

	t.Run("a group with a zone keeps it", func(t *testing.T) {
		h := newHarness(t)
		g := h.addGroup(chatA, func(g *domain.GroupSettings) { g.Language, g.Timezone = "", ptr("Asia/Tokyo") })
		h.clock.Set(time.Date(2026, 3, 27, 12, 3, 0, 0, time.UTC)) // 21:03 in Tokyo
		h.fill(chatA, 10)
		h.eval.set(uk())
		h.run()
		if *g.Timezone != "Asia/Tokyo" {
			t.Errorf("timezone = %s", *g.Timezone)
		}
	})
}

// tests/test_scheduler.py::test_process_group_disables_quote_context_storage (SCH-21)
func TestContextSnapshot(t *testing.T) {
	choice := &domain.QuoteChoice{PrimaryID: 10, ContextIDs: []int64{9, 10}, ContextNeeded: true}
	setup := func(t *testing.T, enabled *bool) *harness {
		h := newHarness(t)
		h.addGroup(chatA, func(g *domain.GroupSettings) { g.QuoteContext = enabled })
		h.fill(chatA, 10)
		r := parsedResult(10, domain.ReasonWorthy)
		r.QuoteChoice = choice
		h.eval.set(r)
		return h
	}

	for _, tt := range []struct {
		name    string
		enabled *bool
		want    bool
	}{{"on", ptr(true), true}, {"unset means on", nil, true}, {"off", ptr(false), false}} {
		t.Run(tt.name, func(t *testing.T) {
			h := setup(t, tt.enabled)
			h.run()
			q := h.store.quote(t, chatA, day27)
			if !tt.want {
				if len(q.Context) != 0 {
					t.Errorf("context stored with the setting off: %+v", q.Context)
				}
				if !strings.Contains(h.onlySent(chatA).text, "<i>«line 10»</i>\n— <b>User 10</b>") {
					t.Errorf("the post should be a plain quote: %q", h.onlySent(chatA).text)
				}
				return
			}
			want := []domain.ContextEntry{
				{MessageID: 109, Author: "User 9", Text: "line 9"},
				{MessageID: 110, Author: "User 10", Text: "line 10", IsPrimary: true},
			}
			if !reflect.DeepEqual(q.Context, want) {
				t.Errorf("context = %+v", q.Context)
			}
			if !strings.Contains(h.onlySent(chatA).text, "<blockquote><b>User 9:</b> line 9\n<b>User 10:</b> <i>«line 10»</i></blockquote>") {
				t.Errorf("post = %q", h.onlySent(chatA).text)
			}
		})
	}
}

// tests/test_scheduler.py::test_process_group_skips_boring_notice_when_disabled (SCH-22)
// tests/test_scheduler.py::test_send_boring_notice_escapes_ai_reason_html (SCH-24)
func TestBoringDay(t *testing.T) {
	boring := func(reason string) domain.EvalResult {
		r := parsedResult(10, domain.ReasonBoringDay)
		r.DayVerdict.ReasonText = reason
		return r
	}

	t.Run("notices on: the notice goes out and the quote row is settled", func(t *testing.T) {
		h := newHarness(t)
		h.addGroup(chatA)
		h.fill(chatA, 10)
		h.sender.nextID = 100
		h.eval.set(boring("LLM says <boring> & unstable"))

		h.run()

		q := h.store.quote(t, chatA, day27)
		if q.DecisionStatus != domain.StatusSkippedBoring || q.NoticeMessageID != 101 || q.OperationError != "" ||
			q.DecisionReason != "LLM says <boring> & unstable" || q.BotMessageID != 0 {
			t.Errorf("quote = %+v", q)
		}
		if !slices.Contains(h.store.callLog(), "MarkNotified(1,101)") {
			t.Errorf("calls = %v", h.store.callLog())
		}
		text := h.onlySent(chatA).text
		if !strings.Contains(text, "<blockquote><i>LLM says &lt;boring&gt; &amp; unstable</i></blockquote>") {
			t.Errorf("notice = %q", text)
		}
		if len(h.sender.pins) != 0 {
			t.Error("a notice is not pinned")
		}
		if h.store.clearedCount(chatA) != 1 {
			t.Errorf("window cleared %d times", h.store.clearedCount(chatA))
		}
		// The row keeps the best message and its score, as for a quote.
		if q.Text != "line 10" || q.Score != 1 {
			t.Errorf("row keeps %q / %v", q.Text, q.Score)
		}
	})

	t.Run("notices off: recorded and cleared, nothing sent", func(t *testing.T) {
		h := newHarness(t)
		h.addGroup(chatA, func(g *domain.GroupSettings) { g.BoringNotice = ptr(false) })
		h.fill(chatA, 10)
		h.eval.set(boring("flat"))

		h.run()

		q := h.store.quote(t, chatA, day27)
		if q.DecisionStatus != domain.StatusSkippedBoring || q.OperationError != "" || q.NoticeMessageID != 0 {
			t.Errorf("quote = %+v", q)
		}
		if len(h.sender.sentTo(chatA)) != 0 || h.store.clearedCount(chatA) != 1 {
			t.Errorf("sent %d, cleared %d", len(h.sender.sentTo(chatA)), h.store.clearedCount(chatA))
		}
		lookups := h.store.count("ForDay")
		h.clock.Advance(time.Minute)
		h.run()
		if h.store.count("ForDay") != lookups {
			t.Error("a finished day was looked at again")
		}
	})

	t.Run("the notice cannot be sent: failed, window kept, retried later", func(t *testing.T) {
		h := newHarness(t)
		h.addGroup(chatA)
		h.fill(chatA, 10)
		h.eval.set(boring("flat"))
		h.sender.setErrors(errors.New("Forbidden: bot was kicked"), nil, nil, nil)

		h.run()

		q := h.store.quote(t, chatA, day27)
		if q.DecisionStatus != domain.StatusBoringNoticeFailed || q.OperationError != "Forbidden: bot was kicked" {
			t.Errorf("quote = %s / %q", q.DecisionStatus, q.OperationError)
		}
		if h.store.clearedCount(chatA) != 0 {
			t.Error("the window was cleared without a notice")
		}
		// Fifteen minutes on, the notice goes out and nothing else is asked of the AI.
		h.sender.setErrors(nil, nil, nil, nil)
		h.clock.Advance(15 * time.Minute)
		h.run()
		if h.status(chatA) != domain.StatusSkippedBoring || h.eval.callCount() != 1 || len(h.sender.sentTo(chatA)) != 1 {
			t.Errorf("status %s, AI calls %d, sent %d", h.status(chatA), h.eval.callCount(), len(h.sender.sentTo(chatA)))
		}
	})

	t.Run("the outcome of the notice is unknown", func(t *testing.T) {
		h := newHarness(t)
		h.addGroup(chatA)
		h.fill(chatA, 10)
		h.eval.set(boring("flat"))
		h.sender.setErrors(errUnknownOutcome, nil, nil, nil)

		h.run()

		if q := h.store.quote(t, chatA, day27); q.DecisionStatus != domain.StatusBoringNoticeUnknown {
			t.Errorf("status = %s", q.DecisionStatus)
		}
		// Never retried, never cleared.
		h.sender.setErrors(nil, nil, nil, nil)
		h.clock.Advance(16 * time.Minute)
		h.run()
		if len(h.sender.sentTo(chatA)) != 0 || h.store.clearedCount(chatA) != 0 {
			t.Errorf("sent %d, cleared %d", len(h.sender.sentTo(chatA)), h.store.clearedCount(chatA))
		}
	})

	t.Run("the notice was sent but the database refused", func(t *testing.T) {
		h := newHarness(t)
		h.addGroup(chatA)
		h.fill(chatA, 10)
		h.eval.set(boring("flat"))
		h.store.fail("MarkNotified", errors.New("connection reset"))

		h.run()

		q := h.store.quote(t, chatA, day27)
		if q.DecisionStatus != domain.StatusBoringNoticeUnknown || q.OperationError != "Telegram notice was sent, but DB finalization failed: connection reset" {
			t.Errorf("quote = %s / %q", q.DecisionStatus, q.OperationError)
		}
	})
}

// tests/test_scheduler.py::test_publish_quote_message_escapes_quote_and_author_html (SCH-25)
func TestPublishRecordsTheSentMessage(t *testing.T) {
	h := newHarness(t)
	h.addGroup(chatA)
	h.fill(chatA, 10)
	h.setMessage(chatA, 10, func(m *domain.WindowMessage) { m.Text = "1 < 2 & 3" })
	h.store.names[1010] = "Alice & Bob"
	h.sender.nextID = 201

	h.run()

	post := h.onlySent(chatA).text
	if !strings.Contains(post, "<i>«1 &lt; 2 &amp; 3»</i>\n— <b>Alice &amp; Bob</b>") {
		t.Errorf("post = %q", post)
	}
	if !slices.Contains(h.store.callLog(), `MarkPublished(1,202,"because")`) {
		t.Errorf("calls = %v", h.store.callLog())
	}
}

// tests/test_scheduler.py::test_publish_quote_message_copies_media_with_day_title (SCH-26)
func TestPublishCopiesAMediaMessage(t *testing.T) {
	h := newHarness(t)
	h.addGroup(chatA)
	h.fill(chatA, 10)
	h.setMessage(chatA, 10, func(m *domain.WindowMessage) {
		m.ContentType, m.MessageID, m.Text = domain.ContentPhoto, 57, "photo: Файл: photo"
		m.Media = &domain.MediaSnapshot{Description: "кот на клавиатуре"}
	})

	h.run()

	post := h.onlySent(chatA)
	if post.copyOf != 57 {
		t.Fatalf("not a copy of message 57: %+v", post)
	}
	if !strings.HasPrefix(post.text, "🏆 <b>Photo of the Day</b>\n\n<blockquote><i>«photo: кот на клавиатуре»</i>\n— <b>User 10</b></blockquote>") {
		t.Errorf("caption = %q", post.text)
	}
	if !strings.Contains(post.text, "<a href='https://t.me/c/1234567890/57'>Original</a>") {
		t.Errorf("caption link = %q", post.text)
	}
	q := h.store.quote(t, chatA, day27)
	if q.Text != "photo: кот на клавиатуре" || q.ContentType != "photo" || q.DecisionStatus != domain.StatusPublished || q.OperationError != "" {
		t.Errorf("quote = %+v", q)
	}
	if !reflect.DeepEqual(h.sender.pins, []int64{101}) {
		t.Errorf("pins = %v", h.sender.pins)
	}
}

// tests/test_scheduler.py::test_publish_quote_message_falls_back_to_text_when_media_copy_fails (SCH-27)
func TestPublishMediaCopyFallsBackToText(t *testing.T) {
	h := newHarness(t)
	h.addGroup(chatA)
	h.fill(chatA, 10)
	h.setMessage(chatA, 10, func(m *domain.WindowMessage) {
		m.ContentType, m.MessageID, m.Text = domain.ContentVoice, 58, "voice: привет"
	})
	h.sender.setErrors(nil, errors.New("copy failed"), nil, nil)

	h.run()

	post := h.onlySent(chatA)
	if post.copyOf != 0 || !strings.HasPrefix(post.text, "🏆 <b>Voice of the Day</b>") {
		t.Errorf("fallback = %+v", post)
	}
	q := h.store.quote(t, chatA, day27)
	if q.DecisionStatus != domain.StatusPublished || q.OperationError != "Media copy failed, text fallback used: copy failed" {
		t.Errorf("quote = %s / %q", q.DecisionStatus, q.OperationError)
	}
}

// tests/test_scheduler.py::test_publish_quote_message_does_not_fallback_on_unknown_media_delivery (SCH-28)
func TestPublishMediaCopyOutcomeUnknownDoesNotFallBack(t *testing.T) {
	h := newHarness(t)
	h.addGroup(chatA)
	h.fill(chatA, 10)
	h.setMessage(chatA, 10, func(m *domain.WindowMessage) { m.ContentType, m.MessageID = domain.ContentVideo, 59 })
	h.sender.setErrors(nil, fmt.Errorf("copyMessage: %w", errUnknownOutcome), nil, nil)

	h.run()

	if len(h.sender.sentTo(chatA)) != 0 {
		t.Fatalf("a text fallback after an unknown outcome would double the post: %+v", h.sender.sentTo(chatA))
	}
	q := h.store.quote(t, chatA, day27)
	wantErr := "Telegram media delivery outcome unknown: copyMessage: " + errUnknownOutcome.Error()
	if q.DecisionStatus != domain.StatusPublishUnknown || q.OperationError != wantErr {
		t.Errorf("quote = %s / %q, want %q", q.DecisionStatus, q.OperationError, wantErr)
	}
	if h.store.clearedCount(chatA) != 0 || len(h.sender.pins) != 0 {
		t.Errorf("cleared %d, pins %v", h.store.clearedCount(chatA), h.sender.pins)
	}
	// Terminal: the next tick finishes the day without a second post.
	h.clock.Advance(time.Minute)
	h.run()
	if h.eval.callCount() != 1 || len(h.sender.sentTo(chatA)) != 0 {
		t.Errorf("AI %d, sent %d", h.eval.callCount(), len(h.sender.sentTo(chatA)))
	}
}

// tests/test_scheduler.py::test_publish_quote_message_can_skip_pin (SCH-30)
func TestPinning(t *testing.T) {
	tests := []struct {
		name     string
		pin      *bool
		pinErr   error
		wantPins []int64
		wantErr  string
	}{
		{"on", ptr(true), nil, []int64{101}, ""},
		{"unset means on", nil, nil, []int64{101}, ""},
		{"off", ptr(false), nil, nil, ""},
		{"a pin that fails does not fail the quote", ptr(true), errors.New("not enough rights"), nil, "Pin failed: not enough rights"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t)
			h.addGroup(chatA, func(g *domain.GroupSettings) { g.Pin = tt.pin })
			h.fill(chatA, 10)
			h.sender.setErrors(nil, nil, tt.pinErr, nil)

			h.run()

			q := h.store.quote(t, chatA, day27)
			if !reflect.DeepEqual(h.sender.pins, tt.wantPins) || q.OperationError != tt.wantErr || q.DecisionStatus != domain.StatusPublished {
				t.Errorf("pins %v, error %q, status %s", h.sender.pins, q.OperationError, q.DecisionStatus)
			}
			if h.store.clearedCount(chatA) != 1 {
				t.Error("the window must still be cleared after a pin failure")
			}
		})
	}
}

// doc inventory 03 §1.4, the outcome table: sending fails.
func TestPublishSendFailures(t *testing.T) {
	t.Run("a failure that did not deliver: publish_failed, retried later", func(t *testing.T) {
		h := newHarness(t)
		h.addGroup(chatA)
		h.fill(chatA, 10)
		long := strings.Repeat("e", 400)
		h.sender.setErrors(errors.New(long), nil, nil, nil)

		h.run()

		q := h.store.quote(t, chatA, day27)
		if q.DecisionStatus != domain.StatusPublishFailed || q.OperationError != long[:250] {
			t.Fatalf("quote = %s / %d chars", q.DecisionStatus, len(q.OperationError))
		}
		if h.store.clearedCount(chatA) != 0 || len(h.sender.pins) != 0 {
			t.Error("a failed post must keep the window")
		}
		// Not again for fifteen minutes; then the row goes and the day is
		// computed afresh, with one more AI call.
		h.sender.setErrors(nil, nil, nil, nil)
		h.clock.Advance(14 * time.Minute)
		h.run()
		if h.eval.callCount() != 1 {
			t.Fatal("retried too early")
		}
		h.clock.Advance(time.Minute)
		h.run()
		if h.eval.callCount() != 2 || h.status(chatA) != domain.StatusPublished {
			t.Errorf("AI calls %d, status %s", h.eval.callCount(), h.status(chatA))
		}
	})

	t.Run("a failure that may have delivered: publish_unknown, never retried", func(t *testing.T) {
		h := newHarness(t)
		h.addGroup(chatA)
		h.fill(chatA, 10)
		h.sender.setErrors(errUnknownOutcome, nil, nil, nil)

		h.run()

		if h.status(chatA) != domain.StatusPublishUnknown {
			t.Fatalf("status = %s", h.status(chatA))
		}
		h.sender.setErrors(nil, nil, nil, nil)
		h.clock.Advance(20 * time.Minute)
		h.run()
		if h.eval.callCount() != 1 || len(h.sender.sentTo(chatA)) != 0 {
			t.Errorf("AI %d, sent %d", h.eval.callCount(), len(h.sender.sentTo(chatA)))
		}
	})

	t.Run("sent, but the database write fails", func(t *testing.T) {
		h := newHarness(t)
		h.addGroup(chatA)
		h.fill(chatA, 10)
		h.store.fail("MarkPublished", errors.New("deadlock detected"))

		h.run()

		q := h.store.quote(t, chatA, day27)
		if q.DecisionStatus != domain.StatusPublishUnknown || q.OperationError != "Telegram message was sent, but DB finalization failed: deadlock detected" {
			t.Errorf("quote = %s / %q", q.DecisionStatus, q.OperationError)
		}
		if h.store.clearedCount(chatA) != 0 || len(h.sender.pins) != 0 {
			t.Error("nothing is pinned or cleared for an unrecorded post")
		}
	})

	t.Run("sent, but the status write also fails: the run fails and the developers hear", func(t *testing.T) {
		h := newHarness(t)
		h.addGroup(chatA)
		h.fill(chatA, 10)
		h.store.fail("MarkPublished", errors.New("connection lost"))
		h.store.fail("SetStatus", errors.New("connection lost"))

		h.run()

		alerts := h.sender.alertList()
		if len(alerts) != 1 || !strings.Contains(alerts[0].text, "set quote 1 to publish_unknown") {
			t.Fatalf("alerts = %+v", alerts)
		}
		// The row stays publishing; stale recovery writes it off in time.
		if h.status(chatA) != domain.StatusPublishing {
			t.Errorf("status = %s", h.status(chatA))
		}
	})

	t.Run("cleaning up fails: noted, still published", func(t *testing.T) {
		h := newHarness(t)
		h.addGroup(chatA)
		h.fill(chatA, 10)
		h.store.fail("DeleteWindow", errors.New("lock timeout"))

		h.run()

		q := h.store.quote(t, chatA, day27)
		if q.DecisionStatus != domain.StatusPublished || q.OperationError != "Cleanup failed: lock timeout" {
			t.Errorf("quote = %s / %q", q.DecisionStatus, q.OperationError)
		}
		// Finished: not tried again, so the quote cannot be posted twice.
		h.clock.Advance(time.Minute)
		h.run()
		if len(h.sender.sentTo(chatA)) != 1 {
			t.Errorf("%d posts", len(h.sender.sentTo(chatA)))
		}
	})

	t.Run("operation errors are joined with a separator", func(t *testing.T) {
		h := newHarness(t)
		h.addGroup(chatA)
		h.fill(chatA, 10)
		h.setMessage(chatA, 10, func(m *domain.WindowMessage) { m.ContentType, m.MessageID = domain.ContentPhoto, 57 })
		h.sender.setErrors(nil, errors.New("copy failed"), errors.New("no rights"), nil)
		h.store.fail("DeleteWindow", errors.New("lock timeout"))

		h.run()

		want := "Media copy failed, text fallback used: copy failed | Pin failed: no rights | Cleanup failed: lock timeout"
		if got := h.store.quote(t, chatA, day27).OperationError; got != want {
			t.Errorf("operation error = %q, want %q", got, want)
		}
	})
}

// AGENTS.md: at most one quote per group per day. doc inventory 03 §1.9: the row
// is created after the AI call, so two runs may both pay and only one creates.
func TestDayTakenByAnotherWorkerIsLeftToIt(t *testing.T) {
	h := newHarness(t)
	h.addGroup(chatA)
	h.fill(chatA, 10)
	at := h.clock.Now().Add(-time.Minute)
	h.store.addQuote(domain.Quote{GroupID: chatA, QuoteDay: day27, DecisionStatus: domain.StatusPublished, BotMessageID: 5,
		StatusChangedAt: at, CreatedAt: at, WindowStart: defaultStart, WindowEnd: defaultEnd})
	h.store.blindForDay = true // this run did not see the row

	h.run()

	if h.eval.callCount() != 1 {
		t.Fatalf("%d AI calls", h.eval.callCount())
	}
	if len(h.sender.sentTo(chatA)) != 0 || len(h.sender.pins) != 0 || h.store.clearedCount(chatA) != 0 {
		t.Errorf("the loser posted: sent %d, pins %d, cleared %d", len(h.sender.sentTo(chatA)), len(h.sender.pins), h.store.clearedCount(chatA))
	}
	if q := h.store.quote(t, chatA, day27); q.BotMessageID != 5 || q.DecisionStatus != domain.StatusPublished {
		t.Errorf("the other worker's row was touched: %+v", q)
	}
	if len(h.sender.alertList()) != 0 {
		t.Errorf("losing the race is not an alert: %v", h.sender.alertList())
	}

	// The loser waits out the gate and then reads the row.
	h.clock.Advance(time.Minute)
	h.run()
	if h.eval.callCount() != 1 {
		t.Error("tried again inside 15 minutes")
	}
	h.store.mu.Lock()
	h.store.blindForDay = false
	h.store.mu.Unlock()
	h.clock.Advance(15 * time.Minute)
	h.run()
	if h.eval.callCount() != 1 || len(h.sender.sentTo(chatA)) != 0 {
		t.Errorf("after reading the row: AI %d, sent %d", h.eval.callCount(), len(h.sender.sentTo(chatA)))
	}
}

// doc §5 / AGENTS.md: a day with no usable verdict gets neither a quote nor a
// notice, only a failed row that is retried.
func TestVerdictErrorNeverPublishes(t *testing.T) {
	h := newHarness(t)
	h.addGroup(chatA)
	h.fill(chatA, 10)
	var scores []domain.Score
	for i := int64(1); i <= 10; i++ {
		scores = append(scores, domain.Score{ID: i, Value: 0.5})
	}
	h.eval.set(domain.EvalResult{Scores: scores, Status: domain.RunAIFailed, DayVerdictError: "AI request failed", ActualModel: ""})

	h.run()

	q := h.store.quote(t, chatA, day27)
	if q.DecisionStatus != domain.StatusPublishFailed || q.OperationError != "AI request failed" || q.Text != "line 1" {
		t.Errorf("quote = %s / %q / %q", q.DecisionStatus, q.OperationError, q.Text)
	}
	if len(h.sender.sentTo(chatA)) != 0 || h.store.clearedCount(chatA) != 0 || len(h.sender.pins) != 0 {
		t.Errorf("sent %d, cleared %d", len(h.sender.sentTo(chatA)), h.store.clearedCount(chatA))
	}
	// The failure is still reported for the day.
	if reps := h.reports.list(); len(reps) != 1 || reps[0].Result.Status != domain.RunAIFailed {
		t.Errorf("reports = %+v", reps)
	}
	if len(h.sender.alertList()) != 0 {
		t.Errorf("an AI failure is recorded on the row, not alerted: %v", h.sender.alertList())
	}

	// Fifteen minutes later the AI is back: the failed row is dropped and the day
	// is judged again.
	h.eval.set(parsedResult(10, domain.ReasonWorthy))
	h.clock.Advance(15 * time.Minute)
	h.run()
	if h.eval.callCount() != 2 || h.status(chatA) != domain.StatusPublished {
		t.Errorf("AI calls %d, status %s", h.eval.callCount(), h.status(chatA))
	}
}

// doc §7.2 F6: a day with no eligible message is finished in memory.
func TestNoEligibleMessageFinishesTheDay(t *testing.T) {
	h := newHarness(t)
	h.addGroup(chatA)
	h.fill(chatA, 10)
	for i := int64(1); i <= 10; i++ {
		h.setMessage(chatA, i, func(m *domain.WindowMessage) { m.ContentType = domain.ContentSticker })
	}
	h.eval.set(parsedResult(10, domain.ReasonWorthy))

	h.run()
	for range 5 {
		h.clock.Advance(time.Minute)
		h.run()
	}

	if h.eval.callCount() != 1 {
		t.Fatalf("%d AI calls for a day nothing could be picked from, want 1", h.eval.callCount())
	}
	if h.store.hasQuote(chatA, day27) || len(h.sender.sentTo(chatA)) != 0 || len(h.sender.alertList()) != 0 {
		t.Error("nothing is recorded, sent or alerted")
	}
	if len(h.reports.list()) != 1 {
		t.Errorf("%d reports", len(h.reports.list()))
	}
	// The messages stay: the retention job takes them in time.
	if h.store.messageCount(chatA) != 10 {
		t.Errorf("%d messages left", h.store.messageCount(chatA))
	}
}

// tests/test_scoring.py::test_pick_best_quote_does_not_allow_sticker_as_primary (SCO-08), end to end.
func TestStickerIsNotQuotedButCanBeContext(t *testing.T) {
	h := newHarness(t)
	h.addGroup(chatA)
	h.fill(chatA, 10)
	h.setMessage(chatA, 10, func(m *domain.WindowMessage) {
		m.ContentType, m.Text = domain.ContentSticker, "sticker: смешной"
	})
	r := parsedResult(10, domain.ReasonWorthy)
	r.QuoteChoice = &domain.QuoteChoice{PrimaryID: 10, ContextIDs: []int64{9, 10}, ContextNeeded: true}
	h.eval.set(r)

	h.run()

	q := h.store.quote(t, chatA, day27)
	if q.MessageID != 109 || q.Text != "line 9" {
		t.Fatalf("quoted %d %q, want message 9", q.MessageID, q.Text)
	}
	if len(q.Context) != 2 || q.Context[1].Text != "sticker: смешной" || q.Context[1].IsPrimary || !q.Context[0].IsPrimary {
		t.Errorf("context = %+v", q.Context)
	}
}

// doc §7.2 F12: a person without a name is "Unknown" to the model and
// "Anonymous" in the group's language everywhere that is shown.
func TestAnonymousAuthor(t *testing.T) {
	for _, tt := range []struct {
		lang, anonymous string
	}{{"en", "Anonymous"}, {"ru", "Аноним"}, {"uk", "Анонім"}} {
		t.Run(tt.lang, func(t *testing.T) {
			h := newHarness(t)
			h.addGroup(chatA, func(g *domain.GroupSettings) { g.Language = tt.lang })
			h.fill(chatA, 10)
			delete(h.store.names, 1010)
			delete(h.store.names, 1009)
			r := parsedResult(10, domain.ReasonWorthy)
			r.QuoteChoice = &domain.QuoteChoice{PrimaryID: 10, ContextIDs: []int64{9, 10}, ContextNeeded: true}
			h.eval.set(r)

			h.run()

			if got := h.eval.call(0).msgs[9].Author; got != "Unknown" {
				t.Errorf("the model sees %q, want Unknown", got)
			}
			q := h.store.quote(t, chatA, day27)
			if q.Context[0].Author != tt.anonymous || q.Context[1].Author != tt.anonymous {
				t.Errorf("snapshot authors = %q, %q", q.Context[0].Author, q.Context[1].Author)
			}
			if rep := h.reports.list()[0]; len(rep.AuthorNames) != 8 {
				t.Errorf("report names = %v", rep.AuthorNames)
			}

			// And in a plain post, with the context off.
			h2 := newHarness(t)
			h2.addGroup(chatA, func(g *domain.GroupSettings) { g.Language = tt.lang; g.QuoteContext = ptr(false) })
			h2.fill(chatA, 10)
			delete(h2.store.names, 1010)
			h2.eval.set(parsedResult(10, domain.ReasonWorthy))
			h2.run()
			if post := h2.onlySent(chatA).text; !strings.Contains(post, "— <b>"+tt.anonymous+"</b>") {
				t.Errorf("post = %q", post)
			}
		})
	}
}

// doc inventory 03 §1.4 step 10: the note under the post when the day was cut.
func TestMessageCap(t *testing.T) {
	t.Run("a day over the cap: the newest 1500, with a note", func(t *testing.T) {
		h := newHarness(t)
		h.addGroup(chatA)
		h.fill(chatA, 1503)
		h.eval.set(parsedResult(1503, domain.ReasonWorthy))
		h.run()

		msgs := h.eval.call(0).msgs
		if len(msgs) != 1500 || msgs[0].ID != 4 || msgs[1499].ID != 1503 {
			t.Fatalf("%d messages from %d to %d", len(msgs), msgs[0].ID, msgs[len(msgs)-1].ID)
		}
		want := "<i>ℹ️ Counted the last 1500 of 1503 messages — context trimmed due to volume.</i>"
		if post := h.onlySent(chatA).text; !strings.Contains(post, want) {
			t.Errorf("post lacks the note: %q", post)
		}
		rep := h.reports.list()[0]
		if rep.SourceCount != 1503 || len(rep.Scored) != 1500 {
			t.Errorf("report source %d scored %d", rep.SourceCount, len(rep.Scored))
		}
	})

	t.Run("a premium chat has no cap", func(t *testing.T) {
		h := newHarness(t)
		h.sched.cfg.PremiumChats = map[int64]bool{chatA: true}
		h.addGroup(chatA)
		h.fill(chatA, 1503)
		h.eval.set(parsedResult(1503, domain.ReasonWorthy))
		h.run()
		if n := len(h.eval.call(0).msgs); n != 1503 {
			t.Errorf("%d messages", n)
		}
		if post := h.onlySent(chatA).text; strings.Contains(post, "context trimmed") {
			t.Errorf("a note for an uncapped day: %q", post)
		}
	})

	t.Run("a premium group row has no cap either", func(t *testing.T) {
		h := newHarness(t)
		h.addGroup(chatA, func(g *domain.GroupSettings) { g.IsPremium = ptr(true) })
		h.fill(chatA, 1501)
		h.eval.set(parsedResult(1501, domain.ReasonWorthy))
		h.run()
		if n := len(h.eval.call(0).msgs); n != 1501 {
			t.Errorf("%d messages", n)
		}
	})

	t.Run("the note reads in the group's language", func(t *testing.T) {
		h := newHarness(t)
		h.addGroup(chatA, func(g *domain.GroupSettings) { g.Language = "ru" })
		h.fill(chatA, 1501)
		h.eval.set(parsedResult(1501, domain.ReasonWorthy))
		h.run()
		want := "<i>ℹ️ Учтены последние 1500 из 1501 сообщений — контекст урезан из-за объёма.</i>"
		if post := h.onlySent(chatA).text; !strings.Contains(post, want) {
			t.Errorf("post = %q", post)
		}
	})
}

// A cancelled or wrongly shaped failure of the store inside a run is the run's
// failure, with the step named.
func TestStoreFailuresAreReportedWithContext(t *testing.T) {
	for _, tt := range []struct {
		method string
		want   string
	}{
		{"Stale", "recover stale quotes: boom"},
		{"ForDay", "load the day's quote: boom"},
		{"CountWindow", "count window messages: boom"},
		{"ListWindow", "list window messages: boom"},
		{"DisplayNames", "resolve author names: boom"},
		{"Create", "create quote: boom"},
	} {
		t.Run(tt.method, func(t *testing.T) {
			h := newHarness(t)
			h.addGroup(chatA)
			h.fill(chatA, 10)
			h.store.fail(tt.method, errors.New("boom"))

			h.run()

			alerts := h.sender.alertList()
			if len(alerts) != 1 || !strings.HasSuffix(alerts[0].text, ": "+tt.want) {
				t.Fatalf("alerts = %+v, want one ending %q", alerts, tt.want)
			}
			if len(h.sender.sentTo(chatA)) != 0 {
				t.Error("something was sent for a failed run")
			}
		})
	}
}

// tests/test_scheduler.py (CatchUp): doc §7.2 F7.
func TestCatchUp(t *testing.T) {
	ctx := context.Background()

	t.Run("after accepting the agreement the day is run", func(t *testing.T) {
		h := newHarness(t)
		g := h.addGroup(chatA, func(g *domain.GroupSettings) { g.AgreementAcceptedAt, g.AgreementVersion = nil, nil })
		h.fill(chatA, 10)
		h.run() // the tick finishes the day with a reminder
		if len(h.sender.reminders) != 1 || h.eval.callCount() != 0 {
			t.Fatalf("reminders %d, AI %d", len(h.sender.reminders), h.eval.callCount())
		}

		at := h.clock.Now()
		h.store.mu.Lock()
		g.AgreementAcceptedAt, g.AgreementVersion = &at, ptr(domain.DocumentVersion)
		h.store.mu.Unlock()
		h.sched.CatchUp(ctx, chatA)

		if h.eval.callCount() != 1 || h.status(chatA) != domain.StatusPublished || len(h.sender.sentTo(chatA)) != 1 {
			t.Errorf("AI %d, status %s, sent %d", h.eval.callCount(), h.status(chatA), len(h.sender.sentTo(chatA)))
		}
		// The window is the closed one of today.
		h.store.mu.Lock()
		w := h.store.windows[len(h.store.windows)-1]
		h.store.mu.Unlock()
		if w.QuoteDay != day27 || !w.End.Equal(defaultEnd) {
			t.Errorf("window = %+v", w)
		}
		// And the minute tick leaves the finished day alone.
		h.clock.Advance(time.Minute)
		h.run()
		if h.eval.callCount() != 1 {
			t.Error("the tick ran the day again")
		}
	})

	t.Run("before today's cutoff there is nothing to catch up", func(t *testing.T) {
		h := newHarness(t)
		h.addGroup(chatA)
		h.fill(chatA, 10)
		h.clock.Set(time.Date(2026, 3, 27, 10, 0, 0, 0, time.UTC)) // 12:00 in Kyiv

		h.sched.CatchUp(ctx, chatA)

		if h.eval.callCount() != 0 || h.store.count("CountWindow") != 0 || len(h.sender.sentTo(chatA)) != 0 {
			t.Errorf("AI %d, counts %d, sent %d", h.eval.callCount(), h.store.count("CountWindow"), len(h.sender.sentTo(chatA)))
		}
	})

	t.Run("a day that already has its quote is not run again", func(t *testing.T) {
		h := newHarness(t)
		h.addGroup(chatA)
		h.fill(chatA, 10)
		h.run()
		h.sched.CatchUp(ctx, chatA)
		if h.eval.callCount() != 1 || len(h.sender.sentTo(chatA)) != 1 {
			t.Errorf("AI %d, sent %d", h.eval.callCount(), len(h.sender.sentTo(chatA)))
		}
	})

	t.Run("an unknown or paused group is left alone", func(t *testing.T) {
		h := newHarness(t)
		h.addGroup(chatA, func(g *domain.GroupSettings) { g.IsActive = false })
		h.fill(chatA, 10)
		h.sched.CatchUp(ctx, chatA)
		h.sched.CatchUp(ctx, chatA-5)
		if h.eval.callCount() != 0 || h.store.count("CountWindow") != 0 {
			t.Error("a group that cannot be run was run")
		}
	})

	t.Run("a failure is alerted under its own key", func(t *testing.T) {
		h := newHarness(t)
		h.addGroup(chatA)
		h.fill(chatA, 10)
		h.store.fail("CountWindow", errors.New("boom"))
		h.sched.CatchUp(ctx, chatA)
		alerts := h.sender.alertList()
		want := alertCall{
			key:  fmt.Sprintf("catch-up:%d", chatA),
			text: fmt.Sprintf("Catch-up after agreement failed for chat %d: count window messages: boom", chatA),
		}
		if len(alerts) != 1 || alerts[0] != want {
			t.Errorf("alerts = %+v, want %+v", alerts, want)
		}
	})

	t.Run("a failure to load the group is alerted", func(t *testing.T) {
		h := newHarness(t)
		h.addGroup(chatA)
		h.store.fail("Get", errors.New("down"))
		h.sched.CatchUp(ctx, chatA)
		alerts := h.sender.alertList()
		if len(alerts) != 1 || alerts[0].key != fmt.Sprintf("catch-up:%d", chatA) {
			t.Errorf("alerts = %+v", alerts)
		}
	})
}

// doc §7.4 R1.
func TestRetentionDeletesMessagesOlderThanSeventyTwoHours(t *testing.T) {
	h := newHarness(t)
	h.sched.retain(context.Background())
	want := t1903.Add(-72 * time.Hour)
	h.store.mu.Lock()
	got := slices.Clone(h.store.retention)
	h.store.mu.Unlock()
	if len(got) != 1 || !got[0].Equal(want) {
		t.Errorf("deleted before %v, want %v", got, want)
	}

	// An error is logged, not raised.
	h.store.fail("DeleteOlderThan", errors.New("boom"))
	h.sched.retain(context.Background())
}

func TestLastTick(t *testing.T) {
	h := newHarness(t)
	if !h.sched.LastTick().IsZero() {
		t.Fatal("LastTick before the first tick")
	}
	h.run()
	if got := h.sched.LastTick(); !got.Equal(t1903) {
		t.Errorf("LastTick = %v, want %v", got, t1903)
	}
	h.clock.Advance(time.Minute)
	h.run()
	if got := h.sched.LastTick(); !got.Equal(t1903.Add(time.Minute)) {
		t.Errorf("LastTick = %v", got)
	}
	// A tick that could not read the groups did not complete.
	h.store.fail("ListActive", errors.New("db down"))
	h.clock.Advance(time.Minute)
	h.run()
	if got := h.sched.LastTick(); !got.Equal(t1903.Add(time.Minute)) {
		t.Errorf("LastTick moved to %v on a failed tick", got)
	}
}
