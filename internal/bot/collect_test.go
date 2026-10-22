// SPDX-License-Identifier: Apache-2.0

package bot

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/FreshLabDev/tg"

	"github.com/FreshLabDev/quoto/internal/domain"
)

// HND-22 and HND-23 (test_handlers.py:650-766): the collector ignores an update
// without a sender, a bot, a command and a text that is only a link; it takes
// media, a text that has a link in it, and media whose caption is a link.
func TestCollectFilters(t *testing.T) {
	user := person(ownerID, "en")
	bot := tg.User{ID: 4242, IsBot: true, FirstName: "Group"}
	text := func(s string) tg.Message { return groupText(user, 5, s) }
	photo := func(caption string) tg.Message {
		m := text("")
		m.Caption = caption
		m.Photo = []tg.Photo{{FileID: "f", FileUniqueID: "u", FileSize: 10}}
		return m
	}
	noSender := text("hello")
	noSender.From = nil
	fromBot := groupText(bot, 5, "hello")

	tests := []struct {
		name string
		m    tg.Message
		keep bool
	}{
		{"plain text", text("hello"), true},
		{"HND-22: no sender", noSender, false},
		{"HND-22: a bot", fromBot, false},
		{"a command", text("/ok"), false},
		{"a command with slashes", text("/s/x/y"), false},
		{"a command for another bot", text("/cmd@otherbot x"), false},
		{"HND-22: only a link", text("https://vt.tiktok.com/ZSx5TH1DA/"), false},
		{"a link with surrounding whitespace", text("  https://example.com \n"), false},
		{"a link in capitals", text("HTTPS://EXAMPLE.COM/A"), false},
		{"www", text("www.example.com/path?x=1"), false},
		{"a bare domain", text("example.com"), false},
		{"a bare domain with a path", text("sub.example.co.uk/a/b?c=d#e"), false},
		{"a deep link", text("tg://resolve?domain=x"), false},
		{"a file name looks like a domain", text("readme.md"), false},
		{"two words and a dot", text("hi.there"), false},
		{"dotted I", text("İstanbul.com"), false},
		{"HND-23: a link inside a sentence", text("see https://example.com now"), true},
		{"a link and a word", text("https://example.com great"), true},
		{"a domain and a word", text("example.com is down"), true},
		{"a path with a space", text("example.com/a b"), true},
		{"a one-letter top level domain", text("a.b"), true},
		{"an address", text("1.2.3.4"), true},
		{"a cyrillic domain is not matched by the pattern", text("пример.рф"), true},
		{"a scheme with nothing after it", text("http://"), true},
		{"a sentence ending in a dot", text("hello."), true},
		{"HND-23: media", photo(""), true},
		{"HND-23: media whose caption is a link", photo("https://example.com"), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t)
			h.message(tt.m)
			if got := len(h.store.saved) == 1; got != tt.keep {
				t.Fatalf("collected = %v, want %v", got, tt.keep)
			}
			if !tt.keep && len(h.store.ensures) != 0 {
				t.Fatal("a message that is not collected still recorded the group")
			}
			if tt.keep && len(h.adapter.sent) != 0 {
				t.Fatalf("the collector spoke: %+v", h.adapter.sent)
			}
		})
	}
}

