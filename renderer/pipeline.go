package renderer

import (
	"log"

	"github.com/vkngwrapper/core/v3/core1_0"
	"github.com/vkngwrapper/extensions/v3/khr_swapchain"
)

// createRenderPass builds a single-subpass render pass. When samples > Samples1,
// it uses 3 attachments (MSAA color, depth, resolve); otherwise 2 (color, depth).
func createRenderPass(deviceDriver core1_0.DeviceDriver, imageFormat core1_0.Format, depthFormat core1_0.Format, samples core1_0.SampleCountFlags) (core1_0.RenderPass, error) {
	msaa := samples != core1_0.Samples1

	var attachments []core1_0.AttachmentDescription
	var subpasses []core1_0.SubpassDescription

	if msaa {
		attachments = []core1_0.AttachmentDescription{
			// Attachment 0: Multisample color (render target)
			//
			// Stored rather than discarded so the water pass can load the
			// opaque scene and blend onto it. Without water in the frame this
			// costs a write of a buffer nobody reads.
			{
				Format:         imageFormat,
				Samples:        samples,
				LoadOp:         core1_0.AttachmentLoadOpClear,
				StoreOp:        core1_0.AttachmentStoreOpStore,
				StencilLoadOp:  core1_0.AttachmentLoadOpDontCare,
				StencilStoreOp: core1_0.AttachmentStoreOpDontCare,
				InitialLayout:  core1_0.ImageLayoutUndefined,
				FinalLayout:    core1_0.ImageLayoutColorAttachmentOptimal,
			},
			// Attachment 1: Depth (multisample)
			//
			// Also stored: the water pass depth-tests against it so a hill in
			// front of a lake still hides the lake.
			{
				Format:         depthFormat,
				Samples:        samples,
				LoadOp:         core1_0.AttachmentLoadOpClear,
				StoreOp:        core1_0.AttachmentStoreOpStore,
				StencilLoadOp:  core1_0.AttachmentLoadOpDontCare,
				StencilStoreOp: core1_0.AttachmentStoreOpDontCare,
				InitialLayout:  core1_0.ImageLayoutUndefined,
				FinalLayout:    core1_0.ImageLayoutDepthStencilAttachmentOptimal,
			},
			// Attachment 2: Resolve / swapchain (single-sample)
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
		subpasses = []core1_0.SubpassDescription{
			{
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
			},
		}
	} else {
		attachments = []core1_0.AttachmentDescription{
			{
				Format:         imageFormat,
				Samples:        core1_0.Samples1,
				LoadOp:         core1_0.AttachmentLoadOpClear,
				StoreOp:        core1_0.AttachmentStoreOpStore,
				StencilLoadOp:  core1_0.AttachmentLoadOpDontCare,
				StencilStoreOp: core1_0.AttachmentStoreOpDontCare,
				InitialLayout:  core1_0.ImageLayoutUndefined,
				FinalLayout:    khr_swapchain.ImageLayoutPresentSrc,
			},
			{
				Format:         depthFormat,
				Samples:        core1_0.Samples1,
				LoadOp:         core1_0.AttachmentLoadOpClear,
				StoreOp:        core1_0.AttachmentStoreOpStore,
				StencilLoadOp:  core1_0.AttachmentLoadOpDontCare,
				StencilStoreOp: core1_0.AttachmentStoreOpDontCare,
				InitialLayout:  core1_0.ImageLayoutUndefined,
				FinalLayout:    core1_0.ImageLayoutDepthStencilAttachmentOptimal,
			},
		}
		subpasses = []core1_0.SubpassDescription{
			{
				PipelineBindPoint: core1_0.PipelineBindPointGraphics,
				ColorAttachments: []core1_0.AttachmentReference{
					{Attachment: 0, Layout: core1_0.ImageLayoutColorAttachmentOptimal},
				},
				DepthStencilAttachment: &core1_0.AttachmentReference{
					Attachment: 1, Layout: core1_0.ImageLayoutDepthStencilAttachmentOptimal,
				},
			},
		}
	}

	renderPass, _, err := deviceDriver.CreateRenderPass(nil, core1_0.RenderPassCreateInfo{
		Attachments: attachments,
		Subpasses:   subpasses,
		SubpassDependencies: []core1_0.SubpassDependency{
			{
				SrcSubpass:    core1_0.SubpassExternal,
				DstSubpass:    0,
				SrcStageMask:  core1_0.PipelineStageColorAttachmentOutput | core1_0.PipelineStageEarlyFragmentTests,
				DstStageMask:  core1_0.PipelineStageColorAttachmentOutput | core1_0.PipelineStageEarlyFragmentTests,
				SrcAccessMask: 0,
				DstAccessMask: core1_0.AccessColorAttachmentWrite | core1_0.AccessDepthStencilAttachmentWrite,
			},
		},
	})
	if err != nil {
		return core1_0.RenderPass{}, err
	}

	log.Println("Render pass created")
	return renderPass, nil
}

// createNonLitPipelineLayout creates a pipeline layout with set 0 = texture sampler only.
// Used by non-lit pipelines (sky, stars, overlay, msdf, ui).
func createNonLitPipelineLayout(deviceDriver core1_0.DeviceDriver, texSetLayout core1_0.DescriptorSetLayout) (core1_0.PipelineLayout, error) {
	layout, _, err := deviceDriver.CreatePipelineLayout(nil, core1_0.PipelineLayoutCreateInfo{
		SetLayouts: []core1_0.DescriptorSetLayout{texSetLayout},
		PushConstantRanges: []core1_0.PushConstantRange{
			{
				StageFlags: core1_0.StageVertex | core1_0.StageFragment,
				Offset:     0,
				Size:       pushConstantSize,
			},
		},
	})
	if err != nil {
		return core1_0.PipelineLayout{}, err
	}
	return layout, nil
}

// createGraphicsPipeline creates the main scene pipeline with depth testing,
// back-face culling, and push constants for per-object MVP + tint + lighting.
// The litPipelineLayout uses: set 0 = texture, set 1 = shadow (UBO + sampler).
func createGraphicsPipeline(deviceDriver core1_0.DeviceDriver, sh ShaderSet, renderPass core1_0.RenderPass, extent core1_0.Extent2D, texSetLayout core1_0.DescriptorSetLayout, shadowSetLayout core1_0.DescriptorSetLayout, samples core1_0.SampleCountFlags, cullMode ...core1_0.CullModeFlags) (core1_0.Pipeline, core1_0.PipelineLayout, error) {
	cull := core1_0.CullModeBack
	if len(cullMode) > 0 {
		cull = cullMode[0]
	}
	// Create shader modules (lit shaders for scene rendering)
	vertModule, _, err := deviceDriver.CreateShaderModule(nil, core1_0.ShaderModuleCreateInfo{
		Code: bytesToUint32Slice(sh.LitVert),
	})
	if err != nil {
		return core1_0.Pipeline{}, core1_0.PipelineLayout{}, err
	}
	defer deviceDriver.DestroyShaderModule(vertModule, nil)

	fragModule, _, err := deviceDriver.CreateShaderModule(nil, core1_0.ShaderModuleCreateInfo{
		Code: bytesToUint32Slice(sh.LitFrag),
	})
	if err != nil {
		return core1_0.Pipeline{}, core1_0.PipelineLayout{}, err
	}
	defer deviceDriver.DestroyShaderModule(fragModule, nil)

	// Lit pipeline layout: set 0 = texture sampler, set 1 = shadow (UBO + sampler)
	pipelineLayout, _, err := deviceDriver.CreatePipelineLayout(nil, core1_0.PipelineLayoutCreateInfo{
		SetLayouts: []core1_0.DescriptorSetLayout{texSetLayout, shadowSetLayout},
		PushConstantRanges: []core1_0.PushConstantRange{
			{
				StageFlags: core1_0.StageVertex | core1_0.StageFragment,
				Offset:     0,
				Size:       pushConstantSize,
			},
		},
	})
	if err != nil {
		return core1_0.Pipeline{}, core1_0.PipelineLayout{}, err
	}

	pipelines, _, err := deviceDriver.CreateGraphicsPipelines(nil, nil, core1_0.GraphicsPipelineCreateInfo{
		Stages: []core1_0.PipelineShaderStageCreateInfo{
			{
				Stage:  core1_0.StageVertex,
				Module: vertModule,
				Name:   "main",
			},
			{
				Stage:  core1_0.StageFragment,
				Module: fragModule,
				Name:   "main",
			},
		},
		VertexInputState: &core1_0.PipelineVertexInputStateCreateInfo{
			VertexBindingDescriptions:   []core1_0.VertexInputBindingDescription{vertexBindingDescription()},
			VertexAttributeDescriptions: vertexAttributeDescriptions(),
		},
		InputAssemblyState: &core1_0.PipelineInputAssemblyStateCreateInfo{
			Topology: core1_0.PrimitiveTopologyTriangleList,
		},
		ViewportState: &core1_0.PipelineViewportStateCreateInfo{
			Viewports: []core1_0.Viewport{
				{
					X:        0,
					Y:        0,
					Width:    float32(extent.Width),
					Height:   float32(extent.Height),
					MinDepth: 0,
					MaxDepth: 1,
				},
			},
			Scissors: []core1_0.Rect2D{
				{
					Offset: core1_0.Offset2D{X: 0, Y: 0},
					Extent: extent,
				},
			},
		},
		RasterizationState: &core1_0.PipelineRasterizationStateCreateInfo{
			PolygonMode: core1_0.PolygonModeFill,
			CullMode:    cull,
			FrontFace:   core1_0.FrontFaceClockwise,
			LineWidth:   1.0,
		},
		MultisampleState: &core1_0.PipelineMultisampleStateCreateInfo{
			RasterizationSamples: samples,
		},
		DepthStencilState: &core1_0.PipelineDepthStencilStateCreateInfo{
			DepthTestEnable:  true,
			DepthWriteEnable: true,
			DepthCompareOp:   core1_0.CompareOpGreater,
		},
		ColorBlendState: &core1_0.PipelineColorBlendStateCreateInfo{
			Attachments: []core1_0.PipelineColorBlendAttachmentState{
				{
					ColorWriteMask: core1_0.ColorComponentRed | core1_0.ColorComponentGreen | core1_0.ColorComponentBlue | core1_0.ColorComponentAlpha,
					BlendEnabled:   false,
				},
			},
		},
		DynamicState: &core1_0.PipelineDynamicStateCreateInfo{
			DynamicStates: []core1_0.DynamicState{
				core1_0.DynamicStateViewport,
				core1_0.DynamicStateScissor,
			},
		},
		Layout:     pipelineLayout,
		RenderPass: renderPass,
		Subpass:    0,
	})
	if err != nil {
		deviceDriver.DestroyPipelineLayout(pipelineLayout, nil)
		return core1_0.Pipeline{}, core1_0.PipelineLayout{}, err
	}

	log.Println("Graphics pipeline created")
	return pipelines[0], pipelineLayout, nil
}

// createTerrainPipeline creates the terrain splat pipeline: same vertex stage,
// vertex format, depth, and culling as the lit pipeline, but a fragment stage
// that blends multiple detail textures by a splat map. Layout: set 0 = terrain
// material (4 samplers), set 1 = shadow.
func createTerrainPipeline(deviceDriver core1_0.DeviceDriver, sh ShaderSet, renderPass core1_0.RenderPass, extent core1_0.Extent2D, terrainSetLayout core1_0.DescriptorSetLayout, shadowSetLayout core1_0.DescriptorSetLayout, samples core1_0.SampleCountFlags) (core1_0.Pipeline, core1_0.PipelineLayout, error) {
	vertModule, _, err := deviceDriver.CreateShaderModule(nil, core1_0.ShaderModuleCreateInfo{
		Code: bytesToUint32Slice(sh.LitVert),
	})
	if err != nil {
		return core1_0.Pipeline{}, core1_0.PipelineLayout{}, err
	}
	defer deviceDriver.DestroyShaderModule(vertModule, nil)

	fragModule, _, err := deviceDriver.CreateShaderModule(nil, core1_0.ShaderModuleCreateInfo{
		Code: bytesToUint32Slice(sh.TerrainFrag),
	})
	if err != nil {
		return core1_0.Pipeline{}, core1_0.PipelineLayout{}, err
	}
	defer deviceDriver.DestroyShaderModule(fragModule, nil)

	pipelineLayout, _, err := deviceDriver.CreatePipelineLayout(nil, core1_0.PipelineLayoutCreateInfo{
		SetLayouts: []core1_0.DescriptorSetLayout{terrainSetLayout, shadowSetLayout},
		PushConstantRanges: []core1_0.PushConstantRange{
			{
				StageFlags: core1_0.StageVertex | core1_0.StageFragment,
				Offset:     0,
				Size:       pushConstantSize,
			},
		},
	})
	if err != nil {
		return core1_0.Pipeline{}, core1_0.PipelineLayout{}, err
	}

	pipelines, _, err := deviceDriver.CreateGraphicsPipelines(nil, nil, core1_0.GraphicsPipelineCreateInfo{
		Stages: []core1_0.PipelineShaderStageCreateInfo{
			{Stage: core1_0.StageVertex, Module: vertModule, Name: "main"},
			{Stage: core1_0.StageFragment, Module: fragModule, Name: "main"},
		},
		VertexInputState: &core1_0.PipelineVertexInputStateCreateInfo{
			VertexBindingDescriptions:   []core1_0.VertexInputBindingDescription{vertexBindingDescription()},
			VertexAttributeDescriptions: vertexAttributeDescriptions(),
		},
		InputAssemblyState: &core1_0.PipelineInputAssemblyStateCreateInfo{
			Topology: core1_0.PrimitiveTopologyTriangleList,
		},
		ViewportState: &core1_0.PipelineViewportStateCreateInfo{
			Viewports: []core1_0.Viewport{{X: 0, Y: 0, Width: float32(extent.Width), Height: float32(extent.Height), MinDepth: 0, MaxDepth: 1}},
			Scissors:  []core1_0.Rect2D{{Offset: core1_0.Offset2D{X: 0, Y: 0}, Extent: extent}},
		},
		RasterizationState: &core1_0.PipelineRasterizationStateCreateInfo{
			PolygonMode: core1_0.PolygonModeFill,
			CullMode:    core1_0.CullModeBack,
			FrontFace:   core1_0.FrontFaceClockwise,
			LineWidth:   1.0,
		},
		MultisampleState: &core1_0.PipelineMultisampleStateCreateInfo{
			RasterizationSamples: samples,
		},
		DepthStencilState: &core1_0.PipelineDepthStencilStateCreateInfo{
			DepthTestEnable:  true,
			DepthWriteEnable: true,
			DepthCompareOp:   core1_0.CompareOpGreater,
		},
		ColorBlendState: &core1_0.PipelineColorBlendStateCreateInfo{
			Attachments: []core1_0.PipelineColorBlendAttachmentState{
				{
					ColorWriteMask: core1_0.ColorComponentRed | core1_0.ColorComponentGreen | core1_0.ColorComponentBlue | core1_0.ColorComponentAlpha,
					BlendEnabled:   false,
				},
			},
		},
		DynamicState: &core1_0.PipelineDynamicStateCreateInfo{
			DynamicStates: []core1_0.DynamicState{core1_0.DynamicStateViewport, core1_0.DynamicStateScissor},
		},
		Layout:     pipelineLayout,
		RenderPass: renderPass,
		Subpass:    0,
	})
	if err != nil {
		deviceDriver.DestroyPipelineLayout(pipelineLayout, nil)
		return core1_0.Pipeline{}, core1_0.PipelineLayout{}, err
	}

	log.Println("Terrain pipeline created")
	return pipelines[0], pipelineLayout, nil
}

// createOverlayPipeline creates a pipeline for HUD/overlay geometry with no
// depth testing and no back-face culling, sharing the same pipeline layout.
func createOverlayPipeline(deviceDriver core1_0.DeviceDriver, sh ShaderSet, renderPass core1_0.RenderPass, pipelineLayout core1_0.PipelineLayout, extent core1_0.Extent2D, samples core1_0.SampleCountFlags) (core1_0.Pipeline, error) {
	vertModule, _, err := deviceDriver.CreateShaderModule(nil, core1_0.ShaderModuleCreateInfo{
		Code: bytesToUint32Slice(sh.MeshVert),
	})
	if err != nil {
		return core1_0.Pipeline{}, err
	}
	defer deviceDriver.DestroyShaderModule(vertModule, nil)

	fragModule, _, err := deviceDriver.CreateShaderModule(nil, core1_0.ShaderModuleCreateInfo{
		Code: bytesToUint32Slice(sh.MeshFrag),
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
		VertexInputState: &core1_0.PipelineVertexInputStateCreateInfo{
			VertexBindingDescriptions:   []core1_0.VertexInputBindingDescription{vertexBindingDescription()},
			VertexAttributeDescriptions: vertexAttributeDescriptions(),
		},
		InputAssemblyState: &core1_0.PipelineInputAssemblyStateCreateInfo{
			Topology: core1_0.PrimitiveTopologyTriangleList,
		},
		ViewportState: &core1_0.PipelineViewportStateCreateInfo{
			Viewports: []core1_0.Viewport{{
				X: 0, Y: 0,
				Width: float32(extent.Width), Height: float32(extent.Height),
				MinDepth: 0, MaxDepth: 1,
			}},
			Scissors: []core1_0.Rect2D{{
				Offset: core1_0.Offset2D{X: 0, Y: 0},
				Extent: extent,
			}},
		},
		RasterizationState: &core1_0.PipelineRasterizationStateCreateInfo{
			PolygonMode: core1_0.PolygonModeFill,
			CullMode:    0, // no culling
			FrontFace:   core1_0.FrontFaceClockwise,
			LineWidth:   1.0,
		},
		MultisampleState: &core1_0.PipelineMultisampleStateCreateInfo{
			RasterizationSamples: samples,
		},
		DepthStencilState: &core1_0.PipelineDepthStencilStateCreateInfo{
			DepthTestEnable:  false,
			DepthWriteEnable: false,
		},
		ColorBlendState: &core1_0.PipelineColorBlendStateCreateInfo{
			Attachments: []core1_0.PipelineColorBlendAttachmentState{{
				ColorWriteMask: core1_0.ColorComponentRed | core1_0.ColorComponentGreen | core1_0.ColorComponentBlue | core1_0.ColorComponentAlpha,
				BlendEnabled:   false,
			}},
		},
		DynamicState: &core1_0.PipelineDynamicStateCreateInfo{
			DynamicStates: []core1_0.DynamicState{
				core1_0.DynamicStateViewport,
				core1_0.DynamicStateScissor,
			},
		},
		Layout:     pipelineLayout,
		RenderPass: renderPass,
		Subpass:    0,
	})
	if err != nil {
		return core1_0.Pipeline{}, err
	}

	log.Println("Overlay pipeline created")
	return pipelines[0], nil
}

// createStarsPipeline creates a pipeline for procedural starfield rendering:
// no vertex input, no depth test, additive blending.
func createStarsPipeline(deviceDriver core1_0.DeviceDriver, sh ShaderSet, renderPass core1_0.RenderPass, pipelineLayout core1_0.PipelineLayout, extent core1_0.Extent2D, samples core1_0.SampleCountFlags) (core1_0.Pipeline, error) {
	vertModule, _, err := deviceDriver.CreateShaderModule(nil, core1_0.ShaderModuleCreateInfo{
		Code: bytesToUint32Slice(sh.StarsVert),
	})
	if err != nil {
		return core1_0.Pipeline{}, err
	}
	defer deviceDriver.DestroyShaderModule(vertModule, nil)

	fragModule, _, err := deviceDriver.CreateShaderModule(nil, core1_0.ShaderModuleCreateInfo{
		Code: bytesToUint32Slice(sh.StarsFrag),
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
		VertexInputState: &core1_0.PipelineVertexInputStateCreateInfo{},
		InputAssemblyState: &core1_0.PipelineInputAssemblyStateCreateInfo{
			Topology: core1_0.PrimitiveTopologyTriangleList,
		},
		ViewportState: &core1_0.PipelineViewportStateCreateInfo{
			Viewports: []core1_0.Viewport{{
				X: 0, Y: 0,
				Width: float32(extent.Width), Height: float32(extent.Height),
				MinDepth: 0, MaxDepth: 1,
			}},
			Scissors: []core1_0.Rect2D{{
				Offset: core1_0.Offset2D{X: 0, Y: 0},
				Extent: extent,
			}},
		},
		RasterizationState: &core1_0.PipelineRasterizationStateCreateInfo{
			PolygonMode: core1_0.PolygonModeFill,
			CullMode:    0, // no culling
			FrontFace:   core1_0.FrontFaceClockwise,
			LineWidth:   1.0,
		},
		MultisampleState: &core1_0.PipelineMultisampleStateCreateInfo{
			RasterizationSamples: samples,
		},
		DepthStencilState: &core1_0.PipelineDepthStencilStateCreateInfo{
			DepthTestEnable:  false,
			DepthWriteEnable: false,
		},
		ColorBlendState: &core1_0.PipelineColorBlendStateCreateInfo{
			Attachments: []core1_0.PipelineColorBlendAttachmentState{{
				ColorWriteMask:      core1_0.ColorComponentRed | core1_0.ColorComponentGreen | core1_0.ColorComponentBlue | core1_0.ColorComponentAlpha,
				BlendEnabled:        true,
				SrcColorBlendFactor: core1_0.BlendFactorOne,
				DstColorBlendFactor: core1_0.BlendFactorOne,
				ColorBlendOp:        core1_0.BlendOpAdd,
				SrcAlphaBlendFactor: core1_0.BlendFactorOne,
				DstAlphaBlendFactor: core1_0.BlendFactorZero,
				AlphaBlendOp:        core1_0.BlendOpAdd,
			}},
		},
		DynamicState: &core1_0.PipelineDynamicStateCreateInfo{
			DynamicStates: []core1_0.DynamicState{
				core1_0.DynamicStateViewport,
				core1_0.DynamicStateScissor,
			},
		},
		Layout:     pipelineLayout,
		RenderPass: renderPass,
		Subpass:    0,
	})
	if err != nil {
		return core1_0.Pipeline{}, err
	}

	log.Println("Stars pipeline created")
	return pipelines[0], nil
}

// createSkyPipeline creates a pipeline for procedural sky dome rendering:
// no vertex input, depth-tested against the far plane, opaque.
func createSkyPipeline(deviceDriver core1_0.DeviceDriver, sh ShaderSet, renderPass core1_0.RenderPass, pipelineLayout core1_0.PipelineLayout, extent core1_0.Extent2D, samples core1_0.SampleCountFlags) (core1_0.Pipeline, error) {
	vertModule, _, err := deviceDriver.CreateShaderModule(nil, core1_0.ShaderModuleCreateInfo{
		Code: bytesToUint32Slice(sh.SkyVert),
	})
	if err != nil {
		return core1_0.Pipeline{}, err
	}
	defer deviceDriver.DestroyShaderModule(vertModule, nil)

	fragModule, _, err := deviceDriver.CreateShaderModule(nil, core1_0.ShaderModuleCreateInfo{
		Code: bytesToUint32Slice(sh.SkyFrag),
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
		VertexInputState: &core1_0.PipelineVertexInputStateCreateInfo{},
		InputAssemblyState: &core1_0.PipelineInputAssemblyStateCreateInfo{
			Topology: core1_0.PrimitiveTopologyTriangleList,
		},
		ViewportState: &core1_0.PipelineViewportStateCreateInfo{
			Viewports: []core1_0.Viewport{{
				X: 0, Y: 0,
				Width: float32(extent.Width), Height: float32(extent.Height),
				MinDepth: 0, MaxDepth: 1,
			}},
			Scissors: []core1_0.Rect2D{{
				Offset: core1_0.Offset2D{X: 0, Y: 0},
				Extent: extent,
			}},
		},
		RasterizationState: &core1_0.PipelineRasterizationStateCreateInfo{
			PolygonMode: core1_0.PolygonModeFill,
			CullMode:    0, // no culling
			FrontFace:   core1_0.FrontFaceClockwise,
			LineWidth:   1.0,
		},
		MultisampleState: &core1_0.PipelineMultisampleStateCreateInfo{
			RasterizationSamples: samples,
		},
		DepthStencilState: &core1_0.PipelineDepthStencilStateCreateInfo{
			// Depth-tested so the sky only shades pixels no geometry
			// reached; see the draw order in recordCommandBuffer.
			DepthTestEnable:  true,
			DepthWriteEnable: false,
			DepthCompareOp:   core1_0.CompareOpGreaterOrEqual,
		},
		ColorBlendState: &core1_0.PipelineColorBlendStateCreateInfo{
			Attachments: []core1_0.PipelineColorBlendAttachmentState{{
				ColorWriteMask: core1_0.ColorComponentRed | core1_0.ColorComponentGreen | core1_0.ColorComponentBlue | core1_0.ColorComponentAlpha,
				BlendEnabled:   false,
			}},
		},
		DynamicState: &core1_0.PipelineDynamicStateCreateInfo{
			DynamicStates: []core1_0.DynamicState{
				core1_0.DynamicStateViewport,
				core1_0.DynamicStateScissor,
			},
		},
		Layout:     pipelineLayout,
		RenderPass: renderPass,
		Subpass:    0,
	})
	if err != nil {
		return core1_0.Pipeline{}, err
	}

	log.Println("Sky pipeline created")
	return pipelines[0], nil
}

// createMSDFPipeline creates a pipeline for MSDF text rendering: no depth test,
// no culling, alpha blending enabled, using msdf.vert + msdf.frag shaders.
func createMSDFPipeline(deviceDriver core1_0.DeviceDriver, sh ShaderSet, renderPass core1_0.RenderPass, pipelineLayout core1_0.PipelineLayout, extent core1_0.Extent2D, samples core1_0.SampleCountFlags) (core1_0.Pipeline, error) {
	vertModule, _, err := deviceDriver.CreateShaderModule(nil, core1_0.ShaderModuleCreateInfo{
		Code: bytesToUint32Slice(sh.MsdfVert),
	})
	if err != nil {
		return core1_0.Pipeline{}, err
	}
	defer deviceDriver.DestroyShaderModule(vertModule, nil)

	fragModule, _, err := deviceDriver.CreateShaderModule(nil, core1_0.ShaderModuleCreateInfo{
		Code: bytesToUint32Slice(sh.MsdfFrag),
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
		VertexInputState: &core1_0.PipelineVertexInputStateCreateInfo{
			VertexBindingDescriptions:   []core1_0.VertexInputBindingDescription{vertexBindingDescription()},
			VertexAttributeDescriptions: vertexAttributeDescriptions(),
		},
		InputAssemblyState: &core1_0.PipelineInputAssemblyStateCreateInfo{
			Topology: core1_0.PrimitiveTopologyTriangleList,
		},
		ViewportState: &core1_0.PipelineViewportStateCreateInfo{
			Viewports: []core1_0.Viewport{{
				X: 0, Y: 0,
				Width: float32(extent.Width), Height: float32(extent.Height),
				MinDepth: 0, MaxDepth: 1,
			}},
			Scissors: []core1_0.Rect2D{{
				Offset: core1_0.Offset2D{X: 0, Y: 0},
				Extent: extent,
			}},
		},
		RasterizationState: &core1_0.PipelineRasterizationStateCreateInfo{
			PolygonMode: core1_0.PolygonModeFill,
			CullMode:    0, // no culling
			FrontFace:   core1_0.FrontFaceClockwise,
			LineWidth:   1.0,
		},
		MultisampleState: &core1_0.PipelineMultisampleStateCreateInfo{
			RasterizationSamples: samples,
		},
		DepthStencilState: &core1_0.PipelineDepthStencilStateCreateInfo{
			DepthTestEnable:  false,
			DepthWriteEnable: false,
		},
		ColorBlendState: &core1_0.PipelineColorBlendStateCreateInfo{
			Attachments: []core1_0.PipelineColorBlendAttachmentState{{
				ColorWriteMask:      core1_0.ColorComponentRed | core1_0.ColorComponentGreen | core1_0.ColorComponentBlue | core1_0.ColorComponentAlpha,
				BlendEnabled:        true,
				SrcColorBlendFactor: core1_0.BlendFactorSrcAlpha,
				DstColorBlendFactor: core1_0.BlendFactorOneMinusSrcAlpha,
				ColorBlendOp:        core1_0.BlendOpAdd,
				SrcAlphaBlendFactor: core1_0.BlendFactorOne,
				DstAlphaBlendFactor: core1_0.BlendFactorOneMinusSrcAlpha,
				AlphaBlendOp:        core1_0.BlendOpAdd,
			}},
		},
		DynamicState: &core1_0.PipelineDynamicStateCreateInfo{
			DynamicStates: []core1_0.DynamicState{
				core1_0.DynamicStateViewport,
				core1_0.DynamicStateScissor,
			},
		},
		Layout:     pipelineLayout,
		RenderPass: renderPass,
		Subpass:    0,
	})
	if err != nil {
		return core1_0.Pipeline{}, err
	}

	log.Println("MSDF pipeline created")
	return pipelines[0], nil
}

// createGrassPipeline creates a pipeline for instanced grass rendering: two-sided,
// depth tested, using grass.vert + grass.frag with alpha-to-coverage so distant
// blades dissolve via MSAA coverage. Two vertex bindings: binding 0 = Vertex
// (per-vertex), binding 1 = GrassInstance (per-instance).
func createGrassPipeline(deviceDriver core1_0.DeviceDriver, sh ShaderSet, renderPass core1_0.RenderPass, litPipelineLayout core1_0.PipelineLayout, extent core1_0.Extent2D, samples core1_0.SampleCountFlags) (core1_0.Pipeline, error) {
	vertModule, _, err := deviceDriver.CreateShaderModule(nil, core1_0.ShaderModuleCreateInfo{
		Code: bytesToUint32Slice(sh.GrassVert),
	})
	if err != nil {
		return core1_0.Pipeline{}, err
	}
	defer deviceDriver.DestroyShaderModule(vertModule, nil)

	fragModule, _, err := deviceDriver.CreateShaderModule(nil, core1_0.ShaderModuleCreateInfo{
		Code: bytesToUint32Slice(sh.GrassFrag),
	})
	if err != nil {
		return core1_0.Pipeline{}, err
	}
	defer deviceDriver.DestroyShaderModule(fragModule, nil)

	// Two vertex bindings: 0 = per-vertex Vertex, 1 = per-instance GrassInstance
	bindings := []core1_0.VertexInputBindingDescription{
		vertexBindingDescription(), // binding 0, stride 44, per-vertex
		{
			Binding:   1,
			Stride:    16, // sizeof(GrassInstance)
			InputRate: core1_0.VertexInputRateInstance,
		},
	}

	// Locations 0-3 from standard vertex + location 4 from instance data
	attrs := append(vertexAttributeDescriptions(), core1_0.VertexInputAttributeDescription{
		Location: 4,
		Binding:  1,
		Format:   core1_0.FormatR32G32B32A32SignedFloat,
		Offset:   0,
	})

	pipelines, _, err := deviceDriver.CreateGraphicsPipelines(nil, nil, core1_0.GraphicsPipelineCreateInfo{
		Stages: []core1_0.PipelineShaderStageCreateInfo{
			{Stage: core1_0.StageVertex, Module: vertModule, Name: "main"},
			{Stage: core1_0.StageFragment, Module: fragModule, Name: "main"},
		},
		VertexInputState: &core1_0.PipelineVertexInputStateCreateInfo{
			VertexBindingDescriptions:   bindings,
			VertexAttributeDescriptions: attrs,
		},
		InputAssemblyState: &core1_0.PipelineInputAssemblyStateCreateInfo{
			Topology: core1_0.PrimitiveTopologyTriangleList,
		},
		ViewportState: &core1_0.PipelineViewportStateCreateInfo{
			Viewports: []core1_0.Viewport{{
				X: 0, Y: 0,
				Width: float32(extent.Width), Height: float32(extent.Height),
				MinDepth: 0, MaxDepth: 1,
			}},
			Scissors: []core1_0.Rect2D{{
				Offset: core1_0.Offset2D{X: 0, Y: 0},
				Extent: extent,
			}},
		},
		RasterizationState: &core1_0.PipelineRasterizationStateCreateInfo{
			PolygonMode: core1_0.PolygonModeFill,
			CullMode:    0, // no culling — grass is two-sided
			FrontFace:   core1_0.FrontFaceClockwise,
			LineWidth:   1.0,
		},
		MultisampleState: &core1_0.PipelineMultisampleStateCreateInfo{
			RasterizationSamples: samples,
			// Fragment alpha carries the distance fade; coverage dissolve
			// replaces shimmering sub-pixel blades at distance.
			AlphaToCoverageEnable: samples != core1_0.Samples1,
		},
		DepthStencilState: &core1_0.PipelineDepthStencilStateCreateInfo{
			DepthTestEnable:  true,
			DepthWriteEnable: true,
			DepthCompareOp:   core1_0.CompareOpGreater,
		},
		ColorBlendState: &core1_0.PipelineColorBlendStateCreateInfo{
			Attachments: []core1_0.PipelineColorBlendAttachmentState{{
				ColorWriteMask: core1_0.ColorComponentRed | core1_0.ColorComponentGreen | core1_0.ColorComponentBlue | core1_0.ColorComponentAlpha,
				BlendEnabled:   false,
			}},
		},
		DynamicState: &core1_0.PipelineDynamicStateCreateInfo{
			DynamicStates: []core1_0.DynamicState{
				core1_0.DynamicStateViewport,
				core1_0.DynamicStateScissor,
			},
		},
		Layout:     litPipelineLayout,
		RenderPass: renderPass,
		Subpass:    0,
	})
	if err != nil {
		return core1_0.Pipeline{}, err
	}

	log.Println("Grass pipeline created")
	return pipelines[0], nil
}

// createParticlePipeline creates a pipeline for instanced billboard particles:
// additive blend, depth test ON / write OFF, no culling.
// Two vertex bindings: binding 0 = Vertex (per-vertex quad), binding 1 = ParticleInstance (per-instance).
func createParticlePipeline(deviceDriver core1_0.DeviceDriver, sh ShaderSet, renderPass core1_0.RenderPass, pipelineLayout core1_0.PipelineLayout, extent core1_0.Extent2D, samples core1_0.SampleCountFlags) (core1_0.Pipeline, error) {
	vertModule, _, err := deviceDriver.CreateShaderModule(nil, core1_0.ShaderModuleCreateInfo{
		Code: bytesToUint32Slice(sh.ParticleVert),
	})
	if err != nil {
		return core1_0.Pipeline{}, err
	}
	defer deviceDriver.DestroyShaderModule(vertModule, nil)

	fragModule, _, err := deviceDriver.CreateShaderModule(nil, core1_0.ShaderModuleCreateInfo{
		Code: bytesToUint32Slice(sh.ParticleFrag),
	})
	if err != nil {
		return core1_0.Pipeline{}, err
	}
	defer deviceDriver.DestroyShaderModule(fragModule, nil)

	// Two vertex bindings: 0 = per-vertex Vertex (44B), 1 = per-instance ParticleInstance (32B)
	bindings := []core1_0.VertexInputBindingDescription{
		vertexBindingDescription(), // binding 0, stride 44, per-vertex
		{
			Binding:   1,
			Stride:    32, // sizeof(ParticleInstance)
			InputRate: core1_0.VertexInputRateInstance,
		},
	}

	// Standard vertex attrs (location 0-3) + instance attrs at location 4-5 (binding 1)
	attrs := append(vertexAttributeDescriptions(),
		core1_0.VertexInputAttributeDescription{
			Location: 4, // used as location 2 in shader but we map it here
			Binding:  1,
			Format:   core1_0.FormatR32G32B32A32SignedFloat,
			Offset:   0, // posSize
		},
		core1_0.VertexInputAttributeDescription{
			Location: 5,
			Binding:  1,
			Format:   core1_0.FormatR32G32B32A32SignedFloat,
			Offset:   16, // color
		},
	)

	pipelines, _, err := deviceDriver.CreateGraphicsPipelines(nil, nil, core1_0.GraphicsPipelineCreateInfo{
		Stages: []core1_0.PipelineShaderStageCreateInfo{
			{Stage: core1_0.StageVertex, Module: vertModule, Name: "main"},
			{Stage: core1_0.StageFragment, Module: fragModule, Name: "main"},
		},
		VertexInputState: &core1_0.PipelineVertexInputStateCreateInfo{
			VertexBindingDescriptions:   bindings,
			VertexAttributeDescriptions: attrs,
		},
		InputAssemblyState: &core1_0.PipelineInputAssemblyStateCreateInfo{
			Topology: core1_0.PrimitiveTopologyTriangleList,
		},
		ViewportState: &core1_0.PipelineViewportStateCreateInfo{
			Viewports: []core1_0.Viewport{{
				X: 0, Y: 0,
				Width: float32(extent.Width), Height: float32(extent.Height),
				MinDepth: 0, MaxDepth: 1,
			}},
			Scissors: []core1_0.Rect2D{{
				Offset: core1_0.Offset2D{X: 0, Y: 0},
				Extent: extent,
			}},
		},
		RasterizationState: &core1_0.PipelineRasterizationStateCreateInfo{
			PolygonMode: core1_0.PolygonModeFill,
			CullMode:    0, // no culling
			FrontFace:   core1_0.FrontFaceClockwise,
			LineWidth:   1.0,
		},
		MultisampleState: &core1_0.PipelineMultisampleStateCreateInfo{
			RasterizationSamples: samples,
		},
		DepthStencilState: &core1_0.PipelineDepthStencilStateCreateInfo{
			DepthTestEnable:  true,
			DepthWriteEnable: false, // particles don't write depth
			DepthCompareOp:   core1_0.CompareOpGreater,
		},
		ColorBlendState: &core1_0.PipelineColorBlendStateCreateInfo{
			Attachments: []core1_0.PipelineColorBlendAttachmentState{{
				ColorWriteMask:      core1_0.ColorComponentRed | core1_0.ColorComponentGreen | core1_0.ColorComponentBlue | core1_0.ColorComponentAlpha,
				BlendEnabled:        true,
				SrcColorBlendFactor: core1_0.BlendFactorSrcAlpha,
				DstColorBlendFactor: core1_0.BlendFactorOne, // additive
				ColorBlendOp:        core1_0.BlendOpAdd,
				SrcAlphaBlendFactor: core1_0.BlendFactorOne,
				DstAlphaBlendFactor: core1_0.BlendFactorZero,
				AlphaBlendOp:        core1_0.BlendOpAdd,
			}},
		},
		DynamicState: &core1_0.PipelineDynamicStateCreateInfo{
			DynamicStates: []core1_0.DynamicState{
				core1_0.DynamicStateViewport,
				core1_0.DynamicStateScissor,
			},
		},
		Layout:     pipelineLayout,
		RenderPass: renderPass,
		Subpass:    0,
	})
	if err != nil {
		return core1_0.Pipeline{}, err
	}

	log.Println("Particle pipeline created")
	return pipelines[0], nil
}

// createSkinnedPipelineLayout creates a pipeline layout for skinned meshes with three
// descriptor set layouts: set 0 = texture sampler, set 1 = joint matrices UBO, set 2 = shadow.
func createSkinnedPipelineLayout(deviceDriver core1_0.DeviceDriver, texSetLayout, jointSetLayout, shadowSetLayout core1_0.DescriptorSetLayout) (core1_0.PipelineLayout, error) {
	layout, _, err := deviceDriver.CreatePipelineLayout(nil, core1_0.PipelineLayoutCreateInfo{
		SetLayouts: []core1_0.DescriptorSetLayout{texSetLayout, jointSetLayout, shadowSetLayout},
		PushConstantRanges: []core1_0.PushConstantRange{
			{
				StageFlags: core1_0.StageVertex | core1_0.StageFragment,
				Offset:     0,
				Size:       pushConstantSize,
			},
		},
	})
	if err != nil {
		return core1_0.PipelineLayout{}, err
	}
	return layout, nil
}

// createSkinnedPipeline creates the pipeline for skinned meshes: same as the main
// lit pipeline but using the skinned vertex shader, skinned vertex input layout,
// and skinned_lit.frag (shadow sampler at set 2 instead of set 1).
func createSkinnedPipeline(deviceDriver core1_0.DeviceDriver, sh ShaderSet, renderPass core1_0.RenderPass, skinnedPipelineLayout core1_0.PipelineLayout, extent core1_0.Extent2D, samples core1_0.SampleCountFlags) (core1_0.Pipeline, error) {
	vertModule, _, err := deviceDriver.CreateShaderModule(nil, core1_0.ShaderModuleCreateInfo{
		Code: bytesToUint32Slice(sh.SkinnedLitVert),
	})
	if err != nil {
		return core1_0.Pipeline{}, err
	}
	defer deviceDriver.DestroyShaderModule(vertModule, nil)

	fragModule, _, err := deviceDriver.CreateShaderModule(nil, core1_0.ShaderModuleCreateInfo{
		Code: bytesToUint32Slice(sh.SkinnedLitFrag),
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
		VertexInputState: &core1_0.PipelineVertexInputStateCreateInfo{
			VertexBindingDescriptions:   []core1_0.VertexInputBindingDescription{skinnedVertexBindingDescription()},
			VertexAttributeDescriptions: skinnedVertexAttributeDescriptions(),
		},
		InputAssemblyState: &core1_0.PipelineInputAssemblyStateCreateInfo{
			Topology: core1_0.PrimitiveTopologyTriangleList,
		},
		ViewportState: &core1_0.PipelineViewportStateCreateInfo{
			Viewports: []core1_0.Viewport{{
				X: 0, Y: 0,
				Width: float32(extent.Width), Height: float32(extent.Height),
				MinDepth: 0, MaxDepth: 1,
			}},
			Scissors: []core1_0.Rect2D{{
				Offset: core1_0.Offset2D{X: 0, Y: 0},
				Extent: extent,
			}},
		},
		RasterizationState: &core1_0.PipelineRasterizationStateCreateInfo{
			PolygonMode: core1_0.PolygonModeFill,
			CullMode:    core1_0.CullModeBack,
			FrontFace:   core1_0.FrontFaceClockwise,
			LineWidth:   1.0,
		},
		MultisampleState: &core1_0.PipelineMultisampleStateCreateInfo{
			RasterizationSamples: samples,
		},
		DepthStencilState: &core1_0.PipelineDepthStencilStateCreateInfo{
			DepthTestEnable:  true,
			DepthWriteEnable: true,
			DepthCompareOp:   core1_0.CompareOpGreater,
		},
		ColorBlendState: &core1_0.PipelineColorBlendStateCreateInfo{
			Attachments: []core1_0.PipelineColorBlendAttachmentState{{
				ColorWriteMask: core1_0.ColorComponentRed | core1_0.ColorComponentGreen | core1_0.ColorComponentBlue | core1_0.ColorComponentAlpha,
				BlendEnabled:   false,
			}},
		},
		DynamicState: &core1_0.PipelineDynamicStateCreateInfo{
			DynamicStates: []core1_0.DynamicState{
				core1_0.DynamicStateViewport,
				core1_0.DynamicStateScissor,
			},
		},
		Layout:     skinnedPipelineLayout,
		RenderPass: renderPass,
		Subpass:    0,
	})
	if err != nil {
		return core1_0.Pipeline{}, err
	}

	log.Println("Skinned pipeline created")
	return pipelines[0], nil
}

// createUIPipeline creates a pipeline for textured UI panels: no depth test,
// no culling, alpha blending enabled, using ui.vert + ui.frag shaders.
func createUIPipeline(deviceDriver core1_0.DeviceDriver, sh ShaderSet, renderPass core1_0.RenderPass, pipelineLayout core1_0.PipelineLayout, extent core1_0.Extent2D, samples core1_0.SampleCountFlags) (core1_0.Pipeline, error) {
	vertModule, _, err := deviceDriver.CreateShaderModule(nil, core1_0.ShaderModuleCreateInfo{
		Code: bytesToUint32Slice(sh.UIVert),
	})
	if err != nil {
		return core1_0.Pipeline{}, err
	}
	defer deviceDriver.DestroyShaderModule(vertModule, nil)

	fragModule, _, err := deviceDriver.CreateShaderModule(nil, core1_0.ShaderModuleCreateInfo{
		Code: bytesToUint32Slice(sh.UIFrag),
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
		VertexInputState: &core1_0.PipelineVertexInputStateCreateInfo{
			VertexBindingDescriptions:   []core1_0.VertexInputBindingDescription{vertexBindingDescription()},
			VertexAttributeDescriptions: vertexAttributeDescriptions(),
		},
		InputAssemblyState: &core1_0.PipelineInputAssemblyStateCreateInfo{
			Topology: core1_0.PrimitiveTopologyTriangleList,
		},
		ViewportState: &core1_0.PipelineViewportStateCreateInfo{
			Viewports: []core1_0.Viewport{{
				X: 0, Y: 0,
				Width: float32(extent.Width), Height: float32(extent.Height),
				MinDepth: 0, MaxDepth: 1,
			}},
			Scissors: []core1_0.Rect2D{{
				Offset: core1_0.Offset2D{X: 0, Y: 0},
				Extent: extent,
			}},
		},
		RasterizationState: &core1_0.PipelineRasterizationStateCreateInfo{
			PolygonMode: core1_0.PolygonModeFill,
			CullMode:    0, // no culling
			FrontFace:   core1_0.FrontFaceClockwise,
			LineWidth:   1.0,
		},
		MultisampleState: &core1_0.PipelineMultisampleStateCreateInfo{
			RasterizationSamples: samples,
		},
		DepthStencilState: &core1_0.PipelineDepthStencilStateCreateInfo{
			DepthTestEnable:  false,
			DepthWriteEnable: false,
		},
		ColorBlendState: &core1_0.PipelineColorBlendStateCreateInfo{
			Attachments: []core1_0.PipelineColorBlendAttachmentState{{
				ColorWriteMask:      core1_0.ColorComponentRed | core1_0.ColorComponentGreen | core1_0.ColorComponentBlue | core1_0.ColorComponentAlpha,
				BlendEnabled:        true,
				SrcColorBlendFactor: core1_0.BlendFactorSrcAlpha,
				DstColorBlendFactor: core1_0.BlendFactorOneMinusSrcAlpha,
				ColorBlendOp:        core1_0.BlendOpAdd,
				SrcAlphaBlendFactor: core1_0.BlendFactorOne,
				DstAlphaBlendFactor: core1_0.BlendFactorOneMinusSrcAlpha,
				AlphaBlendOp:        core1_0.BlendOpAdd,
			}},
		},
		DynamicState: &core1_0.PipelineDynamicStateCreateInfo{
			DynamicStates: []core1_0.DynamicState{
				core1_0.DynamicStateViewport,
				core1_0.DynamicStateScissor,
			},
		},
		Layout:     pipelineLayout,
		RenderPass: renderPass,
		Subpass:    0,
	})
	if err != nil {
		return core1_0.Pipeline{}, err
	}

	log.Println("UI pipeline created")
	return pipelines[0], nil
}

// createWaterPipeline creates the pipeline for animated water surfaces.
//
// It reuses the lit pipeline layout, so the water shader gets the shadow
// cascades and light buffer for free, and set 0 carries the scene colour it
// refracts through instead of a material texture.
//
// Depth is tested but not written. Water is the last opaque-ish thing drawn and
// nothing needs to depth-test against it, while writing would make two water
// fragments at the same depth fight over which wave crest wins.
//
// Blending stays enabled even when the shader composites the refracted scene
// itself: the surface still fades out at the shoreline, and that fade has to
// reach the framebuffer somehow.
func createWaterPipeline(deviceDriver core1_0.DeviceDriver, sh ShaderSet, renderPass core1_0.RenderPass, litPipelineLayout core1_0.PipelineLayout, extent core1_0.Extent2D, samples core1_0.SampleCountFlags) (core1_0.Pipeline, error) {
	vertModule, _, err := deviceDriver.CreateShaderModule(nil, core1_0.ShaderModuleCreateInfo{
		Code: bytesToUint32Slice(sh.WaterVert),
	})
	if err != nil {
		return core1_0.Pipeline{}, err
	}
	defer deviceDriver.DestroyShaderModule(vertModule, nil)

	fragModule, _, err := deviceDriver.CreateShaderModule(nil, core1_0.ShaderModuleCreateInfo{
		Code: bytesToUint32Slice(sh.WaterFrag),
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
		VertexInputState: &core1_0.PipelineVertexInputStateCreateInfo{
			VertexBindingDescriptions:   []core1_0.VertexInputBindingDescription{vertexBindingDescription()},
			VertexAttributeDescriptions: vertexAttributeDescriptions(),
		},
		InputAssemblyState: &core1_0.PipelineInputAssemblyStateCreateInfo{
			Topology: core1_0.PrimitiveTopologyTriangleList,
		},
		ViewportState: &core1_0.PipelineViewportStateCreateInfo{
			Viewports: []core1_0.Viewport{{
				X: 0, Y: 0,
				Width: float32(extent.Width), Height: float32(extent.Height),
				MinDepth: 0, MaxDepth: 1,
			}},
			Scissors: []core1_0.Rect2D{{
				Offset: core1_0.Offset2D{X: 0, Y: 0},
				Extent: extent,
			}},
		},
		RasterizationState: &core1_0.PipelineRasterizationStateCreateInfo{
			PolygonMode: core1_0.PolygonModeFill,
			// Two-sided: waves tilt steeply enough that a crest seen from a low
			// camera can present its back face, and a hole in the lake is far
			// more obvious than the cost of drawing both sides.
			CullMode:  0,
			FrontFace: core1_0.FrontFaceClockwise,
			LineWidth: 1.0,
		},
		MultisampleState: &core1_0.PipelineMultisampleStateCreateInfo{
			RasterizationSamples: samples,
		},
		DepthStencilState: &core1_0.PipelineDepthStencilStateCreateInfo{
			DepthTestEnable:  true,
			DepthWriteEnable: false,
			DepthCompareOp:   core1_0.CompareOpGreater,
		},
		ColorBlendState: &core1_0.PipelineColorBlendStateCreateInfo{
			Attachments: []core1_0.PipelineColorBlendAttachmentState{{
				ColorWriteMask:      core1_0.ColorComponentRed | core1_0.ColorComponentGreen | core1_0.ColorComponentBlue | core1_0.ColorComponentAlpha,
				BlendEnabled:        true,
				SrcColorBlendFactor: core1_0.BlendFactorSrcAlpha,
				DstColorBlendFactor: core1_0.BlendFactorOneMinusSrcAlpha,
				ColorBlendOp:        core1_0.BlendOpAdd,
				SrcAlphaBlendFactor: core1_0.BlendFactorOne,
				DstAlphaBlendFactor: core1_0.BlendFactorZero,
				AlphaBlendOp:        core1_0.BlendOpAdd,
			}},
		},
		DynamicState: &core1_0.PipelineDynamicStateCreateInfo{
			DynamicStates: []core1_0.DynamicState{
				core1_0.DynamicStateViewport,
				core1_0.DynamicStateScissor,
			},
		},
		Layout:     litPipelineLayout,
		RenderPass: renderPass,
		Subpass:    0,
	})
	if err != nil {
		return core1_0.Pipeline{}, err
	}

	log.Println("Water pipeline created")
	return pipelines[0], nil
}
