// Command watercheck asserts that a lamp beside a lake reaches the water, in
// the lamp's own colour, and that water out of every lamp's range does not
// move at all.
//
//	task waterlight
//
// It takes two captures of the same scene at the same fixed clock — one with
// the lights and one with `09-water -lampsoff`, which builds every pile and
// fixture and hands the scene nothing — and two boxes:
//
//   - lit is water carrying a reflection. What the lamps ADD there must be
//     warm: red above green above blue, by a margin.
//   - dark is water out of every lamp's range. It must be byte-identical.
//
// The pair is the whole instrument, the same way it is in `task waterblend`.
// A single frame cannot say what should have been there, and a lake is bright
// enough in places that a brightness floor would pass on the lake. The same
// water is under both captures and subtracts out, so what is left is the
// lamps.
//
// Why the colour test rather than just brightness: water.frag could light the
// surface and still get this wrong in the two ways that matter. A diffuse body
// term multiplies the lamp by the lake's cyan albedo, so what arrives reads
// green-above-red however warm the bulb is; and the night grade's scotopic
// blend is provably one-sided, so a surface that does not report its local
// share comes back blue-shifted whatever lit it. Both show up here as the
// ordering failing, and neither shows up in a mean luma.
//
// Why the dark box is byte-identical rather than merely dim: lightIrradiance
// returns exactly +0.0 outside a light's range, and +0.0 changes no sum. So
// "out of range" has an exact meaning here, and a tolerance would hide the
// failure that matters — a term added unconditionally, outside the range test,
// which lifts the whole lake by an amount no threshold on a night scene would
// catch. It also catches the other direction: a change that was supposed to
// touch only lamplit water and quietly touched all of it.
//
// PROVED not vacuous, on `task waterlight`'s own boxes. Take the local light
// out of water.frag entirely — delete `color += lampGlint * glintGain` and
// force lightLocalLum to 0, which is the shader's state before issue #39 was
// fixed — and the lamp box goes from R +38.56 G +32.48 B +26.52 to
// R +0.00 G +0.00 B +0.00, the spot box from R +105.43 G +78.34 B +52.36 to
// R +0.02 G +0.02 B +0.02.
//
// And proved to catch the half that is not brightness: leave lightLocalLum set
// while deleting only the colour, so the surface is excused a night grade it
// has not earned, and the lamp box comes back R -3.50 G +1.06 B +6.20 and the
// spot box R -4.36 G +2.02 B +8.67 — turning the lights on makes the water
// bluer and takes red away. The ordering test reports that; a mean or a luma
// floor would call it a change and pass.
//
// The dark box is byte-identical in all three builds, which is what it is for.
// The full ablation leaves almost no residue in the lit boxes because they sit
// on deep water, where `absorbed` is 1 and the refracted bed does not show
// through; a box in the shallows would keep whatever the opaque pass put on
// the bed there, and -min would have to clear it.
package main

import (
	"flag"
	"fmt"
	"image"
	"image/png"
	"os"
	"strconv"
	"strings"
)

