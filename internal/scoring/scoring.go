// SPDX-License-Identifier: Apache-2.0

// Package scoring turns a day's stored messages into what the model is asked
// about, and the model's answer into the quote to publish: the #quoto filter,
// the cap on how many messages go out, the payload, the pick of the primary
// message, its context and its stars. It does no I/O and holds no state.
package scoring

import (
	"math"
	"strings"
	"unicode"

	"github.com/FreshLabDev/quoto/internal/domain"
)

// MaxContext is the most messages a quote's context holds.
const MaxContext = 5

// unknownAuthor is the author the model sees for a person core has no name for.
// It is not localised: the payload is a prompt, not interface text.
const unknownAuthor = "Unknown"

// neutralScore is what a message gets when the model did not score it.
const neutralScore = 0.5

// Day is a window's messages ready for the model.
type Day struct {
	// Messages are the messages that go to the model, oldest first: the window
	// without #quoto posts, cut to the newest ones if the group has a cap.
	Messages []domain.WindowMessage
	// SourceCount is how many messages the window held before any filtering.
	SourceCount int
	// Eligible is how many were left after the #quoto filter and before the cap.
	// The group's minimum is checked against it.
	Eligible int
	// Truncated says the cap cut messages off; Eligible is then the total the
	// post's note quotes.
	Truncated bool
}

// Prepare filters and caps the window's messages. source must be oldest first,
// as Messages.ListWindow returns it. maxMessages 0 or less means no cap.
func Prepare(source []domain.WindowMessage, maxMessages int) Day {
	kept := make([]domain.WindowMessage, 0, len(source))
	for _, m := range source {
		if !ContainsQuotoHashtag(m.Text) {
			kept = append(kept, m)
		}
	}
	d := Day{SourceCount: len(source), Eligible: len(kept)}
	if maxMessages > 0 && len(kept) > maxMessages {
		kept = kept[len(kept)-maxMessages:]
		d.Truncated = true
	}
	d.Messages = kept
	return d
}

// ContainsQuotoHashtag reports whether text has the #quoto tag the bot puts in
// its own posts, so that a post quoted back into the chat is not scored again.
// It is Python's (?<![\w])#quoto(?![\w]) with IGNORECASE: the tag is matched
// without regard to case, and a letter, digit or underscore on either side
// makes it part of another word. RE2 has no lookaround and its \w is ASCII, so
// the boundary is checked by hand with Unicode letters and numbers, which is
// what Python's \w means for str.
func ContainsQuotoHashtag(text string) bool {
	const tag = "#quoto"
	runes := []rune(text)
	n := len([]rune(tag))
	for i := 0; i+n <= len(runes); i++ {
		if runes[i] != '#' || !strings.EqualFold(string(runes[i:i+n]), tag) {
			continue
		}
		if i > 0 && isWordRune(runes[i-1]) {
			continue
		}
		if i+n < len(runes) && isWordRune(runes[i+n]) {
			continue
		}
		return true
	}
	return false
}

func isWordRune(r rune) bool {
	return r == '_' || unicode.IsLetter(r) || unicode.IsNumber(r)
}

// kind is the content type with the stored default for an empty one.
func kind(m domain.WindowMessage) string {
	if m.ContentType == "" {
		return domain.ContentText
	}
	return m.ContentType
}

// Payload builds the model's input. names maps user ids to display names; a
// person without one is "Unknown" here, though the post and the snapshots say
// "Anonymous" in the group's language. Ids are messages.id.
func (d Day) Payload(names map[int64]string) []domain.EvalMessage {
	byTelegramID := make(map[int64]int64, len(d.Messages))
	for _, m := range d.Messages {
		byTelegramID[m.MessageID] = m.ID
	}
	out := make([]domain.EvalMessage, 0, len(d.Messages))
	for _, m := range d.Messages {
		author, ok := names[m.UserID]
		if !ok {
			author = unknownAuthor
		}
		em := domain.EvalMessage{
			ID:      m.ID,
			Author:  author,
			Kind:    kind(m),
			Caption: m.Caption,
		}
		if em.Kind == domain.ContentText {
			em.Text = m.Text
		}
		em.Desc = mediaDescription(m)
		if m.ReplyTo != 0 {
			em.ReplyToID = byTelegramID[m.ReplyTo]
		}
		for _, r := range m.Reactions {
			if r.Count > 0 {
				em.Reactions = append(em.Reactions, r)
			}
		}
		out = append(out, em)
	}
	return out
}

