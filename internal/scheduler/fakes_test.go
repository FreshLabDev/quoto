// SPDX-License-Identifier: Apache-2.0

package scheduler

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
	_ "time/tzdata"

	"github.com/FreshLabDev/quoto/internal/deeplink"
	"github.com/FreshLabDev/quoto/internal/domain"
)

// Errors the fake Telegram understands: the scheduler asks the Sender whether a
// failure may have been delivered, and whether the chat is gone.
var (
	errUnknownOutcome = errors.New("timeout: outcome unknown")
	errChatGone       = errors.New("Bad Request: chat not found")
)

// clock is a settable time source.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) Set(t time.Time) {
	c.mu.Lock()
	c.t = t
	c.mu.Unlock()
}

func (c *clock) Advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// fakeStore stands in for *db.Store: Groups, Messages, Quotes and Core in memory,
// with a log of calls and failures that can be switched on per method.
type fakeStore struct {
	mu    sync.Mutex
	clock *clock

	groups map[int64]*domain.GroupSettings
	msgs   map[int64][]domain.WindowMessage
	quotes []*domain.Quote
	names  map[int64]string
	nextID int64

	calls []string
	errs  map[string]error
	// chatErrs fails a method for one chat only, keyed "Method/chat".
	chatErrs map[string]error
	// windows are the windows the scheduler asked about, in order.
	windows []domain.Window
	cleared []clearedWindow
	// claimResult is what ClaimChatLanguageAuto reports; claims records the calls.
	claimResult bool
	claims      []string
	retention   []time.Time
	// retainHook, when set, runs at the start of DeleteOlderThan, outside the lock,
	// and may block.
	retainHook func(ctx context.Context)
	// blindForDay makes ForDay report no quote whatever the table holds: another
	// worker created the row between the lookup and Create.
	blindForDay bool
}

type clearedWindow struct {
	chat int64
	w    domain.Window
}

func newFakeStore(c *clock) *fakeStore {
	return &fakeStore{
		clock:       c,
		groups:      map[int64]*domain.GroupSettings{},
		msgs:        map[int64][]domain.WindowMessage{},
		names:       map[int64]string{},
		errs:        map[string]error{},
		chatErrs:    map[string]error{},
		claimResult: true,
	}
}

// record logs a call and returns the failure set for it. The lock is held.
func (s *fakeStore) record(name string) error {
	s.calls = append(s.calls, name)
	return s.errs[name]
}

// recordChat is record for a method that takes a chat: it fails for the chat
// alone as well as for everyone.
func (s *fakeStore) recordChat(name string, chatID int64) error {
	s.calls = append(s.calls, name)
	if err := s.chatErrs[fmt.Sprintf("%s/%d", name, chatID)]; err != nil {
		return err
	}
	return s.errs[name]
}

func (s *fakeStore) fail(name string, err error) {
	s.mu.Lock()
	s.errs[name] = err
	s.mu.Unlock()
}

func (s *fakeStore) failChat(name string, chatID int64, err error) {
	s.mu.Lock()
	s.chatErrs[fmt.Sprintf("%s/%d", name, chatID)] = err
	s.mu.Unlock()
}

func (s *fakeStore) callLog() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.calls...)
}

func (s *fakeStore) count(name string) int {
	n := 0
	for _, c := range s.callLog() {
		if c == name {
			n++
		}
	}
	return n
}

// Groups.

func (s *fakeStore) Get(_ context.Context, chatID int64) (domain.GroupSettings, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.record("Get"); err != nil {
		return domain.GroupSettings{}, false, err
	}
	g, ok := s.groups[chatID]
	if !ok {
		return domain.GroupSettings{}, false, nil
	}
	return *g, true, nil
}

func (s *fakeStore) ListActive(context.Context) ([]domain.GroupSettings, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.record("ListActive"); err != nil {
		return nil, err
	}
	var out []domain.GroupSettings
	for _, g := range s.groups {
		if g.IsActive {
			out = append(out, *g)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ChatID < out[j].ChatID })
	return out, nil
}

func (s *fakeStore) SetActive(_ context.Context, chatID int64, active bool) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, fmt.Sprintf("SetActive(%d,%t)", chatID, active))
	if err := s.errs["SetActive"]; err != nil {
		return false, err
	}
	g, ok := s.groups[chatID]
	if ok {
		g.IsActive = active
	}
	return ok, nil
}

