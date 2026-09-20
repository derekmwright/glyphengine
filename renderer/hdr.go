package renderer

import (
	"fmt"
	"log"

	"github.com/vkngwrapper/core/v3/core1_0"
	"github.com/vkngwrapper/extensions/v3/khr_swapchain"
)

// hdrFormat is what the scene renders into before tonemapping.
//
// Half-float rather than the swapchain's 8-bit sRGB, because 8 bits per channel
// cannot represent a value above 1 and therefore cannot represent a highlight.
// Everything downstream of that follows: a tonemap curve has nothing to compress,
// bloom has nothing to bloom, and a sun disc is simply white rather than bright.
// The engine's own notes record ACES being tried and reverted for exactly this
// reason -- it is built for HDR input and there was none.
//
// That the target actually holds values above 1, rather than being a costlier
// route to the same clipped image, was checked by emitting 4x from lit.frag and
// exposing at 0.25: the lit geometry came back intact where an 8-bit target
// would have returned a flat quarter-grey. See docs/agents/hdr-tonemap.md.
const hdrFormat = core1_0.FormatR16G16B16A16SignedFloat

// hdrTarget is the offscreen colour buffer the scene renders into, one per
// swapchain image so it can be written while another is being presented.
//
// It is sampled by the tonemap pass and copied from by the water refraction
// pass, which is why it carries both Sampled and TransferSrc.
type hdrTarget struct {
	images  []core1_0.Image
	memory  []core1_0.DeviceMemory
	views   []core1_0.ImageView
	sampler core1_0.Sampler

	// sceneSets sample this image alone, at set 0 binding 0. The bloom
	// prefilter reads the scene through these.
	sceneSets []core1_0.DescriptorSet

	// tonemapSets bind the scene at binding 0 and the bloom chain's finest
	// level at binding 1. They are written after the bloom chain exists, which
	// is why they are not filled in by createHDRTargets.
	//
	// The UI glow layer is an hdrTarget too and uses these for its own resolve,
	// which pairs the same two things: a colour target and the bloom chain over
	// it. Same shape, same writeTonemapSets, different images.
	tonemapSets []core1_0.DescriptorSet

	extent core1_0.Extent2D
}

// hdrSupported reports whether the device can use hdrFormat as a colour
// attachment that is also sampleable and copyable.
//
// The Vulkan spec makes these features mandatory for this format, so this should
// never report false. It is checked anyway because the alternative to a clear
// error at startup is a device that fails somewhere deep in framebuffer
// creation, and because "mandatory" is a claim about the spec rather than about
// the driver in front of you.
func hdrSupported(instanceDriver core1_0.CoreInstanceDriver, physicalDevice core1_0.PhysicalDevice) bool {
	p := instanceDriver.GetPhysicalDeviceFormatProperties(physicalDevice, hdrFormat)
	const need = core1_0.FormatFeatureColorAttachment |
		core1_0.FormatFeatureSampledImage |
		core1_0.FormatFeatureBlitSource
	return p.OptimalTilingFeatures&need == need
}

