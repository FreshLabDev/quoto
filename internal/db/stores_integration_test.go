// SPDX-License-Identifier: Apache-2.0
//go:build integration

package db

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/FreshLabDev/quoto/internal/domain"
)

var (
	ctx   = context.Background()
	alice = domain.Person{ID: 11, Username: "alice", FirstName: "Alice", LastName: "A", LanguageCode: "uk"}
	group = domain.ChatInfo{ID: -1001, Type: "supergroup", Title: "Club"}
	day0  = time.Date(2026, 10, 21, 0, 0, 0, 0, time.UTC)
)

func window() domain.Window {
	return domain.Window{
		QuoteDay: day0,
		Start:    time.Date(2026, 10, 20, 19, 0, 0, 0, time.UTC),
		End:      time.Date(2026, 10, 21, 19, 0, 0, 0, time.UTC),
	}
}

func ensureGroup(t *testing.T, s *Store) domain.GroupSettings {
	t.Helper()
	g, err := s.Ensure(ctx, alice, group)
	if err != nil {
		t.Fatalf("ensure: %v", err)
	}
	return g
}

func save(t *testing.T, s *Store, id int64, at time.Time, text string, media *domain.MediaSource) int64 {
	t.Helper()
	ct := domain.ContentText
	if media != nil {
		ct = media.Kind
	}
	dbID, inserted, err := s.Save(ctx, domain.NewMessage{
		MessageID: id, ChatID: group.ID, UserID: alice.ID, Text: text, ContentType: ct, CreatedAt: at, Media: media,
	})
	if err != nil || !inserted {
		t.Fatalf("save %d: inserted=%t err=%v", id, inserted, err)
	}
	return dbID
}

func TestEnsureCreatesAndHydrates(t *testing.T) {
	s := migratedStore(t)
	g := ensureGroup(t, s)
	if !g.IsActive || g.QuoteMinute == nil || *g.QuoteMinute < 0 || *g.QuoteMinute > domain.QuoteMinuteJitter {
		t.Fatalf("new group %+v", g)
	}
	if g.QuoteHour != nil || g.Timezone != nil || g.MinMessages != nil || g.AgreementVersion != nil {
		t.Fatalf("a new group must leave defaults NULL: %+v", g)
	}
	minute := *g.QuoteMinute

	// A second message keeps the row, and an inactive group wakes up.
	if _, err := s.SetActive(ctx, group.ID, false); err != nil {
		t.Fatal(err)
	}
	g, err := s.Ensure(ctx, alice, group)
	if err != nil || !g.IsActive || *g.QuoteMinute != minute {
		t.Fatalf("second ensure: %+v %v", g, err)
	}

	got, ok, err := s.Get(ctx, group.ID)
	if err != nil || !ok || got.Name != "Club" {
		t.Fatalf("get: %+v ok=%t err=%v", got, ok, err)
	}
	if _, ok, _ := s.Get(ctx, -5); ok {
		t.Fatal("get of an unknown group")
	}
	list, err := s.ListActive(ctx)
	if err != nil || len(list) != 1 || list[0].Name != "Club" {
		t.Fatalf("list: %+v %v", list, err)
	}

	names, err := s.DisplayNames(ctx, []int64{alice.ID, 999})
	if err != nil || !reflect.DeepEqual(names, map[int64]string{alice.ID: "Alice A"}) {
		t.Fatalf("names %v %v", names, err)
	}
}