func (s *fakeStore) SetTimezoneAuto(_ context.Context, chatID int64, tz string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, "SetTimezoneAuto("+tz+")")
	if err := s.errs["SetTimezoneAuto"]; err != nil {
		return false, err
	}
	g, ok := s.groups[chatID]
	if !ok || g.Timezone != nil {
		return false, nil
	}
	g.Timezone = &tz
	return true, nil
}

// Messages.

func (s *fakeStore) CountWindow(_ context.Context, chatID int64, w domain.Window) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.windows = append(s.windows, w)
	if err := s.recordChat("CountWindow", chatID); err != nil {
		return 0, err
	}
	return len(s.msgs[chatID]), nil
}

func (s *fakeStore) ListWindow(_ context.Context, chatID int64, _ domain.Window) ([]domain.WindowMessage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.recordChat("ListWindow", chatID); err != nil {
		return nil, err
	}
	return append([]domain.WindowMessage(nil), s.msgs[chatID]...), nil
}

func (s *fakeStore) DeleteWindow(_ context.Context, chatID int64, w domain.Window) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.recordChat("DeleteWindow", chatID); err != nil {
		return 0, err
	}
	n := len(s.msgs[chatID])
	s.msgs[chatID] = nil
	s.cleared = append(s.cleared, clearedWindow{chatID, w})
	return n, nil
}

func (s *fakeStore) DeleteOlderThan(ctx context.Context, t time.Time) (int64, error) {
	s.mu.Lock()
	hook := s.retainHook
	s.mu.Unlock()
	if hook != nil {
		hook(ctx)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.record("DeleteOlderThan"); err != nil {
		return 0, err
	}
	s.retention = append(s.retention, t)
	return 3, nil
}

// Quotes.

func (s *fakeStore) find(id int64) *domain.Quote {
	for _, q := range s.quotes {
		if q.ID == id {
			return q
		}
	}
	return nil
}

func (s *fakeStore) ForDay(_ context.Context, chatID int64, day time.Time) (*domain.Quote, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.record("ForDay"); err != nil {
		return nil, err
	}
	if s.blindForDay {
		return nil, nil
	}
	for _, q := range s.quotes {
		if q.GroupID == chatID && q.QuoteDay.Equal(day) {
			c := *q
			return &c, nil
		}
	}
	return nil, nil
}

func (s *fakeStore) Create(_ context.Context, n domain.NewQuote) (domain.Quote, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.record("Create"); err != nil {
		return domain.Quote{}, false, err
	}
	for _, q := range s.quotes {
		if q.GroupID == n.GroupID && q.QuoteDay.Equal(n.QuoteDay) {
			return *q, false, nil
		}
	}
	s.nextID++
	snapshot, _ := domain.EncodeContext(n.Context)
	now := s.clock.Now()
	q := &domain.Quote{
		ID: s.nextID, GroupID: n.GroupID, AuthorID: n.AuthorID, Text: n.Text, Score: n.Score,
		ReactionScore: n.ReactionScore, AIScore: n.AIScore, LengthScore: n.LengthScore, ReactionCount: n.ReactionCount,
		MessageID: n.MessageID, ContentType: n.ContentType, AIModel: n.AIModel, AIBestText: n.AIBestText,
		Context:  domain.DecodeContext(snapshot),
		QuoteDay: n.QuoteDay, WindowStart: n.WindowStart, WindowEnd: n.WindowEnd,
		DecisionStatus: n.DecisionStatus, StatusChangedAt: now, DecisionReason: n.DecisionReason,
		OperationError: n.OperationError, CreatedAt: now,
	}
	s.quotes = append(s.quotes, q)
	return *q, true, nil
}

func (s *fakeStore) MarkPublished(_ context.Context, id, botMessageID int64, reason string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, fmt.Sprintf("MarkPublished(%d,%d,%q)", id, botMessageID, reason))
	if err := s.errs["MarkPublished"]; err != nil {
		return err
	}
	q := s.find(id)
	q.BotMessageID, q.DecisionStatus, q.StatusChangedAt, q.OperationError = botMessageID, domain.StatusPublished, s.clock.Now(), ""
	if reason != "" {
		q.DecisionReason = reason
	}
	return nil
}

func (s *fakeStore) MarkNotified(_ context.Context, id, noticeMessageID int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, fmt.Sprintf("MarkNotified(%d,%d)", id, noticeMessageID))
	if err := s.errs["MarkNotified"]; err != nil {
		return err
	}
	q := s.find(id)
	q.NoticeMessageID, q.DecisionStatus, q.StatusChangedAt, q.OperationError = noticeMessageID, domain.StatusSkippedBoring, s.clock.Now(), ""
	return nil
}

