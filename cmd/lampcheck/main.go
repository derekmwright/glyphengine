// Command lampcheck asserts that a lamp's pool of light still reads as the
// lamp's colour at night, and that the ground outside it still does not.
//
//	task nightlight
//
// It takes one capture and two boxes: a patch of ground inside the pool and a
// patch of the same ground outside every pool. Inside must come out warm --
// red above green above blue, for a warm lamp -- and outside must stay on the
// blue-shifted side that atmNightShift puts unlit ground on. One frame is
// enough because the two boxes are the comparison; there is no second render
// to difference against.
//
// Why a check at all: atmNightShift used to blend every lit fragment toward a
// blue-weighted grey on sun altitude alone, and at full night that blend is
// provably one-sided -- the shifted blue channel exceeds the shifted green for
// every colour with no negative channel, so no lamp could produce red > green
// > blue on any surface. The failure is not subtle once you look at the frame,
// but it is invisible to `task smoke`, `task validate` and `task determinism`,
// all of which stayed green through it for as long as it existed.
//
// PROVED not vacuous: with lighting.inc's nightLocalShare forced to return 0 --
// which is exactly the old behaviour -- 21-streetlights' doorway pool goes from
// red 181, green 169, blue 109 to red 154, green 160, blue 177 and this check
// reports POOL IS NOT WARM. The unlit box is unchanged at red 30, green 35,
// blue 43, because nothing local reaches it either way.
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
	in := flag.String("i", "", "capture to measure")
	poolBox := flag.String("pool", "", "x,y,w,h of a patch of ground inside the pool")
	unlitBox := flag.String("unlit", "", "x,y,w,h of the same ground outside every pool")

	// A margin rather than a bare ordering. Channels a step or two apart are
	// a neutral grey that happens to lean, and calling that "warm" would let
	// the check pass on an image no one would describe that way.
	margin := flag.Float64("margin", 6, "how far apart the channels must be, in 8-bit steps")

	// The floor that stops this passing on a black frame, a missing capture or
	// a scene whose lights never got uploaded -- the shape of green result
	// this repo has shipped before. A pool has to be a pool.
	ratio := flag.Float64("ratio", 4, "how many times brighter the pool box must be than the unlit one")
	flag.Parse()

	if *in == "" || *poolBox == "" || *unlitBox == "" {
		fmt.Fprintln(os.Stderr, "lampcheck: -i, -pool and -unlit are required")
		os.Exit(2)
	}

	img, err := load(*in)
	if err != nil {
		fmt.Fprintf(os.Stderr, "lampcheck: %v\n", err)
		os.Exit(2)
	}
	pool, err := meanOf(img, *poolBox)
	if err != nil {
		fmt.Fprintf(os.Stderr, "lampcheck: -pool: %v\n", err)
		os.Exit(2)
	}
	unlit, err := meanOf(img, *unlitBox)
	if err != nil {
		fmt.Fprintf(os.Stderr, "lampcheck: -unlit: %v\n", err)
		os.Exit(2)
	}

	fmt.Printf("pool   R %6.1f G %6.1f B %6.1f   luminance %.4f\n", pool[0], pool[1], pool[2], lum(pool))
	fmt.Printf("unlit  R %6.1f G %6.1f B %6.1f   luminance %.4f\n", unlit[0], unlit[1], unlit[2], lum(unlit))

	fail := false

	if got := lum(pool) / math.Max(lum(unlit), 1e-9); got < *ratio {
		fmt.Printf("THE POOL IS NOT IN THE CAPTURE: only %.1fx the unlit ground, want %.0fx -- this check proves nothing\n", got, *ratio)
		os.Exit(1)
	}

	if pool[0]-pool[1] < *margin || pool[1]-pool[2] < *margin {
		fmt.Printf("POOL IS NOT WARM: want R > G > B by %.0f, got R-G %.1f, G-B %.1f\n",
			*margin, pool[0]-pool[1], pool[1]-pool[2])
		fail = true
	} else {
		fmt.Printf("pool is warm: R-G %.1f, G-B %.1f\n", pool[0]-pool[1], pool[1]-pool[2])
	}

	// The other half. Backing the night shift off inside a pool is only right
	// if it is still fully applied outside one, and the cheap way to get this
	// wrong is a weight that leaks across the whole frame.
	if unlit[2]-unlit[1] < *margin/2 || unlit[1]-unlit[0] < *margin/2 {
		fmt.Printf("UNLIT GROUND IS NOT BLUE-SHIFTED: want B > G > R by %.0f, got B-G %.1f, G-R %.1f\n",
			*margin/2, unlit[2]-unlit[1], unlit[1]-unlit[0])
		fail = true
	} else {
		fmt.Printf("unlit ground still scotopic: B-G %.1f, G-R %.1f\n", unlit[2]-unlit[1], unlit[1]-unlit[0])
	}

	if fail {
		os.Exit(1)
	}
	fmt.Println("lamplight keeps its colour; the ground beside it does not")
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

// meanOf averages one box, in 8-bit channel values as the PNG holds them.
// Ordering is what this command asks about, and sRGB encoding is monotonic, so
// the comparison is the same either way and the numbers are the ones a person
// reads off a colour picker.
func meanOf(img image.Image, spec string) ([3]float64, error) {
	var box [4]int
	parts := strings.Split(spec, ",")
	if len(parts) != 4 {
		return [3]float64{}, fmt.Errorf("want x,y,w,h, got %q", spec)
	}
	for i, p := range parts {
		v, err := strconv.Atoi(strings.TrimSpace(p))
		if err != nil {
			return [3]float64{}, err
		}
		box[i] = v
	}
	r := img.Bounds()
	var sum [3]float64
	n := 0
	for y := box[1]; y < box[1]+box[3]; y++ {
		for x := box[0]; x < box[0]+box[2]; x++ {
			if !image.Pt(x, y).In(r) {
				return [3]float64{}, fmt.Errorf("box %v runs outside the %v capture", box, r)
			}
			cr, cg, cb, _ := img.At(x, y).RGBA()
			sum[0] += float64(cr >> 8)
			sum[1] += float64(cg >> 8)
			sum[2] += float64(cb >> 8)
			n++
		}
	}
	if n == 0 {
		return [3]float64{}, fmt.Errorf("box %v is empty", box)
	}
	return [3]float64{sum[0] / float64(n), sum[1] / float64(n), sum[2] / float64(n)}, nil
}

// lum is Rec. 709 luminance of an sRGB-encoded mean, decoded first: the ratio
// test is about how much light is there, which the encoding is not linear in.
func lum(c [3]float64) float64 {
	return 0.2126*srgbToLinear(c[0]/255) + 0.7152*srgbToLinear(c[1]/255) + 0.0722*srgbToLinear(c[2]/255)
}

func srgbToLinear(c float64) float64 {
	if c <= 0.04045 {
		return c / 12.92
	}
	return math.Pow((c+0.055)/1.055, 2.4)
}
