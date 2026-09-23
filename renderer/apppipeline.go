package renderer

import "github.com/vkngwrapper/core/v3/core1_0"

func createAppPipeline(deviceDriver core1_0.DeviceDriver, desc AppPassDesc, renderPass core1_0.RenderPass, set0Layout, shadowSetLayout, inputSetLayout core1_0.DescriptorSetLayout, samples core1_0.SampleCountFlags) (core1_0.Pipeline, core1_0.PipelineLayout, error) {
	vertSpv, fragSpv := desc.Vert, desc.Frag
	extent := core1_0.Extent2D{Width: 1, Height: 1}
	var bindings []core1_0.VertexInputBindingDescription
	var attrs []core1_0.VertexInputAttributeDescription
	if !desc.Fullscreen {
		bindings = []core1_0.VertexInputBindingDescription{vertexBindingDescription()}
		locations, _ := appVertexLocations(desc.Vert)
		for _, a := range vertexAttributeDescriptions() {
			if locations[uint32(a.Location)] {
				attrs = append(attrs, a)
			}
		}
	}

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

	// Set 0 is the draw texture, set 1 the shared light set, set 2 pass inputs.
	pipelineLayout, _, err := deviceDriver.CreatePipelineLayout(nil, core1_0.PipelineLayoutCreateInfo{
		SetLayouts: []core1_0.DescriptorSetLayout{set0Layout, shadowSetLayout, inputSetLayout},
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

	// Own targets may write depth. HDR passes only test the scene's depth.
	depthState := &core1_0.PipelineDepthStencilStateCreateInfo{
		DepthTestEnable:  desc.DepthTest,
		DepthWriteEnable: desc.DepthTest && desc.Target != nil,
		DepthCompareOp:   core1_0.CompareOpGreater,
	}
	const writeAll = core1_0.ColorComponentRed | core1_0.ColorComponentGreen | core1_0.ColorComponentBlue | core1_0.ColorComponentAlpha
	attachment := core1_0.PipelineColorBlendAttachmentState{ColorWriteMask: writeAll}
	if desc.Blend != BlendNone {
		attachment = core1_0.PipelineColorBlendAttachmentState{
			ColorWriteMask:      writeAll,
			BlendEnabled:        true,
			SrcColorBlendFactor: core1_0.BlendFactorOne,
			DstColorBlendFactor: core1_0.BlendFactorOneMinusSrcAlpha,
			ColorBlendOp:        core1_0.BlendOpAdd,
			SrcAlphaBlendFactor: core1_0.BlendFactorOne,
			DstAlphaBlendFactor: core1_0.BlendFactorOneMinusSrcAlpha,
			AlphaBlendOp:        core1_0.BlendOpAdd,
		}
	}
	if desc.Blend == BlendAdditive {
		attachment.DstColorBlendFactor = core1_0.BlendFactorOne
		attachment.DstAlphaBlendFactor = core1_0.BlendFactorOne
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
			CullMode:    0,
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
		for _, p := range pipelines {
			if p.Handle() != 0 {
				deviceDriver.DestroyPipeline(p, nil)
			}
		}
		deviceDriver.DestroyPipelineLayout(pipelineLayout, nil)
		return core1_0.Pipeline{}, core1_0.PipelineLayout{}, err
	}

	return pipelines[0], pipelineLayout, nil
}
