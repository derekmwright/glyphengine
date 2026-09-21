package renderer

import (
	"log"

	"github.com/vkngwrapper/core/v3/core1_0"
)

// sceneEntryDependency is the external-to-subpass-0 dependency BOTH scene-sized
// render passes declare, and they have to declare the same one.
//
// Render pass compatibility, which is what lets a pipeline created against one
// pass be bound inside the other, allows the two to differ only in load and
// store operations and in image layouts (Vulkan spec, Render Pass
// Compatibility). Subpass dependencies are not on that list, so two passes with
// different dependencies are incompatible however identical their attachments
// are. The validation layer says so in as many words:
//
//	VUID-vkCmdDrawIndexed-renderPass-02684: RenderPasses incompatible ...
//	First srcStageMask is ...TRANSFER..., but second srcStageMask is ...
//
// which is how the blended draws that now run inside the water pass were caught
// the first time they were recorded there. The alternative was a second copy of
// five pipelines built against the water pass to say the same thing.
//
// The masks are the union of what the two passes separately needed: the scene
// pass's colour and depth attachment writes, and the water pass's wait on the
// transfer that produced its refraction source. A union is safe in the
// direction that matters — each pass still waits for everything it used to, and
// waiting for a little more at the top of a pass is not measurable here.
func sceneEntryDependency() core1_0.SubpassDependency {
	return core1_0.SubpassDependency{
		SrcSubpass: core1_0.SubpassExternal,
		DstSubpass: 0,
		SrcStageMask: core1_0.PipelineStageTransfer | core1_0.PipelineStageColorAttachmentOutput |
			core1_0.PipelineStageEarlyFragmentTests | core1_0.PipelineStageLateFragmentTests,
		DstStageMask: core1_0.PipelineStageFragmentShader | core1_0.PipelineStageColorAttachmentOutput |
			core1_0.PipelineStageEarlyFragmentTests,
		SrcAccessMask: core1_0.AccessTransferWrite | core1_0.AccessColorAttachmentWrite |
			core1_0.AccessDepthStencilAttachmentWrite,
		DstAccessMask: core1_0.AccessShaderRead | core1_0.AccessColorAttachmentRead |
			core1_0.AccessColorAttachmentWrite | core1_0.AccessDepthStencilAttachmentRead |
			core1_0.AccessDepthStencilAttachmentWrite,
	}
}

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
				FinalLayout:    core1_0.ImageLayoutShaderReadOnlyOptimal,
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
				FinalLayout:    core1_0.ImageLayoutShaderReadOnlyOptimal,
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
		Attachments:         attachments,
		Subpasses:           subpasses,
		SubpassDependencies: []core1_0.SubpassDependency{sceneEntryDependency()},
	})
	if err != nil {
		return core1_0.RenderPass{}, err
	}

	log.Println("Render pass created")
	return renderPass, nil
}