// Doc 7.2 F14: a message with no text, caption or file is not stored. The Python
// bot stored joins, leaves, pins, polls and dice with a placeholder text, counted
// them towards the day's minimum and sent the placeholders to the model. The
// group is still recorded: the sender was there.
func TestCollectSkipsMessagesWithNothingToQuote(t *testing.T) {
	user := person(ownerID, "en")
	raw := func(js string) tg.Message {
		m := groupText(user, 5, "")
		m.Raw = json.RawMessage(js)
		return m
	}
	skipped := map[string]tg.Message{
		"a join":     raw(`{"message_id":5,"new_chat_members":[{"id":9}]}`),
		"a leave":    raw(`{"message_id":5,"left_chat_member":{"id":9}}`),
		"a pin":      raw(`{"message_id":5,"pinned_message":{"message_id":3}}`),
		"a poll":     raw(`{"message_id":5,"poll":{"id":"1"}}`),
		"a dice":     raw(`{"message_id":5,"dice":{"emoji":"x","value":3}}`),
		"a location": raw(`{"message_id":5,"location":{"latitude":1,"longitude":2}}`),
		"blank text": groupText(user, 5, " \n "),
		"empty":      groupText(user, 5, ""),
	}
	for name, m := range skipped {
		t.Run("skipped: "+name, func(t *testing.T) {
			h := newHarness(t)
			h.message(m)
			if len(h.store.saved) != 0 {
				t.Fatalf("stored %+v", h.store.saved)
			}
			if len(h.store.ensures) != 1 {
				t.Fatalf("the group was recorded %d times, want 1", len(h.store.ensures))
			}
			h.deliver(tg.Update{UpdateID: 2, EditedMessage: &m})
			if len(h.store.edits) != 0 {
				t.Fatalf("an edit was applied: %+v", h.store.edits)
			}
		})
	}

	kept := map[string]tg.Message{
		"a caption on an unmodelled type": func() tg.Message {
			m := raw(`{"message_id":5,"paid_media":{"x":1}}`)
			m.Caption = "look"
			return m
		}(),
		"a sticker": func() tg.Message {
			m := groupText(user, 5, "")
			m.Sticker = &tg.Sticker{FileID: "f", FileUniqueID: "u"}
			return m
		}(),
	}
	for name, m := range kept {
		t.Run("kept: "+name, func(t *testing.T) {
			h := newHarness(t)
			h.message(m)
			if len(h.store.saved) != 1 {
				t.Fatalf("stored %d messages, want 1", len(h.store.saved))
			}
		})
	}
}

// What is stored for a text message: the stripped text, the time Telegram gave,
// the reply, and the group recorded once (messages.save in Python).
func TestCollectStoresTheMessage(t *testing.T) {
	h := newHarness(t)
	user := person(ownerID, "en")
	m := groupText(user, 77, "  hello there \n")
	m.Date = 1760000000
	m.ReplyToMessage = &tg.Message{MessageID: 70}
	h.message(m)

	want := domain.NewMessage{
		MessageID: 77, ChatID: groupID, UserID: ownerID, Text: "hello there", ContentType: "text",
		ReplyTo: 70, CreatedAt: time.Unix(1760000000, 0).UTC(),
	}
	if len(h.store.saved) != 1 || !reflect.DeepEqual(h.store.saved[0], want) {
		t.Fatalf("saved %+v\nwant %+v", h.store.saved, want)
	}
	if len(h.store.ensures) != 1 || h.store.ensures[0].Actor.ID != ownerID || h.store.ensures[0].Chat.ID != groupID {
		t.Fatalf("group recorded as %+v", h.store.ensures)
	}
}

// created_at is the clock's when Telegram gave no date, and always UTC.
func TestCollectDateFallback(t *testing.T) {
	h := newHarness(t)
	m := groupText(person(ownerID, "en"), 1, "hi")
	m.Date = 0
	h.message(m)
	if got := h.store.saved[0].CreatedAt; !got.Equal(h.clock.Now()) || got.Location() != time.UTC {
		t.Fatalf("created at %v", got)
	}
}

// The same message delivered twice is stored once (the unique key is the store's)
// and the media pipeline is not told twice.
func TestCollectDuplicateDelivery(t *testing.T) {
	h := newHarness(t)
	m := groupText(person(ownerID, "en"), 9, "")
	m.Photo = []tg.Photo{{FileID: "f", FileUniqueID: "u", FileSize: 5}}
	h.message(m)
	h.message(m)
	if len(h.store.saved) != 1 {
		t.Fatalf("stored %d times", len(h.store.saved))
	}
	if got := h.media.submitted(); !reflect.DeepEqual(got, []int64{1}) {
		t.Fatalf("media submitted %v, want once", got)
	}
}

