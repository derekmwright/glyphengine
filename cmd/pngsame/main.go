// Command pngsame reports whether two PNG files hold the same pixels.
//
// Byte comparison of PNGs is the wrong oracle across toolchains: Go 1.27's
// encoder emits different bytes than 1.26's for identical pixels, and the
// dependency bump that moved this repository to 1.27 rewrote all 22
// documentation images with zero pixel differences. Compare pixels.
//
// The count it prints is in pixels first and channel samples second, because
// every figure recorded about a render here is in pixels.
//
//	go run ./cmd/pngsame old.png new.png   # exit 0 when identical
package main

import (
	"fmt"
	"image/png"
	"os"
)

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: pngsame a.png b.png")
		os.Exit(2)
	}
	a, err := load(os.Args[1])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	b, err := load(os.Args[2])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	if a.w != b.w || a.h != b.h {
		fmt.Printf("%s %dx%d vs %s %dx%d: sizes differ\n", os.Args[1], a.w, a.h, os.Args[2], b.w, b.h)
		os.Exit(1)
	}
	// Counted two ways on purpose. Every measurement written down about a
	// render in this repository -- issue #40's "382205 / 921600 pixels", the
	// provocation table in docs/agents/state-trace.md -- is a count of
	// PIXELS, and this reported only channel samples, which is up to four
	// times larger. Two numbers that are not comparable are worse than one,
	// so both are printed and each is labelled.
	differing, pixelsDiffering, maxDelta := 0, 0, 0
	for i := range a.pix {
		d := int(a.pix[i]) - int(b.pix[i])
		if d < 0 {
			d = -d
		}
		if d != 0 {
			differing++
			if d > maxDelta {
				maxDelta = d
			}
		}
	}
	// A pixel counts once however many of its four channels moved.
	for i := 0; i+3 < len(a.pix); i += 4 {
		if a.pix[i] != b.pix[i] || a.pix[i+1] != b.pix[i+1] ||
			a.pix[i+2] != b.pix[i+2] || a.pix[i+3] != b.pix[i+3] {
			pixelsDiffering++
		}
	}
	if differing == 0 {
		fmt.Printf("%s and %s: identical pixels (%dx%d)\n", os.Args[1], os.Args[2], a.w, a.h)
		return
	}
	fmt.Printf("%s and %s: %d of %d pixels differ (%.2f %%), %d of %d channel samples, max delta %d/255\n",
		os.Args[1], os.Args[2], pixelsDiffering, a.w*a.h,
		100*float64(pixelsDiffering)/float64(a.w*a.h), differing, len(a.pix), maxDelta)
	os.Exit(1)
}

type pixels struct {
	w, h int
	pix  []uint8
}

func load(path string) (pixels, error) {
	f, err := os.Open(path)
	if err != nil {
		return pixels{}, err
	}
	defer f.Close()
	img, err := png.Decode(f)
	if err != nil {
		return pixels{}, fmt.Errorf("%s: %w", path, err)
	}
	b := img.Bounds()
	p := pixels{w: b.Dx(), h: b.Dy(), pix: make([]uint8, 0, b.Dx()*b.Dy()*4)}
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			r, g, bl, a := img.At(x, y).RGBA()
			p.pix = append(p.pix, uint8(r>>8), uint8(g>>8), uint8(bl>>8), uint8(a>>8))
		}
	}
	return p, nil
}