func (s *fakeStore) SetStatus(_ context.Context, id int64, status string, reason, operationError domain.TextPatch) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, "SetStatus("+status+")")
	if err := s.errs["SetStatus"]; err != nil {
		return err
	}
	q := s.find(id)
	q.DecisionStatus, q.StatusChangedAt = status, s.clock.Now()
	apply := func(p domain.TextPatch, dst *string) {
		if !p.Set {
			return
		}
		*dst = ""
		if p.Value != nil {
			*dst = *p.Value
		}
	}
	apply(reason, &q.DecisionReason)
	apply(operationError, &q.OperationError)
	return nil
}

func (s *fakeStore) AppendError(_ context.Context, id int64, msg string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, "AppendError")
	if err := s.errs["AppendError"]; err != nil {
		return err
	}
	q := s.find(id)
	next := domain.PyStrip(msg)
	if r := []rune(next); len(r) > 250 {
		next = string(r[:250])
	}
	if q.OperationError != "" {
		next = q.OperationError + " | " + next
	}
	q.OperationError = next
	return nil
}

func (s *fakeStore) Delete(_ context.Context, id int64) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, "Delete")
	if err := s.errs["Delete"]; err != nil {
		return false, err
	}
	for i, q := range s.quotes {
		if q.ID == id {
			s.quotes = append(s.quotes[:i], s.quotes[i+1:]...)
			return true, nil
		}
	}
	return false, nil
}

func (s *fakeStore) Stale(_ context.Context, chatID int64, olderThan time.Time) ([]domain.Quote, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.record("Stale"); err != nil {
		return nil, err
	}
	var out []domain.Quote
	for _, q := range s.quotes {
		if q.GroupID == chatID && domain.InProgress(q.DecisionStatus) && q.StatusChangedAt.Before(olderThan) {
			out = append(out, *q)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].WindowEnd.After(out[j].WindowEnd) })
	return out, nil
}

// Core.

func (s *fakeStore) ClaimChatLanguageAuto(_ context.Context, chatID int64, code string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, "ClaimChatLanguageAuto")
	s.claims = append(s.claims, code)
	if err := s.errs["ClaimChatLanguageAuto"]; err != nil {
		return false, err
	}
	if s.claimResult {
		if g, ok := s.groups[chatID]; ok {
			g.Language, g.LanguageSource = code, domain.SourceAuto
		}
	}
	return s.claimResult, nil
}

func (s *fakeStore) DisplayNames(_ context.Context, ids []int64) (map[int64]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.record("DisplayNames"); err != nil {
		return nil, err
	}
	out := map[int64]string{}
	for _, id := range ids {
		if n, ok := s.names[id]; ok {
			out[id] = n
		}
	}
	return out, nil
}

// quote returns a copy of the day's quote row.
func (s *fakeStore) quote(t *testing.T, chatID int64, day time.Time) domain.Quote {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, q := range s.quotes {
		if q.GroupID == chatID && q.QuoteDay.Equal(day) {
			return *q
		}
	}
	t.Fatalf("no quote for chat %d on %s", chatID, day.Format(time.DateOnly))
	return domain.Quote{}
}

func (s *fakeStore) hasQuote(chatID int64, day time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, q := range s.quotes {
		if q.GroupID == chatID && q.QuoteDay.Equal(day) {
			return true
		}
	}
	return false
}

func (s *fakeStore) addQuote(q domain.Quote) *domain.Quote {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nextID++
	q.ID = s.nextID
	s.quotes = append(s.quotes, &q)
	return &q
}

func (s *fakeStore) messageCount(chatID int64) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.msgs[chatID])
}

func (s *fakeStore) clearedCount(chatID int64) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, c := range s.cleared {
		if c.chat == chatID {
			n++
		}
	}
	return n
}

// sentMessage is one message the fake Telegram accepted.
type sentMessage struct {
	chat int64
	text string
	// copyOf is the original message id of a copy; 0 for a plain message.
	copyOf int64
	id     int64
}

type reminderCall struct {
	chat     int64
	lang     string
	outdated bool
}

type alertCall struct{ key, text string }

