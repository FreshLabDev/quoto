// SPDX-License-Identifier: Apache-2.0

package media

import (
	"bytes"
	"context"
	"fmt"
	"hash/fnv"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"math/rand/v2"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/FreshLabDev/tg"

	"github.com/FreshLabDev/quoto/internal/domain"
)

// ---- store ------------------------------------------------------------------

type cacheRow struct {
	CacheEntry
	Kind         string
	FileUniqueID string
	FileID       string
}

// fakeStore keeps rows in memory and applies the same rules as PGStore, without
// the SQL. The SQL has its own tests against PostgreSQL.
type fakeStore struct {
	mu sync.Mutex

	items  map[int64]*Item // by message id
	nextID int64
	group  domain.GroupSettings
	// groupFn, when set, answers Group instead of group.
	groupFn   func(call int) (domain.GroupSettings, bool, error)
	groupCall int
	cache     []cacheRow
	pending   []Item

	claimErr, finishErr, cacheErr error
	claimDenied                   bool

	calls     []string
	claimed   []int64
	finished  map[int64]Result
	finishLog []Result
	created   []NewCache
	lastPend  struct {
		now    time.Time
		minAge time.Duration
		limit  int
	}

	onClaim  func(Item)
	onFinish func(Item, Result)
}

func newFakeStore() *fakeStore {
	open := true
	at := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	version := domain.MinAcceptedVersion
	return &fakeStore{
		items:    map[int64]*Item{},
		finished: map[int64]Result{},
		group: domain.GroupSettings{
			MediaAnalysis:       &open,
			AgreementAcceptedAt: &at,
			AgreementVersion:    &version,
		},
	}
}

// add stores a media row for message id and returns it.
func (s *fakeStore) add(messageDBID int64, it Item) Item {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nextID++
	it.ID, it.MessageDBID = s.nextID, messageDBID
	if it.Status == "" {
		it.Status = domain.MediaPending
	}
	cp := it
	s.items[messageDBID] = &cp
	return it
}

func (s *fakeStore) record(format string, args ...any) {
	s.calls = append(s.calls, fmt.Sprintf(format, args...))
}

func (s *fakeStore) callLog() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.calls...)
}

func (s *fakeStore) Claim(_ context.Context, messageDBID, itemID int64, now time.Time, lease time.Duration) (Item, bool, error) {
	s.mu.Lock()
	if s.claimErr != nil {
		defer s.mu.Unlock()
		return Item{}, false, s.claimErr
	}
	it := s.items[messageDBID]
	s.record("claim %d", messageDBID)
	if it == nil || s.claimDenied || (itemID != 0 && it.ID != itemID) ||
		(it.Status != domain.MediaPending && it.Status != domain.MediaProcessing) {
		s.mu.Unlock()
		return Item{}, false, nil
	}
	it.Status = domain.MediaProcessing
	got := *it
	s.claimed = append(s.claimed, messageDBID)
	hook := s.onClaim
	s.mu.Unlock()
	if hook != nil {
		hook(got)
	}
	return got, true, nil
}

func (s *fakeStore) Item(_ context.Context, messageDBID int64) (Item, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	it := s.items[messageDBID]
	if it == nil {
		return Item{}, false, nil
	}
	return *it, true, nil
}

func (s *fakeStore) Pending(_ context.Context, now time.Time, minAge time.Duration, limit int) ([]Item, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastPend.now, s.lastPend.minAge, s.lastPend.limit = now, minAge, limit
	s.record("pending")
	return append([]Item(nil), s.pending...), nil
}

func (s *fakeStore) Group(_ context.Context, messageDBID int64) (domain.GroupSettings, bool, error) {
	s.mu.Lock()
	s.groupCall++
	call := s.groupCall
	fn := s.groupFn
	g := s.group
	s.record("group %d", messageDBID)
	s.mu.Unlock()
	if fn != nil {
		return fn(call)
	}
	return g, true, nil
}

func (s *fakeStore) FindCache(_ context.Context, by CacheKey, value, kind string) (CacheEntry, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.record("find %s=%s kind=%s", by, value, kind)
	if s.cacheErr != nil {
		return CacheEntry{}, false, s.cacheErr
	}
	for _, r := range s.cache {
		if r.Kind != kind {
			continue
		}
		if (by == CacheByFileUniqueID && r.FileUniqueID == value) ||
			(by == CacheByFileID && r.FileID == value) ||
			(by == CacheBySHA256 && r.SHA256 == value) {
			return r.CacheEntry, true, nil
		}
	}
	return CacheEntry{}, false, nil
}

func (s *fakeStore) PhashCandidates(_ context.Context, kind string) ([]PhashRow, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.record("phashes kind=%s", kind)
	var out []PhashRow
	for _, r := range s.cache {
		if r.Kind == kind && r.Phash != "" {
			out = append(out, PhashRow{ID: r.ID, Phash: r.Phash})
		}
	}
	return out, nil
}

