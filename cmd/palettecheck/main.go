// Command palettecheck asserts that the sky palette reaches the whole
// atmosphere and not just the dome: the sky at the horizon, the fogged
// geometry beside it and the water reflecting both must all move together when
// the palette changes.
//
//	task skypalette
//
// It takes two captures of the same scene at the same fixed clock — one with
// an alien palette and one with the engine's default, which is what
// `09-water -alien` toggles — and three boxes:
//
//   - sky is clear dome just above the skyline, where `mix(horizon, zenith, t)`
//     is nearly all horizon.
//   - fog is distant terrain just below it, which `applyFog` blends toward the
//     same horizon colour.
//   - water is the lake at a grazing angle, where the Fresnel term is near one
//     and what the surface shows is the dome reflected.
//
// and, optionally, a fourth:
//
//   - cloud is the shaded core of a cumulus, where the sun's own light is mostly
//     extinguished and what is left is the march's ambient fill -- which
//     clouds.frag takes from the same palette, through its own call.
//
// The fourth is there because the first three cannot see it. clouds.frag is a
// separate caller of atmSkyPalette in a separate pipeline, nothing in the lit
// family depends on it, and handing it Earth's six colours while everything
// else took the uniform left sky, fog and water reading exactly what they read
// on the shipped build -- this check passed -- over a frame in which 23% of the
// pixels were wrong by up to 22 of 255. It has its own floor and its own angle,
// for the reasons given on the flags.
//
// # Why the direction of the change and not its size
//
// The three regions do not carry the palette in the same proportion, and
// cannot be made to. The sky box is about 90% horizon and 10% zenith at that
// elevation; the fog box is the horizon colour times whatever fraction of the
// sightline the haze fills, which at 09-water's density and scale is about a
// fifth; the water box is the horizon times Fresnel, over a body colour. So
// requiring the three to land on the same value, or to move by the same
// amount, would be requiring something that is not true of a correct renderer.
//
// What IS true of a correct renderer is that each region moves by its own
// weight times the SAME vector — the change in the horizon colour — because
// there is one palette behind all three. So the direction each region moves in
// is the invariant, and the angle between those directions is what this
// measures. Magnitude only has to clear a floor, and the floor is there to
// stop the check passing on a pair that did not move at all.
//
// # Why a pair rather than a single capture
//
// The absolute colours cannot be compared across the three: the terrain has an
// albedo and the water has a body colour, so "the fog box equals the sky box"
// is false in every scene with anything in it. Differencing two captures of
// the same scene removes everything that did not change, which is precisely
// everything except the palette.
//
// PROVED not vacuous, on `task skypalette`'s own boxes: feed the palette to
// sky.frag and leave lighting.inc's fogColor reading the old compiled-in
// constants — the exact drift this whole change exists to prevent — and the
// fog box's movement collapses from 40.05 to 0.49 and the water box's from
// 105.15 to exactly 0.00, while the sky box stays at 122.32. Both report THE
// PALETTE DID NOT REACH.
//
// Note what that says about the floor and the angle. The floor caught this
// one, because the break removed the palette from those regions outright. The
// angle is what catches the subtler version — a region fed a palette that is
// not the sky's — and it is why this reports both.
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