// fakeSender stands in for the Telegram adapter.
type fakeSender struct {
	mu     sync.Mutex
	nextID int64

	sent      []sentMessage
	pins      []int64
	reminders []reminderCall
	alerts    []alertCall

	sendErr, copyErr, pinErr, reminderErr error
	// beforeSend, when set, runs at the start of SendHTML and CopyWithCaption
	// with the context the scheduler passed.
	beforeSend func(ctx context.Context)
}

func newFakeSender() *fakeSender { return &fakeSender{nextID: 100} }

func (s *fakeSender) take(ctx context.Context, m sentMessage, err error) (int64, error) {
	if s.beforeSend != nil {
		s.beforeSend(ctx)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err != nil {
		return 0, err
	}
	s.nextID++
	m.id = s.nextID
	s.sent = append(s.sent, m)
	return m.id, nil
}

func (s *fakeSender) SendHTML(ctx context.Context, chatID int64, html string) (int64, error) {
	s.mu.Lock()
	err := s.sendErr
	s.mu.Unlock()
	return s.take(ctx, sentMessage{chat: chatID, text: html}, err)
}

func (s *fakeSender) CopyWithCaption(ctx context.Context, chatID, messageID int64, caption string) (int64, error) {
	s.mu.Lock()
	err := s.copyErr
	s.mu.Unlock()
	return s.take(ctx, sentMessage{chat: chatID, text: caption, copyOf: messageID}, err)
}

func (s *fakeSender) Pin(_ context.Context, _, messageID int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pinErr != nil {
		return s.pinErr
	}
	s.pins = append(s.pins, messageID)
	return nil
}

func (s *fakeSender) SendAgreementReminder(_ context.Context, chatID int64, lang string, outdated bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.reminderErr != nil {
		return s.reminderErr
	}
	s.reminders = append(s.reminders, reminderCall{chatID, lang, outdated})
	return nil
}

func (s *fakeSender) Alert(_ context.Context, key, text string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.alerts = append(s.alerts, alertCall{key, text})
}

func (s *fakeSender) OutcomeUnknown(err error) bool { return errors.Is(err, errUnknownOutcome) }
func (s *fakeSender) ChatNotFound(err error) bool   { return errors.Is(err, errChatGone) }

func (s *fakeSender) sentTo(chat int64) []sentMessage {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []sentMessage
	for _, m := range s.sent {
		if m.chat == chat {
			out = append(out, m)
		}
	}
	return out
}

func (s *fakeSender) setErrors(send, copyErr, pin, reminder error) {
	s.mu.Lock()
	s.sendErr, s.copyErr, s.pinErr, s.reminderErr = send, copyErr, pin, reminder
	s.mu.Unlock()
}

func (s *fakeSender) alertList() []alertCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]alertCall(nil), s.alerts...)
}

// fakeEvaluator stands in for the AI service.
type fakeEvaluator struct {
	mu     sync.Mutex
	result domain.EvalResult
	// explicit is set once a test chooses the answer; until then fill keeps the
	// canned answer scoring every message it stored.
	explicit bool
	calls    []evalCall
	// hook, when set, runs inside Evaluate and may block; its result replaces the
	// canned one if it returns ok.
	hook func(ctx context.Context) (domain.EvalResult, bool)
}

type evalCall struct {
	msgs   []domain.EvalMessage
	detect bool
}

func (e *fakeEvaluator) Evaluate(ctx context.Context, msgs []domain.EvalMessage, detectLanguage bool) domain.EvalResult {
	e.mu.Lock()
	e.calls = append(e.calls, evalCall{append([]domain.EvalMessage(nil), msgs...), detectLanguage})
	res, hook := e.result, e.hook
	e.mu.Unlock()
	if hook != nil {
		if r, ok := hook(ctx); ok {
			return r
		}
	}
	return res
}

func (e *fakeEvaluator) callCount() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.calls)
}

func (e *fakeEvaluator) call(i int) evalCall {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.calls[i]
}

func (e *fakeEvaluator) set(r domain.EvalResult) {
	e.mu.Lock()
	e.result, e.explicit = r, true
	e.mu.Unlock()
}

// fakeReports records the reports.
type fakeReports struct {
	mu      sync.Mutex
	reports []domain.EvaluationReport
}

func (r *fakeReports) SaveEvaluationReport(_ context.Context, rep domain.EvaluationReport) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.reports = append(r.reports, rep)
}

func (r *fakeReports) list() []domain.EvaluationReport {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]domain.EvaluationReport(nil), r.reports...)
}

