package renderer

import (
	"reflect"
	"testing"
	"unsafe"
)

// panelFixture is a Panel whose layer has a working dynamic mesh and no device
// behind it. UpdateMeshData and flushDynamicMeshes only touch the mapped byte
// slices, the staging slices and the dirty flags, so the whole upload path runs
// here with host memory standing in for the mapped buffers -- which is what
// lets these tests watch a real upload happen or not happen.
type panelFixture struct {
	r     *Renderer
	p     *Panel
	layer *PanelLayer
	ns    *NineSlice
	mesh  *Mesh
	dm    *dynamicMesh
}

func newPanelFixture() *panelFixture {
	ns := &NineSlice{TexSize: 48, Inset: 16}
	m := &Mesh{}
	dm := newDynamicMesh()
	h := &fakeHandles{}
	for i := range dm.vmapped {
		dm.vmapped[i] = make([]byte, panelMaxVerts*sizeOf[Vertex]())
		dm.imapped[i] = make([]byte, panelMaxIndices*2)
		// Distinct handles, so "the mesh points at another slot's buffer" is
		// something a test can read off the mesh rather than infer.
		dm.vbufs[i] = h.buffer()
		dm.ibufs[i] = h.buffer()
	}
	m.vertexBuffer, m.indexBuffer = dm.vbufs[0], dm.ibufs[0]
	layer := &PanelLayer{NineSlice: ns, Color: [3]float32{0.8, 0.7, 0.3}, Opacity: 1, mesh: m}
	return &panelFixture{
		r:     &Renderer{dynamicMeshes: map[*Mesh]*dynamicMesh{m: dm}},
		p:     &Panel{X: 120, Y: 64, Width: 220, Height: 48, Scale: 2, Visible: true, Layers: []*PanelLayer{layer}},
		layer: layer,
		ns:    ns,
		mesh:  m,
		dm:    dm,
	}
}

// uploaded re-arms the check, rebuilds, and reports whether the rebuild pushed
// new geometry at the mesh. UpdateMeshData's one observable effect is marking
// every frame in flight dirty, and that flag is the upload: flushDynamicMeshes
// copies into the frame's buffer and repoints the mesh exactly when it is set.
func (f *panelFixture) uploaded() bool {
	f.dm.dirty = false
	f.p.Rebuild(f.r)
	return f.dm.dirty
}

func vertexBytes(v []Vertex) []byte {
	if len(v) == 0 {
		return nil
	}
	return unsafe.Slice((*byte)(unsafe.Pointer(&v[0])), len(v)*sizeOf[Vertex]())
}

// TestAppendQuadsMatchesGenerateQuads: the append form is the generating form,
// so a caller that switches to it gets the same panel. Compared as raw vertex
// bytes rather than field by field, because a field added to Vertex and left
// unwritten by one of the two paths is exactly the difference this has to see.
func TestAppendQuadsMatchesGenerateQuads(t *testing.T) {
	ns := &NineSlice{TexSize: 48, TexH: 32, Inset: 12}

	for _, c := range []struct {
		name          string
		x, y, w, h, s float32
	}{
		{"ordinary", 100, 50, 300, 120, 2},
		{"narrower than two corners", 10, 10, 18, 90, 2},
		{"shorter than two corners", 10, 10, 200, 14, 2},
		{"unit scale", 0, 0, 64, 64, 1},
	} {
		t.Run(c.name, func(t *testing.T) {
			wantV, wantI := ns.GenerateQuads(c.x, c.y, c.w, c.h, c.s, [3]float32{1, 0.5, 0.25})

			var gotV []Vertex
			var gotI []uint16
			gotV, gotI = ns.AppendQuads(gotV, gotI, c.x, c.y, c.w, c.h, c.s, [3]float32{1, 0.5, 0.25})

			if !reflect.DeepEqual(vertexBytes(gotV), vertexBytes(wantV)) {
				t.Errorf("vertices differ:\n got %+v\nwant %+v", gotV, wantV)
			}
			if !reflect.DeepEqual(gotI, wantI) {
				t.Errorf("indices differ:\n got %v\nwant %v", gotI, wantI)
			}

			// Reusing the buffers is the whole point of the form, so the
			// second pass through them has to land in the same place.
			gotV, gotI = ns.AppendQuads(gotV[:0], gotI[:0], c.x, c.y, c.w, c.h, c.s, [3]float32{1, 0.5, 0.25})
			if !reflect.DeepEqual(vertexBytes(gotV), vertexBytes(wantV)) || !reflect.DeepEqual(gotI, wantI) {
				t.Errorf("a reused pair of buffers produced %d verts / %d indices, want %d / %d",
					len(gotV), len(gotI), len(wantV), len(wantI))
			}
		})
	}
}

