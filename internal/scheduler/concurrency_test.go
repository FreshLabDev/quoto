// SPDX-License-Identifier: Apache-2.0

package scheduler

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/FreshLabDev/quoto/internal/domain"
)

// These tests run inside a synctest bubble: goroutines block on channels the
// bubble owns, so "the run is in the AI call" is a state that synctest.Wait
// reaches deterministically, and the publish timeout runs on the bubble's clock.
// The scheduler's own clock stays the harness's injected one.

// holdEvaluator makes every Evaluate wait for release, then answer with the
// harness's canned result.
func holdEvaluator(h *harness, release <-chan struct{}) {
	h.eval.hook = func(context.Context) (domain.EvalResult, bool) {
		<-release
		return domain.EvalResult{}, false
	}
}

func finished(done <-chan struct{}) bool {
	select {
	case <-done:
		return true
	default:
		return false
	}
}

// doc §7.2 F7. There is no Python test: the accept-agreement handler there raced
// the minute job.
func TestCatchUpWaitsForARunInProgressAndAsksTheAIOnce(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t)
		h.addGroup(chatA)
		h.fill(chatA, 10)
		release := make(chan struct{})
		holdEvaluator(h, release)
		ctx := context.Background()

		h.sched.tick(ctx)
		synctest.Wait()
		if h.eval.callCount() != 1 {
			t.Fatalf("%d AI calls while the tick's run waits", h.eval.callCount())
		}

		caught := make(chan struct{})
		go func() {
			defer close(caught)
			h.sched.CatchUp(ctx, chatA)
		}()
		synctest.Wait()
		if finished(caught) || h.store.count("Get") != 0 || h.eval.callCount() != 1 {
			t.Fatalf("the catch-up did not wait: finished %t, group reads %d, AI calls %d",
				finished(caught), h.store.count("Get"), h.eval.callCount())
		}

		close(release)
		<-caught
		h.sched.wg.Wait()

		if h.eval.callCount() != 1 {
			t.Errorf("%d AI calls for one day, want 1", h.eval.callCount())
		}
		if len(h.sender.sentTo(chatA)) != 1 || h.status(chatA) != domain.StatusPublished {
			t.Errorf("sent %d, status %s", len(h.sender.sentTo(chatA)), h.status(chatA))
		}
		if h.store.count("Get") != 1 {
			t.Errorf("group read %d times by the catch-up", h.store.count("Get"))
		}
	})
}

// A catch-up whose caller gave up does not wait forever behind a long run.
func TestCatchUpStopsWaitingWhenItsContextIsDone(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t)
		h.addGroup(chatA)
		h.fill(chatA, 10)
		release := make(chan struct{})
		holdEvaluator(h, release)

		h.sched.tick(context.Background())
		synctest.Wait()

		ctx, cancel := context.WithCancel(context.Background())
		caught := make(chan struct{})
		go func() {
			defer close(caught)
			h.sched.CatchUp(ctx, chatA)
		}()
		synctest.Wait()
		if finished(caught) {
			t.Fatal("the catch-up did not wait for the run")
		}
		cancel()
		<-caught

		if h.store.count("Get") != 0 || h.eval.callCount() != 1 {
			t.Errorf("group reads %d, AI calls %d", h.store.count("Get"), h.eval.callCount())
		}
		close(release)
		h.sched.wg.Wait()
		if h.status(chatA) != domain.StatusPublished {
			t.Errorf("the run was disturbed: %s", h.status(chatA))
		}
	})
}

// doc §7.2 F7: a group already running is skipped by the next tick.
func TestTickSkipsAGroupWhoseRunIsStillGoing(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t)
		h.addGroup(chatA)
		h.fill(chatA, 10)
		release := make(chan struct{})
		holdEvaluator(h, release)
		ctx := context.Background()

		h.sched.tick(ctx)
		synctest.Wait()
		first := h.sched.LastTick()

		// The tick came back without waiting for the run, and LastTick says so.
		h.clock.Advance(time.Minute)
		h.sched.tick(ctx)
		synctest.Wait()

		if h.eval.callCount() != 1 {
			t.Fatalf("%d AI calls, want 1: the second tick started a second run", h.eval.callCount())
		}
		if !h.sched.LastTick().After(first) {
			t.Errorf("LastTick = %v did not move past %v while a run was in the AI call", h.sched.LastTick(), first)
		}
		close(release)
		h.sched.wg.Wait()
		if h.eval.callCount() != 1 || len(h.sender.sentTo(chatA)) != 1 {
			t.Errorf("AI calls %d, sent %d", h.eval.callCount(), len(h.sender.sentTo(chatA)))
		}
	})
}

