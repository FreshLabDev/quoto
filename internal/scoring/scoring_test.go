// SPDX-License-Identifier: Apache-2.0

package scoring

import (
	"reflect"
	"strings"
	"testing"

	"github.com/FreshLabDev/quoto/internal/domain"
)

// msg builds a stored text message the way tests/test_scoring.py::_message
// did: the author's user id is the internal id plus 1000.
func msg(id, telegramID int64, text string) domain.WindowMessage {
	return domain.WindowMessage{
		ID: id, MessageID: telegramID, UserID: id + 1000, Text: text, ContentType: domain.ContentText,
	}
}

func ids(ms []domain.WindowMessage) []int64 {
	out := make([]int64, 0, len(ms))
	for _, m := range ms {
		out = append(out, m.ID)
	}
	return out
}

func payloadIDs(p []domain.EvalMessage) []int64 {
	out := make([]int64, 0, len(p))
	for _, m := range p {
		out = append(out, m.ID)
	}
	return out
}

func scores(pairs ...float64) []domain.Score {
	var out []domain.Score
	for i := 0; i+1 < len(pairs); i += 2 {
		out = append(out, domain.Score{ID: int64(pairs[i]), Value: pairs[i+1]})
	}
	return out
}

// tests/test_scoring.py::test_score_breakdown_total_uses_ai_only
func TestBreakdownTotalIsTheAIScore(t *testing.T) {
	b := Breakdown{AI: 0.2, ReactionCount: 40}
	if b.Total() != 0.2 {
		t.Fatalf("total = %v, want 0.2: reactions must not count", b.Total())
	}
}

// tests/test_scoring.py::test_resolved_message_text_prefers_ai_description_over_stale_text
// tests/test_scoring.py::test_resolved_message_text_falls_back_to_raw_text_for_plain_messages
func TestResolvedText(t *testing.T) {
	photo := msg(1, 11, "photo: Файл: photo, mime=image/jpeg, size=45388")
	photo.ContentType = domain.ContentPhoto
	photo.Media = &domain.MediaSnapshot{Description: "На экране приложения видно профиль пользователя."}

	captioned := photo
	captioned.Caption = "смотри"

	undescribed := photo
	undescribed.Media = &domain.MediaSnapshot{Status: domain.MediaPending}

	tests := []struct {
		name string
		m    domain.WindowMessage
		want string
	}{
		{"description beats the stale placeholder", photo, "photo: На экране приложения видно профиль пользователя."},
		{"caption comes first", captioned, "смотри\nphoto: На экране приложения видно профиль пользователя."},
		{"plain text", msg(1, 11, "обычный текст"), "обычный текст"},
		{"media without a description keeps the stored text", undescribed, "photo: Файл: photo, mime=image/jpeg, size=45388"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ResolvedText(tt.m); got != tt.want {
				t.Errorf("ResolvedText = %q, want %q", got, tt.want)
			}
		})
	}
}

// tests/test_scoring.py::test_pick_best_quote_sends_reactions_context_and_uses_ai_winner
func TestPayloadReactionsAndAuthors(t *testing.T) {
	a := msg(1, 11, "popular but weaker")
	a.Reactions = []domain.Reaction{{Emoji: "😂", Count: 3}, {Emoji: "❤️", Count: 1}, {Emoji: "👎", Count: 0}}
	b := msg(2, 12, "better AI quote")
	day := Prepare([]domain.WindowMessage{a, b}, 0)

	payload := day.Payload(map[int64]string{1001: "Alice"})

	if want := []domain.Reaction{{Emoji: "😂", Count: 3}, {Emoji: "❤️", Count: 1}}; !reflect.DeepEqual(payload[0].Reactions, want) {
		t.Errorf("reactions = %v, want %v (a zero count is left out, order kept)", payload[0].Reactions, want)
	}
	if payload[1].Reactions != nil {
		t.Errorf("a message without reactions must have none, got %v", payload[1].Reactions)
	}
	if payload[0].Author != "Alice" {
		t.Errorf("author = %q", payload[0].Author)
	}
	if payload[1].Author != "Unknown" {
		t.Errorf("a person without a name is %q to the model, want Unknown (doc §7.2 F12)", payload[1].Author)
	}
	if payload[0].Text != "popular but weaker" || payload[0].Kind != "text" {
		t.Errorf("text message = %+v", payload[0])
	}

	res := domain.EvalResult{Scores: scores(1, 0.1, 2, 0.9), ActualModel: "openrouter/test"}
	ev := Pick(day, res)
	if ev.Best == nil || ev.Best.ID != 2 || ev.Breakdown.Total() != 0.9 {
		t.Fatalf("best = %+v, total %v", ev.Best, ev.Breakdown.Total())
	}
	if ev.Breakdown.Model != "openrouter/test" {
		t.Errorf("model = %q", ev.Breakdown.Model)
	}
}

