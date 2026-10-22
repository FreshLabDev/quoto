// SPDX-License-Identifier: Apache-2.0

package bot

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/FreshLabDev/quoto/internal/domain"
	"github.com/FreshLabDev/quoto/internal/i18n"
)

// barWidth is the width of the score bar.
const barWidth = 6

// bar draws a score in [0, 1] as six circles. It is the Python formula, whose
// two integer steps decide which bars show a circle more or less:
// int(int(x*100)/100*6) filled circles.
func bar(score float64) string {
	filled := int(float64(int(score*100)) / 100 * barWidth)
	filled = max(0, min(filled, barWidth))
	return strings.Repeat("●", filled) + strings.Repeat("○", barWidth-filled)
}

// padRight pads s with spaces to n characters, counting characters and not bytes.
func padRight(s string, n int) string {
	if gap := n - domain.RuneLen(s); gap > 0 {
		return s + strings.Repeat(" ", gap)
	}
	return s
}

// cutRunes is s[:n] in characters, with an ellipsis when it was longer.
func cutRunes(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

// tmeLinkChat turns a chat id into the part of a t.me/c/ link. Only a supergroup
// or channel (-100 prefix) has such a link; the Python version built a broken one
// for any other id.
func tmeLinkChat(chatID int64) (string, bool) {
	s := strconv.FormatInt(chatID, 10)
	if rest, ok := strings.CutPrefix(s, "-100"); ok && rest != "" {
		return rest, true
	}
	return "", false
}

// quoteCard is the details card a signed link opens in a private chat. It is
// HTML, so every name and text from the chat is escaped. Per doc 7.5 S1 it shows
// the total and the model's own bar; the Reactions and Length bars are gone, the
// heart count stays.
func quoteCard(lang string, d *domain.QuoteDetail) string {
	esc := domain.EscapeHTML
	anonymous := i18n.T(lang, "common.anonymous")

	var quote string
	if len(d.Context) >= 2 {
		lines := make([]string, len(d.Context))
		for i, e := range d.Context {
			author := e.Author
			if author == "" {
				author = anonymous
			}
			if e.IsPrimary {
				lines[i] = "<b>" + esc(author) + ":</b> <i>«" + esc(e.Text) + "»</i>"
			} else {
				lines[i] = "<b>" + esc(author) + ":</b> " + esc(e.Text)
			}
		}
		quote = "<blockquote>" + strings.Join(lines, "\n") + "</blockquote>"
	} else {
		author := d.AuthorName
		if author == "" {
			author = anonymous
		}
		quote = "<blockquote><i>«" + esc(d.Text) + "»</i>\n— <b>" + esc(author) + "</b></blockquote>"
	}

	groupName := d.GroupName
	if groupName == "" {
		groupName = "—"
	}
	date := i18n.FormatDate(lang, d.CreatedAt.UTC(), true)

	var sb strings.Builder
	sb.WriteString(i18n.T(lang, "details.title", "id", strconv.FormatInt(d.ID, 10)))
	sb.WriteString("\n" + i18n.T(lang, "status.published") + "\n\n")
	sb.WriteString(quote + "\n\n")

	if linkChat, ok := tmeLinkChat(d.GroupID); ok && d.MessageID != 0 {
		hearts := ""
		if d.ReactionCount > 0 {
			hearts = fmt.Sprintf(" · %d ❤️", d.ReactionCount)
		}
		fmt.Fprintf(&sb, "<a href='https://t.me/c/%s/%d'>%s</a>%s · %s\n\n", linkChat, d.MessageID, esc(groupName), hearts, date)
	} else {
		sb.WriteString(esc(groupName) + " · " + date + "\n\n")
	}

	model := d.AIModel
	if model == "" {
		model = "AI"
	}
	if i := strings.LastIndex(model, "/"); i >= 0 {
		model = model[i+1:]
	}
	fmt.Fprintf(&sb, "<b>%s: %s/10</b>\n", i18n.T(lang, "details.total"), i18n.Score(d.Score*10))
	fmt.Fprintf(&sb, "<code>%s %s</code> %s/10\n", padRight(esc(model), 10), bar(d.AIScore), i18n.Score(d.AIScore*10))

	if d.DecisionReason != "" {
		fmt.Fprintf(&sb, "\n<b>%s:</b> <i>%s</i>\n", i18n.T(lang, "details.decision_reason"), esc(d.DecisionReason))
	}
	if d.AIBestText != "" {
		fmt.Fprintf(&sb, "<b>%s:</b> <i>«%s»</i>\n", i18n.T(lang, "details.ai_choice"), esc(cutRunes(d.AIBestText, 100)))
	}
	return sb.String()
}
