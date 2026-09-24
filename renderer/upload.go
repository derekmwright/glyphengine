package renderer

import (
	"fmt"
	"unsafe"

	"github.com/derekmwright/glyphengine/renderer/framegraph"
	"github.com/vkngwrapper/core/v3/core1_0"
)

// UploadTicket says whether a queued device-local upload has actually reached
// the GPU yet.
//
// The synchronous constructors owe nothing like this because they end in a
// QueueWaitIdle: by the time one returns, the copy has happened and the whole
// graphics queue has drained waiting for it. That is the cost issue #95 is
// about -- a game streaming terrain patches from a worker pays one full queue
// idle per buffer, twice per indexed mesh -- so the asynchronous path returns
// before the copy runs and hands back this instead.
type UploadTicket struct {
	// done is written on the renderer thread, in the deferred retirement of
	// the batch that carried the copy, and read on the renderer thread. There
	// is no lock because there is no other thread: see the threading contract
	// in docs/agents/models.md.
	done bool
}

// Ready is true once the frame whose submission carried the copy has retired,
// which is the first moment the destination buffer holds the data. A nil
// ticket is never ready, so a zero value cannot be mistaken for a finished
// upload.
func (t *UploadTicket) Ready() bool { return t != nil && t.done }

// uploadCopy is one region of a staging buffer bound for one device-local
// destination. src is the offset inside the staging buffer this upload owns.
type uploadCopy struct {
	dst                  core1_0.Buffer
	src, dstOffset, size int
}

// pendingUpload is one call to an asynchronous constructor: one staging
// buffer, however many destination ranges that call needed, and the ticket
// they all report through.
type pendingUpload struct {
	staging core1_0.Buffer
	memory  core1_0.DeviceMemory
	copies  []uploadCopy
	ticket  *UploadTicket
	// mesh is the Mesh whose draws must be skipped until this upload lands;
	// nil for a storage buffer, which has no draw to skip.
	mesh *Mesh
	// graph is true when every destination is a frame-graph resource, so the
	// compiler derives the barriers on both sides and recordUploads must not
	// emit its own. Mesh and arena buffers are not graph resources: nothing
	// declares them, so the compiler cannot know a transfer wrote them.
	graph bool
	// shared is true when the destination may already be read by a frame
	// still in flight -- an arena range next to ranges being drawn, or a
	// storage buffer. A freshly created buffer has no reader to wait for.
	shared bool
}

// queueUpload stages data for one or more destination ranges and queues the
// copies for the next frame's command buffer.
//
// The staging copy happens here rather than at record time on purpose: the
// caller's slices are its own, and a game that generated them on a worker
// wants to reuse or drop them the moment the constructor returns, exactly as
// the synchronous path lets it.
func (r *Renderer) queueUpload(uploads []bufferUpload, m *Mesh, graphOwned, shared bool) (*UploadTicket, error) {
	// History instances share one source slice; keep one staging copy of it,
	// the same way uploadBufferRanges does for the synchronous path.
	size := 0
	sources := make([]int, len(uploads))
	for i, u := range uploads {
		dup := false
		for j := 0; j < i; j++ {
			if len(u.data) == len(uploads[j].data) && unsafe.SliceData(u.data) == unsafe.SliceData(uploads[j].data) {
				sources[i], dup = sources[j], true
				break
			}
		}
		if dup {
			continue
		}
		sources[i] = size
		size += len(u.data)
	}
	if size == 0 {
		return nil, fmt.Errorf("streamed upload: no data")
	}
	staging, mem, err := r.createBuffer(size, core1_0.BufferUsageTransferSrc, core1_0.MemoryPropertyHostVisible|core1_0.MemoryPropertyHostCoherent)
	if err != nil {
		return nil, fmt.Errorf("streamed upload staging: %w", err)
	}
	ptr, _, err := r.deviceDriver.MapMemory(mem, 0, size, 0)
	if err != nil {
		r.deviceDriver.DestroyBuffer(staging, nil)
		r.deviceDriver.FreeMemory(mem, nil)
		return nil, fmt.Errorf("streamed upload staging: map: %w", err)
	}
	dst := unsafe.Slice((*byte)(ptr), size)
	p := pendingUpload{staging: staging, memory: mem, ticket: &UploadTicket{}, mesh: m, graph: graphOwned, shared: shared}
	pos := 0
	for i, u := range uploads {
		if sources[i] == pos {
			pos += copy(dst[pos:], u.data)
		}
		p.copies = append(p.copies, uploadCopy{dst: u.buffer, src: sources[i], dstOffset: u.offset, size: len(u.data)})
	}
	r.deviceDriver.UnmapMemory(mem)

	// The first asynchronous upload of a renderer's life rebuilds the graph:
	// the node's GPU timer bracket and any streamed storage buffer's declared
	// TransferDst only exist from then on, so a program that never streams
	// records exactly the frame it always did. The rebuild happens in
	// prepareAppFrame, before this frame records, so even this first copy is
	// still recorded in the frame after its enqueue.
	if !r.streaming {
		r.streaming, r.graphDirty = true, true
	}
	r.uploadQueue = append(r.uploadQueue, p)
	r.stagingBuffers++
	if m != nil {
		m.upload = p.ticket
		r.uploadPending++
	}
	return p.ticket, nil
}