// mediaDescription is what the model reads in place of a media file: the AI
// description when there is one, else, for a message that is not plain text,
// the stored placeholder.
func mediaDescription(m domain.WindowMessage) string {
	if m.Media != nil && m.Media.Description != "" {
		return m.Media.Description
	}
	if kind(m) != domain.ContentText && m.Text != "" {
		return m.Text
	}
	return ""
}

// ResolvedText is the text a message is quoted with: the canonical
// "caption, then kind: description" text when the media pipeline has described
// it, which beats messages.text that may still hold the pre-analysis
// placeholder; otherwise the stored text.
func ResolvedText(m domain.WindowMessage) string {
	if m.Media != nil && m.Media.Description != "" {
		return domain.CanonicalText(kind(m), m.Caption, m.Media.Description)
	}
	return m.Text
}

// Breakdown is how the picked message scored. The final score is the model's;
// reactions and length no longer take part, and only the reaction count is kept
// for the post and the details card.
type Breakdown struct {
	AI            float64 // 0..1, the model's score for the picked message
	ReactionCount int
	Model         string // the model that actually answered
	// BestText is the text of the message the model scored highest when the
	// model's quote pick is another one; empty when they are the same.
	BestText string
}

// Total is the quote's score.
func (b Breakdown) Total() float64 { return b.AI }

// Stars renders the score as five stars. The count is Python's
// round(total*5): halves go to the even neighbour, so 0.5 stars is none and
// 1.5 is one, which math.Round would turn into one and two.
func (b Breakdown) Stars() string {
	n := 0
	if v := b.AI * 5; !math.IsNaN(v) {
		n = int(math.Max(0, math.Min(5, math.RoundToEven(v))))
	}
	return strings.Repeat("★", n) + strings.Repeat("☆", 5-n)
}

// Evaluation is the model's answer applied to a day.
type Evaluation struct {
	// Best is the picked message; nil when the model scored no eligible one.
	Best      *domain.WindowMessage
	Breakdown Breakdown
	// Context are the messages around the quote, in the model's order, at most
	// MaxContext. It includes Best when it is not empty.
	Context []domain.WindowMessage

	ShouldPublish bool
	ReasonCode    string
	ReasonText    string
	// VerdictError is set when the model gave no usable day verdict. The day is
	// then neither published nor called boring.
	VerdictError string

	// InterfaceLanguage and ChatLanguage are what the model detected, if asked.
	InterfaceLanguage string
	ChatLanguage      string
}

