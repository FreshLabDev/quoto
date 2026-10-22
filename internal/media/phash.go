// SPDX-License-Identifier: Apache-2.0

package media

import (
	"fmt"
	"image"
	"image/color"
	"math"
	"math/bits"
	"sort"
	"strconv"
	"strings"
)

// The perceptual hash is the one the Python version wrote into media_cache:
// grey, 32x32 bicubic, 8x8 DCT, the 63 AC coefficients against their median,
// 64 bits as 16 hex digits. The DCT half is reproduced bit for bit (see
// TestPhashGolden); the resize half is Pillow's own algorithm, so the grid is
// close to Pillow's and the hash stays within a couple of bits of it. The cache
// only asks for a distance of 5.

const (
	hashGrid  = 32 // the grey grid is hashGrid x hashGrid
	hashBlock = 8  // the DCT keeps hashBlock x hashBlock coefficients
)

// dctCos[u][x] = cos((2x+1)*u*pi/64), the same expression Python evaluates in
// its inner loop, computed once.
var dctCos = func() (t [hashBlock][hashGrid]float64) {
	for u := 0; u < hashBlock; u++ {
		for x := 0; x < hashGrid; x++ {
			t[u][x] = math.Cos(float64((2*x+1)*u) * math.Pi / 64)
		}
	}
	return t
}()

// phashFromGrid hashes a 32x32 grey grid in row-major order, grid[x*32+y].
// Stages 3 to 7 of the algorithm: DCT, median of the AC coefficients, bits.
func phashFromGrid(grid []uint8) string {
	var coeffs [hashBlock * hashBlock]float64
	for u := 0; u < hashBlock; u++ {
		for v := 0; v < hashBlock; v++ {
			total := 0.0
			for x := 0; x < hashGrid; x++ {
				for y := 0; y < hashGrid; y++ {
					// float64() forbids a fused multiply-add: Python rounds
					// after every operation, and so must this.
					total += float64(float64(grid[x*hashGrid+y]) * dctCos[u][x] * dctCos[v][y])
				}
			}
			cu, cv := 1.0, 1.0
			if u == 0 {
				cu = 1 / math.Sqrt2
			}
			if v == 0 {
				cv = 1 / math.Sqrt2
			}
			coeffs[u*hashBlock+v] = 0.25 * cu * cv * total
		}
	}
	// DC is left out of the median, in the hash it is the top bit.
	ac := append([]float64(nil), coeffs[1:]...)
	sort.Float64s(ac)
	median := ac[len(ac)/2]
	var h uint64
	for _, c := range coeffs {
		h <<= 1
		if c > median {
			h |= 1
		}
	}
	return fmt.Sprintf("%016x", h)
}

// phashImage hashes a decoded picture. Colour goes to grey with Pillow's
// integer ITU-R 601 formula, then the grey picture is resized to the grid.
func phashImage(img image.Image) string {
	grey, w, h := greyPixels(img)
	return phashFromGrid(resizeGrey(grey, w, h, hashGrid, hashGrid))
}

// greyPixels converts to 8-bit grey the way Pillow's convert("L") does:
// (19595 R + 38470 G + 7471 B + 0x8000) >> 16 on 8-bit channels.
func greyPixels(img image.Image) (pix []uint8, w, h int) {
	b := img.Bounds()
	w, h = b.Dx(), b.Dy()
	pix = make([]uint8, w*h)
	luma := func(r, g, b uint8) uint8 {
		return uint8((19595*uint32(r) + 38470*uint32(g) + 7471*uint32(b) + 0x8000) >> 16)
	}
	switch src := img.(type) {
	case *image.Gray:
		for y := 0; y < h; y++ {
			copy(pix[y*w:(y+1)*w], src.Pix[y*src.Stride:y*src.Stride+w])
		}
	case *image.YCbCr:
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				yi := src.YOffset(b.Min.X+x, b.Min.Y+y)
				ci := src.COffset(b.Min.X+x, b.Min.Y+y)
				r, g, bl := color.YCbCrToRGB(src.Y[yi], src.Cb[ci], src.Cr[ci])
				pix[y*w+x] = luma(r, g, bl)
			}
		}
	default:
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				r, g, bl, _ := img.At(b.Min.X+x, b.Min.Y+y).RGBA()
				pix[y*w+x] = luma(uint8(r>>8), uint8(g>>8), uint8(bl>>8))
			}
		}
	}
	return pix, w, h
}

