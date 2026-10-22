// SPDX-License-Identifier: Apache-2.0
//go:build integration

package media

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/FreshLabDev/quoto/internal/db"
	"github.com/FreshLabDev/quoto/internal/db/dbtest"
	"github.com/FreshLabDev/quoto/internal/domain"
)

// These tests run PGStore against PostgreSQL 17 with the real migrations; the
// SQL is the part the in-memory fake cannot vouch for.

var (
	pgAlice = domain.Person{ID: 11, Username: "alice", FirstName: "Alice", LanguageCode: "ru"}
	pgChat  = domain.ChatInfo{ID: -1001, Type: "supergroup", Title: "Club"}
	pgNoon  = time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	pgCtx   = context.Background()
)

type pgEnv struct {
	t     *testing.T
	s     *db.Store
	pg    *PGStore
	msgID int64
}

func newPGEnv(t *testing.T) *pgEnv {
	t.Helper()
	s := dbtest.New(t)
	if _, err := s.Ensure(pgCtx, pgAlice, pgChat); err != nil {
		t.Fatalf("ensure group: %v", err)
	}
	pg := NewPGStore(s.Pool())
	pg.now = func() time.Time { return pgNoon }
	return &pgEnv{t: t, s: s, pg: pg}
}

func (e *pgEnv) exec(sql string, args ...any) {
	e.t.Helper()
	dbtest.Exec(e.t, e.s, sql, args...)
}

// accept records the group's agreement, as the bot does when an admin agrees.
func (e *pgEnv) accept() {
	e.t.Helper()
	if _, err := e.s.AcceptAgreement(pgCtx, pgChat.ID, pgAlice.ID, "ru"); err != nil {
		e.t.Fatalf("accept agreement: %v", err)
	}
}

// message saves a message with a media row through the same call the bot uses,
// and returns the message id and the media row id.
func (e *pgEnv) message(src domain.MediaSource, caption string) (messageDBID, itemID int64) {
	e.t.Helper()
	e.msgID++
	id, inserted, err := e.s.Save(pgCtx, domain.NewMessage{
		MessageID: e.msgID, ChatID: pgChat.ID, UserID: pgAlice.ID, Text: domain.CanonicalText(src.Kind, caption, ""),
		ContentType: src.Kind, Caption: caption, CreatedAt: pgNoon.Add(-time.Hour), Media: &src,
	})
	if err != nil || !inserted {
		e.t.Fatalf("save: inserted=%t err=%v", inserted, err)
	}
	if err := e.s.Pool().QueryRow(pgCtx, `SELECT id FROM message_media WHERE message_db_id = $1 ORDER BY id LIMIT 1`, id).Scan(&itemID); err != nil {
		e.t.Fatal(err)
	}
	return id, itemID
}

func (e *pgEnv) photo(unique string) (int64, int64) {
	return e.message(domain.MediaSource{Kind: domain.ContentPhoto, FileID: "f-" + unique, FileUniqueID: "u-" + unique,
		MimeType: "image/jpeg", FileSize: 2048, Width: 200, Height: 100, SupportsAnalysis: true}, "")
}

// state puts a media row in a given state.
func (e *pgEnv) state(itemID int64, status string, next *time.Time) {
	e.t.Helper()
	e.exec(`UPDATE message_media SET analysis_status = $2, next_retry_at = $3 WHERE id = $1`, itemID, status, next)
}

type mediaRow struct {
	Status   string
	Err      *string
	Retries  int
	Next     *time.Time
	Snapshot *string
	CacheID  *int64
	SHA256   *string
	Phash    *string
}

func (e *pgEnv) media(itemID int64) mediaRow {
	e.t.Helper()
	var r mediaRow
	err := e.s.Pool().QueryRow(pgCtx, `SELECT analysis_status, analysis_error, retry_count, next_retry_at,
		description_snapshot, media_cache_id, sha256, phash FROM message_media WHERE id = $1`, itemID).
		Scan(&r.Status, &r.Err, &r.Retries, &r.Next, &r.Snapshot, &r.CacheID, &r.SHA256, &r.Phash)
	if err != nil {
		e.t.Fatalf("read media row %d: %v", itemID, err)
	}
	return r
}

func (e *pgEnv) messageRow(messageDBID int64) (text string, status *string) {
	e.t.Helper()
	if err := e.s.Pool().QueryRow(pgCtx, `SELECT text, media_status FROM messages WHERE id = $1`, messageDBID).Scan(&text, &status); err != nil {
		e.t.Fatalf("read message %d: %v", messageDBID, err)
	}
	return text, status
}

func at(offset time.Duration) *time.Time {
	t := pgNoon.Add(offset)
	return &t
}

func str(p *string) string {
	if p == nil {
		return "<NULL>"
	}
	return *p
}

// ---- Claim ------------------------------------------------------------------

