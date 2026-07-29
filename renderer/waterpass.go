package renderer

import (
	"fmt"
	"log"

	"github.com/vkngwrapper/core/v3/core1_0"
	"github.com/vkngwrapper/extensions/v3/khr_swapchain"
)

// The water pass exists for one reason: refraction has to sample what is
// already on screen, and a shader cannot read the attachment it is writing.
//
// So the frame splits. The first pass draws everything opaque and presents it
// as usual. Its result is then copied into an ordinary sampled image, and a
// second pass draws only the water, reading that copy with an offset taken from
// the wave normal. That offset is the whole effect.
//
// Both passes share the depth buffer, which is why the first now stores depth
// instead of discarding it: water still has to be occluded by the terrain in
// front of it. They also share the multisample colour buffer under MSAA, so the
// second pass loads the scene rather than starting from a cleared target.
//
// The split only happens on frames that actually contain water. A scene with no
// water pays for nothing beyond the two changed store ops.

// sceneColorTarget is a single-sample copy of the opaque scene, bound at set 0
// for the water shader.
type sceneColorTarget struct {
	image   core1_0.Image
	memory  core1_0.DeviceMemory
	texture *Texture
	extent  core1_0.Extent2D
}

// createSceneColorTarget allocates the refraction source at swapchain size.
//
// It is deliberately single-sample even under MSAA: it is the resolved image
// being copied, and a multisample copy would need a resolve of its own for no
// visible gain — refraction reads it through a wobbling offset that hides far
// more than an edge sample ever would.
func createSceneColorTarget(
	instanceDriver core1_0.CoreInstanceDriver,
	deviceDriver core1_0.CoreDeviceDriver,
	physicalDevice core1_0.PhysicalDevice,
	descriptorPool core1_0.DescriptorPool,
	texSetLayout core1_0.DescriptorSetLayout,
	extent core1_0.Extent2D,
	format core1_0.Format,
	maxAnisotropy float32,
) (*sceneColorTarget, error) {
	img, _, err := deviceDriver.CreateImage(nil, core1_0.ImageCreateInfo{
		ImageType: core1_0.ImageType2D,
		Format:    format,
		Extent:    core1_0.Extent3D{Width: extent.Width, Height: extent.Height, Depth: 1},
		MipLevels: 1, ArrayLayers: 1,
		Samples:     core1_0.Samples1,
		Tiling:      core1_0.ImageTilingOptimal,
		Usage:       core1_0.ImageUsageTransferDst | core1_0.ImageUsageSampled,
		SharingMode: core1_0.SharingModeExclusive,
	})
	if err != nil {
		return nil, fmt.Errorf("create scene color image: %w", err)
	}

	reqs := deviceDriver.GetImageMemoryRequirements(img)
	memType, err := findMemoryType(instanceDriver, physicalDevice, reqs.MemoryTypeBits, core1_0.MemoryPropertyDeviceLocal)
	if err != nil {
		deviceDriver.DestroyImage(img, nil)
		return nil, fmt.Errorf("scene color memory type: %w", err)
	}
	mem, _, err := deviceDriver.AllocateMemory(nil, core1_0.MemoryAllocateInfo{
		AllocationSize:  reqs.Size,
		MemoryTypeIndex: memType,
	})
	if err != nil {
		deviceDriver.DestroyImage(img, nil)
		return nil, fmt.Errorf("allocate scene color memory: %w", err)
	}
	if _, err := deviceDriver.BindImageMemory(img, mem, 0); err != nil {
		deviceDriver.FreeMemory(mem, nil)
		deviceDriver.DestroyImage(img, nil)
		return nil, fmt.Errorf("bind scene color memory: %w", err)
	}

	view, _, err := deviceDriver.CreateImageView(nil, core1_0.ImageViewCreateInfo{
		Image:    img,
		ViewType: core1_0.ImageViewType2D,
		Format:   format,
		SubresourceRange: core1_0.ImageSubresourceRange{
			AspectMask: core1_0.ImageAspectColor,
			LevelCount: 1, LayerCount: 1,
		},
	})
	if err != nil {
		deviceDriver.FreeMemory(mem, nil)
		deviceDriver.DestroyImage(img, nil)
		return nil, fmt.Errorf("create scene color view: %w", err)
	}

	// Clamp to edge, because a refraction offset near the screen border would
	// otherwise wrap and pull the opposite side of the frame into the water.
	sampler, _, err := deviceDriver.CreateSampler(nil, core1_0.SamplerCreateInfo{
		MagFilter: core1_0.FilterLinear, MinFilter: core1_0.FilterLinear,
		AddressModeU: core1_0.SamplerAddressModeClampToEdge,
		AddressModeV: core1_0.SamplerAddressModeClampToEdge,
		AddressModeW: core1_0.SamplerAddressModeClampToEdge,
		MipmapMode:   core1_0.SamplerMipmapModeLinear,
		BorderColor:  core1_0.BorderColorFloatOpaqueBlack,
	})
	if err != nil {
		deviceDriver.DestroyImageView(view, nil)
		deviceDriver.FreeMemory(mem, nil)
		deviceDriver.DestroyImage(img, nil)
		return nil, fmt.Errorf("create scene color sampler: %w", err)
	}

	sets, _, err := deviceDriver.AllocateDescriptorSets(core1_0.DescriptorSetAllocateInfo{
		DescriptorPool: descriptorPool,
		SetLayouts:     []core1_0.DescriptorSetLayout{texSetLayout},
	})
	if err != nil {
		deviceDriver.DestroySampler(sampler, nil)
		deviceDriver.DestroyImageView(view, nil)
		deviceDriver.FreeMemory(mem, nil)
		deviceDriver.DestroyImage(img, nil)
		return nil, fmt.Errorf("allocate scene color descriptor: %w", err)
	}

	err = deviceDriver.UpdateDescriptorSets([]core1_0.WriteDescriptorSet{{
		DstSet:         sets[0],
		DstBinding:     0,
		DescriptorType: core1_0.DescriptorTypeCombinedImageSampler,
		ImageInfo: []core1_0.DescriptorImageInfo{{
			Sampler:     sampler,
			ImageView:   view,
			ImageLayout: core1_0.ImageLayoutShaderReadOnlyOptimal,
		}},
	}}, nil)
	if err != nil {
		deviceDriver.DestroySampler(sampler, nil)
		deviceDriver.DestroyImageView(view, nil)
		deviceDriver.FreeMemory(mem, nil)
		deviceDriver.DestroyImage(img, nil)
		return nil, fmt.Errorf("update scene color descriptor: %w", err)
	}

	return &sceneColorTarget{
		image:  img,
		memory: mem,
		texture: &Texture{
			image:         img,
			view:          view,
			sampler:       sampler,
			DescriptorSet: sets[0],
		},
		extent: extent,
	}, nil
}

