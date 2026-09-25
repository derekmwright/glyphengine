// Command indicatorcheck asserts that a yamlui `indicator:` block darkens the
// part of a widget it says it covers, leaves everything else alone, stays under
// the label that names the widget, and disappears completely when its value
// reaches zero.
//
//	task indicator
//
// The block draws a cooldown sweep, a wipe or a tint over a widget from YAML a
// game supplies, which means every one of its failures is a picture rather than
// an error:
//
//	the covered region is the wrong one   -- a sweep that unwinds anticlockwise,
//	                                         a wipe anchored to the wrong edge.
//	                                         Both render a perfectly plausible
//	                                         still frame.
//	the overlay escapes the widget        -- a fan built around the wrong centre,
//	                                         a rect that is the parent's. Reads
//	                                         as "the HUD got darker".
//	the overlay swallows the label        -- drawn after the text rather than
//	                                         before it, which is the one thing
//	                                         the issue's draw order is about.
//	a finished cooldown leaves a residue  -- an empty fan, a zero-area quad, a
//	                                         tint at alpha 0 that still writes.
//	                                         Invisible until someone looks at a
//	                                         slot that should be clean.
//
// None of that is caught anywhere else: `task smoke` renders the frame,
// `task validate` is silent, `task determinism` is happy because the same wrong
// image comes out every time, and `task hud` measures text over water on a path
// this feature is not on.
//
// So this takes four captures of the same scene at the same fixed clock, of a
// grid of twelve icons -- three indicator types by four values, the fourth of
// which has no indicator at all:
//
//	ind        the grid with its indicators, labels drawn
//	indnotext  the same grid, labels suppressed
//	plain      the same grid from a YAML with no indicator blocks, labels drawn
//	zero       the indicator YAML again with every value bound to zero
//
// ind against plain is the OVERLAY, over identical geometry at the same frame,
// so every box below subtracts the scene out instead of thresholding it:
//
//   - dark is a region the indicator says it covers. It fails when the geometry
//     is the wrong shape, the wrong size or on the wrong side.
//   - keep is a region of the SAME icon the indicator says it does not cover.
//     It is the half that a "make everything darker" bug passes dark on: zero
//     pixels may differ there, because under a fixed clock two captures of the
//     same build are byte identical and any difference at all is the overlay.
//   - outside is the whole frame beyond the HUD's own rect, which must not move
//     at all. It fails when the fan is built around the wrong centre or the
//     wipe against the wrong rect.
//
// ind against indnotext is the LABEL, recovered the way `task hud` recovers it:
// opaque white text over a known background inverts exactly, so the glyph
// coverage that survived is a number that does not depend on what was behind
// it. The label over a covered icon has to carry the same ink as the label over
// the icon with no indicator, or the overlay is being drawn on top of the text.
//
// zero against plain is the one arm with no tolerance at all: a value of zero
// has to produce the same bytes as a YAML with the block deleted.
package main

import (
	"flag"
	"fmt"
	"image"
	"image/png"
	"math"
	"os"
	"strconv"
	"strings"
)

// boxes collects a repeatable x,y,w,h flag.
type boxes struct {
	rects []image.Rectangle
	args  []string
}

func (b *boxes) String() string { return strings.Join(b.args, " ") }

func (b *boxes) Set(s string) error {
	r, err := parseBox(s)
	if err != nil {
		return err
	}
	b.rects = append(b.rects, r)
	b.args = append(b.args, s)
	return nil
}