// createHDRTargets allocates one HDR colour buffer per swapchain image.
//
// label names the target in the startup log, because there is more than one of
// these now: the scene's, and -- when the UI glow layer is on -- the screen-space
// UI's own. Two lines reading "HDR target" with different sizes is the kind of
// log that makes someone doubt the one that is correct.
func createHDRTargets(
	instanceDriver core1_0.CoreInstanceDriver,
	deviceDriver core1_0.CoreDeviceDriver,
	physicalDevice core1_0.PhysicalDevice,
	descriptorPool core1_0.DescriptorPool,
	texSetLayout core1_0.DescriptorSetLayout,
	extent core1_0.Extent2D,
	count int,
	maxAnisotropy float32,
	label string,
) (*hdrTarget, error) {
	t := &hdrTarget{extent: extent}

	for i := 0; i < count; i++ {
		img, _, err := deviceDriver.CreateImage(nil, core1_0.ImageCreateInfo{
			ImageType:   core1_0.ImageType2D,
			Format:      hdrFormat,
			Extent:      core1_0.Extent3D{Width: extent.Width, Height: extent.Height, Depth: 1},
			MipLevels:   1,
			ArrayLayers: 1,
			Samples:     core1_0.Samples1,
			Tiling:      core1_0.ImageTilingOptimal,
			// Sampled for the tonemap pass, TransferSrc for the water pass's
			// copy into its refraction source.
			Usage:         core1_0.ImageUsageColorAttachment | core1_0.ImageUsageSampled | core1_0.ImageUsageTransferSrc,
			SharingMode:   core1_0.SharingModeExclusive,
			InitialLayout: core1_0.ImageLayoutUndefined,
		})
		if err != nil {
			t.destroy(deviceDriver)
			return nil, fmt.Errorf("create hdr image %d: %w", i, err)
		}
		t.images = append(t.images, img)

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
			return nil, fmt.Errorf("allocate hdr memory %d: %w", i, err)
		}
		t.memory = append(t.memory, mem)

		if _, err := deviceDriver.BindImageMemory(img, mem, 0); err != nil {
			t.destroy(deviceDriver)
			return nil, fmt.Errorf("bind hdr memory %d: %w", i, err)
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
			return nil, fmt.Errorf("create hdr view %d: %w", i, err)
		}
		t.views = append(t.views, view)
	}

	// One sampler for all of them. Clamped and unfiltered between texels is
	// enough: the tonemap pass samples one to one.
	sampler, _, err := deviceDriver.CreateSampler(nil, core1_0.SamplerCreateInfo{
		MagFilter:    core1_0.FilterLinear,
		MinFilter:    core1_0.FilterLinear,
		AddressModeU: core1_0.SamplerAddressModeClampToEdge,
		AddressModeV: core1_0.SamplerAddressModeClampToEdge,
		AddressModeW: core1_0.SamplerAddressModeClampToEdge,
		MipmapMode:   core1_0.SamplerMipmapModeNearest,
	})
	if err != nil {
		t.destroy(deviceDriver)
		return nil, fmt.Errorf("create hdr sampler: %w", err)
	}
	t.sampler = sampler

	layouts := make([]core1_0.DescriptorSetLayout, count)
	for i := range layouts {
		layouts[i] = texSetLayout
	}
	sets, _, err := deviceDriver.AllocateDescriptorSets(core1_0.DescriptorSetAllocateInfo{
		DescriptorPool: descriptorPool,
		SetLayouts:     layouts,
	})
	if err != nil {
		t.destroy(deviceDriver)
		return nil, fmt.Errorf("allocate hdr descriptor sets: %w", err)
	}
	t.sceneSets = sets

	writes := make([]core1_0.WriteDescriptorSet, count)
	for i := 0; i < count; i++ {
		writes[i] = core1_0.WriteDescriptorSet{
			DstSet:         sets[i],
			DstBinding:     0,
			DescriptorType: core1_0.DescriptorTypeCombinedImageSampler,
			ImageInfo: []core1_0.DescriptorImageInfo{{
				Sampler:     sampler,
				ImageView:   t.views[i],
				ImageLayout: core1_0.ImageLayoutShaderReadOnlyOptimal,
			}},
		}
	}
	if err := deviceDriver.UpdateDescriptorSets(writes, nil); err != nil {
		t.destroy(deviceDriver)
		return nil, fmt.Errorf("update hdr descriptor sets: %w", err)
	}

	log.Printf("%s: %dx%d R16G16B16A16_SFLOAT x%d", label, extent.Width, extent.Height, count)
	return t, nil
}

