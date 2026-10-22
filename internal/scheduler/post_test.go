// SPDX-License-Identifier: Apache-2.0

package scheduler

import (
	"strings"
	"testing"
	"time"

	"github.com/FreshLabDev/quoto/internal/domain"
)

const (
	testMsgLink     = "https://t.me/c/1234567890/110"
	testDetailsLink = "https://t.me/quoto_test_bot?start=quote_1_0123456789abcdef"
)

func baseView(lang string) postView {
	return postView{
		Lang: lang,
		Quote: domain.Quote{
			ID: 1, Text: "line 10", ContentType: domain.ContentText, MessageID: 110,
			QuoteDay: time.Date(2026, 3, 27, 0, 0, 0, 0, time.UTC),
		},
		Author:      "User 10",
		Stars:       "★★★★★",
		MsgLink:     testMsgLink,
		DetailsLink: testDetailsLink,
	}
}

// doc inventory 03 §1.6, §6 Formats: the post, golden strings in two languages.
func TestPostTextGolden(t *testing.T) {
	ctx := []domain.ContextEntry{
		{MessageID: 109, Author: "Alice <A>", Text: "setup & context"},
		{MessageID: 110, Author: "Bob", Text: "punch <line>", IsPrimary: true},
	}
	tests := []struct {
		name string
		view func() postView
		want string
	}{
		{"en, a plain quote", func() postView { return baseView("en") },
			"🏆 <b>Quote of the Day</b>\n\n" +
				"<blockquote><i>«line 10»</i>\n— <b>User 10</b></blockquote>\n\n" +
				"<i>★★★★★</i> · 27 March\n\n" +
				"<a href='" + testMsgLink + "'>Original</a> · <a href='" + testDetailsLink + "'>Details</a> · #quoto"},
		{"ru, a plain quote", func() postView { return baseView("ru") },
			"🏆 <b>Цитата дня</b>\n\n" +
				"<blockquote><i>«line 10»</i>\n— <b>User 10</b></blockquote>\n\n" +
				"<i>★★★★★</i> · 27 марта\n\n" +
				"<a href='" + testMsgLink + "'>Оригинал</a> · <a href='" + testDetailsLink + "'>Подробнее</a> · #quoto"},
		{"reactions and a note", func() postView {
			v := baseView("en")
			v.Stars, v.ReactionCount = "★★★☆☆", 4
			v.Note = "ℹ️ Counted the last 1500 of 1800 messages — context trimmed due to volume."
			return v
		},
			"🏆 <b>Quote of the Day</b>\n\n" +
				"<blockquote><i>«line 10»</i>\n— <b>User 10</b></blockquote>\n\n" +
				"<i>★★★☆☆</i> · 4 ❤️ · 27 March\n\n" +
				"<i>ℹ️ Counted the last 1500 of 1800 messages — context trimmed due to volume.</i>\n\n" +
				"<a href='" + testMsgLink + "'>Original</a> · <a href='" + testDetailsLink + "'>Details</a> · #quoto"},
		// tests/test_scheduler.py::test_publish_quote_message_escapes_quote_and_author_html (SCH-25)
		{"HTML in the text and the author is escaped", func() postView {
			v := baseView("en")
			v.Quote.Text, v.Author = `1 < 2 & 3 "q" 'a'`, "Alice & Bob"
			return v
		},
			"🏆 <b>Quote of the Day</b>\n\n" +
				"<blockquote><i>«1 &lt; 2 &amp; 3 &quot;q&quot; &#x27;a&#x27;»</i>\n— <b>Alice &amp; Bob</b></blockquote>\n\n" +
				"<i>★★★★★</i> · 27 March\n\n" +
				"<a href='" + testMsgLink + "'>Original</a> · <a href='" + testDetailsLink + "'>Details</a> · #quoto"},
		{"the text is trimmed", func() postView {
			v := baseView("en")
			v.Quote.Text = "  \n spaced out \t"
			return v
		},
			"🏆 <b>Quote of the Day</b>\n\n" +
				"<blockquote><i>«spaced out»</i>\n— <b>User 10</b></blockquote>\n\n" +
				"<i>★★★★★</i> · 27 March\n\n" +
				"<a href='" + testMsgLink + "'>Original</a> · <a href='" + testDetailsLink + "'>Details</a> · #quoto"},
		// tests/test_scheduler.py::test_publish_quote_message_renders_context_dialog (SCH-29)
		{"a context dialogue", func() postView {
			v := baseView("en")
			v.Quote.Context = ctx
			return v
		},
			"🏆 <b>Quote of the Day</b>\n\n" +
				"<blockquote><b>Alice &lt;A&gt;:</b> setup &amp; context\n<b>Bob:</b> <i>«punch &lt;line&gt;»</i></blockquote>\n\n" +
				"<i>★★★★★</i> · 27 March\n\n" +
				"<a href='" + testMsgLink + "'>Original</a> · <a href='" + testDetailsLink + "'>Details</a> · #quoto"},
		{"one line of context is the quote itself", func() postView {
			v := baseView("en")
			v.Quote.Context = ctx[1:]
			return v
		},
			"🏆 <b>Quote of the Day</b>\n\n" +
				"<blockquote><i>«line 10»</i>\n— <b>User 10</b></blockquote>\n\n" +
				"<i>★★★★★</i> · 27 March\n\n" +
				"<a href='" + testMsgLink + "'>Original</a> · <a href='" + testDetailsLink + "'>Details</a> · #quoto"},
		{"empty context lines are not counted", func() postView {
			v := baseView("en")
			v.Quote.Context = []domain.ContextEntry{ctx[0], {MessageID: 1, Author: "X", Text: " \n "}, ctx[1]}
			return v
		},
			"🏆 <b>Quote of the Day</b>\n\n" +
				"<blockquote><b>Alice &lt;A&gt;:</b> setup &amp; context\n<b>Bob:</b> <i>«punch &lt;line&gt;»</i></blockquote>\n\n" +
				"<i>★★★★★</i> · 27 March\n\n" +
				"<a href='" + testMsgLink + "'>Original</a> · <a href='" + testDetailsLink + "'>Details</a> · #quoto"},
		// doc §7.2 F12: a context line without an author reads "Anonymous" in the group's language.
		{"an anonymous context author, ru", func() postView {
			v := baseView("ru")
			v.Quote.Context = []domain.ContextEntry{{MessageID: 109, Text: "раз"}, {MessageID: 110, Author: "Боб", Text: "два", IsPrimary: true}}
			return v
		},
			"🏆 <b>Цитата дня</b>\n\n" +
				"<blockquote><b>Аноним:</b> раз\n<b>Боб:</b> <i>«два»</i></blockquote>\n\n" +
				"<i>★★★★★</i> · 27 марта\n\n" +
				"<a href='" + testMsgLink + "'>Оригинал</a> · <a href='" + testDetailsLink + "'>Подробнее</a> · #quoto"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := postText(tt.view(), false); got != tt.want {
				t.Errorf("postText:\n got %q\nwant %q", got, tt.want)
			}
		})
	}
}

