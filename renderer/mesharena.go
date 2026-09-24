package renderer

import (
	"encoding/binary"
	"fmt"
	"math"
	"slices"
	"unsafe"

	"github.com/vkngwrapper/core/v3/core1_0"
)

// MeshArenaDesc fixes storage capacity in vertices and indices. Indices passed
// to Alloc are local to that range; Index32 chooses the stored index width.
type MeshArenaDesc struct {
	Name              string
	Vertices, Indices int
	Index32           bool
}

// MeshArena owns one device-local vertex buffer and one index buffer. All
// methods require the renderer thread. Ranges are immutable: replace changed
// geometry with Free and Alloc. Remove all draws borrowing a range before Free.
type MeshArena struct {
	r                         *Renderer
	desc                      MeshArenaDesc
	vertices, indices         lodBuffer
	freeVertices, freeIndices meshFreeList
	live                      map[*Mesh]meshAllocation
	usedVertices, usedIndices int
	destroyed                 bool
}

type meshAllocation struct{ vertex, index meshSpan }
type meshSpan struct{ first, count int }
type meshFreeList []meshSpan

func (f *meshFreeList) alloc(n int) (meshSpan, bool) {
	for i, s := range *f {
		if s.count < n {
			continue
		}
		result := meshSpan{s.first, n}
		if s.count == n {
			*f = slices.Delete(*f, i, i+1)
		} else {
			(*f)[i] = meshSpan{s.first + n, s.count - n}
		}
		return result, true
	}
	return meshSpan{}, false
}

func (f *meshFreeList) free(s meshSpan) {
	i := 0
	for i < len(*f) && (*f)[i].first < s.first {
		i++
	}
	*f = slices.Insert(*f, i, s)
	if i > 0 && (*f)[i-1].first+(*f)[i-1].count == s.first {
		(*f)[i-1].count += s.count
		*f = slices.Delete(*f, i, i+1)
		i--
	}
	if i+1 < len(*f) && (*f)[i].first+(*f)[i].count == (*f)[i+1].first {
		(*f)[i].count += (*f)[i+1].count
		*f = slices.Delete(*f, i+1, i+2)
	}
}

func (r *Renderer) CreateMeshArena(d MeshArenaDesc) (*MeshArena, error) {
	// Vulkan's base vertex is signed 32-bit; host byte sizes must also fit.
	width := 2
	if d.Index32 {
		width = 4
	}
	if d.Vertices <= 0 || d.Indices <= 0 || d.Vertices > math.MaxInt32 || uint64(d.Indices) > math.MaxUint32 || d.Vertices > math.MaxInt/sizeOf[Vertex]() || d.Indices > math.MaxInt/width {
		return nil, fmt.Errorf("mesh arena %q: invalid vertex/index capacity", d.Name)
	}
	a := &MeshArena{r: r, desc: d, freeVertices: meshFreeList{{0, d.Vertices}}, freeIndices: meshFreeList{{0, d.Indices}}, live: make(map[*Mesh]meshAllocation)}
	if err := a.vertices.create(r, d.Vertices*sizeOf[Vertex](), core1_0.BufferUsageVertexBuffer|core1_0.BufferUsageTransferDst, false); err != nil {
		a.release()
		return nil, fmt.Errorf("mesh arena %q: vertices: %w", d.Name, err)
	}
	if err := a.indices.create(r, d.Indices*width, core1_0.BufferUsageIndexBuffer|core1_0.BufferUsageTransferDst, false); err != nil {
		a.release()
		return nil, fmt.Errorf("mesh arena %q: indices: %w", d.Name, err)
	}
	r.meshArenas = append(r.meshArenas, a)
	return a, nil
}

// Alloc uploads both slices in one synchronous staged submission. It waits for
// earlier graphics queue work and completion before returning, so callers may
// discard the slices. Per-frame streaming should use AllocAsync.
func (a *MeshArena) Alloc(vertices []Vertex, indices []uint32) (*Mesh, error) {
	m, _, err := a.alloc(vertices, indices, false)
	return m, err
}

// AllocAsync is Alloc without the queue wait: the range is reserved and the
// data is copied into staging immediately, but the copy into the arena's
// storage is recorded in the next DrawFrame's batch. The range is not drawn
// until its ticket is ready. The caller may discard the slices on return.
//
// The arena's buffers are being read by the frames still in flight, so this
// upload also carries the leading barrier that orders the copy after those
// reads. See recordUploads.
func (a *MeshArena) AllocAsync(vertices []Vertex, indices []uint32) (*Mesh, *UploadTicket, error) {
	return a.alloc(vertices, indices, true)
}

