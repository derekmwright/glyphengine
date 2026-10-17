package renderer

import (
	"fmt"
	"math"
	"unsafe"

	"github.com/vkngwrapper/core/v3/core1_0"
)

// MeshInstance is one placement of an instanced mesh: where it is and what
// colour it is.
//
// Model is a full matrix rather than a position and a rotation because a
// placement system hands out transforms, not turntables — a prop can be rotated
// to the ground normal and scaled to a plot. The grass system's 16-byte
// position-plus-rotation instance is the other trade, and it is the right one
// for a million blades and the wrong one for nine hundred buildings.
type MeshInstance struct {
	Model [16]float32
	Tint  [4]float32 // rgb multiplies the mesh's vertex colour; w is unused here; InstanceSetLOD overwrites it with coverage
}

// meshInstanceSize is the vertex stride of the per-instance binding.
const meshInstanceSize = int(unsafe.Sizeof(MeshInstance{})) // 80

// InstanceSet is a mesh plus the GPU buffer of placements it is drawn at.
//
// One of these is one CmdDrawIndexed, one push-constant upload and one frustum
// test, however many instances it holds. That is the whole point: a colony with
// 900 identical habitat domes is 900 draw calls of a 200-triangle mesh on the
// ordinary path, which costs nothing on the GPU and everything on the CPU that
// records them.
//
// Build one with Renderer.CreateInstanceSet and change it with
// Renderer.UpdateInstanceSet. The buffers are host-visible and written
// directly rather than staged through a transfer, because placements change
// when a player builds something and staging a 72KB copy through a command
// buffer for that is more machinery than the write is worth. An update is
// nonetheless safe against frames in flight: it lands in CPU memory, and a
// frame slot's buffer is written only once that slot's fence has signalled.
//
// Give it back with Renderer.DestroyInstanceSet when the level or the group of
// props it draws goes away; see that method's comment for what a stale draw
// does if a game does not stop drawing it first.
type InstanceSet struct {
	Mesh     *Mesh
	lod      *InstanceSetLOD
	lodLevel int
	batch    *meshRangeBatch

	// buffer and count are the slot snapshot the frame being recorded draws:
	// flushInstanceSets publishes slot f's buffer and the number of placements
	// it holds, and every pass -- shadow, point shadow, depth prepass, lit and
	// the instanced recorder, plus the prepass predicate's own count test --
	// reads this one pair. One pair rather than a frame argument threaded
	// through all of them, so no two passes in a frame can disagree about what
	// the set holds.
	//
	// For a staged set these are a view of slots[f] and are not owned here. The
	// LOD buckets and mesh-range batches that reuse this struct are per-slot by
	// construction and own them directly; see staged below.
	buffer         core1_0.Buffer
	offset         int
	indirect       core1_0.Buffer
	indirectOffset int
	memory         core1_0.DeviceMemory
	mapped         unsafe.Pointer
	capacity       int
	count          int
	destroyed      bool

	// staging is where UpdateInstanceSet writes, and it writes nowhere else.
	// A placement update is a host write, and host coherency does not retire a
	// GPU read: a frame submitted last tick can still be fetching instance
	// attributes while a game rebases its transforms around the camera, which
	// is measured corruption along the silhouettes of whatever the set draws
	// (issue #188) and which ordinary validation cannot see, because the write
	// never goes near the driver. So placements land here and each frame slot
	// takes its own copy once its fence says the GPU is finished with that
	// slot's buffer.
	//
	// Grown to capacity once at creation: a 600-placement set rebased every
	// frame must not allocate per frame.
	staging []MeshInstance

	// slots is one buffer per frame in flight. This is what doubles a set's GPU
	// memory against the single shared buffer this used to be -- 80 bytes per
	// placement per slot -- and it is the cheaper of the two ways to be safe:
	// the alternative is a fresh set per change released through DeferDestroy,
	// which is what the showcase had to implement by hand, costs the same
	// memory for as long as the change rate keeps old sets alive, and pays an
	// allocation and a map per change on top.
	slots [maxFramesInFlight]instanceSlot

	// staged marks a set whose placements live in staging and slots: every set
	// CreateInstanceSet hands out. The LOD buckets and mesh-range batches that
	// reuse this struct own buffer/memory/mapped themselves and have no slots,
	// so the release paths must not go looking for slots to free -- or, worse,
	// free a staged set's published view of a slot's buffer a second time.
	staged bool

	// Bound sphere over every instance, in world space. The draw list frustum
	// tests this once for the set rather than once per instance.
	//
	// Ordinary sets retain all placements. InstanceSetLOD pays for per-placement
	// culling and uploads survivors into these buffers as internal buckets;
	// their bounds enclose only that frame's survivors. See
	// docs/agents/instancing.md and docs/agents/lod-instancing.md for the costs.
	boundCenter [3]float32
	boundRadius float32
}

