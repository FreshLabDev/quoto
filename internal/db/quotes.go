// SPDX-License-Identifier: Apache-2.0

package db

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/FreshLabDev/quoto/internal/domain"
)

const quoteColumns = `q.id, q.group_id, q.author_id, q.text, q.score, q.reaction_score, q.ai_score, q.length_score,
	q.reaction_count, q.message_id, q.content_type, q.bot_message_id, q.notice_message_id, q.ai_model, q.ai_best_text,
	q.context_snapshot, q.quote_day, q.window_start_at, q.window_end_at, q.decision_status, q.status_changed_at,
	q.decision_reason, q.operation_error, q.created_at`

func scanQuote(row pgx.Row, extra ...any) (domain.Quote, error) {
	var q domain.Quote
	var reaction, ai, length *float64
	var reactionCount *int32
	var messageID, botID, noticeID *int64
	var model, best, snapshot, reason, opErr *string
	dest := []any{&q.ID, &q.GroupID, &q.AuthorID, &q.Text, &q.Score, &reaction, &ai, &length,
		&reactionCount, &messageID, &q.ContentType, &botID, &noticeID, &model, &best,
		&snapshot, &q.QuoteDay, &q.WindowStart, &q.WindowEnd, &q.DecisionStatus, &q.StatusChangedAt,
		&reason, &opErr, &q.CreatedAt}
	if err := row.Scan(append(dest, extra...)...); err != nil {
		return domain.Quote{}, err
	}
	q.ReactionScore, q.AIScore, q.LengthScore, q.ReactionCount = f(reaction), f(ai), f(length), i(reactionCount)
	q.MessageID, q.BotMessageID, q.NoticeMessageID = i64(messageID), i64(botID), i64(noticeID)
	q.AIModel, q.AIBestText, q.DecisionReason, q.OperationError = str(model), str(best), str(reason), str(opErr)
	q.Context = domain.DecodeContext(str(snapshot))
	return q, nil
}

// ForDay implements domain.Quotes.
func (s *Store) ForDay(ctx context.Context, chatID int64, day time.Time) (*domain.Quote, error) {
	q, err := scanQuote(s.pool.QueryRow(ctx,
		`SELECT `+quoteColumns+` FROM quotes q WHERE q.group_id = $1 AND q.quote_day = $2`, chatID, day))
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &q, nil
}

// Create implements domain.Quotes.
func (s *Store) Create(ctx context.Context, n domain.NewQuote) (domain.Quote, bool, error) {
	snapshot, ids := domain.EncodeContext(n.Context)
	now := s.utcNow()
	var id int64
	err := s.pool.QueryRow(ctx, `
		INSERT INTO quotes (group_id, author_id, text, score, reaction_score, ai_score, length_score, reaction_count,
			message_id, content_type, ai_model, ai_best_text, context_message_ids, context_snapshot, quote_day,
			window_start_at, window_end_at, decision_status, status_changed_at, decision_reason, operation_error,
			forced_by_admin, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21, false, $19)
		ON CONFLICT (group_id, quote_day) DO NOTHING
		RETURNING id`,
		n.GroupID, n.AuthorID, n.Text, n.Score, n.ReactionScore, n.AIScore, n.LengthScore, n.ReactionCount,
		nullInt64(n.MessageID), n.ContentType, nullStr(n.AIModel), nullStr(n.AIBestText), nullStr(ids), nullStr(snapshot),
		n.QuoteDay, n.WindowStart.UTC(), n.WindowEnd.UTC(), n.DecisionStatus, now,
		nullStr(n.DecisionReason), nullStr(n.OperationError)).Scan(&id)
	created := err == nil
	if err == pgx.ErrNoRows {
		err = nil // another worker, or an earlier run of this one, got the day
	}
	if err != nil {
		return domain.Quote{}, false, err
	}
	existing, err := s.ForDay(ctx, n.GroupID, n.QuoteDay)
	if err != nil {
		return domain.Quote{}, false, err
	}
	if existing == nil {
		return domain.Quote{}, false, pgx.ErrNoRows
	}
	return *existing, created, nil
}

