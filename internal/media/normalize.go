// SPDX-License-Identifier: Apache-2.0

package media

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"io"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	// Registered for image.Decode: a document can be any of these, a sticker is
	// WebP. Output is JPEG only.
	_ "image/gif"
	_ "image/png"

	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp"

	"github.com/FreshLabDev/quoto/internal/domain"
)

// Normalized is the file the model gets, with what the cache keys on.
type Normalized struct {
	Path   string
	Mime   string
	SHA256 string // of the normalized bytes, 64 hex
	Phash  string // empty for audio; up to three hashes joined by ":" for video
}

func (p *Pipeline) normalize(ctx context.Context, src domain.MediaSource, raw, dir string) (Normalized, error) {
	switch kind := domain.AnalysisKind(src); kind {
	case domain.ContentPhoto, domain.ContentImage, domain.ContentSticker:
		return p.normalizeImage(ctx, raw, dir)
	case domain.ContentVideo, domain.ContentAnimation, domain.ContentVideoNote:
		return p.normalizeVideo(ctx, src, raw, dir)
	case domain.ContentVoice, domain.ContentAudio:
		return p.normalizeAudio(ctx, raw, dir)
	default:
		return Normalized{}, fmt.Errorf("unsupported media kind: %s", kind)
	}
}

// isVideoKind says whether a job of this analysis kind runs ffmpeg on video,
// the one expensive thing, of which only maxVideos run at a time.
func isVideoKind(kind string) bool {
	switch kind {
	case domain.ContentVideo, domain.ContentAnimation, domain.ContentVideoNote:
		return true
	}
	return false
}

// errImageTooLarge is returned for a picture over maxImagePixels. The header
// says so before a single pixel is decoded (doc section 2).
var errImageTooLarge = errors.New("image has too many pixels")

// readImageConfig reads the header and refuses a picture that would cost too
// much to decode. The reader is left at the start of the file.
func readImageConfig(f *os.File) (image.Config, error) {
	cfg, _, err := image.DecodeConfig(f)
	if err != nil {
		return cfg, fmt.Errorf("read image header: %w", err)
	}
	if cfg.Width <= 0 || cfg.Height <= 0 {
		return cfg, errors.New("image has no pixels")
	}
	if pixels := int64(cfg.Width) * int64(cfg.Height); pixels > int64(maxImagePixels) {
		return cfg, fmt.Errorf("%w: %dx%d is %d pixels, the limit is %d", errImageTooLarge, cfg.Width, cfg.Height, pixels, maxImagePixels)
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return cfg, err
	}
	return cfg, nil
}

// decodeSlot runs fn while holding the only image-decoding slot: a decoded
// picture is the one big allocation of the Go side, and two at once are not
// needed.
func (p *Pipeline) decodeSlot(ctx context.Context, fn func() error) error {
	if err := p.decoding.Acquire(ctx, 1); err != nil {
		return err
	}
	defer p.decoding.Release(1)
	return fn()
}

// normalizeImage ports _normalize_image: at most 1280 px on the long side,
// JPEG quality 82, then the hash of the result.
func (p *Pipeline) normalizeImage(ctx context.Context, raw, dir string) (Normalized, error) {
	out := filepath.Join(dir, "normalized.jpg")
	if err := p.decodeSlot(ctx, func() error { return shrinkToJPEG(raw, out) }); err != nil {
		return Normalized{}, err
	}
	sum, err := sha256File(out)
	if err != nil {
		return Normalized{}, err
	}
	hash, err := p.phashFile(ctx, out)
	if err != nil {
		return Normalized{}, err
	}
	return Normalized{Path: out, Mime: "image/jpeg", SHA256: sum, Phash: hash}, nil
}

// shrinkToJPEG decodes raw, scales it to fit imageMaxSide (never up) and writes
// a JPEG. Pillow's convert("RGB") drops the alpha channel; the JPEG encoder
// does the same.
func shrinkToJPEG(raw, out string) error {
	in, err := os.Open(raw)
	if err != nil {
		return err
	}
	defer in.Close()
	if _, err := readImageConfig(in); err != nil {
		return err
	}
	img, _, err := image.Decode(bufio.NewReader(in))
	if err != nil {
		return fmt.Errorf("decode image: %w", err)
	}
	img = fitWithin(img, imageMaxSide)

	dst, err := os.Create(out)
	if err != nil {
		return err
	}
	w := bufio.NewWriter(dst)
	err = jpeg.Encode(w, img, &jpeg.Options{Quality: imageQuality})
	if err == nil {
		err = w.Flush()
	}
	if cerr := dst.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return fmt.Errorf("encode jpeg: %w", err)
	}
	return nil
}

