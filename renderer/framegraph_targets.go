package renderer

import (
	"github.com/derekmwright/glyphengine/renderer/framegraph"
	"github.com/vkngwrapper/core/v3/core1_0"
	"github.com/vkngwrapper/extensions/v3/khr_depth_stencil_resolve"
	"github.com/vkngwrapper/extensions/v3/khr_dynamic_rendering"
)

// bindGraphTargets runs after allocation and on every swapchain rebuild. The
// plan and pipeline formats survive a resize; only physical bindings change.
func (r *Renderer) bindGraphTargets() error { return r.bindGraphTargetsMode(true) }
func (r *Renderer) bindGraphTargetsMode(primeLayouts bool) error {
	f := r.frameGraph
	f.images[f.hdr] = graphImage{images: r.hdr.images, views: r.hdr.views}
	f.images[f.depth] = graphImage{images: r.depth.images, views: r.depth.views}
	if r.msaa != nil {
		f.images[f.color] = graphImage{images: r.msaa.images, views: r.msaa.views}
	}
	f.images[f.swapchain] = graphImage{images: r.sc.images, views: r.sc.imageViews}
	if r.sceneColor != nil {
		f.images[f.copy] = graphImage{images: []core1_0.Image{r.sceneColor.image}, views: []core1_0.ImageView{r.sceneColor.texture.view}}
	}
	bindBloom := func(t *bloomTarget, ids [bloomLevels]framegraph.ResourceID) {
		for level, id := range ids {
			binding := graphImage{images: make([]core1_0.Image, len(t.images)), views: make([]core1_0.ImageView, len(t.images))}
			for i := range t.images {
				binding.images[i], binding.views[i] = t.images[i][level], t.views[i][level]
			}
			f.images[id] = binding
		}
	}
	bindBloom(r.bloom, f.bloom)
	if r.uiLayer != nil {
		f.images[f.ui] = graphImage{images: r.uiLayer.color.images, views: r.uiLayer.color.views}
		bindBloom(r.uiLayer.bloom, f.uiBloom)
	}
	r.bindAppGraphImages()
	for i, step := range f.plan.Steps {
		if step.RenderPass == nil || (i == f.engine[graphWater] && r.sceneColor == nil) ||
			(i >= f.engine[graphUILayer] && i < f.engine[graphTonemap] && f.nodes[i].app == nil && r.uiLayer == nil) {
			continue
		}
		n := &f.nodes[i]
		size := step.RenderPass.Extent.Size(uint32(r.sc.extent.Width), uint32(r.sc.extent.Height))
		n.extent = core1_0.Extent2D{Width: int(size[0]), Height: int(size[1])}
		count := len(r.sc.imageViews)
		if n.byFrame {
			count = 1
			if n.app.desc.Target.desc.History {
				count = 2
			}
		}
		n.targets = make([]*renderingTarget, count)
		for instance := range count {
			t := f.bindRenderingTarget(i, instance)
			if i == f.engine[graphTonemap] {
				t.transition(r.sc.images[instance], core1_0.ImageAspectColor, 0, core1_0.ImageLayoutUndefined, framegraph.ImageLayoutPresentSrc)
			}
			n.targets[instance] = t
		}
	}
	r.bindSceneTargets()

	// These two targets can be skipped before their first use. Establish only
	// their resting layout: copy and UI clear overwrite all pixels before reads.
	if !primeLayouts {
		return nil
	}
	var prime []core1_0.ImageMemoryBarrier
	for _, id := range []framegraph.ResourceID{f.copy, f.ui} {
		for _, img := range f.images[id].images {
			prime = append(prime, core1_0.ImageMemoryBarrier{OldLayout: core1_0.ImageLayoutUndefined,
				NewLayout: f.plan.Resources[id].Resting, SrcQueueFamilyIndex: -1, DstQueueFamilyIndex: -1,
				Image: img, SubresourceRange: core1_0.ImageSubresourceRange{AspectMask: core1_0.ImageAspectColor, LevelCount: 1, LayerCount: 1},
				DstAccessMask: core1_0.AccessShaderRead})
		}
	}
	if len(prime) > 0 {
		cmd, err := r.beginSingleTimeCommands()
		if err != nil {
			return err
		}
		err = r.deviceDriver.CmdPipelineBarrier(cmd, core1_0.PipelineStageTopOfPipe, core1_0.PipelineStageFragmentShader, 0, nil, nil, prime)
		if err != nil {
			r.deviceDriver.FreeCommandBuffers(cmd)
			return err
		}
		if err = r.endSingleTimeCommands(cmd); err != nil {
			return err
		}
	}
	return nil
}

// Descriptor sets name the target views. Return them before image owners
// destroy those views, then discard the Go-side rendering bindings. Each owner
// also calls releaseSets so constructor failure and normal teardown both work.
func (r *Renderer) releaseGraphBindings() {
	r.hdr.releaseSets(r.deviceDriver)
	r.bloom.releaseSets(r.deviceDriver)
	if r.uiLayer != nil {
		r.uiLayer.color.releaseSets(r.deviceDriver)
		r.uiLayer.bloom.releaseSets(r.deviceDriver)
	}
	if r.sceneColor != nil && r.sceneColor.texture.DescriptorSet.Handle() != 0 {
		freeSets(r.deviceDriver, []core1_0.DescriptorSet{r.sceneColor.texture.DescriptorSet})
		r.sceneColor.texture.DescriptorSet = core1_0.DescriptorSet{}
	}
	for i := range r.frameGraph.nodes {
		r.frameGraph.nodes[i].targets = nil
	}
	clear(r.frameGraph.images)
	r.sceneTargets = nil
}

func (f *frameGraph) bindRenderingTarget(node, instance int) *renderingTarget {
	n := &f.nodes[node]
	t := newRenderingTarget(n.extent)
	desc := f.plan.Steps[node].RenderPass
	attachment := func(index int) khr_dynamic_rendering.RenderingAttachmentInfo {
		a := desc.Attachments[index]
		views := f.images[a.Resource].views
		vi := instance
		if len(views) == 1 {
			vi = 0
		}
		var clear core1_0.ClearValue
		if index < len(n.clears) {
			clear = n.clears[index]
		}
		return attachmentInfo(views[vi], index == desc.Depth, a.LoadOp, a.StoreOp, clear)
	}
	for j, index := range desc.Color {
		a := attachment(index)
		if resolve := desc.Resolve[j]; resolve >= 0 {
			a.ResolveMode = khr_depth_stencil_resolve.ResolveModeAverage
			a.ResolveImageView = attachment(resolve).ImageView
			a.ResolveImageLayout = desc.Attachments[resolve].SubpassLayout
		}
		t.info.ColorAttachments = append(t.info.ColorAttachments, a)
	}
	if desc.Depth >= 0 {
		a := attachment(desc.Depth)
		t.info.DepthAttachment = &a
	}

	return t
}
