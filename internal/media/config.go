// SPDX-License-Identifier: Apache-2.0

package media

import "time"

// Config is set by cmd/bot from config.Config.
type Config struct {
	AnalysisEnabled bool   // MEDIA_ANALYSIS_ENABLED, the global switch
	FFmpegPath      string // "ffmpeg" if empty
	FFprobePath     string // "ffprobe" if empty
	TempDir         string // os.TempDir() if empty
}

// The Python version read these from MEDIA_* variables that nobody ever set.
// They are package variables, not constants, so a test can shorten one
// (doc section 4.2).
var (
	// maxConcurrent is how many media jobs run at once, and maxVideos how many
	// of those may be a video (doc section 2: ffmpeg is the biggest process).
	maxConcurrent int64 = 2
	maxVideos           = 1
	// queueSize bounds Submit. A full queue drops the id; the row is still
	// pending and the recovery pass finds it.
	queueSize = 256

	// itemTimeout is both the time one job may take and the length of the lease
	// a claim takes (MEDIA_PENDING_ITEM_TIMEOUT_SECONDS).
	itemTimeout = 600 * time.Second
	// recoveryInterval is how often the recovery pass runs and how old a pending
	// row without a retry time must be before it picks it up
	// (MEDIA_PENDING_RETRY_INTERVAL_SECONDS).
	recoveryInterval = 300 * time.Second
	recoveryBatch    = 10

	retryMaxAttempts = 5
	retryBaseDelay   = 300 * time.Second
	retryMaxDelay    = 3600 * time.Second

	// commandTimeout bounds one ffmpeg or ffprobe run.
	commandTimeout = 300 * time.Second

	// maxDownloadBytes is the largest file taken from Telegram (decision M4).
	// A cloud Bot API stops at 20 MB anyway; a local server does not.
	maxDownloadBytes int64 = 100 << 20
	// maxImagePixels is the largest picture decoded (doc section 2). A Telegram
	// photo is at most 1280 px and never gets near it; a document can be anything.
	// A progressive JPEG of 40 million pixels took 850 MB to decode in the review,
	// and 16 million is a 4000x4000 picture, which keeps every camera photo.
	maxImagePixels = 16_000_000

	// maxVideoPixels is the largest frame ffmpeg is asked to read: 4096x2304.
	// An 8K clip of two megabytes took 470 MB.
	maxVideoPixels = 4096 * 2304

	phashMaxDistance = 5

	imageMaxSide = 1280
	imageQuality = 82

	videoFPS       = 12
	videoMaxHeight = 720
	videoCRF       = 30
	// videoMaxSeconds is the one duration cap (decision M3).
	videoMaxSeconds = 3600

	audioBitrate    = "64k"
	audioSampleRate = 24000
)
