package renderer

import (
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
	"testing"
	"unsafe"

	"github.com/vkngwrapper/core/v3/core1_0"
)

func arenaTriangle() ([]Vertex, []uint32) {
	return []Vertex{{Pos: [3]float32{-1, 0, 0}}, {Pos: [3]float32{1, 0, 0}}, {Pos: [3]float32{0, 2, 0}}}, []uint32{0, 1, 2}
}

func mustArena(t *testing.T, r *Renderer, width bool) *MeshArena {
	t.Helper()
	a, err := r.CreateMeshArena(MeshArenaDesc{Name: "patches", Vertices: 12, Indices: 12, Index32: width})
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func TestMeshArenaFragmentationAndDeferredReuse(t *testing.T) {
	for _, width := range []bool{false, true} {
		r, d := bufferFixture()
		a := mustArena(t, r, width)
		v, i := arenaTriangle()
		var meshes []*Mesh
		for n := 0; n < 4; n++ {
			m, err := a.Alloc(v, i)
			if err != nil {
				t.Fatal(err)
			}
			if m.firstIndex != uint32(n*3) || m.vertexOffset != n*3 || m.BoundRadius <= 0 || m.vertexBuffer != a.vertices.buffer || m.indexBuffer != a.indices.buffer {
				t.Fatalf("bad range: %+v", m)
			}
			meshes = append(meshes, m)
		}
		if _, err := a.Alloc(v, i); err == nil || !strings.Contains(err.Error(), "patches") {
			t.Fatal("full arena did not fail by name")
		}
		// Nonadjacent holes are not one larger free range.
		a.Free(meshes[0])
		r.DestroyMesh(meshes[2])
		a.Free(meshes[0])
		for f := 0; f < maxFramesInFlight; f++ {
			if u, j, n := a.Stats(); u != 12 || j != 12 || n != 4 {
				t.Fatal("free was immediate")
			}
			if _, err := a.Alloc(v, i); err == nil {
				t.Fatal("reused in-flight range")
			}
			r.flushDeferredDestroys()
		}
		if _, err := a.Alloc(append(slicesVertices(v), v...), []uint32{0, 1, 2, 3, 4, 5}); err == nil {
			t.Fatal("fragmentation ignored")
		}
		a.Free(meshes[1])
		r.flushAllDeferred()
		m, err := a.Alloc(append(slicesVertices(v), v...), []uint32{0, 1, 2, 3, 4, 5})
		if err != nil || m.firstIndex != 0 || m.vertexOffset != 0 {
			t.Fatalf("coalescing: %v %v", m, err)
		}
		a.Free(m)
		a.Free(meshes[3])
		r.DestroyMeshArena(a)
		if a.vertices.buffer.Handle() == 0 {
			t.Fatal("arena destroyed before retirement")
		}
		r.flushAllDeferred()
		r.destroyMeshArenas()
		bufferBalance(t, d)
	}
}
func slicesVertices(v []Vertex) []Vertex { return append([]Vertex(nil), v...) }

func TestMeshArenaRefusalAndRollback(t *testing.T) {
	r, d := bufferFixture()
	a := mustArena(t, r, false)
	v, i := arenaTriangle()
	m, err := a.Alloc(v, i)
	if err != nil {
		t.Fatal(err)
	}
	func() {
		defer func() {
			if got := recover(); got == nil || !strings.Contains(fmt.Sprint(got), "patches") {
				t.Fatal("live arena destroy did not refuse by name")
			}
		}()
		r.DestroyMeshArena(a)
	}()
	if a.destroyed {
		t.Fatal("refusal changed arena")
	}
	for _, idx := range [][]uint32{nil, {3}, {65536}} {
		if _, err := a.Alloc(v, idx); err == nil {
			t.Fatal("bad indices accepted")
		}
	}
	if _, err := a.Alloc(nil, i); err == nil {
		t.Fatal("empty vertices accepted")
	}
	d.failCall, d.failAt = "CmdCopyBuffer", d.calls["CmdCopyBuffer"]+2
	if _, err := a.Alloc(v, i); !errors.Is(err, errInjected) {
		t.Fatal(err)
	}
	d.failCall = ""
	if u, j, n := a.Stats(); u != 3 || j != 3 || n != 1 {
		t.Fatal("failed upload retained range")
	}
	n, err := a.Alloc(v, i)
	if err != nil || n.firstIndex != 3 || n.vertexOffset != 3 {
		t.Fatal("upload rollback", err)
	}
	if r.UpdateMeshData(m, v, []uint16{0, 1, 2}) == nil {
		t.Fatal("range became dynamic")
	}
	a.Free(m)
	a.Free(n)
	r.DestroyMeshArena(a)
	r.flushAllDeferred()
	if _, err := a.Alloc(v, i); err == nil {
		t.Fatal("destroyed arena allocated")
	}
	bufferBalance(t, d)
}

func arenaBalance(tb testing.TB, d *bufferTestDriver) {
	tb.Helper()
	for kind, n := range d.created {
		if n != d.destroyed[kind] {
			tb.Errorf("%s: created %d destroyed %d", kind, n, d.destroyed[kind])
		}
	}
}

func TestMeshArenaCreationUnwind(t *testing.T) {
	create := func(r *Renderer) error {
		a, err := r.CreateMeshArena(MeshArenaDesc{Name: "unwind", Vertices: 12, Indices: 12})
		if err != nil {
			return err
		}
		v, i := arenaTriangle()
		_, err = a.Alloc(v, i)
		return err
	}
	r, control := bufferFixture()
	if err := create(r); err != nil {
		t.Fatal(err)
	}
	r.destroyMeshArenas()
	arenaBalance(t, control)
	for _, call := range []string{"CreateBuffer", "AllocateMemory", "BindBufferMemory", "MapMemory", "AllocateCommandBuffers", "BeginCommandBuffer", "CmdPipelineBarrier", "CmdCopyBuffer", "EndCommandBuffer"} {
		if control.calls[call] == 0 {
			t.Fatalf("no sites for %s", call)
		}
		for at := 1; at <= control.calls[call]; at++ {
			t.Run(fmt.Sprintf("%s/%d", call, at), func(t *testing.T) {
				r, d := bufferFixture()
				d.failCall, d.failAt = call, at
				if err := create(r); !errors.Is(err, errInjected) {
					t.Fatalf("missing injected error: %v", err)
				}
				r.destroyMeshArenas()
				arenaBalance(t, d)
			})
		}
		t.Logf("%s: all %d arena creation/upload sites unwind", call, control.calls[call])
	}
	for kind, n := range control.created {
		if n == 0 {
			continue
		}
		control.destroyed[kind]--
		captured := &capturingT{TB: t}
		arenaBalance(captured, control)
		control.destroyed[kind]++
		if !captured.failed {
			t.Fatalf("balance meta-check missed %s", kind)
		}
	}
	t.Log("balance meta-check detects a missing destroy for every arena resource kind")
}

type rangeDrawDriver struct {
	fakeDriver
	draws [][3]int
}

func (d *rangeDrawDriver) CmdDrawIndexed(cb core1_0.CommandBuffer, n, instances int, first uint32, base int, instance uint32) {
	d.draws = append(d.draws, [3]int{n, int(first), base})
	d.fakeDriver.CmdDrawIndexed(cb, n, instances, first, base, instance)
}

// Verified break: zeroing the recorder's range arguments produces
// [[3 0 0] [3 0 0] [3 0 0]] and fails this check. Restored offsets retain
// the independent pin below and leave all pre-existing stream pins unchanged.
func TestMeshArenaThreeRangeStream(t *testing.T) {
	r, _ := bufferFixture()
	a := mustArena(t, r, true)
	v, idx := arenaTriangle()
	fx := buildFrame(0)
	fx.draws = nil
	fx.overlays = nil
	fx.celestials = nil
	fx.uiOverlays = nil
	fx.msdfOverlays = nil
	fx.grass = nil
	fx.particles = nil
	fx.lighting.ShadowEnabled = false
	fx.lighting.PointRange = 0
	for range 3 {
		m, err := a.Alloc(v, idx)
		if err != nil {
			t.Fatal(err)
		}
		fx.draws = append(fx.draws, RenderObject{Mesh: m, Model: identityMat(0), MVP: identityMat(0)})
	}
	d := &rangeDrawDriver{fakeDriver: fakeDriver{hashing: true}}
	if err := fx.record(d, 0); err != nil {
		t.Fatal(err)
	}
	want := [][3]int{{3, 0, 0}, {3, 3, 3}, {3, 6, 6}}
	if fmt.Sprint(d.draws) != fmt.Sprint(want) {
		t.Fatalf("range stream %v want %v", d.draws, want)
	}
	t.Logf("three ranges: %d calls, hash %#x, indexed draws %v", d.calls, d.h, d.draws)
	if d.calls != 68 || d.h != 0x40fb439f34810846 {
		t.Fatalf("three-range stream changed: %d %#x", d.calls, d.h)
	}
}

func TestGPULODRangeArguments(t *testing.T) {
	fx, r, input := gpuFrame(t, 40)
	s := r.lodSets[0]
	s.levels[0].Mesh.firstIndex = 17
	s.levels[0].Mesh.vertexOffset = 31
	r.prepareLOD(input, fx.lighting, 0)
	data := unsafe.Slice((*byte)(s.gpu.uniform[0].mapped), 128)
	if binary.LittleEndian.Uint32(data[64:]) != 17 || binary.LittleEndian.Uint32(data[96:]) != 31 {
		t.Fatal("GPU LOD omitted mesh range offsets")
	}
}

// Exercise the real recorder's shadow cascades/cube, terrain, ordinary and
// skinned, translucent, water, grass, particles, instancing and overlay paths.
func TestMeshRangesAcrossRecordingPaths(t *testing.T) {
	fx := buildFrame(40)
	set := func(m *Mesh) {
		if m != nil {
			m.firstIndex = 19
			m.vertexOffset = 37
		}
	}
	for _, ds := range [][]RenderObject{fx.draws, fx.overlays, fx.celestials, fx.msdfOverlays} {
		for _, d := range ds {
			set(d.Mesh)
			if d.Instances != nil {
				set(d.Instances.Mesh)
			}
		}
	}
	for _, d := range fx.uiOverlays {
		set(d.Mesh)
	}
	for _, v := range fx.grass.Variants {
		set(v.Mesh)
	}
	set(fx.particles.QuadMesh)
	d := &rangeDrawDriver{}
	if err := fx.record(d, 0); err != nil {
		t.Fatal(err)
	}
	if len(d.draws) < 50 {
		t.Fatal("too few mesh draws to cover recording paths", len(d.draws))
	}
	for _, call := range d.draws {
		if call[1] != 19 || call[2] != 37 {
			t.Fatal("recording path dropped range offsets", call)
		}
	}
	// Application mesh passes have a separate recorder from the engine loop.
	h := &fakeHandles{next: 50000}
	p := &AppPass{r: &Renderer{fallbackTexture: fx.fallbackTexture}, draws: fx.draws[:1], sets: []core1_0.DescriptorSet{h.descSet()}}
	d.draws = nil
	p.record(&graphFrame{driver: d, scratch: &fx.scratch})
	if len(d.draws) != 1 || d.draws[0][1] != 19 || d.draws[0][2] != 37 {
		t.Fatal("application pass lost range", d.draws)
	}
}

func TestMeshArenaIndexWidthAndCapacityValidation(t *testing.T) {
	r, d := bufferFixture()
	for _, desc := range []MeshArenaDesc{{}, {Vertices: -1, Indices: 3}, {Vertices: 3, Indices: -1}, {Vertices: 1 << 32, Indices: 3}} {
		if _, err := r.CreateMeshArena(desc); err == nil {
			t.Fatal("bad capacity accepted", desc)
		}
	}
	if d.created["Buffer"] != 0 {
		t.Fatal("invalid capacity touched Vulkan")
	}
	v := make([]Vertex, 65537)
	i := []uint32{0, 65535, 65536}
	for _, wide := range []bool{false, true} {
		a, err := r.CreateMeshArena(MeshArenaDesc{Name: "large local indices", Vertices: len(v), Indices: 3, Index32: wide})
		if err != nil {
			t.Fatal(err)
		}
		m, err := a.Alloc(v, i)
		if wide {
			if err != nil {
				t.Fatal(err)
			}
			a.Free(m)
		} else if err == nil {
			t.Fatal("uint16 index wrapped")
		}
		r.DestroyMeshArena(a)
	}
	r.flushAllDeferred()
	arenaBalance(t, d)
}