func main() {
	ind := flag.String("ind", "", "capture of the grid with its indicators")
	indNoText := flag.String("indnotext", "", "the same capture with the labels suppressed")
	plain := flag.String("plain", "", "the same grid from a YAML with no indicator blocks")
	zero := flag.String("zero", "", "the indicator YAML with every value bound to zero")

	var dark, keep, label, labelBase boxes
	flag.Var(&dark, "dark", "x,y,w,h of a region the indicator covers (repeatable)")
	flag.Var(&keep, "keep", "x,y,w,h of a region of the same widget it does not (repeatable)")
	flag.Var(&label, "label", "x,y,w,h around a label drawn over an indicator (repeatable)")
	flag.Var(&labelBase, "labelbase", "x,y,w,h around the same label with no indicator under it (repeatable)")

	outside := flag.String("outside", "", "x,y,w,h of the HUD; nothing beyond it may change")

	minDark := flag.Float64("mindark", 10, "a covered region must lose at least this much mean luma")
	minBright := flag.Float64("minbright", 60, "a covered region must be this bright BEFORE the indicator")
	minInk := flag.Float64("minink", 50, "a label box must carry at least this much recovered ink")
	inkTol := flag.Float64("inktol", 0.2, "allowed fractional deviation from the uncovered label's ink")
	flag.Parse()

	if *ind == "" || *plain == "" {
		fmt.Fprintln(os.Stderr, "indicatorcheck: -ind and -plain are required")
		os.Exit(2)
	}
	indImg := mustLoad(*ind)
	plainImg := mustLoad(*plain)
	mustMatch(indImg, plainImg, *ind, *plain)

	status := 0

	// The coarsest statement there is, and one no box can make: the two YAMLs
	// differ ONLY by the indicator blocks, so if their captures come out byte
	// identical then nothing is being drawn and every number below is about an
	// empty overlay. Not written as a self-check, because a harness that
	// rendered the same file twice and a renderer that draws no indicator
	// produce the same picture and both are failures of the thing under test.
	if same, _, _ := compare(indImg, plainImg); same == 0 {
		fmt.Println("FAIL: the indicator and plain captures are byte identical.")
		fmt.Println("  Nothing is being drawn. Check that the widget tree still has a ShapeFn,")
		fmt.Println("  that the values are bound above zero, and that renderIndicator is reached")
		fmt.Println("  from the widget the block is on.")
		status = 1
	}

	// -- the covered regions --
	for i, r := range dark.rects {
		mustContain(indImg, r, "dark", dark.args[i])
		added, base := gain(indImg, plainImg, r)
		fmt.Printf("dark   %-16s %+7.2f mean luma (background %6.2f, floor %.2f)\n",
			dark.args[i], added, base, -*minDark)
		if base < *minBright {
			fmt.Printf("  FAIL: the box sits at %.1f before the indicator, under %.1f -- the widget it\n", base, *minBright)
			fmt.Printf("  is supposed to measure is not in the capture, so a darkening of nothing\n")
			fmt.Printf("  would pass and this box proves nothing\n")
			status = 1
		}
		if added > -*minDark {
			fmt.Printf("  FAIL: the covered region lost %.2f, under its floor of %.2f\n", -added, *minDark)
			fmt.Printf("  The geometry is the wrong shape, the wrong size or on the wrong side of\n")
			fmt.Printf("  the widget -- check the direction, the start angle and which of cover and\n")
			fmt.Printf("  1-cover the fill selects\n")
			status = 1
		}
	}

	// -- the regions of the same widget it does not cover --
	for i, r := range keep.rects {
		mustContain(indImg, r, "keep", keep.args[i])
		moved := changed(indImg, plainImg, r)
		fmt.Printf("keep   %-16s %6d px changed (ceiling 0)\n", keep.args[i], moved)
		if moved > 0 {
			fmt.Printf("  FAIL: the indicator reached a part of the widget it does not cover.\n")
			fmt.Printf("  This is the arm a \"darken the whole thing\" bug fails: the covered boxes\n")
			fmt.Printf("  pass comfortably and the frame just looks like a dimmer HUD\n")
			status = 1
		}
	}

	// -- and nothing beyond the HUD at all --
	if *outside != "" {
		r, err := parseBox(*outside)
		if err != nil {
			fmt.Fprintf(os.Stderr, "indicatorcheck: -outside: %v\n", err)
			os.Exit(2)
		}
		moved := changedOutside(indImg, plainImg, r)
		fmt.Printf("outside %-15s %6d px changed (ceiling 0)\n", *outside, moved)
		if moved > 0 {
			fmt.Printf("  FAIL: the indicator changed the frame outside the HUD entirely -- a fan\n")
			fmt.Printf("  built around the wrong centre, or a wipe measured against the parent's\n")
			fmt.Printf("  rect rather than the widget's\n")
			status = 1
		}
	}

	// -- the label over it --
	if len(label.rects) > 0 {
		if *indNoText == "" {
			fmt.Fprintln(os.Stderr, "indicatorcheck: -label needs -indnotext to difference against")
			os.Exit(2)
		}
		noText := mustLoad(*indNoText)
		mustMatch(indImg, noText, *ind, *indNoText)
		if len(labelBase.rects) != len(label.rects) {
			fmt.Fprintln(os.Stderr, "indicatorcheck: every -label needs one -labelbase")
			os.Exit(2)
		}
		for i, r := range label.rects {
			mustContain(indImg, r, "label", label.args[i])
			mustContain(indImg, labelBase.rects[i], "labelbase", labelBase.args[i])
			over := bandInk(indImg, noText, r)
			bare := bandInk(indImg, noText, labelBase.rects[i])
			frac := 0.0
			if bare > 0 {
				frac = over / bare
			}
			fmt.Printf("label  %-16s ink %8.1f px, %5.1f%% of the uncovered %8.1f px\n",
				label.args[i], over, frac*100, bare)
			if bare < *minInk {
				fmt.Printf("  FAIL: the uncovered label carries %.1f px of ink, under %.1f -- there is no\n", bare, *minInk)
				fmt.Printf("  text in the capture to compare against and this arm proves nothing\n")
				status = 1
			} else if math.Abs(frac-1) > *inkTol {
				fmt.Printf("  FAIL: the label over the indicator kept %.1f%% of the ink the same label\n", frac*100)
				fmt.Printf("  keeps with nothing under it. The overlay is being composited on top of\n")
				fmt.Printf("  the text instead of under it -- the draw order inside a widget is\n")
				fmt.Printf("  widget, sprite, indicator, text\n")
				status = 1
			}
		}
	}

	// -- and a value of zero leaves nothing at all --
	if *zero != "" {
		zeroImg := mustLoad(*zero)
		mustMatch(indImg, zeroImg, *ind, *zero)
		differing, over1, max := compare(zeroImg, plainImg)
		fmt.Printf("zero vs plain: %d px differ, %d of them above 1/255, max delta %d (tolerance 0)\n",
			differing, over1, max)
		if differing != 0 {
			fmt.Printf("  FAIL: a value of zero did not leave the widget the way a YAML with no\n")
			fmt.Printf("  indicator block does. An empty fan, a zero-area quad or a tint still\n")
			fmt.Printf("  writing at alpha 0 -- check the cover <= 0 and alpha <= 0 exits\n")
			status = 1
		}
	}

	if status != 0 {
		os.Exit(status)
	}
	fmt.Println("the indicator darkens what it covers, moves nothing else, stays under its label, and vanishes at zero")
}

