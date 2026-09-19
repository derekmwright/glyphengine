// Command volumetriccheck asserts that a light asking for volumetric
// scattering puts light in the air INSIDE its cone and not outside it.
//
//	task volumetric
//
// It exists for one bug that has already been measured, on a technique that
// is not what shipped. Issue #47's first proposal was to radiate the existing
// screen-space god-ray pass from a lamp instead of from the sun. Pointed at
// building 0's doorway spotlight in 21-streetlights and differenced against
// the same frame with the pass off, it added +21.3 mean luma to a box in the
// cone's path and +32.5 and +34.0 to boxes the same distance from the bulb
// but straight up and to the side, where no beam could be. The air a beam
// should fill gained two thirds of what the air it must not fill gained,
// because that technique smears pixels that are already bright and has no
// idea which way a light points.
//
// So the arms here are that measurement, inverted, and a build that produces
// a glow around the bulb rather than a cone fails them the way that spike
// did:
//
//   - beam: a box between the bulb and the pool its cone lands in must gain
//     at least a floor.
//   - outside: boxes the same distance from the bulb but outside the cone
//     must gain at most a ceiling. Two of them, up and to the side, because
//     one is a coincidence away from passing.
//   - far: a box beyond every light's range must gain exactly nothing.
//     lightIrradiance and this march both return exactly +0.0 outside a
//     range test, so the bar is 0.00 and not a small number.
//
// Differencing a pair is what makes any of this measurable: a single frame
// cannot say what should have been there, and the same night sky and the same
// warm pools are under both captures and subtract out. Both captures are of
// the same scene at the same fixed clock with only SpotLight.Volumetric
// changed.
//
// -min and -max are per invocation because the arms are not one measurement
// repeated. A beam crossing the frame at 11 m and a beam 40 m away behind a
// building are different amounts of air, and a ceiling tight enough to be
// worth having on the second would fail the first for being correct.
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

// region is one named box with the bound it has to clear, parsed from a
// repeated flag so one invocation can carry as many arms as the scene has.
type region struct {
	name string
	box  image.Rectangle
	// min is a floor the gain must reach, max a ceiling it must not pass.
	// Whichever was not given is left nil, because "no ceiling" and "a
	// ceiling of zero" are opposite requirements and a float cannot say which
	// one an unset flag meant.
	min, max *float64
}

func main() {
	a := flag.String("a", "", "capture WITH volumetric scattering")
	b := flag.String("b", "", "capture WITHOUT it, same scene, same clock")
	var args regionFlags
	flag.Var(&args, "region", "name:x,y,w,h:min=F or :max=F -- repeatable; min is a floor on the mean luma gained, max a ceiling")
	flag.Parse()

	if *a == "" || *b == "" || len(args) == 0 {
		fmt.Fprintln(os.Stderr, "volumetriccheck: -a, -b and at least one -region are required")
		os.Exit(2)
	}

	withVol, err := load(*a)
	if err != nil {
		fmt.Fprintf(os.Stderr, "volumetriccheck: %v\n", err)
		os.Exit(2)
	}
	without, err := load(*b)
	if err != nil {
		fmt.Fprintf(os.Stderr, "volumetriccheck: %v\n", err)
		os.Exit(2)
	}
	if withVol.Bounds() != without.Bounds() {
		fmt.Fprintf(os.Stderr, "volumetriccheck: captures differ in size: %v vs %v\n",
			withVol.Bounds(), without.Bounds())
		os.Exit(2)
	}

	// Prove the comparator before trusting a word it says, the way `task
	// determinism` proves its diff first. An image differenced against ITSELF
	// must gain exactly zero in every box; if it does not, gain() is broken
	// and every number below is noise. This is not hypothetical here: the
	// first version of this tool summed 16-bit channels from RGBA() on one
	// side and 8-bit on the other, which reported a uniform +0.0 for the
	// self-check by luck and +32000 for everything else.
	for _, r := range args {
		if !r.box.In(withVol.Bounds()) {
			fmt.Fprintf(os.Stderr, "volumetriccheck: region %s %v is outside the capture %v\n",
				r.name, r.box, withVol.Bounds())
			os.Exit(2)
		}
		if g, _ := gain(withVol, withVol, r.box); g != 0 {
			fmt.Fprintf(os.Stderr, "SELF-CHECK: a capture differenced against itself gained %.4f in %s, not 0 -- this gate proves nothing\n", g, r.name)
			os.Exit(2)
		}
	}
	// And that it can report something: the two captures must differ
	// somewhere, or both files are the same render and every arm is measuring
	// a no-op. A gate whose captures were identical has shipped here before --
	// see cmd/shaftcheck, where -shafts 0 and -shafts 3 rendered the same
	// bytes for seven weeks.
	if identical(withVol, without) {
		fmt.Fprintln(os.Stderr, "SELF-CHECK: the two captures are pixel-identical -- volumetrics did nothing at all, so no arm below means anything")
		os.Exit(2)
	}

	status := 0
	for _, r := range args {
		g, base := gain(withVol, without, r.box)
		line := fmt.Sprintf("%-12s %v: %+.2f mean luma (background %.1f, grain %.2f)",
			r.name, r.box, g, base, grain(withVol, without, r.box))
		switch {
		case r.min != nil && g < *r.min:
			fmt.Printf("%s  FAIL: under the floor %.2f\n", line, *r.min)
			status = 1
		case r.max != nil && g > *r.max:
			fmt.Printf("%s  FAIL: over the ceiling %.2f\n", line, *r.max)
			status = 1
		default:
			fmt.Printf("%s  ok\n", line)
		}
	}
	if status != 0 {
		fmt.Println()
		fmt.Println("A floor missed in `beam` or `offscreen` means the march is not reaching that air:")
		fmt.Println("  the froxel walk in volInscatter's loop, or LightFlagVolumetric never set.")
		fmt.Println("A ceiling passed in `outside` means light is going where the cone is not:")
		fmt.Println("  lightSpotFactor dropped out of the per-light term, which is a glow, not a beam.")
		fmt.Println("A non-zero `far` means something is adding light with no range test at all.")
		os.Exit(1)
	}
	fmt.Println("light is in the air inside the cones and not outside them")
}