// Day of the quote in the info line: any language, with the locale's own order.
func TestPostTextDateByLanguage(t *testing.T) {
	tests := map[string]string{"en": "27 March", "ru": "27 марта", "uk": "27 березня", "de": "27. März"}
	for lang, want := range tests {
		v := baseView(lang)
		if got := infoLine(v); !strings.HasSuffix(got, want) {
			t.Errorf("%s: info line %q does not end with %q", lang, got, want)
		}
	}
}

// doc inventory 03 §1.6: title by content type.
func TestQuoteTitle(t *testing.T) {
	tests := []struct{ kind, want string }{
		{"text", "🏆 <b>Quote of the Day</b>"},
		{"", "🏆 <b>Quote of the Day</b>"},
		{"photo", "🏆 <b>Photo of the Day</b>"},
		{"image", "🏆 <b>Photo of the Day</b>"},
		{"video", "🏆 <b>Video of the Day</b>"},
		{"animation", "🏆 <b>Video of the Day</b>"},
		{"video_note", "🏆 <b>Video of the Day</b>"},
		{"voice", "🏆 <b>Voice of the Day</b>"},
		{"audio", "🏆 <b>Audio of the Day</b>"},
		{"sticker", "🏆 <b>Quote of the Day</b>"},
		{"document", "🏆 <b>Quote of the Day</b>"},
		{"unknown", "🏆 <b>Quote of the Day</b>"},
	}
	for _, tt := range tests {
		if got := quoteTitle("en", tt.kind); got != tt.want {
			t.Errorf("quoteTitle(%q) = %q, want %q", tt.kind, got, tt.want)
		}
	}
	if got := quoteTitle("ru", "voice"); got != "🏆 <b>Голос дня</b>" {
		t.Errorf("ru voice = %q", got)
	}
}

