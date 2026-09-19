package glyphengine

import (
	"bytes"
	"encoding/binary"
	"math"
	"testing"
	"testing/fstest"
)

// TestHeightmapRoundTrips writes a heightmap with WriteTo and reads it back
// with LoadHeightmap through an in-memory fs.FS, asserting every field
// including the height data survives unchanged. Non-square (5x3) and a
// negative origin on purpose: a square grid with an origin at zero would not
// catch a writer that swapped GridW/GridH or dropped the origin's sign.
//
// Verified to fail: swapping which value WriteTo puts in the header's
// GridW/GridH slots (writing h.GridH into the GridW slot and vice versa)
// made LoadHeightmap read the 5x3 grid back as 3x5, and the test printed
// "grid = 3x5, want 5x3" before the swap was reverted.
func TestHeightmapRoundTrips(t *testing.T) {
	want := &Heightmap{
		GridW: 5, GridH: 3,
		WorldW: 40, WorldD: 18,
		OriginX: -15.5, OriginZ: 7.25,
		Heights: []float32{
			0, 1, 2, 3, 4,
			5, 6, 7, 8, 9,
			10, 11, 12, 13, 14,
		},
	}

	var buf bytes.Buffer
	n, err := want.WriteTo(&buf)
	if err != nil {
		t.Fatalf("WriteTo: %v", err)
	}
	wantBytes := int64(4*4 + 2*4 + len(want.Heights)*4) // 2 u32 + 4 f32 header + heights
	if n != wantBytes {
		t.Errorf("WriteTo wrote %d bytes, want %d", n, wantBytes)
	}
	if int64(buf.Len()) != n {
		t.Errorf("buffer holds %d bytes, WriteTo reported %d", buf.Len(), n)
	}

	fsys := fstest.MapFS{
		"t.heightmap": {Data: buf.Bytes()},
	}
	got, err := LoadHeightmap(fsys, "t.heightmap")
	if err != nil {
		t.Fatalf("LoadHeightmap: %v", err)
	}

	if got.GridW != want.GridW || got.GridH != want.GridH {
		t.Errorf("grid = %dx%d, want %dx%d", got.GridW, got.GridH, want.GridW, want.GridH)
	}
	if got.WorldW != want.WorldW || got.WorldD != want.WorldD {
		t.Errorf("world size = %gx%g, want %gx%g", got.WorldW, got.WorldD, want.WorldW, want.WorldD)
	}
	if got.OriginX != want.OriginX || got.OriginZ != want.OriginZ {
		t.Errorf("origin = %g,%g, want %g,%g", got.OriginX, got.OriginZ, want.OriginX, want.OriginZ)
	}
	if len(got.Heights) != len(want.Heights) {
		t.Fatalf("got %d heights, want %d", len(got.Heights), len(want.Heights))
	}
	for i := range want.Heights {
		if got.Heights[i] != want.Heights[i] {
			t.Errorf("height[%d] = %g, want %g", i, got.Heights[i], want.Heights[i])
		}
	}
}

// TestHeightmapWriteToRejectsMismatchedHeights: a Heightmap built by struct
// literal (skipping NewHeightmap's validation) with the wrong number of
// heights must not silently write a file whose body does not match its own
// header -- LoadHeightmap would then either read garbage into the last rows
// or fail with an opaque EOF far from the actual mistake.
//
// Verified to fail: with the length check removed, this printed "WriteTo
// with 3 heights for a 4x4 grid succeeded, want an error".
func TestHeightmapWriteToRejectsMismatchedHeights(t *testing.T) {
	h := &Heightmap{GridW: 4, GridH: 4, WorldW: 10, WorldD: 10, Heights: make([]float32, 3)}
	if _, err := h.WriteTo(&bytes.Buffer{}); err == nil {
		t.Error("WriteTo with 3 heights for a 4x4 grid succeeded, want an error")
	}
}

