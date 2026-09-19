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
// Verified to fail: swapping the header's GridW/GridH order in WriteTo (write
// GridH then GridW) made LoadHeightmap read the file back as a 3x5 grid
// instead of 5x3 and the test failed with "GridW = 3, want 5" before the
// write order was corrected back.
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
// Verified to fail: before this test existed, binary.Read into the
// heights slice already returned io.ErrUnexpectedEOF here (this specific
// case was not the bug), but the assertion below is what proves it stays
// that way -- see the grid-size and world-size tests for the cases that were
// actually silent.
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
// Verified to fail: with the maxHeightmapGridDim check removed, this either
// panicked with "runtime: makeslice: len out of range" (GridW*GridH as int64
// overflowing back negative) or, for the specific pair below, attempted to
// allocate a slice around 68 GiB (70368735608832 * 4 bytes) and the test
// process was killed by the OS rather than failing cleanly -- neither is
// something a caller loading a game asset can recover from.
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
