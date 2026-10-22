// SPDX-License-Identifier: Apache-2.0

package media

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/FreshLabDev/tg"

	"github.com/FreshLabDev/quoto/internal/domain"
)

// metaOf is the metadata description of a row, which every unsuccessful status
// stores as description_snapshot.
func metaOf(it Item) string { return domain.MetadataDescription(it.Source()) }

func ptr[T any](v T) *T { return &v }

// doc §6 "Media cache" and tests/test_media.py::test_process_media_source_skips_external_ai_when_group_disabled
// (MED-08): a group that switched analysis off gets "disabled" and no model call.
func TestGroupSwitchOffDisables(t *testing.T) {
	r := newRig(t)
	it := r.photo(55, "a")
	r.store.group.MediaAnalysis = ptr(false)

	r.do(55)

	res := r.store.result(t, 55)
	if res.Status != domain.MediaDisabled || res.Description != metaOf(it) {
		t.Fatalf("result %+v", res)
	}
	if r.desc.count() != 0 || len(r.files.getCalls) != 0 {
		t.Fatalf("a disabled group must not reach Telegram or a model: describe=%d get=%v", r.desc.count(), r.files.getCalls)
	}
}

// doc §7.0 R4: a group without the accepted agreement sends nothing to any
// model, whatever the reason it does not count.
func TestAgreementGate(t *testing.T) {
	tests := []struct {
		name  string
		group func(g *domain.GroupSettings)
		open  bool
	}{
		{"accepted 1.2", func(g *domain.GroupSettings) {}, true},
		{"accepted 1.10 counts", func(g *domain.GroupSettings) { g.AgreementVersion = ptr("1.10") }, true},
		{"never accepted", func(g *domain.GroupSettings) { g.AgreementAcceptedAt = nil; g.AgreementVersion = nil }, false},
		{"accepted_at set, version NULL", func(g *domain.GroupSettings) { g.AgreementVersion = nil }, false},
		{"older version 1.1", func(g *domain.GroupSettings) { g.AgreementVersion = ptr("1.1") }, false},
		{"version without acceptance time", func(g *domain.GroupSettings) { g.AgreementAcceptedAt = nil }, false},
		{"garbage version", func(g *domain.GroupSettings) { g.AgreementVersion = ptr("latest") }, false},
		{"switch NULL means on", func(g *domain.GroupSettings) { g.MediaAnalysis = nil }, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newRig(t)
			it := r.photo(1, "a")
			tt.group(&r.store.group)

			r.do(1)

			res := r.store.result(t, 1)
			if tt.open {
				if res.Status != domain.MediaAnalyzed {
					t.Fatalf("status %q, want analyzed", res.Status)
				}
				return
			}
			if res.Status != domain.MediaDisabled || res.Description != metaOf(it) {
				t.Fatalf("result %+v", res)
			}
			if r.desc.count() != 0 || len(r.files.getCalls) != 0 {
				t.Fatal("nothing may leave the bot for a group that has not agreed")
			}
		})
	}
}

// doc §7.0 R4: "checked before the download and again before the model".
func TestAgreementWithdrawnWhileDownloading(t *testing.T) {
	r := newRig(t)
	it := r.photo(1, "a")
	r.store.groupFn = func(call int) (domain.GroupSettings, bool, error) {
		g := r.store.group
		if call >= 2 { // the second look, just before the model
			g.AgreementAcceptedAt = nil
		}
		return g, true, nil
	}

	r.do(1)

	res := r.store.result(t, 1)
	if res.Status != domain.MediaDisabled || res.Description != metaOf(it) {
		t.Fatalf("result %+v", res)
	}
	if len(r.files.dlCalls) != 1 {
		t.Fatalf("the file was downloaded before the second look: %v", r.files.dlCalls)
	}
	if r.desc.count() != 0 {
		t.Fatal("the model was called after the gate closed")
	}
}

