// SPDX-License-Identifier: Apache-2.0

package media

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"
	"math/bits"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/FreshLabDev/quoto/internal/domain"
)

func decodeFile(t testing.TB, path string) (image.Image, string) {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	img, format, err := image.Decode(f)
	if err != nil {
		t.Fatal(err)
	}
	return img, format
}

// tests/test_media.py::test_normalize_image_computes_sha_and_phash (MED-13), and
// doc §6 "Strings in the database": the result is a JPEG, sha256 is 64 hex of
// the normalized bytes, phash is 16 hex.
func TestNormalizeImage(t *testing.T) {
	tests := []struct {
		name         string
		raw          []byte
		w, h         int // expected size of the JPEG
		inW, inH     int
		wantFormat   string
		fromTestdata string
	}{
		// Thin pictures keep the test quick while crossing the real 1280 limit.
		{name: "wide strip is scaled to 1280", raw: pngBytes(3000, 40), w: 1280, h: 17},
		{name: "tall strip is scaled to 1280", raw: pngBytes(40, 3000), w: 17, h: 1280},
		{name: "just over 1280", raw: pngBytes(1300, 30), w: 1280, h: 30},
		{name: "exactly 1280 is left alone", raw: pngBytes(1280, 40), w: 1280, h: 40},
		{name: "a small picture is never enlarged", raw: pngBytes(200, 100), w: 200, h: 100},
		{name: "jpeg in", raw: jpegBytes(640, 480), w: 640, h: 480},
		{name: "sticker, WebP with alpha", fromTestdata: "sticker.webp", w: 96, h: 64},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newRig(t)
			raw := tt.raw
			if tt.fromTestdata != "" {
				var err error
				if raw, err = os.ReadFile(filepath.Join("testdata", tt.fromTestdata)); err != nil {
					t.Fatal(err)
				}
			}
			dir := t.TempDir()

			n, err := r.p.normalizeImage(context.Background(), writeTemp(t, "raw.bin", raw), dir)
			if err != nil {
				t.Fatal(err)
			}

			if n.Mime != "image/jpeg" || filepath.Base(n.Path) != "normalized.jpg" {
				t.Fatalf("normalized %+v", n)
			}
			img, format := decodeFile(t, n.Path)
			if format != "jpeg" || img.Bounds().Dx() != tt.w || img.Bounds().Dy() != tt.h {
				t.Fatalf("output is %s %dx%d, want jpeg %dx%d", format, img.Bounds().Dx(), img.Bounds().Dy(), tt.w, tt.h)
			}
			data, _ := os.ReadFile(n.Path)
			sum := sha256.Sum256(data)
			if n.SHA256 != hex.EncodeToString(sum[:]) || len(n.SHA256) != 64 {
				t.Fatalf("sha256 %q is not that of the file", n.SHA256)
			}
			if _, err := strconv.ParseUint(n.Phash, 16, 64); err != nil || len(n.Phash) != 16 {
				t.Fatalf("phash %q", n.Phash)
			}
		})
	}
}

// doc §6 "Media cache": the hash of the normalized file is the hash of the
// picture. Phase 0 measured 0 bits between a photo and its 82-quality JPEG; the
// cache accepts 5. The test picture is smooth like a photo: a sawtooth pattern
// would not survive any JPEG.
func TestNormalizedImageHashSurvivesJPEG(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 640, 480))
	for y := 0; y < 480; y++ {
		for x := 0; x < 640; x++ {
			fx, fy := float64(x), float64(y)
			img.SetRGBA(x, y, color.RGBA{
				R: uint8(128 + 100*math.Sin(fx/90)*math.Cos(fy/70)),
				G: uint8(128 + 90*math.Sin((fx+fy)/120)),
				B: uint8(128 + 100*math.Cos(fx/50-fy/110)),
				A: 255,
			})
		}
	}
	var raw bytes.Buffer
	if err := png.Encode(&raw, img); err != nil {
		t.Fatal(err)
	}
	r := newRig(t)
	n, err := r.p.normalizeImage(context.Background(), writeTemp(t, "raw.png", raw.Bytes()), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	original, _ := strconv.ParseUint(phashImage(img), 16, 64)
	got, _ := strconv.ParseUint(n.Phash, 16, 64)
	if d := bits.OnesCount64(original ^ got); d > phashMaxDistance {
		t.Fatalf("phash moved %d bits through the JPEG, the cache accepts %d", d, phashMaxDistance)
	} else {
		t.Logf("phash moved %d bits through the JPEG", d)
	}
}