// tests/test_scoring.py::test_pick_best_quote_filters_only_exact_quoto_hashtag_before_ai
func TestPrepareFiltersOnlyTheExactHashtag(t *testing.T) {
	source := []domain.WindowMessage{
		msg(1, 11, "previous report #quoto"),
		msg(2, 12, "plain quoto word stays"),
		msg(3, 13, "Подробности дня #60 stays"),
		msg(4, 14, "other #quotoday stays"),
		msg(5, 15, "mixed #QuOtO punctuation"),
	}
	day := Prepare(source, 0)

	if got := payloadIDs(day.Payload(nil)); !reflect.DeepEqual(got, []int64{2, 3, 4}) {
		t.Fatalf("payload ids = %v, want [2 3 4]", got)
	}
	if day.SourceCount != 5 || day.Eligible != 3 || day.Truncated {
		t.Errorf("day = %+v", day)
	}
	ev := Pick(day, domain.EvalResult{Scores: scores(2, 0.6, 3, 0.7, 4, 0.8)})
	if ev.Best == nil || ev.Best.ID != 4 {
		t.Errorf("best = %+v, want message 4", ev.Best)
	}
}

// tests/test_scoring.py::test_pick_best_quote_skips_ai_when_all_messages_filtered
func TestPrepareEverythingFiltered(t *testing.T) {
	day := Prepare([]domain.WindowMessage{msg(1, 11, "#quoto"), msg(2, 12, "daily card #QUOTO")}, 1500)
	if len(day.Messages) != 0 || day.Eligible != 0 || day.SourceCount != 2 {
		t.Fatalf("day = %+v: want no messages left of 2", day)
	}
}

// doc §6: Python semantics (#quoto filter with Unicode word boundaries by hand).
func TestContainsQuotoHashtag(t *testing.T) {
	tests := []struct {
		text string
		want bool
	}{
		{"#quoto", true},
		{"#QUOTO", true},
		{"#QuOtO", true},
		{"daily card #quoto", true},
		{"#quoto is here", true},
		{"(#quoto)", true},
		{"#quoto.", true},
		{"#quoto, #quoto!", true},
		{"two tags #quotox #quoto", true},
		{"a#quoto #quoto", true},
		{"quoto", false},
		{"# quoto", false},
		{"#quot", false},
		{"#quotoday", false},
		{"#quoto_day", false},
		{"#quoto2", false},
		{"a#quoto", false},
		{"x#quoto", false},
		{"_#quoto", false},
		{"7#quoto", false},
		{"##quoto", true},
		{"", false},
		// \w is Unicode: a Cyrillic or CJK letter touching the tag makes it part of a word.
		{"слово#quoto", false},
		{"#quotoслово", false},
		{"#quoto日本", false},
		{"привет #quoto пока", true},
		// Letters of other scripts, digits of other scripts, a combining mark.
		{"٣#quoto", false},
		{"#quoto٣", false},
		{"é#quoto", false},
		{"#quotó", true},
		{"Подробности дня #60 stays", false},
	}
	for _, tt := range tests {
		t.Run(tt.text, func(t *testing.T) {
			if got := ContainsQuotoHashtag(tt.text); got != tt.want {
				t.Errorf("ContainsQuotoHashtag(%q) = %t, want %t", tt.text, got, tt.want)
			}
		})
	}
}