// tests/test_media.py (Python: _group_media_analysis_enabled returns False on a
// database error): an unreadable setting closes the gate.
func TestGroupReadErrorClosesGate(t *testing.T) {
	r := newRig(t)
	r.photo(1, "a")
	r.store.groupFn = func(int) (domain.GroupSettings, bool, error) {
		return domain.GroupSettings{}, false, errors.New("connection reset")
	}
	r.do(1)
	if res := r.store.result(t, 1); res.Status != domain.MediaDisabled {
		t.Fatalf("status %q, want disabled", res.Status)
	}
	if r.desc.count() != 0 {
		t.Fatal("model called")
	}
}

func TestGroupRowMissingClosesGate(t *testing.T) {
	r := newRig(t)
	r.photo(1, "a")
	r.store.groupFn = func(int) (domain.GroupSettings, bool, error) { return domain.GroupSettings{}, false, nil }
	r.do(1)
	if res := r.store.result(t, 1); res.Status != domain.MediaDisabled {
		t.Fatalf("status %q, want disabled", res.Status)
	}
}

// phase0/02-media.md 1.3 step 2: the global switch and files no model can read
// end as "unsupported", with the metadata description.
func TestUnsupported(t *testing.T) {
	tests := []struct {
		name   string
		global bool
		item   Item
	}{
		{"global switch off", false, Item{Kind: domain.ContentPhoto, FileID: "f", FileUniqueID: "u", MimeType: "image/jpeg"}},
		// tests/test_media.py::test_source_from_tgs_sticker_item_is_unsupported (MED-03)
		{"animated sticker", true, Item{Kind: domain.ContentSticker, FileID: "f", FileUniqueID: "u", MimeType: "application/x-tgsticker", FileSize: 123}},
		{"pdf document", true, Item{Kind: domain.ContentDocument, FileID: "f", FileUniqueID: "u", MimeType: "application/pdf", FileName: "report.pdf", FileSize: 2048}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newRig(t)
			r.p.cfg.AnalysisEnabled = tt.global
			it := r.store.add(9, tt.item)

			r.do(9)

			res := r.store.result(t, 9)
			if res.Status != domain.MediaUnsupported || res.Description != metaOf(it) {
				t.Fatalf("result %+v", res)
			}
			if len(r.files.getCalls) != 0 || r.desc.count() != 0 {
				t.Fatal("nothing should be fetched or sent")
			}
		})
	}
}

// phase0/02-media.md 1.3 step 3: a known file id is answered from the cache
// without a download.
func TestCachedByIDSkipsDownload(t *testing.T) {
	r := newRig(t)
	r.photo(1, "a")
	r.store.cache = []cacheRow{{
		CacheEntry:   CacheEntry{ID: 7, Description: "old words", SHA256: strings.Repeat("a", 64), Phash: "00ff00ff00ff00ff"},
		Kind:         domain.ContentPhoto,
		FileUniqueID: "u-a",
	}}

	r.do(1)

	res := r.store.result(t, 1)
	want := Result{Status: domain.MediaCached, Description: "old words", CacheID: 7,
		SHA256: strings.Repeat("a", 64), Phash: "00ff00ff00ff00ff"}
	if res != want {
		t.Fatalf("result %+v, want %+v", res, want)
	}
	if len(r.files.getCalls) != 0 || r.desc.count() != 0 {
		t.Fatal("a cache hit must not download or call a model")
	}
}

// phase0/02-media.md 1.3 step 4: the same picture under a new file id is found
// by the hash of the normalized file or by its look-alike, after the download
// and before the model.
func TestCachedAfterNormalizing(t *testing.T) {
	// The cache row whose hashes equal those of the normalized picture.
	probe := newRig(t)
	probe.photo(1, "probe")
	norm, err := probe.p.normalizeImage(context.Background(), writeTemp(t, "probe.jpg", jpegBytes(200, 100)), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name string
		row  cacheRow
	}{
		{"same sha256", cacheRow{CacheEntry: CacheEntry{ID: 3, Description: "d", SHA256: norm.SHA256, Phash: "ffffffffffffffff"}, Kind: domain.ContentPhoto}},
		{"look-alike phash", cacheRow{CacheEntry: CacheEntry{ID: 4, Description: "d", SHA256: strings.Repeat("c", 64), Phash: norm.Phash}, Kind: domain.ContentPhoto}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newRig(t)
			r.photo(1, "new")
			r.store.cache = []cacheRow{tt.row}

			r.do(1)

			res := r.store.result(t, 1)
			if res.Status != domain.MediaCached || res.CacheID != tt.row.ID {
				t.Fatalf("result %+v", res)
			}
			if res.SHA256 != norm.SHA256 || res.Phash != norm.Phash {
				t.Fatalf("a cached row takes the hashes of the file just normalized: %+v", res)
			}
			if len(r.files.dlCalls) != 1 || r.desc.count() != 0 {
				t.Fatalf("downloads %v, model calls %d", r.files.dlCalls, r.desc.count())
			}
		})
	}
}

