package renderer

import (
	"github.com/vkngwrapper/core/v3/common"
	"github.com/vkngwrapper/core/v3/core1_0"
	"github.com/vkngwrapper/extensions/v3/khr_swapchain"
)

// This file implements the classic Vulkan "hello triangle" as a self-contained
// diagnostic. It exists to answer one question with no ambiguity: does the
// instance -> device -> swapchain -> render pass -> pipeline -> command buffer
// -> submit -> present path actually work on this machine?
//
// It deliberately uses nothing else. The shader hardcodes its three vertices
// and their colors and indexes them with gl_VertexIndex, so there are no vertex
// buffers, no descriptor sets, no push constants, no textures, and no assets on
// disk. When DrawTriangle fails, the fault is in core Vulkan setup and not in
// anything the scene renderer layers on top.
//
// Depth testing is disabled here on purpose. The main pipelines use reverse-Z
// (depth clears to 0.0, CompareOpGreater), and triangle.vert emits z = 0.0,
// which would fail that test and draw nothing at all -- a confusing result for
// what is supposed to be the simplest possible smoke test.

// createTrianglePipeline builds a depth-less, vertex-buffer-less pipeline for
// the diagnostic triangle, reusing the renderer's main render pass and MSAA
// sample count so it exercises the real swapchain configuration.
func createTrianglePipeline(
	deviceDriver core1_0.DeviceDriver, sh ShaderSet, renderPass core1_0.RenderPass,
	extent core1_0.Extent2D,
	samples core1_0.SampleCountFlags,
) (core1_0.Pipeline, core1_0.PipelineLayout, error) {
	var nilPipeline core1_0.Pipeline
	var nilLayout core1_0.PipelineLayout

	vertModule, _, err := deviceDriver.CreateShaderModule(nil, core1_0.ShaderModuleCreateInfo{
		Code: bytesToUint32Slice(sh.TriangleVert),
	})
	if err != nil {
		return nilPipeline, nilLayout, err
	}
	defer deviceDriver.DestroyShaderModule(vertModule, nil)

	fragModule, _, err := deviceDriver.CreateShaderModule(nil, core1_0.ShaderModuleCreateInfo{
		Code: bytesToUint32Slice(sh.TriangleFrag),
	})
	if err != nil {
		return nilPipeline, nilLayout, err
	}
	defer deviceDriver.DestroyShaderModule(fragModule, nil)

	// Empty layout: the triangle shader takes no uniforms of any kind.
	layout, _, err := deviceDriver.CreatePipelineLayout(nil, core1_0.PipelineLayoutCreateInfo{})
	if err != nil {
		return nilPipeline, nilLayout, err
	}

	pipelines, _, err := deviceDriver.CreateGraphicsPipelines(nil, nil, core1_0.GraphicsPipelineCreateInfo{
		Stages: []core1_0.PipelineShaderStageCreateInfo{
			{Stage: core1_0.StageVertex, Module: vertModule, Name: "main"},
			{Stage: core1_0.StageFragment, Module: fragModule, Name: "main"},
		},
		// No bindings or attributes -- vertices come from gl_VertexIndex.
		VertexInputState:   &core1_0.PipelineVertexInputStateCreateInfo{},
		InputAssemblyState: &core1_0.PipelineInputAssemblyStateCreateInfo{Topology: core1_0.PrimitiveTopologyTriangleList},
		ViewportState: &core1_0.PipelineViewportStateCreateInfo{
			Viewports: []core1_0.Viewport{{
				X: 0, Y: 0,
				Width:    float32(extent.Width),
				Height:   float32(extent.Height),
				MinDepth: 0,
				MaxDepth: 1,
			}},
			Scissors: []core1_0.Rect2D{{
				Offset: core1_0.Offset2D{X: 0, Y: 0},
				Extent: extent,
			}},
		},
		RasterizationState: &core1_0.PipelineRasterizationStateCreateInfo{
			PolygonMode: core1_0.PolygonModeFill,
			// CullMode is left zero (VK_CULL_MODE_NONE -- the bindings expose no
			// constant for it) so the smoke test cannot fail on winding order.
			FrontFace: core1_0.FrontFaceClockwise,
			LineWidth: 1.0,
		},
		MultisampleState: &core1_0.PipelineMultisampleStateCreateInfo{
			RasterizationSamples: samples,
		},
		// Reverse-Z would reject z=0.0 against a 0.0 clear; skip depth entirely.
		DepthStencilState: &core1_0.PipelineDepthStencilStateCreateInfo{
			DepthTestEnable:  false,
			DepthWriteEnable: false,
		},
		ColorBlendState: &core1_0.PipelineColorBlendStateCreateInfo{
			Attachments: []core1_0.PipelineColorBlendAttachmentState{{
				ColorWriteMask: core1_0.ColorComponentRed | core1_0.ColorComponentGreen |
					core1_0.ColorComponentBlue | core1_0.ColorComponentAlpha,
				BlendEnabled: false,
			}},
		},
		DynamicState: &core1_0.PipelineDynamicStateCreateInfo{
			DynamicStates: []core1_0.DynamicState{
				core1_0.DynamicStateViewport,
				core1_0.DynamicStateScissor,
			},
		},
		Layout:     layout,
		RenderPass: renderPass,
		Subpass:    0,
	})
	if err != nil {
		deviceDriver.DestroyPipelineLayout(layout, nil)
		return nilPipeline, nilLayout, err
	}

	return pipelines[0], layout, nil
}