// Only text is checked, not the caption (doc inventory 03 §1.5.3).
func TestPrepareChecksTextNotCaption(t *testing.T) {
	m := msg(1, 11, "photo: a cat")
	m.ContentType = domain.ContentPhoto
	m.Caption = "my #quoto photo"
	if day := Prepare([]domain.WindowMessage{m}, 0); len(day.Messages) != 1 {
		t.Fatalf("a caption with the tag must not filter the message: %+v", day)
	}
}

// tests/test_scoring.py::test_pick_best_quote_sends_internal_reply_id_and_media_description
func TestPayloadReplyKindCaptionAndDescription(t *testing.T) {
	setup := msg(1, 11, "setup")
	photo := msg(2, 12, "photo: человек показывает табличку")
	photo.ContentType = domain.ContentPhoto
	photo.Caption = "смотри"
	photo.ReplyTo = 11
	photo.Media = &domain.MediaSnapshot{Description: "человек показывает табличку"}
	orphan := msg(3, 13, "voice: Файл: voice")
	orphan.ContentType = domain.ContentVoice
	orphan.ReplyTo = 999 // a message of an earlier day

	payload := Prepare([]domain.WindowMessage{setup, photo, orphan}, 0).Payload(nil)

	want := domain.EvalMessage{ID: 2, Author: "Unknown", Kind: "photo", Caption: "смотри", Desc: "человек показывает табличку", ReplyToID: 1}
	if !reflect.DeepEqual(payload[1], want) {
		t.Errorf("photo payload = %+v, want %+v", payload[1], want)
	}
	if payload[1].Text != "" {
		t.Errorf("a photo carries no text field, got %q", payload[1].Text)
	}
	if payload[2].ReplyToID != 0 {
		t.Errorf("a reply to a message outside the payload has no reply_to_id, got %d", payload[2].ReplyToID)
	}
	if payload[2].Desc != "voice: Файл: voice" {
		t.Errorf("a media message without a description sends its stored text as desc, got %q", payload[2].Desc)
	}
	if payload[0].Desc != "" || payload[0].Text != "setup" {
		t.Errorf("a text message sends text only: %+v", payload[0])
	}
}

// tests/test_scoring.py::test_pick_best_quote_ai_best_text_uses_media_description_not_stale_text
func TestPickBestTextUsesMediaDescription(t *testing.T) {
	normal := msg(1, 11, "normal text")
	photo := msg(2, 12, "photo: Файл: photo, mime=image/jpeg, size=45388")
	photo.ContentType = domain.ContentPhoto
	photo.Media = &domain.MediaSnapshot{Description: "скриншот профиля в приложении знакомств"}
	day := Prepare([]domain.WindowMessage{normal, photo}, 0)

	res := domain.EvalResult{
		Scores:      scores(1, 0.9, 2, 0.95),
		ActualModel: "openrouter/test",
		QuoteChoice: &domain.QuoteChoice{PrimaryID: 1},
	}
	ev := Pick(day, res)

	if ev.Best == nil || ev.Best.ID != 1 {
		t.Fatalf("best = %+v, want message 1", ev.Best)
	}
	if want := "photo: скриншот профиля в приложении знакомств"; ev.Breakdown.BestText != want {
		t.Errorf("BestText = %q, want %q", ev.Breakdown.BestText, want)
	}
}

func TestPickBestTextEmptyWhenModelAndScoresAgree(t *testing.T) {
	day := Prepare([]domain.WindowMessage{msg(1, 11, "a"), msg(2, 12, "b")}, 0)
	for name, choice := range map[string]*domain.QuoteChoice{"no choice": nil, "same pick": {PrimaryID: 2}} {
		ev := Pick(day, domain.EvalResult{Scores: scores(1, 0.1, 2, 0.9), QuoteChoice: choice})
		if ev.Best.ID != 2 || ev.Breakdown.BestText != "" {
			t.Errorf("%s: best %d, BestText %q", name, ev.Best.ID, ev.Breakdown.BestText)
		}
	}
}

