// SPDX-License-Identifier: Apache-2.0

package media

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/FreshLabDev/quoto/internal/domain"
)

// The tests in this file run the real ffmpeg and ffprobe on clips generated with
// lavfi. They check what the argument tests cannot: that the filters do what the
// comments say (no upscaling, one pause kept, the cap honoured). They are
// skipped where the tools or an encoder are missing.

// realPipeline is a pipeline wired to the machine's ffmpeg.
func realPipeline(t testing.TB) *Pipeline {
	t.Helper()
	for _, tool := range []string{"ffmpeg", "ffprobe"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s is not installed", tool)
		}
	}
	out, err := exec.Command("ffmpeg", "-hide_banner", "-encoders").CombinedOutput()
	if err != nil {
		t.Skipf("ffmpeg -encoders: %v", err)
	}
	for _, enc := range []string{"libx264", "libmp3lame", " aac "} {
		if !strings.Contains(string(out), enc) {
			t.Skipf("ffmpeg has no %s encoder", strings.TrimSpace(enc))
		}
	}
	return New(Config{TempDir: t.TempDir()}, Deps{})
}

// lavfi runs ffmpeg to make a test clip and returns its path.
func lavfi(t testing.TB, name string, args ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	full := append([]string{"-nostdin", "-hide_banner", "-loglevel", "error", "-y"}, args...)
	full = append(full, path)
	if out, err := exec.CommandContext(ctx, "ffmpeg", full...).CombinedOutput(); err != nil {
		t.Fatalf("make %s: %v\n%s", name, err, out)
	}
	return path
}

// testClip is a video with a sine track.
func testClip(t testing.TB, size string, seconds int) string {
	t.Helper()
	return lavfi(t, "clip.mp4",
		"-f", "lavfi", "-i", fmt.Sprintf("testsrc=size=%s:rate=10:duration=%d", size, seconds),
		"-f", "lavfi", "-i", fmt.Sprintf("sine=frequency=440:duration=%d", seconds),
		"-c:v", "libx264", "-preset", "ultrafast", "-pix_fmt", "yuv420p", "-c:a", "aac", "-shortest")
}

// probe returns the key=value pairs ffprobe prints for the first stream of the
// selector.
func probe(t testing.TB, path, selector, entries string) map[string]string {
	t.Helper()
	out, err := exec.Command("ffprobe", "-v", "error", "-select_streams", selector,
		"-show_entries", "stream="+entries, "-of", "default=noprint_wrappers=1", path).CombinedOutput()
	if err != nil {
		t.Fatalf("ffprobe %s: %v\n%s", path, err, out)
	}
	got := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if k, v, ok := strings.Cut(line, "="); ok {
			got[k] = v
		}
	}
	return got
}

func dimensions(t testing.TB, path string) (w, h int) {
	t.Helper()
	m := probe(t, path, "v:0", "width,height")
	w, _ = strconv.Atoi(m["width"])
	h, _ = strconv.Atoi(m["height"])
	return w, h
}

// doc 7.3 M3 / phase0/02-media.md 1.5: a video is scaled down to 720 px high and
// never up; sizes stay even for x264; 12 fps; the audio track is kept.
func TestRealVideoSizes(t *testing.T) {
	tests := []struct {
		name     string
		size     string
		w, h     int // 0 means "only check the rules"
		maxH     int
		evenOnly bool
	}{
		{name: "small stays small", size: "320x240", w: 320, h: 240},
		{name: "small portrait stays small", size: "240x426", w: 240, h: 426},
		{name: "1080p comes down to 720p", size: "1920x1080", w: 1280, h: 720},
		{name: "portrait 1080x1920 comes down to 720 high", size: "1080x1920", maxH: 720, evenOnly: true},
		{name: "exactly 720p is left alone", size: "1280x720", w: 1280, h: 720},
	}
	p := realPipeline(t)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clip := testClip(t, tt.size, 1)

			got, err := p.normalize(context.Background(), domain.MediaSource{Kind: domain.ContentVideo, SupportsAnalysis: true}, clip, t.TempDir())
			if err != nil {
				t.Fatal(err)
			}

			if got.Mime != "video/mp4" {
				t.Fatalf("mime %q", got.Mime)
			}
			w, h := dimensions(t, got.Path)
			switch {
			case tt.w != 0 && (w != tt.w || h != tt.h):
				t.Fatalf("size %dx%d, want %dx%d", w, h, tt.w, tt.h)
			case tt.maxH != 0 && h != tt.maxH:
				t.Fatalf("height %d, want %d", h, tt.maxH)
			}
			if w%2 != 0 || h%2 != 0 {
				t.Fatalf("size %dx%d is not even", w, h)
			}
			if fps := probe(t, got.Path, "v:0", "r_frame_rate")["r_frame_rate"]; fps != "12/1" {
				t.Fatalf("frame rate %q, want 12/1", fps)
			}
			if codec := probe(t, got.Path, "a:0", "codec_name")["codec_name"]; codec != "aac" {
				t.Fatalf("audio codec %q, want aac", codec)
			}
		})
	}
}