func writeTemp(t testing.TB, name string, data []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// tests/test_media.py::test_store_media_result_updates_existing_pending_item
// (MED-12), the pipeline half: a new file is described once and the outcome
// carries the description, the cache row and the hashes.
func TestAnalyzed(t *testing.T) {
	r := newRig(t)
	r.photo(1, "a")

	r.do(1)

	res := r.store.result(t, 1)
	if res.Status != domain.MediaAnalyzed || res.Description != "a description" || res.CacheID == 0 {
		t.Fatalf("result %+v", res)
	}
	if len(res.SHA256) != 64 || len(res.Phash) != 16 || res.Error != "" || res.RetryCount != nil || !res.NextRetryAt.IsZero() {
		t.Fatalf("result %+v", res)
	}
	if len(r.desc.reqs) != 1 {
		t.Fatalf("model calls %d", r.desc.count())
	}
	req := r.desc.reqs[0]
	if req.Kind != domain.ContentPhoto || req.MimeType != "image/jpeg" || filepath.Base(req.Path) != "normalized.jpg" {
		t.Fatalf("request %+v", req)
	}
	created := r.store.created
	if len(created) != 1 || created[0].Kind != domain.ContentPhoto || created[0].FileUniqueID != "u-a" ||
		created[0].FileID != "f-a" || created[0].Model != "test-model" || created[0].TotalTokens != 18 ||
		created[0].SHA256 != res.SHA256 || created[0].Phash != res.Phash {
		t.Fatalf("cache row %+v", created)
	}
}

// phase0/02-media.md 1.9: one temporary directory per file, gone afterwards,
// also when the job fails.
func TestTempDirRemoved(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprintf("describer fails=%t", fail), func(t *testing.T) {
			r := newRig(t)
			r.photo(1, "a")
			var dirs []string
			r.desc.fn = func(_ context.Context, req domain.MediaDescribeRequest) (domain.MediaDescription, error) {
				dirs = append(dirs, filepath.Dir(req.Path))
				if _, err := os.Stat(req.Path); err != nil {
					t.Errorf("the normalized file must exist while the model reads it: %v", err)
				}
				if fail {
					return domain.MediaDescription{}, errors.New("model said no")
				}
				return domain.MediaDescription{Text: "x", ActualModel: "m"}, nil
			}
			r.do(1)
			if len(dirs) != 1 {
				t.Fatalf("model calls: %d", len(dirs))
			}
			if _, err := os.Stat(dirs[0]); !os.IsNotExist(err) {
				t.Fatalf("temp dir %s survived the job: %v", dirs[0], err)
			}
			entries, _ := os.ReadDir(r.p.cfg.TempDir)
			if len(entries) != 0 {
				t.Fatalf("temp dir not empty: %v", entries)
			}
		})
	}
}

// phase0/02-media.md 1.3 step 8: any other error is final, with the metadata
// description and the error cut to 500 characters.
func TestFinalFailure(t *testing.T) {
	r := newRig(t)
	it := r.photo(1, "a")
	r.desc.fn = func(context.Context, domain.MediaDescribeRequest) (domain.MediaDescription, error) {
		return domain.MediaDescription{}, errors.New(strings.Repeat("я", 800))
	}

	r.do(1)

	res := r.store.result(t, 1)
	if res.Status != domain.MediaFailed || res.Description != metaOf(it) || res.RetryCount != nil || !res.NextRetryAt.IsZero() {
		t.Fatalf("result %+v", res)
	}
	if got := len([]rune(res.Error)); got != 500 {
		t.Fatalf("error is %d runes, want 500", got)
	}
}

