// SPDX-License-Identifier: Apache-2.0

package media

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/FreshLabDev/quoto/internal/domain"
)

// PGStore is the PostgreSQL implementation of Store. It uses the pool of
// *db.Store (search_path is quoto), so the tables are named without a schema.
type PGStore struct {
	pool *pgxpool.Pool
	// promptVersion is the cache's partition: only rows of this version are read.
	// Rows of older versions stay in the table and are never touched (decision R5).
	promptVersion string
	// now stamps the rows the store creates; tests replace it.
	now func() time.Time
}

// NewPGStore returns a store on pool, which must have search_path set to the
// quoto schema, as the pool of db.Store does.
func NewPGStore(pool *pgxpool.Pool) *PGStore {
	return &PGStore{pool: pool, promptVersion: domain.MediaPromptVersion, now: time.Now}
}

var _ Store = (*PGStore)(nil)

// itemColumns is what Item is scanned from, in this order.
const itemColumns = `id, message_db_id, media_kind, telegram_file_id, telegram_file_unique_id, mime_type,
	file_name, file_size, width, height, duration, analysis_status, retry_count`

func scanItem(row pgx.Row) (Item, error) {
	var (
		it                               Item
		fileID, uniqueID, mime, fileName *string
		size                             *int64
		width, height                    *int32
		duration                         *float64
		retries                          int32
	)
	if err := row.Scan(&it.ID, &it.MessageDBID, &it.Kind, &fileID, &uniqueID, &mime, &fileName,
		&size, &width, &height, &duration, &it.Status, &retries); err != nil {
		return Item{}, err
	}
	it.FileID, it.FileUniqueID, it.MimeType, it.FileName = deref(fileID), deref(uniqueID), deref(mime), deref(fileName)
	if size != nil {
		it.FileSize = *size
	}
	if width != nil {
		it.Width = int(*width)
	}
	if height != nil {
		it.Height = int(*height)
	}
	if duration != nil {
		it.Duration = *duration
	}
	it.RetryCount = int(retries)
	return it, nil
}

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// nullable maps the empty string to NULL.
func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// nullablePositive maps 0 to NULL: a token count of 0 means "not reported".
func nullablePositive(n int) any {
	if n == 0 {
		return nil
	}
	return n
}

// Claim implements Store.Claim.
//
// Python wrote the condition as (pending and free) or (processing and free); the
// two branches are the same test on a different status, so it is one here. A
// processing row whose lease has run out is claimable again: its worker died.
func (s *PGStore) Claim(ctx context.Context, messageDBID, itemID int64, now time.Time, lease time.Duration) (Item, bool, error) {
	row := s.pool.QueryRow(ctx, `
		UPDATE message_media
		SET analysis_status = '`+domain.MediaProcessing+`', next_retry_at = $4
		WHERE message_db_id = $1
		  AND id = COALESCE(NULLIF($2::bigint, 0), (SELECT id FROM message_media WHERE message_db_id = $1 ORDER BY id LIMIT 1))
		  AND analysis_status IN ('`+domain.MediaPending+`', '`+domain.MediaProcessing+`')
		  AND (next_retry_at IS NULL OR next_retry_at <= $3)
		RETURNING `+itemColumns,
		messageDBID, itemID, now.UTC(), now.Add(lease).UTC())
	it, err := scanItem(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return Item{}, false, nil
	}
	if err != nil {
		return Item{}, false, fmt.Errorf("claim media row: %w", err)
	}
	return it, true, nil
}

// Item implements Store.Item.
func (s *PGStore) Item(ctx context.Context, messageDBID int64) (Item, bool, error) {
	it, err := scanItem(s.pool.QueryRow(ctx,
		`SELECT `+itemColumns+` FROM message_media WHERE message_db_id = $1 ORDER BY id LIMIT 1`, messageDBID))
	if errors.Is(err, pgx.ErrNoRows) {
		return Item{}, false, nil
	}
	if err != nil {
		return Item{}, false, fmt.Errorf("read media row: %w", err)
	}
	return it, true, nil
}