// tests/test_scoring.py::test_pick_best_quote_does_not_allow_sticker_as_primary
func TestPickStickerIsNeverThePrimary(t *testing.T) {
	normal := msg(1, 11, "normal text")
	sticker := msg(2, 12, "sticker: смешной стикер")
	sticker.ContentType = domain.ContentSticker
	sticker.Media = &domain.MediaSnapshot{Description: "смешной стикер"}
	day := Prepare([]domain.WindowMessage{normal, sticker}, 0)

	res := domain.EvalResult{
		Scores:      scores(1, 0.4, 2, 0.95),
		QuoteChoice: &domain.QuoteChoice{PrimaryID: 2, ContextIDs: []int64{1, 2}, ContextNeeded: true},
	}
	ev := Pick(day, res)

	if ev.Best == nil || ev.Best.ID != 1 {
		t.Fatalf("best = %+v, want the best non-sticker (1)", ev.Best)
	}
	if got := ids(ev.Context); !reflect.DeepEqual(got, []int64{1, 2}) {
		t.Errorf("context = %v, want [1 2]: a sticker may be context", got)
	}
	if ev.Breakdown.AI != 0.4 {
		t.Errorf("score = %v, want the pick's own score 0.4", ev.Breakdown.AI)
	}
	if ev.Breakdown.BestText != "" {
		t.Errorf("the best non-sticker is the pick, BestText = %q", ev.Breakdown.BestText)
	}
}

// doc inventory 03 §1.5.10-11: primary resolution.
func TestPickResolvesThePrimary(t *testing.T) {
	sticker := msg(3, 13, "sticker: x")
	sticker.ContentType = domain.ContentSticker
	day := Prepare([]domain.WindowMessage{msg(1, 11, "a"), msg(2, 12, "b"), sticker, msg(4, 14, "d")}, 0)

	tests := []struct {
		name     string
		res      domain.EvalResult
		wantBest int64 // 0: nobody
		wantAI   float64
	}{
		{"the model's pick beats the top score", domain.EvalResult{
			Scores: scores(1, 0.2, 2, 0.9, 4, 0.5), QuoteChoice: &domain.QuoteChoice{PrimaryID: 1}}, 1, 0.2},
		{"no pick: the top score", domain.EvalResult{Scores: scores(1, 0.2, 2, 0.9, 4, 0.5)}, 2, 0.9},
		{"a tie goes to the first the model listed", domain.EvalResult{Scores: scores(4, 0.7, 1, 0.7, 2, 0.7)}, 4, 0.7},
		{"a pick that is not a message of the day falls back", domain.EvalResult{
			Scores: scores(1, 0.2, 2, 0.9), QuoteChoice: &domain.QuoteChoice{PrimaryID: 99}}, 2, 0.9},
		{"a zero pick means none", domain.EvalResult{
			Scores: scores(1, 0.2, 2, 0.9), QuoteChoice: &domain.QuoteChoice{}}, 2, 0.9},
		{"a sticker with the top score is skipped", domain.EvalResult{Scores: scores(3, 0.99, 1, 0.1)}, 1, 0.1},
		{"a scored id outside the day is ignored", domain.EvalResult{Scores: scores(77, 1.0, 4, 0.3)}, 4, 0.3},
		{"a pick without a score gets the neutral 0.5", domain.EvalResult{
			Scores: scores(2, 0.9), QuoteChoice: &domain.QuoteChoice{PrimaryID: 1}}, 1, 0.5},
		{"a repeated id: the later score, the first position", domain.EvalResult{
			Scores: scores(1, 0.1, 2, 0.8, 1, 0.8)}, 1, 0.8},
		{"only a sticker was scored: nobody", domain.EvalResult{Scores: scores(3, 0.9)}, 0, 0},
		{"no scores and no pick: nobody", domain.EvalResult{}, 0, 0},
		{"the model's pick works without any score", domain.EvalResult{QuoteChoice: &domain.QuoteChoice{PrimaryID: 4}}, 4, 0.5},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ev := Pick(day, tt.res)
			if tt.wantBest == 0 {
				if ev.Best != nil {
					t.Fatalf("best = %d, want nobody", ev.Best.ID)
				}
				if len(ev.Context) != 0 {
					t.Errorf("context without a quote: %v", ids(ev.Context))
				}
				return
			}
			if ev.Best == nil || ev.Best.ID != tt.wantBest {
				t.Fatalf("best = %+v, want %d", ev.Best, tt.wantBest)
			}
			if ev.Breakdown.AI != tt.wantAI {
				t.Errorf("score = %v, want %v", ev.Breakdown.AI, tt.wantAI)
			}
		})
	}
}

