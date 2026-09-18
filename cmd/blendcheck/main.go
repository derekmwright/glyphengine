// Command blendcheck asserts that a blended effect standing in front of water
// is still on screen where the water is.
//
//	task waterblend
//
// It exists for one bug and encodes exactly it. Water is drawn last, in a pass
// of its own, because refraction has to sample the finished frame. Nothing
// blended writes depth, so where a flame stood in front of the lake the depth
// buffer held the lake BED, the water passed the depth test, and the flame was
// painted over — cut off dead flat at the waterline, visible against sky and
// gone against water. Issue #45.
//
// Measuring it needs two captures of the same scene at the same fixed clock,
// one with the plume and one without, because a single frame cannot say what
// should have been there. Differencing gives the light the plume ADDED, which
// is a number a bright sky cannot fake: the same water is underneath both
// captures and subtracts out. A plain brightness floor over the lake would pass
// on the lake.
//
// Two regions, and the second is what makes this hard to fool:
//
//   - box is the part of the plume below the waterline, where the bug was.
//   - ref is the part above it, against sky, where the plume was always drawn
//     correctly. It is the same plume in the same capture pair, so it says how
//     much light this plume is worth at all.
//
// A run where the plume never spawned, the flag stopped working, or the camera
// moved off it fails on ref before box is believed, and the ratio between them
// is what a fixed threshold cannot express: "the plume is as present below the
// waterline as it is above it".
//
// The bug survives differencing as a small non-zero reading rather than as
// zero, because the swallowed plume is still in the refraction copy and the
// water shows a smeared, absorbed ghost of it. That is why there is a ratio and
// not just "greater than nothing".
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
	a := flag.String("a", "", "capture WITH the plume")
	b := flag.String("b", "", "capture WITHOUT it, same scene and clock")
	boxArg := flag.String("box", "", "x,y,w,h of the plume below the waterline")
	refArg := flag.String("ref", "", "x,y,w,h of the plume above it, as the control")
	minGain := flag.Float64("min", 8, "the box must gain at least this much mean luma (0-255)")
	minRef := flag.Float64("minref", 8, "below this the control has no plume either and nothing here means anything")
	minRatio := flag.Float64("ratio", 0.5, "box gain as a fraction of control gain")
	flag.Parse()

	if *a == "" || *b == "" || *boxArg == "" || *refArg == "" {
		fmt.Fprintln(os.Stderr, "blendcheck: -a, -b, -box and -ref are required")
		os.Exit(2)
	}

	box, err := parseBox(*boxArg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "blendcheck: -box: %v\n", err)
		os.Exit(2)
	}
	ref, err := parseBox(*refArg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "blendcheck: -ref: %v\n", err)
		os.Exit(2)
	}

	withPlume, err := load(*a)
	if err != nil {
		fmt.Fprintf(os.Stderr, "blendcheck: %v\n", err)
		os.Exit(2)
	}
	without, err := load(*b)
	if err != nil {
		fmt.Fprintf(os.Stderr, "blendcheck: %v\n", err)
		os.Exit(2)
	}
	if withPlume.Bounds() != without.Bounds() {
		fmt.Fprintf(os.Stderr, "blendcheck: captures differ in size: %v vs %v\n",
			withPlume.Bounds(), without.Bounds())
		os.Exit(2)
	}
	for _, r := range []image.Rectangle{box, ref} {
		if !r.In(withPlume.Bounds()) {
			fmt.Fprintf(os.Stderr, "blendcheck: region %v is outside the capture %v\n", r, withPlume.Bounds())
			os.Exit(2)
		}
	}

	boxGain, boxBase := gain(withPlume, without, box)
	refGain, refBase := gain(withPlume, without, ref)

	fmt.Printf("over water  %v: +%.1f mean luma (background %.1f)\n", box, boxGain, boxBase)
	fmt.Printf("over sky    %v: +%.1f mean luma (background %.1f)\n", ref, refGain, refBase)

	status := 0
	if refGain < *minRef {
		fmt.Printf("  FAIL: the control region gained %.1f, under %.1f -- there is no plume in these\n", refGain, *minRef)
		fmt.Printf("  captures at all, so nothing measured below the waterline means anything\n")
		os.Exit(1)
	}

	ratio := boxGain / refGain
	fmt.Printf("ratio %.3f (floor %.2f), absolute floor %.1f\n", ratio, *minRatio, *minGain)

	if boxGain < *minGain {
		fmt.Printf("  FAIL: the plume adds almost nothing where the water is\n")
		status = 1
	}
	if ratio < *minRatio {
		fmt.Printf("  FAIL: the plume is %.0f%% as bright over water as over sky\n", ratio*100)
		status = 1
	}
	if status != 0 {
		fmt.Printf("  the water pass drawing over blended geometry in front of it is the known\n")
		fmt.Printf("  cause; see renderer/waterorder.go, which decides what is drawn after water\n")
		os.Exit(status)
	}

	fmt.Println("the plume survives the water")
}

// gain returns the mean luma the plume added over the region, and the mean luma
// of the region without it — the second only so a reader can see whether the
// first is a small change to something bright or a large change to something
// dark.
func gain(withPlume, without image.Image, r image.Rectangle) (added, base float64) {
	var sumA, sumB float64
	n := 0
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			sumA += luma(withPlume, x, y)
			sumB += luma(without, x, y)
			n++
		}
	}
	if n == 0 {
		return 0, 0
	}
	return (sumA - sumB) / float64(n), sumB / float64(n)
}

// luma is Rec. 601 luminance, the same measure skycheck uses: a plume is warm
// orange and a max over channels would read it as brighter than it looks.
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