// Pending implements Store.Pending.
func (s *PGStore) Pending(ctx context.Context, now time.Time, minAge time.Duration, limit int) ([]Item, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT `+itemColumns+` FROM message_media
		WHERE (analysis_status = '`+domain.MediaPending+`'
		       AND ((next_retry_at IS NULL AND created_at <= $1) OR next_retry_at <= $2))
		   OR (analysis_status = '`+domain.MediaProcessing+`'
		       AND (next_retry_at IS NULL OR next_retry_at <= $2))
		ORDER BY next_retry_at ASC NULLS FIRST, created_at ASC
		LIMIT $3`, now.Add(-minAge).UTC(), now.UTC(), limit)
	if err != nil {
		return nil, fmt.Errorf("list pending media: %w", err)
	}
	defer rows.Close()
	var items []Item
	for rows.Next() {
		it, err := scanItem(rows)
		if err != nil {
			return nil, fmt.Errorf("list pending media: %w", err)
		}
		items = append(items, it)
	}
	return items, rows.Err()
}

// Group implements Store.Group.
func (s *PGStore) Group(ctx context.Context, messageDBID int64) (domain.GroupSettings, bool, error) {
	var g domain.GroupSettings
	err := s.pool.QueryRow(ctx, `
		SELECT g.media_analysis_enabled, g.agreement_accepted_at, g.agreement_version
		FROM messages m JOIN group_settings g ON g.chat_id = m.chat_id
		WHERE m.id = $1`, messageDBID).Scan(&g.MediaAnalysis, &g.AgreementAcceptedAt, &g.AgreementVersion)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.GroupSettings{}, false, nil
	}
	if err != nil {
		return domain.GroupSettings{}, false, fmt.Errorf("read group settings: %w", err)
	}
	return g, true, nil
}

// cacheColumn maps a key to its column. The names are fixed here, never taken
// from input.
func cacheColumn(by CacheKey) (string, error) {
	switch by {
	case CacheByFileUniqueID:
		return "telegram_file_unique_id", nil
	case CacheByFileID:
		return "telegram_file_id", nil
	case CacheBySHA256:
		return "sha256", nil
	}
	return "", fmt.Errorf("unknown cache key %d", by)
}

// FindCache implements Store.FindCache. The lowest id wins, so the answer does
// not depend on the plan; Python took whichever row came first.
func (s *PGStore) FindCache(ctx context.Context, by CacheKey, value, kind string) (CacheEntry, bool, error) {
	column, err := cacheColumn(by)
	if err != nil {
		return CacheEntry{}, false, err
	}
	return s.oneCache(ctx, `SELECT id, description, sha256, phash FROM media_cache
		WHERE prompt_version = $1 AND media_kind = $2 AND `+column+` = $3 ORDER BY id LIMIT 1`,
		s.promptVersion, kind, value)
}

// CacheByID implements Store.CacheByID. A row of another prompt version is not
// returned, whoever asks.
func (s *PGStore) CacheByID(ctx context.Context, id int64) (CacheEntry, bool, error) {
	return s.oneCache(ctx, `SELECT id, description, sha256, phash FROM media_cache
		WHERE id = $1 AND prompt_version = $2`, id, s.promptVersion)
}

func (s *PGStore) oneCache(ctx context.Context, query string, args ...any) (CacheEntry, bool, error) {
	var (
		c     CacheEntry
		phash *string
	)
	err := s.pool.QueryRow(ctx, query, args...).Scan(&c.ID, &c.Description, &c.SHA256, &phash)
	if errors.Is(err, pgx.ErrNoRows) {
		return CacheEntry{}, false, nil
	}
	if err != nil {
		return CacheEntry{}, false, fmt.Errorf("read media cache: %w", err)
	}
	c.Phash = deref(phash)
	return c, true, nil
}

// PhashCandidates implements Store.PhashCandidates: ids and hashes, not the
// descriptions. The table is never pruned, so what a lookup reads must stay
// small however long it has grown.
func (s *PGStore) PhashCandidates(ctx context.Context, kind string) ([]PhashRow, error) {
	rows, err := s.pool.Query(ctx, `SELECT id, phash FROM media_cache
		WHERE prompt_version = $1 AND media_kind = $2 AND phash IS NOT NULL ORDER BY id`, s.promptVersion, kind)
	if err != nil {
		return nil, fmt.Errorf("list media phashes: %w", err)
	}
	defer rows.Close()
	var out []PhashRow
	for rows.Next() {
		var r PhashRow
		if err := rows.Scan(&r.ID, &r.Phash); err != nil {
			return nil, fmt.Errorf("list media phashes: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// CreateCache implements Store.CreateCache. Two files can normalize to the same
// bytes (the unique key is prompt version, kind and sha256): the second insert
// finds the first row and uses it.
func (s *PGStore) CreateCache(ctx context.Context, c NewCache) (CacheEntry, error) {
	phashAlgo := any(nil)
	if c.Phash != "" {
		phashAlgo = "dct64"
	}
	var id int64
	err := s.pool.QueryRow(ctx, `
		INSERT INTO media_cache (media_kind, telegram_file_unique_id, telegram_file_id, sha256, phash, phash_algo,
			description, model, prompt_version, usage_prompt_tokens, usage_completion_tokens, usage_total_tokens, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
		ON CONFLICT ON CONSTRAINT uq_media_cache_prompt_kind_sha256 DO NOTHING
		RETURNING id`,
		c.Kind, nullable(c.FileUniqueID), nullable(c.FileID), c.SHA256, nullable(c.Phash), phashAlgo,
		c.Description, c.Model, s.promptVersion,
		nullablePositive(c.PromptTokens), nullablePositive(c.CompletionTokens), nullablePositive(c.TotalTokens),
		s.now().UTC()).Scan(&id)
	if err == nil {
		return CacheEntry{ID: id, Description: c.Description, SHA256: c.SHA256, Phash: c.Phash}, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return CacheEntry{}, fmt.Errorf("insert media cache: %w", err)
	}
	existing, ok, err := s.oneCache(ctx, `SELECT id, description, sha256, phash FROM media_cache
		WHERE prompt_version = $1 AND media_kind = $2 AND sha256 = $3`, s.promptVersion, c.Kind, c.SHA256)
	if err != nil {
		return CacheEntry{}, err
	}
	if !ok {
		return CacheEntry{}, errors.New("insert media cache: conflict, but the row is gone")
	}
	return existing, nil
}

// Finish implements Store.Finish.
func (s *PGStore) Finish(ctx context.Context, item Item, r Result) error {
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		// The lock keeps an edit of the caption from slipping in between the
		// read and the write of the text.
		var caption *string
		err := tx.QueryRow(ctx, `SELECT caption FROM messages WHERE id = $1 FOR UPDATE`, item.MessageDBID).Scan(&caption)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil // the message is gone; so is its media row
		}
		if err != nil {
			return err
		}
		if r.Description != "" {
			text := domain.CanonicalText(item.Kind, deref(caption), r.Description)
			_, err = tx.Exec(ctx, `UPDATE messages SET text = $2, media_status = $3 WHERE id = $1`,
				item.MessageDBID, text, r.Status)
		} else {
			_, err = tx.Exec(ctx, `UPDATE messages SET media_status = $2 WHERE id = $1`, item.MessageDBID, r.Status)
		}
		if err != nil {
			return err
		}
		var cacheID, retries, next any
		if r.CacheID != 0 {
			cacheID = r.CacheID
		}
		if r.RetryCount != nil {
			retries = *r.RetryCount
		}
		if !r.NextRetryAt.IsZero() {
			next = r.NextRetryAt.UTC()
		}
		_, err = tx.Exec(ctx, `
			UPDATE message_media
			SET media_cache_id = $2, sha256 = $3, phash = $4, analysis_status = $5, analysis_error = $6,
			    retry_count = COALESCE($7::int, retry_count), next_retry_at = $8, description_snapshot = $9
			WHERE id = $1`,
			item.ID, cacheID, nullable(r.SHA256), nullable(r.Phash), r.Status, nullable(r.Error),
			retries, next, nullable(r.Description))
		return err
	})
	if err != nil {
		return fmt.Errorf("store media result: %w", err)
	}
	return nil
}