// destroy releases the target. The Texture is not registered with the renderer's
// texture list, so it is freed here rather than by the sweep in Destroy — it is
// owned by the swapchain's lifetime, not the application's.
func (s *sceneColorTarget) destroy(deviceDriver core1_0.DeviceDriver) {
	if s == nil {
		return
	}
	deviceDriver.DestroySampler(s.texture.sampler, nil)
	deviceDriver.DestroyImageView(s.texture.view, nil)
	deviceDriver.FreeMemory(s.memory, nil)
	deviceDriver.DestroyImage(s.image, nil)
}

// createWaterRenderPass builds the second pass.
//
// Every attachment loads rather than clears: the opaque scene and its depth are
// already there and must survive, since water is composited over one and
// occluded by the other.
func createWaterRenderPass(deviceDriver core1_0.DeviceDriver, imageFormat core1_0.Format, depthFormat core1_0.Format, samples core1_0.SampleCountFlags) (core1_0.RenderPass, error) {
	msaa := samples != core1_0.Samples1

	var attachments []core1_0.AttachmentDescription
	var subpasses []core1_0.SubpassDescription

	depthAttachment := core1_0.AttachmentDescription{
		Format:         depthFormat,
		Samples:        samples,
		LoadOp:         core1_0.AttachmentLoadOpLoad,
		StoreOp:        core1_0.AttachmentStoreOpDontCare,
		StencilLoadOp:  core1_0.AttachmentLoadOpDontCare,
		StencilStoreOp: core1_0.AttachmentStoreOpDontCare,
		InitialLayout:  core1_0.ImageLayoutDepthStencilAttachmentOptimal,
		FinalLayout:    core1_0.ImageLayoutDepthStencilAttachmentOptimal,
	}

	if msaa {
		attachments = []core1_0.AttachmentDescription{
			// 0: the multisample colour the first pass left behind.
			{
				Format:         imageFormat,
				Samples:        samples,
				LoadOp:         core1_0.AttachmentLoadOpLoad,
				StoreOp:        core1_0.AttachmentStoreOpDontCare,
				StencilLoadOp:  core1_0.AttachmentLoadOpDontCare,
				StencilStoreOp: core1_0.AttachmentStoreOpDontCare,
				InitialLayout:  core1_0.ImageLayoutColorAttachmentOptimal,
				FinalLayout:    core1_0.ImageLayoutColorAttachmentOptimal,
			},
			depthAttachment,
			// 2: resolve to the swapchain. This resolves a second time in the
			// frame, which is the price of drawing water at full MSAA rather
			// than aliasing every wave crest against the sky.
			{
				Format:         imageFormat,
				Samples:        core1_0.Samples1,
				LoadOp:         core1_0.AttachmentLoadOpDontCare,
				StoreOp:        core1_0.AttachmentStoreOpStore,
				StencilLoadOp:  core1_0.AttachmentLoadOpDontCare,
				StencilStoreOp: core1_0.AttachmentStoreOpDontCare,
				InitialLayout:  core1_0.ImageLayoutUndefined,
				FinalLayout:    khr_swapchain.ImageLayoutPresentSrc,
			},
		}
		subpasses = []core1_0.SubpassDescription{{
			PipelineBindPoint: core1_0.PipelineBindPointGraphics,
			ColorAttachments: []core1_0.AttachmentReference{
				{Attachment: 0, Layout: core1_0.ImageLayoutColorAttachmentOptimal},
			},
			DepthStencilAttachment: &core1_0.AttachmentReference{
				Attachment: 1, Layout: core1_0.ImageLayoutDepthStencilAttachmentOptimal,
			},
			ResolveAttachments: []core1_0.AttachmentReference{
				{Attachment: 2, Layout: core1_0.ImageLayoutColorAttachmentOptimal},
			},
		}}
	} else {
		attachments = []core1_0.AttachmentDescription{
			// Without MSAA the first pass drew straight into the swapchain, so
			// water blends directly onto it. It arrives in TransferSrc from the
			// copy that fed the refraction source.
			{
				Format:         imageFormat,
				Samples:        core1_0.Samples1,
				LoadOp:         core1_0.AttachmentLoadOpLoad,
				StoreOp:        core1_0.AttachmentStoreOpStore,
				StencilLoadOp:  core1_0.AttachmentLoadOpDontCare,
				StencilStoreOp: core1_0.AttachmentStoreOpDontCare,
				InitialLayout:  core1_0.ImageLayoutTransferSrcOptimal,
				FinalLayout:    khr_swapchain.ImageLayoutPresentSrc,
			},
			depthAttachment,
		}
		subpasses = []core1_0.SubpassDescription{{
			PipelineBindPoint: core1_0.PipelineBindPointGraphics,
			ColorAttachments: []core1_0.AttachmentReference{
				{Attachment: 0, Layout: core1_0.ImageLayoutColorAttachmentOptimal},
			},
			DepthStencilAttachment: &core1_0.AttachmentReference{
				Attachment: 1, Layout: core1_0.ImageLayoutDepthStencilAttachmentOptimal,
			},
		}}
	}

	renderPass, _, err := deviceDriver.CreateRenderPass(nil, core1_0.RenderPassCreateInfo{
		Attachments: attachments,
		Subpasses:   subpasses,
		SubpassDependencies: []core1_0.SubpassDependency{{
			// Wait for the copy that produced the refraction source before any
			// fragment shader here samples it.
			SrcSubpass:    core1_0.SubpassExternal,
			DstSubpass:    0,
			SrcStageMask:  core1_0.PipelineStageTransfer | core1_0.PipelineStageColorAttachmentOutput | core1_0.PipelineStageLateFragmentTests,
			DstStageMask:  core1_0.PipelineStageFragmentShader | core1_0.PipelineStageColorAttachmentOutput | core1_0.PipelineStageEarlyFragmentTests,
			SrcAccessMask: core1_0.AccessTransferWrite | core1_0.AccessColorAttachmentWrite | core1_0.AccessDepthStencilAttachmentWrite,
			DstAccessMask: core1_0.AccessShaderRead | core1_0.AccessColorAttachmentRead | core1_0.AccessColorAttachmentWrite | core1_0.AccessDepthStencilAttachmentRead,
		}},
	})
	if err != nil {
		return core1_0.RenderPass{}, err
	}

	log.Println("Water render pass created")
	return renderPass, nil
}

// createWaterFramebuffers mirrors createFramebuffers for the second pass. The
// attachments are the same images; only the load and store behaviour differs,
// and that lives in the render pass rather than here.
func createWaterFramebuffers(deviceDriver core1_0.DeviceDriver, renderPass core1_0.RenderPass, imageViews []core1_0.ImageView, depthViews []core1_0.ImageView, msaaViews []core1_0.ImageView, extent core1_0.Extent2D) ([]core1_0.Framebuffer, error) {
	framebuffers := make([]core1_0.Framebuffer, len(imageViews))
	for i, view := range imageViews {
		var attachments []core1_0.ImageView
		if msaaViews != nil {
			attachments = []core1_0.ImageView{msaaViews[i], depthViews[i], view}
		} else {
			attachments = []core1_0.ImageView{view, depthViews[i]}
		}
		fb, _, err := deviceDriver.CreateFramebuffer(nil, core1_0.FramebufferCreateInfo{
			RenderPass:  renderPass,
			Attachments: attachments,
			Width:       extent.Width,
			Height:      extent.Height,
			Layers:      1,
		})
		if err != nil {
			return nil, err
		}
		framebuffers[i] = fb
	}
	return framebuffers, nil
}