func (a *MeshArena) alloc(vertices []Vertex, indices []uint32, async bool) (*Mesh, *UploadTicket, error) {
	if a == nil {
		return nil, nil, fmt.Errorf("mesh arena: nil arena")
	}
	fail := func(reason string) (*Mesh, *UploadTicket, error) {
		return nil, nil, fmt.Errorf("mesh arena %q: %s", a.desc.Name, reason)
	}
	if a.destroyed {
		return fail("destroyed")
	}
	if len(vertices) == 0 || len(indices) == 0 {
		return fail("vertices and indices must be nonempty")
	}
	for _, idx := range indices {
		if uint64(idx) >= uint64(len(vertices)) {
			return fail("index outside range vertices")
		}
		if !a.desc.Index32 && idx > math.MaxUint16 {
			return fail("index exceeds uint16; use Index32")
		}
	}
	v, ok := a.freeVertices.alloc(len(vertices))
	if !ok {
		return fail("full: no contiguous vertex range")
	}
	i, ok := a.freeIndices.alloc(len(indices))
	if !ok {
		a.freeVertices.free(v)
		return fail("full: no contiguous index range")
	}
	width, kind := 2, core1_0.IndexTypeUInt16
	if a.desc.Index32 {
		width, kind = 4, core1_0.IndexTypeUInt32
	}
	idata := make([]byte, len(indices)*width)
	for n, idx := range indices {
		if width == 4 {
			binary.LittleEndian.PutUint32(idata[n*4:], idx)
		} else {
			binary.LittleEndian.PutUint16(idata[n*2:], uint16(idx))
		}
	}
	vdata := unsafe.Slice((*byte)(unsafe.Pointer(&vertices[0])), len(vertices)*sizeOf[Vertex]())
	uploads := []bufferUpload{{a.vertices.buffer, v.first * sizeOf[Vertex](), vdata}, {a.indices.buffer, i.first * width, idata}}
	center, radius := computeBoundingSphere(vertices)
	m := &Mesh{vertexBuffer: a.vertices.buffer, indexBuffer: a.indices.buffer, VertexCount: len(vertices), IndexCount: len(indices), indexType: kind, firstIndex: uint32(i.first), vertexOffset: v.first, owner: a, BoundCenter: center, BoundRadius: radius}
	var ticket *UploadTicket
	var err error
	if async {
		ticket, err = a.r.queueUpload(uploads, m, false, true)
	} else {
		err = a.r.uploadBufferRanges(uploads)
	}
	if err != nil {
		a.freeVertices.free(v)
		a.freeIndices.free(i)
		return nil, nil, fmt.Errorf("mesh arena %q: upload: %w", a.desc.Name, err)
	}
	a.live[m] = meshAllocation{v, i}
	a.usedVertices += v.count
	a.usedIndices += i.count
	return m, ticket, nil
}

// Free retires a range after all frames in flight. Stats includes retiring
// ranges until then. Repeated frees are harmless; a foreign range panics.
func (a *MeshArena) Free(m *Mesh) {
	if m == nil {
		return
	}
	if a == nil || m.owner != a {
		panic("mesh arena Free: range belongs to another arena")
	}
	if m.destroyed {
		return
	}
	allocation, ok := a.live[m]
	if !ok {
		panic(fmt.Sprintf("mesh arena %q: unknown range", a.desc.Name))
	}
	m.destroyed = true
	// A queued copy into this range is dropped if it has not been recorded;
	// one already recorded still lands inside the span, which the free list
	// does not hand back until after this same deferred countdown.
	if m.upload != nil {
		a.r.cancelUpload(m)
	}
	a.r.DeferDestroy(func() {
		a.freeVertices.free(allocation.vertex)
		a.freeIndices.free(allocation.index)
		a.usedVertices -= allocation.vertex.count
		a.usedIndices -= allocation.index.count
		delete(a.live, m)
	})
}

func (a *MeshArena) Stats() (usedVertices, usedIndices, ranges int) {
	if a == nil {
		return
	}
	return a.usedVertices, a.usedIndices, len(a.live)
}

// DestroyMeshArena refuses to destroy storage while any unfreed ranges remain.
// A programming error panics with the arena's name. Freed ranges may still be
// retiring: their callbacks precede this deferred buffer release.
func (r *Renderer) DestroyMeshArena(a *MeshArena) {
	if a == nil {
		return
	}
	if a.r != r {
		panic(fmt.Sprintf("mesh arena %q: belongs to another renderer", a.desc.Name))
	}
	if a.destroyed {
		return
	}
	for m := range a.live {
		if !m.destroyed {
			panic(fmt.Sprintf("mesh arena %q: cannot destroy with live ranges", a.desc.Name))
		}
	}
	a.destroyed = true
	r.DeferDestroy(func() {
		a.release()
		r.meshArenas = slices.DeleteFunc(r.meshArenas, func(other *MeshArena) bool { return other == a })
	})
}

func (a *MeshArena) release() {
	a.indices.destroy(a.r)
	a.vertices.destroy(a.r)
}

// Called after device idle, including applications that did not explicitly
// free their ranges. Borrowers never own the arena's Vulkan handles.
func (r *Renderer) destroyMeshArenas() {
	for _, a := range r.meshArenas {
		for m := range a.live {
			m.destroyed = true
		}
		clear(a.live)
		a.usedVertices, a.usedIndices = 0, 0
		a.destroyed = true
		a.release()
	}
	r.meshArenas = nil
}
