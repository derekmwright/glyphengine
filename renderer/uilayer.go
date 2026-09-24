package renderer

import (
	"github.com/vkngwrapper/core/v3/core1_0"
)

// The screen-space UI's own HDR layer: a half-float target the UI draws into, a
// bloom chain over that target alone, and a fixed-exposure composite onto the
// swapchain in place of the direct draw.
//
// Why a second layer rather than putting the UI back in the scene's:
// a HUD in the scene target is eaten by water refraction, scene exposure and
// bloom, which is the bug that moved it out (see recordUIComposite). Tonemapping
// the UI with the scene reintroduces that in a nicer suit -- with any exposure
// that tracks the time of day, a white label is a different white at midday than
// at dusk, and a UI colour is supposed to be a display value, not a scene value.
// So the UI gets an HDR target of its own with an exposure that does not move,
// and the only thing that changes about a colour at or below 1 is nothing.
//
// What this buys that a game cannot reach from outside the engine: glow that
// crosses BETWEEN elements. A game can already put a holographic panel in the
// world (Emissive plus Translucent above the bloom threshold), and WithShaders
// can replace UIFrag outright for an SDF halo on one element. Neither can make a
// button bleed light onto the panel behind it, because that needs a pass.
//
// Off by default and free when off: no images, no descriptor
// sets and no recorded commands. See WithUIGlowLayer.

// uiLayerTarget is everything the UI layer owns that is sized by the swapchain.
//
// color is an hdrTarget, the same type and the same creation path the scene's
// uses -- one half-float image per swapchain image, a sampler, and the one-
// sampler descriptor sets its bloom prefilter reads it through. Its tonemapSets
// are this layer's resolve sets: the same shape (colour at binding 0, the bloom
// chain's finest level at binding 1) written by the same writeTonemapSets, for
// the same reason.
//
// The graph keeps Go-side bindings for this single-sample, depth-free layer.
type uiLayerTarget struct {
	color *hdrTarget
	bloom *bloomTarget
}

func (t *uiLayerTarget) destroy(deviceDriver core1_0.DeviceDriver) {
	if t == nil {
		return
	}
	t.bloom.destroy(deviceDriver)
	t.color.destroy(deviceDriver)
	t.bloom, t.color = nil, nil
}

// createUILayerTargets allocates the layer, its bloom chain and its resolve
// sets at the current swapchain extent. The graph binds their views afterwards.
//
// Called from New when the option is on and again from recreateSwapchain, which
// is why it is one function rather than a sequence inlined in both: a resize
// that rebuilt the images and forgot the resolve sets would leave the composite
// sampling freed views, which the validation layer catches and a release build
// does not. writeTonemapSets is what pairs the two, and it runs here.
func (r *Renderer) createUILayerTargets() (*uiLayerTarget, error) {
	count := len(r.sc.imageViews)
	t := &uiLayerTarget{}

	var err error
	t.color, err = createHDRTargets(r.instanceDriver, r.deviceDriver, r.physicalDevice,
		r.descriptorPool, r.descriptorSetLayout, r.sc.extent, count, r.maxAnisotropy, "UI glow layer")
	if err != nil {
		return nil, err
	}

	t.bloom, err = createBloomTargets(r.instanceDriver, r.deviceDriver, r.physicalDevice,
		r.descriptorPool, r.descriptorSetLayout,
		r.sc.extent, count)
	if err != nil {
		t.destroy(r.deviceDriver)
		return nil, err
	}

	// The bloom chain needs a defined layout before anything binds it, for the
	// same reason the scene's does: the resolve's descriptor set names it at
	// binding 1 every frame the composite runs, and with the glow strength at
	// zero nothing ever writes it. Vulkan validates a descriptor's declared
	// layout against the image's actual layout at submit whether or not the
	// shader samples it, so a chain left in UNDEFINED reports on every frame.
	//
	// The graph primes the layer's resting layout after binding all targets.
	// It does not clear: the optional layer node clears before any consumer.

	if err := r.primeBloomLayouts(t.bloom); err != nil {
		t.destroy(r.deviceDriver)
		return nil, err
	}

	if err := writeTonemapSets(r.deviceDriver, r.descriptorPool, r.tonemapSetLayout, t.color, t.bloom); err != nil {
		t.destroy(r.deviceDriver)
		return nil, err
	}
	return t, nil
}

