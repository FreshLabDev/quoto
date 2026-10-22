// SPDX-License-Identifier: Apache-2.0

package scheduler

import (
	"strconv"
	"strings"

	"github.com/FreshLabDev/quoto/internal/domain"
	"github.com/FreshLabDev/quoto/internal/i18n"
)

// mediaCaptionLimit is Telegram's caption limit. It is counted in code points
// over the whole HTML text, tags included, which is stricter than Telegram's
// own count and what the Python version did.
const mediaCaptionLimit = 1024

// captionBodyLimits are the quote lengths tried, longest first, when a media
// caption is over the limit. The context is dropped on the first of them.
var captionBodyLimits = [...]int{320, 220, 140, 80}

const (
	// mediaCaptionQuoteLimit cuts the quote of a media caption before the
	// caption as a whole is measured.
	mediaCaptionQuoteLimit = 520
	// mediaCaptionContextLimit cuts each line of the context in a media caption.
	mediaCaptionContextLimit = 140
)

// mediaCopyTypes are the content types the bot publishes by copying the
// original message under the caption instead of posting text.
var mediaCopyTypes = map[string]bool{
	domain.ContentPhoto:     true,
	domain.ContentImage:     true,
	domain.ContentVideo:     true,
	domain.ContentAnimation: true,
	domain.ContentVideoNote: true,
	domain.ContentVoice:     true,
	domain.ContentAudio:     true,
}

// canCopyMedia says the quote is published as a copy of the original message.
func canCopyMedia(q domain.Quote) bool {
	return mediaCopyTypes[contentType(q)] && q.MessageID != 0
}

func contentType(q domain.Quote) string {
	if q.ContentType == "" {
		return domain.ContentText
	}
	return q.ContentType
}

// postView is what a quote post is built from.
type postView struct {
	Lang  string
	Quote domain.Quote
	// Author is the name of the quoted person, already localised when unknown.
	Author string
	// Stars and ReactionCount make the info line.
	Stars         string
	ReactionCount int
	MsgLink       string
	DetailsLink   string
	// Note is a line of interface text under the info line, not escaped. The
	// caption of a media post never carries it.
	Note string
}

// contextLine is one line of a quote's context as the post prints it.
type contextLine struct {
	Author  string
	Text    string
	Primary bool
}

// contextLines reads the quote's stored context. The context counts only when
// at least two lines have text, a single one is the quote itself. A line
// without an author gets "Anonymous" in the post's language.
func contextLines(q domain.Quote, lang string) []contextLine {
	var lines []contextLine
	for _, e := range q.Context {
		text := domain.PyStrip(e.Text)
		if text == "" {
			continue
		}
		author := e.Author
		if author == "" {
			author = i18n.T(lang, "common.anonymous")
		}
		lines = append(lines, contextLine{Author: author, Text: text, Primary: e.IsPrimary})
	}
	if len(lines) <= 1 {
		return nil
	}
	return lines
}

// postText builds the quote post: title, the quote in a blockquote, the info
// line, an optional note and the footer with the two links, separated by blank
// lines. With mediaCaption it builds the caption of a copied media message and
// shortens it until it fits.
func postText(v postView, mediaCaption bool) string {
	q := v.Quote
	safeAuthor := domain.EscapeHTML(v.Author)
	lines := contextLines(q, v.Lang)

	info := infoLine(v)
	footer := "<a href='" + v.MsgLink + "'>" + i18n.T(v.Lang, "common.original") + "</a> · " +
		"<a href='" + v.DetailsLink + "'>" + i18n.T(v.Lang, "common.details") + "</a> · #quoto"

	compose := func(body string) string {
		parts := []string{quoteTitle(v.Lang, contentType(q)), "<blockquote>" + body + "</blockquote>", info}
		if v.Note != "" && !mediaCaption {
			parts = append(parts, "<i>"+v.Note+"</i>")
		}
		parts = append(parts, footer)
		return strings.Join(parts, "\n\n")
	}

	var body string
	switch {
	case len(lines) > 0 && !mediaCaption:
		body = contextBody(lines, 0)
	case len(lines) > 0:
		body = contextBody(lines, mediaCaptionContextLimit)
	default:
		body = quoteBody(displayText(q.Text, mediaCaption), safeAuthor)
	}

	text := compose(body)
	if !mediaCaption || domain.RuneLen(text) <= mediaCaptionLimit {
		return text
	}
	for _, limit := range captionBodyLimits {
		text = compose(quoteBody(domain.Truncate(q.Text, limit), safeAuthor))
		if domain.RuneLen(text) <= mediaCaptionLimit {
			return text
		}
	}
	return compose("— <b>" + safeAuthor + "</b>")
}