// phase0/02-media.md 1.7: a row is taken when it is pending or processing and its
// retry time has come; taking it sets processing and a lease.
func TestPGClaimConditions(t *testing.T) {
	tests := []struct {
		name   string
		status string
		next   *time.Time
		want   bool
	}{
		{"pending, never tried", domain.MediaPending, nil, true},
		{"pending, retry due", domain.MediaPending, at(-time.Minute), true},
		{"pending, retry due this very second", domain.MediaPending, at(0), true},
		{"pending, retry in the future", domain.MediaPending, at(time.Second), false},
		{"processing without a lease time", domain.MediaProcessing, nil, true},
		{"processing, lease expired", domain.MediaProcessing, at(-time.Second), true},
		{"processing, lease alive", domain.MediaProcessing, at(time.Minute), false},
		{"analyzed", domain.MediaAnalyzed, nil, false},
		{"cached", domain.MediaCached, nil, false},
		{"disabled", domain.MediaDisabled, nil, false},
		{"unsupported", domain.MediaUnsupported, nil, false},
		{"failed", domain.MediaFailed, nil, false},
		{"failed with a retry time in the past", domain.MediaFailed, at(-time.Hour), false},
	}
	e := newPGEnv(t)
	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			msg, item := e.photo(fmt.Sprint("claim", i))
			e.state(item, tt.status, tt.next)
			before := e.media(item)

			got, ok, err := e.pg.Claim(pgCtx, msg, item, pgNoon, 600*time.Second)

			if err != nil {
				t.Fatal(err)
			}
			after := e.media(item)
			if ok != tt.want {
				t.Fatalf("claimed = %t, want %t", ok, tt.want)
			}
			if !tt.want {
				if after.Status != before.Status || !sameTime(after.Next, before.Next) {
					t.Fatalf("a refused claim changed the row: %+v -> %+v", before, after)
				}
				return
			}
			if after.Status != domain.MediaProcessing || after.Next == nil || !after.Next.Equal(pgNoon.Add(600*time.Second)) {
				t.Fatalf("after claim: status %q, lease until %v; want processing until %v", after.Status, after.Next, pgNoon.Add(600*time.Second))
			}
			if got.ID != item || got.MessageDBID != msg || got.Status != domain.MediaProcessing || got.FileUniqueID != fmt.Sprint("u-claim", i) {
				t.Fatalf("claimed item %+v", got)
			}
		})
	}
}

func sameTime(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Equal(*b)
}

// The row is named by both ids; a mismatch claims nothing, and 0 means "the
// message's first row".
func TestPGClaimNamesTheRow(t *testing.T) {
	e := newPGEnv(t)
	msg, first := e.photo("a")
	other, otherItem := e.photo("b")
	e.exec(`INSERT INTO message_media (message_db_id, media_kind, analysis_status, created_at) VALUES ($1, 'photo', 'pending', $2)`, msg, pgNoon)
	var second int64
	if err := e.s.Pool().QueryRow(pgCtx, `SELECT max(id) FROM message_media WHERE message_db_id = $1`, msg).Scan(&second); err != nil {
		t.Fatal(err)
	}

	if _, ok, err := e.pg.Claim(pgCtx, msg, otherItem, pgNoon, time.Minute); err != nil || ok {
		t.Fatalf("a row of another message was claimed: ok=%t err=%v", ok, err)
	}
	if _, ok, err := e.pg.Claim(pgCtx, other, first, pgNoon, time.Minute); err != nil || ok {
		t.Fatalf("a row was claimed through another message: ok=%t err=%v", ok, err)
	}
	got, ok, err := e.pg.Claim(pgCtx, msg, 0, pgNoon, time.Minute)
	if err != nil || !ok || got.ID != first {
		t.Fatalf("item 0 claimed %+v ok=%t err=%v, want the first row %d", got, ok, err, first)
	}
	if e.media(second).Status != domain.MediaPending {
		t.Fatal("claiming the first row touched the second")
	}
	got, ok, err = e.pg.Claim(pgCtx, msg, second, pgNoon, time.Minute)
	if err != nil || !ok || got.ID != second {
		t.Fatalf("second row claimed %+v ok=%t err=%v", got, ok, err)
	}
	// The first row now holds a lease; item 0 does not skip ahead to another row.
	if _, ok, _ := e.pg.Claim(pgCtx, msg, 0, pgNoon, time.Minute); ok {
		t.Fatal("item 0 claimed a row whose lease is alive")
	}
}

// Two workers racing for one row: exactly one gets it (the claim is one UPDATE).
func TestPGClaimIsExclusive(t *testing.T) {
	e := newPGEnv(t)
	msg, item := e.photo("race")

	const workers = 8
	var won atomic.Int32
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if _, ok, err := e.pg.Claim(pgCtx, msg, item, pgNoon, 600*time.Second); err != nil {
				t.Error(err)
			} else if ok {
				won.Add(1)
			}
		}()
	}
	close(start)
	wg.Wait()

	if won.Load() != 1 {
		t.Fatalf("%d workers claimed the row, want 1", won.Load())
	}
}

// ---- Item, Pending, Group --------------------------------------------------------

