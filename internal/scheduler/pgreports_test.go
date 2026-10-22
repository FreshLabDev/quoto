// SPDX-License-Identifier: Apache-2.0

package scheduler

import (
	"reflect"
	"testing"
	"time"

	"github.com/FreshLabDev/quoto/internal/domain"
)

// These tests cover what the report store computes before it touches the
// database; the SQL is covered by pgreports_integration_test.go.

func reportOf(msgs []domain.WindowMessage, res domain.EvalResult, selected int64) domain.EvaluationReport {
	return domain.EvaluationReport{
		GroupID: chatA, Window: domain.Window{QuoteDay: day27, Start: defaultStart, End: defaultEnd},
		SourceCount: len(msgs), Scored: msgs, Result: res, SelectedID: selected,
		AuthorNames: map[int64]string{1001: "Ann", 1002: "Bob"},
	}
}

func dayOf(n int) []domain.WindowMessage {
	var out []domain.WindowMessage
	for i := int64(1); i <= int64(n); i++ {
		out = append(out, domain.WindowMessage{ID: i, MessageID: 100 + i, UserID: 1000 + i, Text: "t", ContentType: domain.ContentText})
	}
	return out
}

// tests/test_ai_reports.py::test_save_evaluation_report_stores_compact_run_and_score_rows (REP-02): ranks run by
// score, highest first, and by position in the day among equal scores.
func TestScoreRowsRankByScoreThenPosition(t *testing.T) {
	res := domain.EvalResult{Scores: []domain.Score{
		{ID: 1, Value: 0.3}, {ID: 2, Value: 0.9}, {ID: 3, Value: 0.3}, {ID: 4, Value: 0.9}, {ID: 5, Value: 0.0},
	}}
	rows := scoreRows(reportOf(dayOf(5), res, 2))

	got := map[int64]int{}
	for _, r := range rows {
		got[r.msg.ID] = r.rank
	}
	want := map[int64]int{2: 1, 4: 2, 1: 3, 3: 4, 5: 5}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ranks = %v, want %v", got, want)
	}
}

// tests/test_ai_reports.py::test_save_evaluation_report_stores_compact_run_and_score_rows (REP-03): a message is in
// the report when it was scored, selected, or in the model's context ids.
func TestScoreRowsIncludeScoredSelectedAndContext(t *testing.T) {
	// 1 and 2 are scored; 3 is the quote and was not scored; 4 is context only; 5 is none of those.
	res := domain.EvalResult{
		Scores:      []domain.Score{{ID: 1, Value: 0.5}, {ID: 2, Value: 0.4}},
		QuoteChoice: &domain.QuoteChoice{PrimaryID: 3, ContextIDs: []int64{4, 3, 4}, ContextNeeded: true},
	}
	rows := scoreRows(reportOf(dayOf(5), res, 3))

	type flags struct {
		primary, context bool
		score            float64
		rank             int
	}
	got := map[int64]flags{}
	for _, r := range rows {
		got[r.msg.ID] = flags{r.primary, r.inContext, r.aiScore, r.rank}
	}
	want := map[int64]flags{
		1: {false, false, 0.5, 1},
		2: {false, false, 0.4, 2},
		3: {true, true, 0, 3},
		4: {false, true, 0, 4},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("rows = %+v\nwant   %+v", got, want)
	}
}

func TestScoreRowsClampTheScoreAndNameTheAuthor(t *testing.T) {
	msgs := dayOf(3)
	msgs[0].UserID, msgs[1].UserID, msgs[2].UserID = 1001, 1002, 7777 // 7777 has no name
	res := domain.EvalResult{Scores: []domain.Score{{ID: 1, Value: 1.7}, {ID: 2, Value: -0.2}, {ID: 3, Value: 0.5}}}

	rows := scoreRows(reportOf(msgs, res, 1))

	want := []struct {
		author string
		score  float64
	}{{"Ann", 1}, {"Bob", 0}, {"Unknown", 0.5}}
	for i, w := range want {
		if rows[i].author != w.author || rows[i].aiScore != w.score {
			t.Errorf("row %d = %q %v, want %q %v", i, rows[i].author, rows[i].aiScore, w.author, w.score)
		}
	}
}

// A failed run scored nothing and selected nothing: it has no score rows.
func TestScoreRowsOfAnEmptyAnswer(t *testing.T) {
	if rows := scoreRows(reportOf(dayOf(5), domain.EvalResult{Status: domain.RunAIFailed}, 0)); len(rows) != 0 {
		t.Errorf("%d rows", len(rows))
	}
}

