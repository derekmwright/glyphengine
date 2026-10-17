package renderer

import (
	"bytes"
	"errors"
	"fmt"
	"testing"
	"unsafe"

	"github.com/vkngwrapper/core/v3/core1_0"
)

// An ordinary instance set used to copy new placements straight into one
// persistently mapped, host-coherent buffer while frames that bind it were
// still executing. Host coherency does not retire a GPU read, so a game
// rebasing its placements around the camera every tick could change the vertex
// data a submitted frame was in the middle of drawing from -- measured in the
// showcase as corruption along rock silhouettes (issue #188). No Vulkan
// validation sees it, because the write never reaches the driver.
//
// These tests are the device-free half of that: an update must write no mapped
// memory at all, a copy must land in exactly one slot and only while that
// slot's fence is signalled, and every pass in a frame must read the same slot.
// The rendered half is cmd/instancecheck, under task instances.

// stagedFixture builds a renderer with one staged set. The storage-buffer
// fixture's driver is used because it is the one in this package that hands out
// a real []byte per mapped region: d.mapped[i] is slot i's bytes, which is how
// these tests can tell whether a copy happened at all.
func stagedFixture(t *testing.T, capacity int, instances []MeshInstance) (*Renderer, *bufferTestDriver, *InstanceSet) {
	t.Helper()
	r, d := bufferFixture()
	s, err := r.CreateInstanceSet(fakeMesh(&fakeHandles{}, 24, 36, 1), capacity, instances)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.mapped) != maxFramesInFlight {
		t.Fatalf("%d mapped regions, want one per frame slot (%d)", len(d.mapped), maxFramesInFlight)
	}
	for i, m := range d.mapped {
		if len(m) != capacity*meshInstanceSize {
			t.Fatalf("slot %d is %d bytes, want %d", i, len(m), capacity*meshInstanceSize)
		}
	}
	return r, d, s
}

// placements spreads n instances along x from base, with base in the tint as
// well so two arrays of the same length differ in every byte of every instance.
func placements(n int, base float32) []MeshInstance {
	out := make([]MeshInstance, n)
	for i := range out {
		out[i] = MeshInstance{Model: translate(base+float32(i), base, -base), Tint: [4]float32{base, 1, 1, 1}}
	}
	return out
}

func instanceBytes(in []MeshInstance) []byte {
	if len(in) == 0 {
		return nil
	}
	return unsafe.Slice((*byte)(unsafe.Pointer(&in[0])), len(in)*meshInstanceSize)
}

func allZero(b []byte) bool {
	for _, v := range b {
		if v != 0 {
			return false
		}
	}
	return true
}

// TestUpdateInstanceSetWritesNoDeviceMemory is the bug itself, stated as the
// property that removes it: neither CreateInstanceSet nor UpdateInstanceSet may
// touch a mapped region, because at the moment either is called a frame binding
// that memory may still be executing.
//
// BROKEN: restored the old body of UpdateInstanceSet -- `copy(unsafe.Slice(
// (*MeshInstance)(s.mapped), s.capacity), instances)` against a staged set's
// published view. FAILED with: "slot 0 was written by UpdateInstanceSet: an
// update must not touch device memory", with the same complaint about
// CreateInstanceSet's own placements on the line above it. (With s.mapped nil
// for a staged set the old line panics instead, so the break was made against
// s.slots[0].mapped, which is what routing an update straight into a slot
// buffer means.)
func TestUpdateInstanceSetWritesNoDeviceMemory(t *testing.T) {
	r, d, s := stagedFixture(t, 8, placements(4, 1))
	for i, m := range d.mapped {
		if !allZero(m) {
			t.Errorf("slot %d was written by CreateInstanceSet: nothing may be drawn from a slot before the first copy into it", i)
		}
	}

	r.UpdateInstanceSet(s, placements(6, 9))
	for i, m := range d.mapped {
		if !allZero(m) {
			t.Errorf("slot %d was written by UpdateInstanceSet: an update must not touch device memory", i)
		}
	}
	if s.count != 0 || s.buffer.Handle() != 0 {
		t.Errorf("an update published count %d buffer %#x to the passes; only a flush may", s.count, s.buffer.Handle())
	}
	if got := s.Count(); got != 6 {
		t.Errorf("Count() = %d, want 6: the staged placements are what the next recorded frame draws", got)
	}
}