// A picture larger than the limit is scaled to it; the limit is a variable so
// the test need not decode millions of pixels.
func TestNormalizeImageScalesToTheLimit(t *testing.T) {
	shorten(t, &imageMaxSide, 128)
	r := newRig(t)
	n, err := r.p.normalizeImage(context.Background(), writeTemp(t, "raw.png", pngBytes(600, 400)), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	img, _ := decodeFile(t, n.Path)
	if img.Bounds().Dx() != 128 || img.Bounds().Dy() != 85 { // 400*128/600 = 85.3
		t.Fatalf("output %dx%d, want 128x85", img.Bounds().Dx(), img.Bounds().Dy())
	}
}

func TestScaledSize(t *testing.T) {
	tests := []struct{ w, h, nw, nh int }{
		{3000, 2000, 1280, 853},
		{2000, 3000, 853, 1280},
		{1281, 1280, 1280, 1279}, // 1280*1280/1281 = 1278.99 -> rounds to 1279
		{10000, 10, 1280, 1},     // never zero
		{1280, 2560, 640, 1280},
	}
	for _, tt := range tests {
		if nw, nh := scaledSize(tt.w, tt.h, 1280); nw != tt.nw || nh != tt.nh {
			t.Errorf("scaledSize(%d, %d) = %dx%d, want %dx%d", tt.w, tt.h, nw, nh, tt.nw, tt.nh)
		}
	}
}

// doc §2: image.DecodeConfig first; the header decides before any pixel is
// decoded. More than maxImagePixels is refused, exactly that many is not.
func TestImageTooManyPixelsIsRefused(t *testing.T) {
	shorten(t, &maxImagePixels, 100)
	r := newRig(t)
	_, err := r.p.normalizeImage(context.Background(), writeTemp(t, "raw.png", pngBytes(11, 10)), t.TempDir())
	if !errors.Is(err, errImageTooLarge) {
		t.Fatalf("err = %v, want errImageTooLarge", err)
	}
	if _, err := r.p.normalizeImage(context.Background(), writeTemp(t, "raw.png", pngBytes(10, 10)), t.TempDir()); err != nil {
		t.Fatalf("exactly at the limit is allowed: %v", err)
	}
}

// A picture that is not one, and an empty file, are errors, not panics.
func TestImageGarbage(t *testing.T) {
	r := newRig(t)
	for name, data := range map[string][]byte{"text": []byte("hello"), "empty": nil, "truncated jpeg": jpegBytes(64, 64)[:100]} {
		if _, err := r.p.normalizeImage(context.Background(), writeTemp(t, "raw.bin", data), t.TempDir()); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}

// ---- video, audio, ffmpeg ------------------------------------------------------

func valueAfter(t testing.TB, args []string, flag string) string {
	t.Helper()
	for i, a := range args {
		if a == flag && i+1 < len(args) {
			return args[i+1]
		}
	}
	t.Fatalf("%s not in %q", flag, args)
	return ""
}

func indexOf(args []string, flag string) int {
	for i, a := range args {
		if a == flag {
			return i
		}
	}
	return -1
}

// doc §2: "-threads 1 -filter_threads 1 on input and -threads 1 on output" for
// every ffmpeg call: video, audio and the frames taken for the hash.
func assertSingleThreaded(t testing.TB, args []string) {
	t.Helper()
	in := indexOf(args, "-i")
	if in < 0 {
		t.Fatalf("no -i in %q", args)
	}
	before, after := args[:in], args[in:]
	if !containsPair(before, "-threads", "1") || !containsPair(before, "-filter_threads", "1") {
		t.Fatalf("input side lacks -threads 1 -filter_threads 1: %q", args)
	}
	if !containsPair(after[:len(after)-1], "-threads", "1") {
		t.Fatalf("output side lacks -threads 1: %q", args)
	}
	if args[len(args)-2] != "1" || args[len(args)-3] != "-threads" {
		t.Fatalf("-threads 1 must sit right before the output file: %q", args)
	}
	if !containsPair(args, "-loglevel", "error") || indexOf(args, "-nostdin") < 0 {
		t.Fatalf("ffmpeg must be quiet and must not read stdin: %q", args)
	}
}

func containsPair(args []string, key, value string) bool {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == key && args[i+1] == value {
			return true
		}
	}
	return false
}

// tests/test_media.py::test_long_video_normalization_cuts_to_supported_low_resolution_limit
// (MED-14), changed by decision M3: one duration cap of 3600 s, and the scale
// filter never enlarges.
func TestVideoCommand(t *testing.T) {
	tests := []struct {
		name    string
		probe   string // what ffprobe says; "" makes it fail
		source  float64
		seconds string
	}{
		{"MED-14 a 20000 s video is cut to the cap", "20000.5", 20000, "3600"},
		{"exactly the cap", "3600", 0, "3600"},
		{"just over", "3600.4", 0, "3600"},
		{"an ordinary clip keeps its whole seconds", "12.9", 0, "12"},
		{"under a second is one second", "0.4", 0, "1"},
		{"unknown length takes the cap", "", 0, "3600"},
		{"ffprobe fails, the message's duration is used", "", 30.7, "30"},
		{"ffprobe says N/A, the message's duration is used", "N/A", 45, "45"},
		{"ffprobe says zero", "0.000000", 8, "8"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newRig(t)
			r.run.fn = func(_ context.Context, name string, _ []string) ([]byte, error) {
				if name == "ffprobe" && tt.probe == "" {
					return nil, errors.New("ffprobe exploded")
				}
				return nil, nil
			}
			r.run.duration = tt.probe
			src := domain.MediaSource{Kind: domain.ContentVideo, Duration: tt.source, SupportsAnalysis: true}
			raw := writeTemp(t, "raw.mp4", []byte("video"))

			n, err := r.p.normalizeVideo(context.Background(), src, raw, t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			if n.Mime != "video/mp4" || filepath.Base(n.Path) != "normalized.mp4" || len(n.SHA256) != 64 {
				t.Fatalf("normalized %+v", n)
			}
			encode := r.run.ffmpegCalls()[0]
			if got := valueAfter(t, encode, "-t"); got != tt.seconds {
				t.Fatalf("-t %s, want %s", got, tt.seconds)
			}
			assertSingleThreaded(t, encode)
		})
	}
}

// doc §7.3 M3: scale=-2:720 turned a 320x240 clip into 960x720. The filter caps
// the height at the input's own and keeps the even-size rule x264 needs.
func TestVideoFilter(t *testing.T) {
	args := videoArgs("in.mp4", "out.mp4", 10)
	vf := valueAfter(t, args, "-vf")
	want := "fps=12,scale=-2:'min(720,ih)':force_original_aspect_ratio=decrease:force_divisible_by=2"
	if vf != want {
		t.Fatalf("-vf %q, want %q", vf, want)
	}
	if strings.Contains(vf, "scale=-2:720") {
		t.Fatal("the unconditional 720 scale is the upscaling bug")
	}
	for flag, value := range map[string]string{
		"-c:v": "libx264", "-preset": "veryfast", "-crf": "30", "-c:a": "aac", "-b:a": "64k", "-movflags": "+faststart",
	} {
		if got := valueAfter(t, args, flag); got != value {
			t.Errorf("%s = %q, want %q", flag, got, value)
		}
	}
	if args[len(args)-1] != "out.mp4" || valueAfter(t, args, "-i") != "in.mp4" {
		t.Fatalf("input or output misplaced: %q", args)
	}
}

// tests/test_media.py::test_audio_normalization_uses_mono_mp3_and_silence_trim
// (MED-15), changed by decision M2: only the silence at the start is cut.
func TestAudioCommand(t *testing.T) {
	r := newRig(t)
	raw := writeTemp(t, "raw.ogg", []byte("audio"))

	n, err := r.p.normalizeAudio(context.Background(), raw, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if n.Mime != "audio/mpeg" || filepath.Base(n.Path) != "normalized.mp3" || n.Phash != "" || len(n.SHA256) != 64 {
		t.Fatalf("normalized %+v", n)
	}
	args := r.run.ffmpegCalls()[0]
	if valueAfter(t, args, "-ac") != "1" || valueAfter(t, args, "-ar") != "24000" || valueAfter(t, args, "-b:a") != "64k" {
		t.Fatalf("not mono 24 kHz 64k: %q", args)
	}
	af := valueAfter(t, args, "-af")
	if !strings.HasPrefix(af, "silenceremove=") || !strings.Contains(af, "start_periods=1") {
		t.Fatalf("-af %q", af)
	}
	for _, gone := range []string{"stop_periods", "stop_duration", "stop_threshold"} {
		if strings.Contains(af, gone) {
			t.Fatalf("-af %q still has %s: the first pause would end the clip", af, gone)
		}
	}
	if indexOf(args, "-vn") < 0 {
		t.Fatal("cover art must not be encoded")
	}
	assertSingleThreaded(t, args)
}

// phase0/02-media.md 1.5: frames at 0, duration/2 (from 2 s) and duration-1 (from
// 3 s), each hashed; the hashes are joined by ":".
func TestVideoPhashOffsets(t *testing.T) {
	tests := []struct {
		duration string
		offsets  []string
	}{
		{"0.5", []string{"0.000"}},
		{"1.999", []string{"0.000"}},
		{"2", []string{"0.000", "1.000"}},
		{"2.9", []string{"0.000", "1.450"}},
		{"3", []string{"0.000", "1.500", "2.000"}},
		{"10", []string{"0.000", "5.000", "9.000"}},
		{"", []string{"0.000"}}, // ffprobe failed: only the first frame
	}
	for _, tt := range tests {
		t.Run("duration "+tt.duration, func(t *testing.T) {
			r := newRig(t)
			r.run.duration = tt.duration
			if tt.duration == "" {
				r.run.fn = func(_ context.Context, name string, _ []string) ([]byte, error) {
					if name == "ffprobe" {
						return nil, errors.New("no ffprobe")
					}
					return nil, nil
				}
			}
			video := writeTemp(t, "normalized.mp4", []byte("video"))

			got := r.p.videoPhash(context.Background(), video, t.TempDir())

			var seen []string
			for _, c := range r.run.ffmpegCalls() {
				seen = append(seen, valueAfter(t, c, "-ss"))
				assertSingleThreaded(t, c)
				if valueAfter(t, c, "-frames:v") != "1" {
					t.Errorf("one frame per call: %q", c)
				}
			}
			if !reflect.DeepEqual(seen, tt.offsets) {
				t.Fatalf("frame offsets %q, want %q", seen, tt.offsets)
			}
			parts := strings.Split(got, ":")
			if len(parts) != len(tt.offsets) {
				t.Fatalf("phash %q has %d parts, want %d", got, len(parts), len(tt.offsets))
			}
			for _, part := range parts {
				if len(part) != 16 {
					t.Fatalf("phash part %q", part)
				}
			}
		})
	}
}

// phase0/02-media.md 1.5: a frame that cannot be taken is skipped; with none
// left there is no hash and the video is still analysed.
func TestVideoPhashSkipsBrokenFrames(t *testing.T) {
	r := newRig(t)
	r.run.duration = "10"
	frames := 0
	r.run.fn = func(_ context.Context, name string, args []string) ([]byte, error) {
		if name != "ffprobe" {
			frames++
			if frames == 2 {
				return nil, errors.New("seek failed")
			}
		}
		return nil, nil
	}
	got := r.p.videoPhash(context.Background(), writeTemp(t, "v.mp4", []byte("v")), t.TempDir())
	if parts := strings.Split(got, ":"); len(parts) != 2 {
		t.Fatalf("phash %q, want the two frames that worked", got)
	}

	r = newRig(t)
	r.run.fn = func(_ context.Context, name string, _ []string) ([]byte, error) {
		if name != "ffprobe" {
			return nil, errors.New("no")
		}
		return nil, nil
	}
	if got := r.p.videoPhash(context.Background(), writeTemp(t, "v.mp4", []byte("v")), t.TempDir()); got != "" {
		t.Fatalf("phash %q, want none", got)
	}
}

func TestProbeDuration(t *testing.T) {
	tests := []struct {
		out  string
		err  error
		want float64
	}{
		{"12.5\n", nil, 12.5},
		{"  7.25  \n", nil, 7.25},
		{"3600", nil, 3600},
		{"N/A\n", nil, 0},
		{"", nil, 0},
		{"nan", nil, 0},
		{"inf", nil, 0},
		{"-4", nil, 0},
		{"12.5", errors.New("exit 1"), 0},
	}
	for _, tt := range tests {
		r := newRig(t)
		r.run.fn = func(_ context.Context, _ string, _ []string) ([]byte, error) {
			if tt.err != nil {
				return nil, tt.err
			}
			return []byte(tt.out), nil
		}
		if tt.out == "" && tt.err == nil {
			r.run.fn = func(context.Context, string, []string) ([]byte, error) { return []byte{}, nil }
		}
		if got := r.p.probeDuration(context.Background(), "x.mp4"); got != tt.want {
			t.Errorf("probe of %q (err %v) = %v, want %v", tt.out, tt.err, got, tt.want)
		}
	}
	// The command line is the one Python used.
	r := newRig(t)
	r.p.probeDuration(context.Background(), "x.mp4")
	want := []string{"ffprobe", "-v", "error", "-show_entries", "format=duration", "-of", "default=noprint_wrappers=1:nokey=1", "x.mp4"}
	if got := r.run.calls[0]; !reflect.DeepEqual(got, want) {
		t.Fatalf("ffprobe call %q, want %q", got, want)
	}
}

func TestVideoSeconds(t *testing.T) {
	tests := []struct {
		in   float64
		want int
	}{{0, 3600}, {-1, 3600}, {0.2, 1}, {1, 1}, {59.99, 59}, {3599.9, 3599}, {3600, 3600}, {20000, 3600}, {1e12, 3600}}
	for _, tt := range tests {
		if got := videoSeconds(tt.in); got != tt.want {
			t.Errorf("videoSeconds(%v) = %d, want %d", tt.in, got, tt.want)
		}
	}
}

// Configured binary names reach the runner, not the literal "ffmpeg".
func TestBinaryPaths(t *testing.T) {
	r := newRig(t)
	r.p.ffmpeg, r.p.ffprobe = "/opt/bin/ffmpeg", "/opt/bin/ffprobe"
	r.run.duration = "5"
	if _, err := r.p.normalizeVideo(context.Background(), domain.MediaSource{Kind: domain.ContentVideo}, writeTemp(t, "r.mp4", []byte("v")), t.TempDir()); err != nil {
		t.Fatal(err)
	}
	for _, c := range r.run.calls {
		if c[0] != "/opt/bin/ffmpeg" && c[0] != "/opt/bin/ffprobe" {
			t.Fatalf("call to %q", c[0])
		}
	}
	d := New(Config{}, Deps{})
	if d.ffmpeg != "ffmpeg" || d.ffprobe != "ffprobe" || d.cfg.TempDir != os.TempDir() {
		t.Fatalf("defaults: %q %q %q", d.ffmpeg, d.ffprobe, d.cfg.TempDir)
	}
}

// ffmpeg that does not exist or fails is a final error whose text says why.
func TestNormalizeRunnerErrors(t *testing.T) {
	r := newRig(t)
	r.run.fn = func(_ context.Context, name string, _ []string) ([]byte, error) {
		if name == "ffprobe" {
			return nil, nil
		}
		return nil, fmt.Errorf("command failed with exit status 187: ffmpeg: invalid media data")
	}
	r.run.duration = "5"
	_, err := r.p.normalizeVideo(context.Background(), domain.MediaSource{Kind: domain.ContentVideo}, writeTemp(t, "r.mp4", []byte("v")), t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "invalid media data") {
		t.Fatalf("err = %v", err)
	}
	_, err = r.p.normalizeAudio(context.Background(), writeTemp(t, "r.ogg", []byte("a")), t.TempDir())
	if err == nil {
		t.Fatal("audio: no error")
	}
	if _, err := r.p.normalize(context.Background(), domain.MediaSource{Kind: domain.ContentDocument}, "x", t.TempDir()); err == nil {
		t.Fatal("a document has no normalizer")
	}
}

// The three kinds go to the right normalizer; a video sticker is a video.
func TestNormalizeRouting(t *testing.T) {
	tests := []struct {
		src  domain.MediaSource
		want string
	}{
		{domain.MediaSource{Kind: domain.ContentPhoto}, "normalized.jpg"},
		{domain.MediaSource{Kind: domain.ContentImage}, "normalized.jpg"},
		{domain.MediaSource{Kind: domain.ContentSticker, MimeType: "image/webp"}, "normalized.jpg"},
		{domain.MediaSource{Kind: domain.ContentVideo}, "normalized.mp4"},
		{domain.MediaSource{Kind: domain.ContentAnimation}, "normalized.mp4"},
		{domain.MediaSource{Kind: domain.ContentVideoNote}, "normalized.mp4"},
		{domain.MediaSource{Kind: domain.ContentSticker, MimeType: "video/webm"}, "normalized.mp4"},
		{domain.MediaSource{Kind: domain.ContentVoice}, "normalized.mp3"},
		{domain.MediaSource{Kind: domain.ContentAudio}, "normalized.mp3"},
	}
	for _, tt := range tests {
		r := newRig(t)
		raw := writeTemp(t, "raw", jpegBytes(40, 40))
		n, err := r.p.normalize(context.Background(), tt.src, raw, t.TempDir())
		if err != nil {
			t.Fatalf("%+v: %v", tt.src, err)
		}
		if filepath.Base(n.Path) != tt.want {
			t.Errorf("%s %s: output %s, want %s", tt.src.Kind, tt.src.MimeType, filepath.Base(n.Path), tt.want)
		}
	}
}