func (s *fakeStore) CacheByID(_ context.Context, id int64) (CacheEntry, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.record("cache %d", id)
	for _, r := range s.cache {
		if r.ID == id {
			return r.CacheEntry, true, nil
		}
	}
	return CacheEntry{}, false, nil
}

func (s *fakeStore) CreateCache(_ context.Context, c NewCache) (CacheEntry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.record("create-cache kind=%s sha=%s", c.Kind, c.SHA256)
	s.created = append(s.created, c)
	row := cacheRow{
		CacheEntry:   CacheEntry{ID: int64(100 + len(s.cache)), Description: c.Description, SHA256: c.SHA256, Phash: c.Phash},
		Kind:         c.Kind,
		FileUniqueID: c.FileUniqueID,
		FileID:       c.FileID,
	}
	s.cache = append(s.cache, row)
	return row.CacheEntry, nil
}

func (s *fakeStore) Finish(_ context.Context, item Item, r Result) error {
	s.mu.Lock()
	if s.finishErr != nil {
		defer s.mu.Unlock()
		return s.finishErr
	}
	s.record("finish %d %s", item.MessageDBID, r.Status)
	s.finished[item.MessageDBID] = r
	s.finishLog = append(s.finishLog, r)
	hook := s.onFinish
	s.mu.Unlock()
	if hook != nil {
		hook(item, r)
	}
	return nil
}

func (s *fakeStore) result(t testing.TB, messageDBID int64) Result {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.finished[messageDBID]
	if !ok {
		t.Fatalf("message %d was not finished; calls: %v", messageDBID, s.calls)
	}
	return r
}

// ---- files ------------------------------------------------------------------

// fakeFiles serves payloads by file id and writes them to the destination.
type fakeFiles struct {
	mu       sync.Mutex
	payload  map[string][]byte
	getErr   error
	dlErr    error
	file     tg.File // overrides the answer of GetFile where set
	getCalls []string
	dlCalls  []string
	maxBytes int64
	onDL     func(ctx context.Context, fileID string) error
}

func (f *fakeFiles) GetFile(_ context.Context, fileID string) (tg.File, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.getCalls = append(f.getCalls, fileID)
	if f.getErr != nil {
		return tg.File{}, f.getErr
	}
	out := tg.File{FileID: fileID, FileUniqueID: "u-" + fileID, FilePath: "files/" + fileID}
	if f.file.FileSize != 0 {
		out.FileSize = f.file.FileSize
	}
	return out, nil
}

func (f *fakeFiles) DownloadToFile(ctx context.Context, filePath, dst string, maxBytes int64) error {
	f.mu.Lock()
	f.dlCalls = append(f.dlCalls, filePath)
	f.maxBytes = maxBytes
	err, hook := f.dlErr, f.onDL
	data := f.payload[strings.TrimPrefix(filePath, "files/")]
	f.mu.Unlock()
	if hook != nil {
		if err := hook(ctx, strings.TrimPrefix(filePath, "files/")); err != nil {
			return err
		}
	}
	if err != nil {
		return err
	}
	return os.WriteFile(dst, data, 0o600)
}

// ---- describer --------------------------------------------------------------

type fakeDescriber struct {
	mu    sync.Mutex
	reqs  []domain.MediaDescribeRequest
	files [][]byte
	fn    func(ctx context.Context, req domain.MediaDescribeRequest) (domain.MediaDescription, error)
}

func (d *fakeDescriber) DescribeMedia(ctx context.Context, req domain.MediaDescribeRequest) (domain.MediaDescription, error) {
	d.mu.Lock()
	d.reqs = append(d.reqs, req)
	fn := d.fn
	d.mu.Unlock()
	if data, err := os.ReadFile(req.Path); err == nil {
		d.mu.Lock()
		d.files = append(d.files, data)
		d.mu.Unlock()
	}
	if fn != nil {
		return fn(ctx, req)
	}
	return domain.MediaDescription{Text: "a description", ActualModel: "test-model", PromptTokens: 11, CompletionTokens: 7, TotalTokens: 18}, nil
}

func (d *fakeDescriber) count() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.reqs)
}

// ---- runner -----------------------------------------------------------------

// fakeRunner stands in for ffmpeg and ffprobe: ffprobe answers duration, ffmpeg
// writes its output file (the last argument), a real JPEG when it is a frame.
type fakeRunner struct {
	mu       sync.Mutex
	calls    [][]string
	duration string
	// distinctFrames makes the frames depend on the input file, so that two
	// different videos do not look like one picture to the phash lookup.
	distinctFrames bool
	fn             func(ctx context.Context, name string, args []string) ([]byte, error)
}

func (r *fakeRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	r.mu.Lock()
	r.calls = append(r.calls, append([]string{name}, args...))
	fn := r.fn
	duration := r.duration
	distinct := r.distinctFrames
	r.mu.Unlock()
	if fn != nil {
		if out, err := fn(ctx, name, args); out != nil || err != nil {
			return out, err
		}
	}
	if name == "ffprobe" || strings.HasSuffix(name, "/ffprobe") {
		if duration == "" {
			duration = "12.5"
		}
		return []byte(duration + "\n"), nil
	}
	out := args[len(args)-1]
	data := []byte("normalized " + out)
	if strings.HasSuffix(out, ".jpg") {
		data = jpegBytes(64, 48)
		if distinct {
			data = seededJPEG(64, 48, seedOf(inputContent(args)))
		}
	}
	return nil, os.WriteFile(out, data, 0o600)
}