// TestInstanceStagingCopiesIntoTheFlushedSlotOnly pins the two halves of the
// flush: the slot named gets the placements, the other slot is not touched, and
// the dirty bits say the untouched slot still owes a copy.
func TestInstanceStagingCopiesIntoTheFlushedSlotOnly(t *testing.T) {
	p := placements(5, 2)
	r, d, s := stagedFixture(t, 8, p)

	r.fenceWaited[0] = true
	r.flushInstanceSets(0)

	want := instanceBytes(p)
	if !bytes.Equal(d.mapped[0][:len(want)], want) {
		t.Error("slot 0 does not hold the staged placements after its flush")
	}
	if !allZero(d.mapped[0][len(want):]) {
		t.Error("the flush wrote past the staged count into slot 0's spare capacity")
	}
	if !allZero(d.mapped[1]) {
		t.Error("flushing slot 0 wrote slot 1 as well: a slot is written only while its own fence is signalled")
	}
	if s.buffer != s.slots[0].buffer || s.count != len(p) {
		t.Errorf("passes would bind buffer %#x count %d, want slot 0's %#x and %d",
			s.buffer.Handle(), s.count, s.slots[0].buffer.Handle(), len(p))
	}
	if s.slots[0].dirty {
		t.Error("slot 0 is still dirty after being filled")
	}
	if !s.slots[1].dirty {
		t.Error("slot 1 is clean without ever having been filled")
	}

	// Nothing to copy means nothing copied: the second flush of an unchanged
	// slot must not write at all, which is what makes an unchanging set cost
	// nothing per frame.
	clear(d.mapped[0])
	r.flushInstanceSets(0)
	if !allZero(d.mapped[0]) {
		t.Error("a flush with no update since the last one copied again")
	}
}

// TestInstanceStagingLeavesASlotInFlightAlone is the race, simulated: slot 0 is
// submitted, the game updates, and slot 1 is prepared. Slot 0's bytes are what
// the GPU is reading, so they must be byte-identical to the snapshot taken
// before the update, and each slot must report the count its own buffer holds.
//
// BROKEN: replaced the per-slot buffers with one shared buffer again, by
// reading s.slots[0] instead of s.slots[frame] in flushInstanceSets. FAILED
// with: "preparing slot 1 wrote slot 0, which is still in flight", plus "slot
// counts 3/0, want 6/3: a count is a property of the buffer it describes".
// Moving the flush itself above DrawFrame's WaitForFences is the other half of
// the ordering; the guard recorded in
// TestInstanceStagingRefusesASlotWhoseFenceIsNotWaited catches that one.
func TestInstanceStagingLeavesASlotInFlightAlone(t *testing.T) {
	before, after := placements(6, 1), placements(3, 50)
	r, d, s := stagedFixture(t, 8, before)

	r.fenceWaited[0] = true
	r.flushInstanceSets(0)
	r.fenceWaited[0] = false // submitted; slot 0 is the GPU's again
	snapshot := append([]byte(nil), d.mapped[0]...)

	r.UpdateInstanceSet(s, after)
	if !bytes.Equal(d.mapped[0], snapshot) {
		t.Fatal("the update reached slot 0 while it was in flight")
	}

	r.fenceWaited[1] = true
	r.flushInstanceSets(1)
	if !bytes.Equal(d.mapped[0], snapshot) {
		t.Error("preparing slot 1 wrote slot 0, which is still in flight")
	}
	if want := instanceBytes(after); !bytes.Equal(d.mapped[1][:len(want)], want) {
		t.Error("slot 1 does not hold the placements staged for it")
	}
	if s.slots[0].count != len(before) || s.slots[1].count != len(after) {
		t.Errorf("slot counts %d/%d, want %d/%d: a count is a property of the buffer it describes",
			s.slots[0].count, s.slots[1].count, len(before), len(after))
	}
	if s.buffer != s.slots[1].buffer || s.count != len(after) {
		t.Errorf("passes would bind buffer %#x count %d, want slot 1's %#x and %d",
			s.buffer.Handle(), s.count, s.slots[1].buffer.Handle(), len(after))
	}
}

