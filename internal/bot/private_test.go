// SPDX-License-Identifier: Apache-2.0

package bot

import (
	"strings"
	"testing"
	"time"

	"github.com/FreshLabDev/tg"

	"github.com/FreshLabDev/quoto/internal/domain"
	"github.com/FreshLabDev/quoto/internal/i18n"
	"github.com/FreshLabDev/quoto/internal/menu"
)

func privateMessage(from tg.User, text string) tg.Message {
	return tg.Message{
		MessageID: 10, Date: 1700000000, From: &from,
		Chat: tg.Chat{ID: from.ID, Type: tg.ChatPrivate}, Text: text,
	}
}

// HND-02, HND-03 (test_handlers.py:99, :136): the language of a private chat is
// the Telegram client's, an unknown one is English, and a language somebody
// chose (any bot, through core) wins over the client's.
func TestPrivateLanguage(t *testing.T) {
	tests := []struct {
		name   string
		client string
		claim  string
		lang   string
	}{
		{"client language with a region", "en-US", "", "en"},
		{"unknown client language", "ko", "", "en"},
		{"no client language", "", "", "en"},
		{"russian client", "ru", "", "ru"},
		{"a claim wins over the client", "ru", "de", "de"},
		{"a claim that cannot be rendered is english", "ru", "xx", "en"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t)
			if tt.claim != "" {
				h.store.claims[claimKey{domain.ScopeUser, ownerID}] = domain.LangClaim{Language: tt.claim, Source: domain.SourceManual}
			}
			h.message(privateMessage(person(ownerID, tt.client), "/start"))

			sent := h.onlySent(ownerID)
			source := domain.LangSource("")
			if tt.claim != "" {
				source = domain.SourceManual
			}
			want, markup := menu.PrivatePanel(menu.PrivateInput{
				Owner: ownerID, Lang: tt.lang, LangSource: source,
				BotUsername: testUsername, Version: "1.2.3", Section: menu.SectionHome,
			})
			if sent.Text != want {
				t.Fatalf("panel in %q:\n%s\nwant\n%s", tt.lang, sent.Text, want)
			}
			if strings.Join(labelsOf(sent.Markup), "|") != strings.Join(labelsOf(markup), "|") {
				t.Fatalf("buttons %v, want %v", labelsOf(sent.Markup), labelsOf(markup))
			}
		})
	}

	// The English panel invites to add the bot to a group (HND-03).
	h := newHarness(t)
	h.message(privateMessage(person(ownerID, "ko"), "/start"))
	if got := h.onlySent(ownerID).Text; !strings.Contains(got, "Add me to a group") {
		t.Fatalf("HND-03: panel %q lacks \"Add me to a group\"", got)
	}
}

// The private handler of Python, private_handler: any message in a private chat,
// not only /start, opens the panel, and so does any /start payload except a quote.
func TestPrivateAnyMessageOpensThePanel(t *testing.T) {
	tests := []struct {
		name string
		m    func(u tg.User) tg.Message
	}{
		{"plain text", func(u tg.User) tg.Message { return privateMessage(u, "hello") }},
		{"settings payload", func(u tg.User) tg.Message { return privateMessage(u, "/start settings") }},
		{"settings payload in capitals", func(u tg.User) tg.Message { return privateMessage(u, "/start SETTINGS") }},
		{"unknown payload", func(u tg.User) tg.Message { return privateMessage(u, "/start whatever") }},
		{"another command", func(u tg.User) tg.Message { return privateMessage(u, "/help") }},
		{"a photo", func(u tg.User) tg.Message {
			m := privateMessage(u, "")
			m.Photo = []tg.Photo{{FileID: "f", FileUniqueID: "u"}}
			return m
		}},
		{"another bot's start", func(u tg.User) tg.Message { return privateMessage(u, "/start@otherbot quote_1") }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t)
			h.message(tt.m(person(ownerID, "en")))
			sent := h.onlySent(ownerID)
			if !strings.HasPrefix(sent.Text, "<b>") || sent.Markup == nil {
				t.Fatalf("not a panel: %+v", sent)
			}
			if len(h.store.touched) != 1 || h.store.touched[0].ID != ownerID {
				t.Fatalf("the person was not recorded once: %+v", h.store.touched)
			}
			if len(h.store.quoteReads) != 0 {
				t.Fatalf("a quote was read: %v", h.store.quoteReads)
			}
		})
	}
}