// instanceSlot is one frame slot's copy of a set's placements: its own buffer,
// mapped once for the set's life, plus the count that buffer holds and whether
// staging has moved on since it was filled.
//
// dirty is per slot rather than per set because a set updated once has to be
// copied into every slot, each the first time that slot comes round again,
// and then not again -- clearing it for all slots on one copy leaves the other
// slot drawing the placements from before the update.
type instanceSlot struct {
	buffer core1_0.Buffer
	memory core1_0.DeviceMemory
	mapped unsafe.Pointer
	count  int
	dirty  bool
}

// release gives the slot's buffer and mapping back. Safe on a slot that was
// only partly built, which is what a failed CreateInstanceSet unwinds.
func (sl *instanceSlot) release(deviceDriver core1_0.DeviceDriver) {
	if sl.mapped != nil {
		deviceDriver.UnmapMemory(sl.memory)
		sl.mapped = nil
	}
	if sl.buffer.Handle() != 0 {
		deviceDriver.DestroyBuffer(sl.buffer, nil)
		sl.buffer = core1_0.Buffer{}
	}
	if sl.memory.Handle() != 0 {
		deviceDriver.FreeMemory(sl.memory, nil)
		sl.memory = core1_0.DeviceMemory{}
	}
}

// Count returns how many instances the set currently draws.
//
// This is the staged count -- what the frame about to be recorded will draw --
// not what the last submitted frame drew. The two differ only between an
// UpdateInstanceSet call and the next DrawFrame, which is the window every
// caller asking this question is in: the engine's own draw-list builder skips
// an instanced entity on Count() == 0, so reporting the slot's count would drop
// a set's first frame entirely, the frame after it was created.
func (s *InstanceSet) Count() int {
	if s == nil {
		return 0
	}
	if s.staged {
		return len(s.staging)
	}
	return s.count
}

// Capacity returns how many instances the set can hold without being rebuilt.
func (s *InstanceSet) Capacity() int {
	if s == nil {
		return 0
	}
	return s.capacity
}

// Bounds returns the set's world-space bounding sphere, which is what the draw
// list frustum tests. Like Count, it describes the staged placements: the ones
// the next recorded frame draws.
func (s *InstanceSet) Bounds() (center [3]float32, radius float32) {
	if s == nil {
		return [3]float32{}, 0
	}
	return s.boundCenter, s.boundRadius
}

// CreateInstanceSet allocates a set for mesh with room for capacity instances,
// and fills it with the ones given.
//
// capacity is fixed. A set that has to grow is a new set: resizing would mean
// reallocating a buffer the GPU may still be reading from, and a game that
// knows it can place at most N of something can say so. Passing fewer instances
// than the capacity is fine and normal.
func (r *Renderer) CreateInstanceSet(mesh *Mesh, capacity int, instances []MeshInstance) (*InstanceSet, error) {
	if mesh == nil {
		return nil, fmt.Errorf("create instance set: nil mesh")
	}
	if capacity <= 0 {
		return nil, fmt.Errorf("create instance set: capacity must be positive, got %d", capacity)
	}
	if len(instances) > capacity {
		return nil, fmt.Errorf("create instance set: %d instances exceeds capacity %d", len(instances), capacity)
	}

	size := capacity * meshInstanceSize
	s := &InstanceSet{
		Mesh:     mesh,
		capacity: capacity,
		staged:   true,
		staging:  make([]MeshInstance, 0, capacity),
	}

	// One buffer per frame in flight. Each is mapped once and left mapped:
	// Vulkan allows a persistent mapping, the memory is host-coherent so there
	// is nothing to flush, and the per-slot copy is then a plain copy rather
	// than a map/unmap pair.
	for i := range s.slots {
		sl := &s.slots[i]
		buf, mem, err := r.createBuffer(size,
			core1_0.BufferUsageVertexBuffer,
			core1_0.MemoryPropertyHostVisible|core1_0.MemoryPropertyHostCoherent,
		)
		if err != nil {
			s.destroy(r.deviceDriver)
			return nil, fmt.Errorf("create instance set: %w", err)
		}
		sl.buffer, sl.memory = buf, mem
		ptr, _, err := r.deviceDriver.MapMemory(mem, 0, size, 0)
		if err != nil {
			s.destroy(r.deviceDriver)
			return nil, fmt.Errorf("create instance set: map memory: %w", err)
		}
		sl.mapped = ptr
	}

	// Registered only once every slot exists, so a failed creation leaves
	// nothing for Renderer.Destroy or ResourceCounts to find.
	r.instanceSets = append(r.instanceSets, s)
	// The initial placements are staged like any other update, so nothing is
	// drawn out of a slot buffer before the first copy into it: count is zero
	// in every slot until flushInstanceSets fills one, and both draw paths skip
	// a set whose count is zero.
	r.UpdateInstanceSet(s, instances)
	return s, nil
}