// With every message unscored (the AI failed: all 0.5 in the evaluator's reply
// order, which is the chronological one) the first non-sticker wins.
func TestPickAfterFailedRunTakesTheFirstMessage(t *testing.T) {
	sticker := msg(1, 11, "sticker: x")
	sticker.ContentType = domain.ContentSticker
	day := Prepare([]domain.WindowMessage{sticker, msg(2, 12, "a"), msg(3, 13, "b")}, 0)
	res := domain.EvalResult{
		Scores:          scores(1, 0.5, 2, 0.5, 3, 0.5),
		Status:          domain.RunAIFailed,
		DayVerdictError: "AI request failed",
	}
	ev := Pick(day, res)
	if ev.Best == nil || ev.Best.ID != 2 {
		t.Fatalf("best = %+v, want 2", ev.Best)
	}
	if ev.VerdictError != "AI request failed" || !ev.ShouldPublish {
		t.Errorf("verdict error %q, ShouldPublish %t: with no verdict the day is not called boring", ev.VerdictError, ev.ShouldPublish)
	}
}

func TestPickCarriesTheVerdictAndLanguage(t *testing.T) {
	day := Prepare([]domain.WindowMessage{msg(1, 11, "a")}, 0)
	res := domain.EvalResult{
		Scores:         scores(1, 0.4),
		DayVerdict:     &domain.DayVerdict{ShouldPublish: false, ReasonCode: domain.ReasonBoringDay, ReasonText: "flat day"},
		LanguageChoice: &domain.LanguageChoice{ChatLanguage: "Ukrainian", InterfaceLanguage: "uk"},
	}
	ev := Pick(day, res)
	if ev.ShouldPublish || ev.ReasonCode != "boring_day" || ev.ReasonText != "flat day" {
		t.Errorf("verdict = %t %q %q", ev.ShouldPublish, ev.ReasonCode, ev.ReasonText)
	}
	if ev.InterfaceLanguage != "uk" || ev.ChatLanguage != "Ukrainian" {
		t.Errorf("language = %q %q", ev.InterfaceLanguage, ev.ChatLanguage)
	}
	if ev.Best == nil {
		t.Fatal("a boring day still has its best message: the row keeps it")
	}
}

func TestPickReactionCountIsTheSumOverAllEmoji(t *testing.T) {
	m := msg(1, 11, "a")
	m.Reactions = []domain.Reaction{{Emoji: "😂", Count: 3}, {Emoji: "❤️", Count: 1}}
	ev := Pick(Prepare([]domain.WindowMessage{m, msg(2, 12, "b")}, 0), domain.EvalResult{Scores: scores(1, 0.9)})
	if ev.Breakdown.ReactionCount != 4 {
		t.Errorf("ReactionCount = %d, want 4", ev.Breakdown.ReactionCount)
	}
}

