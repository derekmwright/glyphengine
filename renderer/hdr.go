package renderer

import (
	"fmt"
	"log"
	"unsafe"

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
func createHDRTargets(
	instanceDriver core1_0.CoreInstanceDriver,
	deviceDriver core1_0.CoreDeviceDriver,
	physicalDevice core1_0.PhysicalDevice,
	descriptorPool core1_0.DescriptorPool,
	texSetLayout core1_0.DescriptorSetLayout,
	extent core1_0.Extent2D,
	count int,
	maxAnisotropy float32,
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

	log.Printf("HDR target: %dx%d R16G16B16A16_SFLOAT x%d", extent.Width, extent.Height, count)
	return t, nil
}

func (t *hdrTarget) destroy(deviceDriver core1_0.DeviceDriver) {
	if t == nil {
		return
	}
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

// createTonemapPipeline builds the fullscreen resolve. It reuses sky.vert, which
// is already the fullscreen triangle, and the non-lit pipeline layout, whose
// set 0 is a single combined image sampler -- exactly what this needs.
func createTonemapPipeline(deviceDriver core1_0.DeviceDriver, sh ShaderSet, renderPass core1_0.RenderPass, pipelineLayout core1_0.PipelineLayout, extent core1_0.Extent2D) (core1_0.Pipeline, error) {
	vertModule, _, err := deviceDriver.CreateShaderModule(nil, core1_0.ShaderModuleCreateInfo{
		Code: bytesToUint32Slice(sh.SkyVert),
	})
	if err != nil {
		return core1_0.Pipeline{}, err
	}
	defer deviceDriver.DestroyShaderModule(vertModule, nil)

	fragModule, _, err := deviceDriver.CreateShaderModule(nil, core1_0.ShaderModuleCreateInfo{
		Code: bytesToUint32Slice(sh.TonemapFrag),
	})
	if err != nil {
		return core1_0.Pipeline{}, err
	}
	defer deviceDriver.DestroyShaderModule(fragModule, nil)

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
			Attachments: []core1_0.PipelineColorBlendAttachmentState{{
				ColorWriteMask: core1_0.ColorComponentRed | core1_0.ColorComponentGreen | core1_0.ColorComponentBlue | core1_0.ColorComponentAlpha,
				BlendEnabled:   false,
			}},
		},
		DynamicState: &core1_0.PipelineDynamicStateCreateInfo{
			DynamicStates: []core1_0.DynamicState{core1_0.DynamicStateViewport, core1_0.DynamicStateScissor},
		},
		Layout:     pipelineLayout,
		RenderPass: renderPass,
		Subpass:    0,
	})
	if err != nil {
		return core1_0.Pipeline{}, fmt.Errorf("create tonemap pipeline: %w", err)
	}
	log.Println("Tonemap pipeline created")
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

// recordTonemap resolves the HDR scene into the swapchain image.
//
// Every path that renders a frame has to end with this, which is why it is a
// function rather than a block inside recordCommandBuffer: the scene render
// pass now leaves its result in an offscreen image, so a path that forgets this
// presents whatever the swapchain happened to contain. The validation layer does
// catch it -- an image presented in UNDEFINED layout -- but only because nothing
// else transitions the swapchain any more.
func recordTonemap(
	deviceDriver core1_0.DeviceDriver,
	cmdBuf core1_0.CommandBuffer,
	tonemap tonemapPass,
	pipelineLayout core1_0.PipelineLayout,
	extent core1_0.Extent2D,
) error {
	if err := deviceDriver.CmdBeginRenderPass(cmdBuf, core1_0.SubpassContentsInline, core1_0.RenderPassBeginInfo{
		RenderPass:  tonemap.renderPass,
		Framebuffer: tonemap.framebuffer,
		RenderArea:  core1_0.Rect2D{Offset: core1_0.Offset2D{X: 0, Y: 0}, Extent: extent},
	}); err != nil {
		return err
	}
	deviceDriver.CmdBindPipeline(cmdBuf, core1_0.PipelineBindPointGraphics, tonemap.pipeline)
	deviceDriver.CmdSetViewport(cmdBuf, core1_0.Viewport{
		Width: float32(extent.Width), Height: float32(extent.Height), MinDepth: 0, MaxDepth: 1,
	})
	deviceDriver.CmdSetScissor(cmdBuf, core1_0.Rect2D{Offset: core1_0.Offset2D{X: 0, Y: 0}, Extent: extent})
	deviceDriver.CmdBindDescriptorSets(cmdBuf, core1_0.PipelineBindPointGraphics, pipelineLayout, 0,
		[]core1_0.DescriptorSet{tonemap.set}, nil)

	// Only tint is read, but the layout has to match what the pipeline layout
	// declares, so the whole 256 bytes go across.
	var pc [pushConstantSize / 4]float32
	pc[32] = tonemap.exposure
	pc[33] = tonemap.curve
	pc[34] = tonemap.white
	pc[35] = tonemap.bloom
	deviceDriver.CmdPushConstants(cmdBuf, pipelineLayout, core1_0.StageVertex|core1_0.StageFragment, 0,
		unsafe.Slice((*byte)(unsafe.Pointer(&pc[0])), pushConstantSize))

	deviceDriver.CmdDraw(cmdBuf, 3, 1, 0, 0)
	deviceDriver.CmdEndRenderPass(cmdBuf)
	return nil
}
