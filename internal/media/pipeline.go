// SPDX-License-Identifier: Apache-2.0

// Package media turns the files in group messages into text. A media row is
// queued after its message is stored; a worker takes a lease on it, checks that
// the group allows analysis, looks the file up in the cache, and only for a new
// file downloads it, normalizes it with the standard library or ffmpeg and asks
// a model to describe it. The description goes into messages.text and
// message_media, where scoring reads it.
//
// Memory is bounded on purpose (doc section 2): two jobs at a time and at most
// one of them a video, one picture decoded at a time, ffmpeg on one thread, the
// file always on disk.
package media

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/FreshLabDev/tg"
	"golang.org/x/sync/semaphore"
	"golang.org/x/sync/singleflight"

	"github.com/FreshLabDev/quoto/internal/domain"
	qlog "github.com/FreshLabDev/quoto/internal/log"
)

// Deps are the pieces the pipeline needs. Store is satisfied by *PGStore, Files
// by *tg.Client, Describer by *ai.Service.
type Deps struct {
	Store     Store
	Files     Files
	Describer domain.MediaDescriber
	Log       *slog.Logger
}

// Pipeline is the media queue and its workers.
type Pipeline struct {
	cfg       Config
	store     Store
	files     Files
	describer domain.MediaDescriber
	log       *slog.Logger

	now     func() time.Time
	run     Runner
	ffmpeg  string
	ffprobe string

	// inbox takes the ids Submit was given. The router reads the row to learn
	// whether it is a video and passes it on to light or video, so that a long
	// video never holds up a photo.
	inbox chan job
	light chan job
	video chan job

	slots    *semaphore.Weighted // running jobs, maxConcurrent
	decoding *semaphore.Weighted // one picture decode at a time
	flight   singleflight.Group  // one model call per file

	mu     sync.Mutex
	queued map[int64]struct{} // message ids that are queued or running
}

// job is one message's media row on its way to a worker.
type job struct {
	messageDBID int64
	itemID      int64 // 0 until the router has read the row
	video       bool
}

// New builds the pipeline. Nothing runs until Run is called; Submit may be
// called before that and the ids wait in the queue.
func New(cfg Config, deps Deps) *Pipeline {
	if cfg.FFmpegPath == "" {
		cfg.FFmpegPath = "ffmpeg"
	}
	if cfg.FFprobePath == "" {
		cfg.FFprobePath = "ffprobe"
	}
	if cfg.TempDir == "" {
		cfg.TempDir = os.TempDir()
	}
	log := deps.Log
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Pipeline{
		cfg:       cfg,
		store:     deps.Store,
		files:     deps.Files,
		describer: deps.Describer,
		log:       log,
		now:       time.Now,
		run:       execRunner{},
		ffmpeg:    cfg.FFmpegPath,
		ffprobe:   cfg.FFprobePath,
		inbox:     make(chan job, queueSize),
		light:     make(chan job, queueSize),
		video:     make(chan job, queueSize),
		slots:     semaphore.NewWeighted(maxConcurrent),
		decoding:  semaphore.NewWeighted(1),
		queued:    map[int64]struct{}{},
	}
}

// Submit queues the media row of a just-saved message for analysis. It never
// blocks and never fails the caller: when the queue is full the row stays
// pending and the recovery pass picks it up.
func (p *Pipeline) Submit(messageDBID int64) {
	if !p.reserve(messageDBID) {
		return
	}
	select {
	case p.inbox <- job{messageDBID: messageDBID}:
	default:
		p.release(messageDBID)
		p.log.Warn("media queue is full; the row stays pending for the recovery pass", "message_db_id", messageDBID)
	}
}

