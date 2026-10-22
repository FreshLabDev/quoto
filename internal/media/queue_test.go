// SPDX-License-Identifier: Apache-2.0

package media

import (
	"context"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/FreshLabDev/quoto/internal/domain"
)

// waitFor spins until cond holds. It yields instead of sleeping, so a test that
// waits on a goroutine does not depend on how long that takes; the deadline
// only turns a hang into a failure.
func waitFor(t testing.TB, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("gave up waiting for %s", what)
		}
		runtime.Gosched()
	}
}

// goroutineStacks is the text of every goroutine's stack.
func goroutineStacks() []string {
	buf := make([]byte, 1<<20)
	for {
		n := runtime.Stack(buf, true)
		if n < len(buf) {
			return strings.Split(string(buf[:n]), "\n\n")
		}
		buf = make([]byte, 2*len(buf))
	}
}

// blockedIn counts goroutines whose stack contains every one of parts.
func blockedIn(parts ...string) int {
	count := 0
next:
	for _, stack := range goroutineStacks() {
		for _, p := range parts {
			if !strings.Contains(stack, p) {
				continue next
			}
		}
		count++
	}
	return count
}

// start runs the pipeline and returns a function that stops it and waits.
func (r *rig) start(t testing.TB) (stop func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- r.p.Run(ctx) }()
	var once sync.Once
	stop = func() {
		once.Do(func() {
			cancel()
			if err := <-done; err != nil {
				t.Errorf("Run returned %v", err)
			}
		})
	}
	t.Cleanup(stop)
	return stop
}

// finishedSignal reports each finished message id on a channel.
func (r *rig) finishedSignal(buffer int) <-chan int64 {
	ch := make(chan int64, buffer)
	prev := r.store.onFinish
	r.store.onFinish = func(it Item, res Result) {
		if prev != nil {
			prev(it, res)
		}
		ch <- it.MessageDBID
	}
	return ch
}

func recv[T any](t testing.TB, ch <-chan T, what string) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(20 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
		panic("unreachable")
	}
}

func (r *rig) video(messageDBID int64, suffix string) Item {
	r.run.distinctFrames = true
	r.files.payload["f-"+suffix] = []byte("raw video " + suffix)
	return r.store.add(messageDBID, Item{
		Kind: domain.ContentVideo, FileID: "f-" + suffix, FileUniqueID: "u-" + suffix, MimeType: "video/mp4",
		FileSize: 4096, Duration: 8,
	})
}

// ---- Submit and Run -----------------------------------------------------------

// The brief: Submit never blocks and never fails the caller; a full queue leaves
// the row pending for the recovery pass. The same message queued twice is one job.
func TestSubmitNeverBlocks(t *testing.T) {
	shorten(t, &queueSize, 3)
	r := newRig(t)
	for id := int64(1); id <= 3; id++ {
		r.photo(id, string(rune('a'+id)))
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		for round := 0; round < 3; round++ {
			for id := int64(1); id <= 10; id++ { // 7 ids more than the queue holds
				r.p.Submit(id)
			}
		}
	}()
	recv(t, done, "Submit calls to return with nobody reading the queue")

	if got := len(r.p.inbox); got != 3 {
		t.Fatalf("queue holds %d, want 3", got)
	}
	r.p.mu.Lock()
	reserved := len(r.p.queued)
	r.p.mu.Unlock()
	if reserved != 3 {
		t.Fatalf("%d ids reserved, want 3: an id dropped from a full queue must be free again", reserved)
	}
}

// Submit from the handler, through the router and a worker, to the stored result.
func TestSubmitRunsTheJob(t *testing.T) {
	r := newRig(t)
	r.photo(7, "a")
	done := r.finishedSignal(4)
	r.start(t)

	r.p.Submit(7)

	if id := recv(t, done, "the job"); id != 7 {
		t.Fatalf("finished %d", id)
	}
	if res := r.store.result(t, 7); res.Status != domain.MediaAnalyzed {
		t.Fatalf("result %+v", res)
	}
}

