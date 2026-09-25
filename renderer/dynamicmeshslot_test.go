package renderer

import (
	"reflect"
	"testing"
)

// vertexSpan is how much of a mapped buffer one panel's quads occupy.
func vertexSpan(v []Vertex) int { return len(v) * sizeOf[Vertex]() }

// TestDynamicMeshUpdateAvoidsASlotAFrameInFlightIsReading is the hazard that
// skipping rebuilds turns from rare into routine.
//
// A mesh that is not updated every frame keeps the slot it was last written
// into, and every frame that records while it sits there records its draws
// against that one buffer. The next update arrives on some frame whose own
// fence has signaled -- which says nothing about the OTHER frames still in
// flight, all of them reading that slot. Copying into it is the CPU
// overwriting vertices the GPU has already been told to fetch. With two frames
// in flight it happens on every update whose previous write was exactly two
// frames earlier, which for a button that changes on hover is half of them.
//
// Verified to fail twice. With writeSlot's body replaced by `return frame`
// this reports "the mesh stayed on slot 0, which a frame in flight is reading"
// and "the copy landed in slot 0, which frame 1 is still reading". With
// `boundAt[frame] = bound` moved inside the dirty branch, so a frame that
// flushed nothing never records what it is about to draw from, it reports
// "frame 1 recorded slot -1 but draws from slot 0" -- and the safe-slot choice
// then silently picks slot 0 again, which is the same hazard by another route.
// Nothing else in the package notices either break.
func TestDynamicMeshUpdateAvoidsASlotAFrameInFlightIsReading(t *testing.T) {
	f := newPanelFixture()

	f.p.Rebuild(f.r)
	f.r.flushDynamicMeshes(0)
	if f.dm.bound != 0 {
		t.Fatalf("the first update went to slot %d, want 0", f.dm.bound)
	}

	span := f.mesh.VertexCount * sizeOf[Vertex]()
	if span == 0 {
		t.Fatal("the fixture uploaded nothing, so nothing below means anything")
	}
	firstBytes := append([]byte(nil), f.dm.vmapped[0][:span]...)

	// Every other frame now records against slot 0 without touching it.
	for frame := 1; frame < maxFramesInFlight; frame++ {
		f.r.flushDynamicMeshes(frame)
		if f.dm.bound != 0 {
			t.Fatalf("frame %d moved the mesh to slot %d with no update", frame, f.dm.bound)
		}
		if f.dm.boundAt[frame] != 0 {
			t.Fatalf("frame %d recorded slot %d but draws from slot 0", frame, f.dm.boundAt[frame])
		}
	}

	// Frame 0 comes round again. Its own fence has signaled; frames 1..N-1
	// have not, and they are all on slot 0.
	f.p.X += 40
	f.p.Rebuild(f.r)
	f.r.flushDynamicMeshes(0)

	if f.dm.bound == 0 {
		t.Error("the mesh stayed on slot 0, which a frame in flight is reading")
	}
	if got, want := f.mesh.vertexBuffer.Handle(), f.dm.vbufs[f.dm.bound].Handle(); got != want {
		t.Errorf("the mesh points at vertex buffer %v, but bound says slot %d (%v)", got, f.dm.bound, want)
	}
	if !reflect.DeepEqual(f.dm.vmapped[0][:span], firstBytes) {
		t.Error("the copy landed in slot 0, which frame 1 is still reading")
	}

	want, _ := f.ns.GenerateQuads(f.p.X, f.p.Y, f.p.Width, f.p.Height, f.p.Scale, f.layer.Color)
	if vertexSpan(want) != span {
		t.Fatalf("the two updates are different sizes (%d and %d); the comparison below is not like for like", span, vertexSpan(want))
	}
	if !reflect.DeepEqual(f.dm.vmapped[f.dm.bound][:span], vertexBytes(want)) {
		t.Errorf("slot %d does not hold the vertices that were just staged", f.dm.bound)
	}

	// Frame 0 is now in flight drawing from the slot it moved to, and the
	// bookkeeping has to say so, or the next update reasons from the slot it
	// left. Recording boundAt before the write instead of after passes every
	// check above -- frame 0 lands in slot 1, slot 0 keeps its bytes -- and
	// then the update at frame 1 below writes slot 1 under frame 0. That break
	// was applied and this is the case that failed it: "frame 0 recorded
	// slot 0 but draws from slot 1".
	moved := f.dm.bound
	if f.dm.boundAt[0] != moved {
		t.Fatalf("frame 0 recorded slot %d but draws from slot %d", f.dm.boundAt[0], moved)
	}
	movedBytes := append([]byte(nil), f.dm.vmapped[moved][:span]...)
	f.p.X += 40
	f.p.Rebuild(f.r)
	f.r.flushDynamicMeshes(1)
	if f.dm.bound == moved {
		t.Errorf("the update at frame 1 went to slot %d, which frame 0 is still reading", moved)
	}
	if !reflect.DeepEqual(f.dm.vmapped[moved][:span], movedBytes) {
		t.Errorf("slot %d was overwritten while frame 0 is still reading it", moved)
	}
}

// TestDynamicMeshUpdateTakesItsOwnSlotWhenItIsFree is the counterweight: the
// safe-slot rule must not push every update onto a different buffer than the
// frame's own, or a mesh updated every frame would walk the slots for nothing
// and `unboundSlot` would read as a claim on slot 0.
func TestDynamicMeshUpdateTakesItsOwnSlotWhenItIsFree(t *testing.T) {
	f := newPanelFixture()

	// Nothing has recorded this mesh at all yet.
	f.p.Rebuild(f.r)
	f.r.flushDynamicMeshes(1)
	if f.dm.bound != 1 {
		t.Fatalf("an update on frame 1 with nothing in flight went to slot %d, want 1", f.dm.bound)
	}

	// Now frame 1 is holding slot 1 and frame 0 has still never recorded, so
	// frame 0's own slot is free and is the one to take.
	f.r.flushDynamicMeshes(1)
	f.p.X += 40
	f.p.Rebuild(f.r)
	f.r.flushDynamicMeshes(0)
	if f.dm.bound != 0 {
		t.Errorf("an update on frame 0 went to slot %d though nothing in flight holds slot 0", f.dm.bound)
	}
}

// TestDynamicMeshUpdateCopiesOnce: the old per-frame dirty array copied every
// update into all maxFramesInFlight slots, once per frame, to keep them in
// step. One slot is written now, so the other has to be left alone -- and an
// update that quietly wrote both would put the hazard straight back.
//
// Verified to fail: with the copy wrapped in `for s := range dm.vmapped` it
// reports "slot 1 was written by an update flushed into slot 0".
func TestDynamicMeshUpdateCopiesOnce(t *testing.T) {
	f := newPanelFixture()
	f.p.Rebuild(f.r)
	f.r.flushDynamicMeshes(0)

	span := f.mesh.VertexCount * sizeOf[Vertex]()
	for slot := 1; slot < maxFramesInFlight; slot++ {
		for _, b := range f.dm.vmapped[slot][:span] {
			if b != 0 {
				t.Fatalf("slot %d was written by an update flushed into slot 0", slot)
			}
		}
	}
}
