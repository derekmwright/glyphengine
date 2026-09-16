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
	Tint  [4]float32 // rgb multiplies the mesh's vertex colour; w is unused
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
// Renderer.UpdateInstanceSet. The buffer is host-visible and written directly
// rather than staged through a transfer, because placements change when a
// player builds something and staging a 72KB copy through a command buffer for
// that is more machinery than the write is worth.
type InstanceSet struct {
	Mesh *Mesh

	buffer   core1_0.Buffer
	memory   core1_0.DeviceMemory
	mapped   unsafe.Pointer
	capacity int
	count    int

	// Bound sphere over every instance, in world space. The draw list frustum
	// tests this once for the set rather than once per instance.
	//
	// Per-instance CPU culling is the other option the design had and is not
	// done: it would mean re-uploading the visible subset every frame, which
	// trades the CPU cost this feature exists to remove for a different one.
	// The cost of not doing it is vertex shading for instances that are off
	// screen, which for the sizes this is aimed at is small -- 900 domes of 200
	// triangles is 180k triangles, and the GPU does not notice. Measure before
	// changing that; see docs/agents/instancing.md.
	boundCenter [3]float32
	boundRadius float32
}

// Count returns how many instances the set currently draws.
func (s *InstanceSet) Count() int {
	if s == nil {
		return 0
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
// list frustum tests.
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
	buf, mem, err := r.createBuffer(size,
		core1_0.BufferUsageVertexBuffer,
		core1_0.MemoryPropertyHostVisible|core1_0.MemoryPropertyHostCoherent,
	)
	if err != nil {
		return nil, fmt.Errorf("create instance set: %w", err)
	}

	// Mapped once and left mapped. Vulkan allows a persistent mapping, the
	// memory is host-coherent so there is nothing to flush, and an update is
	// then a copy rather than a map/unmap pair.
	ptr, _, err := r.deviceDriver.MapMemory(mem, 0, size, 0)
	if err != nil {
		r.deviceDriver.DestroyBuffer(buf, nil)
		r.deviceDriver.FreeMemory(mem, nil)
		return nil, fmt.Errorf("create instance set: map memory: %w", err)
	}

	s := &InstanceSet{
		Mesh:     mesh,
		buffer:   buf,
		memory:   mem,
		mapped:   ptr,
		capacity: capacity,
	}
	r.instanceSets = append(r.instanceSets, s)
	r.UpdateInstanceSet(s, instances)
	return s, nil
}

// UpdateInstanceSet replaces the set's placements and recomputes its bounds.
//
// Instances past the set's capacity are dropped rather than silently
// reallocating underneath a buffer the GPU may still be reading.
func (r *Renderer) UpdateInstanceSet(s *InstanceSet, instances []MeshInstance) {
	if s == nil {
		return
	}
	n := len(instances)
	if n > s.capacity {
		n = s.capacity
	}
	s.count = n
	if n > 0 {
		dst := unsafe.Slice((*MeshInstance)(s.mapped), s.capacity)
		copy(dst[:n], instances[:n])
	}
	s.recomputeBounds(instances[:n])
}

// recomputeBounds builds a sphere around every instance's transformed mesh
// bound. It is a loose fit -- centre of the bounding box of the centres, radius
// out to the farthest -- which is what a frustum test wants: cheap, and never
// smaller than the truth.
func (s *InstanceSet) recomputeBounds(instances []MeshInstance) {
	if len(instances) == 0 {
		s.boundCenter, s.boundRadius = [3]float32{}, 0
		return
	}

	mc := s.Mesh.BoundCenter
	centers := make([][3]float32, len(instances))
	scales := make([]float32, len(instances))
	var lo, hi [3]float32
	for i := range instances {
		m := &instances[i].Model
		c := [3]float32{
			m[0]*mc[0] + m[4]*mc[1] + m[8]*mc[2] + m[12],
			m[1]*mc[0] + m[5]*mc[1] + m[9]*mc[2] + m[13],
			m[2]*mc[0] + m[6]*mc[1] + m[10]*mc[2] + m[14],
		}
		sx := float32(math.Sqrt(float64(m[0]*m[0] + m[1]*m[1] + m[2]*m[2])))
		sy := float32(math.Sqrt(float64(m[4]*m[4] + m[5]*m[5] + m[6]*m[6])))
		sz := float32(math.Sqrt(float64(m[8]*m[8] + m[9]*m[9] + m[10]*m[10])))
		centers[i] = c
		scales[i] = max(sx, sy, sz)
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
	for i := range centers {
		dx, dy, dz := centers[i][0]-mid[0], centers[i][1]-mid[1], centers[i][2]-mid[2]
		d := float32(math.Sqrt(float64(dx*dx+dy*dy+dz*dz))) + s.Mesh.BoundRadius*scales[i]
		r = max(r, d)
	}
	s.boundCenter, s.boundRadius = mid, r
}

// destroy releases the set's buffer. Called from the renderer's teardown stack,
// never by a game: a set outlives the frames that reference it.
func (s *InstanceSet) destroy(deviceDriver core1_0.DeviceDriver) {
	if s == nil {
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