// MarkPublished implements domain.Quotes.
func (s *Store) MarkPublished(ctx context.Context, id, botMessageID int64, reason string) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE quotes SET bot_message_id = $2, decision_status = $3, status_changed_at = $4,
			decision_reason = COALESCE(NULLIF($5::text, ''), decision_reason), operation_error = NULL
		WHERE id = $1`, id, botMessageID, domain.StatusPublished, s.utcNow(), reason)
	return err
}

// MarkNotified implements domain.Quotes.
func (s *Store) MarkNotified(ctx context.Context, id, noticeMessageID int64) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE quotes SET notice_message_id = $2, decision_status = $3, status_changed_at = $4, operation_error = NULL
		WHERE id = $1`, id, noticeMessageID, domain.StatusSkippedBoring, s.utcNow())
	return err
}

// SetStatus implements domain.Quotes.
func (s *Store) SetStatus(ctx context.Context, id int64, status string, reason, operationError domain.TextPatch) error {
	sets := []string{"decision_status = $2", "status_changed_at = $3"}
	args := []any{id, status, s.utcNow()}
	for _, p := range []struct {
		column string
		patch  domain.TextPatch
	}{{"decision_reason", reason}, {"operation_error", operationError}} {
		if !p.patch.Set {
			continue
		}
		args = append(args, p.patch.Value)
		sets = append(sets, p.column+" = $"+strconv.Itoa(len(args)))
	}
	_, err := s.pool.Exec(ctx, `UPDATE quotes SET `+strings.Join(sets, ", ")+` WHERE id = $1`, args...)
	return err
}

// maxOperationErrorLen is how much of one error message is kept.
const maxOperationErrorLen = 250

// AppendError implements domain.Quotes.
func (s *Store) AppendError(ctx context.Context, id int64, msg string) error {
	return s.inTx(ctx, func(tx pgx.Tx) error {
		var current *string
		if err := tx.QueryRow(ctx, `SELECT operation_error FROM quotes WHERE id = $1 FOR UPDATE`, id).Scan(&current); err != nil {
			if err == pgx.ErrNoRows {
				return nil
			}
			return err
		}
		next := cut(domain.PyStrip(msg), maxOperationErrorLen)
		if existing := domain.PyStrip(str(current)); existing != "" {
			next = strings.Trim(existing+" | "+next, " |")
		}
		_, err := tx.Exec(ctx, `UPDATE quotes SET operation_error = $2 WHERE id = $1`, id, next)
		return err
	})
}

// Delete implements domain.Quotes.
func (s *Store) Delete(ctx context.Context, id int64) (bool, error) {
	return s.exec1(ctx, `DELETE FROM quotes WHERE id = $1`, id)
}

// Stale implements domain.Quotes.
func (s *Store) Stale(ctx context.Context, chatID int64, olderThan time.Time) ([]domain.Quote, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT `+quoteColumns+` FROM quotes q
		WHERE q.group_id = $1 AND q.decision_status IN ($2, $3) AND q.status_changed_at < $4
		ORDER BY q.window_end_at DESC`, chatID, domain.StatusPublishing, domain.StatusNotifyingBoring, olderThan)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Quote
	for rows.Next() {
		q, err := scanQuote(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, q)
	}
	return out, rows.Err()
}

// Detail implements domain.Quotes.
func (s *Store) Detail(ctx context.Context, id int64, publicOnly bool) (*domain.QuoteDetail, error) {
	query := `SELECT ` + quoteColumns + `, p.first_name, p.last_name, p.username, c.title
		FROM quotes q
		LEFT JOIN core.person p ON p.telegram_user_id = q.author_id
		LEFT JOIN core.chat c ON c.chat_id = q.group_id
		WHERE q.id = $1`
	args := []any{id}
	if publicOnly {
		query += ` AND q.decision_status = $2`
		args = append(args, domain.StatusPublished)
	}
	var first, last, username, title *string
	q, err := scanQuote(s.pool.QueryRow(ctx, query, args...), &first, &last, &username, &title)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if publicOnly {
		q.OperationError = ""
	}
	return &domain.QuoteDetail{
		Quote:      q,
		AuthorName: displayName(str(first), str(last), str(username)),
		GroupName:  str(title),
	}, nil
}
