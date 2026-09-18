package renderer

import (
	"reflect"
	"strings"
	"testing"
)

// testFont is a font with no atlas behind it: the geometry builder only reads
// metrics, which is what lets these tests run on a machine with no GPU.
func testFont() *Font {
	f := &Font{
		Glyphs:   map[rune]Glyph{},
		Ascender: 0.95,
		PxPerEM:  32,
		PxRange:  4,
		BoldBias: 0.02,
	}
	for i, ch := range "abcdefghijklmnopqrstuvwxyz0123456789:./%-" {
		u := float32(i%8) / 8
		v := float32(i/8) / 8
		f.Glyphs[ch] = Glyph{
			Advance:     0.5 + float32(i%5)*0.03,
			PlaneBounds: [4]float32{0.04, -0.1, 0.46, 0.7},
			AtlasBounds: [4]float32{u, v, u + 0.1, v + 0.1},
		}
	}
	// A glyph that advances the cursor and draws nothing, like a space.
	f.Glyphs[' '] = Glyph{Advance: 0.25}
	return f
}

// hudLines is shaped like a game's readout: several short lines, mixed sizes,
// one right-aligned, one with a rune the font does not have.
func hudLines(tick int) []TextLine {
	n := func(v int) string { return strings.Repeat("0", v%4) + "42.7%" }
	return []TextLine{
		{Text: "power " + n(tick), X: 12, Y: 10, Scale: 18, Color: [3]float32{1, 1, 1}},
		{Text: "water " + n(tick+1), X: 12, Y: 32, Scale: 18, Color: [3]float32{0.6, 0.8, 1}},
		{Text: "colonists 128 / 160", X: -12, Y: 10, Scale: 14, Color: [3]float32{1, 0.9, 0.6}, Alpha: 0.8},
		{Text: "день 12", X: 12, Y: 54, Scale: 22, Color: [3]float32{1, 1, 1}},
	}
}

// legacyMSDFGeometry is SetText's body as it was: grown from nil, built in full
// and truncated afterwards. It is here as the oracle for "the output did not
// change", which is the half of this fix that a zero-allocation assertion says
// nothing about.
func legacyMSDFGeometry(font *Font, lines []TextLine, screenW float32) ([]Vertex, []uint16) {
	var vertices []Vertex
	var indices []uint16
	for _, line := range lines {
		fontSize := line.Scale
		cellW := fontSize
		textW := float32(0)
		for _, ch := range line.Text {
			if g, ok := font.Glyphs[ch]; ok {
				textW += g.Advance * fontSize
			} else {
				textW += cellW * 0.5
			}
		}
		cursorX := line.X
		if cursorX < 0 {
			cursorX = screenW - textW + cursorX
		}
		cursorY := line.Y
		for _, ch := range line.Text {
			g, ok := font.Glyphs[ch]
			if !ok {
				cursorX += cellW * 0.5
				continue
			}
			if g.PlaneBounds != [4]float32{} {
				x0 := cursorX + g.PlaneBounds[0]*fontSize
				y0 := cursorY + (font.Ascender-g.PlaneBounds[3])*fontSize
				x1 := cursorX + g.PlaneBounds[2]*fontSize
				y1 := cursorY + (font.Ascender-g.PlaneBounds[1])*fontSize
				uLeft := g.AtlasBounds[0]
				vBottom := 1.0 - g.AtlasBounds[1]
				uRight := g.AtlasBounds[2]
				vTop := 1.0 - g.AtlasBounds[3]
				alpha := line.Alpha
				if alpha == 0 {
					alpha = 1.0
				}
				spr := fontSize / font.PxPerEM * font.PxRange
				norm := [3]float32{alpha, spr, font.BoldBias}
				base := uint16(len(vertices))
				vertices = append(vertices,
					Vertex{Pos: [3]float32{x0, y0, 0}, Color: line.Color, Normal: norm, UV: [2]float32{uLeft, vTop}},
					Vertex{Pos: [3]float32{x1, y0, 0}, Color: line.Color, Normal: norm, UV: [2]float32{uRight, vTop}},
					Vertex{Pos: [3]float32{x1, y1, 0}, Color: line.Color, Normal: norm, UV: [2]float32{uRight, vBottom}},
					Vertex{Pos: [3]float32{x0, y1, 0}, Color: line.Color, Normal: norm, UV: [2]float32{uLeft, vBottom}},
				)
				indices = append(indices, base, base+1, base+2, base+2, base+3, base)
			}
			cursorX += g.Advance * fontSize
		}
	}
	if len(vertices) > msdfMaxVerts {
		vertices = vertices[:msdfMaxVerts]
		indices = indices[:msdfMaxIndices]
	}
	return vertices, indices
}