// UpdateInstanceSet replaces the set's placements and recomputes its bounds.
//
// It writes no GPU memory. The placements are copied into the set's CPU
// staging list, and each frame slot's buffer is filled from it inside DrawFrame
// once that slot's fence has signalled -- the only moment no submitted frame
// can still be reading it. A game may therefore rebase a set's transforms every
// tick, from anywhere in its update, without pooling sets or waiting for the
// device; see the InstanceSet.staging comment for what went wrong when the
// write went straight to the shared buffer.
//
// Several calls in one frame are fine and the last one wins: only the frame
// about to be recorded reads staging.
//
// Instances past the set's capacity are dropped rather than silently
// reallocating underneath buffers the GPU may still be reading.
func (r *Renderer) UpdateInstanceSet(s *InstanceSet, instances []MeshInstance) {
	if s == nil {
		return
	}
	n := len(instances)
	if n > s.capacity {
		n = s.capacity
	}
	s.staging = append(s.staging[:0], instances[:n]...)
	for i := range s.slots {
		s.slots[i].dirty = true
	}
	s.recomputeBounds(s.staging)
}

// flushInstanceSets copies staging into frame slot f's buffer for every staged
// set whose copy in that slot is out of date, and publishes that slot's buffer
// and count as what this frame's passes bind.
//
// Called from DrawFrame once slot f's fence has been waited on, beside the
// joint, particle and dynamic-mesh flushes and for the same reason: the fence
// is the one point in the frame at which nothing submitted can still be reading
// this slot's memory. InstanceSetLOD.upload already had this ordering; the
// ordinary set did not, and host writes are invisible to the validation layer,
// so the ordering is asserted rather than described.
func (r *Renderer) flushInstanceSets(frame int) {
	if !r.fenceWaited[frame] {
		// Loud, because the failure this prevents is silent: a copy landing in
		// a slot a submitted frame is still reading corrupts that frame's
		// geometry on the GPU and nothing anywhere reports it.
		panic(fmt.Sprintf("renderer: instance staging flushed into slot %d before its fence was waited on", frame))
	}
	for _, s := range r.instanceSets {
		// A destroyed set keeps its registration until its deferred release
		// runs; its slots are gone or going, and nothing may draw from it.
		if !s.staged || s.destroyed {
			continue
		}
		sl := &s.slots[frame]
		if sl.dirty {
			if n := len(s.staging); n > 0 {
				copy(unsafe.Slice((*MeshInstance)(sl.mapped), s.capacity), s.staging)
			}
			sl.count = len(s.staging)
			sl.dirty = false
		}
		s.buffer, s.count = sl.buffer, sl.count
	}
}