func TestSubmitRoutesVideoToItsOwnWorker(t *testing.T) {
	r := newRig(t)
	r.video(1, "v")
	done := r.finishedSignal(4)
	r.start(t)

	r.p.Submit(1)

	recv(t, done, "the video")
	res := r.store.result(t, 1)
	if res.Status != domain.MediaAnalyzed || len(r.run.ffmpegCalls()) == 0 {
		t.Fatalf("result %+v, ffmpeg calls %d", res, len(r.run.ffmpegCalls()))
	}
}

func TestSubmitForUnknownRowIsDropped(t *testing.T) {
	r := newRig(t)
	r.photo(1, "a")
	done := r.finishedSignal(4)
	r.start(t)

	r.p.Submit(404) // no such row
	r.p.Submit(1)

	if id := recv(t, done, "the real job"); id != 1 {
		t.Fatalf("finished %d", id)
	}
	waitFor(t, "id 404 to be freed", func() bool {
		r.p.mu.Lock()
		defer r.p.mu.Unlock()
		_, held := r.p.queued[404]
		return !held
	})
}

// The same file in a second message: the first message stores a cache row, the
// second finds it by Telegram's unique id and costs nothing.
func TestSecondMessageWithTheSameFileIsCached(t *testing.T) {
	r := newRig(t)
	r.photo(1, "same")
	r.photo(2, "same")

	r.do(1)
	r.do(2)

	first, second := r.store.result(t, 1), r.store.result(t, 2)
	if first.Status != domain.MediaAnalyzed || second.Status != domain.MediaCached {
		t.Fatalf("statuses %q, %q", first.Status, second.Status)
	}
	if first.CacheID != second.CacheID || second.Description != first.Description {
		t.Fatalf("results %+v, %+v", first, second)
	}
	if r.desc.count() != 1 || len(r.files.dlCalls) != 1 {
		t.Fatalf("model calls %d, downloads %d, want one each", r.desc.count(), len(r.files.dlCalls))
	}
}

// The brief: two messages with one file at the same time cost one model call
// (singleflight on the cache key, the unique file id).
func TestSimultaneousMessagesWithTheSameFileShareOneModelCall(t *testing.T) {
	r := newRig(t)
	r.files.payload["f-1"] = jpegBytes(200, 100)
	r.files.payload["f-2"] = jpegBytes(200, 100)
	// Two messages, two file ids, one Telegram unique id: the same photo
	// forwarded twice.
	r.store.add(1, Item{Kind: domain.ContentPhoto, FileID: "f-1", FileUniqueID: "u-same", MimeType: "image/jpeg"})
	r.store.add(2, Item{Kind: domain.ContentPhoto, FileID: "f-2", FileUniqueID: "u-same", MimeType: "image/jpeg"})

	entered := make(chan struct{}, 4)
	release := make(chan struct{})
	r.desc.fn = func(ctx context.Context, _ domain.MediaDescribeRequest) (domain.MediaDescription, error) {
		entered <- struct{}{}
		select {
		case <-release:
		case <-ctx.Done():
		}
		return domain.MediaDescription{Text: "one call", ActualModel: "m"}, nil
	}

	var wg sync.WaitGroup
	for _, id := range []int64{1, 2} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r.do(id)
		}()
	}
	recv(t, entered, "the first model call")
	// The follower is parked inside singleflight's Do, on the wait group of the
	// leader's call.
	waitFor(t, "the second job to wait for the first", func() bool {
		return blockedIn("singleflight.(*Group).Do", "sync.(*WaitGroup).Wait") >= 1
	})
	close(release)
	wg.Wait()

	if r.desc.count() != 1 {
		t.Fatalf("%d model calls for one file", r.desc.count())
	}
	if len(r.store.created) != 1 {
		t.Fatalf("%d cache rows", len(r.store.created))
	}
	a, b := r.store.result(t, 1), r.store.result(t, 2)
	if a.Status != domain.MediaAnalyzed || b.Status != domain.MediaAnalyzed ||
		a.CacheID != b.CacheID || a.Description != "one call" || b.Description != "one call" {
		t.Fatalf("results %+v, %+v", a, b)
	}
}

