// SPDX-License-Identifier: Apache-2.0

// Package scheduler is the bot's clock. Every minute it asks which groups'
// daily cutoff has just passed and runs each of them: collect the window's
// messages, ask the AI, and publish the quote or a boring-day notice. An hourly
// job deletes old messages.
//
// The state the Python version kept in module globals lives on the Scheduler.
// What is lost on a restart (which days are finished, which reminders went out)
// is rebuilt from the quotes table, as it always was.
package scheduler

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/FreshLabDev/quoto/internal/deeplink"
	"github.com/FreshLabDev/quoto/internal/domain"
	"github.com/FreshLabDev/quoto/internal/window"
)

// Delays and timeouts. Variables, so that tests can shorten them.
var (
	// tickEvery is how often groups are checked for a cutoff that has passed.
	tickEvery = time.Minute
	// retentionEvery is how often old messages are deleted.
	retentionEvery = time.Hour
	// publishTimeout bounds the part of a run that is allowed to finish after a
	// shutdown signal. A flood wait on the send alone can take two minutes.
	publishTimeout = 3 * time.Minute
)

const (
	// concurrency is how many groups run at once. Their payloads are text, so
	// the width costs a few megabytes, not the media pipeline's hundreds.
	concurrency = 8

	// staleAfter is how long a quote may sit in publishing or notifying_boring
	// before it is written off as unknown. The send's outcome was lost: a crash
	// between Telegram's answer and the database, or a hang.
	staleAfter = 15 * time.Minute
	// retryAfter is how long a failed run waits before the next try, and how old
	// a publish_failed or boring_notice_failed quote must be to be retried.
	retryAfter = 15 * time.Minute

	// messageRetention is how long messages are kept. A window is at most 25 hours
	// and the catch-up two, so nothing older than about 27 hours can be used; the
	// rest is chat text that only costs space (doc §7.4 R1).
	messageRetention = 72 * time.Hour
)

// Config is what the scheduler needs from the environment.
type Config struct {
	BotUsername  string
	PremiumChats map[int64]bool // PREMIUM_CHAT_IDS
}

// The narrow views of the stores. *db.Store satisfies all of them.

// Groups is the part of quoto.group_settings the scheduler uses.
type Groups interface {
	Get(ctx context.Context, chatID int64) (domain.GroupSettings, bool, error)
	ListActive(ctx context.Context) ([]domain.GroupSettings, error)
	SetActive(ctx context.Context, chatID int64, active bool) (bool, error)
	SetTimezoneAuto(ctx context.Context, chatID int64, tz string) (bool, error)
}

// Messages is the part of quoto.messages the scheduler uses.
type Messages interface {
	CountWindow(ctx context.Context, chatID int64, w domain.Window) (int, error)
	DeleteWindow(ctx context.Context, chatID int64, w domain.Window) (int, error)
	ListWindow(ctx context.Context, chatID int64, w domain.Window) ([]domain.WindowMessage, error)
	DeleteOlderThan(ctx context.Context, t time.Time) (int64, error)
}

// Quotes is the part of quoto.quotes the scheduler uses.
type Quotes interface {
	ForDay(ctx context.Context, chatID int64, day time.Time) (*domain.Quote, error)
	Create(ctx context.Context, q domain.NewQuote) (quote domain.Quote, created bool, err error)
	MarkPublished(ctx context.Context, id, botMessageID int64, reason string) error
	MarkNotified(ctx context.Context, id, noticeMessageID int64) error
	SetStatus(ctx context.Context, id int64, status string, reason, operationError domain.TextPatch) error
	AppendError(ctx context.Context, id int64, msg string) error
	Delete(ctx context.Context, id int64) (bool, error)
	Stale(ctx context.Context, chatID int64, olderThan time.Time) ([]domain.Quote, error)
}

// Core is the part of the shared core database the scheduler uses.
type Core interface {
	ClaimChatLanguageAuto(ctx context.Context, chatID int64, code string) (bool, error)
	DisplayNames(ctx context.Context, ids []int64) (map[int64]string, error)
}

// Reports stores the day's AI evaluation report. It reports nothing back: a
// report that could not be written is logged by the store and never stops a
// quote. *PGReports implements it.
type Reports interface {
	SaveEvaluationReport(ctx context.Context, report domain.EvaluationReport)
	// ScrubSnapshotsOlderThan blanks the chat text copied into the reports
	// created before the given time and returns how many rows it changed.
	ScrubSnapshotsOlderThan(ctx context.Context, before time.Time) (int64, error)
}

// Deps are the scheduler's collaborators. Groups, Messages, Quotes and Core are
// *db.Store in the bot; Evaluator is *ai.Service and Sender *telegram.Client.
type Deps struct {
	Groups    Groups
	Messages  Messages
	Quotes    Quotes
	Core      Core
	Reports   Reports
	Evaluator domain.Evaluator
	Sender    domain.Sender
	Signer    deeplink.Signer
	Log       *slog.Logger
	Now       func() time.Time // nil = time.Now
}