func TestPGItemScansEveryColumn(t *testing.T) {
	e := newPGEnv(t)
	full := domain.MediaSource{
		Kind: domain.ContentSticker, FileID: "file", FileUniqueID: "unique", MimeType: "image/webp", FileName: "s.webp",
		FileSize: 5_000_000_000, // does not fit 32 bits
		Width:    512, Height: 400, Duration: 3.5,
	}
	msg, item := e.message(full, "")
	e.exec(`UPDATE message_media SET retry_count = 3 WHERE id = $1`, item)

	got, ok, err := e.pg.Item(pgCtx, msg)

	want := Item{ID: item, MessageDBID: msg, Kind: "sticker", FileID: "file", FileUniqueID: "unique", MimeType: "image/webp",
		FileName: "s.webp", FileSize: 5_000_000_000, Width: 512, Height: 400, Duration: 3.5, Status: domain.MediaPending, RetryCount: 3}
	if err != nil || !ok || got != want {
		t.Fatalf("got %+v ok=%t err=%v\nwant %+v", got, ok, err, want)
	}

	// Empty fields are NULL in the table and zero values here.
	msg2, item2 := e.message(domain.MediaSource{Kind: domain.ContentVoice}, "")
	got, ok, err = e.pg.Item(pgCtx, msg2)
	if want := (Item{ID: item2, MessageDBID: msg2, Kind: "voice", Status: domain.MediaPending}); err != nil || !ok || got != want {
		t.Fatalf("bare row: got %+v ok=%t err=%v\nwant %+v", got, ok, err, want)
	}

	if _, ok, err := e.pg.Item(pgCtx, 987654); err != nil || ok {
		t.Fatalf("unknown message: ok=%t err=%v", ok, err)
	}
}

// phase0/02-media.md 1.7: the recovery pass takes pending rows that are due or
// have waited the minimum age, and processing rows whose lease has run out,
// oldest retry time first with never-tried rows ahead, at most limit of them.
func TestPGPendingSelectionAndOrder(t *testing.T) {
	e := newPGEnv(t)
	rows := []struct {
		name    string
		status  string
		next    *time.Time
		created time.Duration // before noon
		listed  bool
	}{
		{"new pending, old enough", domain.MediaPending, nil, 10 * time.Minute, true},
		{"new pending, too young", domain.MediaPending, nil, 4 * time.Minute, false},
		{"new pending, exactly the minimum age", domain.MediaPending, nil, 5 * time.Minute, true},
		{"pending retry due a minute ago", domain.MediaPending, at(-time.Minute), 3 * time.Hour, true},
		{"pending retry due half an hour ago", domain.MediaPending, at(-30 * time.Minute), 2 * time.Hour, true},
		{"pending retry in the future", domain.MediaPending, at(time.Minute), 3 * time.Hour, false},
		{"processing without a lease time", domain.MediaProcessing, nil, time.Hour, true},
		{"processing, lease just expired", domain.MediaProcessing, at(-time.Second), time.Hour, true},
		{"processing, lease alive", domain.MediaProcessing, at(10 * time.Minute), time.Hour, false},
		{"analyzed", domain.MediaAnalyzed, nil, 9 * time.Hour, false},
		{"failed with an old retry time", domain.MediaFailed, at(-time.Hour), 9 * time.Hour, false},
	}
	byMessage := map[int64]string{}
	for i, r := range rows {
		msg, item := e.photo(fmt.Sprint("pend", i))
		e.state(item, r.status, r.next)
		e.exec(`UPDATE message_media SET created_at = $2 WHERE id = $1`, item, pgNoon.Add(-r.created))
		byMessage[msg] = r.name
	}
	// Never-tried rows first, oldest first; then by retry time.
	wantOrder := []string{
		"processing without a lease time",
		"new pending, old enough",
		"new pending, exactly the minimum age",
		"pending retry due half an hour ago",
		"pending retry due a minute ago",
		"processing, lease just expired",
	}
	names := func(items []Item) []string {
		var out []string
		for _, it := range items {
			out = append(out, byMessage[it.MessageDBID])
		}
		return out
	}

	got, err := e.pg.Pending(pgCtx, pgNoon, 300*time.Second, 10)
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(names(got)) != fmt.Sprint(wantOrder) {
		t.Fatalf("listed\n  %q\nwant\n  %q", names(got), wantOrder)
	}

	for _, r := range rows {
		listed := false
		for _, n := range wantOrder {
			listed = listed || n == r.name
		}
		if listed != r.listed {
			t.Fatalf("table error in %q", r.name)
		}
	}

	limited, err := e.pg.Pending(pgCtx, pgNoon, 300*time.Second, 2)
	if err != nil || fmt.Sprint(names(limited)) != fmt.Sprint(wantOrder[:2]) {
		t.Fatalf("limit 2 gave %q err=%v, want %q", names(limited), err, wantOrder[:2])
	}
}

