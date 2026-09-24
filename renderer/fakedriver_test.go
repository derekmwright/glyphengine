package renderer

import (
	"github.com/vkngwrapper/core/v3/common"
	"github.com/vkngwrapper/core/v3/core1_0"
	"github.com/vkngwrapper/core/v3/loader"
	"github.com/vkngwrapper/extensions/v3/khr_dynamic_rendering"
)

// fakeHandles hands out small, deterministic, distinct Vulkan handles with no
// device behind them, via core1_0's exported Internal* constructors (the same
// ones vkngwrapper's own impl package uses to wrap a real handle). A driver
// call that receives one of these is receiving a genuinely different value
// from the next draw's, not the same zero Buffer{} for everything -- which is
// what lets the hash test in this file notice a scratch slice clobbered by a
// nested call instead of two draws that happen to look alike.
type fakeHandles struct{ next uintptr }

func (h *fakeHandles) n() uintptr { h.next++; return h.next }

func (h *fakeHandles) buffer() core1_0.Buffer {
	return core1_0.InternalBuffer(0, loader.VkBuffer(h.n()), 0)
}
func (h *fakeHandles) image() core1_0.Image {
	return core1_0.InternalImage(0, loader.VkImage(h.n()), 0)
}
func (h *fakeHandles) descSet() core1_0.DescriptorSet {
	return core1_0.InternalDescriptorSet(0, 0, loader.VkDescriptorSet(h.n()), 0)
}
func (h *fakeHandles) pipeline() core1_0.Pipeline {
	return core1_0.InternalPipeline(0, loader.VkPipeline(h.n()), 0)
}

// Keep draw handles at their pre-migration identities despite removed objects.
func (h *fakeHandles) pipelineSkipping(removed int) core1_0.Pipeline {
	for range removed {
		h.n()
	}
	return h.pipeline()
}
func (h *fakeHandles) layout() core1_0.PipelineLayout {
	return core1_0.InternalPipelineLayout(0, loader.VkPipelineLayout(h.n()), 0)
}
func (h *fakeHandles) renderPass() core1_0.RenderPass {
	return core1_0.InternalRenderPass(0, loader.VkRenderPass(h.n()), 0)
}
func (h *fakeHandles) framebuffer() core1_0.Framebuffer {
	return core1_0.InternalFramebuffer(0, loader.VkFramebuffer(h.n()), 0)
}
func (h *fakeHandles) commandBuffer() core1_0.CommandBuffer {
	return core1_0.InternalCommandBuffer(0, 0, loader.VkCommandBuffer(h.n()), 0)
}

// fakeDriver is core1_0.DeviceDriver with only the methods recordCommandBuffer's
// call tree actually uses implemented; everything else is the embedded nil
// interface, so calling an unimplemented one panics on a nil pointer and the
// stack trace names the method -- which is how two methods got added to this
// file (CmdCopyImage and CmdPipelineBarrier, both only reached once water is
// in the fixture).
//
// hashing gates the expensive half of every method: folding argument VALUES
// into h is what TestRecordCommandBufferStreamIsUnchanged needs, but doing it
// unconditionally would make this driver's own bookkeeping show up as
// recordCommandBuffer's allocations in the AllocsPerRun tests, which measure
// this driver too since it is on the call stack being measured. With hashing
// off, every method is a counter increment and nothing else.
type fakeDriver struct {
	core1_0.DeviceDriver

	drawsOnly bool
	hashing   bool
	calls     int
	h         Hasher
}

const (
	opBegin = iota
	opEnd
	opBeginRP
	opEndRP
	opBindPipeline
	opSetViewport
	opSetScissor
	opBindDescSets
	opBindVB
	opBindIB
	opPushConstants
	opDraw
	opDrawIndexed
	opBarrier
	opCopyImage
	opDispatch
)

func (d *fakeDriver) fold(op int) {
	d.calls++
	if d.hashing {
		d.h = d.h.Int(op)
	}
}

func (d *fakeDriver) BeginCommandBuffer(cb core1_0.CommandBuffer, o core1_0.CommandBufferBeginInfo) (common.VkResult, error) {
	d.fold(opBegin)
	return core1_0.VKSuccess, nil
}

func (d *fakeDriver) EndCommandBuffer(cb core1_0.CommandBuffer) (common.VkResult, error) {
	d.fold(opEnd)
	return core1_0.VKSuccess, nil
}

