// Command flatquadcheck asserts that a flat yamlui quad reaches the colour its
// YAML asked for, rather than somewhere between that colour and whatever it was
// drawn over.
//
//	task flatquad
//
// This is issue #144's arm. `ui/yamlui`'s quad emitter wrote no UV, so all four
// vertices carried (0, 0); `shaders/ui.frag`'s edgeCoverage then took a
// distance of 0 and an fwidth clamped to 1e-8, px came out 0, and coverage
// landed on its `+ 0.5` clamp for every fragment of the quad. Every flat
// `bg_color` panel and every non-nine-slice progress bar was therefore
// composited at half alpha, edge to edge, on top of whatever `opacity` asked
// for -- not at the edges, where half alpha is what antialiasing means, but in
// the middle, where it is simply the wrong colour.
//
// Nothing else in the repository could see it. `task smoke` renders the frame,
// `task validate` is silent, `task determinism` is happy because the same
// half-alpha image comes out every time, `task indicator` subtracts two
// captures that are both wrong in the same way, and `task hud` measures text on
// a path these quads are not on. The failure is a picture of a panel that is
// too pale, which is indistinguishable from a panel someone chose to make pale.
//
// So this reads pixels. Each `-patch` is an interior box of one flat quad plus
// the colour the YAML asked for, and each is paired with an `-over`: the colour
// underneath it. Three things have to hold, and they fail differently:
//
//   - the box is within -tol 8-bit steps of the requested colour. That is the
//     whole statement, and half alpha misses it by roughly half the distance to
//     the background.
//   - the box is uniform to within -tol. A quad whose interior ramps is a
//     coverage bug of a different shape -- a UV that spans the wrong extent, or
//     a skirt applied to the value rather than to the rect.
//   - the requested colour and the one underneath it are at least -minsep
//     apart. Without that a patch drawn over its own colour passes at any
//     coverage whatsoever and proves nothing.
//
// The midpoint is printed next to every box, computed in linear light the way
// the hardware blends onto an sRGB swapchain, so a failure says which bug it is
// rather than only that the number moved.
//
// PROVED by restoring the zero-UV emitter in ui/yamlui/tree.go and running
// `task flatquad` on a staged tree. Fixed, and broken:
//
//	box                     fixed mean            broken mean       requested
//	panel over the root     (13.00 15.00 20.00)   (147.33 157.89 172.44)  (12.75 15.30 20.40)
//	the bar's fill          (242.00 217.00 51.00) (203.43 188.21 122.21)  (242.25 216.75 51.00)
//	the bar's background    (51.00 15.00 15.00)   (151.30 149.06 155.17)  (51.00 15.30 15.30)
//
// Fixed, the worst box is 0.40 of 255 from the colour its YAML asked for --
// rounding, and nothing else -- with a spread of 0.00 across every interior.
// Broken, they are 152.04, 71.21 and 139.87 away from it, and the two drawn
// straight onto the root land 11.94 and 12.21 from the half-alpha midpoint.
//
// The fill is the one that is not on its midpoint (84.64 from it), and that is
// the bug compounding rather than a second bug: under the break the bar's
// background is itself half alpha, so the fill is averaged with the root and
// the scene rather than with the colour the bar asked for. The interiors also
// stopped being uniform -- 4, 34 and 40 steps of spread -- because at half
// alpha the 3D scene shows through every one of them.
//
// The vacuity arms in the Taskfile were checked the same way and both reject:
// a patch handed the background's colour, and a patch declared to be drawn over
// its own colour.
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

// rgb is a colour as the YAML writes it: three floats in 0..1, sRGB, which is
// what ui.frag decodes and the swapchain re-encodes. What you write is what
// reaches the display, so the expected 8-bit value is simply v*255.
type rgb [3]float64

func (c rgb) bytes() [3]float64 {
	return [3]float64{c[0] * 255, c[1] * 255, c[2] * 255}
}

// patch is an interior box of one flat quad and the colour it asked for.
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
	var v [4]int
	for i := 0; i < 4; i++ {
		n, err := strconv.Atoi(strings.TrimSpace(parts[i]))
		if err != nil {
			return fmt.Errorf("%q is not a number", parts[i])
		}
		v[i] = n
	}
	if v[2] <= 0 || v[3] <= 0 {
		return fmt.Errorf("width and height must be positive, got %dx%d", v[2], v[3])
	}
	c, err := parseRGB(parts[4:])
	if err != nil {
		return err
	}
	p.items = append(p.items, patch{
		rect: image.Rect(v[0], v[1], v[0]+v[2], v[1]+v[3]),
		want: c,
		arg:  s,
	})
	return nil
}

type colors struct{ items []rgb }

func (c *colors) String() string { return fmt.Sprint(len(c.items)) }

func (c *colors) Set(s string) error {
	v, err := parseRGB(strings.Split(s, ","))
	if err != nil {
		return err
	}
	c.items = append(c.items, v)
	return nil
}