// If the job that started the shared call is cancelled, the one waiting on it
// makes the call itself instead of inheriting a failure that was not its own.
func TestFollowerRetriesAfterLeaderIsCancelled(t *testing.T) {
	r := newRig(t)
	r.files.payload["f-1"] = jpegBytes(200, 100)
	r.files.payload["f-2"] = jpegBytes(200, 100)
	r.store.add(1, Item{Kind: domain.ContentPhoto, FileID: "f-1", FileUniqueID: "u-same", MimeType: "image/jpeg"})
	r.store.add(2, Item{Kind: domain.ContentPhoto, FileID: "f-2", FileUniqueID: "u-same", MimeType: "image/jpeg"})

	var calls atomic.Int32
	entered := make(chan struct{}, 4)
	r.desc.fn = func(ctx context.Context, _ domain.MediaDescribeRequest) (domain.MediaDescription, error) {
		if calls.Add(1) == 1 {
			entered <- struct{}{}
			<-ctx.Done() // the leader: cancelled from outside
			return domain.MediaDescription{}, ctx.Err()
		}
		return domain.MediaDescription{Text: "second try", ActualModel: "m"}, nil
	}

	leaderCtx, cancelLeader := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); r.p.process(leaderCtx, job{messageDBID: 1}) }()
	recv(t, entered, "the leader's model call")
	go func() { defer wg.Done(); r.p.process(context.Background(), job{messageDBID: 2}) }()
	waitFor(t, "the follower to wait", func() bool {
		return blockedIn("singleflight.(*Group).Do", "sync.(*WaitGroup).Wait") >= 1
	})
	cancelLeader()
	wg.Wait()

	if res := r.store.result(t, 2); res.Status != domain.MediaAnalyzed || res.Description != "second try" {
		t.Fatalf("follower result %+v", res)
	}
	if calls.Load() != 2 {
		t.Fatalf("%d model calls, want 2", calls.Load())
	}
}

// ---- limits (doc §2) ----------------------------------------------------------

// gated makes every model call wait for the test, and counts jobs between claim
// and finish, which is how long a job holds its slot.
type gated struct {
	entered chan int64
	release chan struct{}

	mu        sync.Mutex
	active    int
	maxActive int
	videos    int
	maxVideos int
}

func (r *rig) gate() *gated {
	g := &gated{entered: make(chan int64, 64), release: make(chan struct{}, 64)}
	r.store.onClaim = func(it Item) {
		g.mu.Lock()
		defer g.mu.Unlock()
		g.active++
		g.maxActive = max(g.maxActive, g.active)
		if isVideoKind(domain.AnalysisKind(it.Source())) {
			g.videos++
			g.maxVideos = max(g.maxVideos, g.videos)
		}
	}
	prev := r.store.onFinish
	r.store.onFinish = func(it Item, res Result) {
		g.mu.Lock()
		g.active--
		if isVideoKind(domain.AnalysisKind(it.Source())) {
			g.videos--
		}
		g.mu.Unlock()
		if prev != nil {
			prev(it, res)
		}
	}
	r.desc.fn = func(ctx context.Context, req domain.MediaDescribeRequest) (domain.MediaDescription, error) {
		g.entered <- 0
		select {
		case <-g.release:
		case <-ctx.Done():
			return domain.MediaDescription{}, ctx.Err()
		}
		return domain.MediaDescription{Text: "d", ActualModel: "m"}, nil
	}
	return g
}

func (g *gated) peaks() (jobs, videos int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.maxActive, g.maxVideos
}