// A message without a sender (a private chat always has one; this is the guard
// of private_handler's message.from_user) does nothing.
func TestPrivateWithoutASender(t *testing.T) {
	h := newHarness(t)
	h.message(tg.Message{MessageID: 1, Chat: tg.Chat{ID: ownerID, Type: tg.ChatPrivate}, Text: "/start"})
	if n := len(h.adapter.sent); n != 0 || len(h.store.touched) != 0 {
		t.Fatalf("sent %d messages, touched %d users", n, len(h.store.touched))
	}
	if keys := h.adapter.alertKeys(); len(keys) != 0 {
		t.Fatalf("alerts: %v", keys)
	}
}

// A failing core call stops the handler before it says anything, and the
// developers are told (doc 40-bot.md, errors in handlers).
func TestPrivateCoreFailureAlerts(t *testing.T) {
	h := newHarness(t)
	h.store.errs["TouchUser"] = errBoom()
	h.message(privateMessage(person(ownerID, "en"), "/start"))
	if len(h.adapter.sent) != 0 {
		t.Fatalf("the bot answered after a failure: %+v", h.adapter.sent)
	}
	if got := h.adapter.alertKeys(); len(got) != 1 || got[0] != "update:message" {
		t.Fatalf("alerts %v, want one with key update:message", got)
	}
}

// A language claim that cannot be read is not a reason to stay silent.
func TestPrivateLanguageReadFailureFallsBackToTheClient(t *testing.T) {
	h := newHarness(t)
	h.store.errs["LanguageClaim"] = errBoom()
	h.message(privateMessage(person(ownerID, "ru"), "/start"))
	want, _ := menu.PrivatePanel(menu.PrivateInput{Owner: ownerID, Lang: "ru", BotUsername: testUsername, Version: "1.2.3", Section: menu.SectionHome})
	if got := h.onlySent(ownerID).Text; got != want {
		t.Fatalf("panel:\n%s\nwant\n%s", got, want)
	}
}

func publishedQuote() *domain.QuoteDetail {
	return &domain.QuoteDetail{
		Quote: domain.Quote{
			ID: 42, GroupID: groupID, AuthorID: 5,
			Text:  "Hello <b>world</b> & co",
			Score: 0.87, AIScore: 0.9, ReactionCount: 3, MessageID: 321,
			AIModel:        "anthropic/claude-sonnet-5-5",
			DecisionReason: "Funny & <sharp>", AIBestText: "best",
			OperationError: "<script>internal failure</script>",
			CreatedAt:      time.Date(2026, 10, 20, 9, 30, 0, 0, time.UTC),
		},
		AuthorName: "Ann <i>", GroupName: "Team & Co",
	}
}

const cardEN = "<b>Day #42</b>\n" +
	"✅ Published\n\n" +
	"<blockquote><i>«Hello &lt;b&gt;world&lt;/b&gt; &amp; co»</i>\n— <b>Ann &lt;i&gt;</b></blockquote>\n\n" +
	"<a href='https://t.me/c/1234567890/321'>Team &amp; Co</a> · 3 ❤️ · 20 October 2026\n\n" +
	"<b>Total: 8.7/10</b>\n" +
	"<code>claude-sonnet-5-5 ●●●●●○</code> 9.0/10\n\n" +
	"<b>Decision reason:</b> <i>Funny &amp; &lt;sharp&gt;</i>\n" +
	"<b>AI choice:</b> <i>«best»</i>\n"

const cardRU = "<b>День #42</b>\n" +
	"✅ Опубликовано\n\n" +
	"<blockquote><i>«Hello &lt;b&gt;world&lt;/b&gt; &amp; co»</i>\n— <b>Ann &lt;i&gt;</b></blockquote>\n\n" +
	"<a href='https://t.me/c/1234567890/321'>Team &amp; Co</a> · 3 ❤️ · 20 октября 2026\n\n" +
	"<b>Итого: 8.7/10</b>\n" +
	"<code>claude-sonnet-5-5 ●●●●●○</code> 9.0/10\n\n" +
	"<b>Причина решения:</b> <i>Funny &amp; &lt;sharp&gt;</i>\n" +
	"<b>Выбор ИИ:</b> <i>«best»</i>\n"