// doc 7.3 M3: one duration cap. A clip longer than the cap is cut to it.
func TestRealVideoDurationCap(t *testing.T) {
	p := realPipeline(t)
	shorten(t, &videoMaxSeconds, 2)
	clip := testClip(t, "160x120", 5)

	got, err := p.normalize(context.Background(), domain.MediaSource{Kind: domain.ContentVideo, SupportsAnalysis: true}, clip, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	if d := p.probeDuration(context.Background(), got.Path); d < 1.5 || d > 2.6 {
		t.Fatalf("duration %.2f s, want about 2 (the cap) out of 5", d)
	}
}

var hexHash = regexp.MustCompile(`^[0-9a-f]{16}$`)

// phase0/02-media.md 1.5: a video of 3 s or more is hashed at three places.
func TestRealVideoPhashHasThreeFrames(t *testing.T) {
	tests := []struct {
		seconds int
		parts   int
	}{
		{1, 1},
		{2, 2},
		{4, 3},
	}
	p := realPipeline(t)
	for _, tt := range tests {
		t.Run(fmt.Sprintf("%d s", tt.seconds), func(t *testing.T) {
			clip := testClip(t, "160x120", tt.seconds)

			got, err := p.normalize(context.Background(), domain.MediaSource{Kind: domain.ContentVideo, SupportsAnalysis: true}, clip, t.TempDir())
			if err != nil {
				t.Fatal(err)
			}

			parts := strings.Split(got.Phash, ":")
			if len(parts) != tt.parts {
				t.Fatalf("phash %q has %d parts, want %d", got.Phash, len(parts), tt.parts)
			}
			for _, part := range parts {
				if !hexHash.MatchString(part) {
					t.Fatalf("part %q is not 16 hex digits", part)
				}
			}
			if got.SHA256 == "" {
				t.Fatal("no sha256")
			}
		})
	}
}

// pauseClip is silence-free sound, a pause, then sound again: 1 + 1.5 + 2.5 s.
func pauseClip(t testing.TB) string {
	t.Helper()
	return lavfi(t, "pause.wav",
		"-filter_complex",
		"sine=frequency=440:duration=1:sample_rate=24000,aformat=sample_fmts=s16:channel_layouts=mono[a];"+
			"anullsrc=r=24000:cl=mono,atrim=duration=1.5,aformat=sample_fmts=s16:channel_layouts=mono[b];"+
			"sine=frequency=440:duration=2.5:sample_rate=24000,aformat=sample_fmts=s16:channel_layouts=mono[c];"+
			"[a][b][c]concat=n=3:v=0:a=1",
		"-c:a", "pcm_s16le")
}

// doc 7.3 M2: only the silence at the start is cut. In Python a 5 s clip with a
// pause in the middle came out at 2.3 s because the filter also stopped at the
// first pause.
func TestRealAudioKeepsAPauseInTheMiddle(t *testing.T) {
	p := realPipeline(t)
	clip := pauseClip(t)

	got, err := p.normalize(context.Background(), domain.MediaSource{Kind: domain.ContentVoice, SupportsAnalysis: true}, clip, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	if d := p.probeDuration(context.Background(), got.Path); d < 4.5 {
		t.Fatalf("duration %.2f s of 5, the middle was cut", d)
	}
	m := probe(t, got.Path, "a:0", "codec_name,sample_rate,channels")
	if m["codec_name"] != "mp3" || m["sample_rate"] != "24000" || m["channels"] != "1" {
		t.Fatalf("output stream %v, want mono 24 kHz mp3", m)
	}
	if got.Mime != "audio/mpeg" {
		t.Fatalf("mime %q", got.Mime)
	}
}

// The part of M2 that stays: silence before the first sound is removed. The
// filter takes the first start_duration (0.2 s) of the sound with it, in Python
// too, so 2 s of sound after 1.5 s of silence come out at 1.8 s.
func TestRealAudioCutsLeadingSilence(t *testing.T) {
	p := realPipeline(t)
	clip := lavfi(t, "lead.wav",
		"-filter_complex",
		"anullsrc=r=24000:cl=mono,atrim=duration=1.5,aformat=sample_fmts=s16:channel_layouts=mono[a];"+
			"sine=frequency=440:duration=2:sample_rate=24000,aformat=sample_fmts=s16:channel_layouts=mono[b];"+
			"[a][b]concat=n=2:v=0:a=1",
		"-c:a", "pcm_s16le")

	got, err := p.normalize(context.Background(), domain.MediaSource{Kind: domain.ContentVoice, SupportsAnalysis: true}, clip, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	if d := p.probeDuration(context.Background(), got.Path); d < 1.5 || d > 2.4 {
		t.Fatalf("duration %.2f s, want about 1.8 out of 3.5", d)
	}
}

// A file that is not media fails with the tool's message instead of a hang or an
// empty result.
func TestRealGarbageFails(t *testing.T) {
	p := realPipeline(t)
	for _, kind := range []string{domain.ContentVideo, domain.ContentVoice} {
		t.Run(kind, func(t *testing.T) {
			junk := writeTemp(t, "junk.bin", []byte("this is not a media file"))

			_, err := p.normalize(context.Background(), domain.MediaSource{Kind: kind, SupportsAnalysis: true}, junk, t.TempDir())

			if err == nil || !strings.Contains(err.Error(), "ffmpeg") {
				t.Fatalf("error %v, want one that names ffmpeg", err)
			}
		})
	}
}
