package renderer

import (
	"fmt"
	"log"

	"github.com/vkngwrapper/core/v3/core1_0"
)

// bloomLevels is how many halvings the chain does below half resolution.
//
// Five is not arbitrary. Each level doubles the width of the glow for the same
// thirteen taps, so the widest halo this can produce spans roughly 1/32 of the
// frame; fewer levels and a bright light produces a tight ring that reads as a
// rendering artefact rather than as glare. The cost of the tail is negligible --
// level 4 at 1280x720 is 40x22 pixels -- so the levels that matter for cost are
// the first two, and those are the ones that carry the detail.
const bloomLevels = 5

// bloomTarget is the mip chain the bloom passes ping-pong through, one chain per
// swapchain image.
//
// Per swapchain image for the same reason the HDR target is: a frame can be
// writing this chain while the GPU is still reading the previous frame's during
// its tonemap. Sharing one chain would be a race that shows up as flicker on
// exactly the frames where the glow is brightest.
type bloomTarget struct {
	// Indexed [image][level].
	images [][]core1_0.Image
	memory [][]core1_0.DeviceMemory
	views  [][]core1_0.ImageView

	// sets samples the matching image at set 0 binding 0, for whichever pass
	// reads that level as its source.
	sets [][]core1_0.DescriptorSet

	extents []core1_0.Extent2D
	sampler core1_0.Sampler
}

// bloomExtents halves the frame bloomLevels times, starting at half resolution.
//
// Clamped at 1 rather than allowed to reach zero: a zero-sized image is an
// invalid create, and on a small enough window the tail levels would otherwise
// collapse. A window narrow enough to hit the clamp has a degenerate chain, not
// a broken one -- levels just stop getting smaller.
func bloomExtents(full core1_0.Extent2D) []core1_0.Extent2D {
	out := make([]core1_0.Extent2D, bloomLevels)
	w, h := full.Width, full.Height
	for i := 0; i < bloomLevels; i++ {
		w = max(w/2, 1)
		h = max(h/2, 1)
		out[i] = core1_0.Extent2D{Width: w, Height: h}
	}
	return out
}

