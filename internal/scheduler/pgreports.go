// SPDX-License-Identifier: Apache-2.0

package scheduler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/FreshLabDev/quoto/internal/domain"
)

// runUniqueConstraint is the one-report-per-group-per-day key. Losing the race
// on it means another worker wrote the day's report first.
const runUniqueConstraint = "uq_ai_evaluation_run_group_day"

// unknownModel stands in for a model name nobody gave, since both columns are
// NOT NULL.
const unknownModel = "unknown"

// PGReports writes the daily AI evaluation report into quoto.ai_evaluation_runs
// and quoto.message_ai_scores. The tables are a record for the operator: nothing
// in the bot reads them.
type PGReports struct {
	pool          *pgxpool.Pool
	log           *slog.Logger
	now           func() time.Time
	fallbackModel string
}

// ReportsOption configures a PGReports.
type ReportsOption func(*PGReports)

// WithReportsLogger sets the logger; the default is slog.Default().
func WithReportsLogger(l *slog.Logger) ReportsOption {
	return func(r *PGReports) { r.log = l }
}

// WithFallbackModel names the model recorded when the AI result carries none
// (a run that failed before any model answered). Python used the configured
// OPENROUTER_EVAL_MODEL; without this the columns say "unknown".
func WithFallbackModel(name string) ReportsOption {
	return func(r *PGReports) { r.fallbackModel = name }
}

// NewPGReports returns the report store on pool, which must have search_path
// quoto as the pool of db.Store has.
func NewPGReports(pool *pgxpool.Pool, opts ...ReportsOption) *PGReports {
	r := &PGReports{pool: pool, log: slog.Default(), now: time.Now}
	for _, opt := range opts {
		opt(r)
	}
	r.log = r.log.With("component", "reports")
	return r
}

// scrubSnapshotsSQL blanks the message text copied into the report tables. The
// numbers stay: scores, ranks, token counts and the media metadata. Rows that
// are already blank are skipped, so an hourly run rewrites nothing once it has
// caught up.
const scrubSnapshotsSQL = `
UPDATE message_ai_scores
SET author_name_snapshot = '', text_snapshot = '', caption_snapshot = NULL, media_description_snapshot = NULL
WHERE created_at < $1
  AND (author_name_snapshot <> '' OR text_snapshot <> '' OR caption_snapshot IS NOT NULL
       OR media_description_snapshot IS NOT NULL)`

const scrubReasonsSQL = `
UPDATE ai_evaluation_runs SET day_reason_text = NULL
WHERE created_at < $1 AND day_reason_text IS NOT NULL`

