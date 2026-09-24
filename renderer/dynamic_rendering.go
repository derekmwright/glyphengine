package renderer

import (
	"github.com/derekmwright/glyphengine/renderer/framegraph"
	"github.com/vkngwrapper/core/v3/common"
	"github.com/vkngwrapper/core/v3/core1_0"
	"github.com/vkngwrapper/extensions/v3/khr_depth_stencil_resolve"
	"github.com/vkngwrapper/extensions/v3/khr_dynamic_rendering"
)

// Pipeline attachment formats are data, with no device lifetime or compatibility cache.
type renderingFormats = khr_dynamic_rendering.PipelineRenderingCreateInfo

func colorDepthFormats(color, depth core1_0.Format) renderingFormats {
	f := renderingFormats{DepthAttachmentFormat: depth}
	if color != 0 {
		f.ColorAttachmentFormats = []core1_0.Format{color}
	}
	return f
}
func renderingOptions(f renderingFormats) common.NextOptions { return common.NextOptions{Next: f} }
func (f *frameGraph) pipelineFormats(node int) renderingFormats {
	d := f.plan.Steps[node].RenderPass
	out := renderingFormats{}
	for _, i := range d.Color {
		out.ColorAttachmentFormats = append(out.ColorAttachmentFormats, d.Attachments[i].Format)
	}
	if d.Depth >= 0 {
		out.DepthAttachmentFormat = d.Attachments[d.Depth].Format
	}
	return out
}

// renderingTarget holds only Go-side bindings. Its barriers replace the implicit
// transitions of hand-recorded passes; graph nodes use the compiler's barriers.
type renderingTarget struct {
	info                       khr_dynamic_rendering.RenderingInfo
	before, after              []core1_0.ImageMemoryBarrier
	src, dst, exitSrc, exitDst core1_0.PipelineStageFlags
}