// createBloomTargets allocates the chain and its descriptor sets.
func createBloomTargets(
	instanceDriver core1_0.CoreInstanceDriver,
	deviceDriver core1_0.CoreDeviceDriver,
	physicalDevice core1_0.PhysicalDevice,
	descriptorPool core1_0.DescriptorPool,
	texSetLayout core1_0.DescriptorSetLayout,
	full core1_0.Extent2D,
	count int,
) (*bloomTarget, error) {
	t := &bloomTarget{extents: bloomExtents(full)}

	// Clamped to edge, and linear: every kernel in bloom.inc reads between
	// texels on purpose, and wrapping would fold the far side of the screen into
	// the glow at the borders.
	sampler, _, err := deviceDriver.CreateSampler(nil, core1_0.SamplerCreateInfo{
		MagFilter:    core1_0.FilterLinear,
		MinFilter:    core1_0.FilterLinear,
		AddressModeU: core1_0.SamplerAddressModeClampToEdge,
		AddressModeV: core1_0.SamplerAddressModeClampToEdge,
		AddressModeW: core1_0.SamplerAddressModeClampToEdge,
		MipmapMode:   core1_0.SamplerMipmapModeNearest,
	})
	if err != nil {
		return nil, fmt.Errorf("create bloom sampler: %w", err)
	}
	t.sampler = sampler

	for i := 0; i < count; i++ {
		// Track each partial row immediately. Failure injection at image
		// 8 of a three-image chain used to leak its first mip (one image,
		// view and allocation); TestAppTargetRebuildFailures covers every mip.
		t.images = append(t.images, nil)
		t.memory = append(t.memory, nil)
		t.views = append(t.views, nil)
		for lvl := 0; lvl < bloomLevels; lvl++ {
			ext := t.extents[lvl]
			img, _, err := deviceDriver.CreateImage(nil, core1_0.ImageCreateInfo{
				ImageType:   core1_0.ImageType2D,
				Format:      hdrFormat,
				Extent:      core1_0.Extent3D{Width: ext.Width, Height: ext.Height, Depth: 1},
				MipLevels:   1,
				ArrayLayers: 1,
				Samples:     core1_0.Samples1,
				Tiling:      core1_0.ImageTilingOptimal,
				// TransferDst is only for the one-time clear in
				// primeBloomLayouts; nothing copies into these at run time.
				Usage:         core1_0.ImageUsageColorAttachment | core1_0.ImageUsageSampled | core1_0.ImageUsageTransferDst,
				SharingMode:   core1_0.SharingModeExclusive,
				InitialLayout: core1_0.ImageLayoutUndefined,
			})
			if err != nil {
				t.destroy(deviceDriver)
				return nil, fmt.Errorf("create bloom image %d/%d: %w", i, lvl, err)
			}
			t.images[i] = append(t.images[i], img)

			reqs := deviceDriver.GetImageMemoryRequirements(img)
			memType, err := findMemoryType(instanceDriver, physicalDevice, reqs.MemoryTypeBits, core1_0.MemoryPropertyDeviceLocal)
			if err != nil {
				t.destroy(deviceDriver)
				return nil, err
			}
			mem, _, err := deviceDriver.AllocateMemory(nil, core1_0.MemoryAllocateInfo{
				AllocationSize:  reqs.Size,
				MemoryTypeIndex: memType,
			})
			if err != nil {
				t.destroy(deviceDriver)
				return nil, fmt.Errorf("allocate bloom memory %d/%d: %w", i, lvl, err)
			}
			t.memory[i] = append(t.memory[i], mem)
			if _, err := deviceDriver.BindImageMemory(img, mem, 0); err != nil {
				t.destroy(deviceDriver)
				return nil, fmt.Errorf("bind bloom memory %d/%d: %w", i, lvl, err)
			}

			view, _, err := deviceDriver.CreateImageView(nil, core1_0.ImageViewCreateInfo{
				Image:    img,
				ViewType: core1_0.ImageViewType2D,
				Format:   hdrFormat,
				SubresourceRange: core1_0.ImageSubresourceRange{
					AspectMask: core1_0.ImageAspectColor,
					LevelCount: 1,
					LayerCount: 1,
				},
			})
			if err != nil {
				t.destroy(deviceDriver)
				return nil, fmt.Errorf("create bloom view %d/%d: %w", i, lvl, err)
			}
			t.views[i] = append(t.views[i], view)

		}

		layouts := make([]core1_0.DescriptorSetLayout, bloomLevels)
		for j := range layouts {
			layouts[j] = texSetLayout
		}
		sets, _, err := deviceDriver.AllocateDescriptorSets(core1_0.DescriptorSetAllocateInfo{
			DescriptorPool: descriptorPool,
			SetLayouts:     layouts,
		})
		if err != nil {
			t.destroy(deviceDriver)
			return nil, fmt.Errorf("allocate bloom descriptor sets %d: %w", i, err)
		}

		writes := make([]core1_0.WriteDescriptorSet, bloomLevels)
		for lvl := range writes {
			writes[lvl] = core1_0.WriteDescriptorSet{
				DstSet:         sets[lvl],
				DstBinding:     0,
				DescriptorType: core1_0.DescriptorTypeCombinedImageSampler,
				ImageInfo: []core1_0.DescriptorImageInfo{{
					Sampler:     sampler,
					ImageView:   t.views[i][lvl],
					ImageLayout: core1_0.ImageLayoutShaderReadOnlyOptimal,
				}},
			}
		}
		if err := deviceDriver.UpdateDescriptorSets(writes, nil); err != nil {
			t.sets = append(t.sets, sets)
			t.destroy(deviceDriver)
			return nil, fmt.Errorf("update bloom descriptor sets %d: %w", i, err)
		}

		t.sets = append(t.sets, sets)
	}

	log.Printf("Bloom chain: %d levels from %dx%d down to %dx%d, x%d",
		bloomLevels, t.extents[0].Width, t.extents[0].Height,
		t.extents[bloomLevels-1].Width, t.extents[bloomLevels-1].Height, count)
	return t, nil
}

func (t *bloomTarget) destroy(deviceDriver core1_0.DeviceDriver) {
	if t == nil {
		return
	}
	// The sets first, for the reason hdrTarget.destroy spells out: the chain
	// is rebuilt on every swapchain rebuild, and this is the biggest single
	// consumer in the pool -- bloomLevels sets per swapchain image, and twice
	// that with the UI glow layer on. It was also the allocation that failed
	// first when the pool ran dry after fifteen rebuilds.
	t.releaseSets(deviceDriver)
	if t.sampler.Handle() != 0 {
		deviceDriver.DestroySampler(t.sampler, nil)
		t.sampler = core1_0.Sampler{}
	}
	for _, vs := range t.views {
		for _, v := range vs {
			deviceDriver.DestroyImageView(v, nil)
		}
	}
	for _, ms := range t.memory {
		for _, m := range ms {
			deviceDriver.FreeMemory(m, nil)
		}
	}
	for _, is := range t.images {
		for _, i := range is {
			deviceDriver.DestroyImage(i, nil)
		}
	}
	t.views, t.memory, t.images = nil, nil, nil
}