// gain returns the mean luma volumetrics added over the region, and the mean
// luma of the region without them -- the second only so a reader can tell a
// small change to something bright from a large change to something dark,
// which at night is most of the question.
func gain(withVol, without image.Image, r image.Rectangle) (added, base float64) {
	var sumA, sumB float64
	n := 0
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			sumA += luma(withVol, x, y)
			sumB += luma(without, x, y)
			n++
		}
	}
	if n == 0 {
		return 0, 0
	}
	return (sumA - sumB) / float64(n), sumB / float64(n)
}

// grain is the mean absolute Laplacian of the ADDED light over the region: how
// much the in-scattering changes from one pixel to the next, in 8-bit luma.
//
// It is reported rather than asserted on, and it is reported because the mean
// gain alone cannot tell a smooth beam from a dithered one. A fixed-step march
// through a cone thinner than one step puts the whole of a segment's light
// into some pixels and none into their neighbours, and the mean is the same
// either way. This is the number that moves: it is what the step sweep in
// docs/agents/lights.md is read against.
//
// The Laplacian of the DIFFERENCE, not of the capture -- the scene underneath
// has edges of its own (a roofline, a lamp post) and they are identical in
// both captures, so differencing removes them and what is left is the march's
// own texture. No blur first, unlike godray.frag's step metric: there the
// question was "steps or dither" and a blur separates them, here dither is
// exactly the thing being measured.
func grain(withVol, without image.Image, r image.Rectangle) float64 {
	d := func(x, y int) float64 { return luma(withVol, x, y) - luma(without, x, y) }
	var sum float64
	n := 0
	for y := r.Min.Y + 1; y < r.Max.Y-1; y++ {
		for x := r.Min.X + 1; x < r.Max.X-1; x++ {
			l := 4*d(x, y) - d(x-1, y) - d(x+1, y) - d(x, y-1) - d(x, y+1)
			if l < 0 {
				l = -l
			}
			sum += l
			n++
		}
	}
	if n == 0 {
		return 0
	}
	return sum / float64(n)
}

// identical reports whether two captures are the same image. Used only for
// the self-check; a real comparison is `diff`, which the Taskfile does.
func identical(a, b image.Image) bool {
	r := a.Bounds()
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			r1, g1, b1, _ := a.At(x, y).RGBA()
			r2, g2, b2, _ := b.At(x, y).RGBA()
			if r1 != r2 || g1 != g2 || b1 != b2 {
				return false
			}
		}
	}
	return true
}

// luma is Rec. 601 luminance in 8-bit units, the same measure shaftcheck,
// blendcheck and skycheck use, so a number here is comparable with a number
// in one of their comments.
func luma(img image.Image, x, y int) float64 {
	r, g, b, _ := img.At(x, y).RGBA()
	return 0.299*float64(r>>8) + 0.587*float64(g>>8) + 0.114*float64(b>>8)
}

type regionFlags []region

func (f *regionFlags) String() string { return "" }

func (f *regionFlags) Set(s string) error {
	parts := strings.Split(s, ":")
	if len(parts) != 3 {
		return fmt.Errorf("want name:x,y,w,h:min=F or name:x,y,w,h:max=F, got %q", s)
	}
	box, err := parseBox(parts[1])
	if err != nil {
		return err
	}
	r := region{name: parts[0], box: box}
	kv := strings.SplitN(parts[2], "=", 2)
	if len(kv) != 2 {
		return fmt.Errorf("want min=F or max=F, got %q", parts[2])
	}
	v, err := strconv.ParseFloat(kv[1], 64)
	if err != nil {
		return fmt.Errorf("%q is not a number", kv[1])
	}
	switch kv[0] {
	case "min":
		r.min = &v
	case "max":
		r.max = &v
	default:
		return fmt.Errorf("want min= or max=, got %q", kv[0])
	}
	*f = append(*f, r)
	return nil
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