// Run starts the workers and the recovery pass (every recoveryInterval, first
// run that long after the start) and returns once ctx is done and the running
// jobs have finished or been cancelled.
func (p *Pipeline) Run(ctx context.Context) error {
	var wg sync.WaitGroup
	start := func(fn func()) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			fn()
		}()
	}
	start(func() { p.route(ctx) })
	for i := int64(0); i < maxConcurrent; i++ {
		start(func() { p.work(ctx, p.light) })
	}
	// One goroutine per allowed video: the limit needs no lock.
	for i := 0; i < maxVideos; i++ {
		start(func() { p.work(ctx, p.video) })
	}
	start(func() { p.recoverLoop(ctx) })
	wg.Wait()
	return nil
}

// reserve marks a message as queued; false means it already is, and the second
// request is dropped.
func (p *Pipeline) reserve(id int64) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, ok := p.queued[id]; ok {
		return false
	}
	p.queued[id] = struct{}{}
	return true
}

func (p *Pipeline) release(id int64) {
	p.mu.Lock()
	delete(p.queued, id)
	p.mu.Unlock()
}

// route reads each submitted row once to see which queue it belongs in.
func (p *Pipeline) route(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case j := <-p.inbox:
			item, ok, err := p.store.Item(ctx, j.messageDBID)
			if err != nil || !ok {
				if err != nil && ctx.Err() == nil {
					p.log.Warn("media row not read; the recovery pass will find it", "message_db_id", j.messageDBID, "error", err)
				}
				p.release(j.messageDBID)
				continue
			}
			p.enqueue(item)
		}
	}
}

// enqueue puts a row, already reserved, in the queue of its class. A full queue
// drops it: the row is still pending.
func (p *Pipeline) enqueue(item Item) {
	j := job{messageDBID: item.MessageDBID, itemID: item.ID, video: isVideoKind(domain.AnalysisKind(item.Source()))}
	q := p.light
	if j.video {
		q = p.video
	}
	select {
	case q <- j:
	default:
		p.release(j.messageDBID)
		p.log.Warn("media queue is full; the row stays pending for the recovery pass", "message_db_id", j.messageDBID)
	}
}

func (p *Pipeline) work(ctx context.Context, queue <-chan job) {
	for {
		select {
		case <-ctx.Done():
			return
		case j := <-queue:
			p.handle(ctx, j)
		}
	}
}

// handle waits for a free slot, so no more than maxConcurrent jobs ever run,
// and only then takes the lease: a job that waits for a slot must not spend its
// lease doing it.
func (p *Pipeline) handle(ctx context.Context, j job) {
	defer p.release(j.messageDBID)
	if err := p.slots.Acquire(ctx, 1); err != nil {
		return
	}
	defer p.slots.Release(1)
	p.process(ctx, j)
}

// recoverLoop runs the recovery pass on a timer.
func (p *Pipeline) recoverLoop(ctx context.Context) {
	ticker := time.NewTicker(recoveryInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			p.recoverPending(ctx)
		}
	}
}

// recoverPending queues what the handler path missed: rows that stayed pending
// (a full queue, a restart), rows whose retry time has come, and rows whose
// lease ran out because a worker died. It replaces Python's process_pending_media
// minus the orphan sweep, which has nothing left to sweep (doc section 7.1).
func (p *Pipeline) recoverPending(ctx context.Context) {
	items, err := p.store.Pending(ctx, p.now(), recoveryInterval, recoveryBatch)
	if err != nil {
		if ctx.Err() == nil {
			p.log.Error("media recovery: cannot list pending rows", "error", err)
		}
		return
	}
	queued := 0
	for _, item := range items {
		if p.reserve(item.MessageDBID) {
			p.enqueue(item)
			queued++
		}
	}
	if len(items) > 0 {
		p.log.Info("media recovery", "found", len(items), "queued", queued)
	}
}