// The eight kinds of file (02-media.md section 1.2, media.py 50-154), their
// mime types and the stored text, in the order Python asked for them.
func TestCollectMediaKinds(t *testing.T) {
	tests := []struct {
		name    string
		m       func(m *tg.Message)
		kind    string
		mime    string
		name_   string
		size    int64
		w, h    int
		dur     float64
		analyse bool
		text    string
	}{
		{
			name: "photo: the biggest size wins, the later one on a tie",
			m: func(m *tg.Message) {
				m.Photo = []tg.Photo{
					{FileID: "small", FileUniqueID: "us", Width: 90, Height: 90, FileSize: 100},
					{FileID: "big", FileUniqueID: "ub", Width: 800, Height: 600, FileSize: 5000},
					{FileID: "tie", FileUniqueID: "ut", Width: 801, Height: 601, FileSize: 5000},
				}
			},
			kind: "photo", mime: "image/jpeg", size: 5000, w: 801, h: 601, analyse: true,
			text: "photo: Файл: photo, mime=image/jpeg, size=5000",
		},
		{
			name: "video",
			m: func(m *tg.Message) {
				m.Video = &tg.Video{FileID: "v", FileUniqueID: "uv", Width: 640, Height: 360, Duration: 12, FileSize: 900, FileName: "a.mp4"}
			},
			kind: "video", mime: "video/mp4", name_: "a.mp4", size: 900, w: 640, h: 360, dur: 12, analyse: true,
			text: "video: Файл: video, name=a.mp4, mime=video/mp4, size=900, duration=12s",
		},
		{
			name: "animation comes before document",
			m: func(m *tg.Message) {
				m.Animation = &tg.Animation{FileID: "g", FileUniqueID: "ug", Width: 320, Height: 240, Duration: 3, MimeType: "video/webm", FileSize: 70}
				m.Document = &tg.Document{FileID: "d", FileUniqueID: "ud", MimeType: "video/webm", FileSize: 70}
			},
			kind: "animation", mime: "video/webm", size: 70, w: 320, h: 240, dur: 3, analyse: true,
			text: "animation: Файл: animation, mime=video/webm, size=70, duration=3s",
		},
		{
			name: "video note",
			m: func(m *tg.Message) {
				m.VideoNote = &tg.VideoNote{FileID: "n", FileUniqueID: "un", Length: 240, Duration: 7, FileSize: 40}
			},
			kind: "video_note", mime: "video/mp4", size: 40, w: 240, h: 240, dur: 7, analyse: true,
			text: "video_note: Файл: video_note, mime=video/mp4, size=40, duration=7s",
		},
		{
			name: "voice",
			m: func(m *tg.Message) {
				m.Voice = &tg.Voice{FileID: "o", FileUniqueID: "uo", Duration: 5, FileSize: 30}
			},
			kind: "voice", mime: "audio/ogg", size: 30, dur: 5, analyse: true,
			text: "voice: Файл: voice, mime=audio/ogg, size=30, duration=5s",
		},
		{
			name: "audio",
			m: func(m *tg.Message) {
				m.Audio = &tg.Audio{FileID: "a", FileUniqueID: "ua", Duration: 200, FileSize: 3000, FileName: "song.mp3"}
			},
			kind: "audio", mime: "audio/mpeg", name_: "song.mp3", size: 3000, dur: 200, analyse: true,
			text: "audio: Файл: audio, name=song.mp3, mime=audio/mpeg, size=3000, duration=200s",
		},
		{
			name: "a static sticker",
			m: func(m *tg.Message) {
				m.Sticker = &tg.Sticker{FileID: "s", FileUniqueID: "us", Width: 512, Height: 512, FileSize: 20000}
			},
			kind: "sticker", mime: "image/webp", size: 20000, w: 512, h: 512, analyse: true,
			text: "sticker: Файл: sticker, mime=image/webp, size=20000",
		},
		{
			name: "an animated sticker cannot be analysed",
			m: func(m *tg.Message) {
				m.Sticker = &tg.Sticker{FileID: "s", FileUniqueID: "us", Width: 512, Height: 512, IsAnimated: true, FileSize: 20}
			},
			kind: "sticker", mime: "application/x-tgsticker", size: 20, w: 512, h: 512,
			text: "sticker: Файл: sticker, mime=application/x-tgsticker, size=20",
		},
		{
			name: "a video sticker can",
			m: func(m *tg.Message) {
				m.Sticker = &tg.Sticker{FileID: "s", FileUniqueID: "us", Width: 512, Height: 512, IsVideo: true, IsAnimated: true, FileSize: 21}
			},
			kind: "sticker", mime: "video/webm", size: 21, w: 512, h: 512, analyse: true,
			text: "sticker: Файл: sticker, mime=video/webm, size=21",
		},
		{
			name: "a document that is an image",
			m: func(m *tg.Message) {
				m.Document = &tg.Document{FileID: "d", FileUniqueID: "ud", MimeType: "image/png", FileSize: 11, FileName: "p.png"}
			},
			kind: "image", mime: "image/png", name_: "p.png", size: 11, analyse: true,
			text: "image: Файл: image, name=p.png, mime=image/png, size=11",
		},
		{
			name: "any other document",
			m: func(m *tg.Message) {
				m.Document = &tg.Document{FileID: "d", FileUniqueID: "ud", FileSize: 12, FileName: "x.bin"}
			},
			kind: "document", mime: "application/octet-stream", name_: "x.bin", size: 12,
			text: "document: Файл: document, name=x.bin, mime=application/octet-stream, size=12",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t)
			m := groupText(person(ownerID, "en"), 31, "")
			tt.m(&m)
			h.message(m)
			if len(h.store.saved) != 1 {
				t.Fatalf("stored %d messages", len(h.store.saved))
			}
			got := h.store.saved[0]
			if got.ContentType != tt.kind || got.Text != tt.text {
				t.Fatalf("stored as %q with text %q\nwant %q with text %q", got.ContentType, got.Text, tt.kind, tt.text)
			}
			src := got.Media
			if src == nil {
				t.Fatal("no media source")
			}
			if src.Kind != tt.kind || src.MimeType != tt.mime || src.FileName != tt.name_ || src.FileSize != tt.size ||
				src.Width != tt.w || src.Height != tt.h || src.Duration != tt.dur || src.SupportsAnalysis != tt.analyse {
				t.Fatalf("media source %+v", *src)
			}
			if src.FileID == "" || src.FileUniqueID == "" {
				t.Fatalf("file ids lost: %+v", *src)
			}
			if tt.name == "photo: the biggest size wins, the later one on a tie" && src.FileID != "tie" {
				t.Fatalf("picked %q", src.FileID)
			}
			if got := h.media.submitted(); !reflect.DeepEqual(got, []int64{1}) {
				t.Fatalf("media submitted %v", got)
			}
		})
	}
}