// TestAppendQuadsIndexesIntoTheWholeBuffer: indices are positions in the
// destination, not in the nine quads, so two panels appended into one buffer
// each point at their own vertices. Rebasing them per panel would draw the
// second panel on top of the first, which is a bug that looks like a missing
// panel rather than like wrong indices.
func TestAppendQuadsIndexesIntoTheWholeBuffer(t *testing.T) {
	ns := &NineSlice{TexSize: 48, Inset: 16}

	v, i := ns.AppendQuads(nil, nil, 0, 0, 200, 80, 2, [3]float32{1, 1, 1})
	first := len(v)
	v, i = ns.AppendQuads(v, i, 400, 0, 200, 80, 2, [3]float32{1, 1, 1})

	if len(v) != 2*first {
		t.Fatalf("two panels produced %d vertices, want %d", len(v), 2*first)
	}
	var minSecond uint16 = 1<<16 - 1
	for _, idx := range i[len(i)/2:] {
		if idx < minSecond {
			minSecond = idx
		}
	}
	if int(minSecond) != first {
		t.Errorf("the second panel's lowest index is %d, want %d -- it is indexing the first panel's vertices", minSecond, first)
	}
	for _, idx := range i {
		if int(idx) >= len(v) {
			t.Fatalf("index %d is past the %d vertices in the buffer", idx, len(v))
		}
	}
}

// TestPanelRebuildSkipsUnchangedLayer: the rebuild and the upload both go away
// when nothing that reaches a vertex has changed. Verified to fail: with the
// `if layer.hasBuilt && layer.built == in` skip deleted from Panel.Rebuild
// this reports "a rebuild with nothing changed still uploaded".
func TestPanelRebuildSkipsUnchangedLayer(t *testing.T) {
	f := newPanelFixture()

	if !f.uploaded() {
		t.Fatal("the first rebuild did not upload; the panel would never appear")
	}
	for i := range 3 {
		if f.uploaded() {
			t.Fatalf("a rebuild with nothing changed still uploaded (frame %d)", i+2)
		}
	}
}

// TestPanelRebuildUploadsWhenAnInputChanges walks every value that reaches a
// vertex, one at a time. A missed input is the silent failure here: the panel
// keeps drawing where it used to be, and nothing anywhere says so.
//
// Verified to fail: with `color` dropped from the quadInputs comparison (the
// field left out of the struct), the "layer colour" case reports that changing
// it uploaded nothing -- which is a button whose hover never lights up.
func TestPanelRebuildUploadsWhenAnInputChanges(t *testing.T) {
	changes := []struct {
		name   string
		change func(f *panelFixture)
	}{
		{"x", func(f *panelFixture) { f.p.X += 4 }},
		{"y", func(f *panelFixture) { f.p.Y += 4 }},
		{"width", func(f *panelFixture) { f.p.Width += 4 }},
		{"height", func(f *panelFixture) { f.p.Height += 4 }},
		{"scale", func(f *panelFixture) { f.p.Scale = 1.5 }},
		{"layer colour", func(f *panelFixture) { f.layer.Color = [3]float32{0.1, 0.2, 0.3} }},
		{"nine-slice inset", func(f *panelFixture) { f.ns.Inset = 8 }},
		{"nine-slice texture width", func(f *panelFixture) { f.ns.TexSize = 64 }},
		{"nine-slice texture height", func(f *panelFixture) { f.ns.TexH = 24 }},
		{"a different nine-slice", func(f *panelFixture) {
			f.layer.NineSlice = &NineSlice{TexSize: f.ns.TexSize, TexH: f.ns.TexH, Inset: f.ns.Inset}
		}},
	}

	for _, c := range changes {
		t.Run(c.name, func(t *testing.T) {
			f := newPanelFixture()
			f.uploaded()
			if f.uploaded() {
				t.Fatal("the fixture was not in a steady state")
			}
			c.change(f)
			if !f.uploaded() {
				t.Errorf("changing %s uploaded nothing; the panel is stale", c.name)
			}
		})
	}
}

// TestPanelRebuildIgnoresWhatDoesNotReachAVertex: the counterweight to the test
// above. Opacity, glow, the blend mode, the texture and the interior fill are
// all read straight off the layer by UIRenderObjects every frame, so making any
// of them rebuild the mesh would give back the per-frame upload for nothing --
// and a skip rule that rebuilds on everything is not a skip rule.
func TestPanelRebuildIgnoresWhatDoesNotReachAVertex(t *testing.T) {
	for _, c := range []struct {
		name   string
		change func(f *panelFixture)
	}{
		{"opacity", func(f *panelFixture) { f.layer.Opacity = 0.5 }},
		{"glow", func(f *panelFixture) { f.layer.Glow = 2 }},
		{"texture mode", func(f *panelFixture) { f.layer.TextureMode = true }},
		{"texture", func(f *panelFixture) { f.ns.Texture = &Texture{} }},
		{"fill", func(f *panelFixture) { f.ns.Fill = &PanelFill{Color: [3]float32{0, 0, 0}, Opacity: 1} }},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newPanelFixture()
			f.uploaded()
			c.change(f)
			if f.uploaded() {
				t.Errorf("changing %s rebuilt the mesh, which no vertex reads", c.name)
			}
		})
	}
}

