// SPDX-License-Identifier: Apache-2.0

package db

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/FreshLabDev/quoto/internal/domain"
)

// saveAttempts and saveBackoff keep the Python behaviour: a message that fails
// to store is tried three times, half a second and a second apart, before the
// error is returned.
const saveAttempts = 3

var saveBackoff = []time.Duration{500 * time.Millisecond, time.Second}

// Save implements domain.Messages.
func (s *Store) Save(ctx context.Context, m domain.NewMessage) (int64, bool, error) {
	var lastErr error
	for attempt := 0; attempt < saveAttempts; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return 0, false, ctx.Err()
			case <-time.After(saveBackoff[attempt-1]):
			}
		}
		id, inserted, err := s.saveOnce(ctx, m)
		if err == nil {
			return id, inserted, nil
		}
		lastErr = err
		// A foreign key or a NOT NULL violation will fail the same way again.
		if hasCode(err, pgForeignKeyViolation) || hasCode(err, pgNotNullViolation) || ctx.Err() != nil {
			break
		}
	}
	return 0, false, lastErr
}

func (s *Store) saveOnce(ctx context.Context, m domain.NewMessage) (id int64, inserted bool, err error) {
	err = s.inTx(ctx, func(tx pgx.Tx) error {
		var mediaStatus any
		if m.Media != nil {
			mediaStatus = domain.MediaPending
		}
		err := tx.QueryRow(ctx, `
			INSERT INTO messages (message_id, chat_id, user_id, text, content_type, caption, media_status, reply_to_message_id, created_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
			ON CONFLICT (message_id, chat_id) DO NOTHING
			RETURNING id`,
			m.MessageID, m.ChatID, m.UserID, m.Text, m.ContentType, nullStr(m.Caption), mediaStatus,
			nullInt64(m.ReplyTo), m.CreatedAt.UTC()).Scan(&id)
		if err == pgx.ErrNoRows {
			return nil // Telegram delivered it twice
		}
		if err != nil {
			return err
		}
		inserted = true
		if m.Media == nil {
			return nil
		}
		src := m.Media
		_, err = tx.Exec(ctx, `
			INSERT INTO message_media (message_db_id, media_kind, telegram_file_id, telegram_file_unique_id, mime_type,
				file_name, file_size, width, height, duration, analysis_status, created_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)`,
			id, src.Kind, nullStr(src.FileID), nullStr(src.FileUniqueID), nullStr(src.MimeType),
			nullStr(src.FileName), nullInt64(src.FileSize), nullInt(src.Width), nullInt(src.Height),
			nullFloat(src.Duration), domain.MediaPending, s.utcNow())
		return err
	})
	return id, inserted, err
}

// Edit implements domain.Messages.
func (s *Store) Edit(ctx context.Context, e domain.MessageEdit) error {
	if e.KeepText {
		_, err := s.pool.Exec(ctx,
			`UPDATE messages SET content_type = $3, caption = $4, reply_to_message_id = $5 WHERE message_id = $1 AND chat_id = $2`,
			e.MessageID, e.ChatID, e.ContentType, nullStr(e.Caption), nullInt64(e.ReplyTo))
		return err
	}
	_, err := s.pool.Exec(ctx,
		`UPDATE messages SET text = $6, content_type = $3, caption = $4, reply_to_message_id = $5 WHERE message_id = $1 AND chat_id = $2`,
		e.MessageID, e.ChatID, e.ContentType, nullStr(e.Caption), nullInt64(e.ReplyTo), e.Text)
	return err
}

// CountWindow implements domain.Messages.
func (s *Store) CountWindow(ctx context.Context, chatID int64, w domain.Window) (int, error) {
	var n int
	err := s.pool.QueryRow(ctx,
		`SELECT count(*) FROM messages WHERE chat_id = $1 AND created_at >= $2 AND created_at < $3`,
		chatID, w.Start, w.End).Scan(&n)
	return n, err
}

// DeleteWindow implements domain.Messages.
func (s *Store) DeleteWindow(ctx context.Context, chatID int64, w domain.Window) (int, error) {
	tag, err := s.pool.Exec(ctx,
		`DELETE FROM messages WHERE chat_id = $1 AND created_at >= $2 AND created_at < $3`, chatID, w.Start, w.End)
	return int(tag.RowsAffected()), err
}

