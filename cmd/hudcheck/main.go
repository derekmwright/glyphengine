// Command hudcheck measures how much of a HUD actually reached the screen.
//
// It takes two captures of the same scene at the same fixed frame clock — one
// with a block of identical HUD lines drawn over it, one without — and reports,
// per line, how much text ink survived to the presented image.
//
//	task hud
//
// The two captures are what make this work. A single frame cannot be measured
// directly: white text over bright sky and the same white text over dark water
// produce completely different pixel values, so any threshold tuned for one
// reads the other as missing. Differencing against the same frame without the
// HUD cancels the background out, and because the text is opaque white the
// blend can be inverted exactly:
//
//	composite = alpha + (1 - alpha) * background      (in linear light)
//	alpha     = (composite - background) / (1 - background)
//
// The recovered alpha is the glyph coverage the rasterizer produced, which does
// not depend on what was behind it. Summed over a line's rows that is "ink": a
// number that is the same for every line of the same string no matter where on
// screen it lands. Lines that disagree are lines the renderer ate.
//
// The blend runs in linear space in both the cases this has to compare — the
// HDR scene target is linear, and Vulkan blending against an sRGB attachment
// decodes, blends and re-encodes — so both captures are decoded to linear
// before the inversion.
package main

import (
	"flag"
	"fmt"
	"image"
	"image/png"
	"math"
	"os"
)

func main() {
	a := flag.String("a", "", "capture WITH the HUD")
	b := flag.String("b", "", "capture WITHOUT the HUD, same scene and clock")
	lines := flag.Int("lines", 26, "number of HUD lines drawn")

	// Defaults mirror Engine.Debugf's layout: first baseline at 18, 24 apart.
	y0 := flag.Int("y0", 18, "top of the first line, in pixels")
	spacing := flag.Int("spacing", 24, "pixels between line tops")
	height := flag.Int("height", 24, "rows to measure per line")
	x0 := flag.Int("x0", 8, "left edge of the measured band")
	x1 := flag.Int("x1", 600, "right edge of the measured band")

	// A line that lost a fifth of its ink has lost whole glyphs; anti-aliasing
	// against different backgrounds moves this by well under a percent.
	tol := flag.Float64("tol", 0.2, "allowed fractional deviation from the median line")
	flag.Parse()

	if *a == "" || *b == "" {
		fmt.Fprintln(os.Stderr, "hudcheck: -a and -b are required")
		os.Exit(2)
	}

	withHUD, err := load(*a)
	if err != nil {
		fmt.Fprintf(os.Stderr, "hudcheck: %v\n", err)
		os.Exit(2)
	}
	without, err := load(*b)
	if err != nil {
		fmt.Fprintf(os.Stderr, "hudcheck: %v\n", err)
		os.Exit(2)
	}
	if withHUD.Bounds() != without.Bounds() {
		fmt.Fprintf(os.Stderr, "hudcheck: captures differ in size: %v vs %v\n", withHUD.Bounds(), without.Bounds())
		os.Exit(2)
	}

	ink := make([]float64, *lines)
	for i := range ink {
		top := *y0 + i**spacing
		ink[i] = bandInk(withHUD, without, *x0, *x1, top, top+*height)
	}

	med := median(ink)

	// A run where nothing was drawn would report every line equal at zero and
	// pass, which is exactly the shape of green result this repo has shipped
	// before. The median has to be real ink before the comparison means
	// anything.
	if med < 50 {
		fmt.Printf("median ink %.1f px — the HUD is not in the capture at all; this check proves nothing\n", med)
		os.Exit(1)
	}

	fail := false
	for i, v := range ink {
		frac := v / med
		mark := "ok"
		if math.Abs(frac-1) > *tol {
			mark = "LOST"
			fail = true
		}
		fmt.Printf("line %02d  ink %8.1f px  %5.1f%% of median  %s\n", i, v, frac*100, mark)
	}

	// Outside its own rows the HUD must leave the frame alone. Before overlays
	// moved past the tonemap this was the other half of the bug: the text sat
	// in the image water refracts through, so it warped the lake as well as
	// vanishing from it.
	bleed := outsideBleed(withHUD, without, *x0, *x1, *y0, *y0+(*lines-1)**spacing+*height)
	fmt.Printf("\nbleed outside the HUD rows: %d px changed\n", bleed)
	if bleed > 0 {
		fmt.Println("  the HUD is perturbing the scene around it")
		fail = true
	}

	fmt.Printf("median ink %.1f px, tolerance %.0f%%\n", med, *tol*100)
	if fail {
		os.Exit(1)
	}
	fmt.Println("HUD legibility: every line intact")
}

