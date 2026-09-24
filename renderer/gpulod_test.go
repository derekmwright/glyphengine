package renderer

import (
	"testing"
	"unsafe"

	"github.com/derekmwright/glyphengine/renderer/framegraph"
	"github.com/vkngwrapper/core/v3/core1_0"
)

func (d *fakeDriver) CmdDrawIndirect(_ core1_0.CommandBuffer, b core1_0.Buffer, offset, count, stride int) {
	d.fold(100)
	if d.hashing {
		d.h = d.h.Uint64(uint64(b.Handle())).Int(offset).Int(count).Int(stride)
	}
}
func (d *fakeDriver) CmdDrawIndexedIndirect(_ core1_0.CommandBuffer, b core1_0.Buffer, offset, count, stride int) {
	d.fold(101)
	if d.hashing {
		d.h = d.h.Uint64(uint64(b.Handle())).Int(offset).Int(count).Int(stride)
	}
}
func (d *fakeDriver) CmdCopyBuffer(_ core1_0.CommandBuffer, src, dst core1_0.Buffer, regions ...core1_0.BufferCopy) error {
	d.fold(102)
	if d.hashing {
		d.h = d.h.Uint64(uint64(src.Handle())).Uint64(uint64(dst.Handle()))
		for _, c := range regions {
			d.h = d.h.Int(c.SrcOffset).Int(c.DstOffset).Int(c.Size)
		}
	}
	return nil
}

func gpuFrame(t *testing.T, n int) (*frame, *Renderer, []RenderObject) {
	t.Helper()
	fx := buildFrame(n)
	r, input := withLOD(fx, n)
	s := r.lodSets[0]
	h := &fakeHandles{next: 20000}
	g := &gpuLOD{pipeline: h.pipeline(), layout: h.layout(), placements: lodBuffer{buffer: h.buffer(), size: n * 80}}
	s.gpu = g
	for f := range maxFramesInFlight {
		g.sets = append(g.sets, h.descSet())
		for i, b := range []*lodBuffer{&g.output[f], &g.commands[f], &g.readback[f], &g.uniform[f], &g.scratch[f]} {
			b.size = []int{n * 80 * 4, 84, 84, 128, (n*6 + ((n+63)/64)*9) * 4}[i]
			b.buffer = h.buffer()
			bytes := make([]byte, b.size)
			b.mapped = unsafe.Pointer(&bytes[0])
		}
		for i := range s.buckets {
			b := &s.buckets[i].frames[f]
			b.buffer = g.output[f].buffer
			b.offset = i * n * 80
			b.indirect = g.commands[f].buffer
			b.indirectOffset = i * 20
		}
	}
	g.copy[0] = core1_0.BufferCopy{Size: 84}
	var err error
	fx.graph, err = newFrameGraph(core1_0.Samples4, core1_0.FormatD32SignedFloat, core1_0.FormatB8G8R8A8SRGB, 1, r)
	if err != nil {
		t.Fatal(err)
	}
	for i := range fx.graph.nodes {
		fx.graph.nodes[i].targets = []*renderingTarget{newRenderingTarget(fx.extent)}
	}
	for i := range fx.graph.images {
		fx.graph.images[i].images = make([]core1_0.Image, 1)
	}
	for id, b := range fx.graph.lodBuffers {
		fx.graph.images[id] = b
	}
	fx.graph.sizeScratch(&fx.scratch)
	return fx, r, input
}

// Verified break: removing indexed indirect submission changes the 40-placement
// stream from 1721 calls / ea9a8852d29ae8ce to 1710 / 07dbac282af4b0ac.
// Dynamic rendering (#120): full-stream hashes include explicit attachment
// barriers and CmdBegin/EndRendering. Base 3317 -> 3342 calls; depth 3353,
// application 3382, compute 3390, glow 3442, volumetric 3348, GPU LOD 1746.
// TestMigrationDrawStreams independently pins every non-rendering/barrier call
// to the measured pre-migration stream, including all draw-side arguments.
func TestGPULODStreamAndAllocation(t *testing.T) {
	for _, n := range []int{40, 160} {
		fx, r, input := gpuFrame(t, n)
		d := &fakeDriver{hashing: true}
		fx.draws = r.prepareLOD(input, fx.lighting, 1)
		if err := fx.record(d, 1); err != nil {
			t.Fatal(err)
		}
		t.Logf("GPU LOD n=%d: %d calls hash %#x", n, d.calls, d.h)
		if n == 40 && (d.calls != 1746 || d.h != 0x79ba242a27987ced) {
			t.Errorf("pin fixture: %d %#x", d.calls, d.h)
		}
		d.hashing = false
		if a := testing.AllocsPerRun(30, func() {
			fx.draws = r.prepareLOD(input, fx.lighting, 1)
			if err := fx.record(d, 1); err != nil {
				panic(err)
			}
		}); a != 0 {
			t.Fatalf("%d placements: %g allocations", n, a)
		}
		// No readback count can suppress an indirect command: the current GPU
		// selection may have filled a previously empty bucket.
		if len(fx.draws) != len(input)+3 {
			t.Fatal("empty previous counts suppressed a bucket")
		}
	}
}

// Verified break: adding four to the emitted buffer offset changes the pinned
// hash f3ef92b26f21e803 to f7ac34f988e0b21b, with the same two calls.
func TestBufferExecutorFixture(t *testing.T) {
	g := framegraph.New()
	id := g.AddBuffer(framegraph.BufferDesc{Name: "draw data", Size: 320})
	g.AddNode(framegraph.Node{Kind: framegraph.Compute, Uses: []framegraph.Use{{Resource: id, Access: framegraph.StorageWrite}}})
	g.AddNode(framegraph.Node{Kind: framegraph.Graphics, Uses: []framegraph.Use{{Resource: id, Access: framegraph.VertexRead}, {Resource: id, Access: framegraph.IndirectRead}}})
	p, err := g.Build()
	if err != nil {
		t.Fatal(err)
	}
	h := &fakeHandles{next: 30000}
	f := &frameGraph{plan: p, images: []graphImage{{buffers: []core1_0.Buffer{h.buffer()}}}}
	d := &fakeDriver{hashing: true}
	scratch := &commandScratch{}
	f.sizeScratch(scratch)
	c := &graphFrame{driver: d, cmd: h.commandBuffer(), scratch: scratch}
	if err = f.barriers(c, p.Steps[1].Barriers); err != nil {
		t.Fatal(err)
	}
	t.Logf("buffer fixture: %d calls hash %#x", d.calls, d.h)
	if d.calls != 2 || d.h != 0xf3ef92b26f21e803 {
		t.Errorf("pin buffer fixture: %d %#x", d.calls, d.h)
	}
}

// Verified before the GPU trace branch: unsafe.Slice panics with a nil mapped
// pointer and a nonzero retired count. Device-local memory has no CPU mapping.
func TestGPULODTraceDoesNotMapDeviceLocalBuckets(t *testing.T) {
	fx, r, input := gpuFrame(t, 40)
	r.prepareLOD(input, fx.lighting, 0)
	for _, d := range r.lodDraws {
		if b := d.Instances; b != nil && b.lod != nil {
			b.mapped = nil
			b.count = 3
		}
	}
	trace := &StateTrace{}
	r.traceStreamedBuffers(trace)
	if len(trace.line) == 0 {
		t.Fatal("empty GPU trace")
	}
}