// messages.text and content_type: initial_message_text and message_content_type
// (media.py 157-181). The placeholders reach the model and stay in Russian.
func TestInitialTextAndContentType(t *testing.T) {
	photo := []tg.Photo{{FileID: "f", FileUniqueID: "u", FileSize: 9}}
	tests := []struct {
		name string
		m    tg.Message
		text string
		kind string
	}{
		{"text is stripped", tg.Message{Text: " hi \n"}, "hi", "text"},
		{"a caption goes before the description", tg.Message{Caption: " look ", Photo: photo},
			"look\nphoto: Файл: photo, mime=image/jpeg, size=9", "photo"},
		{"text wins over a file", tg.Message{Text: "hi", Photo: photo}, "hi", "text"},
		// message_content_type tests `if message.text`, which a blank string passes;
		// initial_message_text strips first, which it does not.
		{"a blank text is a text by type and no text by content", tg.Message{Text: "  \n", Photo: photo},
			"photo: Файл: photo, mime=image/jpeg, size=9", "text"},
		{"a caption alone", tg.Message{Caption: "  just a caption  "}, "just a caption", "unknown"},
		// Nothing to quote: these are not stored (hasContent), so the text is empty;
		// the content type is still what aiogram would say.
		{"a service message keeps aiogram's content type", tg.Message{Raw: json.RawMessage(`{"message_id":1,"new_chat_members":[{"id":5}]}`)},
			"", "new_chat_members"},
		{"a poll", tg.Message{Raw: json.RawMessage(`{"poll":{"id":"1"}}`)}, "", "poll"},
		{"a pinned message", tg.Message{Raw: json.RawMessage(`{"pinned_message":{"message_id":3}}`)}, "", "pinned_message"},
		{"a rich message, new in aiogram 3.31", tg.Message{Raw: json.RawMessage(`{"rich_message":{"x":1}}`)}, "", "rich_message"},
		{"aiogram's order decides when two are set", tg.Message{Raw: json.RawMessage(`{"location":{"latitude":1},"contact":{"phone_number":"1"}}`)},
			"", "contact"},
		{"false and empty fields do not count", tg.Message{Raw: json.RawMessage(`{"text":"","poll":null,"dice":false,"contact":{"a":1}}`)},
			"", "contact"},
		{"no raw message", tg.Message{}, "", "unknown"},
		{"a raw message that is not an object", tg.Message{Raw: json.RawMessage(`[1]`)}, "", "unknown"},
		{"nothing known in the raw message", tg.Message{Raw: json.RawMessage(`{"message_id":1,"date":2}`)}, "", "unknown"},
		{"a caption on a type the bot does not model", tg.Message{Caption: "look", Raw: json.RawMessage(`{"paid_media":{"x":1}}`)},
			"look", "paid_media"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			src := sourceFromMessage(&tt.m)
			if got := initialText(&tt.m, src); got != tt.text {
				t.Errorf("text = %q, want %q", got, tt.text)
			}
			if got := contentType(&tt.m, src); got != tt.kind {
				t.Errorf("content type = %q, want %q", got, tt.kind)
			}
		})
	}
}

