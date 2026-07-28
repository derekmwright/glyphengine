package renderer

import (
	"fmt"
	"math"
	"unsafe"

	"github.com/vkngwrapper/core/v3/core1_0"
)

// Mesh holds GPU vertex/index buffers and their counts.
type Mesh struct {
	vertexBuffer core1_0.Buffer
	vertexMemory core1_0.DeviceMemory
	VertexCount  int

	indexBuffer core1_0.Buffer
	indexMemory core1_0.DeviceMemory
	IndexCount  int
	indexType   core1_0.IndexType

	BoundCenter [3]float32 // object-space bounding sphere center
	BoundRadius float32    // object-space bounding sphere radius (0 = skip culling)
}

func computeBoundingSphere(vertices []Vertex) ([3]float32, float32) {
	if len(vertices) == 0 {
		return [3]float32{}, 0
	}
	var cx, cy, cz float64
	for i := range vertices {
		cx += float64(vertices[i].Pos[0])
		cy += float64(vertices[i].Pos[1])
		cz += float64(vertices[i].Pos[2])
	}
	n := float64(len(vertices))
	cx /= n
	cy /= n
	cz /= n

	var maxDist2 float64
	for i := range vertices {
		dx := float64(vertices[i].Pos[0]) - cx
		dy := float64(vertices[i].Pos[1]) - cy
		dz := float64(vertices[i].Pos[2]) - cz
		d2 := dx*dx + dy*dy + dz*dz
		if d2 > maxDist2 {
			maxDist2 = d2
		}
	}
	return [3]float32{float32(cx), float32(cy), float32(cz)}, float32(math.Sqrt(maxDist2))
}

// createBuffer allocates a Vulkan buffer with the given usage and memory properties.
func (r *Renderer) createBuffer(size int, usage core1_0.BufferUsageFlags, memProps core1_0.MemoryPropertyFlags) (core1_0.Buffer, core1_0.DeviceMemory, error) {
	buf, _, err := r.deviceDriver.CreateBuffer(nil, core1_0.BufferCreateInfo{
		Size:        size,
		Usage:       usage,
		SharingMode: core1_0.SharingModeExclusive,
	})
	if err != nil {
		return core1_0.Buffer{}, core1_0.DeviceMemory{}, fmt.Errorf("create buffer: %w", err)
	}

	memReqs := r.deviceDriver.GetBufferMemoryRequirements(buf)
	memTypeIdx, err := findMemoryType(r.instanceDriver, r.physicalDevice, memReqs.MemoryTypeBits, memProps)
	if err != nil {
		r.deviceDriver.DestroyBuffer(buf, nil)
		return core1_0.Buffer{}, core1_0.DeviceMemory{}, err
	}

	mem, _, err := r.deviceDriver.AllocateMemory(nil, core1_0.MemoryAllocateInfo{
		AllocationSize:  memReqs.Size,
		MemoryTypeIndex: memTypeIdx,
	})
	if err != nil {
		r.deviceDriver.DestroyBuffer(buf, nil)
		return core1_0.Buffer{}, core1_0.DeviceMemory{}, fmt.Errorf("allocate buffer memory: %w", err)
	}

	_, err = r.deviceDriver.BindBufferMemory(buf, mem, 0)
	if err != nil {
		r.deviceDriver.FreeMemory(mem, nil)
		r.deviceDriver.DestroyBuffer(buf, nil)
		return core1_0.Buffer{}, core1_0.DeviceMemory{}, fmt.Errorf("bind buffer memory: %w", err)
	}

	return buf, mem, nil
}