// DrawTriangle renders one frame containing only the diagnostic tri-color
// triangle on a dark background, and presents it.
//
// This is a diagnostic entry point, not a general drawing API -- it is used by
// the 01-triangle example and by CI smoke tests to verify the Vulkan path end
// to end without needing meshes, textures, cameras, or a scene. Ordinary
// rendering goes through DrawFrame.
//
// The pipeline is built lazily on first call and torn down by Destroy, so
// programs that never call this pay nothing for it.
func (r *Renderer) DrawTriangle() error {
	if r.trianglePipeline == nil {
		pipeline, layout, err := createTrianglePipeline(r.deviceDriver, r.shaders, r.renderPass, r.sc.extent, r.msaaSamples)
		if err != nil {
			return err
		}
		r.trianglePipeline = &pipeline
		r.trianglePipelineLayout = &layout
	}

	f := r.currentFrame

	if _, err := r.deviceDriver.WaitForFences(true, common.NoTimeout, r.sync.inFlight[f]); err != nil {
		return err
	}
	r.flushDeferredDestroys()

	imageIndex, result, err := r.swapchainExt.AcquireNextImage(r.sc.swapchain, common.NoTimeout, &r.sync.imageAvailable[f], nil)
	if err != nil {
		if result == khr_swapchain.VKErrorOutOfDate {
			return r.recreateSwapchain()
		}
		return err
	}

	if _, err := r.deviceDriver.ResetFences(r.sync.inFlight[f]); err != nil {
		return err
	}

	cmdBuf := r.commandBuffers[f]
	if _, err := r.deviceDriver.ResetCommandBuffer(cmdBuf, 0); err != nil {
		return err
	}
	if err := r.recordTriangle(cmdBuf, r.framebuffers[imageIndex], r.tonemapFor(imageIndex)); err != nil {
		return err
	}

	fence := r.sync.inFlight[f]
	_, err = r.deviceDriver.QueueSubmit(r.graphicsQueue, &fence, core1_0.SubmitInfo{
		WaitSemaphores:   []core1_0.Semaphore{r.sync.imageAvailable[f]},
		WaitDstStageMask: []core1_0.PipelineStageFlags{core1_0.PipelineStageColorAttachmentOutput},
		CommandBuffers:   []core1_0.CommandBuffer{cmdBuf},
		SignalSemaphores: []core1_0.Semaphore{r.sync.renderFinished[f]},
	})
	if err != nil {
		return err
	}

	r.lastPresented = imageIndex
	presentResult, err := r.swapchainExt.QueuePresent(r.presentQueue, khr_swapchain.PresentInfo{
		WaitSemaphores: []core1_0.Semaphore{r.sync.renderFinished[f]},
		Swapchains:     []khr_swapchain.Swapchain{r.sc.swapchain},
		ImageIndices:   []int{imageIndex},
	})
	if err != nil && presentResult != khr_swapchain.VKErrorOutOfDate {
		return err
	}
	if presentResult == khr_swapchain.VKErrorOutOfDate || presentResult == khr_swapchain.VKSuboptimal || r.framebufferResized {
		r.framebufferResized = false
		return r.recreateSwapchain()
	}

	r.currentFrame = (f + 1) % maxFramesInFlight
	return nil
}