func TestPGGroup(t *testing.T) {
	e := newPGEnv(t)
	msg, _ := e.photo("g")

	fresh, ok, err := e.pg.Group(pgCtx, msg)
	if err != nil || !ok {
		t.Fatalf("fresh group: ok=%t err=%v", ok, err)
	}
	if fresh.MediaAnalysis != nil || fresh.AgreementAcceptedAt != nil || fresh.AgreementVersion != nil || fresh.AgreementAccepted() {
		t.Fatalf("a new group has no switch and no agreement: %+v", fresh)
	}
	if !fresh.MediaAnalysisEnabled() {
		t.Fatal("NULL means enabled")
	}

	e.exec(`UPDATE group_settings SET media_analysis_enabled = false WHERE chat_id = $1`, pgChat.ID)
	off, _, _ := e.pg.Group(pgCtx, msg)
	if off.MediaAnalysis == nil || *off.MediaAnalysis || off.MediaAnalysisEnabled() {
		t.Fatalf("switch off: %+v", off)
	}

	e.accept()
	accepted, _, _ := e.pg.Group(pgCtx, msg)
	if !accepted.AgreementAccepted() || str(accepted.AgreementVersion) != domain.DocumentVersion {
		t.Fatalf("accepted: %+v", accepted)
	}

	// An agreement of a version that no longer counts does not open the gate.
	e.exec(`UPDATE group_settings SET agreement_version = '1.0' WHERE chat_id = $1`, pgChat.ID)
	old, _, _ := e.pg.Group(pgCtx, msg)
	if old.AgreementAcceptedAt == nil || old.AgreementAccepted() {
		t.Fatalf("outdated agreement: %+v", old)
	}

	// A message of a chat that has no settings row, and a message that is gone.
	e.exec(`INSERT INTO messages (message_id, chat_id, user_id, text, content_type, created_at) VALUES (99, -777, $1, 'x', 'text', $2)`, pgAlice.ID, pgNoon)
	var orphan int64
	if err := e.s.Pool().QueryRow(pgCtx, `SELECT id FROM messages WHERE chat_id = -777`).Scan(&orphan); err != nil {
		t.Fatal(err)
	}
	for name, id := range map[string]int64{"chat without settings": orphan, "no such message": 424242} {
		if _, ok, err := e.pg.Group(pgCtx, id); err != nil || ok {
			t.Fatalf("%s: ok=%t err=%v", name, ok, err)
		}
	}
}

// ---- cache ------------------------------------------------------------------

func newCache(kind, unique, sha, phash string) NewCache {
	return NewCache{Kind: kind, FileUniqueID: unique, FileID: "file-" + unique, SHA256: sha, Phash: phash,
		Description: "about " + sha, Model: "model-x", PromptTokens: 11, CompletionTokens: 7, TotalTokens: 18}
}

// doc 6 / phase0/02-media.md 1.4: the lookup is inside one prompt version and one
// media kind; rows of older versions stay untouched and invisible (R5).
func TestPGCacheLookups(t *testing.T) {
	e := newPGEnv(t)
	cur, err := e.pg.CreateCache(pgCtx, newCache("photo", "uniq", "sha-cur", "00ff00ff00ff00ff"))
	if err != nil {
		t.Fatal(err)
	}
	// The same file under the previous prompt version, and another kind.
	e.exec(`INSERT INTO media_cache (media_kind, telegram_file_unique_id, telegram_file_id, sha256, phash, description, model, prompt_version, created_at)
		VALUES ('photo', 'uniq', 'file-uniq', 'sha-old', '00ff00ff00ff00ff', 'old description', 'm', 'v2', $1)`, pgNoon)
	if _, err := e.pg.CreateCache(pgCtx, newCache("video", "uniq", "sha-video", "")); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name  string
		by    CacheKey
		value string
		kind  string
		want  string // description, or "" for no row
	}{
		{"by unique id", CacheByFileUniqueID, "uniq", "photo", "about sha-cur"},
		{"by file id", CacheByFileID, "file-uniq", "photo", "about sha-cur"},
		{"by sha256", CacheBySHA256, "sha-cur", "photo", "about sha-cur"},
		{"the old version is not found by sha256", CacheBySHA256, "sha-old", "photo", ""},
		{"the other kind has its own row", CacheByFileUniqueID, "uniq", "video", "about sha-video"},
		{"unknown unique id", CacheByFileUniqueID, "nope", "photo", ""},
		{"a kind with no rows", CacheByFileUniqueID, "uniq", "voice", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok, err := e.pg.FindCache(pgCtx, tt.by, tt.value, tt.kind)
			if err != nil {
				t.Fatal(err)
			}
			if ok != (tt.want != "") || got.Description != tt.want {
				t.Fatalf("got %+v ok=%t, want description %q", got, ok, tt.want)
			}
		})
	}

	got, ok, err := e.pg.FindCache(pgCtx, CacheByFileUniqueID, "uniq", "photo")
	if err != nil || !ok || got.ID != cur.ID || got.SHA256 != "sha-cur" || got.Phash != "00ff00ff00ff00ff" {
		t.Fatalf("entry %+v ok=%t err=%v, want id %d", got, ok, err, cur.ID)
	}
	if _, _, err := e.pg.FindCache(pgCtx, CacheKey(99), "x", "photo"); err == nil {
		t.Fatal("an unknown key was accepted")
	}
}

// Python took whichever row the planner returned first; the lowest id is now the
// answer, so a lookup does not change with the plan.
func TestPGFindCachePrefersTheOldestRow(t *testing.T) {
	e := newPGEnv(t)
	first, err := e.pg.CreateCache(pgCtx, newCache("photo", "same-file", "sha-1", ""))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.pg.CreateCache(pgCtx, newCache("photo", "same-file", "sha-2", "")); err != nil {
		t.Fatal(err)
	}

	got, ok, err := e.pg.FindCache(pgCtx, CacheByFileUniqueID, "same-file", "photo")

	if err != nil || !ok || got.ID != first.ID {
		t.Fatalf("got %+v ok=%t err=%v, want row %d", got, ok, err, first.ID)
	}
}