// fitWithin scales img down so that neither side is over limit, keeping the
// aspect ratio, like Image.thumbnail. A picture that fits is returned as is.
func fitWithin(img image.Image, limit int) image.Image {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= limit && h <= limit {
		return img
	}
	nw, nh := scaledSize(w, h, limit)
	dst := image.NewRGBA(image.Rect(0, 0, nw, nh))
	// Catmull-Rom is the cubic of Pillow's BICUBIC (a = -0.5).
	draw.CatmullRom.Scale(dst, dst.Bounds(), img, b, draw.Src, nil)
	return dst
}

// scaledSize is the size Image.thumbnail picks: the longer side becomes limit,
// the other keeps the ratio, rounded, at least 1.
func scaledSize(w, h, limit int) (int, int) {
	if w >= h {
		return limit, max(1, int(math.Round(float64(h)*float64(limit)/float64(w))))
	}
	return max(1, int(math.Round(float64(w)*float64(limit)/float64(h)))), limit
}

// phashFile hashes a picture on disk: the normalized JPEG, or a frame of the
// normalized video. Both are small, but the header check and the decoding slot
// apply all the same.
func (p *Pipeline) phashFile(ctx context.Context, path string) (string, error) {
	var hash string
	err := p.decodeSlot(ctx, func() error {
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()
		if _, err := readImageConfig(f); err != nil {
			return err
		}
		img, _, err := image.Decode(bufio.NewReader(f))
		if err != nil {
			return fmt.Errorf("decode image: %w", err)
		}
		hash = phashImage(img)
		return nil
	})
	return hash, err
}

// normalizeVideo ports _normalize_video with decisions M3 (no upscaling, one
// duration cap) and the memory rules of doc section 2 (one ffmpeg thread).
func (p *Pipeline) normalizeVideo(ctx context.Context, src domain.MediaSource, raw, dir string) (Normalized, error) {
	out := filepath.Join(dir, "normalized.mp4")
	if w, h, ok := p.probeFrameSize(ctx, raw); ok && int64(w)*int64(h) > int64(maxVideoPixels) {
		return Normalized{}, fmt.Errorf("%w: %dx%d, the limit is %d pixels", errVideoTooLarge, w, h, maxVideoPixels)
	}
	duration := p.probeDuration(ctx, raw)
	if duration <= 0 {
		duration = src.Duration
	}
	if _, err := p.run.Run(ctx, p.ffmpeg, videoArgs(raw, out, videoSeconds(duration))...); err != nil {
		return Normalized{}, err
	}
	sum, err := sha256File(out)
	if err != nil {
		return Normalized{}, err
	}
	return Normalized{Path: out, Mime: "video/mp4", SHA256: sum, Phash: p.videoPhash(ctx, out, dir)}, nil
}

// errVideoTooLarge is returned for a clip whose frames are bigger than
// maxVideoPixels. ffmpeg decodes at full size whatever it is asked to write, and
// a few megabytes of 8K video cost hundreds of megabytes of memory.
var errVideoTooLarge = errors.New("video frames are too large")

// videoSeconds is how much of the video is kept: its length truncated to whole
// seconds, at least 1, at most videoMaxSeconds. An unknown length keeps the
// most there is room for.
func videoSeconds(duration float64) int {
	if duration <= 0 || math.IsNaN(duration) {
		return videoMaxSeconds
	}
	if duration >= float64(videoMaxSeconds) {
		return videoMaxSeconds
	}
	return max(1, int(duration))
}

// ffmpegArgs is the common frame of every ffmpeg call: no stdin, quiet, and one
// thread for the decoder, the filters and the encoder, because the child counts
// against the container's memory (doc section 2). The -filter_threads option is
// global, and -threads is placed as an input option before -i and as an output
// option before the file, as the measurement in phase0/02-media.md did.
func ffmpegArgs(inputOpts []string, input string, outputOpts []string, output string) []string {
	args := []string{"-nostdin", "-hide_banner", "-loglevel", "error", "-nostats", "-y",
		"-threads", "1", "-filter_threads", "1"}
	args = append(args, inputOpts...)
	// The input is a file we wrote. A playlist or a concat script inside it must
	// not make ffmpeg open anything else.
	args = append(args, "-protocol_whitelist", "file", "-i", input)
	args = append(args, outputOpts...)
	return append(args, "-threads", "1", output)
}