// AGENTS.md: no secret in a log line or a database column. A token-shaped string
// in an error is removed before the error is stored.
func TestFailureErrorIsScrubbed(t *testing.T) {
	r := newRig(t)
	r.photo(1, "a")
	token := "123456789:AAH-abcdefghijklmnopqrstuvwxyz0123456"
	r.desc.fn = func(context.Context, domain.MediaDescribeRequest) (domain.MediaDescription, error) {
		return domain.MediaDescription{}, fmt.Errorf("Post https://api.telegram.org/bot%s/getFile: boom", token)
	}
	r.do(1)
	res := r.store.result(t, 1)
	if strings.Contains(res.Error, token) || !strings.Contains(res.Error, "boom") {
		t.Fatalf("error %q", res.Error)
	}
}

func TestNormalizeFailure(t *testing.T) {
	r := newRig(t)
	r.files.payload["f-a"] = []byte("this is not a picture")
	it := r.store.add(1, Item{Kind: domain.ContentPhoto, FileID: "f-a", FileUniqueID: "u-a", MimeType: "image/jpeg"})

	r.do(1)

	res := r.store.result(t, 1)
	if res.Status != domain.MediaFailed || res.Description != metaOf(it) || !strings.Contains(res.Error, "image") {
		t.Fatalf("result %+v", res)
	}
	if r.desc.count() != 0 {
		t.Fatal("model called for a file that could not be read")
	}
}

// doc §2: image.DecodeConfig first; a picture over the pixel limit is "failed"
// with the metadata description and is never decoded.
func TestImageOverPixelLimit(t *testing.T) {
	shorten(t, &maxImagePixels, 1000)
	r := newRig(t)
	r.files.payload["big"] = pngBytes(40, 30) // 1200 pixels
	it := r.store.add(1, Item{Kind: domain.ContentImage, FileID: "big", FileUniqueID: "u-big", MimeType: "image/png", FileName: "scan.png"})

	r.do(1)

	res := r.store.result(t, 1)
	if res.Status != domain.MediaFailed || res.Description != metaOf(it) || !strings.Contains(res.Error, "too many pixels") {
		t.Fatalf("result %+v", res)
	}
	if r.desc.count() != 0 {
		t.Fatal("model called")
	}
}

// ---- retries ----------------------------------------------------------------

func retryable(msg string, after time.Duration, counts bool) error {
	return &domain.MediaRetryableError{Err: errors.New(msg), RetryAfter: after, CountsAsAttempt: counts}
}

