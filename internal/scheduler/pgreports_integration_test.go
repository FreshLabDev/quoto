// SPDX-License-Identifier: Apache-2.0
//go:build integration

package scheduler

import (
	"context"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/FreshLabDev/quoto/internal/db"
	"github.com/FreshLabDev/quoto/internal/db/dbtest"
	"github.com/FreshLabDev/quoto/internal/domain"
)

var (
	ictx   = context.Background()
	alice  = domain.Person{ID: 11, Username: "alice", FirstName: "Alice", LastName: "A", LanguageCode: "uk"}
	bob    = domain.Person{ID: 12, Username: "bob", FirstName: "Bob"}
	igroup = domain.ChatInfo{ID: -1001, Type: "supergroup", Title: "Club"}
	iday   = time.Date(2026, 10, 21, 0, 0, 0, 0, time.UTC)
	iwin   = domain.Window{
		QuoteDay: iday,
		Start:    time.Date(2026, 10, 20, 19, 0, 0, 0, time.UTC),
		End:      time.Date(2026, 10, 21, 19, 0, 0, 0, time.UTC),
	}
	inow = time.Date(2026, 10, 21, 19, 5, 0, 0, time.UTC)
)

// reportFixture is a migrated database with a group, two people and three
// messages in the window: a tagged one (#quoto, which scoring filters out), a
// plain one and a photo.
type reportFixture struct {
	store   *db.Store
	pool    *pgxpool.Pool
	reports *PGReports
	// all are the window's messages as ListWindow returns them; Scored leaves
	// out the tagged first one.
	all    []domain.WindowMessage
	scored []domain.WindowMessage
}

func newReportFixture(t *testing.T) *reportFixture {
	t.Helper()
	s := dbtest.New(t)
	if _, err := s.Ensure(ictx, alice, igroup); err != nil {
		t.Fatal(err)
	}
	if err := s.TouchUser(ictx, bob); err != nil {
		t.Fatal(err)
	}
	at := iwin.Start.Add(time.Hour)
	save := func(id int64, user int64, text, kind string, replyTo int64, media *domain.MediaSource) {
		t.Helper()
		_, inserted, err := s.Save(ictx, domain.NewMessage{
			MessageID: id, ChatID: igroup.ID, UserID: user, Text: text, ContentType: kind,
			ReplyTo: replyTo, CreatedAt: at, Media: media,
		})
		if err != nil || !inserted {
			t.Fatalf("save %d: inserted=%t err=%v", id, inserted, err)
		}
		at = at.Add(time.Minute)
	}
	save(11, alice.ID, "setup #quoto", domain.ContentText, 0, nil)
	save(12, alice.ID, "real setup", domain.ContentText, 0, nil)
	save(13, bob.ID, "photo: a cat on a keyboard", domain.ContentPhoto, 12, &domain.MediaSource{
		Kind: domain.ContentPhoto, FileID: "AgAD", FileUniqueID: "uniq", MimeType: "image/jpeg", FileSize: 4321,
		Width: 800, Height: 600,
	})
	dbtest.Exec(t, s, `UPDATE message_media SET description_snapshot = 'a cat on a keyboard', sha256 = 'abc', phash = 'f0f0'
		WHERE message_db_id = (SELECT id FROM messages WHERE message_id = 13)`)
	if err := s.SyncAbsolute(ictx, igroup.ID, 13, map[string]int{"😂": 2}); err != nil {
		t.Fatal(err)
	}

	all, err := s.ListWindow(ictx, igroup.ID, iwin)
	if err != nil || len(all) != 3 {
		t.Fatalf("list window: %d messages, %v", len(all), err)
	}
	f := &reportFixture{store: s, pool: s.Pool(), reports: NewPGReports(s.Pool()), all: all, scored: all[1:]}
	f.reports.now = func() time.Time { return inow }
	return f
}

