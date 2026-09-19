// Command shaftcheck asserts that the light-shaft pass draws light through the
// gaps between occluders and not a flat wash over everything.
//
//	task shafts
//
// It exists for one bug and encodes exactly it. `Sky.LightShafts` was
// documented, `godray.frag` was compiled and shipped, `createGodRayPipeline`
// built a pipeline and `recordWaterPass` was handed it — and nothing bound it,
// from 2026-07-29 to 2026-09-19. `-shafts 0`, `1` and `3` rendered byte
// identical frames for seven weeks and nothing in the repository noticed:
// `task smoke` renders the frame, `task validate` is silent, `task determinism`
// is happy because the same wrong image comes out every time, and `task sky`
// passed `-shafts 0` precisely so that it would not be testing the shafts.
//
// Measuring it needs two captures of the same scene at the same fixed clock,
// one with the shafts and one without, because a single frame cannot say what
// should have been there. Differencing gives the light the pass ADDED, which is
// a number a bright sunset cannot fake: the same sky is under both captures and
// subtracts out.
//
// Two regions, and the second is what makes this hard to fool:
//
//   - gap is terrain lit through an opening beside an occluder.
//   - shadow is terrain in that occluder's streak, at the same distance from
//     the sun, so the only thing separating the two is the occluder.
//
// A build that draws nothing fails on gap's absolute floor. A build that draws
// a uniform wash — no threshold, no occlusion, a blur of the whole image —
// passes that floor and fails on the ratio, because a wash lands on both boxes
// equally. Neither failure can be reached by making the scene brighter.
//
// The ratio is deliberately not "shadow must be zero". The shadow box is
// terrain that genuinely receives some light: its own line to the sun leaves
// the occluder before it reaches the disc, and it sits under the same lobe.
// Measured 4.06 with the pass working, 1.0 for a wash, undefined for nothing.
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
	a := flag.String("a", "", "capture WITH the shafts")
	b := flag.String("b", "", "capture WITHOUT them, same scene and clock")
	gapArg := flag.String("gap", "", "x,y,w,h of ground lit through a gap")
	shadowArg := flag.String("shadow", "", "x,y,w,h of ground in an occluder's streak")
	minGain := flag.Float64("min", 12, "the gap must gain at least this much mean luma (0-255)")
	minRatio := flag.Float64("ratio", 2, "gap gain as a multiple of shadow gain")
	flag.Parse()

	if *a == "" || *b == "" || *gapArg == "" || *shadowArg == "" {
		fmt.Fprintln(os.Stderr, "shaftcheck: -a, -b, -gap and -shadow are required")
		os.Exit(2)
	}

	gap, err := parseBox(*gapArg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "shaftcheck: -gap: %v\n", err)
		os.Exit(2)
	}
	shadow, err := parseBox(*shadowArg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "shaftcheck: -shadow: %v\n", err)
		os.Exit(2)
	}

	withShafts, err := load(*a)
	if err != nil {
		fmt.Fprintf(os.Stderr, "shaftcheck: %v\n", err)
		os.Exit(2)
	}
	without, err := load(*b)
	if err != nil {
		fmt.Fprintf(os.Stderr, "shaftcheck: %v\n", err)
		os.Exit(2)
	}
	if withShafts.Bounds() != without.Bounds() {
		fmt.Fprintf(os.Stderr, "shaftcheck: captures differ in size: %v vs %v\n",
			withShafts.Bounds(), without.Bounds())
		os.Exit(2)
	}
	for _, r := range []image.Rectangle{gap, shadow} {
		if !r.In(withShafts.Bounds()) {
			fmt.Fprintf(os.Stderr, "shaftcheck: region %v is outside the capture %v\n", r, withShafts.Bounds())
			os.Exit(2)
		}
	}

	gapGain, gapBase := gain(withShafts, without, gap)
	shadowGain, shadowBase := gain(withShafts, without, shadow)

	fmt.Printf("through the gap %v: +%.1f mean luma (background %.1f)\n", gap, gapGain, gapBase)
	fmt.Printf("in the streak   %v: +%.1f mean luma (background %.1f)\n", shadow, shadowGain, shadowBase)

	status := 0
	if gapGain < *minGain {
		fmt.Printf("  FAIL: the pass adds %.1f where light should come through, under %.1f\n", gapGain, *minGain)
		fmt.Printf("  nothing is being drawn -- check that recordWaterPass still binds godRayPipeline,\n")
		fmt.Printf("  and that SceneLighting.LightShafts survives app.go's edge and elevation fades\n")
		status = 1
	}

	// Guard the division rather than reporting +Inf: a shadow box that gained
	// nothing at all is a pass, and saying so is clearer than a ratio. Only
	// once the gap has cleared its floor, though — with nothing drawn at all
	// both boxes gain nothing, and "blocking completely" would read as good
	// news.
	if shadowGain <= 0 {
		if status == 0 {
			fmt.Printf("ratio: the streak gained nothing, so the occluder is blocking completely\n")
		}
	} else {
		ratio := gapGain / shadowGain
		fmt.Printf("ratio %.3f (floor %.2f), absolute floor %.1f\n", ratio, *minRatio, *minGain)
		if ratio < *minRatio {
			fmt.Printf("  FAIL: the streak is %.0f%% as bright as the gap -- this is a wash, not shafts.\n", 100/ratio)
			fmt.Printf("  bright()'s threshold in godray.frag is what makes an occluder occlude; a\n")
			fmt.Printf("  window that admits plain sky smears the whole frame instead\n")
			status = 1
		}
	}

	if status != 0 {
		os.Exit(status)
	}
	fmt.Println("light comes through the gap and the occluder casts a streak")
}

// gain returns the mean luma the shafts added over the region, and the mean
// luma of the region without them — the second only so a reader can see whether
// the first is a small change to something bright or a large change to
// something dark.
func gain(withShafts, without image.Image, r image.Rectangle) (added, base float64) {
	var sumA, sumB float64
	n := 0
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			sumA += luma(withShafts, x, y)
			sumB += luma(without, x, y)
			n++
		}
	}
	if n == 0 {
		return 0, 0
	}
	return (sumA - sumB) / float64(n), sumB / float64(n)
}

// luma is Rec. 601 luminance, the same measure blendcheck and skycheck use.
func luma(img image.Image, x, y int) float64 {
	r, g, b, _ := img.At(x, y).RGBA()
	return 0.299*float64(r>>8) + 0.587*float64(g>>8) + 0.114*float64(b>>8)
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