// createDeviceLocalBuffer uploads data to a device-local buffer via a staging buffer.
func (r *Renderer) createDeviceLocalBuffer(data []byte, usage core1_0.BufferUsageFlags) (core1_0.Buffer, core1_0.DeviceMemory, error) {
	size := len(data)

	// Create host-visible staging buffer
	stagingBuf, stagingMem, err := r.createBuffer(size,
		core1_0.BufferUsageTransferSrc,
		core1_0.MemoryPropertyHostVisible|core1_0.MemoryPropertyHostCoherent,
	)
	if err != nil {
		return core1_0.Buffer{}, core1_0.DeviceMemory{}, fmt.Errorf("staging: %w", err)
	}
	defer r.deviceDriver.DestroyBuffer(stagingBuf, nil)
	defer r.deviceDriver.FreeMemory(stagingMem, nil)

	// Map, copy, unmap
	ptr, _, err := r.deviceDriver.MapMemory(stagingMem, 0, size, 0)
	if err != nil {
		return core1_0.Buffer{}, core1_0.DeviceMemory{}, fmt.Errorf("map staging memory: %w", err)
	}
	copy(unsafe.Slice((*byte)(ptr), size), data)
	r.deviceDriver.UnmapMemory(stagingMem)

	// Create device-local destination buffer
	dstBuf, dstMem, err := r.createBuffer(size,
		usage|core1_0.BufferUsageTransferDst,
		core1_0.MemoryPropertyDeviceLocal,
	)
	if err != nil {
		return core1_0.Buffer{}, core1_0.DeviceMemory{}, fmt.Errorf("device-local: %w", err)
	}

	// Copy staging → device-local
	cmdBuf, err := r.beginSingleTimeCommands()
	if err != nil {
		r.deviceDriver.FreeMemory(dstMem, nil)
		r.deviceDriver.DestroyBuffer(dstBuf, nil)
		return core1_0.Buffer{}, core1_0.DeviceMemory{}, err
	}

	err = r.deviceDriver.CmdCopyBuffer(cmdBuf, stagingBuf, dstBuf, core1_0.BufferCopy{Size: size})
	if err != nil {
		r.deviceDriver.FreeMemory(dstMem, nil)
		r.deviceDriver.DestroyBuffer(dstBuf, nil)
		return core1_0.Buffer{}, core1_0.DeviceMemory{}, fmt.Errorf("record copy command: %w", err)
	}

	err = r.endSingleTimeCommands(cmdBuf)
	if err != nil {
		r.deviceDriver.FreeMemory(dstMem, nil)
		r.deviceDriver.DestroyBuffer(dstBuf, nil)
		return core1_0.Buffer{}, core1_0.DeviceMemory{}, err
	}

	return dstBuf, dstMem, nil
}

// CreateMesh uploads vertices to a device-local GPU buffer and returns a Mesh.
func (r *Renderer) CreateMesh(vertices []Vertex) (*Mesh, error) {
	bufSize := len(vertices) * sizeOf[Vertex]()
	data := unsafe.Slice((*byte)(unsafe.Pointer(&vertices[0])), bufSize)

	buf, mem, err := r.createDeviceLocalBuffer(data, core1_0.BufferUsageVertexBuffer)
	if err != nil {
		return nil, fmt.Errorf("create vertex buffer: %w", err)
	}

	center, radius := computeBoundingSphere(vertices)
	m := &Mesh{
		vertexBuffer: buf,
		vertexMemory: mem,
		VertexCount:  len(vertices),
		BoundCenter:  center,
		BoundRadius:  radius,
	}
	r.meshes = append(r.meshes, m)
	return m, nil
}

// CreateIndexedMesh uploads vertices and uint16 indices to device-local GPU buffers.
func (r *Renderer) CreateIndexedMesh(vertices []Vertex, indices []uint16) (*Mesh, error) {
	vbufSize := len(vertices) * sizeOf[Vertex]()
	vdata := unsafe.Slice((*byte)(unsafe.Pointer(&vertices[0])), vbufSize)

	vbuf, vmem, err := r.createDeviceLocalBuffer(vdata, core1_0.BufferUsageVertexBuffer)
	if err != nil {
		return nil, fmt.Errorf("create vertex buffer: %w", err)
	}

	ibufSize := len(indices) * 2 // uint16 = 2 bytes
	idata := unsafe.Slice((*byte)(unsafe.Pointer(&indices[0])), ibufSize)

	ibuf, imem, err := r.createDeviceLocalBuffer(idata, core1_0.BufferUsageIndexBuffer)
	if err != nil {
		r.deviceDriver.FreeMemory(vmem, nil)
		r.deviceDriver.DestroyBuffer(vbuf, nil)
		return nil, fmt.Errorf("create index buffer: %w", err)
	}

	center, radius := computeBoundingSphere(vertices)
	m := &Mesh{
		vertexBuffer: vbuf,
		vertexMemory: vmem,
		VertexCount:  len(vertices),
		indexBuffer:  ibuf,
		indexMemory:  imem,
		IndexCount:   len(indices),
		indexType:    core1_0.IndexTypeUInt16,
		BoundCenter:  center,
		BoundRadius:  radius,
	}
	r.meshes = append(r.meshes, m)
	return m, nil
}