// tests/test_media.py::test_transient_media_failure_is_scheduled_with_backoff
// (MED-09), ::test_transient_media_failure_stops_after_max_attempts (MED-10) and
// ::test_provider_cooldown_does_not_consume_retry_attempt (MED-11), plus the
// delay table of doc §6 "Queue claim": 300/600/1200/2400 s capped at 3600.
func TestRetrySchedule(t *testing.T) {
	now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name       string
		have       int
		err        error
		status     string
		count      int
		delay      time.Duration
		errorStays bool
	}{
		{"first failure", 0, retryable("busy", 0, true), domain.MediaPending, 1, 300 * time.Second, true},
		{"MED-09 one attempt used", 1, retryable("busy", 0, true), domain.MediaPending, 2, 600 * time.Second, true},
		{"third", 2, retryable("busy", 0, true), domain.MediaPending, 3, 1200 * time.Second, true},
		{"fourth", 3, retryable("busy", 0, true), domain.MediaPending, 4, 2400 * time.Second, true},
		{"MED-10 fifth is the last", 4, retryable("busy", 0, true), domain.MediaFailed, 5, 0, true},
		{"already past the limit", 7, retryable("busy", 0, true), domain.MediaFailed, 8, 0, true},
		{"MED-11 cooldown keeps the count", 2, retryable("cooldown", 900*time.Second, false), domain.MediaPending, 2, 900 * time.Second, true},
		{"cooldown at the limit is still pending", 4, retryable("cooldown", 900*time.Second, false), domain.MediaPending, 4, 2400 * time.Second, true},
		{"cooldown on a fresh row waits at least the base", 0, retryable("cooldown", 0, false), domain.MediaPending, 0, 300 * time.Second, true},
		{"provider hint longer than the schedule wins", 0, retryable("rate limited", 1800*time.Second, true), domain.MediaPending, 1, 1800 * time.Second, true},
		{"provider hint shorter than the schedule loses", 2, retryable("rate limited", 30*time.Second, true), domain.MediaPending, 3, 1200 * time.Second, true},
		{"wrapped error", 0, fmt.Errorf("describe: %w", retryable("busy", 0, true)), domain.MediaPending, 1, 300 * time.Second, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newRig(t)
			r.clock.t = now
			it := r.photo(1, "a")
			r.store.items[1].RetryCount = tt.have
			r.desc.fn = func(context.Context, domain.MediaDescribeRequest) (domain.MediaDescription, error) {
				return domain.MediaDescription{}, tt.err
			}

			r.do(1)

			res := r.store.result(t, 1)
			if res.Status != tt.status || res.RetryCount == nil || *res.RetryCount != tt.count {
				t.Fatalf("result %+v (retry %v)", res, res.RetryCount)
			}
			if res.Description != metaOf(it) || res.Error == "" {
				t.Fatalf("a retried row keeps the metadata description and the error: %+v", res)
			}
			if tt.status == domain.MediaPending {
				if !res.NextRetryAt.Equal(now.Add(tt.delay)) {
					t.Fatalf("next_retry_at %s, want %s", res.NextRetryAt, now.Add(tt.delay))
				}
			} else if !res.NextRetryAt.IsZero() {
				t.Fatalf("a failed row has no next_retry_at: %s", res.NextRetryAt)
			}
		})
	}
}

func TestRetryDelayCeiling(t *testing.T) {
	want := []time.Duration{300, 600, 1200, 2400, 3600, 3600, 3600}
	for i, w := range want {
		if got := retryDelay(i + 1); got != w*time.Second {
			t.Fatalf("delay after attempt %d = %s, want %s", i+1, got, w*time.Second)
		}
	}
	if got := retryDelay(1 << 30); got != 3600*time.Second {
		t.Fatalf("huge attempt count: %s", got)
	}
	shorten(t, &retryBaseDelay, 10*time.Second)
	shorten(t, &retryMaxDelay, 25*time.Second)
	if retryDelay(1) != 10*time.Second || retryDelay(2) != 20*time.Second || retryDelay(3) != 25*time.Second {
		t.Fatal("the delays must follow the package variables")
	}
}

// tests/test_media.py::test_process_pending_media_times_out_slow_item_and_continues
// (MED-07): a job over its time is counted as an attempt and scheduled again.
func TestJobTimeoutIsRetried(t *testing.T) {
	shorten(t, &itemTimeout, 20*time.Millisecond)
	r := newRig(t)
	r.photo(1, "a")
	r.desc.fn = func(ctx context.Context, _ domain.MediaDescribeRequest) (domain.MediaDescription, error) {
		<-ctx.Done()
		return domain.MediaDescription{}, ctx.Err()
	}

	r.do(1)

	res := r.store.result(t, 1)
	if res.Status != domain.MediaPending || res.RetryCount == nil || *res.RetryCount != 1 ||
		!strings.Contains(res.Error, "timed out") || !res.NextRetryAt.After(r.clock.Now()) {
		t.Fatalf("result %+v", res)
	}
}

// Shutdown: a cancelled job writes nothing; the lease runs out and the next
// start picks the row up.
func TestShutdownLeavesTheRow(t *testing.T) {
	r := newRig(t)
	r.photo(1, "a")
	ctx, cancel := context.WithCancel(context.Background())
	r.desc.fn = func(ctx context.Context, _ domain.MediaDescribeRequest) (domain.MediaDescription, error) {
		cancel()
		<-ctx.Done()
		return domain.MediaDescription{}, ctx.Err()
	}

	r.p.process(ctx, job{messageDBID: 1})

	r.store.mu.Lock()
	defer r.store.mu.Unlock()
	if len(r.store.finishLog) != 0 {
		t.Fatalf("a cancelled job stored %+v", r.store.finishLog)
	}
}

