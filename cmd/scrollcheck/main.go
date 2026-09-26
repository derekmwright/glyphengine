// Command scrollcheck asserts that a yamlui scroll_view clips what it holds to
// its own view rect: exactly up to the edge, and nothing past it.
//
//	task scroll
//
// This is issue #149's first arm. `scroll_view` culled children that were
// entirely outside its view and drew everything else in full, so a row
// straddling an edge painted over whatever the container was sitting on. The
// failure is invisible to everything else in the repository: `task smoke`
// renders the frame, `task validate` is silent, `task determinism` produces the
// same wrong image twice, and a list that happens to be scrolled to a row
// boundary looks perfect.
//
// So this reads pixels, in three captures of one scene that differ only in what
// the list was asked to do:
//
//	aligned   every row on a boundary; nothing straddles anything
//	straddle  the same list 20px down, so one row crosses each edge
//	control   the straddling frame with clipping switched off
//
// and three kinds of arm, which fail differently:
//
//   - -patch is a box and the colour its YAML asked for. Inside an edge it
//     says the row reaches the edge; outside one it says the background is
//     still there. Both are checked on the straddling capture, and a box must
//     be uniform as well as right -- a clip that trims geometry without moving
//     its UVs leaves a box that is the right colour on average and a smear in
//     detail.
//   - -same is a box outside every view rect that must be IDENTICAL between
//     the aligned and straddling captures. Scrolling a list is not allowed to
//     change a single pixel anywhere else on screen.
//   - -differs is a box that must change between the straddling capture and the
//     control. That is the arm that keeps the other two from being vacuous: a
//     gate that passes on a frame where nothing was clipped in the first place
//     is measuring the absence of content, not the presence of a clip.
//
// PROVED by breaking the real code and running `task scroll` on a staged tree:
// renderScrollView's `t.pushClip(viewRect)` replaced with `t.clip`, which is
// the widget exactly as it was before issue #149. What it printed, trimmed:
//
//	patch 500,230,200,8,0.85,0.86,0.88   mean (230.00 51.00 51.00)  off 173.40
//	patch 500,406,200,8,0.85,0.86,0.88   mean (178.00 64.00 204.00) off 155.30
//	patch 500,542,200,8,0.85,0.86,0.88   mean ( 76.00 242.00 140.00) off 140.75
//	patch 422,558,8,16,0.85,0.86,0.88    mean (242.00 217.00 51.00)  off 173.40
//	patch 810,558,8,16,0.85,0.86,0.88    mean (217.00 51.00 242.00)  off 168.30
//	same  432,202,376,24   1504 pixels differ, worst channel 204
//	same  432,228,376,12   4512 pixels differ, worst channel 173
//	same  420,556,10,20     200 pixels differ, worst channel 173
//	differs 432,230,376,8     0 pixels differ, worst channel 0
//	differs 432,406,376,8     0 pixels differ, worst channel 0
//	differs 432,542,376,8     0 pixels differ, worst channel 0
//	differs 422,558,8,16      0 pixels differ, worst channel 0
//	differs 810,558,8,16      0 pixels differ, worst channel 0
//
// Every arm fires, and the three say different things. The colour boxes say a
// row reached 140 to 173 of 255 past the edge it was supposed to stop at. The
// -same boxes say scrolling a list changed pixels in the band above it and in
// the margin beside the strip -- places no list is allowed to touch. And every
// -differs box went to zero, which is the control saying what it is for: with
// the clip gone, the clipped frame and the unclipped one are the same picture.
//
// Four boxes stayed green under that break and are worth knowing about: the
// ones just INSIDE an edge (246, 386, 492, 530 and the two side ones). A row
// that draws too much still draws the part it should, so those arms alone
// would have passed a widget with no clipping at all. They are there to catch
// the opposite failure -- a clip that ate the row -- and the boxes outside the
// edges are what catch this one.
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

// rgb is a colour as the YAML writes it: three floats in 0..1, sRGB. The UI
// pipeline decodes and the swapchain re-encodes, so the expected 8-bit value is
// simply v*255 -- the same arithmetic cmd/flatquadcheck relies on, and the same
// gate that proved it holds for these quads.
type rgb [3]float64