// inputContent is the file named after -i, or empty.
func inputContent(args []string) string {
	for i, a := range args {
		if a == "-i" && i+1 < len(args) {
			b, _ := os.ReadFile(args[i+1])
			return string(b)
		}
	}
	return ""
}

func (r *fakeRunner) ffmpegCalls() [][]string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out [][]string
	for _, c := range r.calls {
		if strings.HasSuffix(c[0], "ffmpeg") {
			out = append(out, c)
		}
	}
	return out
}

// ---- pictures ---------------------------------------------------------------

func testImage(w, h int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.SetRGBA(x, y, color.RGBA{R: uint8(x * 255 / w), G: uint8(y * 255 / h), B: uint8((x + y) % 256), A: 255})
		}
	}
	return img
}

func jpegBytes(w, h int) []byte {
	var b bytes.Buffer
	if err := jpeg.Encode(&b, testImage(w, h), &jpeg.Options{Quality: 90}); err != nil {
		panic(err)
	}
	return b.Bytes()
}

func seedOf(s string) uint64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(s))
	return h.Sum64()
}

// seededJPEG is a picture of random 8x8 blocks. Two seeds give pictures whose
// perceptual hashes are about 32 bits apart, far beyond the match distance of 5,
// and the blocks survive JPEG compression.
func seededJPEG(w, h int, seed uint64) []byte {
	rng := rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15))
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	cols, rows := (w+7)/8, (h+7)/8
	blocks := make([]color.RGBA, cols*rows)
	for i := range blocks {
		blocks[i] = color.RGBA{R: uint8(rng.UintN(256)), G: uint8(rng.UintN(256)), B: uint8(rng.UintN(256)), A: 255}
	}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.SetRGBA(x, y, blocks[(y/8)*cols+x/8])
		}
	}
	var b bytes.Buffer
	if err := jpeg.Encode(&b, img, &jpeg.Options{Quality: 90}); err != nil {
		panic(err)
	}
	return b.Bytes()
}

func pngBytes(w, h int) []byte {
	var b bytes.Buffer
	if err := png.Encode(&b, testImage(w, h)); err != nil {
		panic(err)
	}
	return b.Bytes()
}

// ---- pipeline ---------------------------------------------------------------

type rig struct {
	p     *Pipeline
	store *fakeStore
	files *fakeFiles
	desc  *fakeDescriber
	run   *fakeRunner
	clock *fakeClock
}

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func newRig(t testing.TB) *rig {
	t.Helper()
	r := &rig{
		store: newFakeStore(),
		files: &fakeFiles{payload: map[string][]byte{}},
		desc:  &fakeDescriber{},
		run:   &fakeRunner{},
		clock: &fakeClock{t: time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)},
	}
	r.p = New(Config{AnalysisEnabled: true, TempDir: t.TempDir()}, Deps{
		Store: r.store, Files: r.files, Describer: r.desc,
	})
	r.p.now = r.clock.Now
	r.p.run = r.run
	return r
}

// photo adds a pending photo row with a JPEG payload.
func (r *rig) photo(messageDBID int64, suffix string) Item {
	r.files.payload["f-"+suffix] = jpegBytes(200, 100)
	return r.store.add(messageDBID, Item{
		Kind: domain.ContentPhoto, FileID: "f-" + suffix, FileUniqueID: "u-" + suffix, MimeType: "image/jpeg",
		FileSize: 2048, Width: 200, Height: 100,
	})
}

// distinctPhoto is a pending photo whose picture differs from every other
// distinctPhoto, so the lookup by content (sha256, phash) never finds a neighbour
// and each message costs a model call.
func (r *rig) distinctPhoto(messageDBID int64, suffix string) Item {
	it := r.photo(messageDBID, suffix)
	r.files.payload[it.FileID] = seededJPEG(200, 100, seedOf(suffix))
	return it
}

// do runs one job as a worker would.
func (r *rig) do(messageDBID int64) {
	r.p.process(context.Background(), job{messageDBID: messageDBID})
}

func shorten(t testing.TB, ptr any, v any) {
	t.Helper()
	switch p := ptr.(type) {
	case *time.Duration:
		old := *p
		*p = v.(time.Duration)
		t.Cleanup(func() { *p = old })
	case *int:
		old := *p
		*p = v.(int)
		t.Cleanup(func() { *p = old })
	case *int64:
		old := *p
		*p = v.(int64)
		t.Cleanup(func() { *p = old })
	default:
		t.Fatalf("shorten: unsupported %T", ptr)
	}
}

func (s *fakeStore) claimedIDs() []int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]int64(nil), s.claimed...)
}