// tests/test_scoring.py::test_pick_best_quote_truncates_to_max_messages_and_flags
// tests/test_scoring.py::test_pick_best_quote_no_truncation_within_cap
func TestPrepareCapKeepsTheNewestMessages(t *testing.T) {
	var source []domain.WindowMessage
	for i := int64(1); i <= 6; i++ {
		source = append(source, msg(i, 100+i, "line"))
	}
	tests := []struct {
		name          string
		max           int
		wantIDs       []int64
		wantTruncated bool
	}{
		{"cut to the last two", 2, []int64{5, 6}, true},
		{"exactly at the cap", 6, []int64{1, 2, 3, 4, 5, 6}, false},
		{"far below the cap", 1500, []int64{1, 2, 3, 4, 5, 6}, false},
		{"zero is no cap (premium)", 0, []int64{1, 2, 3, 4, 5, 6}, false},
		{"negative is no cap", -1, []int64{1, 2, 3, 4, 5, 6}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			day := Prepare(source, tt.max)
			if got := ids(day.Messages); !reflect.DeepEqual(got, tt.wantIDs) {
				t.Errorf("ids = %v, want %v", got, tt.wantIDs)
			}
			if day.Truncated != tt.wantTruncated || day.Eligible != 6 || day.SourceCount != 6 {
				t.Errorf("day = {Truncated:%t Eligible:%d Source:%d}", day.Truncated, day.Eligible, day.SourceCount)
			}
		})
	}
}

// The total in the post's note is counted after the #quoto filter.
func TestPrepareCapCountsAfterTheFilter(t *testing.T) {
	source := []domain.WindowMessage{msg(1, 11, "a"), msg(2, 12, "#quoto"), msg(3, 13, "c"), msg(4, 14, "d")}
	day := Prepare(source, 2)
	if got := ids(day.Messages); !reflect.DeepEqual(got, []int64{3, 4}) || !day.Truncated || day.Eligible != 3 || day.SourceCount != 4 {
		t.Fatalf("day = %+v ids %v", day, got)
	}
}