// report builds a parsed report for the fixture: messages 12 and 13 scored,
// 13 selected, the model's context 12 and 13.
func (f *reportFixture) report() domain.EvaluationReport {
	photo := f.scored[1]
	photo.Media.CacheID = 7 // message_ai_scores.media_cache_id has no foreign key
	f.scored[1] = photo
	cost := 0.00123456
	prompt, completion, reasoning, total := 1200, 80, 30, 1310
	return domain.EvaluationReport{
		GroupID:     igroup.ID,
		Window:      iwin,
		SourceCount: 3,
		Scored:      f.scored,
		SelectedID:  f.scored[1].ID,
		AuthorNames: map[int64]string{alice.ID: "Alice A"}, // Bob is shown as Unknown
		Result: domain.EvalResult{
			Scores:         []domain.Score{{ID: f.scored[0].ID, Value: 0.4}, {ID: f.scored[1].ID, Value: 0.9}},
			ActualModel:    "claude-actual",
			RequestedModel: "claude-requested",
			Status:         domain.RunParsed,
			RequestID:      "0123456789abcdef0123456789abcdef",
			DayVerdict:     &domain.DayVerdict{ShouldPublish: true, ReasonCode: domain.ReasonWorthy, ReasonText: "a good day"},
			QuoteChoice: &domain.QuoteChoice{
				PrimaryID:     f.scored[1].ID,
				ContextIDs:    []int64{f.scored[0].ID, f.scored[1].ID, 999999},
				ContextNeeded: true,
			},
			Usage: &domain.TokenUsage{PromptTokens: &prompt, CompletionTokens: &completion, ReasoningTokens: &reasoning, TotalTokens: &total, CostUSD: &cost},
		},
	}
}

type runRecord struct {
	ID                        int64
	GroupID, ChatID           int64
	QuoteDay                  time.Time
	WindowStart, WindowEnd    time.Time
	Requested, Actual, Status string
	MessageCount, SourceCount int
	SelectedDB, SelectedTG    *int64
	ContextIDs                *string
	ContextNeeded             bool
	ShouldPublish             *bool
	ReasonCode, ReasonText    *string
	RequestID                 *string
	Prompt, Completion        *int
	Reasoning, Total          *int
	Cost                      *string
	CreatedAt                 time.Time
}