func TestLanguageThroughCore(t *testing.T) {
	s := migratedStore(t)
	ensureGroup(t, s)

	// The client hint from the touch is the weakest claim.
	claim, err := s.LanguageClaim(ctx, domain.ScopeUser, alice.ID)
	if err != nil || claim.Language != "uk" || claim.Source != domain.SourceClient {
		t.Fatalf("hint %+v %v", claim, err)
	}
	if err := s.SetLanguage(ctx, domain.ScopeUser, alice.ID, "de", domain.SourceManual); err != nil {
		t.Fatal(err)
	}
	claim, _ = s.LanguageClaim(ctx, domain.ScopeUser, alice.ID)
	if claim.Language != "de" || claim.Source != domain.SourceManual {
		t.Fatalf("manual %+v", claim)
	}
	// Following Telegram again: Quoto's claim goes, the hint resurfaces.
	if err := s.ClearLanguage(ctx, domain.ScopeUser, alice.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.TouchUser(ctx, alice); err != nil {
		t.Fatal(err)
	}
	claim, _ = s.LanguageClaim(ctx, domain.ScopeUser, alice.ID)
	if claim.Language != "uk" || claim.Source != domain.SourceClient {
		t.Fatalf("after clear %+v", claim)
	}

	wrote, err := s.ClaimChatLanguageAuto(ctx, group.ID, "uk")
	if err != nil || !wrote {
		t.Fatalf("first auto claim: %t %v", wrote, err)
	}
	wrote, err = s.ClaimChatLanguageAuto(ctx, group.ID, "ru")
	if err != nil || wrote {
		t.Fatalf("second auto claim must not overwrite: %t %v", wrote, err)
	}
	g, _, _ := s.Get(ctx, group.ID)
	if g.Language != "uk" || g.LanguageSource != domain.SourceAuto {
		t.Fatalf("group language %q %q", g.Language, g.LanguageSource)
	}
}

func TestGroupSettingsMutations(t *testing.T) {
	s := migratedStore(t)
	ensureGroup(t, s)
	id := group.ID

	// Time wraps within the day and writes hour and minute together.
	if ok, err := s.AdjustQuoteTime(ctx, id, -30); err != nil || !ok {
		t.Fatal(ok, err)
	}
	g, _, _ := s.Get(ctx, id)
	h, m := g.QuoteTime()
	if h != 20 && h != 21 || m < 0 || m > 59 {
		t.Fatalf("time %d:%d", h, m)
	}
	for range 24 {
		if _, err := s.AdjustQuoteTime(ctx, id, 60); err != nil {
			t.Fatal(err)
		}
	}
	g2, _, _ := s.Get(ctx, id)
	h2, m2 := g2.QuoteTime()
	if h2 != h || m2 != m {
		t.Fatalf("24 hours should come back to %d:%d, got %d:%d", h, m, h2, m2)
	}

	for range 5 {
		if _, err := s.AdjustMinMessages(ctx, id, 100); err != nil {
			t.Fatal(err)
		}
	}
	if g, _, _ = s.Get(ctx, id); g.MinMessagesEffective() != domain.MinMessagesHighest {
		t.Fatalf("min messages %d", g.MinMessagesEffective())
	}

	if g, _, _ = s.Get(ctx, id); !g.PinEnabled() {
		t.Fatal("NULL pin counts as on")
	}
	if _, err := s.Toggle(ctx, id, domain.TogglePin); err != nil {
		t.Fatal(err)
	}
	if g, _, _ = s.Get(ctx, id); g.PinEnabled() {
		t.Fatal("pin should be off after one toggle from NULL")
	}
	if _, err := s.Toggle(ctx, id, domain.TogglePin); err != nil {
		t.Fatal(err)
	}
	if g, _, _ = s.Get(ctx, id); !g.PinEnabled() {
		t.Fatal("pin should be on after the second toggle")
	}

	if ok, _ := s.SetTimezoneAuto(ctx, id, "Europe/Berlin"); !ok {
		t.Fatal("auto timezone on an empty group")
	}
	if ok, _ := s.SetTimezoneAuto(ctx, id, "Asia/Tokyo"); ok {
		t.Fatal("auto timezone must not overwrite")
	}
	if ok, _ := s.SetTimezone(ctx, id, "Asia/Tokyo"); !ok {
		t.Fatal("manual timezone")
	}
	if g, _, _ = s.Get(ctx, id); g.TimezoneName() != "Asia/Tokyo" {
		t.Fatalf("timezone %q", g.TimezoneName())
	}

	if ok, err := s.AdjustQuoteTime(ctx, -777, 10); err != nil || ok {
		t.Fatalf("unknown group: %t %v", ok, err)
	}
}

func TestAcceptAgreement(t *testing.T) {
	s := migratedStore(t)
	ensureGroup(t, s)
	if ok, err := s.AcceptAgreement(ctx, group.ID, alice.ID, "ru"); err != nil || !ok {
		t.Fatal(ok, err)
	}
	g, _, _ := s.Get(ctx, group.ID)
	if !g.AgreementAccepted() || g.AgreementAcceptedBy == nil || *g.AgreementAcceptedBy != alice.ID || *g.AgreementLanguage != "ru" {
		t.Fatalf("agreement %+v", g)
	}
	at := *g.AgreementAcceptedAt
	if ok, err := s.AcceptAgreement(ctx, group.ID, 99, "en"); err != nil || !ok {
		t.Fatal(ok, err)
	}
	g, _, _ = s.Get(ctx, group.ID)
	if !g.AgreementAcceptedAt.Equal(at) || *g.AgreementAcceptedBy != alice.ID {
		t.Fatal("a second acceptance rewrote the first")
	}
}

func TestMessagesLifecycle(t *testing.T) {
	s := migratedStore(t)
	ensureGroup(t, s)
	w := window()

	inside := w.Start.Add(time.Hour)
	save(t, s, 1, w.Start.Add(-time.Second), "before", nil)
	save(t, s, 2, w.Start, "at start", nil)
	save(t, s, 3, inside, "photo caption", &domain.MediaSource{
		Kind: domain.ContentPhoto, FileID: "F", FileUniqueID: "U", Width: 800, Height: 600, FileSize: 1234, SupportsAnalysis: true,
	})
	save(t, s, 4, w.End, "at end", nil)

	if _, inserted, err := s.Save(ctx, domain.NewMessage{MessageID: 2, ChatID: group.ID, UserID: alice.ID, Text: "again", ContentType: "text", CreatedAt: w.Start}); err != nil || inserted {
		t.Fatalf("redelivery: inserted=%t err=%v", inserted, err)
	}

	n, err := s.CountWindow(ctx, group.ID, w)
	if err != nil || n != 2 {
		t.Fatalf("[start,end) holds %d", n)
	}

	if err := s.Edit(ctx, domain.MessageEdit{MessageID: 3, ChatID: group.ID, ContentType: "photo", Caption: "new caption", KeepText: true}); err != nil {
		t.Fatal(err)
	}
	if err := s.Edit(ctx, domain.MessageEdit{MessageID: 2, ChatID: group.ID, Text: "edited", ContentType: "text"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Edit(ctx, domain.MessageEdit{MessageID: 777, ChatID: group.ID, Text: "never stored", ContentType: "text"}); err != nil {
		t.Fatal(err)
	}

	list, err := s.ListWindow(ctx, group.ID, w)
	if err != nil || len(list) != 2 {
		t.Fatalf("list %v %v", list, err)
	}
	if list[0].Text != "edited" || list[1].Text != "photo caption" || list[1].Caption != "new caption" {
		t.Fatalf("texts %+v", list)
	}
	media := list[1].Media
	if media == nil || media.FileID != "F" || media.Width != 800 || media.Status != domain.MediaPending || media.FileSize != 1234 {
		t.Fatalf("media %+v", media)
	}
	if list[1].MediaStatus != domain.MediaPending || list[0].Media != nil {
		t.Fatalf("media status %+v", list)
	}

	// Retention removes the old message and keeps the rest.
	removed, err := s.DeleteOlderThan(ctx, w.Start)
	if err != nil || removed != 1 {
		t.Fatalf("retention removed %d %v", removed, err)
	}
	deleted, err := s.DeleteWindow(ctx, group.ID, w)
	if err != nil || deleted != 2 {
		t.Fatalf("delete window %d %v", deleted, err)
	}
	var rows int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM message_media`).Scan(&rows); err != nil || rows != 0 {
		t.Fatalf("media rows left behind: %d %v", rows, err)
	}
}

func TestReactions(t *testing.T) {
	s := migratedStore(t)
	ensureGroup(t, s)
	w := window()
	save(t, s, 10, w.Start.Add(time.Hour), "funny", nil)

	counts := func() map[string]int {
		list, err := s.ListWindow(ctx, group.ID, w)
		if err != nil || len(list) != 1 {
			t.Fatalf("list %v %v", list, err)
		}
		out := map[string]int{}
		for _, r := range list[0].Reactions {
			out[r.Emoji] = r.Count
		}
		return out
	}

	if err := s.SyncAbsolute(ctx, group.ID, 10, map[string]int{"👍": 3, "❤️": 1, "🔥": 0}); err != nil {
		t.Fatal(err)
	}
	if got := counts(); !reflect.DeepEqual(got, map[string]int{"👍": 3, "❤️": 1}) {
		t.Fatalf("sync %v", got)
	}
	if err := s.SyncAbsolute(ctx, group.ID, 10, map[string]int{"👍": 2}); err != nil {
		t.Fatal(err)
	}
	if got := counts(); !reflect.DeepEqual(got, map[string]int{"👍": 2}) {
		t.Fatalf("sync replaces: %v", got)
	}
	if err := s.ApplyDelta(ctx, group.ID, 10, map[string]int{"👍": -2, "🎉": 1, "💩": -1}); err != nil {
		t.Fatal(err)
	}
	if got := counts(); !reflect.DeepEqual(got, map[string]int{"🎉": 1}) {
		t.Fatalf("delta: %v", got)
	}
	if err := s.ApplyDelta(ctx, group.ID, 404, map[string]int{"🎉": 1}); err != nil {
		t.Fatalf("a message that is not stored is ignored: %v", err)
	}
}

func newQuote(day time.Time, text string) domain.NewQuote {
	return domain.NewQuote{
		GroupID: group.ID, AuthorID: alice.ID, Text: text, Score: 7.5, AIScore: 7.5, ContentType: "text",
		AIModel: "claude-opus-5-5", MessageID: 10, QuoteDay: day,
		WindowStart: window().Start, WindowEnd: window().End, DecisionStatus: domain.StatusPublishing,
		Context: []domain.ContextEntry{
			{MessageID: 9, Author: "Bob", Text: "setup <b>", IsPrimary: false},
			{MessageID: 10, Author: "Alice A", Text: text, IsPrimary: true},
		},
	}
}

func TestQuotes(t *testing.T) {
	s := migratedStore(t)
	ensureGroup(t, s)
	save(t, s, 10, window().Start.Add(time.Hour), "the line", nil)

	q, created, err := s.Create(ctx, newQuote(day0, "the line"))
	if err != nil || !created || q.ID == 0 {
		t.Fatalf("create: %+v %t %v", q, created, err)
	}
	if q.DecisionStatus != domain.StatusPublishing || q.StatusChangedAt.IsZero() || q.CreatedAt.IsZero() {
		t.Fatalf("stamps %+v", q)
	}
	if len(q.Context) != 2 || !q.Context[1].IsPrimary || q.Context[0].Text != "setup <b>" {
		t.Fatalf("context %+v", q.Context)
	}
	if !q.QuoteDay.Equal(day0) || !q.WindowStart.Equal(window().Start) {
		t.Fatalf("days %v %v", q.QuoteDay, q.WindowStart)
	}

	again, created, err := s.Create(ctx, newQuote(day0, "a different line"))
	if err != nil || created || again.ID != q.ID || again.Text != "the line" {
		t.Fatalf("second create for the day: %+v %t %v", again, created, err)
	}
	if got, err := s.ForDay(ctx, group.ID, day0.AddDate(0, 0, 1)); err != nil || got != nil {
		t.Fatalf("other day: %v %v", got, err)
	}

	// A single-message context stores nothing.
	single := newQuote(day0.AddDate(0, 0, 1), "alone")
	single.Context = single.Context[1:]
	sq, _, err := s.Create(ctx, single)
	if err != nil || len(sq.Context) != 0 {
		t.Fatalf("single context: %+v %v", sq.Context, err)
	}

	if err := s.MarkPublished(ctx, q.ID, 555, domain.ReasonWorthy); err != nil {
		t.Fatal(err)
	}
	got, _ := s.ForDay(ctx, group.ID, day0)
	if got.DecisionStatus != domain.StatusPublished || got.BotMessageID != 555 || got.DecisionReason != domain.ReasonWorthy {
		t.Fatalf("published %+v", got)
	}

	// Operation errors append, trimmed and capped.
	if err := s.AppendError(ctx, q.ID, "  first  "); err != nil {
		t.Fatal(err)
	}
	long := make([]rune, 400)
	for i := range long {
		long[i] = 'я'
	}
	if err := s.AppendError(ctx, q.ID, string(long)); err != nil {
		t.Fatal(err)
	}
	got, _ = s.ForDay(ctx, group.ID, day0)
	if want := "first | " + string(long[:250]); got.OperationError != want {
		t.Fatalf("operation error %q", got.OperationError)
	}

	if err := s.SetStatus(ctx, q.ID, domain.StatusPublishUnknown, domain.SetText("stale"), domain.SetNull()); err != nil {
		t.Fatal(err)
	}
	got, _ = s.ForDay(ctx, group.ID, day0)
	if got.DecisionStatus != domain.StatusPublishUnknown || got.DecisionReason != "stale" || got.OperationError != "" {
		t.Fatalf("set status %+v", got)
	}
	if err := s.SetStatus(ctx, q.ID, domain.StatusPublished, domain.TextPatch{}, domain.TextPatch{}); err != nil {
		t.Fatal(err)
	}
	got, _ = s.ForDay(ctx, group.ID, day0)
	if got.DecisionReason != "stale" {
		t.Fatalf("an unset patch must leave the column: %+v", got)
	}

	// Stale: only in-progress statuses older than the cut.
	if _, err := s.pool.Exec(ctx, `UPDATE quotes SET status_changed_at = now() - interval '1 hour' WHERE id = $1`, sq.ID); err != nil {
		t.Fatal(err)
	}
	stale, err := s.Stale(ctx, group.ID, time.Now().Add(-10*time.Minute))
	if err != nil || len(stale) != 1 || stale[0].ID != sq.ID {
		t.Fatalf("stale %+v %v", stale, err)
	}

	d, err := s.Detail(ctx, q.ID, true)
	if err != nil || d == nil || d.AuthorName != "Alice A" || d.GroupName != "Club" {
		t.Fatalf("detail %+v %v", d, err)
	}
	if hidden, err := s.Detail(ctx, sq.ID, true); err != nil || hidden != nil {
		t.Fatalf("an unpublished quote must be hidden: %+v %v", hidden, err)
	}
	if d, err := s.Detail(ctx, sq.ID, false); err != nil || d == nil {
		t.Fatalf("operator detail %+v %v", d, err)
	}

	if ok, err := s.Delete(ctx, q.ID); err != nil || !ok {
		t.Fatal(ok, err)
	}
	if ok, _ := s.Delete(ctx, q.ID); ok {
		t.Fatal("deleted twice")
	}
}

func TestMigrateChatID(t *testing.T) {
	s := migratedStore(t)
	ensureGroup(t, s)
	save(t, s, 10, window().Start.Add(time.Hour), "kept", nil)
	if _, _, err := s.Create(ctx, newQuote(day0, "kept")); err != nil {
		t.Fatal(err)
	}

	newChat := domain.ChatInfo{ID: -1009, Type: "supergroup", Title: "Club"}
	if ok, err := s.MigrateChatID(ctx, group.ID, newChat.ID, alice, newChat); err != nil || !ok {
		t.Fatalf("migrate: %t %v", ok, err)
	}
	if _, ok, _ := s.Get(ctx, group.ID); ok {
		t.Fatal("old id still has settings")
	}
	if _, ok, _ := s.Get(ctx, newChat.ID); !ok {
		t.Fatal("new id has no settings")
	}
	var msgs, quotes int
	_ = s.pool.QueryRow(ctx, `SELECT count(*) FROM messages WHERE chat_id = $1`, newChat.ID).Scan(&msgs)
	_ = s.pool.QueryRow(ctx, `SELECT count(*) FROM quotes WHERE group_id = $1`, newChat.ID).Scan(&quotes)
	if msgs != 1 || quotes != 1 {
		t.Fatalf("moved %d messages, %d quotes", msgs, quotes)
	}
	// Nothing to move the second time.
	if ok, _ := s.MigrateChatID(ctx, group.ID, newChat.ID, alice, newChat); ok {
		t.Fatal("migrated twice")
	}
}

func TestStats(t *testing.T) {
	s := migratedStore(t)
	ensureGroup(t, s)
	bob := domain.Person{ID: 12, FirstName: "Bob"}
	carol := domain.Person{ID: 13, Username: "carol"}
	for _, p := range []domain.Person{bob, carol} {
		if err := s.TouchUser(ctx, p); err != nil {
			t.Fatal(err)
		}
	}

	if st, err := s.Chat(ctx, -555); err != nil || st != nil {
		t.Fatalf("unknown group: %+v %v", st, err)
	}
	empty, err := s.Chat(ctx, group.ID)
	if err != nil || empty == nil || empty.TotalQuotes != 0 || empty.GroupName != "Club" {
		t.Fatalf("empty chat: %+v %v", empty, err)
	}
	if u, err := s.User(ctx, group.ID, alice.ID); err != nil || u == nil || u.Wins != 0 || u.UserName != "Alice A" {
		t.Fatalf("user without wins: %+v %v", u, err)
	}

	add := func(day int, author int64, score float64, status string) {
		q := newQuote(day0.AddDate(0, 0, day), "line")
		q.AuthorID, q.Score, q.DecisionStatus = author, score, status
		q.Text = "line " + string(rune('a'+day))
		if _, _, err := s.Create(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	add(0, alice.ID, 0.9, domain.StatusPublished)
	add(1, alice.ID, 0.7, domain.StatusPublished)
	add(2, bob.ID, 0.8, domain.StatusPublished)
	add(3, bob.ID, 0.6, domain.StatusPublished)
	add(4, carol.ID, 0.95, domain.StatusPublished)
	add(5, carol.ID, 1.0, domain.StatusPublishFailed) // never counts
	add(6, carol.ID, 1.0, domain.StatusSkippedBoring) // never counts

	st, err := s.Chat(ctx, group.ID)
	if err != nil {
		t.Fatal(err)
	}
	if st.TotalQuotes != 5 || st.UniqueAuthors != 3 || len(st.TopAuthors) != 3 {
		t.Fatalf("chat %+v", st)
	}
	// alice and bob both have two wins: alice's average is higher.
	if st.TopAuthors[0].Name != "Alice A" || st.TopAuthors[1].Name != "Bob" || st.TopAuthors[2].Name != "carol" {
		t.Fatalf("top %+v", st.TopAuthors)
	}
	if st.Best == nil || st.Best.Score != 0.95 || st.Best.Author != "carol" || st.Best.Text != "line e" {
		t.Fatalf("best %+v", st.Best)
	}

	// Equal wins share a rank, like the Python query (1 + authors with more).
	u, err := s.User(ctx, group.ID, bob.ID)
	if err != nil || u.Wins != 2 || u.Rank != 1 || u.TotalParticipants != 3 || u.Best == nil || u.Best.Score != 0.8 {
		t.Fatalf("bob %+v %v", u, err)
	}
	if u, _ := s.User(ctx, group.ID, carol.ID); u.Rank != 3 || u.Wins != 1 {
		t.Fatalf("carol %+v", u)
	}
	if u, _ := s.User(ctx, group.ID, alice.ID); u.Rank != 1 {
		t.Fatalf("alice %+v", u)
	}
}
