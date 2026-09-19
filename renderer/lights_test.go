package renderer

import (
	"encoding/binary"
	"math"
	"testing"

	"github.com/derekmwright/glyphengine/renderer/lightcluster"
)

// TestPackLightHeaderLayout pins the LightBuffer header's byte layout against
// the std430 declaration in shaders/lights.inc: four consecutive 16-byte
// fields (grid, zParams, screen, flags), including the derived screen.zw =
// grid.xy / framebuffer size the shader's cluster lookup depends on.
//
// The Mapping is built by hand rather than taken from lightcluster.NewMapping,
// so this stays a test of the byte layout: a change to how the binner derives
// its slice scale should be caught by that package's tests, not by this one
// going red for a reason that has nothing to do with bytes.
//
// Verified to catch a real mistake: swapping which of screenW/screenH is
// written to dst[32:36] vs dst[36:40] makes this fail on screen.x/screen.y
// immediately (720 where 1280 was wanted, and vice versa) rather than
// passing on a coincidence.
func TestPackLightHeaderLayout(t *testing.T) {
	buf := make([]byte, lightHeaderSize)
	m := lightcluster.Mapping{
		Grid:         lightcluster.Grid{X: 16, Y: 9, Z: 24},
		ScreenScaleX: 16.0 / 1280,
		ScreenScaleY: 9.0 / 720,
		SliceScale:   1.5,
		SliceBias:    -2.5,
	}
	packLightHeader(buf, m, 200, 1280, 720, LightFlagHeatmap)

	u32 := func(off int) uint32 { return binary.LittleEndian.Uint32(buf[off:]) }
	f32 := func(off int) float32 { return math.Float32frombits(u32(off)) }

	if got := u32(0); got != 16 {
		t.Errorf("grid.x = %d, want 16", got)
	}
	if got := u32(4); got != 9 {
		t.Errorf("grid.y = %d, want 9", got)
	}
	if got := u32(8); got != 24 {
		t.Errorf("grid.z = %d, want 24", got)
	}
	if got := u32(12); got != 200 {
		t.Errorf("grid.w (numLights) = %d, want 200", got)
	}
	if got := f32(16); got != 1.5 {
		t.Errorf("zParams.x (scale) = %g, want 1.5", got)
	}
	if got := f32(20); got != -2.5 {
		t.Errorf("zParams.y (bias) = %g, want -2.5", got)
	}
	if got, want := f32(24), float32(lightcluster.MaxLightsPerCell); got != want {
		t.Errorf("zParams.z (per-cell cap) = %g, want %g", got, want)
	}
	if got := f32(32); got != 1280 {
		t.Errorf("screen.x = %g, want 1280", got)
	}
	if got := f32(36); got != 720 {
		t.Errorf("screen.y = %g, want 720", got)
	}
	if got, want := f32(40), float32(16)/1280; got != want {
		t.Errorf("screen.z (grid.x/screen.x) = %g, want %g", got, want)
	}
	if got, want := f32(44), float32(9)/720; got != want {
		t.Errorf("screen.w (grid.y/screen.y) = %g, want %g", got, want)
	}
	if got := u32(48); got != LightFlagHeatmap {
		t.Errorf("flags.x = %#x, want %#x", got, LightFlagHeatmap)
	}
}

