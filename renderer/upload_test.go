package renderer

import (
	"errors"
	"fmt"
	"testing"

	"github.com/derekmwright/glyphengine/renderer/framegraph"
	"github.com/vkngwrapper/core/v3/core1_0"
)

// streamFrame drives one frame of the upload path in the order DrawFrame does:
// the fence wait's deferred flush, then claiming the queue, recording the
// batch, and handing it to the slot its submission carried it in.
func streamFrame(r *Renderer, d core1_0.DeviceDriver, f int) {
	r.flushDeferredDestroys()
	r.beginUploadBatch()
	r.recordUploads(&graphFrame{driver: d, scratch: &r.cmdScratch})
	r.finishUploadBatch(f)
}

// TestStreamedUploadRecordsOnceAndRetiresWithItsFence is the whole contract in
// one run: nothing is copied at enqueue, the copy is recorded in the next
// frame and only that frame, the ticket is not ready until the fence of the
// submission that carried it has been waited on, and the staging goes at the
// same moment and not before.
//
// Verified to fail, one break at a time:
//   - finishUploadBatch without its DeferDestroy: "frame 2: ticket ready
//     false, want true" (and TestStreamedMeshIsNotDrawnUntilItLands with
//     "landed upload still skipped: 3 draws, 1 skipped").
//   - beginUploadBatch not clearing uploadQueue: "copy recorded again in
//     frame 1: 4 copies".
//   - retireUploads called straight from finishUploadBatch instead of
//     deferred: "ticket ready before its fence was waited on".
func TestStreamedUploadRecordsOnceAndRetiresWithItsFence(t *testing.T) {
	r, d := bufferFixture()
	v, idx := arenaTriangle()
	m, ticket, err := r.CreateIndexedMesh32Async(v, idx)
	if err != nil {
		t.Fatal(err)
	}
	if d.copies != 0 {
		t.Fatalf("enqueue recorded %d copies; the whole point is that it does not", d.copies)
	}
	if ticket.Ready() {
		t.Fatal("ticket ready before any frame carried the copy")
	}
	if got := r.ResourceCounts().PendingUploads; got != 1 {
		t.Fatalf("PendingUploads %d, want the one staging buffer", got)
	}
	if m.upload != ticket || r.uploadPending != 1 {
		t.Fatal("mesh was not attached to its upload")
	}

	streamFrame(r, d, 0)
	if d.copies != 2 {
		t.Fatalf("frame 0 recorded %d copies, want the vertex and index range", d.copies)
	}
	if ticket.Ready() {
		t.Fatal("ticket ready before its fence was waited on")
	}
	if d.destroyed["Buffer"] != 0 {
		t.Fatal("staging retired before the copy could have run")
	}

	for f := 1; f <= maxFramesInFlight; f++ {
		streamFrame(r, d, f%maxFramesInFlight)
		if d.copies != 2 {
			t.Fatalf("copy recorded again in frame %d: %d copies", f, d.copies)
		}
		if want := f == maxFramesInFlight; ticket.Ready() != want {
			t.Fatalf("frame %d: ticket ready %v, want %v", f, ticket.Ready(), want)
		}
	}
	if m.upload != nil || r.uploadPending != 0 {
		t.Fatal("a landed upload still holds its mesh back")
	}
	if got := d.destroyed["Buffer"]; got != 1 {
		t.Fatalf("%d staging buffers retired, want 1", got)
	}
	if got := r.ResourceCounts().PendingUploads; got != 0 {
		t.Fatalf("PendingUploads %d after retirement", got)
	}
	r.DestroyMesh(m)
	r.flushAllDeferred()
	bufferBalance(t, d)
}

// TestStreamedArenaRangeCarriesItsOwnBarriers pins the two barrier groups
// apart: a fresh buffer has no reader to wait for, an arena range beside
// ranges being drawn does.
//
// Verified to fail: passing shared=false for the arena upload gives
// "arena=true: 1 barrier groups, want 2". Removing the trailing group
// entirely gives "arena=false: 0 barrier groups, want 1".
func TestStreamedArenaRangeCarriesItsOwnBarriers(t *testing.T) {
	for _, arena := range []bool{false, true} {
		r, d := bufferFixture()
		v, idx := arenaTriangle()
		probe := &uploadProbe{bufferTestDriver: d}
		var m *Mesh
		var err error
		if arena {
			a := mustArena(t, r, true)
			m, _, err = a.AllocAsync(v, idx)
		} else {
			m, _, err = r.CreateIndexedMesh32Async(v, idx)
		}
		if err != nil {
			t.Fatal(err)
		}
		streamFrame(r, probe, 0)
		want := 1
		if arena {
			want = 2
		}
		if probe.barriers != want {
			t.Fatalf("arena=%v: %d barrier groups, want %d", arena, probe.barriers, want)
		}
		if probe.widest != 2 {
			t.Fatalf("arena=%v: %d ranges in the widest group, want the vertex and index range in one call", arena, probe.widest)
		}
		_ = m
	}
}