// The details card (handlers.py private_handler, test_handlers.py:58 HND-01 and
// :99 HND-02), with doc 7.5 S1 applied: the total and the model's own bar stay, the
// Reactions and Length bars are gone. Text, author, group and reason are
// escaped; the technical error is never printed (HND-01's other half: only a
// published quote's public fields reach the card).
func TestQuoteCardGoldens(t *testing.T) {
	for lang, want := range map[string]string{"en": cardEN, "ru": cardRU} {
		t.Run(lang, func(t *testing.T) {
			got := quoteCard(lang, publishedQuote())
			if got != want {
				t.Fatalf("card:\n%s\nwant\n%s", got, want)
			}
		})
	}
	if strings.Contains(quoteCard("en", publishedQuote()), "internal failure") {
		t.Fatal("the operation error reached the card")
	}
}

// The Python card, without S1's two bars, on the variants of its data
// (handlers.py:526-585).
func TestQuoteCardVariants(t *testing.T) {
	tests := []struct {
		name   string
		edit   func(d *domain.QuoteDetail)
		has    []string
		hasNot []string
	}{
		{"no reactions, no heart", func(d *domain.QuoteDetail) { d.ReactionCount = 0 },
			[]string{"Team &amp; Co</a> · 20 October 2026"}, []string{"❤️"}},
		{"a supergroup links to the message", func(d *domain.QuoteDetail) {},
			[]string{"<a href='https://t.me/c/1234567890/321'>"}, nil},
		{"no message id: the name is not a link", func(d *domain.QuoteDetail) { d.MessageID = 0 },
			[]string{"Team &amp; Co · 20 October 2026"}, []string{"<a "}},
		{"a basic group has no t.me/c link", func(d *domain.QuoteDetail) { d.GroupID = -5555 },
			[]string{"Team &amp; Co · 20 October 2026"}, []string{"<a ", "t.me"}},
		{"a group the core does not know", func(d *domain.QuoteDetail) { d.GroupName = ""; d.MessageID = 0 },
			[]string{"— · 20 October 2026"}, nil},
		{"an author the core does not know", func(d *domain.QuoteDetail) { d.AuthorName = "" },
			[]string{"— <b>Anonymous</b>"}, nil},
		{"no model name", func(d *domain.QuoteDetail) { d.AIModel = "" },
			[]string{"<code>AI         ●●●●●○</code> 9.0/10"}, nil},
		{"a model without a vendor", func(d *domain.QuoteDetail) { d.AIModel = "claude" },
			[]string{"<code>claude     ●●●●●○</code>"}, nil},
		{"a short model is padded to ten characters", func(d *domain.QuoteDetail) { d.AIModel = "x/gpt-5" },
			[]string{"<code>gpt-5      ●●●●●○</code>"}, nil},
		// Python pads the escaped name (safe_model_short:<10), so "&lt;m&gt;" is nine characters.
		{"a model name is escaped and then padded", func(d *domain.QuoteDetail) { d.AIModel = "v/<m>" },
			[]string{"<code>&lt;m&gt;  ●●●●●○</code>"}, nil},
		{"no reason", func(d *domain.QuoteDetail) { d.DecisionReason = "" },
			nil, []string{"Decision reason"}},
		{"no AI choice", func(d *domain.QuoteDetail) { d.AIBestText = "" },
			nil, []string{"AI choice"}},
		{"a long AI choice is cut at a hundred characters", func(d *domain.QuoteDetail) { d.AIBestText = strings.Repeat("я", 101) },
			[]string{"«" + strings.Repeat("я", 100) + "…»"}, nil},
		{"a hundred characters are not cut", func(d *domain.QuoteDetail) { d.AIBestText = strings.Repeat("я", 100) },
			[]string{"«" + strings.Repeat("я", 100) + "»"}, []string{"…"}},
		{"S1: no Reactions or Length bars", func(d *domain.QuoteDetail) {},
			nil, []string{"Reactions", "Length"}},
		{"escaping", func(d *domain.QuoteDetail) {
			d.Text, d.AuthorName, d.GroupName, d.DecisionReason = "<script>a</script>", "<u>x</u>", "a&b", "'\""
		}, []string{"&lt;script&gt;a&lt;/script&gt;", "&lt;u&gt;x&lt;/u&gt;", "a&amp;b", "&#x27;&quot;"}, []string{"<script>", "<u>"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := publishedQuote()
			tt.edit(d)
			got := quoteCard("en", d)
			for _, want := range tt.has {
				if !strings.Contains(got, want) {
					t.Errorf("card lacks %q:\n%s", want, got)
				}
			}
			for _, not := range tt.hasNot {
				if strings.Contains(got, not) {
					t.Errorf("card has %q:\n%s", not, got)
				}
			}
		})
	}
}