func TestScoreRowArgsFillTheNullableColumns(t *testing.T) {
	m := domain.WindowMessage{
		ID: 7, MessageID: 107, UserID: 1001, Text: "x", ContentType: "",
		Reactions: []domain.Reaction{{Emoji: "👍", Count: 2}, {Emoji: "🔥", Count: 1}},
	}
	rep := reportOf([]domain.WindowMessage{m}, domain.EvalResult{Scores: []domain.Score{{ID: 7, Value: 0.25}}}, 7)
	row := scoreRows(rep)[0]
	args := row.args(55, rep, time.Time{})

	// run_id, group_id, chat_id, quote_day, message_id, telegram_message_id, reply_to, user_id, author, text, content_type.
	if args[0] != int64(55) || args[1] != chatA || args[2] != chatA || args[4] != int64(7) || args[5] != int64(107) {
		t.Errorf("ids = %v", args[:6])
	}
	if args[6] != nil || args[7] != int64(1001) || args[8] != "Ann" || args[10] != domain.ContentText {
		t.Errorf("reply %v, user %v, author %v, content type %v", args[6], args[7], args[8], args[10])
	}
	// caption, reactions JSON, reaction count.
	if args[11] != nil || args[12] != `{"👍":2,"🔥":1}` || args[13] != 3 {
		t.Errorf("caption %v, reactions %v, count %v", args[11], args[12], args[13])
	}
	// Everything about media is NULL for a text message.
	for i := 14; i <= 27; i++ {
		if args[i] != nil {
			t.Errorf("arg %d = %v, want NULL", i, args[i])
		}
	}
	// ai_score, score_10, rank, primary, in context.
	if args[28] != 0.25 || args[29] != 2.5 || args[30] != 1 || args[31] != true || args[32] != false {
		t.Errorf("score %v, x10 %v, rank %v, primary %v, context %v", args[28], args[29], args[30], args[31], args[32])
	}
}

// tests/test_ai_reports.py::test_save_evaluation_report_stores_compact_run_and_score_rows (REP-01)
func TestRunValues(t *testing.T) {
	cost, prompt := 0.0125, 120
	rep := reportOf(dayOf(4), domain.EvalResult{
		Scores: []domain.Score{{ID: 1, Value: 0.5}}, Status: domain.RunParsed, RequestedModel: "m-req", ActualModel: "m-act",
		RequestID:   "req-1",
		QuoteChoice: &domain.QuoteChoice{PrimaryID: 2, ContextIDs: []int64{1, 2, 99}, ContextNeeded: true},
		DayVerdict:  &domain.DayVerdict{ShouldPublish: true, ReasonCode: domain.ReasonWorthy, ReasonText: "good"},
		Usage:       &domain.TokenUsage{PromptTokens: &prompt, CostUSD: &cost},
	}, 2)
	rep.SourceCount = 9
	now := time.Date(2026, 3, 27, 19, 5, 0, 0, time.UTC)

	row := (&PGReports{fallbackModel: "fallback"}).runValues(rep, now)

	if row.groupID != chatA || row.chatID != chatA || !row.quoteDay.Equal(day27) || row.messageCount != 4 || row.sourceCount != 9 {
		t.Errorf("row = %+v", row)
	}
	if row.requestedModel != "m-req" || row.actualModel != "m-act" || row.status != domain.RunParsed || row.requestID != "req-1" {
		t.Errorf("models %q %q, status %q, request %v", row.requestedModel, row.actualModel, row.status, row.requestID)
	}
	if row.selectedDBID != int64(2) || row.selectedTGID != int64(102) {
		t.Errorf("selected %v / %v", row.selectedDBID, row.selectedTGID)
	}
	// Telegram ids of the context the day has; 99 is not in it.
	if row.contextIDs != "[101,102]" || !row.contextNeeded {
		t.Errorf("context %v needed %t", row.contextIDs, row.contextNeeded)
	}
	if row.shouldPublish != true || row.reasonCode != domain.ReasonWorthy || row.reasonText != "good" {
		t.Errorf("verdict %v %v %v", row.shouldPublish, row.reasonCode, row.reasonText)
	}
	// Usage: what was reported is stored, what was not is NULL.
	if row.prompt != 120 || row.completion != nil || row.reasoning != nil || row.total != nil || row.cost != 0.0125 {
		t.Errorf("usage = %v %v %v %v %v", row.prompt, row.completion, row.reasoning, row.total, row.cost)
	}
	if !row.createdAt.Equal(now) || !row.windowStart.Equal(defaultStart) || !row.windowEnd.Equal(defaultEnd) {
		t.Errorf("times = %v %v %v", row.createdAt, row.windowStart, row.windowEnd)
	}
}