// resizeGrey is Pillow's Image.resize((dw, dh)) with its default BICUBIC
// filter (Resample.c): a = -0.5, the kernel stretched by the reduction factor,
// coefficients in 22-bit fixed point, a horizontal pass rounded to 8 bits and
// then a vertical one.
func resizeGrey(src []uint8, sw, sh, dw, dh int) []uint8 {
	hb, hk, hsize := resampleCoeffs(sw, dw)
	vb, vk, vsize := resampleCoeffs(sh, dh)

	mid := make([]uint8, dw*sh)
	for y := 0; y < sh; y++ {
		row := src[y*sw : (y+1)*sw]
		for x := 0; x < dw; x++ {
			xmin, n := hb[2*x], hb[2*x+1]
			k := hk[x*hsize : x*hsize+n]
			acc := int64(1 << (resamplePrecision - 1))
			for i, c := range k {
				acc += int64(row[xmin+i]) * int64(c)
			}
			mid[y*dw+x] = clip8(acc)
		}
	}
	out := make([]uint8, dw*dh)
	for y := 0; y < dh; y++ {
		ymin, n := vb[2*y], vb[2*y+1]
		k := vk[y*vsize : y*vsize+n]
		for x := 0; x < dw; x++ {
			acc := int64(1 << (resamplePrecision - 1))
			for i, c := range k {
				acc += int64(mid[(ymin+i)*dw+x]) * int64(c)
			}
			out[y*dw+x] = clip8(acc)
		}
	}
	return out
}

// resamplePrecision is PRECISION_BITS in Pillow: 32 - 8 - 2.
const resamplePrecision = 22

func clip8(acc int64) uint8 {
	v := acc >> resamplePrecision
	switch {
	case v < 0:
		return 0
	case v > 255:
		return 255
	}
	return uint8(v)
}

// bicubic is Pillow's bicubic_filter with a = -0.5. Its support is 2.
func bicubic(x float64) float64 {
	const a = -0.5
	x = math.Abs(x)
	switch {
	case x < 1:
		return float64(float64(float64((a+2)*x)-(a+3))*x*x) + 1
	case x < 2:
		return float64(float64(float64(float64(float64(x-5)*x)+8)*x)-4) * a
	}
	return 0
}

// resampleCoeffs is precompute_coeffs plus normalize_coeffs_8bpc: for every
// output position the first input index and the number of taps (bounds holds
// the pair), and the taps as fixed-point integers, ksize per position.
func resampleCoeffs(inSize, outSize int) (bounds []int, kk []int32, ksize int) {
	scale := float64(inSize) / float64(outSize)
	filterScale := math.Max(scale, 1)
	support := 2 * filterScale
	ksize = int(math.Ceil(support))*2 + 1
	bounds = make([]int, outSize*2)
	kk = make([]int32, outSize*ksize)
	for xx := 0; xx < outSize; xx++ {
		center := (float64(xx) + 0.5) * scale
		ss := 1 / filterScale
		xmin := int(center - support + 0.5)
		if xmin < 0 {
			xmin = 0
		}
		xmax := int(center + support + 0.5)
		if xmax > inSize {
			xmax = inSize
		}
		xmax -= xmin
		w := make([]float64, xmax)
		sum := 0.0
		for x := 0; x < xmax; x++ {
			w[x] = bicubic((float64(x+xmin) - center + 0.5) * ss)
			sum += w[x]
		}
		for x := 0; x < xmax; x++ {
			v := w[x]
			if sum != 0 {
				v /= sum
			}
			if v < 0 {
				kk[xx*ksize+x] = int32(-0.5 + v*(1<<resamplePrecision))
			} else {
				kk[xx*ksize+x] = int32(0.5 + v*(1<<resamplePrecision))
			}
		}
		bounds[xx*2], bounds[xx*2+1] = xmin, xmax
	}
	return bounds, kk, ksize
}

// phashDistance is the smallest Hamming distance between any hash on the left
// and any on the right, where a value is one hash or up to three joined by ":".
// Nothing to compare, or no readable pair, is "infinitely" far.
func phashDistance(left, right string) int {
	l, r := phashParts(left), phashParts(right)
	best := math.MaxInt
	for _, a := range l {
		for _, b := range r {
			best = min(best, bits.OnesCount64(a^b))
		}
	}
	return best
}

func phashParts(s string) []uint64 {
	var out []uint64
	for _, part := range strings.Split(s, ":") {
		if part == "" {
			continue
		}
		if v, err := strconv.ParseUint(part, 16, 64); err == nil {
			out = append(out, v)
		}
	}
	return out
}