// doc inventory 03 §1.6: the copied media is chosen by content type and needs an original.
func TestCanCopyMedia(t *testing.T) {
	for _, kind := range []string{"photo", "image", "video", "animation", "video_note", "voice", "audio"} {
		if !canCopyMedia(domain.Quote{ContentType: kind, MessageID: 5}) {
			t.Errorf("%s should be copied", kind)
		}
		if canCopyMedia(domain.Quote{ContentType: kind}) {
			t.Errorf("%s without an original message cannot be copied", kind)
		}
	}
	for _, kind := range []string{"text", "", "sticker", "document", "unknown"} {
		if canCopyMedia(domain.Quote{ContentType: kind, MessageID: 5}) {
			t.Errorf("%q is posted as text", kind)
		}
	}
}

// The caption of a copied media message: no note, the context cut to 140
// characters a line, and shortened until it fits 1024 code points counted with
// every tag (doc inventory 03 §1.6, steps 320, 220, 140, 80).
func TestMediaCaptionShortening(t *testing.T) {
	longText := strings.Repeat("я", 2000) // multi-byte: the limit counts code points

	t.Run("a note never goes into a caption", func(t *testing.T) {
		v := baseView("en")
		v.Quote.ContentType = domain.ContentPhoto
		v.Note = "ℹ️ a note"
		if got := postText(v, true); strings.Contains(got, "a note") {
			t.Errorf("caption has the note: %q", got)
		}
		if got := postText(v, false); !strings.Contains(got, "<i>ℹ️ a note</i>") {
			t.Errorf("the text post lost the note: %q", got)
		}
	})

	t.Run("a long quote is cut to 520 before measuring", func(t *testing.T) {
		v := baseView("en")
		v.Quote.ContentType, v.Quote.Text = domain.ContentPhoto, longText
		got := postText(v, true)
		quote := between(got, "<i>«", "»</i>")
		if n := domain.RuneLen(quote); n != 520 || !strings.HasSuffix(quote, "…") {
			t.Errorf("quote has %d runes (%q…), want 520 ending in an ellipsis", n, quote[:10])
		}
		if domain.RuneLen(got) > mediaCaptionLimit {
			t.Errorf("caption is %d runes", domain.RuneLen(got))
		}
	})

	t.Run("context lines are cut to 140", func(t *testing.T) {
		v := baseView("en")
		v.Quote.ContentType = domain.ContentPhoto
		v.Quote.Context = []domain.ContextEntry{
			{MessageID: 1, Author: "A", Text: strings.Repeat("x", 300)},
			{MessageID: 2, Author: "B", Text: "short", IsPrimary: true},
		}
		got := postText(v, true)
		first := strings.SplitN(between(got, "<blockquote>", "</blockquote>"), "\n", 2)[0]
		if want := "<b>A:</b> " + strings.Repeat("x", 139) + "…"; first != want {
			t.Errorf("first line = %q, want %q", first, want)
		}
	})

	// Shortening steps: with an author name of length L the caption is
	// base(L)+limit runes for the first limit that fits. The L below put each step
	// exactly on the 1024 boundary, and one more makes the author-only form.
	emptyAuthor := func(author string) postView {
		v := baseView("en")
		v.Quote.ContentType, v.Quote.Text, v.Author = domain.ContentPhoto, longText, author
		return v
	}
	probe := emptyAuthor("")
	probe.Quote.Text = ""
	overhead := domain.RuneLen(postText(probe, true)) // with no text and no author
	steps := []struct {
		name     string
		authorLn int
		want     int // runes in the quote, 0 for the author-only form
	}{
		{"320 fits exactly", mediaCaptionLimit - 320 - overhead, 320},
		{"320 is one too long, 220 fits", mediaCaptionLimit - 320 - overhead + 1, 220},
		{"220 fits exactly", mediaCaptionLimit - 220 - overhead, 220},
		{"220 is one too long, 140 fits", mediaCaptionLimit - 220 - overhead + 1, 140},
		{"140 fits exactly", mediaCaptionLimit - 140 - overhead, 140},
		{"140 is one too long, 80 fits", mediaCaptionLimit - 140 - overhead + 1, 80},
		{"80 fits exactly", mediaCaptionLimit - 80 - overhead, 80},
		{"nothing fits: the author alone", mediaCaptionLimit - 80 - overhead + 1, 0},
	}
	for _, tt := range steps {
		t.Run(tt.name, func(t *testing.T) {
			got := postText(emptyAuthor(strings.Repeat("a", tt.authorLn)), true)
			if tt.want == 0 {
				if strings.Contains(got, "«") {
					t.Errorf("the quote should be gone: %q", got[:60])
				}
				if !strings.Contains(got, "<blockquote>— <b>aaa") {
					t.Errorf("author-only form missing: %q", got[:80])
				}
				return
			}
			quote := between(got, "<i>«", "»</i>")
			if n := domain.RuneLen(quote); n != tt.want {
				t.Errorf("quote has %d runes, want %d", n, tt.want)
			}
			if n := domain.RuneLen(got); n > mediaCaptionLimit {
				t.Errorf("caption is %d runes, over %d", n, mediaCaptionLimit)
			}
		})
	}
}