func main() {
	a := flag.String("a", "", "capture WITH the lights")
	b := flag.String("b", "", "capture WITHOUT them, same scene and clock")
	litBox := flag.String("lit", "", "x,y,w,h of water carrying a reflection")
	darkBox := flag.String("dark", "", "x,y,w,h of water out of every lamp's range")

	// The floor that stops this passing on a pair where the lights never got
	// uploaded, the flag stopped working, or the camera moved off the lake --
	// the shape of green result this repo has shipped before.
	min := flag.Float64("min", 15, "the lit box must gain at least this much mean red, in 8-bit steps")

	// A margin rather than a bare ordering, for lampcheck's reason: channels a
	// step or two apart are a neutral grey that happens to lean.
	margin := flag.Float64("margin", 4, "how far apart the added channels must be, in 8-bit steps")

	// Zero, and meant literally; see the package comment.
	tol := flag.Int("tol", 0, "largest channel difference allowed anywhere in the dark box")
	flag.Parse()

	if *a == "" || *b == "" || *litBox == "" || *darkBox == "" {
		fmt.Fprintln(os.Stderr, "watercheck: -a, -b, -lit and -dark are required")
		os.Exit(2)
	}

	lit, err := parseBox(*litBox)
	if err != nil {
		fmt.Fprintf(os.Stderr, "watercheck: -lit: %v\n", err)
		os.Exit(2)
	}
	dark, err := parseBox(*darkBox)
	if err != nil {
		fmt.Fprintf(os.Stderr, "watercheck: -dark: %v\n", err)
		os.Exit(2)
	}

	on, err := load(*a)
	if err != nil {
		fmt.Fprintf(os.Stderr, "watercheck: %v\n", err)
		os.Exit(2)
	}
	off, err := load(*b)
	if err != nil {
		fmt.Fprintf(os.Stderr, "watercheck: %v\n", err)
		os.Exit(2)
	}
	if on.Bounds() != off.Bounds() {
		fmt.Fprintf(os.Stderr, "watercheck: captures differ in size: %v vs %v\n", on.Bounds(), off.Bounds())
		os.Exit(2)
	}
	for _, r := range []image.Rectangle{lit, dark} {
		if !r.In(on.Bounds()) {
			fmt.Fprintf(os.Stderr, "watercheck: region %v is outside the capture %v\n", r, on.Bounds())
			os.Exit(2)
		}
	}

	added, base := gain(on, off, lit)
	fmt.Printf("lit   %v: added R %+6.2f G %+6.2f B %+6.2f  (was R %5.2f G %5.2f B %5.2f)\n",
		lit, added[0], added[1], added[2], base[0], base[1], base[2])

	worst, at := maxDiff(on, off, dark)
	fmt.Printf("dark  %v: largest channel difference %d at %v\n", dark, worst, at)

	fail := false

	if added[0] < *min {
		fmt.Printf("THE LAMPS DO NOT REACH THE WATER: the lit box gained %.2f red, want at least %.0f\n", added[0], *min)
		fail = true
	} else if added[0]-added[1] < *margin || added[1]-added[2] < *margin {
		// Only worth asking once there is light to ask about: the ordering of
		// three numbers near zero is noise, and reporting it as a hue failure
		// would point at the grade when the light is what is missing.
		fmt.Printf("WHAT THE LAMPS ADD IS NOT WARM: want R > G > B by %.0f, got R-G %.2f, G-B %.2f\n",
			*margin, added[0]-added[1], added[1]-added[2])
		fail = true
	} else {
		fmt.Printf("the lamps reach the water, warm: R-G %.2f, G-B %.2f\n", added[0]-added[1], added[1]-added[2])
	}

	if worst > *tol {
		fmt.Printf("WATER OUT OF RANGE MOVED: %d, want at most %d -- a light is contributing where it does not reach\n", worst, *tol)
		fail = true
	} else {
		fmt.Printf("water out of every lamp's range is unchanged\n")
	}

	if fail {
		os.Exit(1)
	}
	fmt.Println("lamplight lands on the lake, and only where a lamp reaches")
}

// gain returns the mean each channel gained over the region, and what it was
// without the lights — the second only so a reader can see whether the first
// is a small change to something bright or a large change to something dark.
//
// Per channel rather than as a luminance, because the ordering of the three is
// the question this command asks; sRGB encoding is monotonic, so the
// comparison is the same either way and the numbers are the ones a person
// reads off a colour picker.
func gain(on, off image.Image, r image.Rectangle) (added, base [3]float64) {
	var sumOn, sumOff [3]float64
	n := 0
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			r1, g1, b1, _ := on.At(x, y).RGBA()
			r2, g2, b2, _ := off.At(x, y).RGBA()
			sumOn[0] += float64(r1 >> 8)
			sumOn[1] += float64(g1 >> 8)
			sumOn[2] += float64(b1 >> 8)
			sumOff[0] += float64(r2 >> 8)
			sumOff[1] += float64(g2 >> 8)
			sumOff[2] += float64(b2 >> 8)
			n++
		}
	}
	if n == 0 {
		return added, base
	}
	for i := 0; i < 3; i++ {
		added[i] = (sumOn[i] - sumOff[i]) / float64(n)
		base[i] = sumOff[i] / float64(n)
	}
	return added, base
}

// maxDiff returns the largest single-channel difference anywhere in the region
// and where it is. A mean would average a bright leak away against the rest of
// the box, which is the opposite of what "unchanged" means.
func maxDiff(on, off image.Image, r image.Rectangle) (int, image.Point) {
	worst := 0
	at := r.Min
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			r1, g1, b1, _ := on.At(x, y).RGBA()
			r2, g2, b2, _ := off.At(x, y).RGBA()
			for _, d := range []int{
				int(r1>>8) - int(r2>>8),
				int(g1>>8) - int(g2>>8),
				int(b1>>8) - int(b2>>8),
			} {
				if d < 0 {
					d = -d
				}
				if d > worst {
					worst = d
					at = image.Pt(x, y)
				}
			}
		}
	}
	return worst, at
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
