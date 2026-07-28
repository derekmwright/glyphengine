package renderer

import (
	"fmt"
	"math"
	"unsafe"

	"github.com/vkngwrapper/core/v3/core1_0"
)

// CreateSkinnedIndexedMesh uploads skinned vertices and uint16 indices to device-local GPU buffers.
func (r *Renderer) CreateSkinnedIndexedMesh(vertices []SkinnedVertex, indices []uint16) (*Mesh, error) {
	vbufSize := len(vertices) * sizeOf[SkinnedVertex]()
	vdata := unsafe.Slice((*byte)(unsafe.Pointer(&vertices[0])), vbufSize)

	vbuf, vmem, err := r.createDeviceLocalBuffer(vdata, core1_0.BufferUsageVertexBuffer)
	if err != nil {
		return nil, fmt.Errorf("create skinned vertex buffer: %w", err)
	}

	ibufSize := len(indices) * 2
	idata := unsafe.Slice((*byte)(unsafe.Pointer(&indices[0])), ibufSize)

	ibuf, imem, err := r.createDeviceLocalBuffer(idata, core1_0.BufferUsageIndexBuffer)
	if err != nil {
		r.deviceDriver.FreeMemory(vmem, nil)
		r.deviceDriver.DestroyBuffer(vbuf, nil)
		return nil, fmt.Errorf("create skinned index buffer: %w", err)
	}

	center, radius := computeSkinnedBoundingSphere(vertices)
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

// CreateSkinnedIndexedMesh32 uploads skinned vertices and uint32 indices to device-local GPU buffers.
func (r *Renderer) CreateSkinnedIndexedMesh32(vertices []SkinnedVertex, indices []uint32) (*Mesh, error) {
	vbufSize := len(vertices) * sizeOf[SkinnedVertex]()
	vdata := unsafe.Slice((*byte)(unsafe.Pointer(&vertices[0])), vbufSize)

	vbuf, vmem, err := r.createDeviceLocalBuffer(vdata, core1_0.BufferUsageVertexBuffer)
	if err != nil {
		return nil, fmt.Errorf("create skinned vertex buffer: %w", err)
	}

	ibufSize := len(indices) * 4
	idata := unsafe.Slice((*byte)(unsafe.Pointer(&indices[0])), ibufSize)

	ibuf, imem, err := r.createDeviceLocalBuffer(idata, core1_0.BufferUsageIndexBuffer)
	if err != nil {
		r.deviceDriver.FreeMemory(vmem, nil)
		r.deviceDriver.DestroyBuffer(vbuf, nil)
		return nil, fmt.Errorf("create skinned index buffer: %w", err)
	}

	center, radius := computeSkinnedBoundingSphere(vertices)
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

func computeSkinnedBoundingSphere(vertices []SkinnedVertex) ([3]float32, float32) {
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