// recordTriangle records a single render pass that clears and draws 3 vertices.
func (r *Renderer) recordTriangle(cmdBuf core1_0.CommandBuffer, framebuffer core1_0.Framebuffer, tonemap tonemapPass) error {
	if _, err := r.deviceDriver.BeginCommandBuffer(cmdBuf, core1_0.CommandBufferBeginInfo{}); err != nil {
		return err
	}

	// Clear value count must match the render pass attachment count: color +
	// depth, plus the resolve attachment when MSAA is enabled.
	clearValues := []core1_0.ClearValue{
		core1_0.ClearValueFloat{0.02, 0.02, 0.04, 1.0},
		core1_0.ClearValueDepthStencil{Depth: 0.0, Stencil: 0},
	}
	if r.msaa != nil {
		clearValues = append(clearValues, core1_0.ClearValueFloat{0, 0, 0, 1})
	}

	err := r.deviceDriver.CmdBeginRenderPass(cmdBuf, core1_0.SubpassContentsInline, core1_0.RenderPassBeginInfo{
		RenderPass:  r.renderPass,
		Framebuffer: framebuffer,
		RenderArea: core1_0.Rect2D{
			Offset: core1_0.Offset2D{X: 0, Y: 0},
			Extent: r.sc.extent,
		},
		ClearValues: clearValues,
	})
	if err != nil {
		return err
	}

	r.deviceDriver.CmdBindPipeline(cmdBuf, core1_0.PipelineBindPointGraphics, *r.trianglePipeline)
	r.deviceDriver.CmdSetViewport(cmdBuf, core1_0.Viewport{
		X: 0, Y: 0,
		Width:    float32(r.sc.extent.Width),
		Height:   float32(r.sc.extent.Height),
		MinDepth: 0, MaxDepth: 1,
	})
	r.deviceDriver.CmdSetScissor(cmdBuf, core1_0.Rect2D{
		Offset: core1_0.Offset2D{X: 0, Y: 0},
		Extent: r.sc.extent,
	})
	r.deviceDriver.CmdDraw(cmdBuf, 3, 1, 0, 0)

	r.deviceDriver.CmdEndRenderPass(cmdBuf)

	// The scene render pass writes the HDR target, so even the diagnostic
	// triangle needs resolving or nothing reaches the swapchain.
	if err := recordTonemap(r.deviceDriver, cmdBuf, tonemap, r.tonemapPipelineLayout, r.sc.extent); err != nil {
		return err
	}

	_, err = r.deviceDriver.EndCommandBuffer(cmdBuf)
	return err
}

// destroyTrianglePipeline releases the lazily-created diagnostic pipeline.
func (r *Renderer) destroyTrianglePipeline() {
	if r.trianglePipeline != nil {
		r.deviceDriver.DestroyPipeline(*r.trianglePipeline, nil)
		r.trianglePipeline = nil
	}
	if r.trianglePipelineLayout != nil {
		r.deviceDriver.DestroyPipelineLayout(*r.trianglePipelineLayout, nil)
		r.trianglePipelineLayout = nil
	}
}