// uploadProbe counts the barrier groups the upload node emits and how many
// ranges the widest one carried, so "one group for the batch" is an assertion
// rather than a claim in a comment.
type uploadProbe struct {
	*bufferTestDriver
	barriers, widest int
}

func (d *uploadProbe) CmdPipelineBarrier(cb core1_0.CommandBuffer, src, dst core1_0.PipelineStageFlags, deps core1_0.DependencyFlags, mem []core1_0.MemoryBarrier, buf []core1_0.BufferMemoryBarrier, img []core1_0.ImageMemoryBarrier) error {
	d.barriers++
	d.widest = max(d.widest, len(buf))
	return d.bufferTestDriver.CmdPipelineBarrier(cb, src, dst, deps, mem, buf, img)
}

// TestStreamedMeshIsNotDrawnUntilItLands is the safety net for a game that
// forgets to check its ticket: the draw is dropped rather than recorded
// against a buffer being copied into, and the drop is counted.
//
// Verified to fail: an early `return draws` in dropStreaming gives "0 of 4
// draws dropped, want 1" -- the draw of a buffer being copied into reaches
// the recorder.
func TestStreamedMeshIsNotDrawnUntilItLands(t *testing.T) {
	r, d := bufferFixture()
	v, idx := arenaTriangle()
	a := mustArena(t, r, true)
	fx := buildFrame(0)
	fx.draws, fx.overlays, fx.celestials, fx.uiOverlays, fx.msdfOverlays = nil, nil, nil, nil, nil
	fx.grass, fx.particles = nil, nil
	fx.lighting.ShadowEnabled = false
	fx.lighting.PointRange = 0
	var pending *Mesh
	for n := range 4 {
		var m *Mesh
		var err error
		if n == 1 {
			m, _, err = a.AllocAsync(v, idx)
			pending = m
		} else {
			m, err = a.Alloc(v, idx)
		}
		if err != nil {
			t.Fatal(err)
		}
		fx.draws = append(fx.draws, RenderObject{Mesh: m, Model: identityMat(0), MVP: identityMat(0)})
	}
	all := fx.draws
	skipped := 0
	fx.draws = r.dropStreaming(all, &r.streamDraws, &skipped)
	if skipped != 1 || len(fx.draws) != 3 {
		t.Fatalf("%d of %d draws dropped, want 1", skipped, len(all))
	}
	probe := &rangeDrawDriver{fakeDriver: fakeDriver{}}
	if err := fx.record(probe, 0); err != nil {
		t.Fatal(err)
	}
	if fx.stats.DrawCalls != 3 {
		t.Fatalf("%d draw calls, want the three landed ranges", fx.stats.DrawCalls)
	}
	for _, call := range probe.draws {
		if call[1] == int(pending.firstIndex) && call[2] == pending.vertexOffset {
			t.Fatal("the range still being uploaded was drawn")
		}
	}

	// Once it lands the draw comes back, and with nothing pending the filter
	// hands the caller its own slice again.
	streamFrame(r, d, 0)
	for f := 1; f <= maxFramesInFlight; f++ {
		streamFrame(r, d, f%maxFramesInFlight)
	}
	skipped = 0
	if got := r.dropStreaming(all, &r.streamDraws, &skipped); len(got) != 4 || skipped != 0 {
		t.Fatalf("landed upload still skipped: %d draws, %d skipped", len(got), skipped)
	}
}

// instanceDrawProbe records the instance count of every indexed draw, which
// is how a draw that should not have been recorded is identified without
// depending on Vulkan handle values -- the fixture and the fake device driver
// hand out handles from independent counters and can collide.
type instanceDrawProbe struct {
	fakeDriver
	instances []int
}

func (d *instanceDrawProbe) CmdDrawIndexed(cb core1_0.CommandBuffer, n, instances int, first uint32, base int, instance uint32) {
	d.instances = append(d.instances, instances)
	d.fakeDriver.CmdDrawIndexed(cb, n, instances, first, base, instance)
}