// TestHeightmapWriteToRejectsBadWorldSize covers the same divide-by-zero
// hazard on the write side that LoadHeightmap's header validation covers on
// the read side: a zero, negative, or non-finite world size is not something
// HeightAt can divide by.
//
// Verified to fail: with WriteTo's validWorldSize check removed, all four
// subtests printed "WriteTo with world size ... succeeded, want an error"
// (e.g. "WriteTo with world size 0x10 succeeded" for the zero-width case).
func TestHeightmapWriteToRejectsBadWorldSize(t *testing.T) {
	base := Heightmap{GridW: 2, GridH: 2, OriginX: 0, OriginZ: 0, Heights: make([]float32, 4)}
	cases := []struct {
		name           string
		worldW, worldD float32
	}{
		{"zero width", 0, 10},
		{"negative depth", 10, -1},
		{"NaN width", float32(math.NaN()), 10},
		{"+Inf depth", 10, float32(math.Inf(1))},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := base
			h.WorldW, h.WorldD = c.worldW, c.worldD
			if _, err := h.WriteTo(&bytes.Buffer{}); err == nil {
				t.Errorf("WriteTo with world size %gx%g succeeded, want an error", c.worldW, c.worldD)
			}
		})
	}
}

// TestLoadHeightmapRejectsTruncatedFile: a file that ends partway through the
// height data (a copy that was cut short, a truncated download) must fail to
// load rather than hand back a Heightmap whose tail is implicitly zeroed or
// whose Heights slice is shorter than GridW*GridH -- either of those panics
// or silently flattens terrain the first time something indexes past the
// short slice.
//
// Verified to fail: ignoring binary.Read's error on the heights slice (a
// bare `binary.Read(f, binary.LittleEndian, heights)` with no err check)
// made LoadHeightmap return a Heightmap with the trailing heights left at
// their zero value instead of an error, and this test printed "LoadHeightmap
// on a truncated file succeeded, want an error".
func TestLoadHeightmapRejectsTruncatedFile(t *testing.T) {
	full := &Heightmap{
		GridW: 4, GridH: 4, WorldW: 10, WorldD: 10,
		Heights: make([]float32, 16),
	}
	for i := range full.Heights {
		full.Heights[i] = float32(i)
	}
	var buf bytes.Buffer
	if _, err := full.WriteTo(&buf); err != nil {
		t.Fatalf("WriteTo: %v", err)
	}

	truncated := buf.Bytes()[:buf.Len()-10] // cut off partway through the heights
	fsys := fstest.MapFS{"t.heightmap": {Data: truncated}}
	if _, err := LoadHeightmap(fsys, "t.heightmap"); err == nil {
		t.Error("LoadHeightmap on a truncated file succeeded, want an error")
	}
}

// TestLoadHeightmapRejectsTruncatedHeader: fewer than the 24 header bytes
// (e.g. an empty or near-empty file) must fail cleanly.
//
// This case turns out to be over-determined: binary.Read on a short header
// already returns an error, and even ignoring THAT error leaves header at
// its zero value, which the GridW/GridH floor and the WorldW/WorldD
// validity check both separately reject. Verified to fail only when all
// three guards were removed at once (the header read's own error check, the
// 2x2 floor, and validWorldSize) -- with any one of them still in place this
// test kept passing on the zero header alone, which is why the grid-size and
// world-size tests above exist as their own, independently-verified checks
// rather than this test standing in for them. With all three gone, this
// printed "LoadHeightmap on a 3-byte file succeeded, want an error".
func TestLoadHeightmapRejectsTruncatedHeader(t *testing.T) {
	fsys := fstest.MapFS{"t.heightmap": {Data: []byte{1, 2, 3}}}
	if _, err := LoadHeightmap(fsys, "t.heightmap"); err == nil {
		t.Error("LoadHeightmap on a 3-byte file succeeded, want an error")
	}
}