// HND-19 (test_handlers.py:581): the context of a quote renders as in the
// published post: bold author, the quoted line in italics and guillemets, an
// author the core lost as the anonymous label of the reader's language; fewer
// than two lines is the plain quote.
func TestQuoteCardContext(t *testing.T) {
	d := publishedQuote()
	d.Context = []domain.ContextEntry{
		{Author: "Ann", Text: "first <", IsPrimary: false},
		{Author: "", Text: "second", IsPrimary: true},
	}
	en := quoteCard("en", d)
	wantEN := "<blockquote><b>Ann:</b> first &lt;\n<b>Anonymous:</b> <i>«second»</i></blockquote>"
	if !strings.Contains(en, wantEN) {
		t.Fatalf("context block missing in\n%s\nwant %s", en, wantEN)
	}
	ru := quoteCard("ru", d)
	if !strings.Contains(ru, "<b>"+i18n.T("ru", "common.anonymous")+":</b> <i>«second»</i>") {
		t.Fatalf("the anonymous label is not in the reader's language:\n%s", ru)
	}
	d.Context = d.Context[:1]
	if got := quoteCard("en", d); !strings.Contains(got, "— <b>Ann &lt;i&gt;</b>") {
		t.Fatalf("one context line should fall back to the quote itself:\n%s", got)
	}
}

// UTL: scoring.create_bar(int(score*100), 100) with width 6 and the circles
// style: int(int(x*100)/100*6) filled circles. The cases are the ones where the
// truncation to hundredths and the float steps decide.
func TestBar(t *testing.T) {
	tests := []struct {
		score  float64
		filled int
	}{
		{0, 0}, {0.1, 0}, {0.1666, 0}, {0.1667, 0}, {0.17, 1}, {0.34, 2}, {0.5, 3},
		{0.6667, 3}, {0.67, 4}, {0.9, 5}, {0.99, 5}, {1, 6}, {1.5, 6}, {-0.2, 0},
	}
	for _, tt := range tests {
		want := strings.Repeat("●", tt.filled) + strings.Repeat("○", 6-tt.filled)
		if got := bar(tt.score); got != want {
			t.Errorf("bar(%v) = %s, want %s", tt.score, got, want)
		}
	}
}

// Python pads with format spec <10, which counts characters; Go's width counts
// bytes, so the helper counts runes.
func TestPadRightAndCutRunes(t *testing.T) {
	if got := padRight("яя", 5); got != "яя   " {
		t.Errorf("padRight = %q", got)
	}
	if got := padRight("longer than ten", 10); got != "longer than ten" {
		t.Errorf("padRight shortened a long string: %q", got)
	}
	if got := cutRunes("яяяяя", 3); got != "яяя…" {
		t.Errorf("cutRunes = %q", got)
	}
	if got := cutRunes("яяя", 3); got != "яяя" {
		t.Errorf("cutRunes of an exact fit = %q", got)
	}
	if got := cutRunes("😀😀😀", 2); got != "😀😀…" {
		t.Errorf("cutRunes counted bytes: %q", got)
	}
}

// Python: str(chat_id).replace("-100", "", 1) if it starts with -100. Only those
// ids have a t.me/c link; for the rest Python built a broken one.
func TestTmeLinkChat(t *testing.T) {
	tests := []struct {
		id   int64
		want string
		ok   bool
	}{
		{-1001234567890, "1234567890", true},
		{-5555, "", false},
		{777, "", false},
		{-100, "", false},
		{0, "", false},
	}
	for _, tt := range tests {
		got, ok := tmeLinkChat(tt.id)
		if got != tt.want || ok != tt.ok {
			t.Errorf("tmeLinkChat(%d) = %q, %v; want %q, %v", tt.id, got, ok, tt.want, tt.ok)
		}
	}
}