// gain returns the mean luma the indicator added over a region -- negative when
// it darkened it -- and the mean luma of the region without it, so a reader can
// see whether a small change is a small change to something bright.
func gain(withInd, without image.Image, r image.Rectangle) (added, base float64) {
	var sumA, sumB float64
	n := 0
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			sumA += luma(withInd, x, y)
			sumB += luma(without, x, y)
			n++
		}
	}
	if n == 0 {
		return 0, 0
	}
	return (sumA - sumB) / float64(n), sumB / float64(n)
}

// changed counts pixels that differ at all inside a region.
//
// The threshold is one 8-bit step because there is no noise to allow for: both
// captures are the same build at the same fixed frame clock, and
// `task determinism` is the gate that says two such runs are byte identical.
func changed(a, b image.Image, r image.Rectangle) int {
	count := 0
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			if differs(a, b, x, y) {
				count++
			}
		}
	}
	return count
}

// changedOutside counts pixels that differ anywhere except inside r.
func changedOutside(a, b image.Image, r image.Rectangle) int {
	bounds := a.Bounds()
	count := 0
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			if image.Pt(x, y).In(r) {
				continue
			}
			if differs(a, b, x, y) {
				count++
			}
		}
	}
	return count
}

func differs(a, b image.Image, x, y int) bool {
	ar, ag, ab, _ := a.At(x, y).RGBA()
	br, bg, bb, _ := b.At(x, y).RGBA()
	return ar != br || ag != bg || ab != bb
}