// tests/test_scoring.py::test_pick_best_quote_uses_ai_primary_and_valid_contiguous_context
// tests/test_scoring.py::test_context_validator_accepts_reply_connected_noncontiguous_thread
// tests/test_scoring.py::test_context_validator_accepts_ai_selected_noncontiguous_messages
// tests/test_scoring.py::test_context_validator_limits_ai_context_around_primary
// tests/test_scoring.py::test_context_validator_repairs_primary_and_filters_unknown_ids
// doc inventory 03 §1.5.15.
func TestValidContext(t *testing.T) {
	var day []domain.WindowMessage
	for i := int64(1); i <= 7; i++ {
		day = append(day, msg(i, 10+i, "m"))
	}
	tests := []struct {
		name    string
		primary int64
		choice  *domain.QuoteChoice
		want    []int64
	}{
		{"contiguous, primary last", 3, &domain.QuoteChoice{ContextIDs: []int64{1, 2, 3}, ContextNeeded: true}, []int64{1, 2, 3}},
		{"reply-connected, not contiguous", 5, &domain.QuoteChoice{ContextIDs: []int64{1, 3, 5}, ContextNeeded: true}, []int64{1, 3, 5}},
		{"the model's choice, not contiguous", 3, &domain.QuoteChoice{ContextIDs: []int64{1, 3}, ContextNeeded: true}, []int64{1, 3}},
		{"seven around the fourth: five around it", 4, &domain.QuoteChoice{ContextIDs: []int64{1, 2, 3, 4, 5, 6, 7}, ContextNeeded: true}, []int64{2, 3, 4, 5, 6}},
		{"primary at the end: the last five", 6, &domain.QuoteChoice{ContextIDs: []int64{1, 2, 3, 4, 5, 6}, ContextNeeded: true}, []int64{2, 3, 4, 5, 6}},
		{"primary at the start: the first five", 1, &domain.QuoteChoice{ContextIDs: []int64{1, 2, 3, 4, 5, 6, 7}, ContextNeeded: true}, []int64{1, 2, 3, 4, 5}},
		{"primary one from the start", 2, &domain.QuoteChoice{ContextIDs: []int64{1, 2, 3, 4, 5, 6, 7}, ContextNeeded: true}, []int64{1, 2, 3, 4, 5}},
		{"primary missing from the ids is appended", 6, &domain.QuoteChoice{ContextIDs: []int64{1, 2, 3}, ContextNeeded: true}, []int64{1, 2, 3, 6}},
		{"an id outside the day is dropped", 6, &domain.QuoteChoice{ContextIDs: []int64{4, 5, 999, 6}, ContextNeeded: true}, []int64{4, 5, 6}},
		{"repeats are dropped, the model's order is kept", 3, &domain.QuoteChoice{ContextIDs: []int64{3, 1, 3, 1, 2}, ContextNeeded: true}, []int64{3, 1, 2}},
		{"the model's order is not chronological", 2, &domain.QuoteChoice{ContextIDs: []int64{2, 1}, ContextNeeded: true}, []int64{2, 1}},
		{"no ids and no need: none", 3, &domain.QuoteChoice{}, nil},
		{"ids given, need false: still used", 3, &domain.QuoteChoice{ContextIDs: []int64{1, 3}}, []int64{1, 3}},
		{"need without ids: the primary alone", 3, &domain.QuoteChoice{ContextNeeded: true}, []int64{3}},
		{"every id foreign: the primary alone", 3, &domain.QuoteChoice{ContextIDs: []int64{98, 99}}, []int64{3}},
		{"no choice at all", 3, nil, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ids(validContext(tt.choice, day, find(day, tt.primary)))
			if len(tt.want) == 0 && len(got) == 0 {
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("context = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestPickContextFollowsTheChosenPrimary(t *testing.T) {
	var source []domain.WindowMessage
	for i := int64(1); i <= 4; i++ {
		source = append(source, msg(i, 10+i, "m"))
	}
	res := domain.EvalResult{
		Scores:      scores(1, 0.1, 2, 0.3, 3, 0.8),
		QuoteChoice: &domain.QuoteChoice{PrimaryID: 3, ContextIDs: []int64{1, 2, 3}, ContextNeeded: true},
	}
	ev := Pick(Prepare(source, 0), res)
	if ev.Best.ID != 3 || !reflect.DeepEqual(ids(ev.Context), []int64{1, 2, 3}) {
		t.Fatalf("best %d context %v", ev.Best.ID, ids(ev.Context))
	}
}

// doc §6: Python semantics (stars use banker's rounding), inventory 03 §1.5.14.
func TestStarsRoundHalfToEven(t *testing.T) {
	stars := func(n int) string { return strings.Repeat("★", n) + strings.Repeat("☆", 5-n) }

	// The model answers 0..10; the evaluator divides by 10 and the stars multiply
	// by 5. Values from Python's round(raw/10.0*5).
	wantByRaw := []int{0, 0, 1, 2, 2, 2, 3, 4, 4, 4, 5}
	for raw, want := range wantByRaw {
		total := float64(raw) / 10.0
		if got := (Breakdown{AI: total}).Stars(); got != stars(want) {
			t.Errorf("raw %d (total %v): %s, want %s", raw, total, got, stars(want))
		}
	}
	// Half steps checked against Python by exhaustion: the halves go to the even
	// neighbour, which math.Round would round up.
	halves := map[float64]int{0.5: 0, 1.5: 1, 2.5: 1, 3.5: 2, 4.5: 2, 5.5: 3, 6.5: 3, 7.5: 4, 8.5: 4, 9.5: 5}
	for raw, want := range halves {
		total := raw / 10.0
		if got := (Breakdown{AI: total}).Stars(); got != stars(want) {
			t.Errorf("raw %v (total %v): %s, want %s", raw, total, got, stars(want))
		}
	}
	// Out of range stays on the scale.
	for total, want := range map[float64]int{-0.4: 0, 1.7: 5} {
		if got := (Breakdown{AI: total}).Stars(); got != stars(want) {
			t.Errorf("total %v: %s, want %s", total, got, stars(want))
		}
	}
}