// Pick applies the model's answer to the day. The model's own pick of the
// primary message wins when it is a message of the day and not a sticker;
// otherwise the highest score among the non-stickers does, and on a tie the one
// the model listed first. A sticker can be context, never the quote.
func Pick(d Day, res domain.EvalResult) Evaluation {
	scores, order := scoreTable(res.Scores)

	var fallback *domain.WindowMessage
	var fallbackID int64
	for _, id := range order {
		m := find(d.Messages, id)
		if m == nil || !primaryEligible(*m) {
			continue
		}
		if fallback == nil || scores[id] > scores[fallbackID] {
			fallback, fallbackID = m, id
		}
	}

	best := validPrimary(res.QuoteChoice, d.Messages)
	if best == nil {
		best = fallback
	}

	ev := Evaluation{ShouldPublish: true, VerdictError: res.DayVerdictError}
	if v := res.DayVerdict; v != nil {
		ev.ShouldPublish = v.ShouldPublish
		ev.ReasonCode = v.ReasonCode
		ev.ReasonText = v.ReasonText
	}
	if l := res.LanguageChoice; l != nil {
		ev.InterfaceLanguage = l.InterfaceLanguage
		ev.ChatLanguage = l.ChatLanguage
	}
	if best == nil {
		return ev
	}

	ev.Best = best
	ev.Breakdown = Breakdown{
		AI:            neutralScore,
		ReactionCount: best.ReactionTotal(),
		Model:         res.ActualModel,
	}
	if v, ok := scores[best.ID]; ok {
		ev.Breakdown.AI = v
	}
	if fallback != nil && fallback.ID != best.ID {
		ev.Breakdown.BestText = ResolvedText(*fallback)
	}
	ev.Context = validContext(res.QuoteChoice, d.Messages, best)
	return ev
}

// scoreTable indexes the scores by message id, and lists the ids in the order
// the model gave them. If an id repeats, the later score replaces the earlier
// and the position stays the first, as in a Python dict.
func scoreTable(scores []domain.Score) (map[int64]float64, []int64) {
	table := make(map[int64]float64, len(scores))
	order := make([]int64, 0, len(scores))
	for _, s := range scores {
		if _, seen := table[s.ID]; !seen {
			order = append(order, s.ID)
		}
		table[s.ID] = s.Value
	}
	return table, order
}

func find(messages []domain.WindowMessage, id int64) *domain.WindowMessage {
	for i := range messages {
		if messages[i].ID == id {
			return &messages[i]
		}
	}
	return nil
}

func primaryEligible(m domain.WindowMessage) bool { return kind(m) != domain.ContentSticker }

// validPrimary is the model's primary pick when it can be the quote.
func validPrimary(choice *domain.QuoteChoice, messages []domain.WindowMessage) *domain.WindowMessage {
	if choice == nil || choice.PrimaryID == 0 {
		return nil
	}
	m := find(messages, choice.PrimaryID)
	if m == nil || !primaryEligible(*m) {
		return nil
	}
	return m
}

// validContext is the model's context ids as messages of the day, without
// repeats and in the model's order. The primary message is added at the end if
// the model left it out, and a long selection is cut to a window of MaxContext
// around it.
func validContext(choice *domain.QuoteChoice, messages []domain.WindowMessage, primary *domain.WindowMessage) []domain.WindowMessage {
	if choice == nil {
		return nil
	}
	ids := dedupe(choice.ContextIDs)
	if len(ids) == 0 && !choice.ContextNeeded {
		return nil
	}
	selected := make([]domain.WindowMessage, 0, len(ids)+1)
	hasPrimary := false
	for _, id := range ids {
		if m := find(messages, id); m != nil {
			selected = append(selected, *m)
			hasPrimary = hasPrimary || m.ID == primary.ID
		}
	}
	if !hasPrimary {
		selected = append(selected, *primary)
	}
	return limitContext(selected, primary.ID, MaxContext)
}

// limitContext keeps limit messages with the primary near the middle: two
// before it when there are that many, and the window slides to stay in range.
func limitContext(selected []domain.WindowMessage, primaryID int64, limit int) []domain.WindowMessage {
	if len(selected) <= limit {
		return selected
	}
	idx := -1
	for i, m := range selected {
		if m.ID == primaryID {
			idx = i
			break
		}
	}
	if idx < 0 {
		return selected[:limit]
	}
	start := idx - limit/2
	end := start + limit
	if start < 0 {
		start, end = 0, limit
	}
	if end > len(selected) {
		end = len(selected)
		start = max(0, end-limit)
	}
	return selected[start:end]
}

func dedupe(ids []int64) []int64 {
	seen := make(map[int64]bool, len(ids))
	out := make([]int64, 0, len(ids))
	for _, id := range ids {
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}