// Scheduler runs the daily quote for every active group.
type Scheduler struct {
	cfg Config
	d   Deps
	log *slog.Logger
	now func() time.Time

	sem chan struct{} // width of the pool of group runs
	wg  sync.WaitGroup

	mu     sync.Mutex
	groups map[int64]*groupState

	lastTick  atomic.Int64 // unix nanoseconds of the last completed tick
	retaining atomic.Bool  // a retention job is running
}

// groupState is what the scheduler remembers about one group between ticks.
type groupState struct {
	// lock keeps one run of the group at a time: the minute tick and the catch-up
	// after the agreement are accepted must not both pay for the AI (doc §7.2 F7).
	lock chan struct{}

	// The fields below are guarded by Scheduler.mu.

	// completed is the quote day that is finished: no more runs for it until the
	// process restarts, when the quotes table decides again.
	completed time.Time
	// reminded is the quote day the agreement reminder was sent for.
	reminded time.Time
	// notBefore is the earliest time of the next try after a run that did not
	// finish its day (doc §7.2 F6).
	notBefore time.Time
}

// New builds a Scheduler. Nothing runs until Run is called.
func New(cfg Config, deps Deps) *Scheduler {
	s := &Scheduler{
		cfg:    cfg,
		d:      deps,
		log:    deps.Log,
		now:    deps.Now,
		sem:    make(chan struct{}, concurrency),
		groups: make(map[int64]*groupState),
	}
	if s.log == nil {
		s.log = slog.Default()
	}
	s.log = s.log.With("component", "scheduler")
	if s.now == nil {
		s.now = time.Now
	}
	return s
}

// LastTick is the time of the last completed minute tick, for /healthz. It is
// zero before the first one. A tick only dispatches the groups that are due, so
// a long AI call does not hold it back.
func (s *Scheduler) LastTick() time.Time {
	n := s.lastTick.Load()
	if n == 0 {
		return time.Time{}
	}
	return time.Unix(0, n).UTC()
}

// Run is the minute tick plus the hourly retention job. It returns nil when ctx
// is done and the group runs in flight have finished: a run that has not reached
// its publish step stops with ctx, one that has finishes the step.
func (s *Scheduler) Run(ctx context.Context) error {
	tick := time.NewTicker(tickEvery)
	defer tick.Stop()
	retention := time.NewTicker(retentionEvery)
	defer retention.Stop()
	s.loop(ctx, tick.C, retention.C)
	return nil
}

// loop is Run with the clocks as channels. It checks once at the start, so a
// deploy inside the catch-up window does not wait a minute, and clears old
// messages once, so a restart loop does not postpone the cleanup.
func (s *Scheduler) loop(ctx context.Context, tick, retention <-chan time.Time) {
	s.startRetention(ctx)
	s.tick(ctx)
	for {
		select {
		case <-ctx.Done():
			s.wg.Wait()
			return
		case <-tick:
			s.tick(ctx)
		case <-retention:
			s.startRetention(ctx)
		}
	}
}

// tick starts a run for every group whose cutoff has passed within the catch-up
// window. It does not wait for the runs: a group stuck in a slow AI call must
// not hold back the others, and a group that is still running is skipped.
func (s *Scheduler) tick(ctx context.Context) {
	if ctx.Err() != nil {
		return
	}
	now := s.now()
	groups, err := s.d.Groups.ListActive(ctx)
	if err != nil {
		if ctx.Err() == nil {
			s.log.Error("list active groups", "error", err)
		}
		return
	}
	for _, g := range groups {
		hour, minute := g.QuoteTime()
		w, due := window.Due(now, g.Location(), hour, minute)
		if !due || !s.runnable(g.ChatID, w, now) {
			continue
		}
		st := s.state(g.ChatID)
		select {
		case st.lock <- struct{}{}:
		default:
			continue // its previous run is still going
		}
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			defer func() { <-st.lock }()
			s.runTick(ctx, g, w)
		}()
	}
	s.lastTick.Store(s.now().UnixNano())
}

// runnable says a run for the day may start: the day is not finished and the
// last failed run has waited long enough.
func (s *Scheduler) runnable(chatID int64, w domain.Window, now time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.stateLocked(chatID)
	return !st.completed.Equal(w.QuoteDay) && !now.Before(st.notBefore)
}

func (s *Scheduler) state(chatID int64) *groupState {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stateLocked(chatID)
}

func (s *Scheduler) stateLocked(chatID int64) *groupState {
	st, ok := s.groups[chatID]
	if !ok {
		st = &groupState{lock: make(chan struct{}, 1)}
		s.groups[chatID] = st
	}
	return st
}

// runTick is one group's run from the minute tick: it takes a place in the pool
// of eight, runs, and records the outcome.
func (s *Scheduler) runTick(ctx context.Context, g domain.GroupSettings, w domain.Window) {
	if ctx.Err() != nil {
		return
	}
	select {
	case s.sem <- struct{}{}:
		defer func() { <-s.sem }()
	case <-ctx.Done():
		return
	}
	s.runGroup(ctx, g, w, false)
}

