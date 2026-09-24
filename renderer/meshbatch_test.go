package renderer

import (
	"errors"
	"fmt"
	"reflect"
	"testing"
	"unsafe"

	"github.com/go-gl/mathgl/mgl32"
	"github.com/vkngwrapper/core/v3/core1_0"
)

type batchTestInstance struct {
	bufferTestInstance
	multi, first bool
}

func (d batchTestInstance) GetPhysicalDeviceFeatures(core1_0.PhysicalDevice) *core1_0.PhysicalDeviceFeatures {
	return &core1_0.PhysicalDeviceFeatures{MultiDrawIndirect: d.multi, DrawIndirectFirstInstance: d.first}
}
func (d batchTestInstance) GetPhysicalDeviceProperties(p core1_0.PhysicalDevice) (*core1_0.PhysicalDeviceProperties, error) {
	v, e := d.bufferTestInstance.GetPhysicalDeviceProperties(p)
	v.Limits.MaxDrawIndirectCount = 2
	return v, e
}

func batchSources() []RenderObject {
	h := &fakeHandles{next: 40000}
	a := &MeshArena{}
	var out []RenderObject
	for i := range 3 {
		m := fakeMesh(h, 4, 6, 0.1)
		m.owner = a
		m.vertexOffset = 11 + i*7
		m.firstIndex = uint32(13 + i*9)
		model := mgl32.Ident4()
		model[12] = float32(i) * 2
		out = append(out, RenderObject{Mesh: m, Model: model, MVP: model, Color: [3]float32{1, 1, 1}})
	}
	return out
}

type batchDrawDriver struct {
	fakeDriver
	indirect [][3]int
	direct   [][5]int
}

func (d *batchDrawDriver) CmdDrawIndexedIndirect(cb core1_0.CommandBuffer, b core1_0.Buffer, offset, count, stride int) {
	d.indirect = append(d.indirect, [3]int{offset, count, stride})
	d.fakeDriver.CmdDrawIndexedIndirect(cb, b, offset, count, stride)
}
func (d *batchDrawDriver) CmdDrawIndexed(cb core1_0.CommandBuffer, n, count int, first uint32, base int, instance uint32) {
	d.direct = append(d.direct, [5]int{n, count, int(first), base, int(instance)})
	d.fakeDriver.CmdDrawIndexed(cb, n, count, first, base, instance)
}

func TestMeshBatchArgumentsFallbackAndViews(t *testing.T) {
	for _, caps := range [][2]bool{{true, true}, {false, true}, {true, false}, {false, false}} {
		r, bd := bufferFixture()
		r.instanceDriver = batchTestInstance{multi: caps[0], first: caps[1]}
		sources := batchSources()
		sources[2].ShadowOnly = true
		draws, err := r.prepareMeshBatches(sources, 0)
		if err != nil {
			t.Fatal(err)
		}
		if len(draws) != 1 || draws[0].Instances == nil {
			t.Fatal("not batched")
		}
		b := draws[0].Instances.batch
		instances := unsafe.Slice((*MeshInstance)(r.rangeBatchFrames[0].vertices.mapped), 3)
		if instances[1].Model != sources[1].Model || instances[2].Model != sources[2].Model {
			t.Fatal("lost distinct transforms")
		}
		d := &batchDrawDriver{}
		stats := &RenderStats{}
		b.record(d, stats, core1_0.CommandBuffer{}, nil)
		want := []uint32{6, 1, 13, 11, 0, 6, 1, 22, 18, 1}
		if !reflect.DeepEqual(b.commands[:10], want) {
			t.Fatalf("commands %v want %v", b.commands[:10], want)
		}
		if stats.Instances != 2 || stats.Triangles != 4 {
			t.Fatal(stats)
		}
		if caps[0] && caps[1] {
			if len(d.indirect) != 1 || d.indirect[0] != [3]int{0, 2, 20} {
				t.Fatal(d.indirect)
			}
		} else {
			if !reflect.DeepEqual(d.direct, [][5]int{{6, 1, 13, 11, 0}, {6, 1, 22, 18, 1}}) {
				t.Fatal(d.direct)
			}
		}
		// Shadow view sees the first range alone. The main command bytes must
		// survive, because all views execute only after recording finishes.
		frustum := ExtractFrustum(mgl32.Ident4())
		b.record(d, stats, core1_0.CommandBuffer{}, &frustum)
		if !reflect.DeepEqual(b.commands[:10], want) || b.commands[15] != 6 || b.commands[17] != 13 {
			t.Fatal("views alias")
		}
		if stats.Instances != 3 {
			t.Fatal("per-range shadow culling lost", stats)
		}
		// All three visible requires two API calls when maxDrawIndirectCount=2.
		b.sources[2].ShadowOnly = false
		b.record(d, stats, core1_0.CommandBuffer{}, nil)
		if caps[0] && caps[1] && len(d.indirect) != 4 {
			t.Fatal("device draw limit ignored", d.indirect)
		}
		r.rangeBatchFrames[0].destroy(r)
		arenaBalance(t, bd)
	}
}

