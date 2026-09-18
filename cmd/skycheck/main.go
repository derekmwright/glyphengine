// Command skycheck asserts that nothing in a region of a capture is brighter
// than it should be.
//
//	task sky
//
// It exists for one bug and encodes exactly it. The sun and moon are drawn
// after the sky so that clouds can cover them, and the first version of that
// turned the depth test off and expected the sky's alpha to mask geometry as
// well as cloud. It does not: lit.frag and terrain.frag write alpha 1.0, so
// over a hillside the destination alpha is 1 and the disc was added at full
// strength. A sun that had set drew on top of the hills it set behind.
//
// The failure is loud once you see it and invisible to everything else. The
// frame renders, the validation layer is silent, `task smoke` passes, and
// `task determinism` is perfectly happy because the wrong image is wrong the
// same way every time. Only a person looking at the right camera notices, which
// is how it shipped.
//
// A disc on a dark hillside is a hard, bright, saturated blob against terrain
// that is nearly black, so a brightness ceiling over the terrain region catches
// it with enormous margin and cares about nothing else in the frame.
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
	in := flag.String("in", "", "capture to check")
	y0 := flag.Int("y0", 0, "top of the region")
	y1 := flag.Int("y1", 0, "bottom of the region (0 = to the bottom of the image)")
	maxLuma := flag.Int("max", 120, "no pixel in the region may be brighter than this (0-255)")
	flag.Parse()

	if *in == "" {
		fmt.Fprintln(os.Stderr, "skycheck: -in is required")
		os.Exit(2)
	}

	f, err := os.Open(*in)
	if err != nil {
		fmt.Fprintf(os.Stderr, "skycheck: %v\n", err)
		os.Exit(2)
	}
	defer f.Close()

	img, err := png.Decode(f)
	if err != nil {
		fmt.Fprintf(os.Stderr, "skycheck: decode %s: %v\n", *in, err)
		os.Exit(2)
	}

	r := img.Bounds()
	top, bottom := *y0, *y1
	if bottom <= 0 || bottom > r.Max.Y {
		bottom = r.Max.Y
	}
	if top < r.Min.Y {
		top = r.Min.Y
	}
	if top >= bottom {
		fmt.Fprintf(os.Stderr, "skycheck: empty region y=%d..%d\n", top, bottom)
		os.Exit(2)
	}

	worst, wx, wy := -1, 0, 0
	var sum float64
	var n int
	for y := top; y < bottom; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			l := luma(img, x, y)
			sum += float64(l)
			n++
			if l > worst {
				worst, wx, wy = l, x, y
			}
		}
	}
	mean := sum / float64(n)

	fmt.Printf("region y=%d..%d: mean luma %.1f, brightest %d at (%d,%d), ceiling %d\n",
		top, bottom, mean, worst, wx, wy, *maxLuma)

	// A region that is already bright everywhere cannot tell a disc from its
	// background, so the check would pass or fail for the wrong reason. The
	// camera this runs against puts dark terrain here; if that stops being
	// true the check has stopped meaning anything and should say so.
	if mean > float64(*maxLuma) {
		fmt.Printf("  the region averages brighter than the ceiling -- wrong camera, this proves nothing\n")
		os.Exit(1)
	}

	if worst > *maxLuma {
		fmt.Printf("  FAIL: something is drawing over the terrain\n")
		fmt.Printf("  a celestial disc leaking through geometry is the known cause;\n")
		fmt.Printf("  see createCelestialPipeline, which must depth-test against the scene\n")
		os.Exit(1)
	}

	fmt.Println("nothing is drawing over the terrain")
}

// luma is Rec. 601 luminance, which is what "looks bright to a person" wants
// rather than a max over channels: the sun disc is pale yellow and would read
// lower on a blue-channel test than it does to the eye.
func luma(img image.Image, x, y int) int {
	r, g, b, _ := img.At(x, y).RGBA()
	v := 0.299*float64(r>>8) + 0.587*float64(g>>8) + 0.114*float64(b>>8)
	return int(math.Round(v))
}