// doc section 2: the hash lookup reads ids and hashes only, of the current
// version and the kind, rows without a hash left out.
func TestPGPhashCandidates(t *testing.T) {
	e := newPGEnv(t)
	a, _ := e.pg.CreateCache(pgCtx, newCache("photo", "a", "sha-a", "aaaaaaaaaaaaaaaa"))
	_, _ = e.pg.CreateCache(pgCtx, newCache("photo", "n", "sha-n", "")) // no hash
	_, _ = e.pg.CreateCache(pgCtx, newCache("video", "v", "sha-v", "bbbbbbbbbbbbbbbb:cccccccccccccccc"))
	c, _ := e.pg.CreateCache(pgCtx, newCache("photo", "c", "sha-c", "cccccccccccccccc"))
	e.exec(`INSERT INTO media_cache (media_kind, sha256, phash, description, model, prompt_version, created_at)
		VALUES ('photo', 'sha-old', 'dddddddddddddddd', 'old', 'm', 'v2', $1)`, pgNoon)

	got, err := e.pg.PhashCandidates(pgCtx, "photo")

	want := []PhashRow{{ID: a.ID, Phash: "aaaaaaaaaaaaaaaa"}, {ID: c.ID, Phash: "cccccccccccccccc"}}
	if err != nil || fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("got %v err=%v, want %v", got, err, want)
	}
	if got, err := e.pg.PhashCandidates(pgCtx, "voice"); err != nil || len(got) != 0 {
		t.Fatalf("a kind without rows: %v err=%v", got, err)
	}
	video, err := e.pg.PhashCandidates(pgCtx, "video")
	if err != nil || len(video) != 1 || video[0].Phash != "bbbbbbbbbbbbbbbb:cccccccccccccccc" {
		t.Fatalf("video candidates %v err=%v", video, err)
	}
}

func TestPGCacheByID(t *testing.T) {
	e := newPGEnv(t)
	cur, _ := e.pg.CreateCache(pgCtx, newCache("photo", "a", "sha-a", "aaaaaaaaaaaaaaaa"))
	e.exec(`INSERT INTO media_cache (media_kind, sha256, description, model, prompt_version, created_at)
		VALUES ('photo', 'sha-old', 'old', 'm', 'v2', $1)`, pgNoon)
	var oldID int64
	if err := e.s.Pool().QueryRow(pgCtx, `SELECT id FROM media_cache WHERE prompt_version = 'v2'`).Scan(&oldID); err != nil {
		t.Fatal(err)
	}

	got, ok, err := e.pg.CacheByID(pgCtx, cur.ID)
	if err != nil || !ok || got.Description != "about sha-a" || got.Phash != "aaaaaaaaaaaaaaaa" {
		t.Fatalf("current row: %+v ok=%t err=%v", got, ok, err)
	}
	if _, ok, err := e.pg.CacheByID(pgCtx, oldID); err != nil || ok {
		t.Fatalf("a row of the old prompt version was returned: ok=%t err=%v", ok, err)
	}
	if _, ok, err := e.pg.CacheByID(pgCtx, 31337); err != nil || ok {
		t.Fatalf("unknown id: ok=%t err=%v", ok, err)
	}
}

// phase0/02-media.md 1.6: the unique key is (prompt version, kind, sha256); a
// second insert of the same bytes returns the first row, not an error.
func TestPGCreateCache(t *testing.T) {
	e := newPGEnv(t)

	created, err := e.pg.CreateCache(pgCtx, newCache("photo", "u1", "sha-1", "1111111111111111"))
	if err != nil || created.ID == 0 || created.Description != "about sha-1" {
		t.Fatalf("create: %+v err=%v", created, err)
	}

	// Stored columns.
	var (
		kind, unique, fileID, sha, model, version, desc string
		phash, algo                                     *string
		prompt, completion, total                       *int32
		createdAt                                       time.Time
	)
	err = e.s.Pool().QueryRow(pgCtx, `SELECT media_kind, telegram_file_unique_id, telegram_file_id, sha256, phash, phash_algo,
		description, model, prompt_version, usage_prompt_tokens, usage_completion_tokens, usage_total_tokens, created_at
		FROM media_cache WHERE id = $1`, created.ID).
		Scan(&kind, &unique, &fileID, &sha, &phash, &algo, &desc, &model, &version, &prompt, &completion, &total, &createdAt)
	if err != nil {
		t.Fatal(err)
	}
	if kind != "photo" || unique != "u1" || fileID != "file-u1" || sha != "sha-1" || str(phash) != "1111111111111111" ||
		str(algo) != "dct64" || desc != "about sha-1" || model != "model-x" || version != domain.MediaPromptVersion ||
		*prompt != 11 || *completion != 7 || *total != 18 || !createdAt.Equal(pgNoon) {
		t.Fatalf("stored row: %s %s %s %s phash=%s algo=%s %q %s %s tokens %v/%v/%v at %v",
			kind, unique, fileID, sha, str(phash), str(algo), desc, model, version, prompt, completion, total, createdAt)
	}

	// Same bytes again: the first row comes back; nothing new is stored.
	again := newCache("photo", "u2", "sha-1", "2222222222222222")
	again.Description = "a different answer"
	existing, err := e.pg.CreateCache(pgCtx, again)
	if err != nil || existing.ID != created.ID || existing.Description != "about sha-1" || existing.Phash != "1111111111111111" {
		t.Fatalf("conflict: got %+v err=%v, want the first row %+v", existing, err, created)
	}
	var n int
	_ = e.s.Pool().QueryRow(pgCtx, `SELECT count(*) FROM media_cache`).Scan(&n)
	if n != 1 {
		t.Fatalf("%d cache rows, want 1", n)
	}

	// The same bytes under another kind, and under the old version, are other rows.
	other, err := e.pg.CreateCache(pgCtx, newCache("video", "u3", "sha-1", ""))
	if err != nil || other.ID == created.ID {
		t.Fatalf("other kind: %+v err=%v", other, err)
	}
	e.exec(`INSERT INTO media_cache (media_kind, sha256, description, model, prompt_version, created_at)
		VALUES ('photo', 'sha-9', 'old', 'm', 'v2', $1)`, pgNoon)
	next, err := e.pg.CreateCache(pgCtx, newCache("photo", "u4", "sha-9", ""))
	if err != nil || next.Description != "about sha-9" {
		t.Fatalf("the old version blocked the insert: %+v err=%v", next, err)
	}

	// No hash, no id and no token counts are NULL, not empty strings and zeros.
	bare, err := e.pg.CreateCache(pgCtx, NewCache{Kind: "voice", SHA256: "sha-bare", Description: "d", Model: "m"})
	if err != nil {
		t.Fatal(err)
	}
	err = e.s.Pool().QueryRow(pgCtx, `SELECT telegram_file_unique_id, telegram_file_id, phash, phash_algo,
		usage_prompt_tokens, usage_completion_tokens, usage_total_tokens FROM media_cache WHERE id = $1`, bare.ID).
		Scan(new(*string), new(*string), &phash, &algo, &prompt, &completion, &total)
	if err != nil {
		t.Fatal(err)
	}
	if phash != nil || algo != nil || prompt != nil || completion != nil || total != nil {
		t.Fatalf("bare row has values: %v %v %v %v %v", phash, algo, prompt, completion, total)
	}
}