// compare returns how many pixels differ at all, how many differ by more than
// one 8-bit step, and the largest single-channel difference.
func compare(a, b image.Image) (differing, over1, max int) {
	r := a.Bounds()
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			ar, ag, ab, _ := a.At(x, y).RGBA()
			br, bg, bb, _ := b.At(x, y).RGBA()
			d := 0
			for _, p := range [][2]uint32{{ar, br}, {ag, bg}, {ab, bb}} {
				v := int(p[0]>>8) - int(p[1]>>8)
				if v < 0 {
					v = -v
				}
				if v > d {
					d = v
				}
			}
			if d > 0 {
				differing++
				if d > 1 {
					over1++
				}
				if d > max {
					max = d
				}
			}
		}
	}
	return differing, over1, max
}

// bandInk sums the glyph coverage recovered over a region, exactly as
// cmd/hudcheck does: opaque white text over a known background inverts, so the
// result is the coverage the rasterizer produced and not a function of what was
// behind it. That is the whole reason it can compare a label over a black
// overlay with the same label over a bright icon.
func bandInk(withText, without image.Image, r image.Rectangle) float64 {
	total := 0.0
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			total += alphaAt(withText, without, x, y)
		}
	}
	return total
}

// alphaAt inverts composite = a + (1-a)*background, in linear light, per
// channel, dropping any channel whose background is already near white and
// whose denominator has therefore vanished.
func alphaAt(withText, without image.Image, x, y int) float64 {
	cr, cg, cb, _ := withText.At(x, y).RGBA()
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
		// Opaque white text cannot make a pixel darker; a negative value is
		// the scene having moved, not coverage.
		return 0
	}
	if a > 1 {
		return 1
	}
	return a
}

// luma is Rec. 601 luminance, the same measure uiglowcheck, shaftcheck and
// skycheck use.
func luma(img image.Image, x, y int) float64 {
	r, g, b, _ := img.At(x, y).RGBA()
	return 0.299*float64(r>>8) + 0.587*float64(g>>8) + 0.114*float64(b>>8)
}

// srgbToLinear undoes the display encode. The blend bandInk inverts happened in
// linear light; comparing encoded values would bend every estimate by the curve.
func srgbToLinear(c float64) float64 {
	if c <= 0.04045 {
		return c / 12.92
	}
	return math.Pow((c+0.055)/1.055, 2.4)
}

func parseBox(s string) (image.Rectangle, error) {
	parts := strings.Split(s, ",")
	if len(parts) != 4 {
		return image.Rectangle{}, fmt.Errorf("want x,y,w,h, got %q", s)
	}
	var v [4]int
	for i, p := range parts {
		n, err := strconv.Atoi(strings.TrimSpace(p))
		if err != nil {
			return image.Rectangle{}, fmt.Errorf("%q is not a number", p)
		}
		v[i] = n
	}
	if v[2] <= 0 || v[3] <= 0 {
		return image.Rectangle{}, fmt.Errorf("width and height must be positive, got %dx%d", v[2], v[3])
	}
	return image.Rect(v[0], v[1], v[0]+v[2], v[1]+v[3]), nil
}

func mustContain(img image.Image, r image.Rectangle, name, arg string) {
	if !r.In(img.Bounds()) {
		fmt.Fprintf(os.Stderr, "indicatorcheck: -%s %s is outside the capture %v\n", name, arg, img.Bounds())
		os.Exit(2)
	}
}

func mustMatch(a, b image.Image, pathA, pathB string) {
	if a.Bounds() != b.Bounds() {
		fmt.Fprintf(os.Stderr, "indicatorcheck: %s and %s differ in size: %v vs %v\n",
			pathA, pathB, a.Bounds(), b.Bounds())
		os.Exit(2)
	}
}

func mustLoad(path string) image.Image {
	f, err := os.Open(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "indicatorcheck: %v\n", err)
		os.Exit(2)
	}
	defer f.Close()
	img, err := png.Decode(f)
	if err != nil {
		fmt.Fprintf(os.Stderr, "indicatorcheck: decode %s: %v\n", path, err)
		os.Exit(2)
	}
	return img
}