// primeBloomLayouts clears every level once and leaves it in
// SHADER_READ_ONLY_OPTIMAL.
//
// Necessary because the resolve's descriptor set declares that layout for the
// bloom binding, and Vulkan validates the declared layout against the image's
// actual layout at submit -- whether or not the shader ever samples it. With
// bloom off nothing writes these images, so without this they sit in UNDEFINED
// and every frame reports
// UNASSIGNED-CoreValidation-DrawState-InvalidImageLayout.
//
// A uniform branch in the shader is not enough. That was the first attempt: it
// does stop the read, but the layout check does not care about control flow. The
// clear is worth doing on top of the transition regardless -- it means the
// images hold zeroes rather than whatever the allocation contained, so a future
// change that samples them on the disabled path gets black instead of NaN.
func (r *Renderer) primeBloomLayouts(t *bloomTarget) error {
	var imgs []core1_0.Image
	for _, level := range t.images {
		imgs = append(imgs, level...)
	}
	return r.primeSampledImages(imgs)
}

// primeSampledImages clears images once and leaves them in
// SHADER_READ_ONLY_OPTIMAL.
//
// For any image a descriptor set binds every frame but a pass only sometimes
// writes -- or has not written yet. Vulkan validates the layout a descriptor
// declares against the image's actual layout at submit, whether or not the
// shader samples it, so a uniform branch skipping the read does nothing for
// that check. It caught the bloom chain when bloom is off, and it caught the
// cloud history on the very first frame.
//
// The clear on top of the transition is worth it: the images then hold zeroes
// rather than whatever the allocation contained, so anything that does read
// them early gets black instead of NaN.
func (r *Renderer) primeSampledImages(images []core1_0.Image) error {
	if len(images) == 0 {
		return nil
	}
	cmdBuf, err := r.beginSingleTimeCommands()
	if err != nil {
		return err
	}

	rng := core1_0.ImageSubresourceRange{
		AspectMask: core1_0.ImageAspectColor,
		LevelCount: 1,
		LayerCount: 1,
	}

	var toDst, toRead []core1_0.ImageMemoryBarrier
	for _, img := range images {
		toDst = append(toDst, core1_0.ImageMemoryBarrier{
			OldLayout:           core1_0.ImageLayoutUndefined,
			NewLayout:           core1_0.ImageLayoutTransferDstOptimal,
			SrcQueueFamilyIndex: -1, DstQueueFamilyIndex: -1,
			Image:            img,
			SubresourceRange: rng,
			DstAccessMask:    core1_0.AccessTransferWrite,
		})
		toRead = append(toRead, core1_0.ImageMemoryBarrier{
			OldLayout:           core1_0.ImageLayoutTransferDstOptimal,
			NewLayout:           core1_0.ImageLayoutShaderReadOnlyOptimal,
			SrcQueueFamilyIndex: -1, DstQueueFamilyIndex: -1,
			Image:            img,
			SubresourceRange: rng,
			SrcAccessMask:    core1_0.AccessTransferWrite,
			DstAccessMask:    core1_0.AccessShaderRead,
		})
	}

	r.deviceDriver.CmdPipelineBarrier(cmdBuf,
		core1_0.PipelineStageTopOfPipe, core1_0.PipelineStageTransfer, 0, nil, nil, toDst)
	for _, img := range images {
		r.deviceDriver.CmdClearColorImage(cmdBuf, img, core1_0.ImageLayoutTransferDstOptimal,
			core1_0.ClearValueFloat{0, 0, 0, 1}, rng)
	}
	r.deviceDriver.CmdPipelineBarrier(cmdBuf,
		core1_0.PipelineStageTransfer, core1_0.PipelineStageFragmentShader, 0, nil, nil, toRead)

	return r.endSingleTimeCommands(cmdBuf)
}