// doc §7.4: eight groups run at once; the rest wait their turn.
func TestAtMostEightGroupsRunAtOnce(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t)
		const groups = 20
		for i := range int64(groups) {
			h.addGroup(chatA - i)
			h.fill(chatA-i, 10)
		}
		var inflight, peak atomic.Int32
		release := make(chan struct{})
		h.eval.hook = func(context.Context) (domain.EvalResult, bool) {
			n := inflight.Add(1)
			for {
				p := peak.Load()
				if n <= p || peak.CompareAndSwap(p, n) {
					break
				}
			}
			<-release
			inflight.Add(-1)
			return domain.EvalResult{}, false
		}

		h.sched.tick(context.Background())
		synctest.Wait()
		if got := inflight.Load(); got != concurrency {
			t.Fatalf("%d runs in the AI call, want %d", got, concurrency)
		}

		close(release)
		h.sched.wg.Wait()

		if h.eval.callCount() != groups || peak.Load() != concurrency {
			t.Errorf("AI calls %d, peak %d", h.eval.callCount(), peak.Load())
		}
		for i := range int64(groups) {
			if got := h.store.quote(t, chatA-i, day27).DecisionStatus; got != domain.StatusPublished {
				t.Errorf("group %d: %s", chatA-i, got)
			}
		}
	})
}

// Shutdown during the AI call: the answer is dropped, nothing is recorded, and
// Run returns. doc §5: a cancelled call must not become a quote.
func TestShutdownDuringTheAICallRecordsNothing(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t)
		h.addGroup(chatA)
		h.fill(chatA, 10)
		// Worse than the contract asks: an evaluator that answers with a good verdict
		// after it was cancelled. The scheduler must still drop it.
		h.eval.hook = func(ctx context.Context) (domain.EvalResult, bool) {
			<-ctx.Done()
			return parsedResult(10, domain.ReasonWorthy), true
		}
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- h.sched.Run(ctx) }()
		synctest.Wait()
		if h.eval.callCount() != 1 {
			t.Fatalf("%d AI calls before the shutdown", h.eval.callCount())
		}

		cancel()
		if err := <-done; err != nil {
			t.Fatalf("Run = %v, want nil", err)
		}

		if len(h.store.quotes) != 0 || len(h.reports.list()) != 0 || len(h.sender.sentTo(chatA)) != 0 {
			t.Errorf("quotes %d, reports %d, sent %d", len(h.store.quotes), len(h.reports.list()), len(h.sender.sentTo(chatA)))
		}
		if h.store.messageCount(chatA) != 10 || h.store.clearedCount(chatA) != 0 {
			t.Errorf("the window was touched: %d left, %d clears", h.store.messageCount(chatA), h.store.clearedCount(chatA))
		}
		if len(h.sender.alertList()) != 0 {
			t.Errorf("a shutdown is not an alert: %v", h.sender.alertList())
		}
	})
}

// Shutdown during the send: the post is not abandoned. The context the send gets
// is not cancelled, and Run waits for the row to be finalised.
func TestShutdownDuringTheSendFinishesThePublication(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t)
		h.addGroup(chatA)
		h.fill(chatA, 10)
		ctx, cancel := context.WithCancel(context.Background())
		var sendCtxErr error
		h.sender.beforeSend = func(c context.Context) {
			cancel()
			sendCtxErr = c.Err()
		}
		done := make(chan error, 1)
		go func() { done <- h.sched.Run(ctx) }()
		if err := <-done; err != nil {
			t.Fatalf("Run = %v, want nil", err)
		}

		if sendCtxErr != nil {
			t.Fatalf("the send's context was cancelled by the shutdown: %v", sendCtxErr)
		}
		q := h.store.quote(t, chatA, day27)
		if q.DecisionStatus != domain.StatusPublished || q.BotMessageID == 0 {
			t.Errorf("quote = %s, bot message %d", q.DecisionStatus, q.BotMessageID)
		}
		if len(h.sender.sentTo(chatA)) != 1 || len(h.sender.pins) != 1 || h.store.clearedCount(chatA) != 1 {
			t.Errorf("sent %d, pins %d, cleared %d", len(h.sender.sentTo(chatA)), len(h.sender.pins), h.store.clearedCount(chatA))
		}
		if len(h.reports.list()) != 1 || len(h.sender.alertList()) != 0 {
			t.Errorf("reports %d, alerts %v", len(h.reports.list()), h.sender.alertList())
		}
	})
}

// The part of a run that survives a shutdown is bounded: publishTimeout.
func TestPublicationAfterShutdownIsBoundedByTheTimeout(t *testing.T) {
	old := publishTimeout
	publishTimeout = 30 * time.Second
	t.Cleanup(func() { publishTimeout = old })

	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t)
		h.addGroup(chatA)
		h.fill(chatA, 10)
		ctx, cancel := context.WithCancel(context.Background())
		var sendCtxErr error
		h.sender.beforeSend = func(c context.Context) {
			cancel()
			<-c.Done() // Telegram never answers
			sendCtxErr = c.Err()
		}
		start := time.Now()
		done := make(chan error, 1)
		go func() { done <- h.sched.Run(ctx) }()
		if err := <-done; err != nil {
			t.Fatalf("Run = %v, want nil", err)
		}

		if !errors.Is(sendCtxErr, context.DeadlineExceeded) {
			t.Errorf("the send ended with %v, want the deadline", sendCtxErr)
		}
		if elapsed := time.Since(start); elapsed != publishTimeout {
			t.Errorf("Run returned after %v, want %v", elapsed, publishTimeout)
		}
	})
}