func main() {
	a := flag.String("a", "", "capture with the alien palette")
	b := flag.String("b", "", "capture with the engine default, same scene and clock")
	skyBox := flag.String("sky", "", "x,y,w,h of clear dome just above the skyline")
	fogBox := flag.String("fog", "", "x,y,w,h of distant fogged geometry below it")
	waterBox := flag.String("water", "", "x,y,w,h of water reflecting the dome at a grazing angle")

	// The floor that stops this passing on a pair where the flag stopped
	// working, the palette never reached the GPU, or the camera moved off the
	// lake. It is deliberately in the same units the numbers are printed in,
	// so a reader can check it against the report.
	min := flag.Float64("min", 6, "each box's mean colour must move at least this far, in 8-bit steps")

	// The angle two regions' movements may differ by. Not zero: the sky box
	// carries a little zenith as well as horizon, and the tonemap is not
	// linear, so two regions moving by different amounts along the same
	// palette change do not come out exactly parallel after encoding.
	tol := flag.Float64("tol", 12, "how far each box's movement may differ in direction from the sky's, in degrees")

	// The cloud box needs a higher floor than the others, because a cloud is
	// not opaque: with the march fed the wrong palette outright, the dome
	// showing through the box still moves it 4.13. The shipped build moves it
	// 24.67, so 12 sits a factor of two or more from both. And it needs a wider
	// angle, because the fill is `mix(zenith, horizon, 0.5)` -- half zenith --
	// where the sky box is nine tenths horizon; 10.3 degrees apart is what a
	// correct build reads, and that is the two colours being different colours
	// rather than the two callers being on different palettes.
	cloudBox := flag.String("cloud", "", "x,y,w,h of a cumulus's shaded core (optional)")
	cloudMin := flag.Float64("cloudmin", 12, "the cloud box's floor, in 8-bit steps")
	cloudTol := flag.Float64("cloudtol", 20, "the cloud box's angle from the sky's movement, in degrees")
	flag.Parse()

	if *a == "" || *b == "" || *skyBox == "" || *fogBox == "" || *waterBox == "" {
		fmt.Fprintln(os.Stderr, "palettecheck: -a, -b, -sky, -fog and -water are required")
		os.Exit(2)
	}

	type region struct {
		name     string
		spec     string
		min, tol float64
	}
	named := []region{
		{"sky", *skyBox, *min, *tol},
		{"fog", *fogBox, *min, *tol},
		{"water", *waterBox, *min, *tol},
	}
	if *cloudBox != "" {
		named = append(named, region{"cloud", *cloudBox, *cloudMin, *cloudTol})
	}

	alien, err := load(*a)
	if err != nil {
		fmt.Fprintf(os.Stderr, "palettecheck: %v\n", err)
		os.Exit(2)
	}
	def, err := load(*b)
	if err != nil {
		fmt.Fprintf(os.Stderr, "palettecheck: %v\n", err)
		os.Exit(2)
	}
	if alien.Bounds() != def.Bounds() {
		fmt.Fprintf(os.Stderr, "palettecheck: captures differ in size: %v vs %v\n", alien.Bounds(), def.Bounds())
		os.Exit(2)
	}

	type reading struct {
		name         string
		before, move [3]float64
		length       float64
		min, tol     float64
	}
	readings := make([]reading, 0, len(named))
	for _, n := range named {
		r, err := parseBox(n.spec)
		if err != nil {
			fmt.Fprintf(os.Stderr, "palettecheck: -%s: %v\n", n.name, err)
			os.Exit(2)
		}
		if !r.In(alien.Bounds()) {
			fmt.Fprintf(os.Stderr, "palettecheck: %s region %v is outside the capture %v\n", n.name, r, alien.Bounds())
			os.Exit(2)
		}
		after := mean(alien, r)
		before := mean(def, r)
		var move [3]float64
		for i := range move {
			move[i] = after[i] - before[i]
		}
		readings = append(readings, reading{name: n.name, before: before, move: move, length: norm(move), min: n.min, tol: n.tol})
	}

	for _, r := range readings {
		fmt.Printf("%-5s default R %6.2f G %6.2f B %6.2f  ->  moved R %+6.2f G %+6.2f B %+6.2f  (distance %5.2f)\n",
			r.name, r.before[0], r.before[1], r.before[2], r.move[0], r.move[1], r.move[2], r.length)
	}

	fail := false
	for _, r := range readings {
		if r.length < r.min {
			fmt.Printf("THE PALETTE DID NOT REACH THE %s: it moved %.2f, want at least %.0f\n",
				strings.ToUpper(r.name), r.length, r.min)
			fail = true
		}
	}
	if fail {
		// The angles below are meaningless when one of the vectors is noise,
		// and reporting them would point at a drift when what happened is that
		// a region never got the palette at all.
		os.Exit(1)
	}

	sky := readings[0]
	for _, r := range readings[1:] {
		deg := angle(sky.move, r.move)
		if deg > r.tol {
			fmt.Printf("THE %s IS ON A DIFFERENT PALETTE FROM THE SKY: %.1f degrees apart, want at most %.0f\n",
				strings.ToUpper(r.name), deg, r.tol)
			fail = true
		} else {
			fmt.Printf("%-5s moved with the sky: %.1f degrees apart\n", r.name, deg)
		}
	}

	if fail {
		os.Exit(1)
	}
	if *cloudBox != "" {
		fmt.Println("the sky, the fog, the water's reflection and the clouds are on one palette")
		return
	}
	fmt.Println("the sky, the fog and the water's reflection are on one palette")
}

// mean is the average 8-bit channel value over a region. Per channel rather
// than as a luminance, because the direction of the change in colour space is
// the whole question; sRGB is monotonic, so these are also the numbers a
// colour picker shows.
func mean(img image.Image, r image.Rectangle) [3]float64 {
	var s [3]float64
	n := 0
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			cr, cg, cb, _ := img.At(x, y).RGBA()
			s[0] += float64(cr >> 8)
			s[1] += float64(cg >> 8)
			s[2] += float64(cb >> 8)
			n++
		}
	}
	if n == 0 {
		return s
	}
	for i := range s {
		s[i] /= float64(n)
	}
	return s
}

func norm(v [3]float64) float64 {
	return math.Sqrt(v[0]*v[0] + v[1]*v[1] + v[2]*v[2])
}

// angle between two colour changes, in degrees. Zero means the two regions
// moved the same way through colour space and differ only in how far.
func angle(a, b [3]float64) float64 {
	na, nb := norm(a), norm(b)
	if na == 0 || nb == 0 {
		return 180
	}
	dot := (a[0]*b[0] + a[1]*b[1] + a[2]*b[2]) / (na * nb)
	// Guard the acos: rounding can put a parallel pair a hair past 1.
	if dot > 1 {
		dot = 1
	} else if dot < -1 {
		dot = -1
	}
	return math.Acos(dot) * 180 / math.Pi
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
