// SPDX-License-Identifier: Apache-2.0

package menu

import (
	"strings"
	"testing"
	"time"

	"github.com/FreshLabDev/quoto/internal/domain"
	"github.com/FreshLabDev/quoto/internal/i18n"
)

func filledChatStats() *domain.ChatStats {
	return &domain.ChatStats{
		TotalQuotes: 12, UniqueAuthors: 4, AvgScore: 0.82,
		TopAuthors: []domain.AuthorWins{
			{Name: "Alice <b>", Wins: 5, AvgScore: 0.9},
			{Name: "Bob", Wins: 4, AvgScore: 0.8},
			{Name: "", Wins: 2, AvgScore: 0.75},
			{Name: "Dan", Wins: 1, AvgScore: 0.5},
		},
		Best: &domain.StatsQuote{Text: "a line & more", Score: 0.95, Author: "Alice", Date: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)},
	}
}

func filledUserStats() *domain.UserStats {
	return &domain.UserStats{
		UserName: "Alice", Wins: 3, AvgScore: 0.7, Rank: 2, TotalParticipants: 9,
		Best: &domain.StatsQuote{Text: "a line", Score: 0.9},
	}
}

// HND-27 (test_handlers.py:834, StatsPanelShapeTests): every state of both tabs, in
// every language, is a panel: a two-line head (bold, italic) and exactly one
// blockquote holding the rest. The states are the ones Python tried: no row,
// nothing yet, and filled.
func TestStatsPanelShape(t *testing.T) {
	chat := map[string]*domain.ChatStats{"missing": nil, "empty": {}, "filled": filledChatStats()}
	user := map[string]*domain.UserStats{"missing": nil, "empty": {UserName: "Alice"}, "filled": filledUserStats()}

	check := func(t *testing.T, text string) {
		t.Helper()
		head, body, ok := strings.Cut(text, "\n\n")
		if !ok {
			t.Fatalf("no blank line between the head and the body:\n%s", text)
		}
		lines := strings.Split(head, "\n")
		if len(lines) != 2 {
			t.Fatalf("head has %d lines:\n%s", len(lines), text)
		}
		if !strings.HasPrefix(lines[0], "<b>") || !strings.HasSuffix(lines[0], "</b>") {
			t.Fatalf("first line %q", lines[0])
		}
		if !strings.HasPrefix(lines[1], "<i>") || !strings.HasSuffix(lines[1], "</i>") {
			t.Fatalf("second line %q", lines[1])
		}
		if strings.Count(body, "<blockquote>") != 1 || !strings.HasPrefix(body, "<blockquote>") || !strings.HasSuffix(body, "</blockquote>") {
			t.Fatalf("body is not one blockquote:\n%s", body)
		}
		if strings.Contains(text, "{") || strings.Contains(text, "}") {
			t.Fatalf("an unfilled placeholder:\n%s", text)
		}
	}
	for _, lang := range i18n.Codes() {
		for state, s := range chat {
			t.Run(lang+"/chat/"+state, func(t *testing.T) { check(t, ChatStatsText(lang, s)) })
		}
		for state, s := range user {
			t.Run(lang+"/user/"+state, func(t *testing.T) { check(t, UserStatsText(lang, s)) })
		}
	}
}

// The filled tabs in English and Russian, character for character, as
// _format_chat_stats_text and _format_user_stats_text built them: medals for the
// first three, "4." after that, names escaped, a missing name in the reader's
// language, scores out of ten with one decimal.
func TestStatsGoldens(t *testing.T) {
	const chatEN = "<b>Chat stats</b>\n<i>What the whole chat has collected so far.</i>\n\n" +
		"<blockquote>Total quotes: <b>12</b>\nAuthors: <b>4</b>\nAverage rating: <b>8.2/10</b>\n\n" +
		"<b>Top authors</b>\n" +
		"🥇 <b>Alice &lt;b&gt;</b> — 5 · 9.0/10\n🥈 <b>Bob</b> — 4 · 8.0/10\n🥉 <b>Anonymous</b> — 2 · 7.5/10\n4. <b>Dan</b> — 1 · 5.0/10\n\n" +
		"<b>Best quote</b>\n<i>«a line &amp; more»</i>\n— Alice · 9.5/10</blockquote>"
	const chatRU = "<b>Статистика чата</b>\n<i>Что чат набрал за всё время.</i>\n\n" +
		"<blockquote>Всего цитат: <b>12</b>\nАвторов: <b>4</b>\nСредняя оценка: <b>8.2/10</b>\n\n" +
		"<b>Топ авторов</b>\n" +
		"🥇 <b>Alice &lt;b&gt;</b> — 5 · 9.0/10\n🥈 <b>Bob</b> — 4 · 8.0/10\n🥉 <b>Аноним</b> — 2 · 7.5/10\n4. <b>Dan</b> — 1 · 5.0/10\n\n" +
		"<b>Лучшая цитата</b>\n<i>«a line &amp; more»</i>\n— Alice · 9.5/10</blockquote>"
	const userEN = "<b>Your stats</b>\n<i>Your own quotes of the day in this chat.</i>\n\n" +
		"<blockquote><b>Alice</b>\nWins: <b>3</b>\nAverage rating: <b>7.0/10</b>\nRank: <b>2</b> of 9\n\n" +
		"<b>Best quote</b>\n<i>«a line»</i> · 9.0/10</blockquote>"
	const userRU = "<b>Твоя статистика</b>\n<i>Твои цитаты дня в этом чате.</i>\n\n" +
		"<blockquote><b>Alice</b>\nПобед: <b>3</b>\nСредняя оценка: <b>7.0/10</b>\nМесто: <b>2</b> из 9\n\n" +
		"<b>Лучшая цитата</b>\n<i>«a line»</i> · 9.0/10</blockquote>"

	for _, tt := range []struct{ name, got, want string }{
		{"chat en", ChatStatsText("en", filledChatStats()), chatEN},
		{"chat ru", ChatStatsText("ru", filledChatStats()), chatRU},
		{"user en", UserStatsText("en", filledUserStats()), userEN},
		{"user ru", UserStatsText("ru", filledUserStats()), userRU},
	} {
		if tt.got != tt.want {
			t.Errorf("%s:\n%s\nwant\n%s", tt.name, tt.got, tt.want)
		}
	}
}