// aiogram's content_type tests the names in this order; the list has 79 names
// (aiogram 3.31.0, read out of Message.content_type with inspect.getsource) and
// starts with the ones every message can be.
func TestContentTypeList(t *testing.T) {
	if len(contentTypes) != 79 {
		t.Fatalf("%d content types, want 79", len(contentTypes))
	}
	seen := map[string]bool{}
	for _, name := range contentTypes {
		if seen[name] {
			t.Errorf("%q is listed twice", name)
		}
		seen[name] = true
	}
	if contentTypes[0] != "text" || contentTypes[len(contentTypes)-1] != "community_chat_joined" {
		t.Errorf("first %q, last %q", contentTypes[0], contentTypes[len(contentTypes)-1])
	}
	// live_photo is tested before photo, so a message with both is a live photo.
	idx := map[string]int{}
	for i, name := range contentTypes {
		idx[name] = i
	}
	if idx["live_photo"] > idx["photo"] {
		t.Errorf("live_photo (%d) must come before photo (%d)", idx["live_photo"], idx["photo"])
	}
}

// Media goes to the analysis queue only when the whole chain agrees: the
// environment switch, the group's own, and a message that was newly stored.
func TestCollectSubmitsMediaOnlyWhenEnabled(t *testing.T) {
	photo := func() tg.Message {
		m := groupText(person(ownerID, "en"), 31, "")
		m.Photo = []tg.Photo{{FileID: "f", FileUniqueID: "u", FileSize: 9}}
		return m
	}
	off := false

	t.Run("enabled", func(t *testing.T) {
		h := newHarness(t)
		h.message(photo())
		if len(h.media.submitted()) != 1 {
			t.Fatal("not submitted")
		}
	})
	t.Run("the environment switch is off", func(t *testing.T) {
		h := newHarness(t, func(c *Config) { c.MediaAnalysisEnabled = false })
		h.message(photo())
		if len(h.media.submitted()) != 0 || len(h.store.saved) != 1 {
			t.Fatalf("submitted %v, stored %d", h.media.submitted(), len(h.store.saved))
		}
	})
	t.Run("the group's switch is off", func(t *testing.T) {
		h := newHarness(t)
		h.store.group(groupID).MediaAnalysis = &off
		h.message(photo())
		if len(h.media.submitted()) != 0 || len(h.store.saved) != 1 {
			t.Fatalf("submitted %v, stored %d", h.media.submitted(), len(h.store.saved))
		}
	})
	t.Run("text is never submitted", func(t *testing.T) {
		h := newHarness(t)
		h.message(groupText(person(ownerID, "en"), 1, "hello"))
		if len(h.media.submitted()) != 0 {
			t.Fatal("text was submitted")
		}
	})
	t.Run("no queue wired", func(t *testing.T) {
		h := newHarness(t)
		h.bot.d.Media = nil
		h.message(photo())
		if len(h.store.saved) != 1 || len(h.adapter.alertKeys()) != 0 {
			t.Fatal("a missing queue broke the collector")
		}
	})
}

// The agreement does not gate collection: a group that has not accepted still has
// its messages stored (they are used only after acceptance).
func TestCollectIgnoresTheAgreement(t *testing.T) {
	h := newHarness(t)
	h.store.group(groupID) // no acceptance recorded
	h.message(groupText(person(ownerID, "en"), 1, "hello"))
	if len(h.store.saved) != 1 {
		t.Fatal("the message was not stored")
	}
}