func (t *hdrTarget) destroy(deviceDriver core1_0.DeviceDriver) {
	if t == nil {
		return
	}
	// The sets go back to the pool, before the sampler and views they name.
	// recreateSwapchain builds a whole new target on every rebuild -- not only
	// on a resize -- and until the pool carried
	// VK_DESCRIPTOR_POOL_CREATE_FREE_DESCRIPTOR_SET_BIT these could not be
	// given back, so each rebuild spent MaxSets and never refilled it.
	// Measured on 13-ui -glow on with a rebuild provoked every other frame:
	// the 16th one failed with "allocate bloom descriptor sets 1: vulkan
	// error: out of pool memory". Fifteen is not a number a long session has
	// to work for -- recreateSwapchain's own comment lists what produces a
	// rebuild, and showing a window, moving it between monitors and a
	// suboptimal present are all on it.
	//
	// The validation layer does not report the exhaustion itself: an
	// allocation refused for want of pool space is a legitimate return, not
	// misuse. What it did report, in that run, was the wreckage afterwards --
	// invalid framebuffer handles from the failure path.
	//
	// Safe without any deferral here, and only here: both callers have
	// already idled the device. recreateSwapchain waits before it destroys
	// anything, and Destroy waits before it unwinds the init stack this sits
	// on.
	freeSets(deviceDriver, t.sceneSets)
	freeSets(deviceDriver, t.tonemapSets)
	if t.sampler.Handle() != 0 {
		deviceDriver.DestroySampler(t.sampler, nil)
		t.sampler = core1_0.Sampler{}
	}
	for _, v := range t.views {
		deviceDriver.DestroyImageView(v, nil)
	}
	for _, m := range t.memory {
		deviceDriver.FreeMemory(m, nil)
	}
	for _, i := range t.images {
		deviceDriver.DestroyImage(i, nil)
	}
	t.views, t.memory, t.images = nil, nil, nil
	t.sceneSets, t.tonemapSets = nil, nil
}

// createTonemapSetLayout is set 0 for the resolve: the scene at binding 0 and
// the bloom chain's finest level at binding 1.
//
// Separate from the plain one-sampler layout because the resolve is the only
// pass that reads both. Binding 1 is written whether or not bloom is on -- a
// descriptor a shader statically references has to be valid even on the path
// that never samples it.
func createTonemapSetLayout(deviceDriver core1_0.DeviceDriver) (core1_0.DescriptorSetLayout, error) {
	bindings := make([]core1_0.DescriptorSetLayoutBinding, 2)
	for i := range bindings {
		bindings[i] = core1_0.DescriptorSetLayoutBinding{
			Binding:         i,
			DescriptorType:  core1_0.DescriptorTypeCombinedImageSampler,
			DescriptorCount: 1,
			StageFlags:      core1_0.StageFragment,
		}
	}
	layout, _, err := deviceDriver.CreateDescriptorSetLayout(nil, core1_0.DescriptorSetLayoutCreateInfo{
		Bindings: bindings,
	})
	if err != nil {
		return core1_0.DescriptorSetLayout{}, fmt.Errorf("create tonemap descriptor set layout: %w", err)
	}
	return layout, nil
}

// writeTonemapSets allocates and fills the resolve's descriptor sets, pairing
// each HDR image with the matching bloom chain's level 0.
func writeTonemapSets(
	deviceDriver core1_0.DeviceDriver,
	descriptorPool core1_0.DescriptorPool,
	layout core1_0.DescriptorSetLayout,
	hdr *hdrTarget,
	bloom *bloomTarget,
) error {
	count := len(hdr.views)
	layouts := make([]core1_0.DescriptorSetLayout, count)
	for i := range layouts {
		layouts[i] = layout
	}
	sets, _, err := deviceDriver.AllocateDescriptorSets(core1_0.DescriptorSetAllocateInfo{
		DescriptorPool: descriptorPool,
		SetLayouts:     layouts,
	})
	if err != nil {
		return fmt.Errorf("allocate tonemap descriptor sets: %w", err)
	}
	hdr.tonemapSets = sets

	writes := make([]core1_0.WriteDescriptorSet, 0, count*2)
	for i := 0; i < count; i++ {
		writes = append(writes,
			core1_0.WriteDescriptorSet{
				DstSet:         sets[i],
				DstBinding:     0,
				DescriptorType: core1_0.DescriptorTypeCombinedImageSampler,
				ImageInfo: []core1_0.DescriptorImageInfo{{
					Sampler:     hdr.sampler,
					ImageView:   hdr.views[i],
					ImageLayout: core1_0.ImageLayoutShaderReadOnlyOptimal,
				}},
			},
			core1_0.WriteDescriptorSet{
				DstSet:         sets[i],
				DstBinding:     1,
				DescriptorType: core1_0.DescriptorTypeCombinedImageSampler,
				ImageInfo: []core1_0.DescriptorImageInfo{{
					Sampler:     bloom.sampler,
					ImageView:   bloom.views[i][0],
					ImageLayout: core1_0.ImageLayoutShaderReadOnlyOptimal,
				}},
			},
		)
	}
	if err := deviceDriver.UpdateDescriptorSets(writes, nil); err != nil {
		return fmt.Errorf("update tonemap descriptor sets: %w", err)
	}
	return nil
}