func (c rgb) bytes() [3]float64 { return [3]float64{c[0] * 255, c[1] * 255, c[2] * 255} }

type patch struct {
	rect image.Rectangle
	want rgb
	arg  string
}

type patches struct{ items []patch }

func (p *patches) String() string { return fmt.Sprint(len(p.items)) }

func (p *patches) Set(s string) error {
	parts := strings.Split(s, ",")
	if len(parts) != 7 {
		return fmt.Errorf("want x,y,w,h,r,g,b, got %q", s)
	}
	r, err := parseRect(parts[:4])
	if err != nil {
		return err
	}
	c, err := parseRGB(parts[4:])
	if err != nil {
		return err
	}
	p.items = append(p.items, patch{rect: r, want: c, arg: s})
	return nil
}

type boxes struct {
	items []image.Rectangle
	args  []string
}

func (b *boxes) String() string { return fmt.Sprint(len(b.items)) }

func (b *boxes) Set(s string) error {
	r, err := parseRect(strings.Split(s, ","))
	if err != nil {
		return err
	}
	b.items = append(b.items, r)
	b.args = append(b.args, s)
	return nil
}

func parseRect(parts []string) (image.Rectangle, error) {
	if len(parts) != 4 {
		return image.Rectangle{}, fmt.Errorf("want x,y,w,h, got %q", strings.Join(parts, ","))
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

func parseRGB(parts []string) (rgb, error) {
	var c rgb
	for i, p := range parts {
		f, err := strconv.ParseFloat(strings.TrimSpace(p), 64)
		if err != nil {
			return rgb{}, fmt.Errorf("%q is not a number", p)
		}
		if f < 0 || f > 1 {
			return rgb{}, fmt.Errorf("%q is outside 0..1", p)
		}
		c[i] = f
	}
	return c, nil
}

func main() {
	aligned := flag.String("aligned", "", "the capture with every row on a boundary")
	straddle := flag.String("straddle", "", "the same list scrolled so a row crosses each edge")
	control := flag.String("control", "", "the straddling capture with clipping switched off")

	var colour patches
	var same, differs boxes
	flag.Var(&colour, "patch", "x,y,w,h,r,g,b: a box of the straddling capture and the colour it must be (repeatable)")
	flag.Var(&same, "same", "x,y,w,h: a box outside every view that must be identical in -aligned and -straddle (repeatable)")
	flag.Var(&differs, "differs", "x,y,w,h: a box that must change between -straddle and -control (repeatable)")

	tol := flag.Float64("tol", 1.5, "allowed deviation from the stated colour, in 8-bit steps")
	sameTol := flag.Float64("sametol", 0, "allowed per-pixel difference in a -same box, in 8-bit steps")
	minDiff := flag.Float64("mindiff", 24, "a -differs box must move at least this far on its worst pixel")
	flag.Parse()

	if *aligned == "" || *straddle == "" || *control == "" {
		fmt.Fprintln(os.Stderr, "scrollcheck: -aligned, -straddle and -control are all required")
		os.Exit(2)
	}
	if len(colour.items) == 0 && len(same.items) == 0 && len(differs.items) == 0 {
		fmt.Fprintln(os.Stderr, "scrollcheck: at least one -patch, -same or -differs is required")
		os.Exit(2)
	}

	a := mustLoad(*aligned)
	b := mustLoad(*straddle)
	c := mustLoad(*control)
	status := 0

	for _, p := range colour.items {
		if !p.rect.In(b.Bounds()) {
			fmt.Fprintf(os.Stderr, "scrollcheck: -patch %s is outside the capture %v\n", p.arg, b.Bounds())
			os.Exit(2)
		}
		want := p.want.bytes()
		lo, hi, mean := span(b, p.rect)

		dev, spread := 0.0, 0.0
		for ch := 0; ch < 3; ch++ {
			dev = math.Max(dev, math.Abs(mean[ch]-want[ch]))
			spread = math.Max(spread, hi[ch]-lo[ch])
		}
		fmt.Printf("patch  %-34s mean (%6.2f %6.2f %6.2f)  want (%6.2f %6.2f %6.2f)  off %5.2f  spread %5.2f\n",
			p.arg, mean[0], mean[1], mean[2], want[0], want[1], want[2], dev, spread)

		if dev > *tol {
			fmt.Printf("  FAIL: this box is %.2f off the colour it must be, over its %.2f.\n", dev, *tol)
			fmt.Printf("  Inside an edge that means the row stops short of it; outside one it means\n")
			fmt.Printf("  the row reached past the clip and painted over the background.\n")
			status = 1
		}
		if spread > *tol {
			fmt.Printf("  FAIL: the box is not uniform: %.2f between its darkest and brightest pixel,\n", spread)
			fmt.Printf("  over its %.2f. A trim that cut the geometry and left the UVs behind reads\n", *tol)
			fmt.Printf("  like this -- right on average, a smear in detail.\n")
			status = 1
		}
	}

	for i, r := range same.items {
		n, worst := diff(a, b, r)
		fmt.Printf("same   %-34s %d pixels differ, worst channel %.0f\n", same.args[i], n, worst)
		if worst > *sameTol {
			fmt.Printf("  FAIL: scrolling the list changed %d pixels in a box that is outside every\n", n)
			fmt.Printf("  view rect, by up to %.0f of 255. A row is drawing past the container that\n", worst)
			fmt.Printf("  holds it.\n")
			status = 1
		}
	}

	for i, r := range differs.items {
		n, worst := diff(b, c, r)
		fmt.Printf("differs %-33s %d pixels differ, worst channel %.0f\n", differs.args[i], n, worst)
		if worst < *minDiff {
			fmt.Printf("  FAIL: this box is the same with clipping ON and OFF (worst %.0f, needs %.0f).\n", worst, *minDiff)
			fmt.Printf("  Nothing was being clipped here, so the arms above proved nothing: they were\n")
			fmt.Printf("  reading a frame that had no row near this edge. Check the offsets, the\n")
			fmt.Printf("  boxes, and that -scrollclip=off reaches the widget tree.\n")
			status = 1
		}
	}

	if status != 0 {
		os.Exit(status)
	}
	fmt.Println("every scroll_view clips its content to its view rect, exactly up to the edge and nowhere past it")
}

// span returns the per-channel minimum, maximum and mean over a box, in 8-bit
// units.
func span(img image.Image, r image.Rectangle) (lo, hi, mean [3]float64) {
	lo = [3]float64{255, 255, 255}
	var sum [3]float64
	n := 0.0
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			cr, cg, cb, _ := img.At(x, y).RGBA()
			for ch, v := range [3]uint32{cr, cg, cb} {
				f := float64(v >> 8)
				if f < lo[ch] {
					lo[ch] = f
				}
				if f > hi[ch] {
					hi[ch] = f
				}
				sum[ch] += f
			}
			n++
		}
	}
	for ch := range sum {
		mean[ch] = sum[ch] / n
	}
	return lo, hi, mean
}