// Private chats and channels are not collected; a basic group is.
func TestCollectChatTypes(t *testing.T) {
	for kind, keep := range map[string]bool{tg.ChatGroup: true, tg.ChatSupergroup: true, tg.ChatChannel: false} {
		h := newHarness(t)
		m := groupText(person(ownerID, "en"), 1, "hello")
		m.Chat.Type = kind
		h.message(m)
		if got := len(h.store.saved) == 1; got != keep {
			t.Errorf("%s: collected = %v, want %v", kind, got, keep)
		}
	}
}

// Python's messages.save was retried by the pool; here a lost write is retried
// three times with 0.5 s and 1 s between, and a message that still cannot be
// stored is reported once (doc 40-bot.md, collection).
func TestSaveRetries(t *testing.T) {
	m := groupText(person(ownerID, "en"), 5, "hello")

	t.Run("recovers on the third attempt", func(t *testing.T) {
		h := newHarness(t)
		h.store.saveResults = []error{errBoom(), errBoom()}
		h.message(m)
		if len(h.store.saved) != 1 || h.store.saveCalls != 3 {
			t.Fatalf("stored %d after %d calls", len(h.store.saved), h.store.saveCalls)
		}
		if got := h.clock.waits(); !reflect.DeepEqual(got, []time.Duration{500 * time.Millisecond, time.Second}) {
			t.Fatalf("waits %v", got)
		}
		if keys := h.adapter.alertKeys(); len(keys) != 0 {
			t.Fatalf("alerts %v", keys)
		}
	})

	t.Run("gives up after three and tells a developer", func(t *testing.T) {
		h := newHarness(t)
		h.store.saveResults = []error{errBoom(), errBoom(), errBoom()}
		h.message(m)
		if len(h.store.saved) != 0 || h.store.saveCalls != 3 {
			t.Fatalf("stored %d after %d calls", len(h.store.saved), h.store.saveCalls)
		}
		if got := h.clock.waits(); len(got) != 2 {
			t.Fatalf("waits %v, want two", got)
		}
		keys := h.adapter.alertKeys()
		if len(keys) != 1 || keys[0] != "message-save:-1001234567890:5" {
			t.Fatalf("alerts %v", keys)
		}
		// Reported by save, not as a failed update: no second alert.
		if h.adapter.alerts[0].Text == "" {
			t.Fatal("empty alert")
		}
	})

	t.Run("stops when asked to", func(t *testing.T) {
		h := newHarness(t)
		h.store.saveResults = []error{errBoom(), errBoom(), errBoom()}
		h.bot.sleep = func(context.Context, time.Duration) error { return context.Canceled }
		h.message(m)
		if h.store.saveCalls != 1 {
			t.Fatalf("%d save calls after cancellation, want 1", h.store.saveCalls)
		}
	})
}

// A group that cannot be recorded costs the message and is an error of the
// update (alerted by the dispatcher).
func TestCollectGroupFailure(t *testing.T) {
	h := newHarness(t)
	h.store.errs["Ensure"] = errBoom()
	h.message(groupText(person(ownerID, "en"), 1, "hello"))
	if got := h.adapter.alertKeys(); len(got) != 1 || got[0] != "update:message" {
		t.Fatalf("alerts %v", got)
	}
}