func quoteBody(text, safeAuthor string) string {
	return "<i>«" + domain.EscapeHTML(text) + "»</i>\n— <b>" + safeAuthor + "</b>"
}

// displayText is the quote's text for the body: trimmed, and for a media caption
// cut to mediaCaptionQuoteLimit.
func displayText(text string, mediaCaption bool) string {
	if !mediaCaption {
		return domain.PyStrip(text)
	}
	return domain.Truncate(text, mediaCaptionQuoteLimit)
}

// contextBody prints the context as a dialogue: the quoted line in italics
// inside guillemets, the others as they are. textLimit 0 means no cut.
func contextBody(lines []contextLine, textLimit int) string {
	rendered := make([]string, 0, len(lines))
	for _, l := range lines {
		text := l.Text
		if textLimit > 0 {
			text = domain.Truncate(text, textLimit)
		}
		author := domain.EscapeHTML(l.Author)
		text = domain.EscapeHTML(text)
		if l.Primary {
			rendered = append(rendered, "<b>"+author+":</b> <i>«"+text+"»</i>")
		} else {
			rendered = append(rendered, "<b>"+author+":</b> "+text)
		}
	}
	return strings.Join(rendered, "\n")
}

// infoLine is "★★★☆☆ · 3 ❤️ · 21 March": stars, the number of reactions of any
// emoji when there are some, and the day of the window's closing cutoff.
func infoLine(v postView) string {
	parts := []string{"<i>" + v.Stars + "</i>"}
	if v.ReactionCount > 0 {
		parts = append(parts, strconv.Itoa(v.ReactionCount)+" ❤️")
	}
	parts = append(parts, i18n.FormatDate(v.Lang, v.Quote.QuoteDay, false))
	return strings.Join(parts, " · ")
}

// quoteTitle is the heading by content type: photos and images, the three video
// kinds, voice and audio have their own; everything else, stickers included,
// is a quote.
func quoteTitle(lang, contentType string) string {
	switch contentType {
	case domain.ContentPhoto, domain.ContentImage:
		return i18n.T(lang, "quote_post.titles.photo")
	case domain.ContentVideo, domain.ContentAnimation, domain.ContentVideoNote:
		return i18n.T(lang, "quote_post.titles.video")
	case domain.ContentVoice:
		return i18n.T(lang, "quote_post.titles.voice")
	case domain.ContentAudio:
		return i18n.T(lang, "quote_post.titles.audio")
	}
	return i18n.T(lang, "quote_post.titles.text")
}

// boringText is the notice that replaces the quote on a day with nothing worth
// quoting. The model's reason, when it gave one, goes in a blockquote.
func boringText(lang, detailsLink, reason string) string {
	var b strings.Builder
	b.WriteString(i18n.T(lang, "boring.title"))
	b.WriteString("\n\n<i>")
	b.WriteString(i18n.T(lang, "boring.body"))
	b.WriteString("</i>")
	if reason != "" {
		b.WriteString("\n\n<blockquote><i>")
		b.WriteString(domain.EscapeHTML(reason))
		b.WriteString("</i></blockquote>")
	}
	b.WriteString("\n\n<a href='")
	b.WriteString(detailsLink)
	b.WriteString("'>")
	b.WriteString(i18n.T(lang, "common.details"))
	b.WriteString("</a> · #quoto")
	return b.String()
}

// messageLink is the link to the original message. Supergroup ids carry a -100
// prefix that t.me/c/ links leave out; any other id is used as it is.
func messageLink(chatID, messageID int64) string {
	id := strconv.FormatInt(chatID, 10)
	id = strings.TrimPrefix(id, "-100")
	return "https://t.me/c/" + id + "/" + strconv.FormatInt(messageID, 10)
}
