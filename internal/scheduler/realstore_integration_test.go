// SPDX-License-Identifier: Apache-2.0
//go:build integration

package scheduler

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/FreshLabDev/quoto/internal/db"
	"github.com/FreshLabDev/quoto/internal/db/dbtest"
	"github.com/FreshLabDev/quoto/internal/deeplink"
	"github.com/FreshLabDev/quoto/internal/domain"
)

// The scheduler tests run the daily cycle on an in-memory fakeStore whose
// CountWindow, ListWindow and DeleteWindow ignore the window (fakes_test.go:
// CountWindow returns len(msgs[chat]), DeleteWindow empties the chat). These
// tests run the same cycle on the real PostgreSQL stores.

type realRig struct {
	t      *testing.T
	s      *db.Store
	clock  *clock
	sender *fakeSender
	eval   *fakeEvaluator
	sched  *Scheduler
}

func newRealRig(t *testing.T, minMessages int) *realRig {
	t.Helper()
	ctx := context.Background()
	s := dbtest.New(t)
	c := &clock{t: t1903}
	r := &realRig{t: t, s: s, clock: c, sender: newFakeSender(), eval: &fakeEvaluator{}}
	r.sched = New(Config{BotUsername: botUsername}, Deps{
		Groups: s, Messages: s, Quotes: s, Core: s,
		Reports: &fakeReports{}, Evaluator: r.eval, Sender: r.sender,
		Signer: deeplink.NewSigner("123456:TESTTOKEN"),
		Log:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		Now:    c.Now,
	})
	admin := domain.Person{ID: 999, FirstName: "Admin", LanguageCode: "en"}
	if _, err := s.Ensure(ctx, admin, domain.ChatInfo{ID: chatA, Type: "supergroup", Title: "Club"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AcceptAgreement(ctx, chatA, admin.ID, "en"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetLanguage(ctx, domain.ScopeChat, chatA, "en", domain.SourceManual); err != nil {
		t.Fatal(err)
	}
	dbtest.Exec(t, s, `UPDATE group_settings SET quote_minute = 0, min_messages = $2 WHERE chat_id = $1`, chatA, minMessages)
	return r
}

// say stores a message of user (named "User <user-1000>") at the given time.
func (r *realRig) say(id int64, user int64, at time.Time, text, contentType string) int64 {
	r.t.Helper()
	ctx := context.Background()
	if err := r.s.TouchUser(ctx, domain.Person{ID: user, FirstName: fmt.Sprintf("User %d", user-1000)}); err != nil {
		r.t.Fatal(err)
	}
	dbID, _, err := r.s.Save(ctx, domain.NewMessage{
		MessageID: id, ChatID: chatA, UserID: user, Text: text, ContentType: contentType, CreatedAt: at,
	})
	if err != nil {
		r.t.Fatal(err)
	}
	return dbID
}

func (r *realRig) tick() {
	r.t.Helper()
	r.sched.tick(context.Background())
	r.sched.wg.Wait()
}

func (r *realRig) rows(where string, args ...any) int {
	r.t.Helper()
	var n int
	if err := r.s.Pool().QueryRow(context.Background(), `SELECT count(*) FROM messages WHERE `+where, args...).Scan(&n); err != nil {
		r.t.Fatal(err)
	}
	return n
}

var (
	winStart = time.Date(2026, 3, 26, 19, 0, 0, 0, time.UTC) // 21:00 Kyiv, winter time
	winEnd   = time.Date(2026, 3, 27, 19, 0, 0, 0, time.UTC)
)

func TestDayCycleOnRealStores(t *testing.T) {
	r := newRealRig(t, 3)
	ctx := context.Background()

	// Ids 1..6 are in the window; 7 is one second before it, 8 is exactly at its end.
	for i := int64(1); i <= 6; i++ {
		r.say(100+i, 1000+i, winStart.Add(time.Duration(i)*time.Hour), fmt.Sprintf("line %d", i), domain.ContentText)
	}
	r.say(107, 1007, winStart.Add(-time.Second), "before the window", domain.ContentText)
	r.say(108, 1008, winEnd, "at the end of the window", domain.ContentText)
	r.eval.set(parsedResult(6, domain.ReasonWorthy))

	r.tick()

	if r.eval.callCount() != 1 {
		t.Fatalf("%d model calls", r.eval.callCount())
	}
	for _, m := range r.eval.call(0).msgs {
		if m.Text == "before the window" || m.Text == "at the end of the window" {
			t.Errorf("the model was given %q, outside [start,end)", m.Text)
		}
	}
	if n := len(r.eval.call(0).msgs); n != 6 {
		t.Errorf("the model was given %d messages, want 6", n)
	}
	sent := r.sender.sentTo(chatA)
	if len(sent) != 1 {
		t.Fatalf("%d messages sent", len(sent))
	}
	q, err := r.s.ForDay(ctx, chatA, day27)
	if err != nil || q == nil {
		t.Fatalf("quote: %v %v", q, err)
	}
	if q.DecisionStatus != domain.StatusPublished || q.BotMessageID != sent[0].id || q.Text != "line 6" || q.AuthorID != 1006 {
		t.Errorf("quote %+v", q)
	}
	if !q.WindowStart.Equal(winStart) || !q.WindowEnd.Equal(winEnd) {
		t.Errorf("window %v - %v", q.WindowStart, q.WindowEnd)
	}
	// The window is cleared; what lay outside it is not.
	if n := r.rows(`created_at >= $1 AND created_at < $2`, winStart, winEnd); n != 0 {
		t.Errorf("%d messages of the window left", n)
	}
	if n := r.rows(`chat_id = $1`, chatA); n != 2 {
		t.Errorf("%d messages left, want the 2 outside the window", n)
	}

	// Once per day.
	r.clock.Advance(time.Minute)
	r.tick()
	if r.eval.callCount() != 1 || len(r.sender.sentTo(chatA)) != 1 {
		t.Errorf("second tick: %d model calls, %d messages", r.eval.callCount(), len(r.sender.sentTo(chatA)))
	}
}

func TestSmallDayOnRealStoresIsSilentAndCleared(t *testing.T) {
	r := newRealRig(t, 5)
	for i := int64(1); i <= 4; i++ {
		r.say(100+i, 1000+i, winStart.Add(time.Duration(i)*time.Hour), "x", domain.ContentText)
	}
	r.tick()
	if r.eval.callCount() != 0 || len(r.sender.sentTo(chatA)) != 0 {
		t.Errorf("%d model calls, %d messages for a day below the minimum", r.eval.callCount(), len(r.sender.sentTo(chatA)))
	}
	if n := r.rows(`chat_id = $1`, chatA); n != 0 {
		t.Errorf("%d messages left", n)
	}
}