// farPlaneDepthState is the depth configuration for everything that draws at the
// far plane after all opaque geometry: the sky dome and the starfield.
//
// Both are the fullscreen triangle from sky.vert at NDC z = 0. Depth is
// reversed, so 0 is the far plane and the test has to be GreaterOrEqual —
// Greater would reject them everywhere, since a cleared depth buffer holds
// exactly 0. Neither writes depth: they are backdrops, and the layers after
// them still need to see the world's depth rather than the sky's.
//
// Shared rather than written twice because it drifted: the starfield was created
// with no depth test at all, so it painted over the silhouette of any terrain
// that reached up into the sky. Two copies of a subtle rule is one copy too many.
func farPlaneDepthState() *core1_0.PipelineDepthStencilStateCreateInfo {
	return &core1_0.PipelineDepthStencilStateCreateInfo{
		DepthTestEnable:  true,
		DepthWriteEnable: false,
		DepthCompareOp:   core1_0.CompareOpGreaterOrEqual,
	}
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

// createSkyPipelineLayout is the non-lit layout plus the shadow/light set at
// set 1, shared by regular and volumetric sky pipelines.
//
// skyvolumetric.frag marches the froxel grid for in-scattering, so it needs lights.inc's
// three storage buffers -- which live at bindings 3, 4 and 5 of the set the
// shadow data owns, and only there. Three ways to give it those, and this is
// the least invasive of them:
// Custom SkyFrag can also sample the directional map from this same set.
//
//   - Add the bindings to the shared texture set layout at set 0. That layout
//     backs EVERY texture in the engine, so it would put three storage-buffer
//     bindings on every material a game ever uploads, and every one of those
//     descriptor sets would have to be written with the light buffers or be
//     incomplete.
//   - Give the sky its own second set with copies of the buffers. A second
//     descriptor naming the same buffers is a second thing to update on a
//     resize and a second place for a frame in flight to read the wrong one.
//   - Bind the set that already holds them, at the index the lit pipelines do
//     not use for the sky's set 0. That is this.
//
// Set 0 stays the texture layout and the push constant range is unchanged, so
// this layout is COMPATIBLE with the non-lit one for set 0 and for push
// constants: the stars and celestial draws that follow the sky rebind set 0
// with the non-lit layout and are unaffected.
func createSkyPipelineLayout(deviceDriver core1_0.DeviceDriver, texSetLayout, shadowSetLayout core1_0.DescriptorSetLayout) (core1_0.PipelineLayout, error) {
	layout, _, err := deviceDriver.CreatePipelineLayout(nil, core1_0.PipelineLayoutCreateInfo{
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
		return core1_0.PipelineLayout{}, err
	}
	return layout, nil
}

// createLitVariantPipeline builds one of the pipelines that share lit.vert: the
// same vertex format, depth state, blend state, and shadow set, differing only
// in the fragment stage and in what set 0 binds.
//
// The plain lit, terrain-splat, and material pipelines were three copies of this
// function that had to agree on reverse-Z, culling, and the push constant range.
// They are one pipeline with a different material concept plugged into set 0, and
// a fourth copy is how one of them quietly ends up with the wrong compare op.
func createLitVariantPipeline(deviceDriver core1_0.DeviceDriver, vertSpv, fragSpv []byte, label string, renderPass core1_0.RenderPass, extent core1_0.Extent2D, set0Layout, shadowSetLayout core1_0.DescriptorSetLayout, samples core1_0.SampleCountFlags, cull core1_0.CullModeFlags, blend bool) (core1_0.Pipeline, core1_0.PipelineLayout, error) {
	return createLitVariantPipelineWithInput(deviceDriver, vertSpv, fragSpv, label, renderPass, extent,
		set0Layout, shadowSetLayout, samples, cull, blend,
		[]core1_0.VertexInputBindingDescription{vertexBindingDescription()}, vertexAttributeDescriptions())
}

// createLitVariantPipelineWithInput is createLitVariantPipeline with the vertex
// input state supplied, which is what the instanced variant needs: a second
// per-instance binding carrying the model matrix the ordinary path pushes.
func createLitVariantPipelineWithInput(deviceDriver core1_0.DeviceDriver, vertSpv, fragSpv []byte, label string, renderPass core1_0.RenderPass, extent core1_0.Extent2D, set0Layout, shadowSetLayout core1_0.DescriptorSetLayout, samples core1_0.SampleCountFlags, cull core1_0.CullModeFlags, blend bool, bindings []core1_0.VertexInputBindingDescription, attrs []core1_0.VertexInputAttributeDescription) (core1_0.Pipeline, core1_0.PipelineLayout, error) {
	vertModule, _, err := deviceDriver.CreateShaderModule(nil, core1_0.ShaderModuleCreateInfo{
		Code: bytesToUint32Slice(vertSpv),
	})
	if err != nil {
		return core1_0.Pipeline{}, core1_0.PipelineLayout{}, err
	}
	defer deviceDriver.DestroyShaderModule(vertModule, nil)

	fragModule, _, err := deviceDriver.CreateShaderModule(nil, core1_0.ShaderModuleCreateInfo{
		Code: bytesToUint32Slice(fragSpv),
	})
	if err != nil {
		return core1_0.Pipeline{}, core1_0.PipelineLayout{}, err
	}
	defer deviceDriver.DestroyShaderModule(fragModule, nil)

	// set 0 = this variant's material, set 1 = shadow (UBO + samplers)
	pipelineLayout, _, err := deviceDriver.CreatePipelineLayout(nil, core1_0.PipelineLayoutCreateInfo{
		SetLayouts: []core1_0.DescriptorSetLayout{set0Layout, shadowSetLayout},
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

	// Opaque writes depth and does not blend. Translucent takes the water
	// pipeline's state, which is the one already proven in this render pass:
	// depth still tested, so a ghost behind a hill stays behind it, but not
	// written, so two translucent surfaces do not fight over which one exists
	// and the opaque depth every later pass reads stays the opaque depth.
	depthState := &core1_0.PipelineDepthStencilStateCreateInfo{
		DepthTestEnable:  true,
		DepthWriteEnable: !blend,
		DepthCompareOp:   core1_0.CompareOpGreater,
	}
	const writeAll = core1_0.ColorComponentRed | core1_0.ColorComponentGreen | core1_0.ColorComponentBlue | core1_0.ColorComponentAlpha
	attachment := core1_0.PipelineColorBlendAttachmentState{ColorWriteMask: writeAll}
	if blend {
		attachment = core1_0.PipelineColorBlendAttachmentState{
			ColorWriteMask:      writeAll,
			BlendEnabled:        true,
			SrcColorBlendFactor: core1_0.BlendFactorSrcAlpha,
			DstColorBlendFactor: core1_0.BlendFactorOneMinusSrcAlpha,
			ColorBlendOp:        core1_0.BlendOpAdd,
			SrcAlphaBlendFactor: core1_0.BlendFactorOne,
			DstAlphaBlendFactor: core1_0.BlendFactorZero,
			AlphaBlendOp:        core1_0.BlendOpAdd,
		}
	}
	blendState := &core1_0.PipelineColorBlendStateCreateInfo{
		Attachments: []core1_0.PipelineColorBlendAttachmentState{attachment},
	}

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
			Viewports: []core1_0.Viewport{{X: 0, Y: 0, Width: float32(extent.Width), Height: float32(extent.Height), MinDepth: 0, MaxDepth: 1}},
			Scissors:  []core1_0.Rect2D{{Offset: core1_0.Offset2D{X: 0, Y: 0}, Extent: extent}},
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
		DepthStencilState: depthState,
		ColorBlendState:   blendState,
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

	log.Printf("%s pipeline created", label)
	return pipelines[0], pipelineLayout, nil
}

// createGraphicsPipeline creates the main scene pipeline with depth testing,
// back-face culling, and push constants for per-object MVP + tint + lighting.
// The litPipelineLayout uses: set 0 = texture, set 1 = shadow (UBO + sampler).
func createGraphicsPipeline(deviceDriver core1_0.DeviceDriver, sh ShaderSet, renderPass core1_0.RenderPass, extent core1_0.Extent2D, texSetLayout core1_0.DescriptorSetLayout, shadowSetLayout core1_0.DescriptorSetLayout, samples core1_0.SampleCountFlags, cullMode ...core1_0.CullModeFlags) (core1_0.Pipeline, core1_0.PipelineLayout, error) {
	cull := core1_0.CullModeBack
	if len(cullMode) > 0 {
		cull = cullMode[0]
	}
	return createLitVariantPipeline(deviceDriver, sh.LitVert, sh.LitFrag, "Graphics",
		renderPass, extent, texSetLayout, shadowSetLayout, samples, cull, false)
}

// createTerrainPipeline creates the terrain splat pipeline: same vertex stage,
// vertex format, depth, and culling as the lit pipeline, but a fragment stage
// that blends multiple detail textures by a splat map. Layout: set 0 = terrain
// material (4 samplers), set 1 = shadow.
func createTerrainPipeline(deviceDriver core1_0.DeviceDriver, sh ShaderSet, renderPass core1_0.RenderPass, extent core1_0.Extent2D, terrainSetLayout core1_0.DescriptorSetLayout, shadowSetLayout core1_0.DescriptorSetLayout, samples core1_0.SampleCountFlags) (core1_0.Pipeline, core1_0.PipelineLayout, error) {
	return createLitVariantPipeline(deviceDriver, sh.LitVert, sh.TerrainFrag, "Terrain",
		renderPass, extent, terrainSetLayout, shadowSetLayout, samples, core1_0.CullModeBack, false)
}

// createMaterialPipeline creates the material pipeline: the lit path with normal,
// metallic-roughness, and occlusion maps. Layout: set 0 = material (4 samplers +
// a per-material UBO), set 1 = shadow.
func createMaterialPipeline(deviceDriver core1_0.DeviceDriver, sh ShaderSet, renderPass core1_0.RenderPass, extent core1_0.Extent2D, materialSetLayout core1_0.DescriptorSetLayout, shadowSetLayout core1_0.DescriptorSetLayout, samples core1_0.SampleCountFlags, cullMode ...core1_0.CullModeFlags) (core1_0.Pipeline, core1_0.PipelineLayout, error) {
	cull := core1_0.CullModeBack
	if len(cullMode) > 0 {
		cull = cullMode[0]
	}
	return createLitVariantPipeline(deviceDriver, sh.LitVert, sh.LitMaterialFrag, "Material",
		renderPass, extent, materialSetLayout, shadowSetLayout, samples, cull, false)
}

// createTranslucentPipeline is the lit pipeline with the water pipeline's depth
// and blend state: the fourth lit variant.
//
// Same shaders and the same layout as the opaque lit path, so a translucent
// draw is lit, fogged and shadow-receiving exactly as it would be at full
// opacity. The only differences are that lit.frag writes its alpha from
// sunDir.w rather than 1, and that this pipeline blends the result.
//
// A double-sided twin exists for the same reason the opaque path has one:
// DoubleSided is a component a game can already put on an entity, and a
// translucent pipeline that ignored it would cull the back faces of a glass box
// and leave nothing where its far wall should be.
func createTranslucentPipeline(deviceDriver core1_0.DeviceDriver, sh ShaderSet, renderPass core1_0.RenderPass, extent core1_0.Extent2D, texSetLayout core1_0.DescriptorSetLayout, shadowSetLayout core1_0.DescriptorSetLayout, samples core1_0.SampleCountFlags, cullMode ...core1_0.CullModeFlags) (core1_0.Pipeline, core1_0.PipelineLayout, error) {
	cull := core1_0.CullModeBack
	label := "Translucent"
	if len(cullMode) > 0 {
		cull = cullMode[0]
		label = "Translucent double-sided"
	}
	return createLitVariantPipeline(deviceDriver, sh.LitVert, sh.LitFrag, label,
		renderPass, extent, texSetLayout, shadowSetLayout, samples, cull, true)
}

// createInstancedPipeline is the lit pipeline for InstanceSets: the same
// fragment stage and the same layout, with a vertex stage that takes the model
// matrix and tint from a per-instance vertex binding instead of from push
// constants.
//
// Everything that made this worth doing is in the vertex input state. The rest
// of the pipeline is deliberately identical to the opaque lit one, so an
// instanced prop is lit, fogged and shadowed exactly as the same mesh drawn
// individually would be -- which is the property that lets the two be compared.
func createInstancedPipeline(deviceDriver core1_0.DeviceDriver, sh ShaderSet, renderPass core1_0.RenderPass, extent core1_0.Extent2D, texSetLayout core1_0.DescriptorSetLayout, shadowSetLayout core1_0.DescriptorSetLayout, samples core1_0.SampleCountFlags, cullMode ...core1_0.CullModeFlags) (core1_0.Pipeline, core1_0.PipelineLayout, error) {
	cull := core1_0.CullModeBack
	label := "Instanced"
	if len(cullMode) > 0 {
		cull = cullMode[0]
		label = "Instanced double-sided"
	}
	return createLitVariantPipelineWithInput(deviceDriver, sh.LitInstancedVert, sh.LitFrag, label,
		renderPass, extent, texSetLayout, shadowSetLayout, samples, cull, false,
		[]core1_0.VertexInputBindingDescription{vertexBindingDescription(), instanceBindingDescription()},
		instanceAttributeDescriptions())
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
// no vertex input, depth-tested against the far plane, additive blending.
// createCelestialPipeline draws the sun and moon discs after the sky rather
// than with the opaque geometry.
//
// In the draw list they wrote depth, so the sky pass -- which is where clouds
// are composited -- was depth-rejected on exactly those pixels, and a cloud
// could never be drawn in front of the sun. Drawing them here instead, blended
// against the destination alpha the sky pass writes, puts the clouds back in
// front.
//
// Terrain occlusion is the depth test's job, not the blend's. The first version
// of this turned the depth test off and expected the destination alpha to mask
// geometry as well as cloud; it does not, because geometry writes alpha 1.0,
// and the result was a sun disc drawn on top of the hills it had set behind.

func createCelestialPipeline(deviceDriver core1_0.DeviceDriver, sh ShaderSet, renderPass core1_0.RenderPass, pipelineLayout core1_0.PipelineLayout, extent core1_0.Extent2D, samples core1_0.SampleCountFlags) (core1_0.Pipeline, error) {
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
		// The same state the stars use, and for the same reason: the disc sits at
		// far * 0.98, so GreaterOrEqual passes where nothing has been drawn --
		// depth is cleared to 0 under reverse-Z -- and fails wherever geometry
		// is nearer. No depth write, because nothing needs to test against a
		// celestial body.
		DepthStencilState: farPlaneDepthState(),
		ColorBlendState: &core1_0.PipelineColorBlendStateCreateInfo{
			Attachments: []core1_0.PipelineColorBlendAttachmentState{{
				ColorWriteMask: core1_0.ColorComponentRed | core1_0.ColorComponentGreen | core1_0.ColorComponentBlue | core1_0.ColorComponentAlpha,
				BlendEnabled:   true,

				// Gated on destination alpha, exactly as the stars are. The sky
				// pass writes cloud transmittance there, so a cloud drawn over
				// the sun masks the disc for free.
				//
				// It does *not* mask against geometry, which is what this used
				// to claim. lit.frag and terrain.frag write alpha 1.0, so over
				// a hillside the destination alpha is 1 and the disc was added
				// at full strength -- a sun below the horizon drawn on top of
				// the terrain in front of it. The depth test above is what
				// actually handles geometry.
				SrcColorBlendFactor: core1_0.BlendFactorDstAlpha,
				DstColorBlendFactor: core1_0.BlendFactorOne,
				ColorBlendOp:        core1_0.BlendOpAdd,
				SrcAlphaBlendFactor: core1_0.BlendFactorZero,
				DstAlphaBlendFactor: core1_0.BlendFactorOne,
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

	log.Println("Celestial pipeline created")
	return pipelines[0], nil
}

// createStarsPipeline creates a pipeline for procedural starfield rendering:
// no vertex input, depth-tested against the far plane, additive blending.
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
		// The starfield decides where a star goes purely from the reconstructed
		// view ray, so it has no idea a mountain is in the way. Without this it
		// drew over the silhouette of anything reaching up into the sky.
		DepthStencilState: farPlaneDepthState(),
		ColorBlendState: &core1_0.PipelineColorBlendStateCreateInfo{
			Attachments: []core1_0.PipelineColorBlendAttachmentState{{
				ColorWriteMask: core1_0.ColorComponentRed | core1_0.ColorComponentGreen | core1_0.ColorComponentBlue | core1_0.ColorComponentAlpha,
				BlendEnabled:   true,
				// Additive, but scaled by the destination alpha, which sky.frag
				// wrote as the cloud layer's transmittance. Under clear sky that
				// is 1 and this is the plain additive blend it always was; under
				// overcast it goes to 0 and the stars behind the cloud disappear,
				// which is the point. Without it a starfield speckles straight
				// through solid cloud.
				SrcColorBlendFactor: core1_0.BlendFactorDstAlpha,
				DstColorBlendFactor: core1_0.BlendFactorOne,
				ColorBlendOp:        core1_0.BlendOpAdd,
				// Leave alpha alone: it is still the cloud transmittance, and
				// nothing downstream should see stars change it.
				SrcAlphaBlendFactor: core1_0.BlendFactorZero,
				DstAlphaBlendFactor: core1_0.BlendFactorOne,
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
		// Depth-tested so the sky only shades pixels no geometry reached; see
		// the draw order in recordCommandBuffer.
		DepthStencilState: farPlaneDepthState(),
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
// overlayBlend is the "over" both screen-space overlay pipelines use, in the two
// forms the two destinations need.
//
// Straight (premultiplied false) is the direct path onto the swapchain: the
// shader writes an unscaled colour and the blend unit multiplies it by the
// source alpha. Premultiplied is the UI glow layer: ui.frag and msdf.frag have
// already scaled by coverage, so the source factor is One and multiplying again
// would count alpha twice.
//
// The ALPHA factors are One / OneMinusSrcAlpha in BOTH, and always were. That is
// what makes the layer's accumulated alpha a correct "over" rather than a² --
// the half of premultiplied alpha that was already right here before there was a
// layer to need it.
//
// Getting this pair out of step is not a subtle miscolouring, it is the failure
// this whole feature is one line away from: a dark fringe on exactly the
// antialiased edges #15 added, which reads as "the antialiasing is broken"
// rather than as a blend mode. Measured by doing it, twice -- once on the way in,
// before this function existed, where 13-ui through the layer differed from the
// direct path on 33745 pixels at a maximum of 55/255, and once afterwards
// through `task uiglow` with the premultiplied case returning SrcAlpha, which
// reports 60747 px differing, 15204 of them above 1/255, max delta 60 against a
// tolerance of 2. Every one of them is a glyph edge or a panel border, and the
// gate's GLOW arm stays green throughout.
func overlayBlend(premultiplied bool) core1_0.PipelineColorBlendAttachmentState {
	src := core1_0.BlendFactorSrcAlpha
	if premultiplied {
		src = core1_0.BlendFactorOne
	}
	return core1_0.PipelineColorBlendAttachmentState{
		ColorWriteMask:      core1_0.ColorComponentRed | core1_0.ColorComponentGreen | core1_0.ColorComponentBlue | core1_0.ColorComponentAlpha,
		BlendEnabled:        true,
		SrcColorBlendFactor: src,
		DstColorBlendFactor: core1_0.BlendFactorOneMinusSrcAlpha,
		ColorBlendOp:        core1_0.BlendOpAdd,
		SrcAlphaBlendFactor: core1_0.BlendFactorOne,
		DstAlphaBlendFactor: core1_0.BlendFactorOneMinusSrcAlpha,
		AlphaBlendOp:        core1_0.BlendOpAdd,
	}
}

func createMSDFPipeline(deviceDriver core1_0.DeviceDriver, sh ShaderSet, renderPass core1_0.RenderPass, pipelineLayout core1_0.PipelineLayout, extent core1_0.Extent2D, samples core1_0.SampleCountFlags, premultiplied bool) (core1_0.Pipeline, error) {
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
			Attachments: []core1_0.PipelineColorBlendAttachmentState{overlayBlend(premultiplied)},
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

// createSkinnedPipeline creates a pipeline for skinned meshes: same as the main
// lit pipeline but using the skinned vertex shader and skinned vertex input
// layout. The fragment shader is a parameter because there are two -- the plain
// one and the material variant -- and they differ only in what set 0 holds.
// Both put the shadow sampler at set 2, because set 1 is the joint UBO.
func createSkinnedPipeline(deviceDriver core1_0.DeviceDriver, sh ShaderSet, fragSpv []byte, renderPass core1_0.RenderPass, skinnedPipelineLayout core1_0.PipelineLayout, extent core1_0.Extent2D, samples core1_0.SampleCountFlags, blend bool) (core1_0.Pipeline, error) {
	vertModule, _, err := deviceDriver.CreateShaderModule(nil, core1_0.ShaderModuleCreateInfo{
		Code: bytesToUint32Slice(sh.SkinnedLitVert),
	})
	if err != nil {
		return core1_0.Pipeline{}, err
	}
	defer deviceDriver.DestroyShaderModule(vertModule, nil)

	fragModule, _, err := deviceDriver.CreateShaderModule(nil, core1_0.ShaderModuleCreateInfo{
		Code: bytesToUint32Slice(fragSpv),
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
			DepthWriteEnable: !blend,
			DepthCompareOp:   core1_0.CompareOpGreater,
		},
		ColorBlendState: &core1_0.PipelineColorBlendStateCreateInfo{
			Attachments: []core1_0.PipelineColorBlendAttachmentState{{
				ColorWriteMask:      core1_0.ColorComponentRed | core1_0.ColorComponentGreen | core1_0.ColorComponentBlue | core1_0.ColorComponentAlpha,
				BlendEnabled:        blend,
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
func createUIPipeline(deviceDriver core1_0.DeviceDriver, sh ShaderSet, renderPass core1_0.RenderPass, pipelineLayout core1_0.PipelineLayout, extent core1_0.Extent2D, samples core1_0.SampleCountFlags, premultiplied bool) (core1_0.Pipeline, error) {
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
			Attachments: []core1_0.PipelineColorBlendAttachmentState{overlayBlend(premultiplied)},
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

// createGodRayPipeline creates the light-shaft pipeline: a fullscreen triangle
// blended additively over the frame.
//
// It runs in the water render pass rather than one of its own, which is what
// the renderPass parameter has to be: godray.frag samples the scene copy that
// pass makes, and a pass of its own would need a second copy for nothing. See
// recordLightShafts for where in that pass it goes and why.
//
// From 2026-07-29 (b14c350) to 2026-09-19 this pipeline was created, handed to
// recordCommandBuffer, handed on to recordWaterPass and never bound by
// anything, and neither LightShafts nor SunScreenPos was packed into any push
// constant -- so the shader could not have found the sun even if it had run.
// Nothing noticed for seven weeks, which is why `task shafts` exists.
//
// No depth test: the shafts are light in the air between the eye and everything
// else, so there is nothing for them to be behind.
func createGodRayPipeline(deviceDriver core1_0.DeviceDriver, sh ShaderSet, renderPass core1_0.RenderPass, pipelineLayout core1_0.PipelineLayout, extent core1_0.Extent2D, samples core1_0.SampleCountFlags) (core1_0.Pipeline, error) {
	vertModule, _, err := deviceDriver.CreateShaderModule(nil, core1_0.ShaderModuleCreateInfo{
		Code: bytesToUint32Slice(sh.SkyVert),
	})
	if err != nil {
		return core1_0.Pipeline{}, err
	}
	defer deviceDriver.DestroyShaderModule(vertModule, nil)

	fragModule, _, err := deviceDriver.CreateShaderModule(nil, core1_0.ShaderModuleCreateInfo{
		Code: bytesToUint32Slice(sh.GodRayFrag),
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
				Width: float32(extent.Width), Height: float32(extent.Height),
				MinDepth: 0, MaxDepth: 1,
			}},
			Scissors: []core1_0.Rect2D{{Extent: extent}},
		},
		RasterizationState: &core1_0.PipelineRasterizationStateCreateInfo{
			PolygonMode: core1_0.PolygonModeFill,
			CullMode:    0,
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
				DstColorBlendFactor: core1_0.BlendFactorOne, // additive: shafts add light, never remove it
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

	log.Println("God ray pipeline created")
	return pipelines[0], nil
}

// createSkyVolumetricPipeline builds the in-scattering draw that fronts the
// sky: the same fullscreen triangle at the same far-plane depth test, blended
// additively over whatever the dome left there.
//
// It exists as a pipeline rather than as four lines at the end of sky.frag for
// a measured reason recorded in shaders/skyvolumetric.frag -- putting the
// march in that shader moved three pixels of a scene with no volumetric light
// in it, because the compiler schedules the dome's existing arithmetic
// differently once the march shares the function. Here the dome is untouched
// and the cost is skipped rather than branched over.
//
// farPlaneDepthState is the sky's own, so this covers exactly the pixels the
// sky covered: depth GreaterOrEqual against a buffer cleared to 0, which is
// everything no geometry reached. Sharing that call rather than restating it
// is deliberate -- a depth state that drifted from the sky's would put the
// beam on pixels the dome is not on, and reverse-Z makes that the kind of
// mistake that draws nothing at all rather than something slightly wrong.
func createSkyVolumetricPipeline(deviceDriver core1_0.DeviceDriver, sh ShaderSet, renderPass core1_0.RenderPass, pipelineLayout core1_0.PipelineLayout, extent core1_0.Extent2D, samples core1_0.SampleCountFlags) (core1_0.Pipeline, error) {
	vertModule, _, err := deviceDriver.CreateShaderModule(nil, core1_0.ShaderModuleCreateInfo{
		Code: bytesToUint32Slice(sh.SkyVert),
	})
	if err != nil {
		return core1_0.Pipeline{}, err
	}
	defer deviceDriver.DestroyShaderModule(vertModule, nil)

	fragModule, _, err := deviceDriver.CreateShaderModule(nil, core1_0.ShaderModuleCreateInfo{
		Code: bytesToUint32Slice(sh.SkyVolumetricFrag),
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
		DepthStencilState: farPlaneDepthState(),
		ColorBlendState: &core1_0.PipelineColorBlendStateCreateInfo{
			Attachments: []core1_0.PipelineColorBlendAttachmentState{{
				ColorWriteMask: core1_0.ColorComponentRed | core1_0.ColorComponentGreen | core1_0.ColorComponentBlue | core1_0.ColorComponentAlpha,
				BlendEnabled:   true,
				// Plain additive. Unlike the star pass above this is NOT scaled
				// by destination alpha: a star is a thing behind the cloud
				// layer and has to be hidden by it, while in-scattering is
				// light in the air between the eye and the cloud, so a cloud
				// cannot occlude it. The two differ deliberately.
				SrcColorBlendFactor: core1_0.BlendFactorOne,
				DstColorBlendFactor: core1_0.BlendFactorOne,
				ColorBlendOp:        core1_0.BlendOpAdd,
				// Alpha is the cloud transmittance the dome wrote. Nothing
				// here has an opinion about it, and the fragment shader
				// outputs 0 there so a driver that ignored these factors
				// would still leave it alone.
				SrcAlphaBlendFactor: core1_0.BlendFactorZero,
				DstAlphaBlendFactor: core1_0.BlendFactorOne,
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

	log.Println("Sky volumetric pipeline created")
	return pipelines[0], nil
}