// HND-24 (test_handlers.py:768, :780): an edit updates the stored message; a
// bot's edit is ignored. Doc 7.2 F11: a message with a file keeps its text.
func TestEditedMessages(t *testing.T) {
	edited := func(m tg.Message) tg.Update { return tg.Update{UpdateID: 1, EditedMessage: &m} }
	user := person(ownerID, "en")

	t.Run("text", func(t *testing.T) {
		h := newHarness(t)
		m := groupText(user, 8, " new text ")
		m.ReplyToMessage = &tg.Message{MessageID: 2}
		h.deliver(edited(m))
		want := domain.MessageEdit{MessageID: 8, ChatID: groupID, Text: "new text", ContentType: "text", ReplyTo: 2}
		if len(h.store.edits) != 1 || h.store.edits[0] != want {
			t.Fatalf("edits %+v\nwant %+v", h.store.edits, want)
		}
	})

	t.Run("F11: a file keeps the stored text", func(t *testing.T) {
		h := newHarness(t)
		m := groupText(user, 8, "")
		m.Caption = " caption "
		m.Photo = []tg.Photo{{FileID: "f", FileUniqueID: "u", FileSize: 3}}
		h.deliver(edited(m))
		if len(h.store.edits) != 1 || !h.store.edits[0].KeepText || h.store.edits[0].Caption != "caption" || h.store.edits[0].ContentType != "photo" {
			t.Fatalf("edits %+v", h.store.edits)
		}
	})

	ignored := []struct {
		name string
		m    func() tg.Message
	}{
		{"HND-24: a bot", func() tg.Message { return groupText(tg.User{ID: 9, IsBot: true}, 8, "x") }},
		{"no sender", func() tg.Message { m := groupText(user, 8, "x"); m.From = nil; return m }},
		{"an edit that makes it a link", func() tg.Message { return groupText(user, 8, "https://example.com") }},
		{"an edit that makes it a command", func() tg.Message { return groupText(user, 8, "/start") }},
		{"a private chat", func() tg.Message {
			m := groupText(user, 8, "x")
			m.Chat = tg.Chat{ID: ownerID, Type: tg.ChatPrivate}
			return m
		}},
	}
	for _, tt := range ignored {
		t.Run("ignored: "+tt.name, func(t *testing.T) {
			h := newHarness(t)
			h.deliver(edited(tt.m()))
			if len(h.store.edits) != 0 || len(h.adapter.alertKeys()) != 0 {
				t.Fatalf("edits %+v, alerts %v", h.store.edits, h.adapter.alertKeys())
			}
		})
	}

	t.Run("a failing store is an error", func(t *testing.T) {
		h := newHarness(t)
		h.store.errs["Edit"] = errors.New("down")
		h.deliver(edited(groupText(user, 8, "x")))
		if got := h.adapter.alertKeys(); len(got) != 1 || got[0] != "update:edited_message" {
			t.Fatalf("alerts %v", got)
		}
	})
}

func emoji(e string) tg.ReactionType { return tg.ReactionType{Type: tg.ReactionEmoji, Emoji: e} }
func custom(id string) tg.ReactionType {
	return tg.ReactionType{Type: tg.ReactionCustomEmoji, CustomEmojiID: id}
}

// HND-20, HND-21 (test_handlers.py:620, :637): the reaction of one person is the
// difference between the lists Telegram sends; no difference is no call.
func TestReactionDelta(t *testing.T) {
	paid := tg.ReactionType{Type: tg.ReactionPaid}
	tests := []struct {
		name     string
		old, new []tg.ReactionType
		want     map[string]int
	}{
		{"HND-20: one swapped", []tg.ReactionType{emoji("🔥"), emoji("❤️")}, []tg.ReactionType{emoji("🔥"), emoji("😂")},
			map[string]int{"❤️": -1, "😂": 1}},
		{"added", nil, []tg.ReactionType{emoji("👍")}, map[string]int{"👍": 1}},
		{"removed", []tg.ReactionType{emoji("👍")}, nil, map[string]int{"👍": -1}},
		{"a custom emoji", nil, []tg.ReactionType{custom("5368")}, map[string]int{"custom:5368": 1}},
		{"a custom emoji removed", []tg.ReactionType{custom("5368"), emoji("👍")}, []tg.ReactionType{emoji("👍")}, map[string]int{"custom:5368": -1}},
		{"a paid reaction is not counted", []tg.ReactionType{paid}, []tg.ReactionType{paid, emoji("👍")}, map[string]int{"👍": 1}},
		{"a custom emoji with no id is not counted", nil, []tg.ReactionType{{Type: tg.ReactionCustomEmoji}}, nil},
		{"HND-21: nothing changed", []tg.ReactionType{emoji("🔥")}, []tg.ReactionType{emoji("🔥")}, nil},
		{"HND-21: nothing at all", nil, nil, nil},
		{"only a paid reaction changed", nil, []tg.ReactionType{paid}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t)
			h.deliver(tg.Update{UpdateID: 1, MessageReaction: &tg.MessageReactionUpdated{
				Chat: groupChat(), MessageID: 12, OldReaction: tt.old, NewReaction: tt.new,
			}})
			if tt.want == nil {
				if len(h.store.deltas) != 0 {
					t.Fatalf("deltas %+v, want no call", h.store.deltas)
				}
				return
			}
			if len(h.store.deltas) != 1 {
				t.Fatalf("deltas %+v", h.store.deltas)
			}
			got := h.store.deltas[0]
			if got.Chat != groupID || got.Message != 12 || !reflect.DeepEqual(got.Counts, tt.want) {
				t.Fatalf("delta %+v, want %v", got, tt.want)
			}
		})
	}

	t.Run("an anonymous reaction counts too", func(t *testing.T) {
		h := newHarness(t)
		h.deliver(tg.Update{UpdateID: 1, MessageReaction: &tg.MessageReactionUpdated{
			Chat: groupChat(), MessageID: 12, ActorChat: &tg.Chat{ID: -100777}, NewReaction: []tg.ReactionType{emoji("👍")},
		}})
		if len(h.store.deltas) != 1 {
			t.Fatal("not counted")
		}
	})

	t.Run("a failing store is an error", func(t *testing.T) {
		h := newHarness(t)
		h.store.errs["ApplyDelta"] = errBoom()
		h.deliver(tg.Update{UpdateID: 1, MessageReaction: &tg.MessageReactionUpdated{Chat: groupChat(), MessageID: 1, NewReaction: []tg.ReactionType{emoji("👍")}}})
		if got := h.adapter.alertKeys(); len(got) != 1 || got[0] != "update:message_reaction" {
			t.Fatalf("alerts %v", got)
		}
	})
}