// TestLoadHeightmapRejectsUndersizedGrid: a grid smaller than 2x2 (including
// the all-zero header a fully truncated/garbage file reads as) has no cell
// for HeightAt or TerrainMesh to interpolate across.
//
// Verified to fail: with the GridW/GridH < 2 check removed, this loaded
// successfully into a Heightmap{GridW: 0, GridH: 0} and the test failed with
// "LoadHeightmap on a 0x0 grid succeeded, want an error"; downstream,
// TerrainMesh's stepX := h.WorldW / float32(h.GridW-1) divides by -1 instead
// of erroring here where the bad header actually is.
func TestLoadHeightmapRejectsUndersizedGrid(t *testing.T) {
	header := struct {
		GridW, GridH     uint32
		WorldW, WorldD   float32
		OriginX, OriginZ float32
	}{GridW: 0, GridH: 0, WorldW: 10, WorldD: 10}
	var buf bytes.Buffer
	if err := binary.Write(&buf, binary.LittleEndian, header); err != nil {
		t.Fatal(err)
	}
	fsys := fstest.MapFS{"t.heightmap": {Data: buf.Bytes()}}
	if _, err := LoadHeightmap(fsys, "t.heightmap"); err == nil {
		t.Error("LoadHeightmap on a 0x0 grid succeeded, want an error")
	}
}

// TestLoadHeightmapRejectsAbsurdGrid is the case the issue asked about
// directly: a corrupt or misaligned header whose GridW/GridH land near
// 0xFFFFFFFF must not make LoadHeightmap try to allocate the resulting grid.
//
// Verified to fail: with the maxHeightmapGridDim check removed, LoadHeightmap
// panicked with "runtime error: makeslice: len out of range" -- GridW*GridH
// as int64 (18446744065119617025) overflows back to -8589934591, and
// make([]float32, count) panics on the negative length rather than the
// header being rejected here where the bad data actually is. A GridW/GridH
// pair that stayed positive after overflow would instead attempt a
// multi-gigabyte allocation, which is the case maxHeightmapGridDim's own
// comment describes; this pair panics instead, which is not something a
// caller loading a game asset can recover from either.
func TestLoadHeightmapRejectsAbsurdGrid(t *testing.T) {
	header := struct {
		GridW, GridH     uint32
		WorldW, WorldD   float32
		OriginX, OriginZ float32
	}{GridW: 0xFFFFFFFF, GridH: 0xFFFFFFFF, WorldW: 10, WorldD: 10}
	var buf bytes.Buffer
	if err := binary.Write(&buf, binary.LittleEndian, header); err != nil {
		t.Fatal(err)
	}
	fsys := fstest.MapFS{"t.heightmap": {Data: buf.Bytes()}}
	if _, err := LoadHeightmap(fsys, "t.heightmap"); err == nil {
		t.Error("LoadHeightmap on a 0xFFFFFFFFx0xFFFFFFFF grid succeeded, want an error")
	}
}

// Verified to fail (LoadHeightmap side): with the validWorldSize check
// removed, this printed "LoadHeightmap with WorldW=0 succeeded, want an
// error".
//
// TestLoadHeightmapRejectsBadWorldSize is LoadHeightmap's side of
// TestHeightmapWriteToRejectsBadWorldSize: even if a file was not produced by
// WriteTo (hand-crafted, or written by a future tool with a bug), a zero,
// negative or non-finite world size must not load successfully into a
// Heightmap that then hands back NaN/Inf from every HeightAt call.
func TestLoadHeightmapRejectsBadWorldSize(t *testing.T) {
	header := struct {
		GridW, GridH     uint32
		WorldW, WorldD   float32
		OriginX, OriginZ float32
	}{GridW: 2, GridH: 2, WorldW: 0, WorldD: 10}
	var buf bytes.Buffer
	if err := binary.Write(&buf, binary.LittleEndian, header); err != nil {
		t.Fatal(err)
	}
	if err := binary.Write(&buf, binary.LittleEndian, make([]float32, 4)); err != nil {
		t.Fatal(err)
	}
	fsys := fstest.MapFS{"t.heightmap": {Data: buf.Bytes()}}
	if _, err := LoadHeightmap(fsys, "t.heightmap"); err == nil {
		t.Error("LoadHeightmap with WorldW=0 succeeded, want an error")
	}
}