// doc §2: "2 media jobs at once, 1 of them may be a video". Eight photos and
// four videos are queued; no moment has more than two jobs or more than one
// video, and everything finishes.
func TestAtMostTwoJobsAndOneVideo(t *testing.T) {
	r := newRig(t)
	done := r.finishedSignal(64)
	g := r.gate()
	var ids []int64
	for i := int64(1); i <= 8; i++ {
		r.distinctPhoto(i, "p"+string(rune('a'+i)))
		ids = append(ids, i)
	}
	for i := int64(11); i <= 14; i++ {
		r.video(i, "v"+string(rune('a'+i)))
		ids = append(ids, i)
	}
	r.start(t)

	// Mix the two kinds so that videos are not all at the end of the queue.
	for _, id := range []int64{1, 11, 2, 12, 3, 13, 4, 14, 5, 6, 7, 8} {
		r.p.Submit(id)
	}

	recv(t, g.entered, "the first model call")
	for i := 0; i < len(ids); i++ {
		g.release <- struct{}{}
		recv(t, done, "a job to finish")
		if i+1 < len(ids) {
			// More work is waiting; whatever starts next reaches the model.
			recv(t, g.entered, "the next model call")
		}
	}

	jobs, videos := g.peaks()
	if jobs > 2 || videos > 1 {
		t.Fatalf("peak %d jobs and %d videos, the limits are 2 and 1", jobs, videos)
	}
	if jobs < 2 {
		t.Fatalf("peak %d jobs: two should have run side by side", jobs)
	}
	for _, id := range ids {
		if res := r.store.result(t, id); res.Status != domain.MediaAnalyzed {
			t.Fatalf("message %d: %+v", id, res)
		}
	}
}

// A third job waits for a slot without taking its lease: it is not claimed
// until one of the two running jobs is done.
func TestThirdJobWaitsWithoutALease(t *testing.T) {
	r := newRig(t)
	done := r.finishedSignal(8)
	g := r.gate()
	for i := int64(1); i <= 2; i++ {
		r.distinctPhoto(i, "p"+string(rune('a'+i)))
	}
	r.video(3, "v")
	r.start(t)

	r.p.Submit(1)
	r.p.Submit(2)
	recv(t, g.entered, "the first photo at the model")
	recv(t, g.entered, "the second photo at the model")
	r.p.Submit(3)
	// The video worker has taken the video and is parked in Acquire, where both
	// slots are held by the photos.
	waitFor(t, "the video to wait for a slot", func() bool {
		return len(r.p.video) == 0 && blockedIn("semaphore.(*Weighted).Acquire") >= 1
	})
	if slices.Contains(r.store.claimedIDs(), 3) {
		t.Fatal("the video took its lease while it had no slot")
	}

	g.release <- struct{}{}
	recv(t, done, "a photo to finish")
	recv(t, g.entered, "the video at the model")
	if !slices.Contains(r.store.claimedIDs(), 3) {
		t.Fatal("the video should run once a slot is free")
	}
	g.release <- struct{}{}
	g.release <- struct{}{}
	recv(t, done, "the second job")
	recv(t, done, "the third job")
}

// One video at a time: the second waits in the queue while the first runs, even
// though a slot is free for it.
func TestSecondVideoWaitsForTheFirst(t *testing.T) {
	r := newRig(t)
	done := r.finishedSignal(8)
	g := r.gate()
	r.video(1, "v1")
	r.video(2, "v2")
	r.start(t)

	r.p.Submit(1)
	recv(t, g.entered, "the first video at the model")
	r.p.Submit(2)
	waitFor(t, "the second video to be routed", func() bool { return len(r.p.video) == 1 })
	if slices.Contains(r.store.claimedIDs(), 2) {
		t.Fatal("two videos run at once")
	}

	g.release <- struct{}{}
	recv(t, done, "the first video")
	recv(t, g.entered, "the second video at the model")
	g.release <- struct{}{}
	recv(t, done, "the second video")
	if _, videos := g.peaks(); videos != 1 {
		t.Fatalf("peak %d videos", videos)
	}
}

// A photo is not held up by a video that is waiting for its own turn.
func TestPhotoPassesAWaitingVideo(t *testing.T) {
	r := newRig(t)
	done := r.finishedSignal(8)
	g := r.gate()
	r.video(1, "v1")
	r.video(2, "v2")
	r.distinctPhoto(3, "p")
	r.start(t)

	r.p.Submit(1)
	recv(t, g.entered, "the first video")
	r.p.Submit(2)
	r.p.Submit(3)
	recv(t, g.entered, "the photo, past the second video")
	if slices.Contains(r.store.claimedIDs(), 2) {
		t.Fatal("the second video overtook the first")
	}
	for i := 0; i < 3; i++ {
		g.release <- struct{}{}
	}
	for i := 0; i < 3; i++ {
		recv(t, done, "a job")
	}
}