func (d *fakeDriver) CmdBeginRendering(cb core1_0.CommandBuffer, o khr_dynamic_rendering.RenderingInfo) error {
	if d.drawsOnly {
		return nil
	}

	d.fold(opBeginRP)
	if d.hashing {
		d.h = d.h.Int(int(o.Flags)).Int(o.LayerCount).Int(int(o.ViewMask)).Int(o.RenderArea.Offset.X).Int(o.RenderArea.Offset.Y).Int(o.RenderArea.Extent.Width).Int(o.RenderArea.Extent.Height).Int(len(o.ColorAttachments))
		for _, a := range o.ColorAttachments {
			d.hashAttachment(a)
		}
		if o.DepthAttachment != nil {
			d.h = d.h.Byte('d')
			d.hashAttachment(*o.DepthAttachment)
		}
		if o.StencilAttachment != nil {
			d.h = d.h.Byte('s')
			d.hashAttachment(*o.StencilAttachment)
		}
	}
	return nil
}
func (d *fakeDriver) hashAttachment(a khr_dynamic_rendering.RenderingAttachmentInfo) {
	d.h = d.h.Uint64(uint64(a.ImageView.Handle())).Int(int(a.ImageLayout)).Int(int(a.ResolveMode)).Uint64(uint64(a.ResolveImageView.Handle())).Int(int(a.ResolveImageLayout)).Int(int(a.LoadOp)).Int(int(a.StoreOp))
	switch v := a.ClearValue.(type) {
	case core1_0.ClearValueFloat:
		d.h = d.h.Byte('f').Float32s(v[:])
	case *core1_0.ClearValueFloat:
		d.h = d.h.Byte('f').Float32s(v[:])
	case core1_0.ClearValueDepthStencil:
		d.h = d.h.Byte('d').Float32(v.Depth).Int(int(v.Stencil))
	default:
		d.h = d.h.Byte('?')
	}
}
func (d *fakeDriver) CmdEndRendering(cb core1_0.CommandBuffer) {
	if d.drawsOnly {
		return
	}
	d.fold(opEndRP)
}

func (h *fakeHandles) depthTarget(size, layer int) *renderingTarget {
	id := h.n()
	return depthRenderingTarget(core1_0.InternalImage(0, loader.VkImage(id), 0), core1_0.InternalImageView(0, loader.VkImageView(id), 0), core1_0.FormatD32SignedFloat, layer, core1_0.Extent2D{Width: size, Height: size})
}
func (h *fakeHandles) colorTarget() *renderingTarget {
	id := h.n()
	t := newRenderingTarget(core1_0.Extent2D{Width: 640, Height: 360})
	t.info.ColorAttachments = []khr_dynamic_rendering.RenderingAttachmentInfo{attachmentInfo(core1_0.InternalImageView(0, loader.VkImageView(id), 0), false, core1_0.AttachmentLoadOpDontCare, core1_0.AttachmentStoreOpStore, nil)}
	return t
}

func (d *fakeDriver) CmdBindPipeline(cb core1_0.CommandBuffer, bp core1_0.PipelineBindPoint, p core1_0.Pipeline) {
	d.fold(opBindPipeline)
	if d.hashing {
		d.h = d.h.Int(int(bp)).Uint64(uint64(p.Handle()))
	}
}

func (d *fakeDriver) CmdSetViewport(cb core1_0.CommandBuffer, viewports ...core1_0.Viewport) {
	d.fold(opSetViewport)
	if d.hashing {
		d.h = d.h.Int(len(viewports))
		for _, v := range viewports {
			d.h = d.h.Float32(v.X).Float32(v.Y).Float32(v.Width).Float32(v.Height).Float32(v.MinDepth).Float32(v.MaxDepth)
		}
	}
}

func (d *fakeDriver) CmdSetScissor(cb core1_0.CommandBuffer, scissors ...core1_0.Rect2D) {
	d.fold(opSetScissor)
	if d.hashing {
		d.h = d.h.Int(len(scissors))
		for _, s := range scissors {
			d.h = d.h.Int(s.Offset.X).Int(s.Offset.Y).Int(s.Extent.Width).Int(s.Extent.Height)
		}
	}
}

func (d *fakeDriver) CmdBindDescriptorSets(cb core1_0.CommandBuffer, bp core1_0.PipelineBindPoint, layout core1_0.PipelineLayout, firstSet int, sets []core1_0.DescriptorSet, dynamicOffsets []int) {
	d.fold(opBindDescSets)
	if d.hashing {
		d.h = d.h.Int(int(bp)).Uint64(uint64(layout.Handle())).Int(firstSet).Int(len(sets))
		for _, s := range sets {
			d.h = d.h.Uint64(uint64(s.Handle()))
		}
		for _, o := range dynamicOffsets {
			d.h = d.h.Int(o)
		}
	}
}

func (d *fakeDriver) CmdBindVertexBuffers(cb core1_0.CommandBuffer, firstBinding int, buffers []core1_0.Buffer, offsets []int) {
	d.fold(opBindVB)
	if d.hashing {
		d.h = d.h.Int(firstBinding).Int(len(buffers))
		for _, b := range buffers {
			d.h = d.h.Uint64(uint64(b.Handle()))
		}
		for _, o := range offsets {
			d.h = d.h.Int(o)
		}
	}
}

