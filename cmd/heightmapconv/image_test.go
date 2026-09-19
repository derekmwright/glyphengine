package main

import (
	"encoding/binary"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"
)

// writeGray16PNG writes a 2x2 16-bit greyscale PNG with an asymmetric
// pattern -- all four corners different -- so a row or column flip in the
// reader shows up as a wrong-looking image, not a coincidentally-correct
// symmetric one.
func writeGray16PNG(t *testing.T, path string, tl, tr, bl, br uint16) {
	t.Helper()
	img := image.NewGray16(image.Rect(0, 0, 2, 2))
	img.SetGray16(0, 0, color.Gray16{Y: tl})
	img.SetGray16(1, 0, color.Gray16{Y: tr})
	img.SetGray16(0, 1, color.Gray16{Y: bl})
	img.SetGray16(1, 1, color.Gray16{Y: br})
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		t.Fatal(err)
	}
}

// TestReadPNGHeights16Bit checks the raw sample values readPNGHeights
// returns (row-major, row 0 first) match the PNG's own pixels exactly --
// the orientation FLIP (row 0 -> far edge) is heightsFromSamples's job, not
// this function's, and is tested separately so a bug in one is not masked
// by a compensating bug in the other.
func TestReadPNGHeights16Bit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "h.png")
	writeGray16PNG(t, path, 100, 200, 300, 40000)

	samples, w, h, warn, err := readPNGHeights(path)
	if err != nil {
		t.Fatalf("readPNGHeights: %v", err)
	}
	if warn != "" {
		t.Errorf("unexpected terracing warning on a 16-bit PNG: %s", warn)
	}
	if w != 2 || h != 2 {
		t.Fatalf("size = %dx%d, want 2x2", w, h)
	}
	want := []uint16{100, 200, 300, 40000} // row-major, row 0 first
	for i, v := range want {
		if samples[i] != v {
			t.Errorf("samples[%d] = %d, want %d", i, samples[i], v)
		}
	}
}

// TestReadPNGHeights8BitWarnsAndSpreads checks the 8-bit path both warns
// (the issue's explicit ask) and spreads its value across the full 16-bit
// range (v*257) rather than leaving it in the low byte, which would make an
// 8-bit source read as a nearly-flat (max value 255 of 65535) terrain.
func TestReadPNGHeights8BitWarnsAndSpreads(t *testing.T) {
	path := filepath.Join(t.TempDir(), "h8.png")
	img := image.NewGray(image.Rect(0, 0, 2, 1))
	img.SetGray(0, 0, color.Gray{Y: 0})
	img.SetGray(1, 0, color.Gray{Y: 255})
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := png.Encode(f, img); err != nil {
		t.Fatal(err)
	}
	f.Close()

	samples, _, _, warn, err := readPNGHeights(path)
	if err != nil {
		t.Fatalf("readPNGHeights: %v", err)
	}
	if warn == "" {
		t.Error("8-bit PNG produced no terracing warning, want one")
	}
	if samples[0] != 0 || samples[1] != 65535 {
		t.Errorf("samples = %v, want [0 65535] (v*257 spread across the 16-bit range)", samples)
	}
}

// TestReadRawHeightsEndianness covers both byte orders for the headerless
// .r16 format -- a mismatch here silently scrambles every sample rather than
// failing to load, so both directions are checked against a value picked to
// be very different in each order (0x00FF vs 0xFF00).
func TestReadRawHeightsEndianness(t *testing.T) {
	dir := t.TempDir()

	le := filepath.Join(dir, "le.r16")
	buf := make([]byte, 4)
	binary.LittleEndian.PutUint16(buf[0:2], 0x00FF)
	binary.LittleEndian.PutUint16(buf[2:4], 0xFF00)
	if err := os.WriteFile(le, buf, 0o644); err != nil {
		t.Fatal(err)
	}
	samples, err := readRawHeights(le, 2, 1, false)
	if err != nil {
		t.Fatalf("readRawHeights (LE): %v", err)
	}
	if samples[0] != 0x00FF || samples[1] != 0xFF00 {
		t.Errorf("LE samples = %v, want [0x00FF 0xFF00]", samples)
	}

	be := filepath.Join(dir, "be.r16")
	binary.BigEndian.PutUint16(buf[0:2], 0x00FF)
	binary.BigEndian.PutUint16(buf[2:4], 0xFF00)
	if err := os.WriteFile(be, buf, 0o644); err != nil {
		t.Fatal(err)
	}
	samples, err = readRawHeights(be, 2, 1, true)
	if err != nil {
		t.Fatalf("readRawHeights (BE): %v", err)
	}
	if samples[0] != 0x00FF || samples[1] != 0xFF00 {
		t.Errorf("BE samples = %v, want [0x00FF 0xFF00]", samples)
	}
}