// TestPackLightsRoundTrip packs GpuLights and decodes them back byte for
// byte, pinning field order (posRange, color, dirCone, params) and the
// 64-byte stride the shader's std430 array indexing assumes.
//
// The stride is the half of this worth having. A field-order mistake shows up
// as a light in the wrong place or the wrong colour, which is visible in the
// first capture anyone takes. A stride mistake does not: light 0 reads
// correctly and every later light reads a window sliding further into its
// neighbour, so a one-light scene is perfect and a scene full of lamps
// flickers as the binner reorders them. Verified to catch it: leaving
// gpuLightSize at 48 while packLights writes four vec4s fails here on light
// 1's posRange, at byte 48, before any capture is taken.
func TestPackLightsRoundTrip(t *testing.T) {
	lights := []GpuLight{
		{PosRange: [4]float32{1, 2, 3, 4}, Color: [4]float32{0.1, 0.2, 0.3, 0.4}, DirCone: [4]float32{0, 0, 0, 0}},
		{PosRange: [4]float32{-5, 6, -7, 8}, Color: [4]float32{0.5, 0.6, 0.7, 0.94}, DirCone: [4]float32{0, -1, 0, 0.87}, Params: [4]float32{1.5, 0, 0, 0}},
	}
	buf := make([]byte, len(lights)*gpuLightSize)
	if n := packLights(buf, lights); n != len(lights) {
		t.Fatalf("packLights wrote %d lights, want %d", n, len(lights))
	}

	f32 := func(off int) float32 { return math.Float32frombits(binary.LittleEndian.Uint32(buf[off:])) }
	for i, want := range lights {
		base := i * gpuLightSize
		for c := 0; c < 4; c++ {
			if got := f32(base + c*4); got != want.PosRange[c] {
				t.Errorf("light %d posRange[%d] = %g, want %g", i, c, got, want.PosRange[c])
			}
			if got := f32(base + 16 + c*4); got != want.Color[c] {
				t.Errorf("light %d color[%d] = %g, want %g", i, c, got, want.Color[c])
			}
			if got := f32(base + 32 + c*4); got != want.DirCone[c] {
				t.Errorf("light %d dirCone[%d] = %g, want %g", i, c, got, want.DirCone[c])
			}
			if got := f32(base + 48 + c*4); got != want.Params[c] {
				t.Errorf("light %d params[%d] = %g, want %g", i, c, got, want.Params[c])
			}
		}
	}

	// The stride the shader indexes with, asserted as a number rather than
	// inferred from the offsets above: those are all written relative to
	// gpuLightSize, so a wrong stride moves the expectations with it and the
	// loop keeps passing.
	if gpuLightSize != 64 {
		t.Errorf("gpuLightSize = %d, want 64 -- shaders/lights.inc's GpuLight is four vec4s", gpuLightSize)
	}

	// Overrun: a buffer too small for every light must truncate rather than
	// write past dst. Broken by removing the limit clamp in packLights --
	// this then panics on an out-of-range slice write instead of returning 1.
	small := make([]byte, gpuLightSize)
	if n := packLights(small, lights); n != 1 {
		t.Errorf("packLights on a 1-light buffer wrote %d, want 1", n)
	}
}

// TestPackCellsAndIndices covers the two simpler SSBOs the same way: exact
// byte layout, and truncation instead of an overrun when the destination is
// smaller than the input.
func TestPackCellsAndIndices(t *testing.T) {
	cells := []LightGridCell{{Offset: 0, Count: 3}, {Offset: 3, Count: 5}}
	buf := make([]byte, len(cells)*lightCellSize)
	if n := packCells(buf, cells); n != len(cells) {
		t.Fatalf("packCells wrote %d, want %d", n, len(cells))
	}
	if got := binary.LittleEndian.Uint32(buf[0:]); got != 0 {
		t.Errorf("cell 0 offset = %d, want 0", got)
	}
	if got := binary.LittleEndian.Uint32(buf[4:]); got != 3 {
		t.Errorf("cell 0 count = %d, want 3", got)
	}
	if got := binary.LittleEndian.Uint32(buf[8:]); got != 3 {
		t.Errorf("cell 1 offset = %d, want 3", got)
	}
	if got := binary.LittleEndian.Uint32(buf[12:]); got != 5 {
		t.Errorf("cell 1 count = %d, want 5", got)
	}
	if n := packCells(make([]byte, lightCellSize), cells); n != 1 {
		t.Errorf("packCells on a 1-cell buffer wrote %d, want 1", n)
	}

	indices := []uint32{7, 9, 42}
	ibuf := make([]byte, len(indices)*4)
	if n := packIndices(ibuf, indices); n != len(indices) {
		t.Fatalf("packIndices wrote %d, want %d", n, len(indices))
	}
	for i, want := range indices {
		if got := binary.LittleEndian.Uint32(ibuf[i*4:]); got != want {
			t.Errorf("index %d = %d, want %d", i, got, want)
		}
	}
	if n := packIndices(make([]byte, 4), indices); n != 1 {
		t.Errorf("packIndices on a 1-uint buffer wrote %d, want 1", n)
	}
}
