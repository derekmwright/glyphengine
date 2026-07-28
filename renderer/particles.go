package renderer

import (
	"fmt"
	"unsafe"

	"github.com/vkngwrapper/core/v3/core1_0"
)

// ParticleInstance is the per-instance GPU data (32 bytes).
// Matches shader binding 1: vec4 posSize + vec4 color.
type ParticleInstance struct {
	X, Y, Z, Size float32
	R, G, B, A    float32
}

const particleInstanceSize = int(unsafe.Sizeof(ParticleInstance{}))

// ParticleSystem manages GPU resources for instanced billboard particles.
// Instance buffers are double-buffered per frame in flight and persistently
// mapped; new data is staged CPU-side and flushed inside DrawFrame after the
// frame's fence wait, so the GPU never reads a buffer mid-write.
type ParticleSystem struct {
	QuadMesh         *Mesh
	InstanceBuffers  [maxFramesInFlight]core1_0.Buffer
	InstanceMemories [maxFramesInFlight]core1_0.DeviceMemory
	mapped           [maxFramesInFlight][]byte
	staging          []ParticleInstance
	dirty            [maxFramesInFlight]bool
	InstanceCount    int
	MaxInstances     int
}

// CreateParticleSystem allocates a unit quad mesh and per-frame host-visible
// instance buffers, persistently mapped.
func CreateParticleSystem(r *Renderer, maxInstances int) (*ParticleSystem, error) {
	// Unit quad: 4 vertices at corners (-0.5,-0.5) to (0.5,0.5) with UVs.
	vertices := []Vertex{
		{Pos: [3]float32{-0.5, -0.5, 0}, UV: [2]float32{0, 0}},
		{Pos: [3]float32{0.5, -0.5, 0}, UV: [2]float32{1, 0}},
		{Pos: [3]float32{0.5, 0.5, 0}, UV: [2]float32{1, 1}},
		{Pos: [3]float32{-0.5, 0.5, 0}, UV: [2]float32{0, 1}},
	}
	// CW winding for Vulkan Y-flip
	indices := []uint16{0, 2, 1, 0, 3, 2}

	quadMesh, err := r.CreateIndexedMesh(vertices, indices)
	if err != nil {
		return nil, fmt.Errorf("create particle quad mesh: %w", err)
	}

	ps := &ParticleSystem{
		QuadMesh:     quadMesh,
		MaxInstances: maxInstances,
		staging:      make([]ParticleInstance, 0, maxInstances),
	}

	instanceSize := maxInstances * particleInstanceSize
	for i := 0; i < maxFramesInFlight; i++ {
		buf, mem, err := r.createBuffer(instanceSize,
			core1_0.BufferUsageVertexBuffer,
			core1_0.MemoryPropertyHostVisible|core1_0.MemoryPropertyHostCoherent,
		)
		if err != nil {
			ps.Destroy(r.deviceDriver)
			return nil, fmt.Errorf("create particle instance buffer %d: %w", i, err)
		}
		ps.InstanceBuffers[i] = buf
		ps.InstanceMemories[i] = mem

		ptr, _, err := r.deviceDriver.MapMemory(mem, 0, instanceSize, 0)
		if err != nil {
			ps.Destroy(r.deviceDriver)
			return nil, fmt.Errorf("map particle instance buffer %d: %w", i, err)
		}
		ps.mapped[i] = unsafe.Slice((*byte)(ptr), instanceSize)
	}

	return ps, nil
}

// UpdateInstances stages particle data CPU-side; it is copied to the current
// frame's instance buffer inside DrawFrame once that frame's fence signals.
func (ps *ParticleSystem) UpdateInstances(r *Renderer, instances []ParticleInstance) {
	count := len(instances)
	if count > ps.MaxInstances {
		count = ps.MaxInstances
		instances = instances[:count]
	}
	ps.InstanceCount = count
	ps.staging = append(ps.staging[:0], instances...)
	for i := range ps.dirty {
		ps.dirty[i] = true
	}
}

// flushUploads copies staged instance data into the given frame's buffer.
// Called from DrawFrame after the frame's fence wait.
func (ps *ParticleSystem) flushUploads(frame int) {
	if !ps.dirty[frame] {
		return
	}
	ps.dirty[frame] = false
	if len(ps.staging) == 0 {
		return
	}
	byteSize := len(ps.staging) * particleInstanceSize
	src := unsafe.Slice((*byte)(unsafe.Pointer(&ps.staging[0])), byteSize)
	copy(ps.mapped[frame][:byteSize], src)
}

// Destroy releases GPU resources.
func (ps *ParticleSystem) Destroy(deviceDriver core1_0.DeviceDriver) {
	for i := 0; i < maxFramesInFlight; i++ {
		if ps.InstanceBuffers[i].Handle() == 0 {
			continue
		}
		if ps.mapped[i] != nil {
			deviceDriver.UnmapMemory(ps.InstanceMemories[i])
			ps.mapped[i] = nil
		}
		deviceDriver.DestroyBuffer(ps.InstanceBuffers[i], nil)
		deviceDriver.FreeMemory(ps.InstanceMemories[i], nil)
	}
}