// CreateIndexedMesh32 uploads vertices and uint32 indices to device-local GPU buffers.
func (r *Renderer) CreateIndexedMesh32(vertices []Vertex, indices []uint32) (*Mesh, error) {
	vbufSize := len(vertices) * sizeOf[Vertex]()
	vdata := unsafe.Slice((*byte)(unsafe.Pointer(&vertices[0])), vbufSize)

	vbuf, vmem, err := r.createDeviceLocalBuffer(vdata, core1_0.BufferUsageVertexBuffer)
	if err != nil {
		return nil, fmt.Errorf("create vertex buffer: %w", err)
	}

	ibufSize := len(indices) * 4 // uint32 = 4 bytes
	idata := unsafe.Slice((*byte)(unsafe.Pointer(&indices[0])), ibufSize)

	ibuf, imem, err := r.createDeviceLocalBuffer(idata, core1_0.BufferUsageIndexBuffer)
	if err != nil {
		r.deviceDriver.FreeMemory(vmem, nil)
		r.deviceDriver.DestroyBuffer(vbuf, nil)
		return nil, fmt.Errorf("create index buffer: %w", err)
	}

	center, radius := computeBoundingSphere(vertices)
	m := &Mesh{
		vertexBuffer: vbuf,
		vertexMemory: vmem,
		VertexCount:  len(vertices),
		indexBuffer:  ibuf,
		indexMemory:  imem,
		IndexCount:   len(indices),
		indexType:    core1_0.IndexTypeUInt32,
		BoundCenter:  center,
		BoundRadius:  radius,
	}
	r.meshes = append(r.meshes, m)
	return m, nil
}

// dynamicMesh holds per-frame-in-flight buffer sets for a mesh whose contents
// change at runtime (text, UI panels, overlays). Updates are staged CPU-side
// and flushed in DrawFrame after the frame's fence wait, then the Mesh is
// repointed at the freshly written buffers, so the GPU never reads a buffer
// the CPU is writing. Buffers are persistently mapped.
type dynamicMesh struct {
	vbufs   [maxFramesInFlight]core1_0.Buffer
	vmems   [maxFramesInFlight]core1_0.DeviceMemory
	vmapped [maxFramesInFlight][]byte
	ibufs   [maxFramesInFlight]core1_0.Buffer
	imems   [maxFramesInFlight]core1_0.DeviceMemory
	imapped [maxFramesInFlight][]byte

	stagingV []Vertex
	stagingI []uint16
	dirty    [maxFramesInFlight]bool
}

// destroy unmaps and releases all per-frame buffer sets.
func (dm *dynamicMesh) destroy(deviceDriver core1_0.DeviceDriver) {
	for i := 0; i < maxFramesInFlight; i++ {
		if dm.vmapped[i] != nil {
			deviceDriver.UnmapMemory(dm.vmems[i])
			dm.vmapped[i] = nil
		}
		if dm.vbufs[i].Handle() != 0 {
			deviceDriver.DestroyBuffer(dm.vbufs[i], nil)
			deviceDriver.FreeMemory(dm.vmems[i], nil)
		}
		if dm.imapped[i] != nil {
			deviceDriver.UnmapMemory(dm.imems[i])
			dm.imapped[i] = nil
		}
		if dm.ibufs[i].Handle() != 0 {
			deviceDriver.DestroyBuffer(dm.ibufs[i], nil)
			deviceDriver.FreeMemory(dm.imems[i], nil)
		}
	}
}

