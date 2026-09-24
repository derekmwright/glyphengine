package renderer

import (
	"fmt"

	"github.com/derekmwright/glyphengine/shaders"
	"github.com/vkngwrapper/core/v3/core1_0"
)

// SceneColor returns a borrowed, stable texture for the resolved HDR scene.
// DrawFrame selects the acquired swapchain image before updating pass inputs.
// It may be read by own-target application passes after the scene has rendered.
func (r *Renderer) SceneColor() *Texture {
	if r.hdrReadTexture.scene == nil {
		r.hdrReadTexture = Texture{scene: r, id: newResourceID()}
	}
	return &r.hdrReadTexture
}

func (r *Renderer) prepareSceneColor(image int) {
	if r.hdrReadTexture.scene != nil {
		r.hdrReadTexture.image = r.hdr.images[image]
		r.hdrReadTexture.view = r.hdr.views[image]
		r.hdrReadTexture.sampler = r.hdr.sampler
		r.hdrReadTexture.DescriptorSet = r.hdr.sceneSets[image]
	}
}

// SceneDepth enables a single-sample R32F copy of scene depth for this renderer's
// lifetime. Reverse-Z samples are unchanged; MSAA takes the maximum (nearest).
// The stable pointer is populated when the next DrawFrame rebuilds the graph.
func (r *Renderer) SceneDepth() *Texture {
	if r.depthResolve == nil {
		r.depthResolve = &sceneDepthResources{r: r}
		r.graphDirty = true
	}
	if r.depthResolve.texture.scene == nil {
		r.depthResolve.texture = Texture{scene: r, sceneDepth: true, id: newResourceID()}
	}
	return &r.depthResolve.texture
}

type sceneDepthResources struct {
	r        *Renderer
	color    *appImages
	texture  Texture
	sets     []core1_0.DescriptorSet
	pipeline core1_0.Pipeline
	layout   core1_0.PipelineLayout
}

func (r *Renderer) ensureSceneDepth() error {
	s := r.depthResolve
	if s == nil {
		return nil
	}
	if err := r.ensureAppLayout(); err != nil {
		return err
	}
	if s.color == nil {
		var err error
		s.color, err = r.newAppImages(core1_0.FormatR32SignedFloat, core1_0.ImageAspectColor, r.sc.extent, len(r.sc.imageViews), appImageOptions{sampled: true})
		if err != nil {
			return fmt.Errorf("scene depth target: %w", err)
		}
		s.sets, err = r.allocatePassSets(nil, len(r.sc.imageViews))
		if err != nil {
			s.releaseTargets()
			return err
		}
		for i, set := range s.sets {
			tex := Texture{view: r.depth.views[i], sampler: s.color.sampler}
			if err = r.writeAppSampler(set, 0, &tex); err != nil {
				s.releaseTargets()
				return err
			}
		}
	}
	if s.pipeline.Handle() == 0 {
		f := r.frameGraph
		if f.depthNode < 0 {
			var err error
			f, err = r.buildAppGraph()
			if err != nil {
				return err
			}
		}
		pass, err := f.renderPass(r.deviceDriver, f.depthNode)
		if err != nil {
			s.releaseTargets()
			return err
		}
		frag := shaders.DepthResolveFragSpv
		if r.msaaSamples != core1_0.Samples1 {
			frag = shaders.DepthResolveMSFragSpv
		}
		s.pipeline, s.layout, err = createAppPipeline(r.deviceDriver, AppPassDesc{Vert: shaders.DepthResolveVertSpv, Frag: frag, Fullscreen: true}, pass, r.appSetLayout, r.shadow.descriptorSetLayout, r.appSetLayout, core1_0.Samples1)
		if err != nil {
			s.releaseTargets()
			return fmt.Errorf("scene depth pipeline: %w", err)
		}
	}
	return nil
}

func (r *Renderer) prepareSceneDepth(image int) error {
	if err := r.ensureSceneDepth(); err != nil {
		return err
	}
	id := r.depthResolve.texture.id
	r.depthResolve.texture = r.depthResolve.color.textures[image]
	r.depthResolve.texture.scene, r.depthResolve.texture.sceneDepth, r.depthResolve.texture.id = r, true, id
	return nil
}

func (s *sceneDepthResources) record(c *graphFrame) {
	d := c.driver
	sc := c.scratch
	d.CmdBindPipeline(c.cmd, core1_0.PipelineBindPointGraphics, s.pipeline)
	sc.setViewport(d, c.cmd, core1_0.Viewport{Width: float32(c.extent.Width), Height: float32(c.extent.Height), MinDepth: 0, MaxDepth: 1})
	sc.setScissor(d, c.cmd, core1_0.Rect2D{Extent: c.extent})
	sc.bindDescriptorSets(d, c.cmd, core1_0.PipelineBindPointGraphics, s.layout, 0, s.sets[c.imageIndex])
	d.CmdDraw(c.cmd, 3, 1, 0, 0)
}

func (s *sceneDepthResources) releaseTargets() {
	freeSets(s.r.deviceDriver, s.sets)
	s.sets = nil
	s.color.destroy(s.r.deviceDriver)
	s.color = nil
}
func (s *sceneDepthResources) destroy() {
	s.releaseTargets()
	if s.pipeline.Handle() != 0 {
		s.r.deviceDriver.DestroyPipeline(s.pipeline, nil)
	}
	if s.layout.Handle() != 0 {
		s.r.deviceDriver.DestroyPipelineLayout(s.layout, nil)
	}
}