func between(s, open, closing string) string {
	i := strings.Index(s, open)
	if i < 0 {
		return ""
	}
	s = s[i+len(open):]
	j := strings.Index(s, closing)
	if j < 0 {
		return s
	}
	return s[:j]
}

// doc inventory 03 §1.7: the boring-day notice, golden.
func TestBoringTextGolden(t *testing.T) {
	tests := []struct {
		name, lang, reason, want string
	}{
		{"en without a reason", "en", "",
			"😴 <b>No quote of the day today</b>\n\n" +
				"<i>I didn't find a line worth the day. See you tomorrow!</i>\n\n" +
				"<a href='" + testDetailsLink + "'>Details</a> · #quoto"},
		{"ru with a reason", "ru", "день как день",
			"😴 <b>Сегодня без цитаты дня</b>\n\n" +
				"<i>Я не нашёл фразы, достойной дня. До завтра!</i>\n\n" +
				"<blockquote><i>день как день</i></blockquote>\n\n" +
				"<a href='" + testDetailsLink + "'>Подробнее</a> · #quoto"},
		// tests/test_scheduler.py::test_send_boring_notice_escapes_ai_reason_html (SCH-24)
		{"the reason is escaped", "en", "LLM says <boring> & unstable",
			"😴 <b>No quote of the day today</b>\n\n" +
				"<i>I didn't find a line worth the day. See you tomorrow!</i>\n\n" +
				"<blockquote><i>LLM says &lt;boring&gt; &amp; unstable</i></blockquote>\n\n" +
				"<a href='" + testDetailsLink + "'>Details</a> · #quoto"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := boringText(tt.lang, testDetailsLink, tt.reason); got != tt.want {
				t.Errorf("boringText:\n got %q\nwant %q", got, tt.want)
			}
		})
	}
}

// doc inventory 03 §1.6: the -100 prefix of a supergroup is left out of t.me/c links.
func TestMessageLink(t *testing.T) {
	tests := []struct {
		chat, msg int64
		want      string
	}{
		{-1001234567890, 57, "https://t.me/c/1234567890/57"},
		{-12345, 9, "https://t.me/c/-12345/9"},
		{777, 3, "https://t.me/c/777/3"},
		{-1009999999999, 1, "https://t.me/c/9999999999/1"},
	}
	for _, tt := range tests {
		if got := messageLink(tt.chat, tt.msg); got != tt.want {
			t.Errorf("messageLink(%d, %d) = %q, want %q", tt.chat, tt.msg, got, tt.want)
		}
	}
}

func TestCutRunes(t *testing.T) {
	tests := []struct {
		in   string
		n    int
		want string
	}{
		{"hello", 3, "hel"},
		{"hello", 5, "hello"},
		{"hello", 99, "hello"},
		{"привет", 3, "при"},
		{"a😂b", 2, "a😂"},
		{"abc", 0, ""},
		{"", 5, ""},
	}
	for _, tt := range tests {
		if got := cutRunes(tt.in, tt.n); got != tt.want {
			t.Errorf("cutRunes(%q, %d) = %q, want %q", tt.in, tt.n, got, tt.want)
		}
	}
}