// TestInstanceStagingFillsEverySlotAfterOneUpdate is the other half of a
// per-slot dirty bit. One update owes a copy to every slot, each the first time
// that slot comes round -- clearing the bit for all of them on the first copy
// leaves the next frame drawing the placements from before the update, which is
// a one-frame flicker in a scene that otherwise looks right.
//
// BROKEN: cleared the dirty bit for every slot inside flushInstanceSets
// (`for i := range s.slots { s.slots[i].dirty = false }` instead of
// sl.dirty = false). FAILED with: "slot 0 still holds the placements from
// before the update after that slot came round again".
func TestInstanceStagingFillsEverySlotAfterOneUpdate(t *testing.T) {
	r, d, s := stagedFixture(t, 8, placements(6, 1))
	after := placements(6, 50)

	r.fenceWaited[0] = true
	r.flushInstanceSets(0)
	r.fenceWaited[0] = false

	r.UpdateInstanceSet(s, after)

	r.fenceWaited[1] = true
	r.flushInstanceSets(1)
	r.fenceWaited[1] = false

	r.fenceWaited[0] = true
	r.flushInstanceSets(0)

	want := instanceBytes(after)
	if !bytes.Equal(d.mapped[0][:len(want)], want) {
		t.Error("slot 0 still holds the placements from before the update after that slot came round again")
	}
	for i := range s.slots {
		if s.slots[i].dirty {
			t.Errorf("slot %d is still dirty once every slot has been filled", i)
		}
	}
}

// TestInstanceStagingRefusesASlotWhoseFenceIsNotWaited is the ordering, made
// checkable without a device. The copy is safe only because DrawFrame has
// waited this slot's fence first; a call site that drifts above the wait is
// back to the original bug, and nothing about the resulting frame would say so.
//
// BROKEN: deleted the r.fenceWaited[frame] guard from flushInstanceSets.
// FAILED with: "flushing slot 1 before its fence was waited on was allowed, and
// wrote 66 bytes of it".
//
// BROKEN, with the guard in place: moved DrawFrame's r.flushInstanceSets(f)
// call above its WaitForFences. cmd/instancecheck panicked on its first frame
// with "renderer: instance staging flushed into slot 0 before its fence was
// waited on", which is the point of the guard: the call site cannot drift back
// above the wait and leave the corruption to be found in a screenshot.
func TestInstanceStagingRefusesASlotWhoseFenceIsNotWaited(t *testing.T) {
	r, d, _ := stagedFixture(t, 8, placements(3, 4))
	r.fenceWaited[0] = true // the other slot's fence proves nothing about this one

	defer func() {
		written := 0
		for _, b := range d.mapped[1] {
			if b != 0 {
				written++
			}
		}
		if recover() == nil {
			t.Errorf("flushing slot 1 before its fence was waited on was allowed, and wrote %d bytes of it", written)
		}
		if written != 0 {
			t.Errorf("%d bytes of slot 1 were written before the panic", written)
		}
	}()
	r.flushInstanceSets(1)
}

// instanceBindRecorder records what was bound at binding 1 -- the per-instance
// vertex binding, see instanceBindingDescription -- and the instance count of
// every draw, so a pass that read a different slot from the one the frame
// flushed is visible as a different buffer handle rather than as a wrong pixel
// on hardware.
type instanceBindRecorder struct {
	fakeDriver
	perInstance []uint64
	instances   []int
}

