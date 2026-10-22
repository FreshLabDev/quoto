// SPDX-License-Identifier: Apache-2.0

package db

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/FreshLabDev/quoto/internal/domain"
)

// Stats count published quotes only: a quote that failed, is still being sent
// or was a boring day is not a win.

type statsQuoteRow struct {
	text   string
	score  float64
	author int64
	date   time.Time
}

// bestQuote is the highest-scored published quote of the group, optionally of
// one author. Ties go to the earliest quote, then the lowest id, so the card
// does not change between two reads.
func (s *Store) bestQuote(ctx context.Context, chatID int64, author *int64) (*statsQuoteRow, error) {
	var q statsQuoteRow
	err := s.pool.QueryRow(ctx, `
		SELECT text, score, author_id, created_at FROM quotes
		WHERE group_id = $1 AND decision_status = $2 AND ($3::bigint IS NULL OR author_id = $3)
		ORDER BY score DESC, created_at ASC, id ASC LIMIT 1`, chatID, domain.StatusPublished, author).
		Scan(&q.text, &q.score, &q.author, &q.date)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	return &q, err
}

func (s *Store) groupExists(ctx context.Context, chatID int64) (bool, error) {
	var ok bool
	err := s.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM group_settings WHERE chat_id = $1)`, chatID).Scan(&ok)
	return ok, err
}

// Chat implements domain.StatsReader.
func (s *Store) Chat(ctx context.Context, chatID int64) (*domain.ChatStats, error) {
	ok, err := s.groupExists(ctx, chatID)
	if err != nil || !ok {
		return nil, err
	}
	var title *string
	if err := s.pool.QueryRow(ctx, `SELECT title FROM core.chat WHERE chat_id = $1`, chatID).Scan(&title); err != nil && err != pgx.ErrNoRows {
		return nil, err
	}
	out := &domain.ChatStats{GroupName: domain.PyStrip(str(title))}

	var avg *float64
	if err := s.pool.QueryRow(ctx, `
		SELECT count(*), count(DISTINCT author_id), avg(score) FROM quotes
		WHERE group_id = $1 AND decision_status = $2`, chatID, domain.StatusPublished).
		Scan(&out.TotalQuotes, &out.UniqueAuthors, &avg); err != nil {
		return nil, err
	}
	if out.TotalQuotes == 0 {
		return out, nil
	}
	out.AvgScore = f(avg)

	rows, err := s.pool.Query(ctx, `
		SELECT author_id, count(*) AS wins, avg(score) FROM quotes
		WHERE group_id = $1 AND decision_status = $2
		GROUP BY author_id ORDER BY wins DESC, avg(score) DESC, author_id ASC LIMIT 3`, chatID, domain.StatusPublished)
	if err != nil {
		return nil, err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		var line domain.AuthorWins
		var avg *float64
		if err := rows.Scan(&id, &line.Wins, &avg); err != nil {
			rows.Close()
			return nil, err
		}
		line.AvgScore = f(avg)
		ids = append(ids, id)
		out.TopAuthors = append(out.TopAuthors, line)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	best, err := s.bestQuote(ctx, chatID, nil)
	if err != nil {
		return nil, err
	}
	allIDs := ids
	if best != nil {
		allIDs = append(allIDs, best.author)
	}
	names, err := s.DisplayNames(ctx, allIDs)
	if err != nil {
		return nil, err
	}
	for i := range out.TopAuthors {
		out.TopAuthors[i].Name = names[ids[i]]
	}
	if best != nil {
		out.Best = &domain.StatsQuote{Text: best.text, Score: best.score, Author: names[best.author], Date: best.date}
	}
	return out, nil
}

// User implements domain.StatsReader.
func (s *Store) User(ctx context.Context, chatID, userID int64) (*domain.UserStats, error) {
	ok, err := s.groupExists(ctx, chatID)
	if err != nil || !ok {
		return nil, err
	}
	names, err := s.DisplayNames(ctx, []int64{userID})
	if err != nil {
		return nil, err
	}
	out := &domain.UserStats{UserName: names[userID]}

	var avg *float64
	if err := s.pool.QueryRow(ctx, `
		SELECT count(*), avg(score) FROM quotes
		WHERE group_id = $1 AND author_id = $2 AND decision_status = $3`, chatID, userID, domain.StatusPublished).
		Scan(&out.Wins, &avg); err != nil {
		return nil, err
	}
	if out.Wins == 0 {
		return out, nil
	}
	out.AvgScore = f(avg)

	if err := s.pool.QueryRow(ctx, `
		SELECT 1 + count(*) FILTER (WHERE wins > $3), count(*) FROM (
			SELECT count(*) AS wins FROM quotes
			WHERE group_id = $1 AND decision_status = $2 GROUP BY author_id) per_author`,
		chatID, domain.StatusPublished, out.Wins).Scan(&out.Rank, &out.TotalParticipants); err != nil {
		return nil, err
	}

	best, err := s.bestQuote(ctx, chatID, &userID)
	if err != nil {
		return nil, err
	}
	if best != nil {
		out.Best = &domain.StatsQuote{Text: best.text, Score: best.score, Date: best.date}
	}
	return out, nil
}
