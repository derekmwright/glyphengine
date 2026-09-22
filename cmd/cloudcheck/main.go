// Command cloudcheck checks the fixed-clock captures made by task clouds.
// It measures visible sunset colour, an independently rendered high layer,
// and attenuation of that layer behind cumulus. It is not a realism score.
package main

import (
	"flag"
	"fmt"
	"image"
	"image/png"
	"math"
	"os"
	"path/filepath"
)

func main() {
	dir := flag.String("dir", "examples/.clouds", "directory of task clouds captures")
	flag.Parse()
	failed := false
	check := func(ok bool, format string, args ...any) {
		label := "PASS"
		if !ok {
			label, failed = "FAIL", true
		}
		fmt.Printf("%s: %s\n", label, fmt.Sprintf(format, args...))
	}
	load := func(name string) image.Image {
		f, err := os.Open(filepath.Join(*dir, name+".png"))
		if err != nil {
			fatal(err)
		}
		defer f.Close()
		im, err := png.Decode(f)
		if err != nil {
			fatal(err)
		}
		if im.Bounds() != image.Rect(0, 0, 1280, 720) {
			fatal(fmt.Errorf("%s: expected a 1280x720 capture, got %v", name, im.Bounds()))
		}
		return im
	}
	// Sunlit underside, away from the sun disc, and the shaded core above it.
	// Restoring the old cloudLight makes gold R-G 0.37 (now 39.92) and rose
	// B-G -30.05 (now 15.33). Both checks must fail on that ablation.
	lit := image.Rect(420, 365, 475, 400)
	goldImage := load("gold")
	gold, rose := mean(goldImage, lit), mean(load("rose"), lit)
	core := mean(goldImage, image.Rect(270, 270, 320, 300))
	check(gold[0]-gold[1] > 20 && gold[0]-gold[2] > 30 && luma(gold) > 120,
		"gold underside RGB %.2f, R-G %.2f, R-B %.2f, luma %.2f /255", gold, gold[0]-gold[1], gold[0]-gold[2], luma(gold))
	check(rose[2]-rose[1] > 8 && rose[0]-rose[1] > 50 && luma(rose) > 120,
		"rose underside RGB %.2f, B-G %.2f, luma %.2f /255", rose, rose[2]-rose[1], luma(rose))
	check(luma(gold)-luma(core) > 40, "lit/core contrast %.2f /255", luma(gold)-luma(core))
	noon, night := mean(load("noon"), lit), mean(load("night"), lit)
	check(math.Abs(noon[0]-noon[1]) < 15 && math.Abs(noon[1]-noon[2]) < 15 && luma(noon) > 160,
		"noon underside RGB %.2f, luma %.2f /255", noon, luma(noon))
	check(luma(night) > 1 && luma(night) < 80, "night underside luma %.2f /255", luma(night))

	clear, low, high, both := load("clear"), load("low"), load("high"), load("both")
	var visible, overlaps int
	var bare, covered float64
	// Removing cloudTransmit from the cirrus composite raises the covered
	// change from 4.224 to 25.317 /255 and its ratio from .182 to 1.093.
	// This entire band is sky in the north-facing fixture. Require an absolute
	// 8/255 change before counting a wisp or a layer intersection as visible.
	for y := 0; y < 500; y++ {
		for x := 0; x < 1280; x++ {
			c, l, h, b := rgb(clear, x, y), rgb(low, x, y), rgb(high, x, y), rgb(both, x, y)
			d := difference(c, h)
			if d >= 8 {
				visible++
				if difference(c, l) > 40 {
					overlaps++
					bare += d
					covered += difference(l, b)
				}
			}
		}
	}
	fraction := float64(visible) / (1280 * 500)
	check(fraction > 0.02 && fraction < 0.75, "visible cirrus %.2f%% of sky (8/255 floor)", fraction*100)
	check(overlaps >= 250, "visible layer intersections: %d pixels", overlaps)
	if overlaps > 0 {
		check(covered/bare < 0.45 && covered/float64(overlaps) < 8,
			"cirrus behind cumulus %.3f /255 vs %.3f unobstructed; ratio %.3f",
			covered/float64(overlaps), bare/float64(overlaps), covered/bare)
	}
	// A second independently rendered image must agree, including nonempty
	// content; the visibility floor above prevents identical blank captures.
	repeat := load("repeat")
	var changed int
	for y := 0; y < 720; y++ {
		for x := 0; x < 1280; x++ {
			if rgb(both, x, y) != rgb(repeat, x, y) {
				changed++
			}
		}
	}
	check(changed == 0, "fixed-clock repeat: %d changed pixels", changed)
	if failed {
		os.Exit(1)
	}
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "cloudcheck:", err)
	os.Exit(2)
}

func rgb(im image.Image, x, y int) [3]float64 {
	r, g, b, _ := im.At(x, y).RGBA()
	return [3]float64{float64(r) / 257, float64(g) / 257, float64(b) / 257}
}

func mean(im image.Image, box image.Rectangle) [3]float64 {
	var sum [3]float64
	for y := box.Min.Y; y < box.Max.Y; y++ {
		for x := box.Min.X; x < box.Max.X; x++ {
			c := rgb(im, x, y)
			for i := range sum {
				sum[i] += c[i]
			}
		}
	}
	for i := range sum {
		sum[i] /= float64(box.Dx() * box.Dy())
	}
	return sum
}

func difference(a, b [3]float64) float64 {
	return (math.Abs(a[0]-b[0]) + math.Abs(a[1]-b[1]) + math.Abs(a[2]-b[2])) / 3
}

func luma(c [3]float64) float64 { return .2126*c[0] + .7152*c[1] + .0722*c[2] }