// Doc 7.2 F1: for a chat with anonymous reactions the count is total_count (the
// Python code read .count, which does not exist), and the totals replace what is
// stored.
func TestReactionCount(t *testing.T) {
	h := newHarness(t)
	h.deliver(tg.Update{UpdateID: 1, MessageReactionCount: &tg.MessageReactionCountUpdated{
		Chat: groupChat(), MessageID: 12,
		Reactions: []tg.ReactionCount{
			{Type: emoji("🔥"), TotalCount: 4},
			{Type: custom("99"), TotalCount: 2},
			{Type: tg.ReactionType{Type: tg.ReactionPaid}, TotalCount: 7},
		},
	}})
	if len(h.store.absolutes) != 1 {
		t.Fatalf("absolutes %+v", h.store.absolutes)
	}
	got := h.store.absolutes[0]
	if got.Chat != groupID || got.Message != 12 || !reflect.DeepEqual(got.Counts, map[string]int{"🔥": 4, "custom:99": 2}) {
		t.Fatalf("sync %+v", got)
	}

	t.Run("no reactions left clears the message", func(t *testing.T) {
		h := newHarness(t)
		h.deliver(tg.Update{UpdateID: 1, MessageReactionCount: &tg.MessageReactionCountUpdated{Chat: groupChat(), MessageID: 12}})
		if len(h.store.absolutes) != 1 || len(h.store.absolutes[0].Counts) != 0 {
			t.Fatalf("absolutes %+v", h.store.absolutes)
		}
	})
	t.Run("a failing store is an error", func(t *testing.T) {
		h := newHarness(t)
		h.store.errs["SyncAbsolute"] = errBoom()
		h.deliver(tg.Update{UpdateID: 1, MessageReactionCount: &tg.MessageReactionCountUpdated{Chat: groupChat(), MessageID: 12}})
		if got := h.adapter.alertKeys(); len(got) != 1 || got[0] != "update:message_reaction_count" {
			t.Fatalf("alerts %v", got)
		}
	})
}

// aiogram dropped updates no handler was registered for; an update of a kind the bot does not
// ask for (or an empty one) does nothing here either.
func TestUnknownUpdateIsIgnored(t *testing.T) {
	h := newHarness(t)
	h.deliver(tg.Update{UpdateID: 1})
	if h.api.count() != 0 || len(h.adapter.sent) != 0 || len(h.adapter.alertKeys()) != 0 {
		t.Fatal("an update of no known kind did something")
	}
}

// The Python check ran on message.text only, so a file with a link as its caption
// is stored.
func TestLinkOnlyNeverMatchesAFile(t *testing.T) {
	m := tg.Message{Text: "", Caption: "https://example.com", Photo: []tg.Photo{{FileID: "f"}}}
	if isLinkOnly(&m) {
		t.Fatal("a photo with a link caption was taken for a bare link")
	}
}