// createTonemapRenderPass writes the swapchain from the HDR scene.
//
// Single sample and no depth: MSAA was already resolved into the HDR target, and
// a fullscreen triangle has nothing to depth-test against. LoadOp is DontCare
// because every pixel is written.
func createTonemapRenderPass(deviceDriver core1_0.DeviceDriver, swapchainFormat core1_0.Format) (core1_0.RenderPass, error) {
	renderPass, _, err := deviceDriver.CreateRenderPass(nil, core1_0.RenderPassCreateInfo{
		Attachments: []core1_0.AttachmentDescription{{
			Format:         swapchainFormat,
			Samples:        core1_0.Samples1,
			LoadOp:         core1_0.AttachmentLoadOpDontCare,
			StoreOp:        core1_0.AttachmentStoreOpStore,
			StencilLoadOp:  core1_0.AttachmentLoadOpDontCare,
			StencilStoreOp: core1_0.AttachmentStoreOpDontCare,
			InitialLayout:  core1_0.ImageLayoutUndefined,
			FinalLayout:    khr_swapchain.ImageLayoutPresentSrc,
		}},
		Subpasses: []core1_0.SubpassDescription{{
			PipelineBindPoint: core1_0.PipelineBindPointGraphics,
			ColorAttachments: []core1_0.AttachmentReference{
				{Attachment: 0, Layout: core1_0.ImageLayoutColorAttachmentOptimal},
			},
		}},
		SubpassDependencies: []core1_0.SubpassDependency{{
			// Wait for the scene's writes to the HDR target to be visible to the
			// sampler before reading it.
			SrcSubpass:    core1_0.SubpassExternal,
			DstSubpass:    0,
			SrcStageMask:  core1_0.PipelineStageColorAttachmentOutput,
			DstStageMask:  core1_0.PipelineStageFragmentShader,
			SrcAccessMask: core1_0.AccessColorAttachmentWrite,
			DstAccessMask: core1_0.AccessShaderRead,
		}},
	})
	if err != nil {
		return core1_0.RenderPass{}, fmt.Errorf("create tonemap render pass: %w", err)
	}
	log.Println("Tonemap render pass created")
	return renderPass, nil
}