func (h *harness) startQuote(user tg.User, payload string) {
	h.t.Helper()
	h.message(privateMessage(user, "/start "+payload))
}

// The deep links (handlers.py private_handler, utils.parse_quote_start_payload,
// parse_legacy_quote_start_payload; doc 40-bot.md, deep links): signed links open
// the card; a bad signature, a missing quote, and a quote the reader may not
// see are told apart only where the link itself is broken.
func TestQuoteDeepLinks(t *testing.T) {
	signed := func(h *harness) string { return h.signer.Payload(42) }

	t.Run("a signed link opens the card without asking Telegram", func(t *testing.T) {
		h := newHarness(t)
		h.store.quotes[42] = publishedQuote()
		h.startQuote(person(ownerID, "en"), signed(h))
		sent := h.onlySent(ownerID)
		if sent.Text != cardEN {
			t.Fatalf("card:\n%s", sent.Text)
		}
		if sent.Markup != nil {
			t.Fatalf("the card has buttons: %v", labelsOf(sent.Markup))
		}
		if n := len(h.api.named("getChatMember")); n != 0 {
			t.Fatalf("a signed link asked Telegram about membership %d times", n)
		}
		if len(h.store.quoteReads) != 1 || h.store.quoteReads[0] != 42 {
			t.Fatalf("quotes read: %v", h.store.quoteReads)
		}
	})

	t.Run("the card speaks the reader's language", func(t *testing.T) {
		h := newHarness(t)
		h.store.quotes[42] = publishedQuote()
		h.startQuote(person(ownerID, "ru"), signed(h))
		if got := h.onlySent(ownerID).Text; got != cardRU {
			t.Fatalf("card:\n%s", got)
		}
	})

	t.Run("a signature for another quote", func(t *testing.T) {
		h := newHarness(t)
		h.store.quotes[42] = publishedQuote()
		forged := "quote_42_" + strings.TrimPrefix(h.signer.Payload(43), "quote_43_")
		h.startQuote(person(ownerID, "en"), forged)
		if got := h.onlySent(ownerID).Text; got != i18n.T("en", "private.invalid_quote_link") {
			t.Fatalf("reply %q", got)
		}
		if len(h.store.quoteReads) != 0 {
			t.Fatal("a forged link reached the database")
		}
	})

	broken := []string{
		"quote_", "quote_abc", "quote_12_3_4", "quote_12_deadbeefdeadbeef", "quote__", "quote_-5", "quote_ 5",
		"quote_" + strings.Repeat("9", 40) + "_" + strings.Repeat("a", 16),
	}
	for _, payload := range broken {
		t.Run("broken "+payload, func(t *testing.T) {
			h := newHarness(t)
			h.startQuote(person(ownerID, "en"), payload)
			if got := h.onlySent(ownerID).Text; got != i18n.T("en", "private.invalid_quote_link") {
				t.Fatalf("reply %q", got)
			}
			if len(h.store.quoteReads) != 0 {
				t.Fatal("a broken link reached the database")
			}
		})
	}

	// Python's str.isdigit accepts digits of other scripts and superscripts; the
	// Go side treats them as a quote that does not exist (doc 40-bot.md, deep links).
	for _, payload := range []string{"quote_١٢٣", "quote_²", "quote_١٢٣_0123456789abcdef", "quote_٤٢"} {
		t.Run("not-ASCII digits "+payload, func(t *testing.T) {
			h := newHarness(t)
			h.store.quotes[42] = publishedQuote()
			h.startQuote(person(ownerID, "en"), payload)
			if got := h.onlySent(ownerID).Text; got != i18n.T("en", "private.quote_not_found") {
				t.Fatalf("reply %q", got)
			}
			if len(h.store.quoteReads) != 0 {
				t.Fatal("the link reached the database")
			}
		})
	}

	t.Run("a quote that does not exist", func(t *testing.T) {
		h := newHarness(t)
		h.startQuote(person(ownerID, "en"), signed(h))
		if got := h.onlySent(ownerID).Text; got != i18n.T("en", "private.quote_not_found") {
			t.Fatalf("reply %q", got)
		}
	})

	t.Run("a database failure is an error, not a not-found", func(t *testing.T) {
		h := newHarness(t)
		h.store.errs["Detail"] = errBoom()
		h.startQuote(person(ownerID, "en"), signed(h))
		if len(h.adapter.sent) != 0 {
			t.Fatalf("answered: %+v", h.adapter.sent)
		}
		if keys := h.adapter.alertKeys(); len(keys) != 1 {
			t.Fatalf("alerts %v", keys)
		}
	})
}