// tests/test_ai_reports.py::test_save_evaluation_report_skips_score_rows_for_failed_ai (REP-04)
func TestRunValuesOfAFailedRun(t *testing.T) {
	rep := reportOf(dayOf(3), domain.EvalResult{Status: domain.RunAIFailed}, 0)

	row := (&PGReports{}).runValues(rep, time.Time{})

	if row.requestedModel != unknownModel || row.actualModel != unknownModel || row.status != domain.RunAIFailed {
		t.Errorf("models %q %q, status %q", row.requestedModel, row.actualModel, row.status)
	}
	for name, v := range map[string]any{
		"selectedDBID": row.selectedDBID, "selectedTGID": row.selectedTGID, "contextIDs": row.contextIDs,
		"shouldPublish": row.shouldPublish, "reasonCode": row.reasonCode, "reasonText": row.reasonText,
		"requestID": row.requestID, "prompt": row.prompt, "cost": row.cost,
	} {
		if v != nil {
			t.Errorf("%s = %v, want NULL", name, v)
		}
	}
	if row.contextNeeded {
		t.Error("context_needed set without a choice")
	}
}

func TestRunValuesDefaultsTheStatus(t *testing.T) {
	if got := (&PGReports{}).runValues(reportOf(nil, domain.EvalResult{}, 0), time.Time{}).status; got != domain.RunParsed {
		t.Errorf("status = %q", got)
	}
}

func TestContextTelegramIDs(t *testing.T) {
	tests := []struct {
		name string
		ids  []int64
		want []int64
	}{
		{"model order is kept", []int64{3, 1, 2}, []int64{103, 101, 102}},
		{"repeats are dropped", []int64{2, 2, 1, 2}, []int64{102, 101}},
		{"ids outside the day are dropped", []int64{9, 1, 0}, []int64{101}},
		{"none", nil, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rep := reportOf(dayOf(3), domain.EvalResult{QuoteChoice: &domain.QuoteChoice{ContextIDs: tt.ids}}, 0)
			if got := contextTelegramIDs(rep); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
	if got := contextTelegramIDs(reportOf(dayOf(3), domain.EvalResult{}, 0)); got != nil {
		t.Errorf("without a choice: %v", got)
	}
}

// Python wrote json.dumps(ids, separators=(",", ":")).
func TestCompactIDs(t *testing.T) {
	tests := []struct {
		ids  []int64
		want string
	}{
		{[]int64{11, 12}, "[11,12]"},
		{[]int64{5}, "[5]"},
		{nil, "[]"},
		{[]int64{9999999999, -3}, "[9999999999,-3]"},
	}
	for _, tt := range tests {
		if got := compactIDs(tt.ids); got != tt.want {
			t.Errorf("compactIDs(%v) = %q, want %q", tt.ids, got, tt.want)
		}
	}
}

// Python wrote json.dumps(dict, ensure_ascii=False, separators=(",", ":")): keys
// in insertion order, no escaping of < > & or of non-ASCII text.
func TestReactionsJSON(t *testing.T) {
	tests := []struct {
		name string
		in   []domain.Reaction
		want string
	}{
		{"none", nil, ""},
		{"order is kept, not sorted", []domain.Reaction{{Emoji: "😂", Count: 2}, {Emoji: "❤️", Count: 1}, {Emoji: "👍", Count: 5}}, `{"😂":2,"❤️":1,"👍":5}`},
		{"zero counts are skipped", []domain.Reaction{{Emoji: "👍", Count: 0}, {Emoji: "🔥", Count: 3}}, `{"🔥":3}`},
		{"only zero counts", []domain.Reaction{{Emoji: "👍", Count: 0}}, ""},
		{"markup characters are not escaped", []domain.Reaction{{Emoji: "<&>", Count: 1}}, `{"<&>":1}`},
		{"quotes and backslashes are", []domain.Reaction{{Emoji: `"\`, Count: 1}}, `{"\"\\":1}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := reactionsJSON(tt.in); got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}