// createResolvePipeline builds a fullscreen resolve into the swapchain image.
// It reuses sky.vert, which is already the fullscreen triangle, and a pipeline
// layout whose set 0 pairs a colour target with its bloom chain -- exactly what
// both resolves need.
//
// Two of them exist: the scene's tonemap, which writes every pixel of an opaque
// image and therefore does not blend, and the UI glow layer's composite, which
// lays a transparent layer over what the tonemap just wrote and therefore does.
// One function with a blend flag rather than two near-identical copies, for the
// reason createLitVariantPipeline gives: a second copy is how one of them
// quietly ends up with the wrong state, and the state that would differ here is
// precisely the one whose failure reads as "the antialiasing is broken".
//
// blend is premultiplied "over": the source is already scaled by its own alpha
// (see ui.frag), so the source factor is One rather than SrcAlpha.
func createResolvePipeline(deviceDriver core1_0.DeviceDriver, sh ShaderSet, frag []byte, label string, renderPass core1_0.RenderPass, pipelineLayout core1_0.PipelineLayout, extent core1_0.Extent2D, blend bool) (core1_0.Pipeline, error) {
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

	attachment := core1_0.PipelineColorBlendAttachmentState{
		ColorWriteMask: core1_0.ColorComponentRed | core1_0.ColorComponentGreen | core1_0.ColorComponentBlue | core1_0.ColorComponentAlpha,
		BlendEnabled:   false,
	}
	if blend {
		attachment.BlendEnabled = true
		attachment.SrcColorBlendFactor = core1_0.BlendFactorOne
		attachment.DstColorBlendFactor = core1_0.BlendFactorOneMinusSrcAlpha
		attachment.ColorBlendOp = core1_0.BlendOpAdd
		attachment.SrcAlphaBlendFactor = core1_0.BlendFactorOne
		attachment.DstAlphaBlendFactor = core1_0.BlendFactorOneMinusSrcAlpha
		attachment.AlphaBlendOp = core1_0.BlendOpAdd
	}

	pipelines, _, err := deviceDriver.CreateGraphicsPipelines(nil, nil, core1_0.GraphicsPipelineCreateInfo{
		Stages: []core1_0.PipelineShaderStageCreateInfo{
			{Stage: core1_0.StageVertex, Module: vertModule, Name: "main"},
			{Stage: core1_0.StageFragment, Module: fragModule, Name: "main"},
		},
		VertexInputState:   &core1_0.PipelineVertexInputStateCreateInfo{},
		InputAssemblyState: &core1_0.PipelineInputAssemblyStateCreateInfo{Topology: core1_0.PrimitiveTopologyTriangleList},
		ViewportState: &core1_0.PipelineViewportStateCreateInfo{
			Viewports: []core1_0.Viewport{{X: 0, Y: 0, Width: float32(extent.Width), Height: float32(extent.Height), MinDepth: 0, MaxDepth: 1}},
			Scissors:  []core1_0.Rect2D{{Offset: core1_0.Offset2D{X: 0, Y: 0}, Extent: extent}},
		},
		RasterizationState: &core1_0.PipelineRasterizationStateCreateInfo{
			PolygonMode: core1_0.PolygonModeFill,
			CullMode:    0,
			FrontFace:   core1_0.FrontFaceClockwise,
			LineWidth:   1.0,
		},
		MultisampleState:  &core1_0.PipelineMultisampleStateCreateInfo{RasterizationSamples: core1_0.Samples1},
		DepthStencilState: &core1_0.PipelineDepthStencilStateCreateInfo{DepthTestEnable: false, DepthWriteEnable: false},
		ColorBlendState: &core1_0.PipelineColorBlendStateCreateInfo{
			Attachments: []core1_0.PipelineColorBlendAttachmentState{attachment},
		},
		DynamicState: &core1_0.PipelineDynamicStateCreateInfo{
			DynamicStates: []core1_0.DynamicState{core1_0.DynamicStateViewport, core1_0.DynamicStateScissor},
		},
		Layout:     pipelineLayout,
		RenderPass: renderPass,
		Subpass:    0,
	})
	if err != nil {
		return core1_0.Pipeline{}, fmt.Errorf("create %s pipeline: %w", label, err)
	}
	log.Printf("%s pipeline created", label)
	return pipelines[0], nil
}

// createTonemapFramebuffers makes one framebuffer per swapchain image. The
// tonemap pass has no depth attachment, so this is simpler than the scene's.
func createTonemapFramebuffers(deviceDriver core1_0.DeviceDriver, renderPass core1_0.RenderPass, imageViews []core1_0.ImageView, extent core1_0.Extent2D) ([]core1_0.Framebuffer, error) {
	out := make([]core1_0.Framebuffer, len(imageViews))
	for i, view := range imageViews {
		fb, _, err := deviceDriver.CreateFramebuffer(nil, core1_0.FramebufferCreateInfo{
			RenderPass:  renderPass,
			Attachments: []core1_0.ImageView{view},
			Width:       extent.Width,
			Height:      extent.Height,
			Layers:      1,
		})
		if err != nil {
			return nil, fmt.Errorf("create tonemap framebuffer %d: %w", i, err)
		}
		out[i] = fb
	}
	return out, nil
}

// tonemapFor bundles the resolve state for one swapchain image.
func (r *Renderer) tonemapFor(imageIndex int) tonemapPass {
	return tonemapPass{
		renderPass:  r.tonemapRenderPass,
		pipeline:    r.tonemapPipeline,
		framebuffer: r.tonemapFramebuffers[imageIndex],
		layout:      r.tonemapPipelineLayout,
		set:         r.hdr.tonemapSets[imageIndex],
		exposure:    r.exposure,
		curve:       r.tonemapCurve,
		white:       r.tonemapWhite,
		bloom:       r.bloomIntensity,
		ui:          r.uiLayerFor(imageIndex),
	}
}

