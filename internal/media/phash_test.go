// SPDX-License-Identifier: Apache-2.0

package media

import (
	"encoding/json"
	"image"
	"image/color"
	"math/bits"
	"os"
	"strconv"
	"strings"
	"testing"
)

// goldenCase is one record of testdata/phash_golden.json, written by Pillow
// 12.2.0: the grey 32x32 grid Image.resize produced and the hash Python's
// _image_phash derived from it.
type goldenCase struct {
	Name   string `json:"name"`
	Gray32 []int  `json:"gray32"`
	Phash  string `json:"phash"`
}

func loadGolden(t *testing.T) []goldenCase {
	t.Helper()
	raw, err := os.ReadFile("testdata/phash_golden.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []goldenCase
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	if len(cases) != 13 {
		t.Fatalf("golden file has %d records, want 13", len(cases))
	}
	return cases
}

func gridOf(c goldenCase) []uint8 {
	grid := make([]uint8, len(c.Gray32))
	for i, v := range c.Gray32 {
		grid[i] = uint8(v)
	}
	return grid
}

// doc §6 "Media cache": 13 golden phash vectors. Stages 3 to 7 (DCT, median,
// bits) must match Pillow-fed Python bit for bit on a given grey grid.
func TestPhashGolden(t *testing.T) {
	for _, c := range loadGolden(t) {
		t.Run(c.Name, func(t *testing.T) {
			if got := phashFromGrid(gridOf(c)); got != c.Phash {
				t.Fatalf("phash = %s, want %s", got, c.Phash)
			}
		})
	}
}

// synthetic rebuilds the RGB pictures the golden file calls synth:grad:WxH and
// synth:mix:WxH, from the formulas in phase0/02-media.md section 7.3.
func synthetic(kind string, w, h int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			var r, g, b int
			switch kind {
			case "grad":
				r, g, b = (4*x+3*y)%256, (2*x)%256, (5*y)%256
			case "mix":
				r, g, b = (7*x+13*y+(x*y)%251)%256, (x*y)%256, (3*x+11*y)%256
			}
			img.SetRGBA(x, y, color.RGBA{R: uint8(r), G: uint8(g), B: uint8(b), A: 255})
		}
	}
	return img
}

// doc §6 "Media cache" and phase0/02-media.md 7.3: grey conversion and resize
// from the picture itself, compared with Pillow's hash. The cache only needs
// distance <= 5; the brief asks for <= 2.
func TestPhashEndToEndSynthetic(t *testing.T) {
	byName := map[string]goldenCase{}
	for _, c := range loadGolden(t) {
		byName[c.Name] = c
	}
	for _, name := range []string{"synth:grad:64x48", "synth:mix:100x75", "synth:mix:640x480", "synth:grad:1280x853"} {
		t.Run(name, func(t *testing.T) {
			c, ok := byName[name]
			if !ok {
				t.Fatalf("no golden record %s", name)
			}
			parts := strings.Split(name, ":")
			size := strings.Split(parts[2], "x")
			w, _ := strconv.Atoi(size[0])
			h, _ := strconv.Atoi(size[1])
			img := synthetic(parts[1], w, h)

			grey, gw, gh := greyPixels(img)
			grid := resizeGrey(grey, gw, gh, hashGrid, hashGrid)
			differing := 0
			for i, v := range gridOf(c) {
				if grid[i] != v {
					differing++
				}
			}
			t.Logf("grid cells differing from Pillow: %d of 1024", differing)

			got := phashImage(img)
			a, _ := strconv.ParseUint(got, 16, 64)
			b, _ := strconv.ParseUint(c.Phash, 16, 64)
			if d := bits.OnesCount64(a ^ b); d > 2 {
				t.Fatalf("phash %s is %d bits from Pillow's %s", got, d, c.Phash)
			}
		})
	}
}

// A nearest-neighbour grid was measured at distance 18 on real pictures, so the
// resize must smooth: shrinking a fine checkerboard has to give flat grey, not
// a copy of the samples it happened to land on.
func TestResizeSmooths(t *testing.T) {
	const side = 256
	src := make([]uint8, side*side)
	for y := 0; y < side; y++ {
		for x := 0; x < side; x++ {
			if (x+y)%2 == 0 {
				src[y*side+x] = 255
			}
		}
	}
	for i, v := range resizeGrey(src, side, side, hashGrid, hashGrid) {
		if v < 120 || v > 135 {
			t.Fatalf("cell %d = %d, a checkerboard should average to mid grey", i, v)
		}
	}
}

func TestPhashDistance(t *testing.T) {
	huge := phashDistance("", "0000000000000000")
	tests := []struct {
		name        string
		left, right string
		want        int
	}{
		// tests/test_media.py::test_find_cache_hits_phash_with_distance_five
		{"five bits apart", "0000000000000000", "000000000000001f", 5},
		{"equal", "ded14c23388fd964", "ded14c23388fd964", 0},
		{"all bits", "0000000000000000", "ffffffffffffffff", 64},
		{"case does not matter", "00000000000000ab", "00000000000000AB", 0},
		// Video hashes: the best pair counts.
		{"best pair of several", "ffffffffffffffff:0000000000000000", "000000000000000f", 4},
		{"three against three", "0000000000000000:1111111111111111:2222222222222222", "2222222222222223:ffffffffffffffff", 1},
		{"empty parts dropped", ":0000000000000000:", "000000000000000f", 4},
		{"unreadable pair skipped", "zz:0000000000000000", "0000000000000001", 1},
		{"nothing readable", "zz", "0000000000000000", huge},
		{"empty left", "", "0000000000000000", huge},
		{"empty right", "0000000000000000", "", huge},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := phashDistance(tt.left, tt.right); got != tt.want {
				t.Fatalf("distance(%q, %q) = %d, want %d", tt.left, tt.right, got, tt.want)
			}
		})
	}
	if huge <= phashMaxDistance {
		t.Fatalf("no data must never count as a hit, got %d", huge)
	}
}