// ---- shutdown and recovery -------------------------------------------------------

// Run returns once its context is done, with a model call in flight: the call is
// cancelled and nothing is stored.
func TestRunStopsWithRunningJobs(t *testing.T) {
	r := newRig(t)
	r.photo(1, "a")
	entered := make(chan struct{}, 1)
	var sawCancel atomic.Bool
	r.desc.fn = func(ctx context.Context, _ domain.MediaDescribeRequest) (domain.MediaDescription, error) {
		entered <- struct{}{}
		<-ctx.Done()
		sawCancel.Store(true)
		return domain.MediaDescription{}, ctx.Err()
	}
	stop := r.start(t)
	r.p.Submit(1)
	recv(t, entered, "the model call")

	stop()

	if !sawCancel.Load() {
		t.Fatal("the running call was not cancelled")
	}
	if n := len(r.store.finishLog); n != 0 {
		t.Fatalf("a cancelled job stored %d results", n)
	}
}

// phase0/02-media.md 1.7: the recovery pass asks for 10 rows, rows older than
// the interval count as stuck.
func TestRecoveryQueuesPendingRows(t *testing.T) {
	r := newRig(t)
	photo := r.photo(1, "a")
	video := r.video(2, "v")
	held := r.photo(3, "held")
	r.store.pending = []Item{photo, video, held, {ID: 99, MessageDBID: 99, Kind: domain.ContentPhoto}}
	r.p.reserve(held.MessageDBID) // already queued or running

	r.p.recoverPending(context.Background())

	r.store.mu.Lock()
	got := r.store.lastPend
	r.store.mu.Unlock()
	if !got.now.Equal(r.clock.Now()) || got.minAge != 300*time.Second || got.limit != 10 {
		t.Fatalf("Pending(%s, %s, %d)", got.now, got.minAge, got.limit)
	}
	if len(r.p.light) != 2 || len(r.p.video) != 1 { // the photo and the unknown row; the video
		t.Fatalf("queued %d light and %d video jobs", len(r.p.light), len(r.p.video))
	}
	j := <-r.p.video
	if j.messageDBID != 2 || j.itemID != video.ID || !j.video {
		t.Fatalf("video job %+v", j)
	}
}

func TestRecoveryLoopProcessesRows(t *testing.T) {
	shorten(t, &recoveryInterval, 2*time.Millisecond)
	r := newRig(t)
	it := r.photo(1, "a")
	r.store.pending = []Item{it}
	done := r.finishedSignal(4)
	r.start(t)

	if id := recv(t, done, "the recovery pass to find the row"); id != 1 {
		t.Fatalf("finished %d", id)
	}
	if res := r.store.result(t, 1); res.Status != domain.MediaAnalyzed {
		t.Fatalf("result %+v", res)
	}
}

// "first run 300 s after start": nothing is looked for before the first tick.
func TestRecoveryWaitsForTheFirstTick(t *testing.T) {
	shorten(t, &recoveryInterval, time.Hour)
	r := newRig(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { r.p.recoverLoop(ctx); close(done) }()
	cancel()
	recv(t, done, "the loop to stop")
	if calls := r.store.callLog(); len(calls) != 0 {
		t.Fatalf("store used before the first tick: %v", calls)
	}
}

// The recovery pass respects the same limits: it only fills the queues.
func TestRecoveryDoesNotExceedLimits(t *testing.T) {
	r := newRig(t)
	g := r.gate()
	done := r.finishedSignal(32)
	var items []Item
	for i := int64(1); i <= 6; i++ {
		items = append(items, r.distinctPhoto(i, "p"+string(rune('a'+i))))
	}
	for i := int64(7); i <= 9; i++ {
		items = append(items, r.video(i, "v"+string(rune('a'+i))))
	}
	r.store.pending = items
	r.start(t)

	r.p.recoverPending(context.Background())
	for i := 0; i < len(items); i++ {
		recv(t, g.entered, "a model call")
		g.release <- struct{}{}
		recv(t, done, "a job")
	}
	if jobs, videos := g.peaks(); jobs > 2 || videos > 1 {
		t.Fatalf("peak %d jobs, %d videos", jobs, videos)
	}
}