// process is one job: lease, analysis, outcome.
func (p *Pipeline) process(ctx context.Context, j job) {
	item, claimed, err := p.store.Claim(ctx, j.messageDBID, j.itemID, p.now(), itemTimeout)
	if err != nil {
		if ctx.Err() == nil {
			p.log.Warn("media row not claimed", "message_db_id", j.messageDBID, "error", err)
		}
		return
	}
	if !claimed {
		return
	}
	src := item.Source()
	started := p.now()

	// Claiming a lease that had run out counted as an attempt. A row that has
	// used them all was claimed by workers that never finished it, which means
	// this file takes the process down; it is not tried again.
	if tries := max(1, retryMaxAttempts); item.RetryCount >= tries {
		count := item.RetryCount
		res := Result{
			Status:      domain.MediaFailed,
			Description: domain.MetadataDescription(src),
			Error:       "analysis never finished: the worker stopped every time this file was tried",
			RetryCount:  &count,
		}
		p.log.Warn("media analysis given up: earlier workers did not finish this file",
			"message_db_id", item.MessageDBID, "attempts", count)
		if err := p.store.Finish(ctx, item, res); err != nil && ctx.Err() == nil {
			p.log.Error("media result not stored", "message_db_id", item.MessageDBID, "status", res.Status, "error", err)
		}
		return
	}

	jobCtx, cancel := context.WithTimeout(ctx, itemTimeout)
	res, err := p.analyse(jobCtx, item, src)
	timedOut := jobCtx.Err() != nil && ctx.Err() == nil
	cancel()

	if ctx.Err() != nil {
		// Shutting down: the result of a cancelled job is meaningless. The row
		// stays processing and the lease runs out; the next start finds it.
		return
	}
	if timedOut {
		err = &domain.MediaRetryableError{Err: errors.New("media analysis item timed out"), CountsAsAttempt: true}
	}
	if err != nil {
		res = p.failure(item, src, err)
	}
	if err := p.store.Finish(ctx, item, res); err != nil {
		p.log.Error("media result not stored", "message_db_id", item.MessageDBID, "status", res.Status, "error", err)
		return
	}
	p.log.Log(ctx, levelFor(res.Status), "media done", "message_db_id", item.MessageDBID, "kind", item.Kind,
		"status", res.Status, "took", p.now().Sub(started).Round(time.Millisecond))
}

func levelFor(status string) slog.Level {
	if status == domain.MediaFailed {
		return slog.LevelWarn
	}
	return slog.LevelDebug
}

// scrubber removes token-shaped strings from what is stored and logged. The tg
// client and the AI clients redact their own errors; this is the last line.
var scrubber = qlog.NewRedactor(nil)

// failure turns an error into the row's next state: a temporary one is retried
// on the schedule, anything else is final, with the metadata description.
func (p *Pipeline) failure(item Item, src domain.MediaSource, err error) Result {
	meta := domain.MetadataDescription(src)
	detail := cutRunes(scrubber.Scrub(err.Error()), 500)

	var retry *domain.MediaRetryableError
	if errors.As(err, &retry) {
		res := retryResult(item, meta, retry, detail, p.now())
		if res.Status == domain.MediaFailed {
			p.log.Warn("media analysis permanently failed", "message_db_id", item.MessageDBID,
				"attempts", *res.RetryCount, "error", detail)
		} else {
			p.log.Warn("media analysis deferred", "message_db_id", item.MessageDBID,
				"retry", fmt.Sprintf("%d/%d", *res.RetryCount, max(1, retryMaxAttempts)),
				"in", res.NextRetryAt.Sub(p.now()).Round(time.Second), "error", detail)
		}
		return res
	}
	// A file over the limit and a Telegram outage are expected and out of our
	// hands; everything else probably points at a bug.
	if errors.Is(err, tg.ErrFileTooLarge) {
		p.log.Debug("media analysis skipped", "message_db_id", item.MessageDBID, "error", detail)
	} else {
		p.log.Warn("media analysis failed", "message_db_id", item.MessageDBID, "error", detail)
	}
	return Result{Status: domain.MediaFailed, Description: meta, Error: detail}
}

// cutRunes is Python's s[:n].
func cutRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}