// recomputeBounds builds a sphere around every instance's transformed mesh
// bound. It is a loose fit -- centre of the bounding box of the centres, radius
// out to the farthest -- which is what a frustum test wants: cheap, and never
// smaller than the truth.
//
// Two walks over the placements rather than one walk plus a pair of scratch
// slices: UpdateInstanceSet is on the per-frame path for a game that rebases
// its placements around the camera, and two slice allocations per set per frame
// is what that costs. The second walk repeats the same centre and scale
// arithmetic on the same inputs, so the result is unchanged: checked, not
// assumed, by running both forms over 500 random sets of up to 200 placements
// and comparing centre and radius for bit equality.
func (s *InstanceSet) recomputeBounds(instances []MeshInstance) {
	if len(instances) == 0 {
		s.boundCenter, s.boundRadius = [3]float32{}, 0
		return
	}

	mc := s.Mesh.BoundCenter
	center := func(m *[16]float32) [3]float32 {
		return [3]float32{
			m[0]*mc[0] + m[4]*mc[1] + m[8]*mc[2] + m[12],
			m[1]*mc[0] + m[5]*mc[1] + m[9]*mc[2] + m[13],
			m[2]*mc[0] + m[6]*mc[1] + m[10]*mc[2] + m[14],
		}
	}

	var lo, hi [3]float32
	for i := range instances {
		c := center(&instances[i].Model)
		if i == 0 {
			lo, hi = c, c
			continue
		}
		for a := 0; a < 3; a++ {
			lo[a] = min(lo[a], c[a])
			hi[a] = max(hi[a], c[a])
		}
	}

	mid := [3]float32{(lo[0] + hi[0]) / 2, (lo[1] + hi[1]) / 2, (lo[2] + hi[2]) / 2}
	var r float32
	for i := range instances {
		m := &instances[i].Model
		c := center(m)
		sx := float32(math.Sqrt(float64(m[0]*m[0] + m[1]*m[1] + m[2]*m[2])))
		sy := float32(math.Sqrt(float64(m[4]*m[4] + m[5]*m[5] + m[6]*m[6])))
		sz := float32(math.Sqrt(float64(m[8]*m[8] + m[9]*m[9] + m[10]*m[10])))
		dx, dy, dz := c[0]-mid[0], c[1]-mid[1], c[2]-mid[2]
		d := float32(math.Sqrt(float64(dx*dx+dy*dy+dz*dz))) + s.Mesh.BoundRadius*max(sx, sy, sz)
		r = max(r, d)
	}
	s.boundCenter, s.boundRadius = mid, r
}

// DestroyInstanceSet releases the set's instance buffers -- one per frame in
// flight -- after the frames currently in flight have finished drawing from
// them, and deregisters it from r.instanceSets so Renderer.Destroy does not
// free it a second time at shutdown.
//
// Deferred through DeferDestroy for the same reason every other release here
// is, and the reason DestroyMesh and DestroyTexture now are too: a
// submitted frame can still be reading this set's buffer in
// recordInstanced/recordInstancedShadow at the moment a game decides to give
// it back -- the harder case, not the easier one, is a level's -reload swap,
// where the set is replaced and released in the same tick the new one starts
// drawing. Measured, not assumed: freeing now instead of deferring produced
// zero validation-layer messages in every configuration tried, including
// with the draw list still naming the freed set -- the nil Mesh below (and,
// short of that, examples/22-level's own ResourceCounts assertion) always
// wins the race to notice first, so the layer never gets a turn. See the
// break recorded in docs/agents/instancing.md for exactly what was tried.
//
// Everything the deferred callback needs (every slot's buffer, memory and
// mapped pointer) is captured into locals before this returns, and s's own
// copies are zeroed
// immediately -- not left for the callback to clear -- because a game must be
// able to tell a destroyed set from a live one the moment this call returns,
// the same contract DestroyModel gives ModelMesh.
//
// s.Mesh is nilled for the same reason, and it is what makes a stale draw --
// one from a game that did not remove the set from its draw list first, see
// docs/agents/instancing.md's "stop drawing it first" -- fail LOUD. Left
// alone, a stale draw would pass this set's now-zeroed buffer handle straight
// to vkCmdBindVertexBuffers: caught by the validation layer if one is
// running, silently wrong if not. With Mesh nil, recordInstanced and
// recordInstancedShadow panic on set.Mesh.vertexBuffer the moment they reach
// a destroyed set, in Go, every time, layer or no layer -- the same trade
// DestroyModel already makes for ModelMesh's Mesh/Texture/Material fields.
//
// s.count is deliberately left as it was, NOT zeroed. Both draw functions
// skip a set on `set.count == 0` before they ever reach set.Mesh; zeroing
// count here would route a stale draw through that skip instead of the nil
// pointer above, turning a loud failure back into the silent one this exists
// to avoid.
//
// Idempotent and nil-safe: a second call, or a call on nil, does nothing.
func (r *Renderer) DestroyInstanceSet(s *InstanceSet) {
	if s == nil || s.destroyed {
		return
	}
	s.destroyed = true

	buffer, memory, mapped := s.buffer, s.memory, s.mapped
	slots := s.slots
	if s.staged {
		// buffer/memory/mapped are only the published view of whichever slot
		// the last recorded frame bound, so releasing them as well would
		// destroy one of the slot buffers below a second time.
		buffer, memory, mapped = core1_0.Buffer{}, core1_0.DeviceMemory{}, nil
	}
	s.slots = [maxFramesInFlight]instanceSlot{}
	s.buffer = core1_0.Buffer{}
	s.memory = core1_0.DeviceMemory{}
	s.mapped = nil
	s.Mesh = nil

	r.DeferDestroy(func() {
		// Every slot, not just the last one bound: each frame in flight has its
		// own buffer and each has to come back.
		for i := range slots {
			slots[i].release(r.deviceDriver)
		}
		if mapped != nil {
			r.deviceDriver.UnmapMemory(memory)
		}
		if buffer.Handle() != 0 {
			r.deviceDriver.DestroyBuffer(buffer, nil)
		}
		if memory.Handle() != 0 {
			r.deviceDriver.FreeMemory(memory, nil)
		}
		// Deregistered here, not at the top of this function, for the same
		// reason retireMesh and retireTexture deregister at retirement: a
		// ResourceCounts taken right after this call still has to report the
		// set as live, because it genuinely still is for
		// maxFramesInFlight more frames. examples/22-level's -reload loop
		// asserts exactly that in the same tick as an -instanced swap.
		for i, other := range r.instanceSets {
			if other == s {
				r.instanceSets = append(r.instanceSets[:i], r.instanceSets[i+1:]...)
				break
			}
		}
	})
}

