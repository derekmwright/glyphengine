package main

import (
	"fmt"
	"image"
	"log"
	"math"
	"sort"
)

// checkPlanet reads the -cascades 3 arm of 23-shadow-coverage: a mountain 4 km
// behind the camera, sun behind the camera, cascades of 90 m, 3 km and 120 km
// centred on the origin. Each box is a ground band straight ahead of the
// camera; the world distances come from the camera at (0, 50, 140), 45 degrees
// vertical field of view, 800x600.
//
//   - near, z about -40: 32 m from the centre along the light, the 90 m map.
//   - past near depth, z about -130: 104 m along the light and 78 m off its
//     axis, inside the 90 m map's XY bounds and past its 90 m ReceiverDepth,
//     so the 3 km tier serves it.
//   - middle, z about -235, -650 and -1250: the 3 km tier.
//   - beyond, z past -1633, where the mountain's shadow ends: lit in both
//     captures, the control that the darkening above is the mountain's.
//
// Measured 2026-10-17 on RX 7900 XTX under the fixed clock: every shadowed box
// 118.3-118.7/255 with the mountain, 180.3-180.7 without; beyond 180.33 in
// both. The same captures with -receivers=false are byte-identical: on this
// flat ground every tier holds the casters on each receiver's light ray, so
// the receiver rule changes which map is read, not what it says.
//
// Verified break (2026-10-17): passing the no-mountain capture as -planet fails
// with "the mountain's shadow is missing from near (90 m map)".
func checkPlanet(planet, control image.Image) {
	shadowed := []struct {
		name string
		box  image.Rectangle
	}{
		{"near (90 m map)", image.Rect(370, 240, 430, 250)},
		{"past near depth (3 km map)", image.Rect(370, 181, 430, 186)},
		{"middle, z -235", image.Rect(370, 145, 430, 155)},
		{"middle, z -650", image.Rect(370, 95, 430, 105)},
		{"middle, z -1250", image.Rect(370, 68, 430, 74)},
	}
	for _, s := range shadowed {
		a, b := mean(planet, s.box), mean(control, s.box)
		fmt.Printf("planet %s: mountain %.2f, no mountain %.2f /255\n", s.name, a, b)
		if b < 100 || b-a < 30 {
			log.Fatalf("the mountain's shadow is missing from %s", s.name)
		}
	}
	beyond := image.Rect(370, 54, 430, 60)
	a, b := mean(planet, beyond), mean(control, beyond)
	fmt.Printf("planet beyond the shadow: mountain %.2f, no mountain %.2f /255\n", a, b)
	if math.Abs(a-b) > 2 || b < 100 {
		log.Fatal("ground past the mountain's shadow changed; the comparison is not isolating the mountain")
	}
}

// probeMetresPerPixel is the ground footprint of one pixel for each -probe
// tier: the camera looks straight down from 4, 100 and 1500 units with a
// 45-degree vertical field of view over 600 rows.
var probeMetresPerPixel = [3]float64{4, 100, 1500}

// checkProbe measures the mountain shadow's side edge in a -probe capture. The
// stock filter is four bilinear comparison taps a texel either side, so a
// straight edge aligned with the map's X axis ramps over three texels; the
// width between the last column on the shadowed plateau and the first on the
// lit one, over three, is the texel size. The one-level threshold trims the
// ramp ends, which reads a few percent short.
//
// Measured 2026-10-17: 46, 62 and 165 pixels, 0.0847, 2.854 and 113.9 m per
// texel against 0.0879, 2.930 and 117.19 from 2 * Radius / 2048.
//
// Verified break (2026-10-17): passing the 3 km probe as -probe0 fails with
// "probe 0: texel size 0.1141 m is not within 8% of 0.08789".
func checkProbe(tier int, img image.Image) {
	want := [3]float64{0.087890625, 2.9296875, 117.1875}[tier]
	mpp := 2 * probeMetresPerPixel[tier] * math.Tan(22.5*math.Pi/180) / 600
	w := img.Bounds().Dx()
	l := make([]float64, w)
	for x := range w {
		l[x] = mean(img, image.Rect(x, 200, x+1, 201))
	}
	med := func(s []float64) float64 {
		c := append([]float64(nil), s...)
		sort.Float64s(c)
		return c[len(c)/2]
	}
	left, right := med(l[:w/10]), med(l[w-w/10:])
	first, last := -1, -1
	for x := range w {
		if math.Abs(l[x]-left) > 1 {
			first = x
			break
		}
	}
	for x := w - 1; x >= 0; x-- {
		if math.Abs(l[x]-right) > 1 {
			last = x
			break
		}
	}
	if first < 0 || last <= first || right-left < 30 {
		log.Fatalf("probe %d: no shadow edge in the capture (plateaus %.1f, %.1f)", tier, left, right)
	}
	got := float64(last-first+1) * mpp / 3
	fmt.Printf("probe %d: edge %d px, %.4g m per texel, arithmetic %.4g\n", tier, last-first+1, got, want)
	if math.Abs(got-want)/want > 0.08 {
		log.Fatalf("probe %d: texel size %.4g m is not within 8%% of %.4g", tier, got, want)
	}
}
