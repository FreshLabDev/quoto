// SPDX-License-Identifier: Apache-2.0

package media

import (
	"context"
	"image"
	"image/color"
	"image/png"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/image/draw"

	"github.com/FreshLabDev/quoto/internal/domain"
)

// noisyPicture is a gradient with texture: smooth areas and sharp ones both.
func noisyPicture(w, h int) *image.RGBA {
	rng := rand.New(rand.NewPCG(1, 2))
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			n := uint8(rng.IntN(40))
			img.SetRGBA(x, y, color.RGBA{uint8(x * 255 / w), uint8(y * 255 / h), uint8((x/37+y/53)%2*120) + n, 255})
		}
	}
	return img
}

// The two-pass scaler must give the picture the single draw call gives, up to the
// rounding of the middle image; it exists only to avoid that call's scratch buffer.
func TestScaleCatmullRomBandedMatchesSinglePass(t *testing.T) {
	for _, tc := range []struct{ w, h int }{{3000, 2000}, {1300, 3900}, {1281, 17}} {
		src := noisyPicture(tc.w, tc.h)
		nw, nh := scaledSize(tc.w, tc.h, imageMaxSide)
		want := image.NewRGBA(image.Rect(0, 0, nw, nh))
		draw.CatmullRom.Scale(want, want.Bounds(), src, src.Bounds(), draw.Src, nil)
		got := image.NewRGBA(image.Rect(0, 0, nw, nh))
		scaleCatmullRom(got, src)
		maxDiff, sum := 0, 0
		for i := range want.Pix {
			d := int(want.Pix[i]) - int(got.Pix[i])
			if d < 0 {
				d = -d
			}
			sum += d
			maxDiff = max(maxDiff, d)
		}
		mean := float64(sum) / float64(len(want.Pix))
		if maxDiff > 16 || mean > 0.2 {
			t.Errorf("%dx%d: max channel difference %d, mean %.3f", tc.w, tc.h, maxDiff, mean)
		}
	}
}

func TestBigImage(t *testing.T) {
	dir := t.TempDir()
	write := func(name string, w, h int) string {
		path := filepath.Join(dir, name)
		f, err := os.Create(path)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		if err := png.Encode(f, image.NewGray(image.Rect(0, 0, w, h))); err != nil {
			t.Fatal(err)
		}
		return path
	}
	if bigImage(write("small.png", 1280, 960)) {
		t.Error("1280x960 counted as heavy")
	}
	if !bigImage(write("big.png", 3000, 2100)) {
		t.Error("3000x2100 not counted as heavy")
	}
	if bigImage(filepath.Join(dir, "missing")) {
		t.Error("a missing file counted as heavy")
	}
}

// A video in ffmpeg and a large picture are the two jobs that cost memory
// (about 120 MB and 130 MB); they must not run together.
func TestLargePictureWaitsForTheVideo(t *testing.T) {
	r := newRig(t)
	started := make(chan struct{})
	release := make(chan struct{})
	r.run.fn = func(ctx context.Context, name string, args []string) ([]byte, error) {
		if strings.HasSuffix(name, "ffmpeg") && containsPair(args, "-c:v", "libx264") {
			close(started)
			<-release
		}
		return nil, nil
	}
	big := filepath.Join(t.TempDir(), "big.png")
	f, err := os.Create(big)
	if err != nil {
		t.Fatal(err)
	}
	if err := png.Encode(f, image.NewGray(image.Rect(0, 0, 3000, 2100))); err != nil {
		t.Fatal(err)
	}
	f.Close()

	videoDone := make(chan error, 1)
	go func() {
		_, err := r.p.normalizeVideo(context.Background(), domain.MediaSource{Kind: domain.ContentVideo}, big, t.TempDir())
		videoDone <- err
	}()
	<-started
	imageDone := make(chan error, 1)
	go func() {
		_, err := r.p.normalizeImage(context.Background(), big, t.TempDir())
		imageDone <- err
	}()
	select {
	case err := <-imageDone:
		t.Fatalf("a large picture was normalized while ffmpeg was running (err %v)", err)
	case <-time.After(300 * time.Millisecond):
	}
	close(release)
	if err := <-videoDone; err != nil {
		t.Fatal(err)
	}
	if err := <-imageDone; err != nil {
		t.Fatal(err)
	}
}