// TestPanelRebuildLeavesTheRightVerticesBehind: skipping is only safe if what
// the mesh already holds is what a rebuild would have written. This reads the
// mapped buffer the flush copied into and compares it with the quads generated
// from scratch, so a skip that quietly kept an older frame's geometry -- the
// one failure mode that survives every "did it upload" check -- shows up here.
func TestPanelRebuildLeavesTheRightVerticesBehind(t *testing.T) {
	f := newPanelFixture()
	f.p.Rebuild(f.r)
	f.r.flushDynamicMeshes(0)
	f.r.flushDynamicMeshes(1)

	f.p.X += 40
	f.p.Rebuild(f.r)
	f.r.flushDynamicMeshes(0)

	for range 5 {
		f.p.Rebuild(f.r)
	}

	want, wantI := f.ns.GenerateQuads(f.p.X, f.p.Y, f.p.Width, f.p.Height, f.p.Scale, f.layer.Color)
	if f.mesh.VertexCount != len(want) || f.mesh.IndexCount != len(wantI) {
		t.Fatalf("mesh holds %d verts / %d indices, want %d / %d",
			f.mesh.VertexCount, f.mesh.IndexCount, len(want), len(wantI))
	}
	got := f.dm.vmapped[f.dm.bound][:len(want)*sizeOf[Vertex]()]
	if !reflect.DeepEqual(got, vertexBytes(want)) {
		t.Error("the buffer the mesh points at is not the geometry the panel currently describes")
	}
}

// TestPanelSteadyStateAllocatesNothing is the whole of issue #137 in one
// number: a panel that has not changed costs no allocations to rebuild and
// hand to the renderer.
//
// Verified to fail: with UIRenderObjects' `objs := p.objs[:0]` put back to
// `var objs []UIRenderObject` it reports 1 allocation per frame. It does NOT
// fail when the skip is deleted from Rebuild -- the scratch buffers absorb
// that on their own -- which is what TestPanelRebuildSkipsUnchangedLayer is
// for. Neither claim is the other one.
func TestPanelSteadyStateAllocatesNothing(t *testing.T) {
	f := newPanelFixture()
	f.p.Rebuild(f.r)
	f.r.flushDynamicMeshes(0)
	f.r.flushDynamicMeshes(1)

	n := testing.AllocsPerRun(200, func() {
		f.p.Rebuild(f.r)
		f.p.UIRenderObjects(1280, 720)
	})
	if n != 0 {
		t.Errorf("a steady-state panel allocated %v times per frame, want 0", n)
	}
	if len(f.p.UIRenderObjects(1280, 720)) != 1 {
		t.Fatal("the fixture stopped producing a render object, so the measurement above covered nothing")
	}
}

// TestPanelMovingEveryFrameAllocatesNothing is the other half, and the half
// the skip rule cannot help with: a panel that really is moving every frame
// still has to regenerate and re-upload, and that is what the scratch buffers
// are for.
//
// Verified to fail: with Rebuild put back to GenerateQuads and its fresh pair
// of slices this reports 6 allocations per frame. The steady-state test above
// does not fail on that change, because a panel that is not moving never
// reaches the generator at all -- two separate claims needing two tests.
func TestPanelMovingEveryFrameAllocatesNothing(t *testing.T) {
	f := newPanelFixture()
	f.p.Rebuild(f.r)
	f.r.flushDynamicMeshes(0)
	f.r.flushDynamicMeshes(1)

	var step float32
	n := testing.AllocsPerRun(200, func() {
		step++
		f.p.X = 120 + step
		f.p.Rebuild(f.r)
		f.p.UIRenderObjects(1280, 720)
	})
	if n != 0 {
		t.Errorf("a panel moving every frame allocated %v times per frame, want 0", n)
	}
	if f.mesh.VertexCount == 0 {
		t.Fatal("the fixture stopped uploading, so the measurement above covered nothing")
	}
}

// BenchmarkPanelSteadyState is the number behind issue #137: what one
// unchanging nine-slice layer costs per frame to rebuild and hand to the
// renderer. AMD Ryzen 9 5900X, -benchtime 200000x -count 5: 3120 B and 7
// allocs per frame with GenerateQuads and a fresh object slice, 0 and 0 with
// the scratch buffers and the skip rule.
func BenchmarkPanelSteadyState(b *testing.B) {
	f := newPanelFixture()
	f.p.Rebuild(f.r)
	f.r.flushDynamicMeshes(0)
	f.r.flushDynamicMeshes(1)
	if len(f.p.UIRenderObjects(1280, 720)) != 1 {
		b.Fatal("the fixture produced no render object")
	}

	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		f.p.Rebuild(f.r)
		f.p.UIRenderObjects(1280, 720)
	}
}
