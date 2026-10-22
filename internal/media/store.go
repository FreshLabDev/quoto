// SPDX-License-Identifier: Apache-2.0

package media

import (
	"context"
	"time"

	"github.com/FreshLabDev/quoto/internal/domain"
)

// Store is what the pipeline needs from the database. *PGStore implements it;
// the unit tests use a fake.
//
// Every method that compares with the clock takes "now" from the caller, so a
// test with a fake clock and a test against PostgreSQL see the same behaviour.
type Store interface {
	// Claim takes the lease on a message's media row: status processing and
	// next_retry_at = now + lease. It succeeds only for a row that is pending
	// or processing and whose next_retry_at is empty or past, so the handler
	// path and the recovery pass cannot analyse one file twice. itemID 0 means
	// the message's first row. It returns the row as it was claimed.
	Claim(ctx context.Context, messageDBID, itemID int64, now time.Time, lease time.Duration) (Item, bool, error)

	// Item is the first message_media row of a message, whatever its status.
	Item(ctx context.Context, messageDBID int64) (Item, bool, error)

	// Pending lists rows the recovery pass should take, at most limit:
	//   pending and (next_retry_at is empty and created_at <= now - minAge,
	//                or next_retry_at <= now)
	//   or processing and (next_retry_at is empty or <= now),
	// ordered by next_retry_at ascending (empty first), then created_at.
	Pending(ctx context.Context, now time.Time, minAge time.Duration, limit int) ([]Item, error)

	// Group reads the settings of the group a message belongs to. Only
	// MediaAnalysis, AgreementAcceptedAt and AgreementVersion are filled.
	// found is false when the message or its group row is gone.
	Group(ctx context.Context, messageDBID int64) (g domain.GroupSettings, found bool, err error)

	// FindCache returns the first cache row of the current prompt version and
	// the given analysis kind whose column named by by equals value.
	FindCache(ctx context.Context, by CacheKey, value, kind string) (CacheEntry, bool, error)
	// PhashCandidates is every cache row of the current prompt version and kind
	// with a phash, as (id, phash) only. The descriptions stay in the table.
	PhashCandidates(ctx context.Context, kind string) ([]PhashRow, error)
	// CacheByID reads one cache row, the one a phash comparison chose.
	CacheByID(ctx context.Context, id int64) (CacheEntry, bool, error)
	// CreateCache inserts a cache row. When one with the same (prompt version,
	// kind, sha256) exists already, it returns that row instead of failing.
	CreateCache(ctx context.Context, c NewCache) (CacheEntry, error)

	// Finish writes the outcome: messages.media_status, messages.text when the
	// result carries a description, and the message_media row. A message that
	// was deleted in the meantime is not an error.
	Finish(ctx context.Context, item Item, r Result) error
}

// CacheKey names the column a cache lookup compares.
type CacheKey int

const (
	CacheByFileUniqueID CacheKey = iota
	CacheByFileID
	CacheBySHA256
)

func (k CacheKey) String() string {
	switch k {
	case CacheByFileUniqueID:
		return "file_unique_id"
	case CacheByFileID:
		return "file_id"
	case CacheBySHA256:
		return "sha256"
	}
	return "unknown"
}

// Item is a message_media row, as far as the pipeline reads it.
type Item struct {
	ID           int64
	MessageDBID  int64
	Kind         string // the raw kind: photo, video, sticker, image, ...
	FileID       string
	FileUniqueID string
	MimeType     string
	FileName     string
	FileSize     int64
	Width        int
	Height       int
	Duration     float64
	Status       string
	RetryCount   int
}

// Source rebuilds the MediaSource of the row. SupportsAnalysis is not stored: it
// is recomputed from the kind and the MIME type, as the Python recovery pass did.
func (it Item) Source() domain.MediaSource {
	return domain.MediaSource{
		Kind:             it.Kind,
		FileID:           it.FileID,
		FileUniqueID:     it.FileUniqueID,
		MimeType:         it.MimeType,
		FileName:         it.FileName,
		FileSize:         it.FileSize,
		Width:            it.Width,
		Height:           it.Height,
		Duration:         it.Duration,
		SupportsAnalysis: supportsAnalysis(it.Kind, it.MimeType),
	}
}

// supportsAnalysis says whether any model can read the kind. A document that is
// not an image, and an animated (tgs) sticker, cannot.
func supportsAnalysis(kind, mime string) bool {
	switch kind {
	case domain.ContentPhoto, domain.ContentImage, domain.ContentVideo, domain.ContentAnimation,
		domain.ContentVideoNote, domain.ContentVoice, domain.ContentAudio:
		return true
	case domain.ContentSticker:
		return mime != "application/x-tgsticker"
	}
	return false
}

// CacheEntry is a media_cache row without the columns nobody reads back.
type CacheEntry struct {
	ID          int64
	Description string
	SHA256      string
	Phash       string
}

// PhashRow is the part of a cache row a perceptual comparison needs.
type PhashRow struct {
	ID    int64
	Phash string
}

// NewCache is a media_cache row to insert. The prompt version is the store's.
type NewCache struct {
	Kind             string // the analysis kind
	FileUniqueID     string
	FileID           string
	SHA256           string
	Phash            string
	Description      string
	Model            string
	PromptTokens     int
	CompletionTokens int
	TotalTokens      int
}

// Result is the outcome of one job, as Finish writes it.
type Result struct {
	Status string
	// Description goes to description_snapshot and, when not empty, into
	// messages.text. For every status but analyzed and cached it is the
	// metadata description.
	Description string
	Error       string // analysis_error, already cut to 500 runes
	CacheID     int64  // 0 is NULL
	SHA256      string
	Phash       string
	// RetryCount replaces retry_count when not nil.
	RetryCount *int
	// NextRetryAt is the zero time for NULL.
	NextRetryAt time.Time
}