// Links from before 0.10.2 carry no signature, so the card is shown only to
// somebody Telegram says is in the quote's group (_can_view_legacy_quote). Every
// way of not being allowed gets the answer of a quote that does not exist, so
// the reply does not tell whether the quote exists.
func TestLegacyQuoteLinks(t *testing.T) {
	tests := []struct {
		name   string
		status string
		fails  bool
		shown  bool
	}{
		{"member", "member", false, true},
		{"administrator", "administrator", false, true},
		{"creator", "creator", false, true},
		{"restricted but still in the group", "restricted-in", false, true},
		{"restricted and out of the group", "restricted", false, false},
		{"left", "left", false, false},
		{"kicked", "kicked", false, false},
		{"telegram cannot say", "", true, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t)
			h.store.quotes[42] = publishedQuote()
			h.api.setMember(ownerID, tt.status)
			h.api.memberFails = tt.fails
			h.startQuote(person(ownerID, "en"), "quote_42")
			got := h.onlySent(ownerID).Text
			if tt.shown && got != cardEN {
				t.Fatalf("reply %q, want the card", got)
			}
			if !tt.shown && got != i18n.T("en", "private.quote_not_found") {
				t.Fatalf("reply %q, want not found", got)
			}
			calls := h.api.named("getChatMember")
			if len(calls) != 1 || calls[0].Query.Get("chat_id") != "-1001234567890" || calls[0].Query.Get("user_id") != "777" {
				t.Fatalf("membership asked as %+v", calls)
			}
		})
	}

	t.Run("a missing quote and a hidden one look the same", func(t *testing.T) {
		h := newHarness(t)
		h.store.quotes[42] = publishedQuote()
		h.api.setMember(ownerID, "left")
		h.startQuote(person(ownerID, "en"), "quote_42")
		h.startQuote(person(ownerID, "en"), "quote_43")
		sent := h.adapter.sentTo(ownerID)
		if len(sent) != 2 || sent[0].Text != sent[1].Text || sent[0].Markup != nil || sent[1].Markup != nil {
			t.Fatalf("replies differ: %+v", sent)
		}
		// Telegram is not asked about a quote that is not there.
		if n := len(h.api.named("getChatMember")); n != 1 {
			t.Fatalf("%d membership lookups, want 1", n)
		}
	})

	t.Run("a quote with no group is hidden", func(t *testing.T) {
		h := newHarness(t)
		d := publishedQuote()
		d.GroupID = 0
		h.store.quotes[42] = d
		h.startQuote(person(ownerID, "en"), "quote_42")
		if got := h.onlySent(ownerID).Text; got != i18n.T("en", "private.quote_not_found") {
			t.Fatalf("reply %q", got)
		}
		if n := len(h.api.named("getChatMember")); n != 0 {
			t.Fatalf("%d membership lookups", n)
		}
	})
}

// Doc 40-bot.md, deep links: a payload with the shape of a quote link but digits that are not
// ASCII is "quote not found"; Python's isdigit accepted them and then crashed in int().
func TestQuoteIDShape(t *testing.T) {
	tests := []struct {
		payload string
		want    bool
	}{
		{"quote_١٢٣", true},
		{"quote_²", true},
		{"quote_١٢٣_abc", true},
		{"quote_12", true}, // an overflowing or odd id with no signature is a quote we do not have
		{"quote_12_abc", false},
		{"quote_abc", false},
		{"quote_", false},
		{"quote_١٢٣_a_b", false},
		{"other_١٢٣", false},
	}
	for _, tt := range tests {
		if got := quoteIDShape(tt.payload); got != tt.want {
			t.Errorf("quoteIDShape(%q) = %v, want %v", tt.payload, got, tt.want)
		}
	}
}