func load(path string) (image.Image, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	img, err := png.Decode(f)
	if err != nil {
		return nil, fmt.Errorf("decode %s: %w", path, err)
	}
	return img, nil
}

// bandInk sums recovered glyph coverage over one line's rows.
func bandInk(withHUD, without image.Image, x0, x1, y0, y1 int) float64 {
	r := withHUD.Bounds()
	total := 0.0
	for y := max(y0, r.Min.Y); y < min(y1, r.Max.Y); y++ {
		for x := max(x0, r.Min.X); x < min(x1, r.Max.X); x++ {
			total += alphaAt(withHUD, without, x, y)
		}
	}
	return total
}

// alphaAt inverts the blend at one pixel to recover the coverage the glyph
// rasterizer produced there.
//
// Each channel gives its own estimate and they should agree, so the mean of the
// usable ones is taken. A channel whose background is already near white gives
// (c-b)/(1-b) with a vanishing denominator — the sky's blue channel is the one
// that does this — so it is dropped rather than allowed to dominate.
func alphaAt(withHUD, without image.Image, x, y int) float64 {
	cr, cg, cb, _ := withHUD.At(x, y).RGBA()
	br, bg, bb, _ := without.At(x, y).RGBA()

	sum, n := 0.0, 0
	for _, ch := range [][2]uint32{{cr, br}, {cg, bg}, {cb, bb}} {
		c := srgbToLinear(float64(ch[0]) / 65535)
		b := srgbToLinear(float64(ch[1]) / 65535)
		if 1-b < 0.05 {
			continue
		}
		sum += (c - b) / (1 - b)
		n++
	}
	if n == 0 {
		return 0
	}
	a := sum / float64(n)
	if a < 0 {
		// Negative means the HUD made the pixel darker, which opaque white text
		// cannot do. It is the scene having moved, not coverage.
		return 0
	}
	if a > 1 {
		return 1
	}
	return a
}

// outsideBleed counts pixels the HUD changed that are not part of it: anything
// outside the horizontal band, and anything above or below the block of lines.
//
// The threshold is one 8-bit step. Under a fixed frame clock two runs of the
// same build are byte-identical — `task determinism` is the gate for that — so
// any difference at all here is the HUD leaking, not noise.
func outsideBleed(withHUD, without image.Image, x0, x1, y0, y1 int) int {
	r := withHUD.Bounds()
	count := 0
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			if y >= y0 && y < y1 && x >= x0 && x < x1 {
				continue
			}
			cr, cg, cb, _ := withHUD.At(x, y).RGBA()
			br, bg, bb, _ := without.At(x, y).RGBA()
			if cr != br || cg != bg || cb != bb {
				count++
			}
		}
	}
	return count
}

// srgbToLinear undoes the display encode. The blend this inverts happened in
// linear light; comparing encoded values instead would bend every coverage
// estimate by the curve.
func srgbToLinear(c float64) float64 {
	if c <= 0.04045 {
		return c / 12.92
	}
	return math.Pow((c+0.055)/1.055, 2.4)
}

func median(v []float64) float64 {
	if len(v) == 0 {
		return 0
	}
	s := append([]float64(nil), v...)
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
	return s[len(s)/2]
}