func parseRGB(parts []string) (rgb, error) {
	if len(parts) != 3 {
		return rgb{}, fmt.Errorf("want r,g,b, got %q", strings.Join(parts, ","))
	}
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
	shot := flag.String("shot", "", "capture to read")

	var boxes patches
	var over colors
	flag.Var(&boxes, "patch", "x,y,w,h,r,g,b: an interior box of a flat quad and the colour its YAML asked for (repeatable)")
	flag.Var(&over, "over", "r,g,b: the colour that patch is drawn over; one per -patch")

	tol := flag.Float64("tol", 1, "allowed deviation from the requested colour, in 8-bit steps")
	minSep := flag.Float64("minsep", 32, "the requested colour and the one under it must differ by at least this, in 8-bit steps")
	flag.Parse()

	if *shot == "" || len(boxes.items) == 0 {
		fmt.Fprintln(os.Stderr, "flatquadcheck: -shot and at least one -patch are required")
		os.Exit(2)
	}
	if len(over.items) != len(boxes.items) {
		fmt.Fprintln(os.Stderr, "flatquadcheck: every -patch needs one -over")
		os.Exit(2)
	}

	img := mustLoad(*shot)
	status := 0

	for i, p := range boxes.items {
		if !p.rect.In(img.Bounds()) {
			fmt.Fprintf(os.Stderr, "flatquadcheck: -patch %s is outside the capture %v\n", p.arg, img.Bounds())
			os.Exit(2)
		}
		bg := over.items[i]
		want := p.want.bytes()
		mid := midpoint(p.want, bg).bytes()
		lo, hi, mean := span(img, p.rect)

		// How far the requested colour is from what it sits on. A box whose
		// two colours are the same cannot tell full coverage from half.
		sep := 0.0
		for ch := 0; ch < 3; ch++ {
			if d := math.Abs(want[ch] - bg.bytes()[ch]); d > sep {
				sep = d
			}
		}

		devWant, devMid, spread := 0.0, 0.0, 0.0
		for ch := 0; ch < 3; ch++ {
			devWant = math.Max(devWant, math.Abs(mean[ch]-want[ch]))
			devMid = math.Max(devMid, math.Abs(mean[ch]-mid[ch]))
			spread = math.Max(spread, hi[ch]-lo[ch])
		}

		fmt.Printf("patch  %-28s mean (%6.2f %6.2f %6.2f)  want (%6.2f %6.2f %6.2f)  off %5.2f  spread %5.2f\n",
			p.arg, mean[0], mean[1], mean[2], want[0], want[1], want[2], devWant, spread)
		fmt.Printf("       half alpha over (%4.2f %4.2f %4.2f) would read (%6.2f %6.2f %6.2f), which is %5.2f away\n",
			bg[0], bg[1], bg[2], mid[0], mid[1], mid[2], devMid)

		if sep < *minSep {
			fmt.Printf("  FAIL: the requested colour and the one under it are only %.1f apart, under %.1f --\n", sep, *minSep)
			fmt.Printf("  full coverage and half coverage would read the same here, so this box proves\n")
			fmt.Printf("  nothing. Pick a patch whose background is visibly different.\n")
			status = 1
			continue
		}
		if devWant > *tol {
			fmt.Printf("  FAIL: the interior is %.2f off the colour the YAML asked for, over its %.2f.\n", devWant, *tol)
			if devMid < devWant {
				fmt.Printf("  It is nearer the half-alpha midpoint (%.2f away) than the requested colour.\n", devMid)
				fmt.Printf("  That is issue #144: the quad emitter wrote no UV, so ui.frag's edgeCoverage\n")
				fmt.Printf("  saw d = 0 and fwidth clamped to 1e-8 and returned its 0.5 clamp everywhere.\n")
				fmt.Printf("  Check appendQuad in ui/yamlui/tree.go against ui.AppendQuad.\n")
			} else {
				fmt.Printf("  It is not the half-alpha midpoint either (%.2f away from that), so this is\n", devMid)
				fmt.Printf("  a colour going somewhere else -- an opacity, a tint, or the sRGB decode.\n")
			}
			status = 1
		}
		if spread > *tol {
			fmt.Printf("  FAIL: the interior is not uniform: %.2f between its darkest and brightest\n", spread)
			fmt.Printf("  pixel, over its %.2f. The coverage ramp is reaching inside the quad -- a UV\n", *tol)
			fmt.Printf("  that spans the wrong extent, or a skirt sized against the wrong rect.\n")
			status = 1
		}
	}

	if status != 0 {
		os.Exit(status)
	}
	fmt.Println("every flat quad reaches the colour its YAML asked for, uniformly, over a background it is not being averaged with")
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

// midpoint is what half coverage of fg over bg produces: the blend happens in
// linear light, because the swapchain is an _SRGB format and the hardware
// decodes, blends and re-encodes around it. Averaging the encoded values
// instead would put this several steps off and make the diagnosis wrong in the
// dark end, where the curve is steepest.
func midpoint(fg, bg rgb) rgb {
	var out rgb
	for ch := 0; ch < 3; ch++ {
		out[ch] = linearToSrgb(0.5*srgbToLinear(fg[ch]) + 0.5*srgbToLinear(bg[ch]))
	}
	return out
}

func srgbToLinear(c float64) float64 {
	if c <= 0.04045 {
		return c / 12.92
	}
	return math.Pow((c+0.055)/1.055, 2.4)
}

func linearToSrgb(c float64) float64 {
	if c <= 0.0031308 {
		return c * 12.92
	}
	return 1.055*math.Pow(c, 1/2.4) - 0.055
}

func mustLoad(path string) image.Image {
	f, err := os.Open(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "flatquadcheck: %v\n", err)
		os.Exit(2)
	}
	defer f.Close()
	img, err := png.Decode(f)
	if err != nil {
		fmt.Fprintf(os.Stderr, "flatquadcheck: decode %s: %v\n", path, err)
		os.Exit(2)
	}
	return img
}
