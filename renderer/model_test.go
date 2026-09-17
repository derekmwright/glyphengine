package renderer

import "testing"

// These run without a device: the merge and the bounds are arithmetic over the
// decoded arrays, and nothing here records a command or uploads anything.

func tri(x float32, color [3]float32) ModelMesh {
	return ModelMesh{
		BaseColor: [3]float32{1, 1, 1},
		Verts: []Vertex{
			{Pos: [3]float32{x, 0, 0}, Color: color},
			{Pos: [3]float32{x + 1, 0, 0}, Color: color},
			{Pos: [3]float32{x, 1, 0}, Color: color},
		},
		Idx: []uint32{0, 1, 2},
	}
}

// The offset is the whole of the merge. Getting it wrong produces a mesh that
// still draws, with triangles reaching into the wrong primitive's vertices —
// which looks like a modelling mistake rather than a loader one.
func TestMergeGeometryOffsetsIndices(t *testing.T) {
	meshes := []ModelMesh{
		tri(0, [3]float32{1, 1, 1}),
		tri(10, [3]float32{1, 1, 1}),
		tri(20, [3]float32{1, 1, 1}),
	}

	verts, idx := mergeGeometry(meshes)

	if len(verts) != 9 {
		t.Fatalf("got %d vertices, want 9", len(verts))
	}
	want := []uint32{0, 1, 2, 3, 4, 5, 6, 7, 8}
	if len(idx) != len(want) {
		t.Fatalf("got %d indices, want %d", len(idx), len(want))
	}
	for i := range want {
		if idx[i] != want[i] {
			t.Fatalf("indices = %v, want %v", idx, want)
		}
	}

	// Every index has to address a vertex that exists, which is the property
	// the offset arithmetic is there to preserve.
	for i, n := range idx {
		if int(n) >= len(verts) {
			t.Errorf("index %d is %d, past the %d merged vertices", i, n, len(verts))
		}
	}

	// And the triangles must still point at their own primitive's geometry:
	// the third triangle's vertices came from x=20.
	if got := verts[idx[6]].Pos[0]; got != 20 {
		t.Errorf("third triangle's first vertex is at x=%v, want 20 — indices crossed primitives", got)
	}
}

// The merged mesh has one material where the source had several, so a model
// coloured by material factors rather than a texture would come out white.
func TestMergeGeometryBakesBaseColour(t *testing.T) {
	red := tri(0, [3]float32{1, 1, 1})
	red.BaseColor = [3]float32{1, 0, 0}
	blue := tri(10, [3]float32{1, 1, 1})
	blue.BaseColor = [3]float32{0, 0, 1}

	verts, _ := mergeGeometry([]ModelMesh{red, blue})

	if got := verts[0].Color; got != [3]float32{1, 0, 0} {
		t.Errorf("first primitive's colour is %v, want the red base colour baked in", got)
	}
	if got := verts[3].Color; got != [3]float32{0, 0, 1} {
		t.Errorf("second primitive's colour is %v, want the blue base colour baked in", got)
	}
}

func TestMergeGeometryOnReleasedModelIsEmpty(t *testing.T) {
	m := &Model{Meshes: []ModelMesh{tri(0, [3]float32{1, 1, 1})}}
	m.ReleaseGeometry()

	if verts, idx := mergeGeometry(m.Meshes); len(verts) != 0 || len(idx) != 0 {
		t.Errorf("released model merged to %d verts and %d indices, want none",
			len(verts), len(idx))
	}
}

func TestModelBounds(t *testing.T) {
	m := &Model{Meshes: []ModelMesh{
		{Verts: []Vertex{{Pos: [3]float32{-1, 0, -2}}, {Pos: [3]float32{1, 3, 0}}}},
		{Verts: []Vertex{{Pos: [3]float32{0, -1, 5}}}},
	}}

	min, max, ok := m.Bounds()
	if !ok {
		t.Fatal("Bounds reported no geometry, so the rest of this proves nothing")
	}
	if min != ([3]float32{-1, -1, -2}) {
		t.Errorf("min = %v, want {-1 -1 -2}", min)
	}
	if max != ([3]float32{1, 3, 5}) {
		t.Errorf("max = %v, want {1 3 5}", max)
	}
}

// ok has to distinguish "cannot answer" from "the model is flat". A caller
// seating a model on the ground acts on the height, and zero is a real answer
// for a plane but a wrong one for a model whose geometry was released.
func TestModelBoundsReportsWhenItCannotAnswer(t *testing.T) {
	flat := &Model{Meshes: []ModelMesh{
		{Verts: []Vertex{{Pos: [3]float32{0, 0, 0}}, {Pos: [3]float32{1, 0, 1}}}},
	}}
	if _, _, ok := flat.Bounds(); !ok {
		t.Error("a flat model reported no geometry; zero height is an answer, not a failure")
	}

	released := &Model{Meshes: []ModelMesh{tri(0, [3]float32{1, 1, 1})}}
	released.ReleaseGeometry()
	if _, _, ok := released.Bounds(); ok {
		t.Error("a released model answered Bounds; it has nothing to answer with")
	}

	if _, _, ok := (&Model{}).Bounds(); ok {
		t.Error("an empty model answered Bounds")
	}
}