// TestReadRawHeightsRejectsWrongSize: a .r16 file whose byte count does not
// match -rawsize is corrupt input (wrong dimensions, or not a raw heightmap
// at all) and must fail rather than silently read a truncated or
// out-of-bounds grid.
func TestReadRawHeightsRejectsWrongSize(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bad.r16")
	if err := os.WriteFile(path, []byte{1, 2, 3}, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := readRawHeights(path, 2, 2, false); err == nil {
		t.Error("readRawHeights on a 3-byte file for a 2x2 grid succeeded, want an error")
	}
}

// TestHeightsFromSamplesOrientation pins the documented convention: image
// row 0 (first in the file) lands at the FAR edge (grid iz = GridH-1), and
// image column 0 stays at grid ix = 0 with no flip. An asymmetric 2x2
// sample set (all four corners distinct) makes a row/column swap or a
// missing flip produce a visibly different grid rather than a
// coincidentally correct one.
//
// Verified to fail: see raster.go's heightsFromSamples for the break/restore
// record of removing the "h-1-row" flip.
func TestHeightsFromSamplesOrientation(t *testing.T) {
	// samples row-major, row 0 first: (0,0)=TL=10 (0,1)=TR=20
	//                                  (1,0)=BL=30 (1,1)=BR=40
	samples := []uint16{10, 20, 30, 40}
	heights := heightsFromSamples(samples, 2, 2, 0, 65535)

	// grid ix=0 (left column, unflipped): iz=0 (near/min Z) should be the
	// image's BOTTOM-left sample (row 1, col 0) = 30; iz=1 (far/max Z)
	// should be the image's TOP-left sample (row 0, col 0) = 10.
	get := func(ix, iz int) float32 { return heights[iz*2+ix] }
	cases := []struct {
		ix, iz int
		want   float32
	}{
		{0, 0, 30}, // bottom-left image pixel -> near edge
		{1, 0, 40}, // bottom-right -> near edge
		{0, 1, 10}, // top-left -> far edge
		{1, 1, 20}, // top-right -> far edge
	}
	for _, c := range cases {
		if got := get(c.ix, c.iz); got != c.want {
			t.Errorf("grid(ix=%d,iz=%d) = %g, want %g", c.ix, c.iz, got, c.want)
		}
	}
}

// TestHeightsFromSamplesRange checks sample 0 and sample 65535 map to
// rangeMin/rangeMax exactly, and a mid sample lands proportionally between.
func TestHeightsFromSamplesRange(t *testing.T) {
	samples := []uint16{0, 65535, 32768, 0}
	heights := heightsFromSamples(samples, 2, 2, -10, 30)
	if heights[2] != -10 { // row 1 (index 2,3) -> iz=0 (flipped)
		t.Errorf("sample 0 -> %g, want -10 (rangeMin)", heights[2])
	}
	if heights[3] != 30 {
		t.Errorf("sample 65535 -> %g, want 30 (rangeMax)", heights[3])
	}
	// 32768/65535 ~= 0.5000076, over a span of 40 -> ~10.0003
	mid := heights[0]
	if mid < 9.9 || mid > 10.1 {
		t.Errorf("sample 32768 -> %g, want ~10 (midpoint of -10..30)", mid)
	}
}