// beginUploadBatch moves every queued copy into the frame being recorded.
//
// It appends rather than replaces: a frame that fails between here and its
// submission never carried its batch, and the copies have to survive into the
// next attempt rather than be dropped with their staging still allocated.
func (r *Renderer) beginUploadBatch() {
	if len(r.uploadQueue) == 0 {
		return
	}
	r.uploadBatch = append(r.uploadBatch, r.uploadQueue...)
	clear(r.uploadQueue)
	r.uploadQueue = r.uploadQueue[:0]
}

// recordUploads records the whole batch into the frame's command buffer: one
// group of copies between at most two barrier groups, rather than the
// per-buffer submit-and-idle the synchronous path uses.
//
// The trailing group is what makes the copies visible to the draws later in
// this same command buffer. The leading group is only for destinations that
// frames still in flight may be reading -- an arena range beside ranges being
// drawn. A pipeline barrier's first synchronization scope covers everything
// submitted earlier on the queue, including earlier submissions, so this is
// what orders the transfer write after those reads without a queue wait.
func (r *Renderer) recordUploads(c *graphFrame) {
	pre, post := 0, 0
	for _, u := range r.uploadBatch {
		if u.graph {
			continue
		}
		post += len(u.copies)
		if u.shared {
			pre += len(u.copies)
		}
	}
	if n := max(pre, post); n > cap(r.uploadBarriers) {
		r.uploadBarriers = make([]core1_0.BufferMemoryBarrier, n)
	}
	barriers := func(onlyShared bool) []core1_0.BufferMemoryBarrier {
		out := r.uploadBarriers[:0]
		for _, u := range r.uploadBatch {
			if u.graph || (onlyShared && !u.shared) {
				continue
			}
			for _, cp := range u.copies {
				out = append(out, core1_0.BufferMemoryBarrier{Buffer: cp.dst, Offset: cp.dstOffset, Size: cp.size,
					SrcQueueFamilyIndex: -1, DstQueueFamilyIndex: -1})
			}
		}
		r.uploadBarriers = out
		return out
	}
	if pre > 0 {
		bs := barriers(true)
		for i := range bs {
			bs[i].SrcAccessMask, bs[i].DstAccessMask = core1_0.AccessMemoryRead, core1_0.AccessTransferWrite
		}
		if err := c.driver.CmdPipelineBarrier(c.cmd, core1_0.PipelineStageAllCommands, core1_0.PipelineStageTransfer, 0, nil, bs, nil); err != nil {
			panic(err)
		}
	}
	for _, u := range r.uploadBatch {
		for _, cp := range u.copies {
			if err := c.scratch.copyBuffer(c.driver, c.cmd, u.staging, cp.dst, core1_0.BufferCopy{SrcOffset: cp.src, DstOffset: cp.dstOffset, Size: cp.size}); err != nil {
				panic(err)
			}
		}
	}
	if post > 0 {
		bs := barriers(false)
		for i := range bs {
			bs[i].SrcAccessMask, bs[i].DstAccessMask = core1_0.AccessTransferWrite, core1_0.AccessVertexAttributeRead|core1_0.AccessIndexRead
		}
		if err := c.driver.CmdPipelineBarrier(c.cmd, core1_0.PipelineStageTransfer, core1_0.PipelineStageVertexInput, 0, nil, bs, nil); err != nil {
			panic(err)
		}
	}
}