// harness wires a Scheduler to the fakes. The clock starts at 21:03 in Kyiv on
// 2026-03-27 (19:03 UTC, winter time): three minutes after the default cutoff.
type harness struct {
	t       *testing.T
	clock   *clock
	store   *fakeStore
	sender  *fakeSender
	eval    *fakeEvaluator
	reports *fakeReports
	sched   *Scheduler
}

const (
	botUsername = "quoto_test_bot"
	chatA       = int64(-1001234567890)
)

var (
	day27 = time.Date(2026, 3, 27, 0, 0, 0, 0, time.UTC)
	t1903 = time.Date(2026, 3, 27, 19, 3, 0, 0, time.UTC)
)

func newHarness(t *testing.T) *harness {
	t.Helper()
	c := &clock{t: t1903}
	h := &harness{
		t: t, clock: c, store: newFakeStore(c), sender: newFakeSender(),
		eval: &fakeEvaluator{}, reports: &fakeReports{},
	}
	h.sched = New(Config{BotUsername: botUsername}, Deps{
		Groups: h.store, Messages: h.store, Quotes: h.store, Core: h.store,
		Reports: h.reports, Evaluator: h.eval, Sender: h.sender,
		Signer: deeplink.NewSigner("123456:TESTTOKEN"),
		Log:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		Now:    c.Now,
	})
	h.eval.result = parsedResult(0, "worthy")
	return h
}

// addGroup stores an active group that accepted the agreement, speaks English
// and uses the default cutoff and time zone. tweak may change it.
func (h *harness) addGroup(chat int64, tweak ...func(*domain.GroupSettings)) *domain.GroupSettings {
	accepted := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	version := domain.DocumentVersion
	g := &domain.GroupSettings{
		ChatID: chat, IsActive: true, AgreementAcceptedAt: &accepted, AgreementVersion: &version,
		Name: "Club", Language: "en", LanguageSource: domain.SourceManual,
	}
	for _, f := range tweak {
		f(g)
	}
	h.store.mu.Lock()
	h.store.groups[chat] = g
	h.store.mu.Unlock()
	return g
}

// fill stores n text messages for the chat, ids 1..n, Telegram ids 100+i, authors
// 1000+i named "User i".
func (h *harness) fill(chat int64, n int) {
	h.t.Helper()
	h.store.mu.Lock()
	defer h.store.mu.Unlock()
	for i := int64(1); i <= int64(n); i++ {
		h.store.msgs[chat] = append(h.store.msgs[chat], domain.WindowMessage{
			ID: i, MessageID: 100 + i, UserID: 1000 + i, Text: fmt.Sprintf("line %d", i), ContentType: domain.ContentText,
			CreatedAt: t1903.Add(-time.Duration(n-int(i)+1) * time.Hour),
		})
		h.store.names[1000+i] = fmt.Sprintf("User %d", i)
	}
	h.eval.mu.Lock()
	if !h.eval.explicit && len(h.eval.result.Scores) < n {
		h.eval.result = parsedResult(n, domain.ReasonWorthy)
	}
	h.eval.mu.Unlock()
}

// setMessage edits message id i of the chat.
func (h *harness) setMessage(chat, id int64, f func(*domain.WindowMessage)) {
	h.t.Helper()
	h.store.mu.Lock()
	defer h.store.mu.Unlock()
	for i := range h.store.msgs[chat] {
		if h.store.msgs[chat][i].ID == id {
			f(&h.store.msgs[chat][i])
			return
		}
	}
	h.t.Fatalf("no message %d in chat %d", id, chat)
}

// run is one tick and the wait for every run it started.
func (h *harness) run() {
	h.t.Helper()
	h.sched.tick(context.Background())
	h.sched.wg.Wait()
}

// parsedResult is a parsed answer that scores the first n messages in order
// 0.1, 0.2, ... and gives the day the verdict in the given reason code.
func parsedResult(n int, reason string) domain.EvalResult {
	var scores []domain.Score
	for i := 1; i <= n; i++ {
		scores = append(scores, domain.Score{ID: int64(i), Value: float64(i) / 10})
	}
	return domain.EvalResult{
		Scores: scores, ActualModel: "claude-actual", RequestedModel: "claude-requested", Status: domain.RunParsed,
		RequestID:  strings.Repeat("a", 32),
		DayVerdict: &domain.DayVerdict{ShouldPublish: reason == domain.ReasonWorthy, ReasonCode: reason, ReasonText: "because"},
	}
}

func ptr[T any](v T) *T { return &v }