// The three smaller states in English.
func TestStatsEmptyStates(t *testing.T) {
	head := "<b>Chat stats</b>\n<i>What the whole chat has collected so far.</i>\n\n"
	if got, want := ChatStatsText("en", nil), head+"<blockquote>Stats are not available yet — this group is not registered.</blockquote>"; got != want {
		t.Errorf("no row:\n%s", got)
	}
	if got, want := ChatStatsText("en", &domain.ChatStats{}), head+"<blockquote>This chat has no published quotes yet.</blockquote>"; got != want {
		t.Errorf("no quotes:\n%s", got)
	}
	userHead := "<b>Your stats</b>\n<i>Your own quotes of the day in this chat.</i>\n\n"
	if got, want := UserStatsText("en", nil), userHead+"<blockquote>Stats are not available yet.</blockquote>"; got != want {
		t.Errorf("no row:\n%s", got)
	}
	got := UserStatsText("en", &domain.UserStats{UserName: "Al<i>"})
	if !strings.Contains(got, "<b>Al&lt;i&gt;</b>, you have no published quotes in this chat yet.") {
		t.Errorf("no wins:\n%s", got)
	}
	// A person core knows no name for is "Anonymous", in the reader's language.
	if got := UserStatsText("ru", &domain.UserStats{Wins: 1, Rank: 1, TotalParticipants: 1}); !strings.Contains(got, "<b>Аноним</b>") {
		t.Errorf("anonymous person:\n%s", got)
	}
}

// The best quote is cut at eighty characters (not bytes) with an ellipsis, and the
// cut is not trimmed; eighty exactly is left alone.
func TestStatsBestQuoteIsCutAtEighty(t *testing.T) {
	for _, tt := range []struct {
		n   int
		cut bool
	}{{79, false}, {80, false}, {81, true}} {
		text := strings.Repeat("я", tt.n)
		s := filledChatStats()
		s.Best.Text = text
		got := ChatStatsText("en", s)
		want := "«" + strings.Repeat("я", min(tt.n, 80))
		if tt.cut {
			want += "…"
		}
		want += "»"
		if !strings.Contains(got, want) {
			t.Errorf("%d characters: no %q in\n%s", tt.n, want, got)
		}
		u := filledUserStats()
		u.Best.Text = text
		if !strings.Contains(UserStatsText("en", u), want) {
			t.Errorf("%d characters: user tab lacks %q", tt.n, want)
		}
	}
	// A space at the cut stays: Python did s[:80] and nothing else.
	s := filledChatStats()
	s.Best.Text = strings.Repeat("a", 79) + " b"
	if got := ChatStatsText("en", s); !strings.Contains(got, strings.Repeat("a", 79)+" …»") {
		t.Errorf("the cut was trimmed:\n%s", got)
	}
}

// Statistics without a best quote, and a group with more than three top authors,
// are both shapes the store returns.
func TestStatsWithoutABestQuote(t *testing.T) {
	s := filledChatStats()
	s.Best = nil
	got := ChatStatsText("en", s)
	if strings.Contains(got, "Best quote") || strings.Count(got, "<blockquote>") != 1 {
		t.Errorf("chat:\n%s", got)
	}
	u := filledUserStats()
	u.Best = nil
	if got := UserStatsText("en", u); strings.Contains(got, "Best quote") {
		t.Errorf("user:\n%s", got)
	}
}