// ---- Finish -----------------------------------------------------------------

// phase0/02-media.md 1.8: the result lands in the media row and, with a
// description, in the message text, in one transaction.
func TestPGFinish(t *testing.T) {
	retries := func(n int) *int { return &n }
	tests := []struct {
		name       string
		caption    string
		start      string // status before; the row starts with retry_count 2
		res        func(cacheID int64) Result
		wantText   string // "" = unchanged
		wantStatus string
		check      func(t *testing.T, r mediaRow, cacheID int64)
	}{
		{
			name: "analyzed", start: domain.MediaProcessing,
			res: func(c int64) Result {
				return Result{Status: domain.MediaAnalyzed, Description: "a cat", CacheID: c, SHA256: "sha", Phash: "ab"}
			},
			wantText: "photo: a cat", wantStatus: domain.MediaAnalyzed,
			check: func(t *testing.T, r mediaRow, c int64) {
				if r.CacheID == nil || *r.CacheID != c || str(r.SHA256) != "sha" || str(r.Phash) != "ab" || str(r.Snapshot) != "a cat" ||
					r.Err != nil || r.Next != nil || r.Retries != 2 {
					t.Fatalf("row %+v", r)
				}
			},
		},
		{
			name: "analyzed with a caption", caption: "look at this", start: domain.MediaProcessing,
			res: func(c int64) Result {
				return Result{Status: domain.MediaCached, Description: "a cat", CacheID: c}
			},
			wantText: "look at this\nphoto: a cat", wantStatus: domain.MediaCached,
			check: func(t *testing.T, r mediaRow, _ int64) {
				if r.SHA256 != nil || r.Phash != nil {
					t.Fatalf("empty hashes must be NULL: %+v", r)
				}
			},
		},
		{
			name: "retry later", start: domain.MediaProcessing,
			res: func(int64) Result {
				return Result{Status: domain.MediaPending, Error: "rate limited", RetryCount: retries(3), NextRetryAt: pgNoon.Add(600 * time.Second)}
			},
			wantStatus: domain.MediaPending,
			check: func(t *testing.T, r mediaRow, _ int64) {
				if r.Retries != 3 || r.Next == nil || !r.Next.Equal(pgNoon.Add(600*time.Second)) || str(r.Err) != "rate limited" || r.Snapshot != nil {
					t.Fatalf("row %+v", r)
				}
			},
		},
		{
			name: "retry that does not count as an attempt", start: domain.MediaProcessing,
			res: func(int64) Result {
				return Result{Status: domain.MediaPending, Error: "cooldown", NextRetryAt: pgNoon.Add(time.Minute)}
			},
			wantStatus: domain.MediaPending,
			check: func(t *testing.T, r mediaRow, _ int64) {
				if r.Retries != 2 {
					t.Fatalf("retry_count %d, want it left at 2", r.Retries)
				}
			},
		},
		{
			name: "retry count reset to zero", start: domain.MediaProcessing,
			res:        func(int64) Result { return Result{Status: domain.MediaFailed, Error: "boom", RetryCount: retries(0)} },
			wantStatus: domain.MediaFailed,
			check: func(t *testing.T, r mediaRow, _ int64) {
				if r.Retries != 0 {
					t.Fatalf("retry_count %d", r.Retries)
				}
			},
		},
		{
			name: "failed with the metadata description", caption: "cap", start: domain.MediaProcessing,
			res: func(int64) Result {
				return Result{Status: domain.MediaFailed, Description: "Файл: photo.jpg", Error: "download failed", RetryCount: retries(5)}
			},
			wantText: "cap\nphoto: Файл: photo.jpg", wantStatus: domain.MediaFailed,
			check: func(t *testing.T, r mediaRow, _ int64) {
				if str(r.Snapshot) != "Файл: photo.jpg" || str(r.Err) != "download failed" || r.Retries != 5 || r.Next != nil {
					t.Fatalf("row %+v", r)
				}
			},
		},
		{
			name: "a retry clears the old error and time", start: domain.MediaProcessing,
			res:        func(int64) Result { return Result{Status: domain.MediaDisabled, Description: "d"} },
			wantText:   "photo: d",
			wantStatus: domain.MediaDisabled,
			check: func(t *testing.T, r mediaRow, _ int64) {
				if r.Err != nil || r.Next != nil {
					t.Fatalf("row %+v", r)
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newPGEnv(t)
			msg, item := e.message(domain.MediaSource{Kind: domain.ContentPhoto, FileUniqueID: "u", SupportsAnalysis: true}, tt.caption)
			e.exec(`UPDATE message_media SET analysis_status = $2, retry_count = 2, analysis_error = 'old', next_retry_at = $3 WHERE id = $1`,
				item, tt.start, at(time.Hour))
			cache, err := e.pg.CreateCache(pgCtx, newCache("photo", "u", "sha", ""))
			if err != nil {
				t.Fatal(err)
			}
			beforeText, _ := e.messageRow(msg)

			err = e.pg.Finish(pgCtx, Item{ID: item, MessageDBID: msg, Kind: "photo"}, tt.res(cache.ID))

			if err != nil {
				t.Fatal(err)
			}
			text, status := e.messageRow(msg)
			wantText := tt.wantText
			if wantText == "" {
				wantText = beforeText
			}
			if text != wantText || str(status) != tt.wantStatus {
				t.Fatalf("message: text %q status %s, want %q %s", text, str(status), wantText, tt.wantStatus)
			}
			row := e.media(item)
			if row.Status != tt.wantStatus {
				t.Fatalf("media status %q", row.Status)
			}
			tt.check(t, row, cache.ID)
		})
	}
}

// A message deleted while its file was analysed (retention, a purge) is not an
// error: there is nothing left to write to.
func TestPGFinishAfterTheMessageIsGone(t *testing.T) {
	e := newPGEnv(t)
	msg, item := e.photo("gone")
	e.exec(`DELETE FROM messages WHERE id = $1`, msg)

	err := e.pg.Finish(pgCtx, Item{ID: item, MessageDBID: msg, Kind: "photo"}, Result{Status: domain.MediaAnalyzed, Description: "x"})

	if err != nil {
		t.Fatalf("finish after delete: %v", err)
	}
	var n int
	_ = e.s.Pool().QueryRow(pgCtx, `SELECT count(*) FROM message_media`).Scan(&n)
	if n != 0 {
		t.Fatalf("%d media rows left", n)
	}
}

// Finish writes the text and the status together: if the media update fails,
// the message keeps its old text.
func TestPGFinishIsOneTransaction(t *testing.T) {
	e := newPGEnv(t)
	msg, item := e.photo("tx")
	before, _ := e.messageRow(msg)
	// A cache id that does not exist breaks the foreign key on message_media.
	err := e.pg.Finish(pgCtx, Item{ID: item, MessageDBID: msg, Kind: "photo"},
		Result{Status: domain.MediaAnalyzed, Description: "x", CacheID: 123456})

	if err == nil || !strings.Contains(err.Error(), "store media result") {
		t.Fatalf("error %v", err)
	}
	if text, status := e.messageRow(msg); text != before || str(status) != domain.MediaPending {
		t.Fatalf("message changed: %q %s", text, str(status))
	}
	if row := e.media(item); row.Status != domain.MediaPending {
		t.Fatalf("media row changed: %+v", row)
	}
}

// ---- the whole pipeline over PostgreSQL ---------------------------------------------

func (e *pgEnv) pipeline() (*Pipeline, *fakeFiles, *fakeDescriber, *fakeClock) {
	files := &fakeFiles{payload: map[string][]byte{}}
	desc := &fakeDescriber{}
	clock := &fakeClock{t: pgNoon}
	p := New(Config{AnalysisEnabled: true, TempDir: e.t.TempDir()}, Deps{Store: e.pg, Files: files, Describer: desc})
	p.now = clock.Now
	p.run = &fakeRunner{}
	return p, files, desc, clock
}

func TestPGPipelineAnalyzesAndCaches(t *testing.T) {
	e := newPGEnv(t)
	e.accept()
	p, files, desc, _ := e.pipeline()
	first, firstItem := e.photo("same")
	second, secondItem := e.photo("same")
	files.payload["f-same"] = jpegBytes(200, 100)

	p.process(pgCtx, job{messageDBID: first})
	p.process(pgCtx, job{messageDBID: second})

	one, two := e.media(firstItem), e.media(secondItem)
	if one.Status != domain.MediaAnalyzed || two.Status != domain.MediaCached {
		t.Fatalf("statuses %q, %q", one.Status, two.Status)
	}
	if one.CacheID == nil || two.CacheID == nil || *one.CacheID != *two.CacheID {
		t.Fatalf("cache ids %v, %v", one.CacheID, two.CacheID)
	}
	if desc.count() != 1 || len(files.dlCalls) != 1 {
		t.Fatalf("%d model calls and %d downloads for one file", desc.count(), len(files.dlCalls))
	}
	for _, id := range []int64{first, second} {
		if text, status := e.messageRow(id); text != "photo: a description" || (str(status) != "analyzed" && str(status) != "cached") {
			t.Fatalf("message %d: %q %s", id, text, str(status))
		}
	}
	var rows int
	_ = e.s.Pool().QueryRow(pgCtx, `SELECT count(*) FROM media_cache WHERE prompt_version = $1 AND phash_algo = 'dct64' AND phash IS NOT NULL`, domain.MediaPromptVersion).Scan(&rows)
	if rows != 1 {
		t.Fatalf("%d cache rows with a hash, want 1", rows)
	}
}

// doc 7.4 R4: no agreement, nothing leaves the server.
func TestPGPipelineWithoutAgreementSendsNothing(t *testing.T) {
	e := newPGEnv(t)
	p, files, desc, _ := e.pipeline()
	msg, item := e.photo("noagreement")
	files.payload["f-noagreement"] = jpegBytes(200, 100)

	p.process(pgCtx, job{messageDBID: msg})

	row := e.media(item)
	if row.Status != domain.MediaDisabled || row.Snapshot == nil || !strings.HasPrefix(*row.Snapshot, "Файл:") {
		t.Fatalf("row %+v", row)
	}
	if desc.count() != 0 || len(files.dlCalls) != 0 {
		t.Fatalf("%d model calls, %d downloads", desc.count(), len(files.dlCalls))
	}
	if text, status := e.messageRow(msg); !strings.HasPrefix(text, "photo: Файл:") || str(status) != "disabled" {
		t.Fatalf("message %q %s", text, str(status))
	}
}

// A retryable failure goes back to pending with the next time written, the
// recovery pass lists the row once that time has come, and the second try ends it.
func TestPGPipelineRetryThroughRecovery(t *testing.T) {
	e := newPGEnv(t)
	e.accept()
	p, files, desc, clock := e.pipeline()
	msg, item := e.photo("retry")
	files.payload["f-retry"] = jpegBytes(200, 100)
	var calls atomic.Int32
	desc.fn = func(context.Context, domain.MediaDescribeRequest) (domain.MediaDescription, error) {
		if calls.Add(1) == 1 {
			return domain.MediaDescription{}, &domain.MediaRetryableError{Err: errors.New("provider overloaded"), CountsAsAttempt: true}
		}
		return domain.MediaDescription{Text: "second time lucky", ActualModel: "m"}, nil
	}

	p.process(pgCtx, job{messageDBID: msg})

	row := e.media(item)
	wantNext := pgNoon.Add(300 * time.Second)
	if row.Status != domain.MediaPending || row.Retries != 1 || row.Next == nil || !row.Next.Equal(wantNext) || !strings.Contains(str(row.Err), "provider overloaded") {
		t.Fatalf("after the first try: %+v", row)
	}
	// Python stores the metadata description while a retry waits, so a message
	// that is read in the meantime has something to say about its file.
	meta := domain.MetadataDescription(domain.MediaSource{Kind: domain.ContentPhoto, MimeType: "image/jpeg", FileSize: 2048})
	if text, status := e.messageRow(msg); text != "photo: "+meta || str(status) != domain.MediaPending || str(row.Snapshot) != meta {
		t.Fatalf("message %q %s, snapshot %s; want %q", text, str(status), str(row.Snapshot), meta)
	}

	// Too early: the retry time has not come.
	clock.Advance(299 * time.Second)
	if items, err := e.pg.Pending(pgCtx, clock.Now(), 300*time.Second, 10); err != nil || len(items) != 0 {
		t.Fatalf("listed before the retry time: %v err=%v", items, err)
	}
	clock.Advance(time.Second)
	items, err := e.pg.Pending(pgCtx, clock.Now(), 300*time.Second, 10)
	if err != nil || len(items) != 1 || items[0].ID != item {
		t.Fatalf("recovery list %v err=%v", items, err)
	}

	p.process(pgCtx, job{messageDBID: items[0].MessageDBID, itemID: items[0].ID})

	row = e.media(item)
	if row.Status != domain.MediaAnalyzed || row.Retries != 1 || row.Next != nil || row.Err != nil {
		t.Fatalf("after the second try: %+v", row)
	}
	if text, _ := e.messageRow(msg); text != "photo: second time lucky" {
		t.Fatalf("message text %q", text)
	}
}

// Submit, the workers and the store together.
func TestPGPipelineRun(t *testing.T) {
	e := newPGEnv(t)
	e.accept()
	p, files, _, _ := e.pipeline()
	msg, item := e.photo("run")
	files.payload["f-run"] = jpegBytes(200, 100)
	ctx, cancel := context.WithCancel(pgCtx)
	done := make(chan error, 1)
	go func() { done <- p.Run(ctx) }()
	defer func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("Run: %v", err)
		}
	}()

	p.Submit(msg)

	waitFor(t, "the row to be analysed", func() bool { return e.media(item).Status == domain.MediaAnalyzed })
}