// finishUploadBatch hands the batch that this frame's submission carried to
// the slot it was submitted in. DeferDestroy's countdown is exactly the wait
// this needs: the callback runs at the flush that follows the fence wait for
// this same slot, which is the first moment the copy is known to have
// completed and therefore both the first moment the staging can go and the
// first moment the ticket is honestly ready.
func (r *Renderer) finishUploadBatch(f int) {
	if len(r.uploadBatch) == 0 {
		return
	}
	r.uploadRetiring[f] = append(r.uploadRetiring[f][:0], r.uploadBatch...)
	clear(r.uploadBatch)
	r.uploadBatch = r.uploadBatch[:0]
	// One closure per slot, built once and reused, so a streaming frame costs
	// no allocation at all -- see TestStreamedUploadFrameDoesNotAllocate.
	if r.uploadRetirers[f] == nil {
		slot := f
		r.uploadRetirers[f] = func() { r.retireUploads(slot) }
	}
	r.DeferDestroy(r.uploadRetirers[f])
}

// retireUploads frees the staging of a completed batch and marks its tickets.
func (r *Renderer) retireUploads(f int) {
	for i := range r.uploadRetiring[f] {
		u := &r.uploadRetiring[f][i]
		r.deviceDriver.DestroyBuffer(u.staging, nil)
		r.deviceDriver.FreeMemory(u.memory, nil)
		r.stagingBuffers--
		u.ticket.done = true
		if u.mesh != nil && u.mesh.upload == u.ticket {
			u.mesh.upload = nil
			r.uploadPending--
		}
	}
	clear(r.uploadRetiring[f])
	r.uploadRetiring[f] = r.uploadRetiring[f][:0]
}

// cancelUpload drops a destroyed mesh's copies if they have not been recorded
// yet, and detaches the mesh either way.
//
// Only the queue is searched. Once a copy is in the batch or retiring it has
// been recorded into a command buffer that will run, so its destination has
// to outlive it -- which is why DestroyMesh defers a streamed mesh's buffers
// instead of freeing them where a static mesh's are freed immediately.
func (r *Renderer) cancelUpload(m *Mesh) {
	for i := range r.uploadQueue {
		if r.uploadQueue[i].mesh != m {
			continue
		}
		r.deviceDriver.DestroyBuffer(r.uploadQueue[i].staging, nil)
		r.deviceDriver.FreeMemory(r.uploadQueue[i].memory, nil)
		r.stagingBuffers--
		r.uploadQueue = append(r.uploadQueue[:i], r.uploadQueue[i+1:]...)
		break
	}
	if m.upload != nil {
		m.upload = nil
		r.uploadPending--
	}
}

// destroyPendingUploads releases staging for copies that were never submitted.
// Anything already submitted retires through its deferred callback, which
// Destroy has already drained by the time this runs.
func (r *Renderer) destroyPendingUploads() {
	for _, q := range [][]pendingUpload{r.uploadQueue, r.uploadBatch} {
		for _, u := range q {
			r.deviceDriver.DestroyBuffer(u.staging, nil)
			r.deviceDriver.FreeMemory(u.memory, nil)
			r.stagingBuffers--
		}
	}
	r.uploadQueue, r.uploadBatch = nil, nil
}

