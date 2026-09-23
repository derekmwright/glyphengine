package renderer

import (
	"fmt"

	"github.com/derekmwright/glyphengine/renderer/framegraph"
	"github.com/vkngwrapper/core/v3/core1_0"
)

// bindGraphTargets runs after allocation and on every swapchain rebuild. The
// plan and cached render passes survive a resize; only physical bindings change.
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
		var err error
		n.pass, err = f.renderPass(r.deviceDriver, i)
		if err != nil {
			f.destroyFramebuffers(r.deviceDriver)
			return err
		}
		size := step.RenderPass.Extent.Size(uint32(r.sc.extent.Width), uint32(r.sc.extent.Height))
		n.extent = core1_0.Extent2D{Width: int(size[0]), Height: int(size[1])}
		count := len(r.sc.imageViews)
		if n.byFrame {
			count = 1
			if n.app.desc.Target.desc.History {
				count = 2
			}
		}
		for instance := range count {
			views := make([]core1_0.ImageView, len(step.RenderPass.Attachments))
			for j, a := range step.RenderPass.Attachments {
				binding := f.images[a.Resource].views
				index := instance
				if len(binding) == 1 {
					index = 0
				}
				views[j] = binding[index]
			}
			fb, _, err := r.deviceDriver.CreateFramebuffer(nil, core1_0.FramebufferCreateInfo{
				RenderPass: n.pass, Attachments: views, Width: n.extent.Width, Height: n.extent.Height, Layers: 1})
			if err != nil {
				f.destroyFramebuffers(r.deviceDriver)
				return fmt.Errorf("frame graph framebuffer %s/%d: %w", n.name, instance, err)
			}
			n.framebuffers = append(n.framebuffers, fb)
		}
	}
	r.tonemapFramebuffers = f.nodes[f.engine[graphTonemap]].framebuffers
	r.waterFramebuffers = f.nodes[f.engine[graphWater]].framebuffers
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
			f.destroyFramebuffers(r.deviceDriver)
			return err
		}
		err = r.deviceDriver.CmdPipelineBarrier(cmd, core1_0.PipelineStageTopOfPipe, core1_0.PipelineStageFragmentShader, 0, nil, nil, prime)
		if err != nil {
			r.deviceDriver.FreeCommandBuffers(cmd)
			f.destroyFramebuffers(r.deviceDriver)
			return err
		}
		if err = r.endSingleTimeCommands(cmd); err != nil {
			f.destroyFramebuffers(r.deviceDriver)
			return err
		}
	}
	return nil
}

// Descriptor sets name the target views. Return them before framebuffers, and
// framebuffers before image owners destroy those views. Each owner's destroy
// also calls releaseSets so both constructor failure and normal teardown work.
func (r *Renderer) releaseGraphFramebuffers() {
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
		n := &r.frameGraph.nodes[i]
		for _, fb := range n.framebuffers {
			r.deviceDriver.DestroyFramebuffer(fb, nil)
		}
		n.framebuffers = nil
	}
	clear(r.frameGraph.images)
	r.tonemapFramebuffers, r.waterFramebuffers = nil, nil
}
