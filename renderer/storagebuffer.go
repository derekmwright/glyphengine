package renderer

import (
	"fmt"
	"slices"
	"unsafe"

	"github.com/vkngwrapper/core/v3/core1_0"
)

type StorageBufferDesc struct {
	Name    string
	Size    int
	History bool
}

// StorageBuffer owns device-local bytes. All methods require the renderer thread.
// History alternates two independent read/write instances by frame slot.
type StorageBuffer struct {
	r         *Renderer
	desc      StorageBufferDesc
	buffers   []core1_0.Buffer
	memory    []core1_0.DeviceMemory
	destroyed bool
}

func (r *Renderer) CreateStorageBuffer(d StorageBufferDesc) (*StorageBuffer, error) {
	if d.Size <= 0 {
		return nil, fmt.Errorf("storage buffer %q: Size must be positive", d.Name)
	}
	props, err := r.instanceDriver.GetPhysicalDeviceProperties(r.physicalDevice)
	if err != nil {
		return nil, err
	}
	if d.Size > props.Limits.MaxStorageBufferRange {
		return nil, fmt.Errorf("storage buffer %q: Size exceeds MaxStorageBufferRange", d.Name)
	}
	b := &StorageBuffer{r: r, desc: d}
	n := 1
	if d.History {
		n = 2
	}
	for range n {
		buf, mem, err := r.createBuffer(d.Size, core1_0.BufferUsageStorageBuffer|core1_0.BufferUsageVertexBuffer|core1_0.BufferUsageIndirectBuffer|core1_0.BufferUsageTransferDst, core1_0.MemoryPropertyDeviceLocal)
		if err != nil {
			b.release()
			return nil, err
		}
		b.buffers = append(b.buffers, buf)
		b.memory = append(b.memory, mem)
	}
	if err := r.uploadBuffers(b.buffers, make([]byte, d.Size)); err != nil {
		b.release()
		return nil, fmt.Errorf("storage buffer %q: initialize: %w", d.Name, err)
	}
	r.storageBuffers = append(r.storageBuffers, b)
	r.graphDirty = true
	return b, nil
}

// UploadStorageBuffer replaces a whole buffer or its prefix in every history
// instance. The staging submission waits for earlier queue work and completes
// before returning; it is intended for updates, not a per-frame streaming loop.
func (r *Renderer) UploadStorageBuffer(b *StorageBuffer, data []byte) error {
	if b == nil || b.r != r || b.destroyed {
		return fmt.Errorf("storage buffer: upload requires a live buffer of this renderer")
	}
	if len(data) > b.desc.Size {
		return fmt.Errorf("storage buffer %q: upload exceeds Size", b.desc.Name)
	}
	if len(data) == 0 {
		return nil
	}
	return r.uploadBuffers(b.buffers, data)
}

func (r *Renderer) uploadBuffers(buffers []core1_0.Buffer, data []byte) error {
	staging, mem, err := r.createBuffer(len(data), core1_0.BufferUsageTransferSrc, core1_0.MemoryPropertyHostVisible|core1_0.MemoryPropertyHostCoherent)
	if err != nil {
		return err
	}
	defer r.deviceDriver.FreeMemory(mem, nil)
	defer r.deviceDriver.DestroyBuffer(staging, nil)
	ptr, _, err := r.deviceDriver.MapMemory(mem, 0, len(data), 0)
	if err != nil {
		return err
	}
	copy(unsafe.Slice((*byte)(ptr), len(data)), data)
	r.deviceDriver.UnmapMemory(mem)
	cmds, _, err := r.deviceDriver.AllocateCommandBuffers(core1_0.CommandBufferAllocateInfo{CommandPool: r.commandPool, Level: core1_0.CommandBufferLevelPrimary, CommandBufferCount: 1})
	if err != nil {
		return err
	}
	cmd := cmds[0]
	defer r.deviceDriver.FreeCommandBuffers(cmd)
	if _, err = r.deviceDriver.BeginCommandBuffer(cmd, core1_0.CommandBufferBeginInfo{Flags: core1_0.CommandBufferUsageOneTimeSubmit}); err != nil {
		return err
	}
	barriers := make([]core1_0.BufferMemoryBarrier, len(buffers))
	for i, b := range buffers {
		barriers[i] = core1_0.BufferMemoryBarrier{Buffer: b, Size: len(data), SrcQueueFamilyIndex: -1, DstQueueFamilyIndex: -1, SrcAccessMask: core1_0.AccessMemoryRead | core1_0.AccessMemoryWrite, DstAccessMask: core1_0.AccessTransferWrite}
	}
	if err = r.deviceDriver.CmdPipelineBarrier(cmd, core1_0.PipelineStageAllCommands, core1_0.PipelineStageTransfer, 0, nil, barriers, nil); err != nil {
		return err
	}
	for _, b := range buffers {
		if err = r.deviceDriver.CmdCopyBuffer(cmd, staging, b, core1_0.BufferCopy{Size: len(data)}); err != nil {
			return err
		}
	}
	for i := range barriers {
		barriers[i].SrcAccessMask = core1_0.AccessTransferWrite
		barriers[i].DstAccessMask = core1_0.AccessMemoryRead | core1_0.AccessMemoryWrite
	}
	if err = r.deviceDriver.CmdPipelineBarrier(cmd, core1_0.PipelineStageTransfer, core1_0.PipelineStageAllCommands, 0, nil, barriers, nil); err != nil {
		return err
	}
	if _, err = r.deviceDriver.EndCommandBuffer(cmd); err != nil {
		return err
	}
	if _, err = r.deviceDriver.QueueSubmit(r.graphicsQueue, nil, core1_0.SubmitInfo{CommandBuffers: cmds}); err != nil {
		return err
	}
	_, err = r.deviceDriver.QueueWaitIdle(r.graphicsQueue)
	return err
}

func (r *Renderer) DestroyStorageBuffer(b *StorageBuffer) {
	if b == nil || b.r != r || b.destroyed {
		return
	}
	b.destroyed = true
	for _, p := range slices.Clone(r.appPasses) {
		if p.compute != nil && slices.Contains(p.compute.desc.Buffers, b) {
			r.DestroyAppPass(p)
		}
	}
	r.storageBuffers = slices.DeleteFunc(r.storageBuffers, func(x *StorageBuffer) bool { return x == b })
	r.graphDirty = true
	r.DeferDestroy(b.release)
}

func (b *StorageBuffer) release() {
	for _, buf := range b.buffers {
		b.r.deviceDriver.DestroyBuffer(buf, nil)
	}
	for _, mem := range b.memory {
		b.r.deviceDriver.FreeMemory(mem, nil)
	}
	b.buffers, b.memory = nil, nil
}