func (d *fakeDriver) CmdBindIndexBuffer(cb core1_0.CommandBuffer, buffer core1_0.Buffer, offset int, indexType core1_0.IndexType) {
	d.fold(opBindIB)
	if d.hashing {
		d.h = d.h.Uint64(uint64(buffer.Handle())).Int(offset).Int(int(indexType))
	}
}

func (d *fakeDriver) CmdPushConstants(cb core1_0.CommandBuffer, layout core1_0.PipelineLayout, stages core1_0.ShaderStageFlags, offset int, valueBytes []byte) {
	d.fold(opPushConstants)
	if d.hashing {
		// The VALUES pushed, not the slice's address -- this is what catches a
		// scratch array reused by a nested call before the outer one reads it.
		d.h = d.h.Uint64(uint64(layout.Handle())).Int(int(stages)).Int(offset).Int(len(valueBytes)).Bytes(valueBytes)
	}
}

func (d *fakeDriver) CmdDraw(cb core1_0.CommandBuffer, vertexCount, instanceCount int, firstVertex, firstInstance uint32) {
	d.fold(opDraw)
	if d.hashing {
		d.h = d.h.Int(vertexCount).Int(instanceCount).Int(int(firstVertex)).Int(int(firstInstance))
	}
}

func (d *fakeDriver) CmdDrawIndexed(cb core1_0.CommandBuffer, indexCount, instanceCount int, firstIndex uint32, vertexOffset int, firstInstance uint32) {
	d.fold(opDrawIndexed)
	if d.hashing {
		d.h = d.h.Int(indexCount).Int(instanceCount).Int(int(firstIndex)).Int(vertexOffset).Int(int(firstInstance))
	}
}

func (d *fakeDriver) CmdPipelineBarrier(cb core1_0.CommandBuffer, src, dst core1_0.PipelineStageFlags, deps core1_0.DependencyFlags, mem []core1_0.MemoryBarrier, bufMem []core1_0.BufferMemoryBarrier, imgMem []core1_0.ImageMemoryBarrier) error {
	if d.drawsOnly {
		return nil
	}

	d.fold(opBarrier)
	if d.hashing {
		d.h = d.h.Int(int(src)).Int(int(dst)).Int(int(deps)).Int(len(mem)).Int(len(bufMem)).Int(len(imgMem))
		for _, b := range bufMem {
			d.h = d.h.Int(int(b.SrcAccessMask)).Int(int(b.DstAccessMask)).Int(b.SrcQueueFamilyIndex).Int(b.DstQueueFamilyIndex).Uint64(uint64(b.Buffer.Handle())).Int(b.Offset).Int(b.Size)
		}
		for _, b := range imgMem {
			d.h = d.h.Int(int(b.SrcAccessMask)).Int(int(b.DstAccessMask)).Int(int(b.OldLayout)).Int(int(b.NewLayout)).
				Int(b.SrcQueueFamilyIndex).Int(b.DstQueueFamilyIndex).Uint64(uint64(b.Image.Handle())).
				Int(int(b.SubresourceRange.AspectMask)).Int(b.SubresourceRange.BaseMipLevel).Int(b.SubresourceRange.LevelCount).
				Int(b.SubresourceRange.BaseArrayLayer).Int(b.SubresourceRange.LayerCount)
		}
	}
	return nil
}

func (d *fakeDriver) CmdCopyImage(cb core1_0.CommandBuffer, srcImage core1_0.Image, srcLayout core1_0.ImageLayout, dstImage core1_0.Image, dstLayout core1_0.ImageLayout, regions ...core1_0.ImageCopy) error {
	d.fold(opCopyImage)
	if d.hashing {
		d.h = d.h.Uint64(uint64(srcImage.Handle())).Int(int(srcLayout)).Uint64(uint64(dstImage.Handle())).Int(int(dstLayout)).Int(len(regions))
		for _, r := range regions {
			d.h = d.h.Int(int(r.SrcSubresource.AspectMask)).Int(r.SrcSubresource.MipLevel).Int(r.SrcSubresource.BaseArrayLayer).Int(r.SrcSubresource.LayerCount).
				Int(r.SrcOffset.X).Int(r.SrcOffset.Y).Int(r.SrcOffset.Z).
				Int(int(r.DstSubresource.AspectMask)).Int(r.DstSubresource.MipLevel).Int(r.DstSubresource.BaseArrayLayer).Int(r.DstSubresource.LayerCount).
				Int(r.DstOffset.X).Int(r.DstOffset.Y).Int(r.DstOffset.Z).
				Int(r.Extent.Width).Int(r.Extent.Height).Int(r.Extent.Depth)
		}
	}
	return nil
}

func (d *fakeDriver) CmdDispatch(cb core1_0.CommandBuffer, x, y, z int) {
	d.fold(opDispatch)
	if d.hashing {
		d.h = d.h.Int(x).Int(y).Int(z)
	}
}