// SetTonemap configures the HDR resolve.
//
// exposure multiplies the scene before the curve; zero or less leaves it alone.
// curve selects the mapping: 0 is identity, 1 is extended Reinhard with white
// mapping to white at the given white point.
//
// The default is identity at no exposure change, so adding the HDR target did
// not change how anything looks. That is deliberate — a render target and a look
// are separate decisions, and shipping them together makes it impossible to say
// which one moved the image. Nothing in the engine currently emits above 1, so a
// curve has nothing to compress until something does.
func (r *Renderer) SetTonemap(exposure, curve, whitePoint float32) {
	r.exposure, r.tonemapCurve, r.tonemapWhite = exposure, curve, whitePoint
}

// recordTonemap resolves the HDR scene into the swapchain image, then lets
// composite draw on top of the result.
//
// Every path that renders a frame has to end with this, which is why it is a
// function rather than a block inside recordCommandBuffer: the scene render
// pass now leaves its result in an offscreen image, so a path that forgets this
// presents whatever the swapchain happened to contain. The validation layer does
// catch it -- an image presented in UNDEFINED layout -- but only because nothing
// else transitions the swapchain any more.
//
// composite is where screen-space UI goes, and may be nil. It is called inside
// this render pass rather than in one of its own because this pass already
// holds the swapchain image at the right extent; see recordUIComposite for why
// UI belongs on this side of the resolve at all.
//
// The timer comes in so the two intervals can be written here, adjacent and not
// nested. Bracketing the composite from the caller would have put it inside
// PassTonemap, and passes that contain each other sum to more than the frame
// they are in -- which is the exact mismatch that caught this instrument's
// first version.
func recordTonemap(
	deviceDriver core1_0.DeviceDriver,
	cmdBuf core1_0.CommandBuffer,
	tonemap tonemapPass,
	pipelineLayout core1_0.PipelineLayout,
	extent core1_0.Extent2D,
	timer *gpuTimer,
	frame int,
	composite func(core1_0.CommandBuffer),
	scratch *commandScratch,
) error {
	timer.begin(deviceDriver, cmdBuf, frame, PassTonemap)
	if err := deviceDriver.CmdBeginRenderPass(cmdBuf, core1_0.SubpassContentsInline, core1_0.RenderPassBeginInfo{
		RenderPass:  tonemap.renderPass,
		Framebuffer: tonemap.framebuffer,
		RenderArea:  core1_0.Rect2D{Offset: core1_0.Offset2D{X: 0, Y: 0}, Extent: extent},
	}); err != nil {
		return err
	}
	deviceDriver.CmdBindPipeline(cmdBuf, core1_0.PipelineBindPointGraphics, tonemap.pipeline)
	scratch.setViewport(deviceDriver, cmdBuf, core1_0.Viewport{
		Width: float32(extent.Width), Height: float32(extent.Height), MinDepth: 0, MaxDepth: 1,
	})
	scratch.setScissor(deviceDriver, cmdBuf, core1_0.Rect2D{Offset: core1_0.Offset2D{X: 0, Y: 0}, Extent: extent})
	scratch.bindDescriptorSets(deviceDriver, cmdBuf, core1_0.PipelineBindPointGraphics, pipelineLayout, 0, tonemap.set)

	// Only tint is read, but the layout has to match what the pipeline layout
	// declares, so the whole 256 bytes go across.
	scratch.resetPC()
	scratch.pc[32] = tonemap.exposure
	scratch.pc[33] = tonemap.curve
	scratch.pc[34] = tonemap.white
	scratch.pc[35] = tonemap.bloom
	scratch.pushConstants(deviceDriver, cmdBuf, pipelineLayout, core1_0.StageVertex|core1_0.StageFragment)

	deviceDriver.CmdDraw(cmdBuf, 3, 1, 0, 0)
	timer.end(deviceDriver, cmdBuf, frame, PassTonemap)

	if composite != nil {
		timer.begin(deviceDriver, cmdBuf, frame, PassComposite)
		composite(cmdBuf)
		timer.end(deviceDriver, cmdBuf, frame, PassComposite)
	}

	deviceDriver.CmdEndRenderPass(cmdBuf)
	return nil
}

// Tonemap returns the current exposure, curve and white point, as last set by
// SetTonemap. Exposed so a harness can toggle a curve and put it back.
func (r *Renderer) Tonemap() (exposure, curve, whitePoint float32) {
	return r.exposure, r.tonemapCurve, r.tonemapWhite
}