func newRenderingTarget(extent core1_0.Extent2D) *renderingTarget {
	return &renderingTarget{info: khr_dynamic_rendering.RenderingInfo{RenderArea: core1_0.Rect2D{Extent: extent}, LayerCount: 1}}
}
func attachmentInfo(view core1_0.ImageView, depth bool, load core1_0.AttachmentLoadOp, store core1_0.AttachmentStoreOp, clear core1_0.ClearValue) khr_dynamic_rendering.RenderingAttachmentInfo {
	layout := core1_0.ImageLayoutColorAttachmentOptimal
	if depth {
		layout = core1_0.ImageLayoutDepthStencilAttachmentOptimal
	}
	return khr_dynamic_rendering.RenderingAttachmentInfo{ImageView: view, ImageLayout: layout, LoadOp: load, StoreOp: store, ClearValue: clear}
}
func layoutScope(layout core1_0.ImageLayout) (core1_0.PipelineStageFlags, core1_0.AccessFlags) {
	switch layout {
	case core1_0.ImageLayoutColorAttachmentOptimal:
		return core1_0.PipelineStageColorAttachmentOutput, core1_0.AccessColorAttachmentRead | core1_0.AccessColorAttachmentWrite
	case core1_0.ImageLayoutDepthStencilAttachmentOptimal:
		return core1_0.PipelineStageEarlyFragmentTests | core1_0.PipelineStageLateFragmentTests, core1_0.AccessDepthStencilAttachmentRead | core1_0.AccessDepthStencilAttachmentWrite
	case framegraph.ImageLayoutPresentSrc:
		return core1_0.PipelineStageBottomOfPipe, 0
	default:
		return core1_0.PipelineStageFragmentShader | core1_0.PipelineStageComputeShader, core1_0.AccessShaderRead
	}
}
func (t *renderingTarget) transition(img core1_0.Image, aspect core1_0.ImageAspectFlags, layer int, initial, final core1_0.ImageLayout) {
	layout := core1_0.ImageLayoutColorAttachmentOptimal
	if aspect&core1_0.ImageAspectDepth != 0 {
		layout = core1_0.ImageLayoutDepthStencilAttachmentOptimal
	}
	stage, access := layoutScope(layout)
	src, srcAccess := layoutScope(initial)
	if initial == core1_0.ImageLayoutUndefined {
		// Discard contents, but still order reuse after previous readers and writers.
		src = core1_0.PipelineStageAllCommands
		srcAccess = core1_0.AccessMemoryRead | core1_0.AccessMemoryWrite
	}
	b := core1_0.ImageMemoryBarrier{Image: img, OldLayout: initial, NewLayout: layout,
		SrcQueueFamilyIndex: -1, DstQueueFamilyIndex: -1,
		SubresourceRange: core1_0.ImageSubresourceRange{AspectMask: aspect, BaseArrayLayer: layer, LayerCount: 1, LevelCount: 1},
		SrcAccessMask:    srcAccess, DstAccessMask: access}
	t.before = append(t.before, b)
	t.src |= src
	t.dst |= stage
	if layout != final {
		dst, dstAccess := layoutScope(final)
		b.OldLayout, b.NewLayout, b.SrcAccessMask, b.DstAccessMask = layout, final, access, dstAccess
		t.after = append(t.after, b)
		t.exitSrc |= stage
		t.exitDst |= dst
	}
}
func (t *renderingTarget) begin(d core1_0.DeviceDriver, dynamic khr_dynamic_rendering.ExtensionDriver, cmd core1_0.CommandBuffer) error {
	if len(t.before) > 0 {
		if err := d.CmdPipelineBarrier(cmd, t.src, t.dst, 0, nil, nil, t.before); err != nil {
			return err
		}
	}
	return dynamic.CmdBeginRendering(cmd, t.info)
}
func (t *renderingTarget) finish(d core1_0.DeviceDriver, cmd core1_0.CommandBuffer) error {
	if len(t.after) > 0 {
		return d.CmdPipelineBarrier(cmd, t.exitSrc, t.exitDst, 0, nil, nil, t.after)
	}
	return nil
}
func (t *renderingTarget) end(d core1_0.DeviceDriver, dynamic khr_dynamic_rendering.ExtensionDriver, cmd core1_0.CommandBuffer) error {
	dynamic.CmdEndRendering(cmd)
	return t.finish(d, cmd)
}
func depthRenderingTarget(img core1_0.Image, view core1_0.ImageView, format core1_0.Format, layer int, extent core1_0.Extent2D) *renderingTarget {
	t := newRenderingTarget(extent)
	a := attachmentInfo(view, true, core1_0.AttachmentLoadOpClear, core1_0.AttachmentStoreOpStore, core1_0.ClearValueDepthStencil{Depth: 1})
	t.info.DepthAttachment = &a
	t.transition(img, depthAspect(format), layer, core1_0.ImageLayoutUndefined, core1_0.ImageLayoutDepthStencilReadOnlyOptimal)
	return t
}
func (r *Renderer) bindSceneTargets() {
	r.sceneTargets = make([]*renderingTarget, len(r.hdr.views))
	for i := range r.sceneTargets {
		t := newRenderingTarget(r.sc.extent)
		color, view := r.hdr.images[i], r.hdr.views[i]
		final := core1_0.ImageLayoutShaderReadOnlyOptimal
		if r.msaa != nil {
			color, view, final = r.msaa.images[i], r.msaa.views[i], core1_0.ImageLayoutColorAttachmentOptimal
		}
		a := attachmentInfo(view, false, core1_0.AttachmentLoadOpClear, core1_0.AttachmentStoreOpStore, &r.cmdScratch.colorClear)
		t.transition(color, core1_0.ImageAspectColor, 0, core1_0.ImageLayoutUndefined, final)
		if r.msaa != nil {
			a.ResolveMode = khr_depth_stencil_resolve.ResolveModeAverage
			a.ResolveImageView, a.ResolveImageLayout = r.hdr.views[i], core1_0.ImageLayoutColorAttachmentOptimal
			t.transition(r.hdr.images[i], core1_0.ImageAspectColor, 0, core1_0.ImageLayoutUndefined, core1_0.ImageLayoutShaderReadOnlyOptimal)
		}
		t.info.ColorAttachments = []khr_dynamic_rendering.RenderingAttachmentInfo{a}
		dep := attachmentInfo(r.depth.views[i], true, core1_0.AttachmentLoadOpClear, core1_0.AttachmentStoreOpStore, core1_0.ClearValueDepthStencil{Depth: 0})
		t.info.DepthAttachment = &dep
		t.transition(r.depth.images[i], depthAspect(r.depth.format), 0, core1_0.ImageLayoutUndefined, core1_0.ImageLayoutDepthStencilAttachmentOptimal)
		r.sceneTargets[i] = t
	}
}

// Separate depth/stencil layouts are not enabled. A depth-only view may still
// belong to a combined format, whose transitions must include both aspects.
func depthAspect(format core1_0.Format) core1_0.ImageAspectFlags {
	aspect := core1_0.ImageAspectDepth
	if format == core1_0.FormatD32SignedFloatS8UnsignedInt || format == core1_0.FormatD24UnsignedNormalizedS8UnsignedInt {
		aspect |= core1_0.ImageAspectStencil
	}
	return aspect
}