func TestClaimLostOrFailedDoesNothing(t *testing.T) {
	t.Run("claim denied", func(t *testing.T) {
		r := newRig(t)
		r.photo(1, "a")
		r.store.claimDenied = true
		r.do(1)
		if len(r.store.finishLog) != 0 || len(r.files.getCalls) != 0 || r.store.groupCall != 0 {
			t.Fatal("a job that lost the lease must stop at once")
		}
	})
	t.Run("claim error", func(t *testing.T) {
		r := newRig(t)
		r.photo(1, "a")
		r.store.claimErr = errors.New("db down")
		r.do(1)
		if len(r.store.finishLog) != 0 || len(r.files.getCalls) != 0 {
			t.Fatal("a job that could not claim must stop at once")
		}
	})
}

func TestFinishErrorIsLoggedNotFatal(t *testing.T) {
	r := newRig(t)
	r.photo(1, "a")
	r.store.finishErr = errors.New("db down")
	r.do(1) // must return
}

// ---- download classification (decisions M1 and M4) ---------------------------

func TestDownloadErrors(t *testing.T) {
	apiErr := func(status int, desc string, retryAfter time.Duration) error {
		return &tg.APIError{Method: "getFile", StatusCode: status, ErrorCode: status, Description: desc, RetryAfter: retryAfter}
	}
	tests := []struct {
		name string
		get  error // from GetFile
		dl   error // from DownloadToFile
		// what must happen
		status string
		count  int
		delay  time.Duration
	}{
		{name: "M4 too large from the download", dl: fmt.Errorf("write: %w", tg.ErrFileTooLarge), status: domain.MediaFailed},
		{name: "M4 cloud 20 MB limit", get: apiErr(400, "Bad Request: file is too big", 0), status: domain.MediaFailed},
		{name: "M1 network error", dl: &net.OpError{Op: "read", Err: errors.New("connection reset by peer")}, status: domain.MediaPending, count: 1, delay: 300 * time.Second},
		{name: "M1 network error from getFile", get: errors.New("telegram transport failed: dial tcp: i/o timeout"), status: domain.MediaPending, count: 1, delay: 300 * time.Second},
		{name: "M1 body cut off mid-download", dl: fmt.Errorf("write download: %w", io.ErrUnexpectedEOF), status: domain.MediaPending, count: 1, delay: 300 * time.Second},
		{name: "M1 flood wait", get: apiErr(429, "Too Many Requests: retry after 1200", 1200*time.Second), status: domain.MediaPending, count: 1, delay: 1200 * time.Second},
		{name: "server error", dl: apiErr(502, "Bad Gateway", 0), status: domain.MediaPending, count: 1, delay: 300 * time.Second},
		{name: "file id expired", get: apiErr(400, "Bad Request: wrong file_id", 0), status: domain.MediaFailed},
		{name: "local file missing", dl: fmt.Errorf("local bot api file unavailable: %w", os.ErrNotExist), status: domain.MediaFailed},
		{name: "plain error", dl: errors.New("create download target: disk full"), status: domain.MediaFailed},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newRig(t)
			it := r.photo(1, "a")
			r.files.getErr, r.files.dlErr = tt.get, tt.dl

			r.do(1)

			res := r.store.result(t, 1)
			if res.Status != tt.status {
				t.Fatalf("status %q, want %q (%+v)", res.Status, tt.status, res)
			}
			if res.Description != metaOf(it) || res.Error == "" {
				t.Fatalf("result %+v", res)
			}
			if tt.status == domain.MediaPending {
				if res.RetryCount == nil || *res.RetryCount != tt.count || !res.NextRetryAt.Equal(r.clock.Now().Add(tt.delay)) {
					t.Fatalf("result %+v, want count %d delay %s", res, tt.count, tt.delay)
				}
			} else if res.RetryCount != nil {
				t.Fatalf("a final failure leaves retry_count alone: %+v", res)
			}
			if r.desc.count() != 0 {
				t.Fatal("model called")
			}
		})
	}
}