// destroy releases the set's buffer directly, with no deferral. Called only
// from the renderer's teardown stack (Renderer.Destroy has already waited for
// the device to go idle), and only for a set still in r.instanceSets --
// DestroyInstanceSet removes an entry from that list as soon as its own
// deferred free runs, so this never runs twice against the same set.
func (s *InstanceSet) destroy(deviceDriver core1_0.DeviceDriver) {
	if s == nil {
		return
	}
	for i := range s.slots {
		s.slots[i].release(deviceDriver)
	}
	if s.staged {
		// A view of a slot buffer that has just been released; see
		// DestroyInstanceSet. Also the shape a failed CreateInstanceSet
		// unwinds through, where nothing was ever published.
		s.buffer, s.memory, s.mapped = core1_0.Buffer{}, core1_0.DeviceMemory{}, nil
		return
	}
	if s.mapped != nil {
		deviceDriver.UnmapMemory(s.memory)
		s.mapped = nil
	}
	if s.buffer.Handle() != 0 {
		deviceDriver.DestroyBuffer(s.buffer, nil)
		s.buffer = core1_0.Buffer{}
	}
	if s.memory.Handle() != 0 {
		deviceDriver.FreeMemory(s.memory, nil)
		s.memory = core1_0.DeviceMemory{}
	}
}

// instanceBindingDescription is binding 1: one MeshInstance per instance.
func instanceBindingDescription() core1_0.VertexInputBindingDescription {
	return core1_0.VertexInputBindingDescription{
		Binding:   1,
		Stride:    meshInstanceSize,
		InputRate: core1_0.VertexInputRateInstance,
	}
}

// instanceAttributeDescriptions adds the per-instance attributes to the
// standard per-vertex ones.
//
// A mat4 attribute is four consecutive locations of vec4, which is why the
// model occupies 4 through 7 and the tint lands at 8 rather than 5.
func instanceAttributeDescriptions() []core1_0.VertexInputAttributeDescription {
	attrs := vertexAttributeDescriptions()
	for i := 0; i < 4; i++ {
		attrs = append(attrs, core1_0.VertexInputAttributeDescription{
			Location: uint32(4 + i),
			Binding:  1,
			Format:   core1_0.FormatR32G32B32A32SignedFloat,
			Offset:   i * 16,
		})
	}
	return append(attrs, core1_0.VertexInputAttributeDescription{
		Location: 8,
		Binding:  1,
		Format:   core1_0.FormatR32G32B32A32SignedFloat,
		Offset:   64,
	})
}