// deleteBatch is how many stale messages one retention statement removes, so a
// first run over a large backlog does not hold one long lock.
const deleteBatch = 1000

// DeleteOlderThan implements domain.Messages.
func (s *Store) DeleteOlderThan(ctx context.Context, t time.Time) (int64, error) {
	var total int64
	for {
		tag, err := s.pool.Exec(ctx,
			`DELETE FROM messages WHERE id IN (SELECT id FROM messages WHERE created_at < $1 LIMIT $2)`, t, deleteBatch)
		if err != nil {
			return total, err
		}
		total += tag.RowsAffected()
		if tag.RowsAffected() < deleteBatch {
			return total, nil
		}
	}
}

// ListWindow implements domain.Messages.
func (s *Store) ListWindow(ctx context.Context, chatID int64, w domain.Window) ([]domain.WindowMessage, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, message_id, user_id, text, content_type, caption, media_status, reply_to_message_id, created_at
		FROM messages
		WHERE chat_id = $1 AND created_at >= $2 AND created_at < $3
		ORDER BY created_at, id`, chatID, w.Start, w.End)
	if err != nil {
		return nil, err
	}
	var msgs []domain.WindowMessage
	index := map[int64]int{}
	for rows.Next() {
		var m domain.WindowMessage
		var caption, mediaStatus *string
		var replyTo *int64
		if err := rows.Scan(&m.ID, &m.MessageID, &m.UserID, &m.Text, &m.ContentType, &caption, &mediaStatus, &replyTo, &m.CreatedAt); err != nil {
			rows.Close()
			return nil, err
		}
		m.Caption, m.MediaStatus, m.ReplyTo = str(caption), str(mediaStatus), i64(replyTo)
		index[m.ID] = len(msgs)
		msgs = append(msgs, m)
	}
	rows.Close()
	if err := rows.Err(); err != nil || len(msgs) == 0 {
		return msgs, err
	}
	ids := make([]int64, len(msgs))
	for k, m := range msgs {
		ids[k] = m.ID
	}

	reactions, err := s.pool.Query(ctx,
		`SELECT message_db_id, emoji, count FROM reactions WHERE message_db_id = ANY($1) ORDER BY message_db_id, id`, ids)
	if err != nil {
		return nil, err
	}
	for reactions.Next() {
		var id int64
		var r domain.Reaction
		if err := reactions.Scan(&id, &r.Emoji, &r.Count); err != nil {
			reactions.Close()
			return nil, err
		}
		msgs[index[id]].Reactions = append(msgs[index[id]].Reactions, r)
	}
	reactions.Close()
	if err := reactions.Err(); err != nil {
		return nil, err
	}

	// Only the first media row of a message is ever read.
	media, err := s.pool.Query(ctx, `
		SELECT DISTINCT ON (message_db_id) message_db_id, id, analysis_status, description_snapshot, media_kind,
			telegram_file_id, telegram_file_unique_id, mime_type, file_name, file_size, width, height, duration,
			sha256, phash, media_cache_id
		FROM message_media WHERE message_db_id = ANY($1) ORDER BY message_db_id, id`, ids)
	if err != nil {
		return nil, err
	}
	for media.Next() {
		var msgID int64
		var snap domain.MediaSnapshot
		var description, fileID, uniqueID, mime, fileName, sha, phash *string
		var size, cacheID *int64
		var width, height *int32
		var duration *float64
		if err := media.Scan(&msgID, &snap.ID, &snap.Status, &description, &snap.Kind, &fileID, &uniqueID, &mime,
			&fileName, &size, &width, &height, &duration, &sha, &phash, &cacheID); err != nil {
			media.Close()
			return nil, err
		}
		snap.Description, snap.FileID, snap.FileUniqueID, snap.MimeType, snap.FileName =
			str(description), str(fileID), str(uniqueID), str(mime), str(fileName)
		snap.FileSize, snap.Width, snap.Height, snap.Duration = i64(size), i(width), i(height), f(duration)
		snap.SHA256, snap.Phash, snap.CacheID = str(sha), str(phash), i64(cacheID)
		msgs[index[msgID]].Media = &snap
	}
	media.Close()
	return msgs, media.Err()
}