// M4: the size in the message is checked before Telegram is asked anything, and
// the limit is passed to the download.
func TestDownloadLimit(t *testing.T) {
	t.Run("size known from the message", func(t *testing.T) {
		r := newRig(t)
		r.photo(1, "a")
		r.store.items[1].FileSize = maxDownloadBytes + 1
		r.do(1)
		res := r.store.result(t, 1)
		if res.Status != domain.MediaFailed || len(r.files.getCalls) != 0 {
			t.Fatalf("result %+v, getFile calls %v", res, r.files.getCalls)
		}
	})
	t.Run("size known from getFile", func(t *testing.T) {
		r := newRig(t)
		r.photo(1, "a")
		r.files.file.FileSize = maxDownloadBytes + 1
		r.do(1)
		res := r.store.result(t, 1)
		if res.Status != domain.MediaFailed || len(r.files.dlCalls) != 0 {
			t.Fatalf("result %+v, downloads %v", res, r.files.dlCalls)
		}
	})
	t.Run("limit is 100 MiB and goes to the client", func(t *testing.T) {
		r := newRig(t)
		r.photo(1, "a")
		r.do(1)
		if r.files.maxBytes != 100<<20 {
			t.Fatalf("limit passed to DownloadToFile: %d", r.files.maxBytes)
		}
	})
	t.Run("no file id", func(t *testing.T) {
		r := newRig(t)
		r.store.add(1, Item{Kind: domain.ContentPhoto, MimeType: "image/jpeg"})
		r.do(1)
		if res := r.store.result(t, 1); res.Status != domain.MediaFailed || !strings.Contains(res.Error, "file_id") {
			t.Fatalf("result %+v", res)
		}
	})
}

func TestSuffixForMime(t *testing.T) {
	tests := map[string]string{
		"image/jpeg": ".jpg", "image/png": ".png", "image/webp": ".webp", "video/mp4": ".mp4", "video/webm": ".webm",
		"audio/ogg": ".ogg", "audio/mpeg": ".mp3", "application/x-tgsticker": ".tgs", "application/pdf": "", "": "",
	}
	for mime, want := range tests {
		if got := suffixForMime(mime); got != want {
			t.Errorf("suffixForMime(%q) = %q, want %q", mime, got, want)
		}
	}
}

// The downloaded file is named after the MIME type, then after the path
// Telegram gave, then ".bin".
func TestRawFileName(t *testing.T) {
	tests := []struct{ mime, path, want string }{
		{"image/jpeg", "photos/file_1.jpg", "telegram.jpg"},
		{"application/pdf", "documents/file_2.pdf", "telegram.pdf"},
		{"application/pdf", "documents/file_3", "telegram.bin"},
	}
	for _, tt := range tests {
		r := newRig(t)
		var dst string
		files := &namedFiles{fakeFiles: r.files, path: tt.path, dst: &dst}
		r.p.files = files
		r.files.payload[strings.TrimPrefix(tt.path, "files/")] = jpegBytes(8, 8)
		if _, err := r.p.download(context.Background(), domain.MediaSource{FileID: "x", MimeType: tt.mime}, t.TempDir()); err != nil {
			t.Fatal(err)
		}
		if filepath.Base(dst) != tt.want {
			t.Errorf("mime %q path %q: file %q, want %q", tt.mime, tt.path, filepath.Base(dst), tt.want)
		}
	}
}

// namedFiles answers GetFile with a fixed path and remembers the destination.
type namedFiles struct {
	*fakeFiles
	path string
	dst  *string
}

func (n *namedFiles) GetFile(_ context.Context, id string) (tg.File, error) {
	return tg.File{FileID: id, FilePath: n.path}, nil
}

func (n *namedFiles) DownloadToFile(ctx context.Context, filePath, dst string, max int64) error {
	*n.dst = dst
	return os.WriteFile(dst, []byte("x"), 0o600)
}