// diff counts differing pixels in a box and the worst single-channel gap.
func diff(x, y image.Image, r image.Rectangle) (count int, worst float64) {
	if !r.In(x.Bounds()) || !r.In(y.Bounds()) {
		fmt.Fprintf(os.Stderr, "scrollcheck: box %v is outside a capture (%v, %v)\n", r, x.Bounds(), y.Bounds())
		os.Exit(2)
	}
	for py := r.Min.Y; py < r.Max.Y; py++ {
		for px := r.Min.X; px < r.Max.X; px++ {
			xr, xg, xb, _ := x.At(px, py).RGBA()
			yr, yg, yb, _ := y.At(px, py).RGBA()
			d := 0.0
			for _, pair := range [3][2]uint32{{xr, yr}, {xg, yg}, {xb, yb}} {
				if v := math.Abs(float64(pair[0]>>8) - float64(pair[1]>>8)); v > d {
					d = v
				}
			}
			if d > 0 {
				count++
			}
			if d > worst {
				worst = d
			}
		}
	}
	return count, worst
}

func mustLoad(path string) image.Image {
	f, err := os.Open(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "scrollcheck: %v\n", err)
		os.Exit(2)
	}
	defer f.Close()
	img, err := png.Decode(f)
	if err != nil {
		fmt.Fprintf(os.Stderr, "scrollcheck: decode %s: %v\n", path, err)
		os.Exit(2)
	}
	return img
}
