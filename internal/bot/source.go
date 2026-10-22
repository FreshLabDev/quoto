// SPDX-License-Identifier: Apache-2.0

package bot

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/FreshLabDev/tg"

	"github.com/FreshLabDev/quoto/internal/domain"
)

// sourceFromMessage reads the file a message carries, in the order the Python
// version asked: photo, video, animation, video note, voice, audio, sticker,
// document. An animation comes before a document because Telegram fills both
// for a GIF. Nil means the message carries no file.
func sourceFromMessage(m *tg.Message) *domain.MediaSource {
	switch {
	case len(m.Photo) > 0:
		// The largest size; on a tie the later one, like a stable sort that
		// takes the last element.
		best := m.Photo[0]
		for _, p := range m.Photo[1:] {
			if p.FileSize >= best.FileSize {
				best = p
			}
		}
		return &domain.MediaSource{
			Kind: domain.ContentPhoto, FileID: best.FileID, FileUniqueID: best.FileUniqueID,
			MimeType: "image/jpeg", FileSize: best.FileSize, Width: best.Width, Height: best.Height,
			SupportsAnalysis: true,
		}
	case m.Video != nil:
		v := m.Video
		return &domain.MediaSource{
			Kind: domain.ContentVideo, FileID: v.FileID, FileUniqueID: v.FileUniqueID,
			MimeType: orElse(v.MimeType, "video/mp4"), FileName: v.FileName, FileSize: v.FileSize,
			Width: v.Width, Height: v.Height, Duration: float64(v.Duration), SupportsAnalysis: true,
		}
	case m.Animation != nil:
		a := m.Animation
		return &domain.MediaSource{
			Kind: domain.ContentAnimation, FileID: a.FileID, FileUniqueID: a.FileUniqueID,
			MimeType: orElse(a.MimeType, "video/mp4"), FileName: a.FileName, FileSize: a.FileSize,
			Width: a.Width, Height: a.Height, Duration: float64(a.Duration), SupportsAnalysis: true,
		}
	case m.VideoNote != nil:
		n := m.VideoNote
		return &domain.MediaSource{
			Kind: domain.ContentVideoNote, FileID: n.FileID, FileUniqueID: n.FileUniqueID,
			MimeType: "video/mp4", FileSize: n.FileSize, Width: n.Length, Height: n.Length,
			Duration: float64(n.Duration), SupportsAnalysis: true,
		}
	case m.Voice != nil:
		v := m.Voice
		return &domain.MediaSource{
			Kind: domain.ContentVoice, FileID: v.FileID, FileUniqueID: v.FileUniqueID,
			MimeType: orElse(v.MimeType, "audio/ogg"), FileSize: v.FileSize,
			Duration: float64(v.Duration), SupportsAnalysis: true,
		}
	case m.Audio != nil:
		a := m.Audio
		return &domain.MediaSource{
			Kind: domain.ContentAudio, FileID: a.FileID, FileUniqueID: a.FileUniqueID,
			MimeType: orElse(a.MimeType, "audio/mpeg"), FileName: a.FileName, FileSize: a.FileSize,
			Duration: float64(a.Duration), SupportsAnalysis: true,
		}
	case m.Sticker != nil:
		s := m.Sticker
		mime := "image/webp"
		switch {
		case s.IsVideo:
			mime = "video/webm"
		case s.IsAnimated:
			mime = "application/x-tgsticker"
		}
		return &domain.MediaSource{
			Kind: domain.ContentSticker, FileID: s.FileID, FileUniqueID: s.FileUniqueID,
			MimeType: mime, FileSize: s.FileSize, Width: s.Width, Height: s.Height,
			SupportsAnalysis: !s.IsAnimated || s.IsVideo,
		}
	case m.Document != nil:
		d := m.Document
		mime := orElse(d.MimeType, "application/octet-stream")
		isImage := strings.HasPrefix(mime, "image/")
		kind := domain.ContentDocument
		if isImage {
			kind = domain.ContentImage
		}
		return &domain.MediaSource{
			Kind: kind, FileID: d.FileID, FileUniqueID: d.FileUniqueID, MimeType: mime,
			FileName: d.FileName, FileSize: d.FileSize, SupportsAnalysis: isImage,
		}
	}
	return nil
}

func orElse(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}