// uiLayerPass is everything the layer draw closure and recordUIResolve need for one
// frame. Nil on the tonemapPass means the layer was never created and the UI
// draws straight onto the swapchain, exactly as it always has.
type uiLayerPass struct {
	uiPipeline   core1_0.Pipeline
	msdfPipeline core1_0.Pipeline
	layout       core1_0.PipelineLayout

	// bloom is the chain over this layer alone. Its enabled flag is the glow
	// strength being positive, so a layer with the glow turned down records the
	// UI draws and the composite and nothing else.
	bloom bloomPass

	resolve       core1_0.Pipeline
	resolveSet    core1_0.DescriptorSet
	resolveLayout core1_0.PipelineLayout
	exposure      float32
	strength      float32
}

// uiLayerFor bundles the UI layer state for one swapchain image, or nil when the
// layer was not created.
func (r *Renderer) uiLayerFor(imageIndex int) *uiLayerPass {
	if r.uiLayer == nil {
		return nil
	}
	return &uiLayerPass{
		uiPipeline:   r.uiLayerUIPipeline,
		msdfPipeline: r.uiLayerMSDFPipeline,
		layout:       r.pipelineLayout,
		bloom: bloomPass{
			enabled:     r.uiGlowStrength > 0,
			prefilter:   r.bloomPrefilterPipeline,
			down:        r.bloomDownPipeline,
			up:          r.bloomUpPipeline,
			layout:      r.pipelineLayout,
			sceneSet:    r.uiLayer.color.sceneSets[imageIndex],
			sets:        r.uiLayer.bloom.sets[imageIndex],
			extents:     r.uiLayer.bloom.extents,
			sceneExtent: r.sc.extent,
			threshold:   r.uiGlowThreshold,
			knee:        r.uiGlowKnee,
			radius:      r.uiGlowRadius,
		},
		resolve:       r.uiResolvePipeline,
		resolveSet:    r.uiLayer.color.tonemapSets[imageIndex],
		resolveLayout: r.tonemapPipelineLayout,
		exposure:      r.uiExposure,
		strength:      r.uiGlowStrength,
	}
}

// recordUIResolve composites the finished layer onto the swapchain, inside the
// tonemap pass and after its resolve triangle -- exactly where the direct UI
// draws used to go.
//
// It REPLACES those draws rather than stacking on them: one fullscreen triangle
// of the finished layer instead of N quads. The cost of the feature is the layer
// pass and its bloom upstream of here, not this.
func recordUIResolve(
	deviceDriver core1_0.DeviceDriver,
	cmdBuf core1_0.CommandBuffer,
	p *uiLayerPass,
	extent core1_0.Extent2D,
	scratch *commandScratch,
) {
	deviceDriver.CmdBindPipeline(cmdBuf, core1_0.PipelineBindPointGraphics, p.resolve)
	// Set explicitly rather than inherited from the resolve triangle that ran a
	// command earlier, for the reason recordUIComposite gives: a pass that only
	// works because something before it happened to set the viewport is one edit
	// away from not working.
	scratch.setViewport(deviceDriver, cmdBuf, core1_0.Viewport{
		Width: float32(extent.Width), Height: float32(extent.Height), MinDepth: 0, MaxDepth: 1,
	})
	scratch.setScissor(deviceDriver, cmdBuf, core1_0.Rect2D{Offset: core1_0.Offset2D{X: 0, Y: 0}, Extent: extent})
	scratch.bindDescriptorSets(deviceDriver, cmdBuf, core1_0.PipelineBindPointGraphics, p.resolveLayout, 0, p.resolveSet)

	scratch.resetPC()
	scratch.pc[32] = p.exposure
	scratch.pc[35] = p.strength
	scratch.pushConstants(deviceDriver, cmdBuf, p.resolveLayout, core1_0.StageVertex|core1_0.StageFragment)

	deviceDriver.CmdDraw(cmdBuf, 3, 1, 0, 0)
}