// createBloomPipeline builds one fullscreen bloom stage. additive turns on the
// blend the upsample accumulates through.
func createBloomPipeline(
	deviceDriver core1_0.DeviceDriver,
	sh ShaderSet,
	frag []byte,
	renderPass core1_0.RenderPass,
	pipelineLayout core1_0.PipelineLayout,
	additive bool,
) (core1_0.Pipeline, error) {
	vertModule, _, err := deviceDriver.CreateShaderModule(nil, core1_0.ShaderModuleCreateInfo{
		Code: bytesToUint32Slice(sh.SkyVert),
	})
	if err != nil {
		return core1_0.Pipeline{}, err
	}
	defer deviceDriver.DestroyShaderModule(vertModule, nil)

	fragModule, _, err := deviceDriver.CreateShaderModule(nil, core1_0.ShaderModuleCreateInfo{
		Code: bytesToUint32Slice(frag),
	})
	if err != nil {
		return core1_0.Pipeline{}, err
	}
	defer deviceDriver.DestroyShaderModule(fragModule, nil)

	blend := core1_0.PipelineColorBlendAttachmentState{
		ColorWriteMask: core1_0.ColorComponentRed | core1_0.ColorComponentGreen | core1_0.ColorComponentBlue | core1_0.ColorComponentAlpha,
	}
	if additive {
		blend.BlendEnabled = true
		blend.SrcColorBlendFactor = core1_0.BlendFactorOne
		blend.DstColorBlendFactor = core1_0.BlendFactorOne
		blend.ColorBlendOp = core1_0.BlendOpAdd
		blend.SrcAlphaBlendFactor = core1_0.BlendFactorOne
		blend.DstAlphaBlendFactor = core1_0.BlendFactorOne
		blend.AlphaBlendOp = core1_0.BlendOpAdd
	}

	// Viewport and scissor are dynamic because every level is a different size
	// and they all share one pipeline.
	pipelines, _, err := deviceDriver.CreateGraphicsPipelines(nil, nil, core1_0.GraphicsPipelineCreateInfo{
		Stages: []core1_0.PipelineShaderStageCreateInfo{
			{Stage: core1_0.StageVertex, Module: vertModule, Name: "main"},
			{Stage: core1_0.StageFragment, Module: fragModule, Name: "main"},
		},
		VertexInputState:   &core1_0.PipelineVertexInputStateCreateInfo{},
		InputAssemblyState: &core1_0.PipelineInputAssemblyStateCreateInfo{Topology: core1_0.PrimitiveTopologyTriangleList},
		ViewportState: &core1_0.PipelineViewportStateCreateInfo{
			Viewports: []core1_0.Viewport{{Width: 1, Height: 1, MinDepth: 0, MaxDepth: 1}},
			Scissors:  []core1_0.Rect2D{{Extent: core1_0.Extent2D{Width: 1, Height: 1}}},
		},
		RasterizationState: &core1_0.PipelineRasterizationStateCreateInfo{
			PolygonMode: core1_0.PolygonModeFill,
			CullMode:    0,
			FrontFace:   core1_0.FrontFaceClockwise,
			LineWidth:   1.0,
		},
		MultisampleState:  &core1_0.PipelineMultisampleStateCreateInfo{RasterizationSamples: core1_0.Samples1},
		DepthStencilState: &core1_0.PipelineDepthStencilStateCreateInfo{},
		ColorBlendState: &core1_0.PipelineColorBlendStateCreateInfo{
			Attachments: []core1_0.PipelineColorBlendAttachmentState{blend},
		},
		DynamicState: &core1_0.PipelineDynamicStateCreateInfo{
			DynamicStates: []core1_0.DynamicState{core1_0.DynamicStateViewport, core1_0.DynamicStateScissor},
		},
		Layout:     pipelineLayout,
		RenderPass: renderPass,
		Subpass:    0,
	})
	if err != nil {
		return core1_0.Pipeline{}, fmt.Errorf("create bloom pipeline: %w", err)
	}
	return pipelines[0], nil
}

// bloomPass is the draw state recordBloomStage needs for one frame.
type bloomPass struct {
	enabled bool

	prefilter core1_0.Pipeline
	down      core1_0.Pipeline
	up        core1_0.Pipeline
	layout    core1_0.PipelineLayout

	// sceneSet samples the HDR scene at set 0 binding 0, for the prefilter.
	sceneSet core1_0.DescriptorSet

	sets    []core1_0.DescriptorSet
	extents []core1_0.Extent2D

	sceneExtent core1_0.Extent2D
	threshold   float32
	knee        float32
	radius      float32
}