func (f *reportFixture) runs(t *testing.T) []runRecord {
	t.Helper()
	rows, err := f.pool.Query(ictx, `
		SELECT id, group_id, chat_id, quote_day, window_start_at, window_end_at, requested_model, actual_model, status,
			message_count, source_message_count, selected_message_db_id, selected_telegram_message_id,
			context_message_ids, context_needed, should_publish, day_reason_code, day_reason_text, request_id,
			prompt_tokens, completion_tokens, reasoning_tokens, total_tokens, cost_usd::text, created_at
		FROM ai_evaluation_runs ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []runRecord
	for rows.Next() {
		var r runRecord
		if err := rows.Scan(&r.ID, &r.GroupID, &r.ChatID, &r.QuoteDay, &r.WindowStart, &r.WindowEnd, &r.Requested, &r.Actual, &r.Status,
			&r.MessageCount, &r.SourceCount, &r.SelectedDB, &r.SelectedTG, &r.ContextIDs, &r.ContextNeeded, &r.ShouldPublish,
			&r.ReasonCode, &r.ReasonText, &r.RequestID, &r.Prompt, &r.Completion, &r.Reasoning, &r.Total, &r.Cost, &r.CreatedAt); err != nil {
			t.Fatal(err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

type scoreRecord struct {
	RunID                      int64
	GroupID, ChatID            int64
	QuoteDay                   time.Time
	MessageDB                  *int64
	TelegramID                 int64
	ReplyTo, UserID            *int64
	Author, Text, ContentType  string
	Caption, Reactions         *string
	ReactionCount              int
	MediaStatus, MediaDesc     *string
	MediaKind, FileID, UniqID  *string
	Mime, FileName             *string
	FileSize                   *int64
	Width, Height              *int
	Duration                   *float64
	SHA, Phash                 *string
	CacheID                    *int64
	AIScore, AIScoreRaw        float64
	Rank                       int
	SelectedPrimary, InContext bool
	CreatedAt                  time.Time
}

func (f *reportFixture) scores(t *testing.T) []scoreRecord {
	t.Helper()
	rows, err := f.pool.Query(ictx, `
		SELECT run_id, group_id, chat_id, quote_day, message_db_id, telegram_message_id, reply_to_message_id, user_id,
			author_name_snapshot, text_snapshot, content_type, caption_snapshot, reactions_snapshot, reaction_count,
			media_status, media_description_snapshot, media_kind, telegram_file_id, telegram_file_unique_id, mime_type,
			file_name, file_size, width, height, duration, sha256, phash, media_cache_id,
			ai_score, ai_score_raw, rank, is_selected_primary, is_selected_context, created_at
		FROM message_ai_scores ORDER BY rank`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []scoreRecord
	for rows.Next() {
		var r scoreRecord
		if err := rows.Scan(&r.RunID, &r.GroupID, &r.ChatID, &r.QuoteDay, &r.MessageDB, &r.TelegramID, &r.ReplyTo, &r.UserID,
			&r.Author, &r.Text, &r.ContentType, &r.Caption, &r.Reactions, &r.ReactionCount,
			&r.MediaStatus, &r.MediaDesc, &r.MediaKind, &r.FileID, &r.UniqID, &r.Mime,
			&r.FileName, &r.FileSize, &r.Width, &r.Height, &r.Duration, &r.SHA, &r.Phash, &r.CacheID,
			&r.AIScore, &r.AIScoreRaw, &r.Rank, &r.SelectedPrimary, &r.InContext, &r.CreatedAt); err != nil {
			t.Fatal(err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func str(s string) *string { return &s }

// tests/test_ai_reports.py::test_save_evaluation_report_stores_compact_run_and_score_rows
// (REP-01, REP-02, REP-03).
func TestReportWritesRunAndScores(t *testing.T) {
	f := newReportFixture(t)
	f.reports.SaveEvaluationReport(ictx, f.report())

	runs := f.runs(t)
	if len(runs) != 1 {
		t.Fatalf("%d runs, want 1", len(runs))
	}
	r := runs[0]
	checks := []struct {
		name      string
		got, want any
	}{
		{"group_id", r.GroupID, igroup.ID},
		{"chat_id equals group_id", r.ChatID, igroup.ID},
		{"quote_day", r.QuoteDay, iday},
		{"window_start_at", r.WindowStart.UTC(), iwin.Start},
		{"window_end_at", r.WindowEnd.UTC(), iwin.End},
		{"requested_model", r.Requested, "claude-requested"},
		{"actual_model", r.Actual, "claude-actual"},
		{"status", r.Status, "parsed"},
		{"message_count", r.MessageCount, 2},
		{"source_message_count", r.SourceCount, 3},
		{"selected_message_db_id", *r.SelectedDB, f.scored[1].ID},
		{"selected_telegram_message_id", *r.SelectedTG, int64(13)},
		{"context_message_ids is compact JSON of Telegram ids", *r.ContextIDs, "[12,13]"},
		{"context_needed", r.ContextNeeded, true},
		{"should_publish", *r.ShouldPublish, true},
		{"day_reason_code", *r.ReasonCode, "worthy"},
		{"day_reason_text", *r.ReasonText, "a good day"},
		{"request_id", *r.RequestID, "0123456789abcdef0123456789abcdef"},
		{"prompt_tokens", *r.Prompt, 1200},
		{"completion_tokens", *r.Completion, 80},
		{"reasoning_tokens", *r.Reasoning, 30},
		{"total_tokens", *r.Total, 1310},
		{"cost_usd", *r.Cost, "0.00123456"},
		{"created_at", r.CreatedAt.UTC(), inow},
	}
	for _, c := range checks {
		if !reflect.DeepEqual(c.got, c.want) {
			t.Errorf("%s = %v, want %v", c.name, c.got, c.want)
		}
	}

	scores := f.scores(t)
	if len(scores) != 2 {
		t.Fatalf("%d score rows, want 2", len(scores))
	}
	best, other := scores[0], scores[1]
	if best.TelegramID != 13 || best.Rank != 1 || !best.SelectedPrimary || !best.InContext {
		t.Errorf("best = %+v", best)
	}
	if other.TelegramID != 12 || other.Rank != 2 || other.SelectedPrimary || !other.InContext {
		t.Errorf("other = %+v", other)
	}
	if best.AIScore != 0.9 || best.AIScoreRaw != 9.0 || other.AIScore != 0.4 || other.AIScoreRaw != 4.0 {
		t.Errorf("scores: %v/%v and %v/%v", best.AIScore, best.AIScoreRaw, other.AIScore, other.AIScoreRaw)
	}
	if best.RunID != r.ID || best.GroupID != igroup.ID || best.ChatID != igroup.ID || !best.QuoteDay.Equal(iday) || best.CreatedAt.UTC() != inow {
		t.Errorf("keys = %+v", best)
	}

	// The photo's snapshot: content, caption, reply, the file and its description.
	want := scoreRecord{
		MessageDB: &f.scored[1].ID, TelegramID: 13, ReplyTo: ptr(int64(12)), UserID: ptr(bob.ID),
		Author: "Unknown", Text: "photo: a cat on a keyboard", ContentType: "photo",
		Reactions: str(`{"😂":2}`), ReactionCount: 2,
		MediaStatus: str("pending"), MediaDesc: str("a cat on a keyboard"), MediaKind: str("photo"),
		FileID: str("AgAD"), UniqID: str("uniq"), Mime: str("image/jpeg"), FileSize: ptr(int64(4321)),
		Width: ptr(800), Height: ptr(600), SHA: str("abc"), Phash: str("f0f0"), CacheID: ptr(int64(7)),
	}
	got := best
	got.RunID, got.GroupID, got.ChatID, got.QuoteDay = 0, 0, 0, time.Time{}
	got.AIScore, got.AIScoreRaw, got.Rank, got.SelectedPrimary, got.InContext, got.CreatedAt = 0, 0, 0, false, false, time.Time{}
	want.MediaStatus = got.MediaStatus // the pipeline's state is not under test here
	if !reflect.DeepEqual(got, want) {
		t.Errorf("photo snapshot:\n got %+v\nwant %+v", got, want)
	}
	// The plain message: no caption, no media, no reactions, a known author.
	if other.Author != "Alice A" || other.Caption != nil || other.Reactions != nil || other.MediaKind != nil ||
		other.Width != nil || other.Duration != nil || other.ReplyTo != nil || other.ReactionCount != 0 {
		t.Errorf("plain snapshot = %+v", other)
	}
}

// tests/test_ai_reports.py::test_save_evaluation_report_skips_score_rows_for_failed_ai (REP-04)
func TestReportOfAFailedRunHasNoScores(t *testing.T) {
	f := newReportFixture(t)
	rep := f.report()
	rep.Result = domain.EvalResult{
		Scores:          []domain.Score{{ID: f.scored[0].ID, Value: 0.5}, {ID: f.scored[1].ID, Value: 0.5}},
		Status:          domain.RunAIFailed,
		DayVerdictError: "AI request failed",
	}
	rep.SelectedID = f.scored[0].ID
	f.reports.SaveEvaluationReport(ictx, rep)

	runs := f.runs(t)
	if len(runs) != 1 || runs[0].Status != "ai_failed" {
		t.Fatalf("runs = %+v", runs)
	}
	r := runs[0]
	if n := len(f.scores(t)); n != 0 {
		t.Errorf("%d score rows for a failed run", n)
	}
	// Nothing the AI did not report is invented.
	if r.ShouldPublish != nil || r.ReasonCode != nil || r.ReasonText != nil || r.RequestID != nil || r.ContextIDs != nil ||
		r.Prompt != nil || r.Completion != nil || r.Reasoning != nil || r.Total != nil || r.Cost != nil || r.ContextNeeded {
		t.Errorf("a failed run must leave the AI's fields empty: %+v", r)
	}
	if *r.SelectedTG != 12 || r.MessageCount != 2 {
		t.Errorf("selected %d, message_count %d", *r.SelectedTG, r.MessageCount)
	}
	if r.Requested != "unknown" || r.Actual != "unknown" {
		t.Errorf("models = %q, %q: the NOT NULL columns still need a value", r.Requested, r.Actual)
	}
}

func TestReportUsesTheFallbackModel(t *testing.T) {
	f := newReportFixture(t)
	f.reports.fallbackModel = "configured/eval-model"
	rep := f.report()
	rep.Result = domain.EvalResult{Status: domain.RunParseFailed}
	f.reports.SaveEvaluationReport(ictx, rep)
	r := f.runs(t)[0]
	if r.Requested != "configured/eval-model" || r.Actual != "configured/eval-model" {
		t.Errorf("models = %q, %q", r.Requested, r.Actual)
	}
}

// The columns without a database default are all written, even by the barest
// report: the schema's only defaults are on other tables (task brief).
func TestReportOfAnEmptyDayFillsEveryNotNullColumn(t *testing.T) {
	f := newReportFixture(t)
	f.reports.SaveEvaluationReport(ictx, domain.EvaluationReport{GroupID: igroup.ID, Window: iwin})
	runs := f.runs(t)
	if len(runs) != 1 {
		t.Fatalf("the barest report was not stored: %+v", runs)
	}
	if r := runs[0]; r.Status != "parsed" || r.MessageCount != 0 || r.CreatedAt.IsZero() {
		t.Errorf("run = %+v", r)
	}
	if n := len(f.scores(t)); n != 0 {
		t.Errorf("%d score rows", n)
	}
}

// tests/test_ai_reports.py::test_successful_rerun_replaces_failed_report_of_the_same_day and
// test_rerun_drops_scores_of_the_replaced_report (REP-05)
func TestReportReplacesAFailedOne(t *testing.T) {
	for _, status := range []string{domain.RunAIFailed, domain.RunParseFailed, domain.RunEmpty} {
		t.Run(status, func(t *testing.T) {
			f := newReportFixture(t)
			failed := f.report()
			failed.Result = domain.EvalResult{Status: status, DayVerdictError: "boom"}
			f.reports.SaveEvaluationReport(ictx, failed)
			first := f.runs(t)
			if len(first) != 1 || first[0].Status != status {
				t.Fatalf("first run = %+v", first)
			}
			// Scores of the earlier run must go with it.
			dbtest.Exec(t, f.store, `INSERT INTO message_ai_scores (run_id, group_id, chat_id, quote_day, telegram_message_id,
				author_name_snapshot, text_snapshot, content_type, reaction_count, ai_score, ai_score_raw, rank,
				is_selected_primary, is_selected_context, created_at)
				VALUES ($1, $2, $2, $3, 4242, 'old', 'old', 'text', 0, 0.5, 5, 1, false, false, now())`,
				first[0].ID, igroup.ID, iday)

			f.reports.SaveEvaluationReport(ictx, f.report())

			runs := f.runs(t)
			if len(runs) != 1 || runs[0].Status != "parsed" || runs[0].ID == first[0].ID {
				t.Fatalf("runs after the replacement = %+v (old id %d)", runs, first[0].ID)
			}
			scores := f.scores(t)
			if len(scores) != 2 {
				t.Fatalf("%d score rows, want the 2 of the new run", len(scores))
			}
			for _, s := range scores {
				if s.RunID != runs[0].ID || s.TelegramID == 4242 {
					t.Errorf("a stale score survived: %+v", s)
				}
			}
		})
	}
}

// tests/test_ai_reports.py::test_parsed_report_is_not_touched_by_a_later_run (REP-06)
func TestReportKeepsAParsedOne(t *testing.T) {
	f := newReportFixture(t)
	f.reports.SaveEvaluationReport(ictx, f.report())
	before := f.runs(t)

	later := f.report()
	later.Result.ActualModel = "another-model"
	later.Result.Status = domain.RunAIFailed
	f.reports.SaveEvaluationReport(ictx, later)

	after := f.runs(t)
	if !reflect.DeepEqual(before, after) {
		t.Errorf("the parsed report changed:\nbefore %+v\nafter  %+v", before, after)
	}
	if n := len(f.scores(t)); n != 2 {
		t.Errorf("%d score rows, want the original 2", n)
	}
	if outcome, err := f.reports.save(ictx, later); err != nil || outcome != reportKept {
		t.Errorf("save = %v, %v; want kept", outcome, err)
	}
}

// tests/test_ai_reports.py::test_failed_replacement_keeps_the_previous_report (REP-07)
func TestReportFailedReplacementKeepsTheEarlierOne(t *testing.T) {
	f := newReportFixture(t)
	failed := f.report()
	failed.Result = domain.EvalResult{Status: domain.RunParseFailed}
	f.reports.SaveEvaluationReport(ictx, failed)
	before := f.runs(t)

	// A score row for a person core does not know violates the foreign key, after
	// the old report has been deleted and the new run inserted.
	broken := f.report()
	broken.Scored = append([]domain.WindowMessage(nil), broken.Scored...)
	broken.Scored[0].UserID = 987654321
	if _, err := f.reports.save(ictx, broken); err == nil {
		t.Fatal("the replacement should have failed")
	}
	f.reports.SaveEvaluationReport(ictx, broken) // logged, not raised

	if after := f.runs(t); !reflect.DeepEqual(before, after) {
		t.Errorf("the earlier report did not survive:\nbefore %+v\nafter  %+v", before, after)
	}
	if n := len(f.scores(t)); n != 0 {
		t.Errorf("%d score rows leaked from the rolled-back replacement", n)
	}
}

// Two workers finishing the same day: one report, no error for the loser.
func TestReportConcurrentSavesWriteOnce(t *testing.T) {
	f := newReportFixture(t)
	rep := f.report()
	const workers = 4
	outcomes := make([]reportOutcome, workers)
	errs := make([]error, workers)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			outcomes[i], errs[i] = f.reports.save(ictx, rep)
		}()
	}
	close(start)
	wg.Wait()

	written := 0
	for i := range workers {
		if errs[i] != nil {
			t.Errorf("worker %d: %v", i, errs[i])
		}
		if outcomes[i] == reportWritten {
			written++
		}
	}
	if written != 1 {
		t.Errorf("%d workers wrote the report, want exactly 1 (outcomes %v)", written, outcomes)
	}
	if n := len(f.runs(t)); n != 1 {
		t.Errorf("%d runs", n)
	}
	if n := len(f.scores(t)); n != 2 {
		t.Errorf("%d score rows", n)
	}
}
