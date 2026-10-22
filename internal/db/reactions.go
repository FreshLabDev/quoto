// SPDX-License-Identifier: Apache-2.0

package db

import (
	"context"
	"sort"

	"github.com/jackc/pgx/v5"
)

// lockMessage locks the stored message row and returns its id, or 0 when the
// message is not stored. Locking the message first, then its reactions, is the
// order every reaction writer keeps, so concurrent updates cannot deadlock.
func lockMessage(ctx context.Context, tx pgx.Tx, chatID, messageID int64) (int64, error) {
	var id int64
	err := tx.QueryRow(ctx, `SELECT id FROM messages WHERE message_id = $1 AND chat_id = $2 FOR UPDATE`, messageID, chatID).Scan(&id)
	if err == pgx.ErrNoRows {
		return 0, nil
	}
	return id, err
}

// SyncAbsolute implements domain.Reactions. A count of zero or less counts as
// absent and removes the row.
func (s *Store) SyncAbsolute(ctx context.Context, chatID, messageID int64, counts map[string]int) error {
	return s.inTx(ctx, func(tx pgx.Tx) error {
		id, err := lockMessage(ctx, tx, chatID, messageID)
		if err != nil || id == 0 {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT emoji, count FROM reactions WHERE message_db_id = $1 FOR UPDATE`, id)
		if err != nil {
			return err
		}
		existing := map[string]int{}
		for rows.Next() {
			var emoji string
			var count int
			if err := rows.Scan(&emoji, &count); err != nil {
				rows.Close()
				return err
			}
			existing[emoji] = count
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		for emoji := range existing {
			if counts[emoji] <= 0 {
				if _, err := tx.Exec(ctx, `DELETE FROM reactions WHERE message_db_id = $1 AND emoji = $2`, id, emoji); err != nil {
					return err
				}
			}
		}
		for _, emoji := range sortedKeys(counts) {
			count := counts[emoji]
			if count <= 0 || existing[emoji] == count {
				continue
			}
			if _, err := tx.Exec(ctx, `
				INSERT INTO reactions (message_db_id, emoji, count) VALUES ($1, $2, $3)
				ON CONFLICT (message_db_id, emoji) DO UPDATE SET count = EXCLUDED.count`, id, emoji, count); err != nil {
				return err
			}
		}
		return nil
	})
}

// ApplyDelta implements domain.Reactions.
func (s *Store) ApplyDelta(ctx context.Context, chatID, messageID int64, deltas map[string]int) error {
	if len(deltas) == 0 {
		return nil
	}
	return s.inTx(ctx, func(tx pgx.Tx) error {
		id, err := lockMessage(ctx, tx, chatID, messageID)
		if err != nil || id == 0 {
			return err
		}
		for _, emoji := range sortedKeys(deltas) {
			delta := deltas[emoji]
			if delta == 0 {
				continue
			}
			var count int
			if delta > 0 {
				err = tx.QueryRow(ctx, `
					INSERT INTO reactions (message_db_id, emoji, count) VALUES ($1, $2, $3)
					ON CONFLICT (message_db_id, emoji) DO UPDATE SET count = reactions.count + EXCLUDED.count
					RETURNING count`, id, emoji, delta).Scan(&count)
			} else {
				// Taking away a reaction we never saw is a no-op, not a negative row.
				err = tx.QueryRow(ctx,
					`UPDATE reactions SET count = count + $3 WHERE message_db_id = $1 AND emoji = $2 RETURNING count`,
					id, emoji, delta).Scan(&count)
				if err == pgx.ErrNoRows {
					continue
				}
			}
			if err != nil {
				return err
			}
			if count <= 0 {
				if _, err := tx.Exec(ctx, `DELETE FROM reactions WHERE message_db_id = $1 AND emoji = $2`, id, emoji); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

func sortedKeys(m map[string]int) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