// videoArgs: 12 fps, at most 720 px high without ever scaling up (decision M3),
// dimensions even for x264, CRF 30, veryfast, AAC 64k, moov atom first.
func videoArgs(input, output string, seconds int) []string {
	filter := fmt.Sprintf("fps=%d,scale=-2:'min(%d,ih)':force_original_aspect_ratio=decrease:force_divisible_by=2",
		videoFPS, videoMaxHeight)
	return ffmpegArgs(nil, input, []string{
		"-t", strconv.Itoa(seconds),
		"-vf", filter,
		"-c:v", "libx264", "-preset", "veryfast", "-crf", strconv.Itoa(videoCRF),
		"-c:a", "aac", "-b:a", audioBitrate,
		"-movflags", "+faststart",
	}, output)
}

// normalizeAudio ports _normalize_audio with decision M2: only the silence at
// the start is cut. With stop_periods the first pause longer than half a second
// ended the clip (a 5 s clip with a pause in the middle came out as 2.3 s).
func (p *Pipeline) normalizeAudio(ctx context.Context, raw, dir string) (Normalized, error) {
	out := filepath.Join(dir, "normalized.mp3")
	if _, err := p.run.Run(ctx, p.ffmpeg, audioArgs(raw, out)...); err != nil {
		return Normalized{}, err
	}
	sum, err := sha256File(out)
	if err != nil {
		return Normalized{}, err
	}
	return Normalized{Path: out, Mime: "audio/mpeg", SHA256: sum}, nil
}

func audioArgs(input, output string) []string {
	return ffmpegArgs(nil, input, []string{
		"-vn", "-ac", "1", "-ar", strconv.Itoa(audioSampleRate), "-b:a", audioBitrate,
		"-af", "silenceremove=start_periods=1:start_duration=0.2:start_threshold=-50dB",
	}, output)
}

// videoPhash hashes up to three frames of the normalized video: the first, the
// middle (from 2 s) and one second before the end (from 3 s). A frame that
// cannot be taken or decoded is skipped, as in Python; no frame at all means no
// hash.
func (p *Pipeline) videoPhash(ctx context.Context, video, dir string) string {
	duration := p.probeDuration(ctx, video)
	offsets := []float64{0}
	if duration >= 2 {
		offsets = append(offsets, duration/2)
	}
	if duration >= 3 {
		offsets = append(offsets, math.Max(0, duration-1))
	}
	var hashes []string
	for i, offset := range offsets {
		if ctx.Err() != nil {
			break
		}
		frame := filepath.Join(dir, fmt.Sprintf("frame_%d.jpg", i))
		args := ffmpegArgs([]string{"-ss", strconv.FormatFloat(offset, 'f', 3, 64)}, video,
			[]string{"-frames:v", "1"}, frame)
		if _, err := p.run.Run(ctx, p.ffmpeg, args...); err != nil {
			p.log.Debug("video frame skipped", "offset", offset, "error", err)
			continue
		}
		hash, err := p.phashFile(ctx, frame)
		if err != nil {
			p.log.Debug("video frame not hashed", "offset", offset, "error", err)
			continue
		}
		hashes = append(hashes, hash)
	}
	return strings.Join(hashes, ":")
}

// probeDuration is the length in seconds ffprobe reports, 0 when it cannot say.
func (p *Pipeline) probeDuration(ctx context.Context, path string) float64 {
	out, err := p.run.Run(ctx, p.ffprobe, "-v", "error", "-protocol_whitelist", "file",
		"-show_entries", "format=duration", "-of", "default=noprint_wrappers=1:nokey=1", path)
	if err != nil {
		return 0
	}
	v, err := strconv.ParseFloat(strings.TrimSpace(string(out)), 64)
	if err != nil || math.IsNaN(v) || math.IsInf(v, 0) || v < 0 {
		return 0
	}
	return v
}

// probeFrameSize is the width and height of the first video stream, ok false
// when ffprobe cannot say (the caller then goes on, and ffmpeg decides).
func (p *Pipeline) probeFrameSize(ctx context.Context, path string) (width, height int, ok bool) {
	out, err := p.run.Run(ctx, p.ffprobe, "-v", "error", "-protocol_whitelist", "file", "-select_streams", "v:0",
		"-show_entries", "stream=width,height", "-of", "csv=p=0", path)
	if err != nil {
		return 0, 0, false
	}
	w, h, found := strings.Cut(strings.TrimSpace(string(out)), ",")
	if !found {
		return 0, 0, false
	}
	width, errW := strconv.Atoi(strings.TrimSpace(w))
	height, errH := strconv.Atoi(strings.TrimSpace(h))
	if errW != nil || errH != nil || width <= 0 || height <= 0 {
		return 0, 0, false
	}
	return width, height, true
}

// sha256File hashes a file without reading it into memory.
func sha256File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