func (d *instanceBindRecorder) CmdBindVertexBuffers(cb core1_0.CommandBuffer, firstBinding int, buffers []core1_0.Buffer, offsets []int) {
	for i, b := range buffers {
		if firstBinding+i == 1 {
			d.perInstance = append(d.perInstance, uint64(b.Handle()))
		}
	}
	d.fakeDriver.CmdBindVertexBuffers(cb, firstBinding, buffers, offsets)
}

func (d *instanceBindRecorder) CmdDraw(cb core1_0.CommandBuffer, vertexCount, instanceCount int, firstVertex, firstInstance uint32) {
	d.instances = append(d.instances, instanceCount)
	d.fakeDriver.CmdDraw(cb, vertexCount, instanceCount, firstVertex, firstInstance)
}

func (d *instanceBindRecorder) CmdDrawIndexed(cb core1_0.CommandBuffer, indexCount, instanceCount int, firstIndex uint32, vertexOffset int, firstInstance uint32) {
	d.instances = append(d.instances, instanceCount)
	d.fakeDriver.CmdDrawIndexed(cb, indexCount, instanceCount, firstIndex, vertexOffset, firstInstance)
}

// TestInstanceSetPassesBindOneSlotSnapshot is the property that makes a frame
// internally consistent: the cascade shadow pass, the depth prepass and the lit
// instanced pass all draw the set in the same frame, and the prepass's depth is
// only valid for the main pass to test EQUAL against if all three read the same
// placements. Two of them reading different slots would discard whole props in
// the main pass, and only on the frames where an update landed between them.
//
// BROKEN: made recordInstancedShadow bind set.slots[0].buffer rather than
// set.buffer, with the frame flushed into slot 1. FAILED with: "pass 0 bound
// 0x1 at binding 1, want slot 1's 0x3: every pass in a frame reads one
// snapshot".
func TestInstanceSetPassesBindOneSlotSnapshot(t *testing.T) {
	p := placements(7, 3)
	r, _, s := stagedFixture(t, 8, p)

	// Slot 1 on purpose: a pass that quietly binds the first slot, or a count
	// taken from the set rather than the slot, passes if the frame is slot 0.
	const frame = 1
	r.fenceWaited[frame] = true
	r.flushInstanceSets(frame)

	h := &fakeHandles{}
	m := identityMat(1)
	draws := []RenderObject{{Mesh: s.Mesh, Texture: fakeTexture(h), Instances: s, MVP: m, Model: m}}

	d := &instanceBindRecorder{}
	var stats RenderStats
	var scratch commandScratch
	viewport := core1_0.Viewport{Width: 64, Height: 32, MaxDepth: 1}
	scissor := core1_0.Rect2D{Extent: core1_0.Extent2D{Width: 64, Height: 32}}
	lighting := SceneLighting{VP: m}

	recordInstancedShadow(d, &stats, h.commandBuffer(), h.pipeline(), h.layout(), viewport, scissor, draws, m, Frustum{}, &scratch)
	recordDepthPrepass(&graphFrame{driver: d, cmd: h.commandBuffer(), extent: scissor.Extent, scratch: &scratch,
		stats: &stats, draws: draws, lighting: lighting,
		prepass: depthPrepassPipelines{depth: h.pipeline(), depthInstanced: h.pipeline(), layout: h.layout(), mode: DepthPrepassOn, active: true}})
	recordInstanced(d, &stats, h.commandBuffer(), h.pipeline(), h.pipeline(), depthPrepassPipelines{}, h.layout(),
		viewport, scissor, draws, lighting, fakeTexture(h), h.descSet(), &scratch)

	if len(d.perInstance) != 3 {
		t.Fatalf("%d per-instance binds, want one from each of the shadow, prepass and lit passes", len(d.perInstance))
	}
	want := uint64(s.slots[frame].buffer.Handle())
	for i, got := range d.perInstance {
		if got != want {
			t.Errorf("pass %d bound %#x at binding 1, want slot %d's %#x: every pass in a frame reads one snapshot",
				i, got, frame, want)
		}
	}
	if len(d.instances) != 3 {
		t.Fatalf("%d instanced draws, want 3", len(d.instances))
	}
	for i, got := range d.instances {
		if got != len(p) {
			t.Errorf("pass %d drew %d instances, want slot %d's %d", i, got, frame, len(p))
		}
	}
}