func TestMeshBatchMaterialBoundaryAndFrameStorage(t *testing.T) {
	r, d := bufferFixture()
	r.instanceDriver = batchTestInstance{multi: true, first: true}
	sources := batchSources()
	sources[2].Roughness = 0.7
	for f := range maxFramesInFlight {
		out, err := r.prepareMeshBatches(sources, f)
		if err != nil {
			t.Fatal(err)
		}
		if len(out) != 2 || out[1].Mesh != sources[2].Mesh || out[1].Instances != nil {
			t.Fatal("material boundary lost")
		}
	}
	if r.rangeBatchFrames[0].vertices.buffer == r.rangeBatchFrames[1].vertices.buffer || r.rangeBatchFrames[0].commands.buffer == r.rangeBatchFrames[1].commands.buffer {
		t.Fatal("frames share writable storage")
	}
	before := d.destroyed["Buffer"]
	if _, err := r.prepareMeshBatches(append(sources, sources...), 0); err != nil {
		t.Fatal(err)
	}
	if d.destroyed["Buffer"] != before || len(r.deferredDestroys) != 1 {
		t.Fatal("growth freed old buffers immediately")
	}
	r.flushAllDeferred()
	for f := range r.rangeBatchFrames {
		r.rangeBatchFrames[f].destroy(r)
	}
	arenaBalance(t, d)
}

func TestMeshBatchCreationUnwind(t *testing.T) {
	create := func(r *Renderer) error {
		r.instanceDriver = batchTestInstance{multi: true, first: true}
		_, err := r.prepareMeshBatches(batchSources(), 0)
		return err
	}
	cleanup := func(r *Renderer) {
		for f := range r.rangeBatchFrames {
			r.rangeBatchFrames[f].destroy(r)
		}
		r.flushAllDeferred()
	}
	r, control := bufferFixture()
	if err := create(r); err != nil {
		t.Fatal(err)
	}
	cleanup(r)
	arenaBalance(t, control)
	for _, call := range []string{"CreateBuffer", "AllocateMemory", "BindBufferMemory", "MapMemory"} {
		if control.calls[call] != 2 {
			t.Fatalf("%s: expected two creation sites", call)
		}
		for at := 1; at <= 2; at++ {
			t.Run(fmt.Sprintf("%s/%d", call, at), func(t *testing.T) {
				r, d := bufferFixture()
				d.failCall, d.failAt = call, at
				if err := create(r); !errors.Is(err, errInjected) {
					t.Fatal(err)
				}
				cleanup(r)
				arenaBalance(t, d)
			})
		}
	}
	for kind, n := range control.created {
		if n == 0 {
			continue
		}
		control.destroyed[kind]--
		capture := &capturingT{TB: t}
		arenaBalance(capture, control)
		control.destroyed[kind]++
		if !capture.failed {
			t.Fatalf("balance meta-check missed %s", kind)
		}
	}
}

func TestMeshBatchSteadyRecordingAllocations(t *testing.T) {
	r, driver := bufferFixture()
	r.instanceDriver = batchTestInstance{multi: true, first: true}
	sources := batchSources()
	gpu := &fakeDriver{}
	stats := &RenderStats{}
	record := func() {
		draws, err := r.prepareMeshBatches(sources, 0)
		if err != nil {
			panic(err)
		}
		draws[0].Instances.batch.record(gpu, stats, core1_0.CommandBuffer{}, nil)
	}
	record()
	if n := testing.AllocsPerRun(30, record); n != 0 {
		t.Fatalf("steady batches allocate %g per frame", n)
	}
	r.rangeBatchFrames[0].destroy(r)
	arenaBalance(t, driver)
}