// contentTypes are the names aiogram's Message.content_type returns (aiogram
// 3.31.0, 79 of them), in the order it tests them. A message with a caption but
// no file the bot knows is stored under the first of these that it has, so its
// content_type is the one the Python bot stored.
var contentTypes = []string{
	"text", "audio", "animation", "document", "game", "live_photo", "photo", "sticker", "video", "video_note",
	"voice", "checklist", "contact", "venue", "location", "new_chat_members", "left_chat_member",
	"chat_owner_left", "chat_owner_changed", "invoice", "successful_payment", "users_shared",
	"connected_website", "migrate_from_chat_id", "migrate_to_chat_id", "pinned_message", "new_chat_title",
	"new_chat_photo", "delete_chat_photo", "group_chat_created", "supergroup_chat_created",
	"channel_chat_created", "paid_media", "passport_data", "proximity_alert_triggered", "poll", "dice",
	"message_auto_delete_timer_changed", "forum_topic_created", "forum_topic_edited", "forum_topic_closed",
	"forum_topic_reopened", "general_forum_topic_hidden", "general_forum_topic_unhidden", "giveaway_created",
	"giveaway", "giveaway_completed", "giveaway_winners", "video_chat_scheduled", "video_chat_started",
	"video_chat_ended", "video_chat_participants_invited", "web_app_data", "user_shared", "chat_shared",
	"story", "write_access_allowed", "chat_background_set", "boost_added", "checklist_tasks_done",
	"checklist_tasks_added", "direct_message_price_changed", "refunded_payment", "gift", "unique_gift",
	"gift_upgrade_sent", "paid_message_price_changed", "suggested_post_approved",
	"suggested_post_approval_failed", "suggested_post_declined", "suggested_post_paid",
	"suggested_post_refunded", "managed_bot_created", "poll_option_added", "poll_option_deleted",
	"rich_message", "community_chat_added", "community_chat_removed", "community_chat_joined",
}

// rawContentType finds the first of contentTypes present in the message as
// Telegram sent it. tg models only the fields the family uses, so the rest are
// read from Raw.
func rawContentType(raw json.RawMessage) string {
	if len(raw) == 0 {
		return domain.ContentUnknown
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return domain.ContentUnknown
	}
	for _, name := range contentTypes {
		switch string(fields[name]) {
		case "", "null", "false", `""`:
			continue
		}
		return name
	}
	return domain.ContentUnknown
}

// contentType is messages.content_type: "text" for a message with text, the kind
// of its file, else what the service message is.
func contentType(m *tg.Message, src *domain.MediaSource) string {
	if m.Text != "" {
		return domain.ContentText
	}
	if src != nil {
		return src.Kind
	}
	return rawContentType(m.Raw)
}

// caption is messages.caption: the stripped caption, empty for none.
func caption(m *tg.Message) string { return domain.PyStrip(m.Caption) }

// initialText is messages.text when a message is stored or edited: the text; else
// for a file, its caption and a description from its metadata; else the caption.
// A message with none of them is not stored (see hasContent), so the result is
// empty only for a message nobody keeps.
func initialText(m *tg.Message, src *domain.MediaSource) string {
	if text := domain.PyStrip(m.Text); text != "" {
		return text
	}
	capt := caption(m)
	if src != nil {
		return domain.CanonicalText(src.Kind, capt, domain.MetadataDescription(*src))
	}
	return capt
}

// hasContent says whether a message has anything to quote: text, a caption or a
// file. A join, a leave, a pin, a poll or a dice has none. The Python bot stored
// those with a placeholder text, counted them towards the day's minimum and sent
// the placeholders to the model; a quiet day with a few joins then cost an AI
// call (doc 7.2 F14).
func hasContent(m *tg.Message, src *domain.MediaSource) bool {
	return src != nil || domain.PyStrip(m.Text) != "" || caption(m) != ""
}

func replyTo(m *tg.Message) int64 {
	if m.ReplyToMessage == nil {
		return 0
	}
	return m.ReplyToMessage.MessageID
}

// newMessage is the row to store for m. fallback stands for the time when
// Telegram sent none.
func newMessage(m *tg.Message, fallback time.Time) domain.NewMessage {
	src := sourceFromMessage(m)
	created := fallback
	if m.Date != 0 {
		created = time.Unix(m.Date, 0).UTC()
	}
	var userID int64
	if m.From != nil {
		userID = m.From.ID
	}
	return domain.NewMessage{
		MessageID:   m.MessageID,
		ChatID:      m.Chat.ID,
		UserID:      userID,
		Text:        initialText(m, src),
		ContentType: contentType(m, src),
		Caption:     caption(m),
		ReplyTo:     replyTo(m),
		CreatedAt:   created,
		Media:       src,
	}
}

// messageEdit is the update to apply for an edited message. A message with a
// file keeps its stored text: that text holds the description the media pipeline
// wrote, and the edit carries only the placeholder (doc 7.2 F11).
func messageEdit(m *tg.Message) domain.MessageEdit {
	src := sourceFromMessage(m)
	return domain.MessageEdit{
		MessageID:   m.MessageID,
		ChatID:      m.Chat.ID,
		Text:        initialText(m, src),
		ContentType: contentType(m, src),
		Caption:     caption(m),
		ReplyTo:     replyTo(m),
		KeepText:    src != nil,
	}
}