// TestStreamedInstanceSetIsNotDrawnUntilItLands covers the two ways a draw
// names geometry other than its own Mesh: an InstanceSet, and an
// InstanceSetLOD whose levels have not been selected between yet.
//
// Both were admitted by a filter that looked only at RenderObject.Mesh, and an
// instanced draw over a streamed mesh is the same mid-copy read as a plain one.
//
// Verified to fail: reverting uploading to `d.Mesh != nil && d.Mesh.upload !=
// nil` gives "0 of 4 draws dropped, want 2", and with that assertion removed,
// "the instance set over a mesh still uploading was drawn (77 instances)".
// Neither draw here has a pending Mesh of its own, which is the point.
func TestStreamedInstanceSetIsNotDrawnUntilItLands(t *testing.T) {
	r, _ := bufferFixture()
	v, idx := arenaTriangle()
	a := mustArena(t, r, true)
	landed, err := a.Alloc(v, idx)
	if err != nil {
		t.Fatal(err)
	}
	pending, _, err := a.AllocAsync(v, idx)
	if err != nil {
		t.Fatal(err)
	}
	far, _, err := a.AllocAsync(v, idx)
	if err != nil {
		t.Fatal(err)
	}

	h := &fakeHandles{next: 30000}
	fx := buildFrame(0)
	fx.draws, fx.overlays, fx.celestials, fx.uiOverlays, fx.msdfOverlays = nil, nil, nil, nil, nil
	fx.grass, fx.particles = nil, nil
	fx.lighting.ShadowEnabled = false
	fx.lighting.PointRange = 0
	m := identityMat(0)
	all := []RenderObject{
		{Mesh: landed, Model: m, MVP: m},
		{Mesh: landed, Model: m, MVP: m, Instances: fakeInstanceSet(h, landed, 11, 30), Texture: fakeTexture(h)},
		// Its own Mesh has landed; the set it would actually bind has not.
		// That is the shape prepareLOD produces, and the shape a check of
		// RenderObject.Mesh alone cannot see.
		{Mesh: landed, Model: m, MVP: m, Instances: fakeInstanceSet(h, pending, 77, 30), Texture: fakeTexture(h)},
		// Selection has not run, so the near level being ready is not enough.
		{Mesh: landed, Model: m, MVP: m, InstancesLOD: &InstanceSetLOD{levels: []LODLevel{{Mesh: landed, MaxDistance: 10}, {Mesh: far, MaxDistance: 90}}}},
	}
	skipped := 0
	fx.draws = r.dropStreaming(all, &r.streamDraws, &skipped)
	if skipped != 2 || len(fx.draws) != 2 {
		t.Fatalf("%d of %d draws dropped, want 2", skipped, len(all))
	}

	d := &instanceDrawProbe{fakeDriver: fakeDriver{}}
	if err := fx.record(d, 0); err != nil {
		t.Fatal(err)
	}
	for _, n := range d.instances {
		if n == 77 {
			t.Fatal("the instance set over a mesh still uploading was drawn (77 instances)")
		}
	}
	found := false
	for _, n := range d.instances {
		found = found || n == 11
	}
	if !found {
		t.Fatal("the landed instance set was dropped too; the filter is not discriminating")
	}
}

// TestStreamedUploadFrameDoesNotAllocate holds the per-frame path to zero
// allocations with the queue warm: the batch, the retirement slots, the
// barrier scratch and the retirement closures are all retained and reused.
//
// Verified to fail: building the retirement closure at every
// finishUploadBatch instead of once per slot reports "1 allocations per
// streaming frame"; a fresh []core1_0.BufferMemoryBarrier per barrier group
// reports 2.
func TestStreamedUploadFrameDoesNotAllocate(t *testing.T) {
	r, d := bufferFixture()
	v, idx := arenaTriangle()
	a := mustArena(t, r, true)
	if _, _, err := a.AllocAsync(v, idx); err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.CreateIndexedMesh32Async(v, idx); err != nil {
		t.Fatal(err)
	}
	// The enqueued work, replayed every iteration: staging allocation and the
	// host copy belong to the constructor, which a streaming game calls at
	// whatever rate it generates geometry. What has to be free is the frame.
	saved := append([]pendingUpload(nil), r.uploadQueue...)
	frame := 0
	run := func() {
		r.uploadQueue = append(r.uploadQueue[:0], saved...)
		streamFrame(r, d, frame%maxFramesInFlight)
		frame++
	}
	for range 8 {
		run()
	}
	if n := testing.AllocsPerRun(50, run); n != 0 {
		t.Fatalf("%v allocations per streaming frame", n)
	}
}