// CreateDynamicIndexedMesh allocates per-frame-in-flight vertex + index buffers
// at max size for runtime updates, persistently mapped.
func (r *Renderer) CreateDynamicIndexedMesh(maxVertices, maxIndices int) (*Mesh, error) {
	vbufSize := maxVertices * sizeOf[Vertex]()
	ibufSize := maxIndices * 2 // uint16

	dm := &dynamicMesh{}
	for i := 0; i < maxFramesInFlight; i++ {
		vbuf, vmem, err := r.createBuffer(vbufSize,
			core1_0.BufferUsageVertexBuffer,
			core1_0.MemoryPropertyHostVisible|core1_0.MemoryPropertyHostCoherent,
		)
		if err != nil {
			dm.destroy(r.deviceDriver)
			return nil, fmt.Errorf("dynamic vertex buffer %d: %w", i, err)
		}
		dm.vbufs[i], dm.vmems[i] = vbuf, vmem

		vptr, _, err := r.deviceDriver.MapMemory(vmem, 0, vbufSize, 0)
		if err != nil {
			dm.destroy(r.deviceDriver)
			return nil, fmt.Errorf("map dynamic vertex buffer %d: %w", i, err)
		}
		dm.vmapped[i] = unsafe.Slice((*byte)(vptr), vbufSize)

		ibuf, imem, err := r.createBuffer(ibufSize,
			core1_0.BufferUsageIndexBuffer,
			core1_0.MemoryPropertyHostVisible|core1_0.MemoryPropertyHostCoherent,
		)
		if err != nil {
			dm.destroy(r.deviceDriver)
			return nil, fmt.Errorf("dynamic index buffer %d: %w", i, err)
		}
		dm.ibufs[i], dm.imems[i] = ibuf, imem

		iptr, _, err := r.deviceDriver.MapMemory(imem, 0, ibufSize, 0)
		if err != nil {
			dm.destroy(r.deviceDriver)
			return nil, fmt.Errorf("map dynamic index buffer %d: %w", i, err)
		}
		dm.imapped[i] = unsafe.Slice((*byte)(iptr), ibufSize)
	}

	m := &Mesh{
		vertexBuffer: dm.vbufs[0],
		vertexMemory: dm.vmems[0],
		VertexCount:  0,
		indexBuffer:  dm.ibufs[0],
		indexMemory:  dm.imems[0],
		IndexCount:   0,
		indexType:    core1_0.IndexTypeUInt16,
	}
	r.meshes = append(r.meshes, m)
	r.dynamicMeshes[m] = dm
	return m, nil
}

// UpdateMeshData stages new vertex + index data for a dynamic mesh; the data
// is copied into the current frame's buffers inside DrawFrame once that
// frame's fence has signaled. An empty vertex slice hides the mesh.
func (r *Renderer) UpdateMeshData(m *Mesh, vertices []Vertex, indices []uint16) error {
	dm, ok := r.dynamicMeshes[m]
	if !ok {
		return fmt.Errorf("UpdateMeshData: mesh was not created with CreateDynamicIndexedMesh")
	}
	// Clamp to allocated capacity.
	if maxV := len(dm.vmapped[0]) / sizeOf[Vertex](); len(vertices) > maxV {
		vertices = vertices[:maxV]
	}
	if maxI := len(dm.imapped[0]) / 2; len(indices) > maxI {
		indices = indices[:maxI]
	}
	dm.stagingV = append(dm.stagingV[:0], vertices...)
	dm.stagingI = append(dm.stagingI[:0], indices...)
	for i := range dm.dirty {
		dm.dirty[i] = true
	}
	return nil
}

// flushDynamicMeshes copies staged mesh data into the given frame's buffers
// and repoints each mesh at them. Called from DrawFrame after the frame's
// fence wait, so the GPU is guaranteed to be done reading these buffers.
// Meshes that weren't updated keep pointing at their last-written buffers.
func (r *Renderer) flushDynamicMeshes(frame int) {
	for m, dm := range r.dynamicMeshes {
		if !dm.dirty[frame] {
			continue
		}
		dm.dirty[frame] = false

		m.VertexCount = len(dm.stagingV)
		m.IndexCount = len(dm.stagingI)
		if len(dm.stagingV) == 0 {
			continue
		}

		vbytes := len(dm.stagingV) * sizeOf[Vertex]()
		copy(dm.vmapped[frame][:vbytes], unsafe.Slice((*byte)(unsafe.Pointer(&dm.stagingV[0])), vbytes))
		if len(dm.stagingI) > 0 {
			ibytes := len(dm.stagingI) * 2
			copy(dm.imapped[frame][:ibytes], unsafe.Slice((*byte)(unsafe.Pointer(&dm.stagingI[0])), ibytes))
		}

		m.vertexBuffer = dm.vbufs[frame]
		m.vertexMemory = dm.vmems[frame]
		m.indexBuffer = dm.ibufs[frame]
		m.indexMemory = dm.imems[frame]
	}
}

// DestroyMesh releases GPU resources for a mesh. Dynamic meshes are destroyed
// after all in-flight frames finish referencing them.
func (r *Renderer) DestroyMesh(m *Mesh) {
	if dm, ok := r.dynamicMeshes[m]; ok {
		delete(r.dynamicMeshes, m)
		r.DeferDestroy(func() { dm.destroy(r.deviceDriver) })
		return
	}
	if m.indexBuffer.Handle() != 0 {
		r.deviceDriver.DestroyBuffer(m.indexBuffer, nil)
		r.deviceDriver.FreeMemory(m.indexMemory, nil)
	}
	r.deviceDriver.DestroyBuffer(m.vertexBuffer, nil)
	r.deviceDriver.FreeMemory(m.vertexMemory, nil)
}
