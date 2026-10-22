// SPDX-License-Identifier: Apache-2.0

package menu

import (
	"strconv"

	"github.com/FreshLabDev/quoto/internal/domain"
	"github.com/FreshLabDev/quoto/internal/i18n"
)

var medals = []string{"🥇", "🥈", "🥉"}

// anonymous is the name of a person core knows no name for, in the language of
// whoever is reading (F12).
func anonymous(lang, name string) string {
	if name == "" {
		return i18n.T(lang, "common.anonymous")
	}
	return name
}

// cut keeps the first n code points and marks a cut with an ellipsis. It is
// Python's text[:n] + "…" and not domain.Truncate: the stats tabs never trimmed
// the blanks around a quote or counted the ellipsis in the limit.
func cut(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n]) + "…"
}

// score shows a 0..1 score the way every card does: out of ten, one decimal.
func score(v float64) string { return i18n.Score(v * 10) }

// ChatStatsText is the chat tab of the stats screen: title, hint, and the
// numbers in one quote. Nil means the group has no settings row.
func ChatStatsText(lang string, s *domain.ChatStats) string {
	title, hint := i18n.T(lang, "stats.title"), i18n.T(lang, "stats.hint")
	if s == nil {
		return Panel(title, hint, i18n.T(lang, "stats.missing"))
	}
	if s.TotalQuotes == 0 {
		return Panel(title, hint, i18n.T(lang, "stats.empty"))
	}

	lines := []string{
		i18n.T(lang, "stats.total_quotes", "count", strconv.Itoa(s.TotalQuotes)),
		i18n.T(lang, "stats.unique_authors", "count", strconv.Itoa(s.UniqueAuthors)),
		i18n.T(lang, "stats.avg_score", "score", score(s.AvgScore)),
		"",
		i18n.T(lang, "stats.top_authors"),
	}
	for i, a := range s.TopAuthors {
		medal := strconv.Itoa(i+1) + "."
		if i < len(medals) {
			medal = medals[i]
		}
		lines = append(lines, i18n.T(lang, "stats.author_row",
			"medal", medal,
			"name", domain.EscapeHTML(anonymous(lang, a.Name)),
			"wins", strconv.Itoa(a.Wins),
			"score", score(a.AvgScore),
		))
	}
	if b := s.Best; b != nil {
		lines = append(lines,
			"",
			i18n.T(lang, "stats.best_quote"),
			"<i>«"+domain.EscapeHTML(cut(b.Text, 80))+"»</i>",
			"— "+domain.EscapeHTML(anonymous(lang, b.Author))+" · "+score(b.Score)+"/10",
		)
	}
	return Panel(title, hint, lines...)
}

// UserStatsText is the personal tab, in the same shape.
func UserStatsText(lang string, s *domain.UserStats) string {
	title, hint := i18n.T(lang, "user_stats.title"), i18n.T(lang, "user_stats.hint")
	if s == nil {
		return Panel(title, hint, i18n.T(lang, "user_stats.missing"))
	}
	user := domain.EscapeHTML(anonymous(lang, s.UserName))
	if s.Wins == 0 {
		return Panel(title, hint, i18n.T(lang, "user_stats.empty", "user", user))
	}

	lines := []string{
		i18n.T(lang, "user_stats.user", "user", user),
		i18n.T(lang, "user_stats.wins", "count", strconv.Itoa(s.Wins)),
		i18n.T(lang, "user_stats.avg_score", "score", score(s.AvgScore)),
		i18n.T(lang, "user_stats.rank", "rank", strconv.Itoa(s.Rank), "total", strconv.Itoa(s.TotalParticipants)),
	}
	if b := s.Best; b != nil {
		lines = append(lines,
			"",
			i18n.T(lang, "user_stats.best_quote"),
			"<i>«"+domain.EscapeHTML(cut(b.Text, 80))+"»</i> · "+score(b.Score)+"/10",
		)
	}
	return Panel(title, hint, lines...)
}