// TestStreamedUploadFailureUnwinds injects a failure at every Vulkan call the
// asynchronous constructors make, including the staging allocation, and
// requires that nothing is left created. The meta-check at the end breaks the
// balance deliberately for every resource kind, because a balance check that
// has never failed proves nothing.
func TestStreamedUploadFailureUnwinds(t *testing.T) {
	for _, arena := range []bool{false, true} {
		create := func(r *Renderer) error {
			v, i := arenaTriangle()
			if !arena {
				_, _, err := r.CreateIndexedMesh32Async(v, i)
				return err
			}
			a, err := r.CreateMeshArena(MeshArenaDesc{Name: "streamed", Vertices: 12, Indices: 12, Index32: true})
			if err != nil {
				return err
			}
			_, _, err = a.AllocAsync(v, i)
			return err
		}
		// Teardown mirrors a real shutdown: the device is idle, whatever was
		// queued but never submitted is swept, and the tracked meshes go back.
		cleanup := func(r *Renderer) {
			meshes := r.meshes
			r.meshes = nil
			for _, m := range meshes {
				r.DestroyMesh(m)
			}
			r.flushAllDeferred()
			r.destroyPendingUploads()
			r.destroyMeshArenas()
		}
		r, control := bufferFixture()
		if err := create(r); err != nil {
			t.Fatal(err)
		}
		cleanup(r)
		bufferBalance(t, control)
		for _, call := range []string{"CreateBuffer", "AllocateMemory", "BindBufferMemory", "MapMemory"} {
			if control.calls[call] == 0 {
				t.Fatalf("no sites for %s", call)
			}
			for at := 1; at <= control.calls[call]; at++ {
				t.Run(fmt.Sprintf("arena=%v/%s/%d", arena, call, at), func(t *testing.T) {
					r, d := bufferFixture()
					d.failCall, d.failAt = call, at
					if err := create(r); !errors.Is(err, errInjected) {
						t.Fatalf("missing injected error: %v", err)
					}
					if r.uploadPending != 0 {
						t.Fatal("a failed upload still holds a mesh back")
					}
					cleanup(r)
					bufferBalance(t, d)
				})
			}
			t.Logf("arena=%v %s: all %d streamed upload sites unwind", arena, call, control.calls[call])
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
		t.Logf("arena=%v: balance meta-check detects a missing destroy for every streamed resource kind", arena)
	}
}

// TestStreamedUploadCancelledBeforeItRuns covers a game that publishes a patch
// and retires it again in the same frame: the queued copy is dropped, its
// staging goes back, and nothing is left waiting.
//
// Verified to fail: skipping cancelUpload in DestroyMesh gives "a cancelled
// upload is still queued".
func TestStreamedUploadCancelledBeforeItRuns(t *testing.T) {
	r, d := bufferFixture()
	v, idx := arenaTriangle()
	m, ticket, err := r.CreateIndexedMesh32Async(v, idx)
	if err != nil {
		t.Fatal(err)
	}
	r.DestroyMesh(m)
	if r.ResourceCounts().PendingUploads != 0 || r.uploadPending != 0 || len(r.uploadQueue) != 0 {
		t.Fatal("a cancelled upload is still queued")
	}
	streamFrame(r, d, 0)
	if d.copies != 0 {
		t.Fatalf("%d copies recorded for a cancelled upload", d.copies)
	}
	if ticket.Ready() {
		t.Fatal("a cancelled upload reported success")
	}
	r.flushAllDeferred()
	bufferBalance(t, d)
}

// TestStreamedStorageBufferDeclaresItsDestination checks the half of the
// batch the frame graph owns: a streamed storage buffer is declared as the
// transfer node's TransferDst, so the compiler derives its barriers to the
// real consumer instead of the node emitting one of its own.
//
// Verified to fail: replacing the b.streamed latch with a bare graphDirty
// gives "the first streamed upload must ask for the rebuild that declares
// it"; the node then declares no uses and the compiler has nothing to derive
// the compute reader's barrier from.
func TestStreamedStorageBufferDeclaresItsDestination(t *testing.T) {
	r, d := bufferFixture()
	b, err := r.CreateStorageBuffer(StorageBufferDesc{Name: "streamed counts", Size: 64})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = r.UploadStorageBufferAsync(b, make([]byte, 64)); err != nil {
		t.Fatal(err)
	}
	if !b.streamed || !r.graphDirty {
		t.Fatal("the first streamed upload must ask for the rebuild that declares it")
	}
	c := &AppCompute{desc: AppComputeDesc{Name: "reader", Stage: StageBeforeScene, Buffers: []*StorageBuffer{b}}, dispatch: [3]uint32{1, 1, 1}}
	p := &AppPass{r: r, desc: AppPassDesc{Name: c.desc.Name, Stage: c.desc.Stage}, enabled: true, compute: c}
	c.pass = p
	r.appPasses = []*AppPass{p}
	f, err := newFrameGraph(core1_0.Samples4, core1_0.FormatD32SignedFloat, core1_0.FormatB8G8R8A8SRGB, 1, r)
	if err != nil {
		t.Fatal(err)
	}
	if f.declarations[0].Name != "streamed uploads" || len(f.declarations[0].Uses) != 1 {
		t.Fatalf("transfer node declares %+v", f.declarations[0])
	}
	if f.declarations[0].Uses[0].Resource != f.storage[b] || f.declarations[0].Uses[0].Access != framegraph.TransferDst {
		t.Fatalf("transfer node does not declare the streamed buffer: %+v", f.declarations[0].Uses[0])
	}
	derived := 0
	for _, step := range f.plan.Steps {
		for _, bar := range step.Barriers {
			if bar.Buffer && bar.Resource == f.storage[b] && bar.SrcAccess&core1_0.AccessTransferWrite != 0 {
				derived++
			}
		}
	}
	if derived == 0 {
		t.Fatal("the compiler derived no barrier from the streamed write to its reader")
	}
	t.Logf("%d derived barriers from the streamed storage write", derived)

	// The node emits nothing itself for a graph-owned destination.
	probe := &uploadProbe{bufferTestDriver: d}
	streamFrame(r, probe, 0)
	if probe.barriers != 0 {
		t.Fatalf("%d hand-emitted barrier groups for a declared destination", probe.barriers)
	}
	if d.copies == 0 {
		t.Fatal("the storage copy was not recorded")
	}
}

// uploadFrame is a minimal scene whose only work is the streamed upload node,
// so the stream pinned below is the batch and almost nothing else.
func uploadFrame(t *testing.T, queue bool) *frame {
	t.Helper()
	r, _ := bufferFixture()
	fx := buildFrame(0)
	fx.draws, fx.overlays, fx.celestials, fx.uiOverlays, fx.msdfOverlays = nil, nil, nil, nil, nil
	fx.grass, fx.particles = nil, nil
	fx.lighting.ShadowEnabled = false
	fx.lighting.PointRange = 0
	if queue {
		v, idx := arenaTriangle()
		a := mustArena(t, r, true)
		if _, _, err := a.AllocAsync(v, idx); err != nil {
			t.Fatal(err)
		}
		if _, _, err := r.CreateIndexedMesh32Async(v, idx); err != nil {
			t.Fatal(err)
		}
		r.beginUploadBatch()
	}
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
	fx.graph.sizeScratch(&fx.scratch)
	return fx
}

// goldenIdleUploadStreamHash and goldenUploadStreamHash pin the same frame
// with nothing queued and with two uploads queued.
//
// Two pinned values rather than one, for the reason
// TestVolumetricSkyDrawIsRecorded keeps two: the idle hash is the claim that
// the node costs a frame that streams nothing exactly nothing, and a single
// value cannot say that. The difference between the call counts says the
// other half -- a batch that silently stopped recording would match one
// pinned hash and not the count.
//
// Verified to fail: removing the trailing barrier group records 5 calls
// instead of 6 and hashes 0x6bb8e880c9a9c328. Dropping AccessIndexRead from
// that group leaves the count at 6 and moves the hash to 0xbd4a9d4f98151c0f,
// which is the argument-only change no call count can see.
const goldenIdleUploadStreamHash = Hasher(0xb693395a3760ca28)
const goldenUploadStreamHash = Hasher(0x939fd73133119a0f)

func TestStreamedUploadBatchStream(t *testing.T) {
	off := &fakeDriver{hashing: true}
	if err := uploadFrame(t, false).record(off, 0); err != nil {
		t.Fatal(err)
	}
	on := &fakeDriver{hashing: true}
	if err := uploadFrame(t, true).record(on, 0); err != nil {
		t.Fatal(err)
	}
	t.Logf("idle: %d calls hash %#x; two queued uploads: %d calls hash %#x", off.calls, uint64(off.h), on.calls, uint64(on.h))
	if got, want := on.calls-off.calls, 6; got != want {
		t.Errorf("the batch records %d driver calls, want %d: two barrier groups and four copies", got, want)
	}
	if off.h != goldenIdleUploadStreamHash {
		t.Errorf("idle stream hash %#x, want the unchanged %#x: an empty batch is not free", uint64(off.h), uint64(goldenIdleUploadStreamHash))
	}
	if on.h != goldenUploadStreamHash {
		t.Errorf("batch stream hash %#x, want %#x: the copies or their barriers changed", uint64(on.h), uint64(goldenUploadStreamHash))
	}
}