// SetUIGlow configures the glare added around screen-space UI that asks for it.
//
// It is SetBloom for the UI layer and takes the same four numbers, because it is
// the same chain instantiated a second time over a different image. strength
// scales what the glow contributes and zero or less switches the chain off and
// skips recording it; threshold is the linear value above which a UI pixel
// starts to glow; knee softens the ramp either side of it; radius widens the
// upsample tent in source texels.
//
// It does nothing at all unless the layer exists -- see WithUIGlowLayer. That is
// on purpose: the targets are allocated at construction and on resize, so
// whether the layer is there is a decision made once, and how it looks is a
// decision a game can change per frame.
//
// Keep threshold - knee at or above 1, and for a stronger reason than the scene
// bloom has. Below 1 is where ordinary UI lives: a UI colour is an sRGB value at
// or below 1, so after srgbToLinear and the premultiply nothing an ordinary
// element writes can exceed 1.0, and a ramp reaching under that would make every
// white label glow. The default 1.2 / 0.2 puts the foot of the ramp exactly at
// 1.0, and that was measured rather than reasoned: 13-ui with the layer on and
// nothing asking to glow differs from the same geometry on the direct path on
// 5009 pixels of 921600, every one of them a blended UI pixel, NONE of them by
// more than 1/255 -- which is the 8-bit rounding step that blending in a float
// layer and encoding once costs, not the glow leaking in. `task uiglow` holds
// that number.
//
// A strength of zero or less switches the chain off and skips recording it. That
// is worth knowing because the chain runs whether or not anything in the frame
// actually clears the threshold -- the recorder cannot tell, since text carries
// its emission per vertex -- so it is a fixed cost a game that wants no glow
// should not pay. Measured on a Radeon RX 7900 XTX at 1280x720, 200-frame means:
// the layer alone is 0.017 ms and its chain another 0.063 ms.
//
// Defaults: SetUIGlow(0.7, 1.2, 0.2, 1.0), the scene bloom's own starting point.
func (r *Renderer) SetUIGlow(strength, threshold, knee, radius float32) {
	if knee < 0 {
		knee = 0
	}
	if radius <= 0 {
		radius = 1
	}
	r.uiGlowStrength, r.uiGlowThreshold, r.uiGlowKnee, r.uiGlowRadius = strength, threshold, knee, radius
}

// UIGlow returns the current strength, threshold, knee and radius, as last set
// by SetUIGlow. Exposed for the same reason Bloom is: so a harness can switch the
// glow off and put the same settings back rather than guessing them.
func (r *Renderer) UIGlow() (strength, threshold, knee, radius float32) {
	return r.uiGlowStrength, r.uiGlowThreshold, r.uiGlowKnee, r.uiGlowRadius
}

// SetUIExposure scales the whole UI layer at composite time.
//
// It is separate from SetTonemap and it is fixed: nothing in the engine moves
// it, and in particular the day/night cycle does not. That separation is the
// entire reason the layer exists. A HUD whose brightness wandered between midday
// and dusk is the bug the screen-space channels were moved past the tonemap to
// fix, and putting them through an exposure that tracks the sun would bring it
// straight back.
//
// The default is 1, which makes the composite exact: a UI colour written as 0.05
// still reaches the display as 0.05. Anything else is a look a game chose, and it
// scales the glow with the rest of the layer.
func (r *Renderer) SetUIExposure(exposure float32) { r.uiExposure = exposure }

// UIExposure returns the UI layer's fixed exposure, as last set by
// SetUIExposure.
func (r *Renderer) UIExposure() float32 { return r.uiExposure }

// UIGlowLayer reports whether the screen-space UI has its own HDR layer, i.e.
// whether WithUIGlowLayer was passed to New.
//
// Exposed because every other knob on this feature is silent without it:
// SetUIGlow on a renderer built without the layer is not an error and not a
// warning, it simply has nowhere to apply, and a game or a gate needs to be able
// to ask rather than infer it from a picture.
func (r *Renderer) UIGlowLayer() bool { return r.uiLayer != nil }