// recordBloomStage is one prefilter, downsample or upsample draw. Its node
// supplies the render pass; level is the destination level in either direction.
func recordBloomStage(deviceDriver core1_0.DeviceDriver, cmdBuf core1_0.CommandBuffer, b bloomPass, level int, up bool, scratch *commandScratch) {
	dst := b.extents[level]
	source := b.sceneExtent
	pipeline, src := b.prefilter, b.sceneSet
	pc := [4]float32{b.threshold, b.knee}
	if up {
		source, pipeline, src = b.extents[level+1], b.up, b.sets[level+1]
		pc = [4]float32{b.radius, 0}
	} else if level > 0 {
		source, pipeline, src = b.extents[level-1], b.down, b.sets[level-1]
		pc = [4]float32{}
	}
	// One texel of the SOURCE being sampled. Every kernel offsets in source
	// texels, so passing the destination's size here would scale the blur by two
	// at each level and produce a glow that grows with resolution.
	pc[2], pc[3] = 1/float32(source.Width), 1/float32(source.Height)
	deviceDriver.CmdBindPipeline(cmdBuf, core1_0.PipelineBindPointGraphics, pipeline)
	scratch.setViewport(deviceDriver, cmdBuf, core1_0.Viewport{
		Width: float32(dst.Width), Height: float32(dst.Height), MinDepth: 0, MaxDepth: 1,
	})
	scratch.setScissor(deviceDriver, cmdBuf, core1_0.Rect2D{Offset: core1_0.Offset2D{X: 0, Y: 0}, Extent: dst})
	scratch.bindDescriptorSets(deviceDriver, cmdBuf, core1_0.PipelineBindPointGraphics, b.layout, 0, src)

	scratch.resetPC()
	copy(scratch.pc[32:36], pc[:])
	scratch.pushConstants(deviceDriver, cmdBuf, b.layout, core1_0.StageVertex|core1_0.StageFragment)

	deviceDriver.CmdDraw(cmdBuf, 3, 1, 0, 0)
}

// bloomFor bundles the bloom state for one swapchain image.
func (r *Renderer) bloomFor(imageIndex int) bloomPass {
	return bloomPass{
		enabled:     r.bloomIntensity > 0,
		prefilter:   r.bloomPrefilterPipeline,
		down:        r.bloomDownPipeline,
		up:          r.bloomUpPipeline,
		layout:      r.pipelineLayout,
		sceneSet:    r.hdr.sceneSets[imageIndex],
		sets:        r.bloom.sets[imageIndex],
		extents:     r.bloom.extents,
		sceneExtent: r.sc.extent,
		threshold:   r.bloomThreshold,
		knee:        r.bloomKnee,
		radius:      r.bloomRadius,
	}
}

// SetBloom configures the glare added around anything brighter than threshold.
//
// intensity scales what the bloom contributes; zero or less switches the whole
// chain off and skips recording it, so a scene that does not want bloom does not
// pay for it. threshold is the luminance above which a pixel starts to glow, and
// knee softens the transition either side of it -- a hard cutoff makes a moving
// highlight pop in and out as it crosses. radius widens the upsample tent in
// source texels.
//
// The default is off. Bloom on a scene with nothing above 1 is a blur of the
// whole image, because a threshold of 1 selects every white pixel; it needs
// something genuinely bright, which in this engine means an emissive material.
// See docs/agents/bloom.md.
//
// Keep threshold-knee at or above 1. Below that the ramp reaches into what an
// 8-bit frame could already represent, and the sky goes with it: a daytime sky
// sits around 0.68 in linear, so a knee of 0.4 under a threshold of 1.0 lifts
// the entire frame rather than selecting the highlights. That is measurable --
// 16-materials moved by RMS 0.025 across the sky before the threshold was
// raised -- and it looks like haze, not like a mistake, which is what makes it
// worth writing down.
//
// Sensible starting point: SetBloom(0.7, 1.2, 0.2, 1.0).
func (r *Renderer) SetBloom(intensity, threshold, knee, radius float32) {
	if knee < 0 {
		knee = 0
	}
	if radius <= 0 {
		radius = 1
	}
	r.bloomIntensity, r.bloomThreshold, r.bloomKnee, r.bloomRadius = intensity, threshold, knee, radius
}

// Bloom returns the current intensity, threshold, knee and radius, as last set
// by SetBloom. Exposed so a harness can switch bloom off and restore the same
// settings rather than guessing them.
func (r *Renderer) Bloom() (intensity, threshold, knee, radius float32) {
	return r.bloomIntensity, r.bloomThreshold, r.bloomKnee, r.bloomRadius
}

func (t *bloomTarget) releaseSets(d core1_0.DeviceDriver) {
	if t == nil {
		return
	}
	for _, sets := range t.sets {
		freeSets(d, sets)
	}
	t.sets = nil
}