// uploading reports whether any mesh this draw would make the GPU read is
// still being copied into.
//
// All three, not just RenderObject.Mesh: an instanced draw binds
// InstanceSet.Mesh, and a LOD draw binds whichever level survives selection --
// which has not been decided yet at the point the draw list is filtered. A
// check of the draw's own Mesh alone let an InstanceSet or InstanceSetLOD over
// a streamed mesh reach the recorder, which is exactly the mid-copy read the
// skip exists to prevent.
//
// Any level, rather than the one that would be picked: selection happens after
// this, in prepareLOD, and a set whose far level is still uploading would
// otherwise be admitted and then chosen by a camera that moved.
func uploading(d *RenderObject) bool {
	if d.Mesh != nil && d.Mesh.upload != nil {
		return true
	}
	if d.Instances != nil && d.Instances.Mesh != nil && d.Instances.Mesh.upload != nil {
		return true
	}
	if d.InstancesLOD != nil {
		for i := range d.InstancesLOD.levels {
			if m := d.InstancesLOD.levels[i].Mesh; m != nil && m.upload != nil {
				return true
			}
		}
	}
	return false
}

// dropStreaming removes draws whose geometry has not finished uploading, into
// a retained scratch slice.
//
// A game that forgets to check its ticket gets a missing patch for a frame or
// two rather than a draw reading a buffer mid-copy, which is the difference
// between a visibly late patch and corrupt geometry on somebody else's driver.
// The guard is the whole cost when nothing is streaming: one integer compare
// and the caller's own slice back. Nothing below it runs on a frame with no
// upload in flight, which is why uploading can afford to walk LOD levels.
func (r *Renderer) dropStreaming(draws []RenderObject, scratch *[]RenderObject, skipped *int) []RenderObject {
	if r.uploadPending == 0 || len(draws) == 0 {
		return draws
	}
	out := (*scratch)[:0]
	for i := range draws {
		if uploading(&draws[i]) {
			*skipped++
			continue
		}
		out = append(out, draws[i])
	}
	*scratch = out
	return out
}

// appendUploadGraph declares the engine's transfer node. It is the first node
// in the frame, ahead of GPU LOD selection, shadows and the scene, so every
// copy is in place before anything can read it.
//
// The node declares TransferDst on storage buffers that have actually been
// streamed to, and nothing else: those are frame-graph resources, so the
// compiler derives their barriers to the real consumers (#115). Mesh and arena
// buffers are not declared anywhere in the graph, and cannot be without a
// rebuild per created mesh, so recordUploads emits their group itself.
func (r *Renderer) appendUploadGraph(f *frameGraph, g *framegraph.Graph, add func(framegraph.Node, graphNode)) {
	if r.uploadTimer == nil {
		r.uploadTimer = &AppPass{desc: AppPassDesc{Name: "upload"}}
	}
	// Timed only once something has streamed: two timestamps per frame is not
	// much, but a scene that never uploads should record the command buffer it
	// always recorded.
	r.uploadTimer.desc.Timed = r.streaming
	f.uploadTimer = r.uploadTimer
	var uses []framegraph.Use
	for _, b := range r.storageBuffers {
		if b.streamed {
			uses = append(uses, framegraph.Use{Resource: f.storage[b], Access: framegraph.TransferDst})
		}
	}
	add(framegraph.Node{Name: "streamed uploads", Kind: framegraph.Transfer, Optional: true, Uses: uses},
		graphNode{name: "streamed uploads", app: r.uploadTimer, record: r.recordUploads,
			enabled: func(*graphFrame) bool { return len(r.uploadBatch) > 0 }, begin: -1, end: -1, resolve: -1})
}