// TestInstanceStagingLastUpdateWinsAndClampsToCapacity covers the two things a
// caller can get wrong per frame: several updates before the frame is recorded
// (only the last one can be drawn, since only one is in the buffer), and more
// placements than the set was built for (dropped, because growing would mean
// reallocating under a buffer the GPU may be reading).
func TestInstanceStagingLastUpdateWinsAndClampsToCapacity(t *testing.T) {
	r, d, s := stagedFixture(t, 8, placements(2, 1))

	r.UpdateInstanceSet(s, placements(4, 20))
	last := placements(5, 60)
	r.UpdateInstanceSet(s, last)
	if got := s.Count(); got != len(last) {
		t.Errorf("Count() = %d after two updates, want the last one's %d", got, len(last))
	}

	r.fenceWaited[0] = true
	r.flushInstanceSets(0)
	want := instanceBytes(last)
	if !bytes.Equal(d.mapped[0][:len(want)], want) {
		t.Error("the slot holds something other than the last update staged before the frame")
	}
	if !allZero(d.mapped[0][len(want):]) {
		t.Error("the earlier, longer update is still in the buffer past the current count")
	}

	over := placements(12, 100)
	r.UpdateInstanceSet(s, over)
	if got := s.Count(); got != 8 {
		t.Errorf("Count() = %d past capacity 8, want the clamp", got)
	}
	r.fenceWaited[1] = true
	r.flushInstanceSets(1)
	if s.slots[1].count != 8 {
		t.Errorf("slot count %d, want the clamped 8", s.slots[1].count)
	}
	if clamped := instanceBytes(over[:8]); !bytes.Equal(d.mapped[1], clamped) {
		t.Error("the clamped copy is not the first capacity placements of the update")
	}
	// The bound has to describe what is drawn, not what was asked for: a sphere
	// around the dropped placements would hold the set on screen when the
	// instances that justify it are not there.
	s2 := &InstanceSet{Mesh: s.Mesh}
	s2.recomputeBounds(over[:8])
	if c, rad := s.Bounds(); c != s2.boundCenter || rad != s2.boundRadius {
		t.Errorf("bounds %v/%v cover more than the %d placements the set kept (%v/%v)", c, rad, 8, s2.boundCenter, s2.boundRadius)
	}
}

// Rebasing placements around the camera is a per-frame call in the showcase, so
// an allocation here is an allocation per set per frame. The staging slice is
// grown once at creation; the bound recompute walks the placements twice rather
// than keeping two scratch slices for the same reason.
//
// BROKEN: restored recomputeBounds's `centers := make([][3]float32, ...)` and
// `scales := make([]float32, ...)`. FAILED with: "UpdateInstanceSet allocates
// 2 times per call at 600 placements; a set rebased every frame pays this every
// frame".
func TestUpdateInstanceSetDoesNotAllocate(t *testing.T) {
	r, _, s := stagedFixture(t, 600, placements(600, 1))
	p := placements(600, 7)
	allocs := testing.AllocsPerRun(20, func() { r.UpdateInstanceSet(s, p) })
	if allocs != 0 {
		t.Errorf("UpdateInstanceSet allocates %v times per call at %d placements; a set rebased every frame pays this every frame", allocs, len(p))
	}
}