// runGroup runs one group's day and settles the outcome in memory. A failure is
// logged and sent to the developers, with the keys and texts Python used:
// "scheduler:<chat>:<day>" for the tick, "catch-up:<chat>" for the catch-up
// (the key is what limits the same alert to once per ten minutes).
func (s *Scheduler) runGroup(ctx context.Context, g domain.GroupSettings, w domain.Window, catchUp bool) {
	log := s.log.With("chat_id", g.ChatID, "quote_day", w.QuoteDay.Format(time.DateOnly))
	log.Info("quote run started", "window_start", w.Start, "window_end", w.End)

	done, err := s.processGroup(ctx, g, w)
	if err != nil {
		if ctx.Err() != nil {
			log.Info("quote run interrupted by shutdown", "error", err)
			return
		}
		log.Error("quote run failed", "error", err)
		key := fmt.Sprintf("scheduler:%d:%s", g.ChatID, w.QuoteDay.Format(time.DateOnly))
		text := fmt.Sprintf("Scheduler group run failed for chat %d: %v", g.ChatID, err)
		if catchUp {
			key = fmt.Sprintf("catch-up:%d", g.ChatID)
			text = fmt.Sprintf("Catch-up after agreement failed for chat %d: %v", g.ChatID, err)
		}
		s.d.Sender.Alert(ctx, key, text)
		done = false
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.stateLocked(g.ChatID)
	if done {
		st.completed = w.QuoteDay
		return
	}
	st.notBefore = s.now().Add(retryAfter)
}

// CatchUp processes the last closed window of one group right after its admin
// accepted the agreement, so that accepting late does not lose the day (doc
// §7.2 F7). It does nothing unless today's cutoff has already passed. It waits
// for a run of the group that is in progress, then looks at the day afresh: a
// day the tick finished only because the agreement was missing is run now.
// It is synchronous; the bot calls it in a goroutine.
func (s *Scheduler) CatchUp(ctx context.Context, chatID int64) {
	log := s.log.With("chat_id", chatID)
	st := s.state(chatID)
	select {
	case st.lock <- struct{}{}:
	case <-ctx.Done():
		return
	}
	defer func() { <-st.lock }()

	// The group is read once the lock is ours, so that it is as fresh as the run
	// ahead of us left it.
	g, ok, err := s.d.Groups.Get(ctx, chatID)
	if err != nil {
		if ctx.Err() == nil {
			log.Error("catch-up failed", "error", err)
			s.d.Sender.Alert(ctx, fmt.Sprintf("catch-up:%d", chatID),
				fmt.Sprintf("Catch-up after agreement failed for chat %d: load group: %v", chatID, err))
		}
		return
	}
	if !ok || !g.IsActive {
		return
	}
	hour, minute := g.QuoteTime()
	w, today := window.LastClosed(s.now(), g.Location(), hour, minute)
	if !today {
		log.Info("catch-up skipped: today's cutoff has not passed yet")
		return
	}

	s.mu.Lock()
	if st.completed.Equal(w.QuoteDay) {
		st.completed = time.Time{}
	}
	st.notBefore = time.Time{}
	s.mu.Unlock()

	s.runGroup(ctx, g, w, true)
}

// startRetention runs the cleanup in its own goroutine: the first run on a
// database that has never been cleaned deletes everything stale, in batches,
// and must not hold up the tick. Only one runs at a time.
func (s *Scheduler) startRetention(ctx context.Context) {
	if !s.retaining.CompareAndSwap(false, true) {
		return
	}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer s.retaining.Store(false)
		s.retain(ctx)
	}()
}

// retain deletes messages older than messageRetention, and the copies of their
// text in the evaluation reports. The two are independent: one failing does not
// skip the other.
func (s *Scheduler) retain(ctx context.Context) {
	before := s.now().Add(-messageRetention)
	if n, err := s.d.Messages.DeleteOlderThan(ctx, before); err != nil {
		if ctx.Err() == nil {
			s.log.Error("delete old messages", "error", err, "older_than", before)
		}
	} else {
		s.log.Info("old messages deleted", "count", n, "older_than", before)
	}
	if n, err := s.d.Reports.ScrubSnapshotsOlderThan(ctx, before); err != nil {
		if ctx.Err() == nil {
			s.log.Error("scrub report snapshots", "error", err, "older_than", before)
		}
	} else {
		s.log.Info("report snapshots scrubbed", "count", n, "older_than", before)
	}
}

// commitContext is the context of the part of a run that must not be cut off
// by a shutdown: once the AI has answered, the quote row is created and the
// message sent, and abandoning that between Telegram's answer and the database
// would leave a posted quote recorded as unknown. It keeps ctx's values and
// ignores its cancellation, with a timeout of its own.
func commitContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), publishTimeout)
}