// doc §7.4 R1: the cleanup runs once at the start and then on its own clock, one
// job at a time, and never holds up the minute tick.
func TestRetentionRunsAtStartAndOnItsOwnClock(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t)
		ctx, cancel := context.WithCancel(context.Background())
		tick, retention := make(chan time.Time), make(chan time.Time)
		done := make(chan struct{})
		go func() {
			defer close(done)
			h.sched.loop(ctx, tick, retention)
		}()
		synctest.Wait()
		if got := h.store.count("DeleteOlderThan"); got != 1 {
			t.Fatalf("%d cleanups at start, want 1", got)
		}
		if got := h.store.count("ListActive"); got != 1 {
			t.Fatalf("%d group listings at start, want 1", got)
		}

		retention <- time.Time{}
		synctest.Wait()
		if got := h.store.count("DeleteOlderThan"); got != 2 {
			t.Errorf("%d cleanups after the hourly clock, want 2", got)
		}
		if got := h.store.count("ListActive"); got != 1 {
			t.Errorf("the hourly clock ran the tick: %d listings", got)
		}

		tick <- time.Time{}
		synctest.Wait()
		if got := h.store.count("ListActive"); got != 2 {
			t.Errorf("%d group listings after a tick, want 2", got)
		}
		if got := h.store.count("DeleteOlderThan"); got != 2 {
			t.Errorf("the minute clock ran the cleanup: %d", got)
		}

		cancel()
		<-done
	})
}

func TestRetentionIsSingleFlightAndDoesNotBlockTheTick(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t)
		var entered atomic.Int32
		unblock := make(chan struct{})
		h.store.retainHook = func(ctx context.Context) {
			entered.Add(1)
			select {
			case <-unblock:
			case <-ctx.Done():
			}
		}
		ctx, cancel := context.WithCancel(context.Background())
		tick, retention := make(chan time.Time), make(chan time.Time)
		done := make(chan struct{})
		go func() {
			defer close(done)
			h.sched.loop(ctx, tick, retention)
		}()
		synctest.Wait()

		// The first cleanup is still going, and the loop has moved on.
		if entered.Load() != 1 || !h.sched.LastTick().Equal(t1903) {
			t.Fatalf("cleanups started %d, LastTick %v", entered.Load(), h.sched.LastTick())
		}
		retention <- time.Time{}
		synctest.Wait()
		if entered.Load() != 1 {
			t.Errorf("a second cleanup started while the first was running: %d", entered.Load())
		}
		h.clock.Advance(time.Minute)
		tick <- time.Time{}
		synctest.Wait()
		if !h.sched.LastTick().Equal(t1903.Add(time.Minute)) {
			t.Errorf("the tick waited for the cleanup: LastTick %v", h.sched.LastTick())
		}

		close(unblock)
		synctest.Wait()
		retention <- time.Time{}
		synctest.Wait()
		if entered.Load() != 2 {
			t.Errorf("%d cleanups started in all, want 2", entered.Load())
		}
		cancel()
		<-done
	})
}

// Run drives the loop with real tickers: one tick every tickEvery.
func TestRunTicksEveryMinute(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t)
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- h.sched.Run(ctx) }()
		synctest.Wait()
		if got := h.store.count("ListActive"); got != 1 {
			t.Fatalf("%d listings at start, want 1", got)
		}

		for i := range 3 {
			h.clock.Advance(time.Minute)
			<-time.After(tickEvery + time.Second)
			synctest.Wait()
			if got := h.store.count("ListActive"); got != i+2 {
				t.Fatalf("%d listings after %d minutes, want %d", got, i+1, i+2)
			}
		}
		if !h.sched.LastTick().Equal(t1903.Add(3 * time.Minute)) {
			t.Errorf("LastTick = %v", h.sched.LastTick())
		}
		cancel()
		if err := <-done; err != nil {
			t.Errorf("Run = %v", err)
		}
	})
}

// Run on a context that is already done starts nothing and returns nil.
func TestRunOnADoneContextReturnsNil(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t)
		h.addGroup(chatA)
		h.fill(chatA, 10)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if err := h.sched.Run(ctx); err != nil {
			t.Errorf("Run = %v", err)
		}
		if h.eval.callCount() != 0 || h.store.count("ListActive") != 0 {
			t.Errorf("AI calls %d, listings %d", h.eval.callCount(), h.store.count("ListActive"))
		}
	})
}