// ScrubSnapshotsOlderThan removes the chat text that SaveEvaluationReport copied
// into message_ai_scores (author name, text, caption, media description) and the
// model's reason text on ai_evaluation_runs, for reports created before the
// given time. Nothing in the bot reads those columns; they would otherwise keep
// the day's messages after the messages table has dropped them. It returns the
// number of score rows blanked.
func (r *PGReports) ScrubSnapshotsOlderThan(ctx context.Context, before time.Time) (int64, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("scrub snapshots: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	tag, err := tx.Exec(ctx, scrubSnapshotsSQL, before)
	if err != nil {
		return 0, fmt.Errorf("scrub message_ai_scores: %w", err)
	}
	if _, err := tx.Exec(ctx, scrubReasonsSQL, before); err != nil {
		return 0, fmt.Errorf("scrub ai_evaluation_runs: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("scrub snapshots: %w", err)
	}
	return tag.RowsAffected(), nil
}

// reportOutcome says what SaveEvaluationReport did with the day.
type reportOutcome int

const (
	reportWritten  reportOutcome = iota // no report existed
	reportReplaced                      // a report that was not parsed was replaced
	reportKept                          // the day's report is parsed (or just written by someone else)
)

// SaveEvaluationReport records the run. A day that already has a parsed report
// keeps it: that report is the day's record. A day whose earlier run failed has
// its report, scores included, replaced in one transaction, so a failure
// halfway leaves the earlier one. Score rows are written only for a parsed run.
// Errors are logged, never returned: the report must not stop a quote.
func (r *PGReports) SaveEvaluationReport(ctx context.Context, report domain.EvaluationReport) {
	log := r.log.With("chat_id", report.GroupID, "quote_day", report.Window.QuoteDay.Format(time.DateOnly))
	outcome, err := r.save(ctx, report)
	switch {
	case err != nil:
		log.Error("saving the AI evaluation report failed", "error", err)
	case outcome == reportKept:
		log.Info("AI evaluation report already exists")
	case outcome == reportReplaced:
		log.Info("AI evaluation report replaced: the earlier run did not parse")
	}
}

func (r *PGReports) save(ctx context.Context, report domain.EvaluationReport) (reportOutcome, error) {
	now := r.now().UTC()
	run := r.runValues(report, now)
	status := run.status

	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // a rollback after commit is a no-op

	outcome := reportWritten
	var existingID int64
	var existingStatus string
	err = tx.QueryRow(ctx,
		`SELECT id, status FROM ai_evaluation_runs WHERE group_id = $1 AND quote_day = $2 FOR UPDATE`,
		report.GroupID, report.Window.QuoteDay).Scan(&existingID, &existingStatus)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
	case err != nil:
		return 0, fmt.Errorf("look up the day's report: %w", err)
	case existingStatus == domain.RunParsed:
		return reportKept, nil
	default:
		if _, err := tx.Exec(ctx, `DELETE FROM message_ai_scores WHERE run_id = $1`, existingID); err != nil {
			return 0, fmt.Errorf("delete the old scores: %w", err)
		}
		if _, err := tx.Exec(ctx, `DELETE FROM ai_evaluation_runs WHERE id = $1`, existingID); err != nil {
			return 0, fmt.Errorf("delete the old report: %w", err)
		}
		outcome = reportReplaced
	}

	var runID int64
	err = tx.QueryRow(ctx, insertRunSQL, run.args()...).Scan(&runID)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == runUniqueConstraint {
			return reportKept, nil
		}
		return 0, fmt.Errorf("insert the report: %w", err)
	}

	if status == domain.RunParsed {
		rows := scoreRows(report)
		batch := &pgx.Batch{}
		for _, row := range rows {
			batch.Queue(insertScoreSQL, row.args(runID, report, now)...)
		}
		br := tx.SendBatch(ctx, batch)
		for range rows {
			if _, err := br.Exec(); err != nil {
				_ = br.Close()
				return 0, fmt.Errorf("insert a score: %w", err)
			}
		}
		if err := br.Close(); err != nil {
			return 0, fmt.Errorf("insert the scores: %w", err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("commit: %w", err)
	}
	return outcome, nil
}

const insertRunSQL = `
INSERT INTO ai_evaluation_runs (
	group_id, chat_id, quote_day, window_start_at, window_end_at, requested_model, actual_model, status,
	message_count, source_message_count, selected_message_db_id, selected_telegram_message_id,
	context_message_ids, context_needed, should_publish, day_reason_code, day_reason_text, request_id,
	prompt_tokens, completion_tokens, reasoning_tokens, total_tokens, cost_usd, created_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21, $22, $23, $24)
RETURNING id`

const insertScoreSQL = `
INSERT INTO message_ai_scores (
	run_id, group_id, chat_id, quote_day, message_db_id, telegram_message_id, reply_to_message_id, user_id,
	author_name_snapshot, text_snapshot, content_type, caption_snapshot, reactions_snapshot, reaction_count,
	media_status, media_description_snapshot, media_kind, telegram_file_id, telegram_file_unique_id, mime_type,
	file_name, file_size, width, height, duration, sha256, phash, media_cache_id,
	ai_score, ai_score_raw, rank, is_selected_primary, is_selected_context, created_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21, $22, $23,
	$24, $25, $26, $27, $28, $29, $30, $31, $32, $33, $34)`

// runRow is an ai_evaluation_runs row. Every column is set: the schema's only
// defaults are on other tables.
type runRow struct {
	groupID, chatID               int64
	quoteDay                      time.Time
	windowStart, windowEnd        time.Time
	requestedModel, actualModel   string
	status                        string
	messageCount, sourceCount     int
	selectedDBID, selectedTGID    any // int64 or nil
	contextIDs                    any // string or nil
	contextNeeded                 bool
	shouldPublish                 any // bool or nil
	reasonCode, reasonText        any // string or nil
	requestID                     any // string or nil
	prompt, completion, reasoning any // int or nil
	total                         any // int or nil
	cost                          any // float64 or nil
	createdAt                     time.Time
}

func (r runRow) args() []any {
	return []any{
		r.groupID, r.chatID, r.quoteDay, r.windowStart, r.windowEnd, r.requestedModel, r.actualModel, r.status,
		r.messageCount, r.sourceCount, r.selectedDBID, r.selectedTGID,
		r.contextIDs, r.contextNeeded, r.shouldPublish, r.reasonCode, r.reasonText, r.requestID,
		r.prompt, r.completion, r.reasoning, r.total, r.cost, r.createdAt,
	}
}

// runValues maps the report to its run row.
func (r *PGReports) runValues(rep domain.EvaluationReport, now time.Time) runRow {
	res := rep.Result
	status := res.Status
	if status == "" {
		status = domain.RunParsed
	}
	row := runRow{
		groupID:        rep.GroupID,
		chatID:         rep.GroupID,
		quoteDay:       rep.Window.QuoteDay,
		windowStart:    rep.Window.Start.UTC(),
		windowEnd:      rep.Window.End.UTC(),
		requestedModel: firstNonEmpty(res.RequestedModel, r.fallbackModel, unknownModel),
		actualModel:    firstNonEmpty(res.ActualModel, r.fallbackModel, unknownModel),
		status:         status,
		messageCount:   len(rep.Scored),
		sourceCount:    rep.SourceCount,
		requestID:      nilIfEmpty(res.RequestID),
		createdAt:      now,
	}
	if m := findScored(rep.Scored, rep.SelectedID); m != nil {
		row.selectedDBID, row.selectedTGID = m.ID, m.MessageID
	}
	if ids := contextTelegramIDs(rep); len(ids) > 0 {
		row.contextIDs = compactIDs(ids)
	}
	if c := res.QuoteChoice; c != nil {
		row.contextNeeded = c.ContextNeeded
	}
	if v := res.DayVerdict; v != nil {
		row.shouldPublish, row.reasonCode, row.reasonText = v.ShouldPublish, v.ReasonCode, v.ReasonText
	}
	if u := res.Usage; u != nil {
		row.prompt, row.completion = ptrAny(u.PromptTokens), ptrAny(u.CompletionTokens)
		row.reasoning, row.total = ptrAny(u.ReasoningTokens), ptrAny(u.TotalTokens)
		row.cost = ptrAny(u.CostUSD)
	}
	return row
}

// scoreRow is a message_ai_scores row, apart from what comes from the report.
type scoreRow struct {
	msg       domain.WindowMessage
	author    string
	aiScore   float64 // clamped to 0..1
	rank      int
	primary   bool
	inContext bool
}

// scoreRows lists the messages the report records: those the model scored, the
// quote, and the messages the model picked as context. They are ranked by score,
// highest first, and by position in the day among equal scores; a message that
// was not scored ranks as 0. The context is the model's own selection, before
// the bot cut it to five, so an id the model picked counts even if the post
// does not show it.
func scoreRows(rep domain.EvaluationReport) []scoreRow {
	scores := make(map[int64]float64, len(rep.Result.Scores))
	for _, s := range rep.Result.Scores {
		scores[s.ID] = s.Value
	}
	inContext := make(map[int64]bool)
	for _, id := range contextInternalIDs(rep.Result.QuoteChoice) {
		inContext[id] = true
	}

	var rows []scoreRow
	for _, m := range rep.Scored {
		_, scored := scores[m.ID]
		if !scored && m.ID != rep.SelectedID && !inContext[m.ID] {
			continue
		}
		author, ok := rep.AuthorNames[m.UserID]
		if !ok {
			author = unknownAuthor
		}
		rows = append(rows, scoreRow{
			msg:       m,
			author:    author,
			aiScore:   min(1, max(0, scores[m.ID])),
			primary:   m.ID == rep.SelectedID,
			inContext: inContext[m.ID],
		})
	}

	// rows are in the day's order, so a stable sort by score keeps position as
	// the tie-break.
	order := make([]int, len(rows))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool { return scores[rows[order[a]].msg.ID] > scores[rows[order[b]].msg.ID] })
	for rank, i := range order {
		rows[i].rank = rank + 1
	}
	return rows
}

// unknownAuthor is the name the model saw for a person core has no name for.
const unknownAuthor = "Unknown"

func (s scoreRow) args(runID int64, rep domain.EvaluationReport, now time.Time) []any {
	m := s.msg
	var media domain.MediaSnapshot
	if m.Media != nil {
		media = *m.Media
	}
	contentType := m.ContentType
	if contentType == "" {
		contentType = domain.ContentText
	}
	return []any{
		runID, rep.GroupID, rep.GroupID, rep.Window.QuoteDay, m.ID, m.MessageID, nilIfZero(m.ReplyTo), nilIfZero(m.UserID),
		s.author, m.Text, contentType, nilIfEmpty(m.Caption), nilIfEmpty(reactionsJSON(m.Reactions)), m.ReactionTotal(),
		nilIfEmpty(m.MediaStatus), nilIfEmpty(media.Description), nilIfEmpty(media.Kind), nilIfEmpty(media.FileID),
		nilIfEmpty(media.FileUniqueID), nilIfEmpty(media.MimeType),
		nilIfEmpty(media.FileName), nilIfZero(media.FileSize), nilIfZero(int64(media.Width)), nilIfZero(int64(media.Height)),
		nilIfZeroF(media.Duration), nilIfEmpty(media.SHA256), nilIfEmpty(media.Phash), nilIfZero(media.CacheID),
		s.aiScore, s.aiScore * 10.0, s.rank, s.primary, s.inContext, now,
	}
}

// contextInternalIDs are the context ids the model gave, without repeats.
func contextInternalIDs(c *domain.QuoteChoice) []int64 {
	if c == nil {
		return nil
	}
	seen := make(map[int64]bool, len(c.ContextIDs))
	var out []int64
	for _, id := range c.ContextIDs {
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}

// contextTelegramIDs maps the model's context ids to Telegram ids, dropping the
// ones that are not in the day.
func contextTelegramIDs(rep domain.EvaluationReport) []int64 {
	var out []int64
	for _, id := range contextInternalIDs(rep.Result.QuoteChoice) {
		if m := findScored(rep.Scored, id); m != nil {
			out = append(out, m.MessageID)
		}
	}
	return out
}

func findScored(ms []domain.WindowMessage, id int64) *domain.WindowMessage {
	if id == 0 {
		return nil
	}
	for i := range ms {
		if ms[i].ID == id {
			return &ms[i]
		}
	}
	return nil
}

// compactIDs is a JSON list of ids as Python's json.dumps(separators=(",", ":"))
// wrote it: "[11,12]".
func compactIDs(ids []int64) string {
	parts := make([]string, len(ids))
	for i, id := range ids {
		parts[i] = strconv.FormatInt(id, 10)
	}
	return "[" + strings.Join(parts, ",") + "]"
}

// reactionsJSON is the message's reactions as a compact JSON object in stored
// order, {"😂":2,"❤️":1}; empty when there are none. It is built by hand because
// a map would sort the keys, and json.Marshal would escape < > & as Python's
// ensure_ascii=False does not.
func reactionsJSON(rs []domain.Reaction) string {
	var b strings.Builder
	for _, r := range rs {
		if r.Count <= 0 {
			continue
		}
		if b.Len() == 0 {
			b.WriteByte('{')
		} else {
			b.WriteByte(',')
		}
		b.WriteString(jsonString(r.Emoji))
		b.WriteByte(':')
		b.WriteString(strconv.Itoa(r.Count))
	}
	if b.Len() == 0 {
		return ""
	}
	b.WriteByte('}')
	return b.String()
}

func jsonString(s string) string {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(s)
	return strings.TrimRight(buf.String(), "\n")
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

func nilIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func nilIfZero(n int64) any {
	if n == 0 {
		return nil
	}
	return n
}

func nilIfZeroF(f float64) any {
	if f == 0 {
		return nil
	}
	return f
}

// ptrAny turns a nil pointer into an untyped nil, which pgx sends as NULL, and
// any other pointer into its value.
func ptrAny[T any](p *T) any {
	if p == nil {
		return nil
	}
	return *p
}