// TestCreateInstanceSetUnwindsEveryBuffer injects a failure at every driver
// call CreateInstanceSet makes, at every position it reaches -- which is each
// of them once per frame slot now -- and requires every handle the attempt made
// to come back. A slot buffer created and then abandoned because a later slot
// failed to map is a leak no example would ever notice.
//
// BROKEN: removed the s.destroy(r.deviceDriver) from the MapMemory arm of
// CreateInstanceSet. FAILED with: at MapMemory/1 with "Buffer: created 1
// destroyed 0" and "DeviceMemory: created 1 destroyed 0", and at MapMemory/2
// with the same pair at "created 2 destroyed 0".
func TestCreateInstanceSetUnwindsEveryBuffer(t *testing.T) {
	mesh := fakeMesh(&fakeHandles{}, 24, 36, 1)
	create := func(r *Renderer) error {
		_, err := r.CreateInstanceSet(mesh, 8, placements(8, 1))
		return err
	}

	r, control := bufferFixture()
	if err := create(r); err != nil {
		t.Fatal(err)
	}
	for _, s := range r.instanceSets {
		s.destroy(control)
	}
	bufferBalance(t, control)

	for _, call := range []string{"CreateBuffer", "AllocateMemory", "BindBufferMemory", "MapMemory"} {
		if control.calls[call] != maxFramesInFlight {
			t.Errorf("%s was called %d times, want one per frame slot (%d)", call, control.calls[call], maxFramesInFlight)
		}
		for at := 1; at <= control.calls[call]; at++ {
			t.Run(fmt.Sprintf("%s/%d", call, at), func(t *testing.T) {
				r, d := bufferFixture()
				d.failCall, d.failAt = call, at
				err := create(r)
				if !errors.Is(err, errInjected) {
					t.Fatalf("error %v", err)
				}
				if len(r.instanceSets) != 0 {
					t.Error("a failed creation registered the set, so teardown would free it again")
				}
				bufferBalance(t, d)
			})
		}
	}
}

// TestDestroyInstanceSetReleasesEverySlotBuffer is DestroyInstanceSet's
// existing deferred-release property with the per-slot buffers counted: all of
// them, and none of them before the frames in flight have retired. The
// published view of whichever slot the last frame bound is NOT a fourth buffer,
// and releasing it as one would be a double free.
//
// BROKEN: released only slots[0] in the deferred closure. FAILED with:
// "released 1 buffers, want one per frame slot (2)" and "Buffer: created 2
// destroyed 1".
func TestDestroyInstanceSetReleasesEverySlotBuffer(t *testing.T) {
	r, d, s := stagedFixture(t, 8, placements(8, 1))
	r.fenceWaited[0] = true
	r.flushInstanceSets(0) // publishes slot 0's buffer as s.buffer

	before := d.destroyed["Buffer"]
	r.DestroyInstanceSet(s)
	if got := d.destroyed["Buffer"] - before; got != 0 {
		t.Errorf("%d buffers released at the call; a submitted frame can still be drawing from them", got)
	}
	r.flushAllDeferred()
	if got := d.destroyed["Buffer"] - before; got != maxFramesInFlight {
		t.Errorf("released %d buffers, want one per frame slot (%d)", got, maxFramesInFlight)
	}
	bufferBalance(t, d)
	if len(r.instanceSets) != 0 {
		t.Errorf("%d sets still registered after the deferred release ran", len(r.instanceSets))
	}
}

// A frame that is skipped -- minimised, or the swapchain resized under it --
// returns from DrawFrame before any staging is flushed. The dirty bits have to
// survive that, or the update made during the skipped frame is lost and the
// slot keeps drawing older placements with nothing to say it is behind.
func TestInstanceStagingSurvivesASkippedFrame(t *testing.T) {
	r, d, s := stagedFixture(t, 8, placements(4, 1))
	after := placements(4, 30)

	r.fenceWaited[0] = true
	r.flushInstanceSets(0)
	r.fenceWaited[0] = false

	r.UpdateInstanceSet(s, after)
	// Slot 1's frame reaches neither the fence nor the flush.
	for i := range s.slots {
		if !s.slots[i].dirty {
			t.Errorf("slot %d went clean across a frame that never flushed", i)
		}
	}
	if s.count != len(placements(4, 1)) || s.buffer != s.slots[0].buffer {
		t.Error("a skipped frame changed what the passes would bind")
	}

	r.fenceWaited[0] = true
	r.flushInstanceSets(0)
	if want := instanceBytes(after); !bytes.Equal(d.mapped[0][:len(want)], want) {
		t.Error("the update staged during the skipped frame never reached the next frame's slot")
	}
}