// TestMSDFTextRebuildDoesNotAllocate is the reported bug. A text overlay is rebuilt
// every frame, and building its two slices from nil each time was 73% of a
// consumer game's total allocation.
//
// It drives MSDFText.rebuild -- everything SetText does short of the upload --
// rather than the builder on its own, because the bug was never that the
// geometry COULD not be built without allocating; it was that SetText threw its
// buffers away. A test of the builder alone stays green with that bug put back.
//
// Verified to fail that way: with rebuild handing the builder nil, nil again it
// reports 13 allocations per call for this four-line readout.
func TestMSDFTextRebuildDoesNotAllocate(t *testing.T) {
	text := &MSDFText{font: testFont()}

	// The first build is allowed to grow the buffers; every one after it is not,
	// including ones whose text is a different length.
	text.rebuild(hudLines(3), 1280)

	tick := 0
	lines := hudLines(0)
	allocs := testing.AllocsPerRun(200, func() {
		tick++
		// Vary the readout the way a live one does, without allocating the
		// input inside the measured function: reuse the slice, change a line.
		lines[0].Scale = 18 + float32(tick%3)
		text.rebuild(lines, 1280)
	})
	if allocs != 0 {
		t.Fatalf("rebuilding the text geometry allocates %v times per call, want 0", allocs)
	}
	if len(text.verts) == 0 || len(text.idx) == 0 {
		t.Fatal("rebuild produced no geometry, so the zero above means nothing")
	}
}

// TestMSDFGeometryMatchesTheOldBuilder: reusing buffers must not change a single
// vertex. It also covers the two ways a retained buffer classically goes wrong --
// leftovers from a longer previous string, and a second overlay's text bleeding
// in -- by rebuilding into the SAME buffers in a deliberately awkward order.
func TestMSDFGeometryMatchesTheOldBuilder(t *testing.T) {
	font := testFont()
	var verts []Vertex
	var idx []uint16

	long := []TextLine{{Text: strings.Repeat("colony status nominal ", 40), X: 4, Y: 4, Scale: 16, Color: [3]float32{1, 1, 1}}}
	inputs := [][]TextLine{
		hudLines(1),
		long,                                   // grows the buffers well past the next one
		hudLines(2),                            // shorter: nothing of `long` may survive
		nil,                                    // no text at all
		{{Text: "   ", X: 0, Y: 0, Scale: 12}}, // only glyphs that draw nothing
		hudLines(7),
	}
	for i, lines := range inputs {
		verts, idx = appendMSDFGeometry(verts[:0], idx[:0], font, lines, 1280)
		wantV, wantI := legacyMSDFGeometry(font, lines, 1280)
		if len(verts) != len(wantV) || len(idx) != len(wantI) {
			t.Fatalf("input %d: %d verts / %d indices, want %d / %d", i, len(verts), len(idx), len(wantV), len(wantI))
		}
		if len(wantV) > 0 && (!reflect.DeepEqual(verts, wantV) || !reflect.DeepEqual(idx, wantI)) {
			t.Fatalf("input %d: geometry differs from the old builder's", i)
		}
	}
}

// TestMSDFGeometryStopsAtTheMeshCapacity: text longer than the mesh can hold
// gives the same first msdfMaxChars glyphs the old build-then-truncate did, and
// the retained buffer does not balloon to fit the string it could not draw.
func TestMSDFGeometryStopsAtTheMeshCapacity(t *testing.T) {
	font := testFont()
	huge := []TextLine{{Text: strings.Repeat("abcdefgh", msdfMaxChars), X: 0, Y: 0, Scale: 10, Color: [3]float32{1, 1, 1}}}

	verts, idx := appendMSDFGeometry(nil, nil, font, huge, 1280)
	wantV, wantI := legacyMSDFGeometry(font, huge, 1280)

	if len(verts) != msdfMaxVerts || len(idx) != msdfMaxIndices {
		t.Fatalf("got %d verts / %d indices, want the cap %d / %d", len(verts), len(idx), msdfMaxVerts, msdfMaxIndices)
	}
	if !reflect.DeepEqual(verts, wantV) || !reflect.DeepEqual(idx, wantI) {
		t.Fatal("the capped geometry differs from the old builder's truncated output")
	}
	for i, v := range idx {
		if int(v) >= len(verts) {
			t.Fatalf("index %d refers to vertex %d of %d", i, v, len(verts))
		}
	}
	// The old builder grew to fit all 16384 glyphs before throwing seven
	// eighths of them away; a retained buffer must not keep that.
	if cap(verts) > 2*msdfMaxVerts {
		t.Errorf("retained vertex capacity is %d for a mesh that holds %d", cap(verts), msdfMaxVerts)
	}
}

func BenchmarkMSDFGeometry(b *testing.B) {
	font := testFont()
	lines := hudLines(0)
	b.Run("retained", func(b *testing.B) {
		var verts []Vertex
		var idx []uint16
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			verts, idx = appendMSDFGeometry(verts[:0], idx[:0], font, lines, 1280)
		}
	})
	b.Run("from nil, as before", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			legacyMSDFGeometry(font, lines, 1280)
		}
	})
}
