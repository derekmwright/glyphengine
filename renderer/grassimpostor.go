package renderer

import (
	"fmt"
	"image"
	"log"
	"math"
	"unsafe"

	"github.com/go-gl/mathgl/mgl32"
	"github.com/vkngwrapper/core/v3/core1_0"
)

// grassImpostor is the baked atlas that stands in for grass meshes at distance.
//
// One cell per variant, side by side. Baked once at load from the real meshes
// rather than shipped as an asset, so the impostor cannot drift from what it
// replaces -- change the flora model and the billboard follows on the next run.
//
// Why this exists: grass is primitive-bound, not fill-bound. Measured on
// 08-grass, the pass costs 3.70 ms at 1920x1080 and 1.97 ms at 640x360 -- nine
// times the pixels for under twice the cost -- while dropping MSAA from 4x to 1x
// takes it from 3.05 to 1.36 ms. That combination says the cost is triangle
// setup and per-sample coverage on thin slivers, which is the worst case a
// rasteriser has. A blade is about 340 triangles; a billboard is two.
// grassImpostorCellSize is the atlas resolution per variant.
//
// 256 is generous for something that only draws past the LOD distance, where a
// blade covers a handful of pixels -- but the atlas is baked once, costs a
// megabyte, and being able to see what was baked is worth more than the memory.
const grassImpostorCellSize = 256

type grassImpostor struct {
	image  core1_0.Image
	memory core1_0.DeviceMemory
	view   core1_0.ImageView
	set    core1_0.DescriptorSet
	fb     core1_0.Framebuffer

	sampler    core1_0.Sampler
	renderPass core1_0.RenderPass
	pipeline   core1_0.Pipeline

	extent core1_0.Extent2D
	cells  int // one per variant, laid out left to right

	// worldHeight and worldWidth are the size in world units of what one cell
	// depicts, so the billboard quad can be built to match exactly.
	worldHeight float32
	worldWidth  float32
}

// bakeGrassImpostors renders each grass variant's mesh into an atlas cell.
//
// Orthographic, from the side, framed to the mesh's own bounds. A single view is
// enough here where it would not be for a tree: grass blades are thin and
// near-symmetric about their vertical axis, and at the distance impostors take
// over the parallax between one side and another is well under a pixel.
func (r *Renderer) bakeGrassImpostors(cellSize int) error {
	gs := r.grass
	if gs == nil || len(gs.Variants) == 0 {
		return nil
	}
	// The cell index and count share one push-constant float, so the count is
	// bounded by how they are packed. Failing here costs impostors and nothing
	// else -- the caller logs and draws meshes at every distance.
	if len(gs.Variants) > grassImpostorMaxCells {
		return fmt.Errorf("grass impostor: %d variants exceeds the %d the atlas can index",
			len(gs.Variants), grassImpostorMaxCells)
	}

	imp := &grassImpostor{
		cells: len(gs.Variants),
		extent: core1_0.Extent2D{
			Width:  cellSize * len(gs.Variants),
			Height: cellSize,
		},
	}

	// Frame every variant with one projection so the cells share a scale. A
	// per-cell fit would draw a clover and a grass blade the same size on
	// screen, which is exactly the information the impostor has to keep.
	//
	// Anchored at the blade's base rather than centred on its bounding sphere.
	// Centring puts the base wherever the sphere's centre happens to fall --
	// measured, about 69 percent down the cell for the shortest variant -- and
	// the billboard would then have to know that offset to sit on the ground.
	// Framing y from 0 to the tallest tip means the quad maps to the cell
	// exactly: base at the bottom edge, tip at the top.
	var halfWidth, tipHeight float32
	for _, v := range gs.Variants {
		if v.Mesh == nil {
			continue
		}
		if v.Mesh.BoundRadius > halfWidth {
			halfWidth = v.Mesh.BoundRadius
		}
		if top := v.Mesh.BoundCenter[1] + v.Mesh.BoundRadius; top > tipHeight {
			tipHeight = top
		}
	}
	if halfWidth <= 0 || tipHeight <= 0 {
		return fmt.Errorf("grass impostor: variants have no bounds to frame")
	}
	imp.worldHeight = tipHeight * grassBladeScale
	imp.worldWidth = 2 * halfWidth * grassBladeScale

	sampler, err := deviceSampler(r.deviceDriver)
	if err != nil {
		return err
	}
	imp.sampler = sampler

	img, mem, view, err := createOffscreenColor(r.instanceDriver, r.deviceDriver, r.physicalDevice, imp.extent)
	if err != nil {
		imp.destroy(r.deviceDriver)
		return fmt.Errorf("grass impostor image: %w", err)
	}
	imp.image, imp.memory, imp.view = img, mem, view

	// A clearing pass: cells are written by discard-heavy geometry, so whatever
	// the cutout rejects has to already be transparent black.
	imp.renderPass, err = createGrassBakeRenderPass(r.deviceDriver)
	if err != nil {
		imp.destroy(r.deviceDriver)
		return fmt.Errorf("grass impostor render pass: %w", err)
	}

	fb, _, err := r.deviceDriver.CreateFramebuffer(nil, core1_0.FramebufferCreateInfo{
		RenderPass:  imp.renderPass,
		Attachments: []core1_0.ImageView{imp.view},
		Width:       imp.extent.Width,
		Height:      imp.extent.Height,
		Layers:      1,
	})
	if err != nil {
		imp.destroy(r.deviceDriver)
		return fmt.Errorf("grass impostor framebuffer: %w", err)
	}
	imp.fb = fb

	imp.pipeline, err = createGrassBakePipeline(r.deviceDriver, r.shaders, imp.renderPass, r.pipelineLayout)
	if err != nil {
		imp.destroy(r.deviceDriver)
		return fmt.Errorf("grass impostor pipeline: %w", err)
	}

	sets, _, err := r.deviceDriver.AllocateDescriptorSets(core1_0.DescriptorSetAllocateInfo{
		DescriptorPool: r.descriptorPool,
		SetLayouts:     []core1_0.DescriptorSetLayout{r.descriptorSetLayout},
	})
	if err != nil {
		imp.destroy(r.deviceDriver)
		return fmt.Errorf("grass impostor descriptor set: %w", err)
	}
	imp.set = sets[0]
	if err := r.deviceDriver.UpdateDescriptorSets([]core1_0.WriteDescriptorSet{{
		DstSet:         imp.set,
		DstBinding:     0,
		DescriptorType: core1_0.DescriptorTypeCombinedImageSampler,
		ImageInfo: []core1_0.DescriptorImageInfo{{
			Sampler:     imp.sampler,
			ImageView:   imp.view,
			ImageLayout: core1_0.ImageLayoutShaderReadOnlyOptimal,
		}},
	}}, nil); err != nil {
		imp.destroy(r.deviceDriver)
		return fmt.Errorf("grass impostor descriptor write: %w", err)
	}

	if err := r.recordGrassBake(imp, cellSize, halfWidth, tipHeight); err != nil {
		imp.destroy(r.deviceDriver)
		return err
	}

	r.grassImpostor = imp
	log.Printf("Grass impostors: %dx%d atlas, %d cells, %.2f world units tall",
		imp.extent.Width, imp.extent.Height, imp.cells, imp.worldHeight)
	return nil
}

// recordGrassBake draws every variant into its cell in one pass.
func (r *Renderer) recordGrassBake(imp *grassImpostor, cellSize int, halfWidth, tipHeight float32) error {
	cmdBuf, err := r.beginSingleTimeCommands()
	if err != nil {
		return err
	}

	if err := r.deviceDriver.CmdBeginRenderPass(cmdBuf, core1_0.SubpassContentsInline, core1_0.RenderPassBeginInfo{
		RenderPass:  imp.renderPass,
		Framebuffer: imp.fb,
		RenderArea:  core1_0.Rect2D{Offset: core1_0.Offset2D{X: 0, Y: 0}, Extent: imp.extent},
		// Transparent black, so anything the cutout discards stays empty.
		ClearValues: []core1_0.ClearValue{core1_0.ClearValueFloat{0, 0, 0, 0}},
	}); err != nil {
		return err
	}
	r.deviceDriver.CmdBindPipeline(cmdBuf, core1_0.PipelineBindPointGraphics, imp.pipeline)

	for i, v := range r.grass.Variants {
		if v.Mesh == nil {
			continue
		}
		// One cell of the atlas, via the viewport rather than a separate pass.
		r.deviceDriver.CmdSetViewport(cmdBuf, core1_0.Viewport{
			X: float32(i * cellSize), Y: 0,
			Width: float32(cellSize), Height: float32(cellSize),
			MinDepth: 0, MaxDepth: 1,
		})
		r.deviceDriver.CmdSetScissor(cmdBuf, core1_0.Rect2D{
			Offset: core1_0.Offset2D{X: i * cellSize, Y: 0},
			Extent: core1_0.Extent2D{Width: cellSize, Height: cellSize},
		})

		tex := v.Texture
		if tex == nil {
			tex = r.grass.Texture
		}
		if tex == nil {
			tex = r.fallbackTexture
		}
		r.deviceDriver.CmdBindDescriptorSets(cmdBuf, core1_0.PipelineBindPointGraphics, r.pipelineLayout, 0,
			[]core1_0.DescriptorSet{tex.DescriptorSet}, nil)

		c := v.Mesh.BoundCenter

		// Orthographic box around the mesh, built for Vulkan's clip space
		// rather than taken from mgl32.Ortho.
		//
		// mgl32 is an OpenGL library: its Ortho maps depth to [-1, 1] and Vulkan
		// clips to [0, 1], so half the mesh lands behind the near plane and is
		// discarded. The first version of this baked an entirely empty atlas for
		// exactly that reason, and an empty atlas looks identical to a
		// transparent one in any image viewer -- it took bypassing the matrix in
		// the vertex shader to tell the two apart.
		//
		// x maps [-halfWidth, halfWidth] to [-1, 1]. y maps [0, tipHeight] to
		// [1, -1], putting the blade's base on the cell's bottom edge, with the
		// sign carrying Vulkan's y-down clip space. z maps into [0, 1] purely to
		// stay inside the frustum; there is no depth test, so nothing depends on
		// the ordering.
		var proj mgl32.Mat4
		proj[0] = 1 / halfWidth        // m00
		proj[5] = -2 / tipHeight       // m11, y down and base-anchored
		proj[13] = 1                   // m13, the base-anchoring offset
		proj[10] = 1 / (2 * halfWidth) // m22
		proj[14] = 0.5                 // m23, z into [0, 1]
		proj[15] = 1
		// Centre horizontally only: y is already anchored by the projection.
		view := mgl32.Translate3D(-c[0], 0, -c[2])
		mvp := proj.Mul4(view)

		var pc [pushConstantSize / 4]float32
		copy(pc[:16], mvp[:])
		pc[16], pc[21], pc[26], pc[31] = 1, 1, 1, 1 // identity model
		pc[32], pc[33], pc[34] = 1, 1, 1            // untinted
		r.deviceDriver.CmdPushConstants(cmdBuf, r.pipelineLayout, core1_0.StageVertex|core1_0.StageFragment, 0,
			unsafe.Slice((*byte)(unsafe.Pointer(&pc[0])), pushConstantSize))

		r.deviceDriver.CmdBindVertexBuffers(cmdBuf, 0, []core1_0.Buffer{v.Mesh.vertexBuffer}, []int{0})
		if v.Mesh.indexBuffer.Handle() != 0 {
			// The mesh's own index type, not an assumed one. glTF flora load as
			// uint16 and reading them as uint32 gives garbage indices, which
			// scatters the geometry out of clip -- an empty atlas that looks
			// exactly like a transparent one.
			r.deviceDriver.CmdBindIndexBuffer(cmdBuf, v.Mesh.indexBuffer, 0, v.Mesh.indexType)
			r.deviceDriver.CmdDrawIndexed(cmdBuf, v.Mesh.IndexCount, 1, 0, 0, 0)
		} else {
			r.deviceDriver.CmdDraw(cmdBuf, v.Mesh.VertexCount, 1, 0, 0)
		}
	}

	r.deviceDriver.CmdEndRenderPass(cmdBuf)
	return r.endSingleTimeCommands(cmdBuf)
}

func (imp *grassImpostor) destroy(deviceDriver core1_0.DeviceDriver) {
	if imp == nil {
		return
	}
	if imp.pipeline.Handle() != 0 {
		deviceDriver.DestroyPipeline(imp.pipeline, nil)
	}
	if imp.fb.Handle() != 0 {
		deviceDriver.DestroyFramebuffer(imp.fb, nil)
	}
	if imp.renderPass.Handle() != 0 {
		deviceDriver.DestroyRenderPass(imp.renderPass, nil)
	}
	if imp.view.Handle() != 0 {
		deviceDriver.DestroyImageView(imp.view, nil)
	}
	if imp.memory.Handle() != 0 {
		deviceDriver.FreeMemory(imp.memory, nil)
	}
	if imp.image.Handle() != 0 {
		deviceDriver.DestroyImage(imp.image, nil)
	}
	if imp.sampler.Handle() != 0 {
		deviceDriver.DestroySampler(imp.sampler, nil)
	}
	*imp = grassImpostor{}
}

// GrassImpostorAtlas reads the baked atlas back as an image.
//
// For checking the bake rather than for anything at run time. An impostor that
// is subtly wrong -- framed off centre, cut off at the top, silhouetted against
// the wrong cutout -- is very hard to diagnose from a field of grass a hundred
// units away, and obvious in the atlas.
func (r *Renderer) GrassImpostorAtlas() (*image.RGBA, error) {
	if r.grassImpostor == nil {
		return nil, fmt.Errorf("renderer: no grass impostor atlas has been baked")
	}
	imp := r.grassImpostor
	return r.readbackImage(imp.image, int(imp.extent.Width), int(imp.extent.Height))
}

// deviceSampler is the clamped linear sampler the impostor atlas is read
// through. Clamped because a cell's edges are transparent and wrapping would
// bring the neighbouring variant in.
func deviceSampler(deviceDriver core1_0.DeviceDriver) (core1_0.Sampler, error) {
	s, _, err := deviceDriver.CreateSampler(nil, core1_0.SamplerCreateInfo{
		MagFilter:    core1_0.FilterLinear,
		MinFilter:    core1_0.FilterLinear,
		AddressModeU: core1_0.SamplerAddressModeClampToEdge,
		AddressModeV: core1_0.SamplerAddressModeClampToEdge,
		AddressModeW: core1_0.SamplerAddressModeClampToEdge,
		MipmapMode:   core1_0.SamplerMipmapModeNearest,
	})
	if err != nil {
		return core1_0.Sampler{}, fmt.Errorf("create impostor sampler: %w", err)
	}
	return s, nil
}

// createGrassBakeRenderPass clears to transparent black and ends sampleable.
func createGrassBakeRenderPass(deviceDriver core1_0.DeviceDriver) (core1_0.RenderPass, error) {
	renderPass, _, err := deviceDriver.CreateRenderPass(nil, core1_0.RenderPassCreateInfo{
		Attachments: []core1_0.AttachmentDescription{{
			Format:  hdrFormat,
			Samples: core1_0.Samples1,
			// Clear rather than discard: the bake is discard-heavy, so every
			// texel the cutout rejects keeps whatever the clear put there.
			LoadOp:         core1_0.AttachmentLoadOpClear,
			StoreOp:        core1_0.AttachmentStoreOpStore,
			StencilLoadOp:  core1_0.AttachmentLoadOpDontCare,
			StencilStoreOp: core1_0.AttachmentStoreOpDontCare,
			InitialLayout:  core1_0.ImageLayoutUndefined,
			FinalLayout:    core1_0.ImageLayoutShaderReadOnlyOptimal,
		}},
		Subpasses: []core1_0.SubpassDescription{{
			PipelineBindPoint: core1_0.PipelineBindPointGraphics,
			ColorAttachments: []core1_0.AttachmentReference{
				{Attachment: 0, Layout: core1_0.ImageLayoutColorAttachmentOptimal},
			},
		}},
		SubpassDependencies: []core1_0.SubpassDependency{{
			SrcSubpass:    core1_0.SubpassExternal,
			DstSubpass:    0,
			SrcStageMask:  core1_0.PipelineStageColorAttachmentOutput,
			DstStageMask:  core1_0.PipelineStageFragmentShader,
			SrcAccessMask: core1_0.AccessColorAttachmentWrite,
			DstAccessMask: core1_0.AccessShaderRead,
		}},
	})
	if err != nil {
		return core1_0.RenderPass{}, err
	}
	return renderPass, nil
}

// createGrassBakePipeline draws mesh geometry with no depth test and no
// culling: the bake wants both faces of a blade, and a depth buffer would only
// decide which of two coplanar leaves wins.
func createGrassBakePipeline(deviceDriver core1_0.DeviceDriver, sh ShaderSet, renderPass core1_0.RenderPass, pipelineLayout core1_0.PipelineLayout) (core1_0.Pipeline, error) {
	vertModule, _, err := deviceDriver.CreateShaderModule(nil, core1_0.ShaderModuleCreateInfo{
		Code: bytesToUint32Slice(sh.GrassBakeVert),
	})
	if err != nil {
		return core1_0.Pipeline{}, err
	}
	defer deviceDriver.DestroyShaderModule(vertModule, nil)

	fragModule, _, err := deviceDriver.CreateShaderModule(nil, core1_0.ShaderModuleCreateInfo{
		Code: bytesToUint32Slice(sh.GrassBakeFrag),
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
		InputAssemblyState: &core1_0.PipelineInputAssemblyStateCreateInfo{Topology: core1_0.PrimitiveTopologyTriangleList},
		ViewportState: &core1_0.PipelineViewportStateCreateInfo{
			Viewports: []core1_0.Viewport{{Width: 1, Height: 1, MinDepth: 0, MaxDepth: 1}},
			Scissors:  []core1_0.Rect2D{{Extent: core1_0.Extent2D{Width: 1, Height: 1}}},
		},
		RasterizationState: &core1_0.PipelineRasterizationStateCreateInfo{
			PolygonMode: core1_0.PolygonModeFill,
			CullMode:    0,
			FrontFace:   core1_0.FrontFaceCounterClockwise,
			LineWidth:   1.0,
		},
		MultisampleState:  &core1_0.PipelineMultisampleStateCreateInfo{RasterizationSamples: core1_0.Samples1},
		DepthStencilState: &core1_0.PipelineDepthStencilStateCreateInfo{},
		ColorBlendState: &core1_0.PipelineColorBlendStateCreateInfo{
			Attachments: []core1_0.PipelineColorBlendAttachmentState{{
				ColorWriteMask: core1_0.ColorComponentRed | core1_0.ColorComponentGreen | core1_0.ColorComponentBlue | core1_0.ColorComponentAlpha,
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
		return core1_0.Pipeline{}, err
	}
	return pipelines[0], nil
}

// readbackImage copies a half-float colour target to the host as 8-bit RGBA.
//
// For inspecting a baked artifact, not for anything at run time. Alpha is kept
// because coverage is exactly what an impostor bake has to be checked for -- a
// silhouette that is subtly wrong is invisible in a field of grass and obvious
// in the atlas.
func (r *Renderer) readbackImage(img core1_0.Image, w, h int) (*image.RGBA, error) {
	const bytesPerTexel = 8 // RGBA16F
	size := w * h * bytesPerTexel

	r.deviceDriver.DeviceWaitIdle()

	buf, mem, err := r.createBuffer(size,
		core1_0.BufferUsageTransferDst,
		core1_0.MemoryPropertyHostVisible|core1_0.MemoryPropertyHostCoherent)
	if err != nil {
		return nil, err
	}
	defer func() {
		r.deviceDriver.FreeMemory(mem, nil)
		r.deviceDriver.DestroyBuffer(buf, nil)
	}()

	cmdBuf, err := r.beginSingleTimeCommands()
	if err != nil {
		return nil, err
	}
	rng := core1_0.ImageSubresourceRange{AspectMask: core1_0.ImageAspectColor, LevelCount: 1, LayerCount: 1}
	r.deviceDriver.CmdPipelineBarrier(cmdBuf,
		core1_0.PipelineStageFragmentShader, core1_0.PipelineStageTransfer, 0, nil, nil,
		[]core1_0.ImageMemoryBarrier{{
			OldLayout:           core1_0.ImageLayoutShaderReadOnlyOptimal,
			NewLayout:           core1_0.ImageLayoutTransferSrcOptimal,
			SrcQueueFamilyIndex: -1, DstQueueFamilyIndex: -1,
			Image: img, SubresourceRange: rng,
			SrcAccessMask: core1_0.AccessShaderRead, DstAccessMask: core1_0.AccessTransferRead,
		}})
	r.deviceDriver.CmdCopyImageToBuffer(cmdBuf, img, core1_0.ImageLayoutTransferSrcOptimal, buf,
		core1_0.BufferImageCopy{
			ImageSubresource: core1_0.ImageSubresourceLayers{AspectMask: core1_0.ImageAspectColor, LayerCount: 1},
			ImageExtent:      core1_0.Extent3D{Width: w, Height: h, Depth: 1},
		})
	r.deviceDriver.CmdPipelineBarrier(cmdBuf,
		core1_0.PipelineStageTransfer, core1_0.PipelineStageFragmentShader, 0, nil, nil,
		[]core1_0.ImageMemoryBarrier{{
			OldLayout:           core1_0.ImageLayoutTransferSrcOptimal,
			NewLayout:           core1_0.ImageLayoutShaderReadOnlyOptimal,
			SrcQueueFamilyIndex: -1, DstQueueFamilyIndex: -1,
			Image: img, SubresourceRange: rng,
			SrcAccessMask: core1_0.AccessTransferRead, DstAccessMask: core1_0.AccessShaderRead,
		}})
	if err := r.endSingleTimeCommands(cmdBuf); err != nil {
		return nil, err
	}

	ptr, _, err := r.deviceDriver.MapMemory(mem, 0, size, 0)
	if err != nil {
		return nil, err
	}
	defer r.deviceDriver.UnmapMemory(mem)

	raw := unsafe.Slice((*uint16)(ptr), w*h*4)
	out := image.NewRGBA(image.Rect(0, 0, w, h))
	for i := 0; i < w*h*4; i++ {
		v := half16(raw[i])
		if v < 0 {
			v = 0
		}
		if v > 1 {
			v = 1
		}
		out.Pix[i] = uint8(v*255 + 0.5)
	}
	return out, nil
}

// half16 decodes an IEEE half. Written out rather than taking a dependency
// because this is the only place the engine reads one back.
func half16(h uint16) float32 {
	sign := uint32(h>>15) << 31
	exp := uint32(h>>10) & 0x1f
	mant := uint32(h) & 0x3ff
	switch exp {
	case 0:
		if mant == 0 {
			return math.Float32frombits(sign)
		}
		shift := uint32(0)
		for mant&0x400 == 0 {
			mant <<= 1
			shift++
		}
		mant &= 0x3ff
		return math.Float32frombits(sign | (127-15-shift+1)<<23 | mant<<13)
	case 0x1f:
		return math.Float32frombits(sign | 0xff<<23 | mant<<13)
	default:
		return math.Float32frombits(sign | (exp+127-15)<<23 | mant<<13)
	}
}

// createGrassImpostorPipeline builds the billboard pass.
//
// Shares grass.frag and the lit pipeline layout with the mesh path, so the
// impostor lights, cuts out and fades identically -- only the vertex stage
// differs. It declares binding 1 alone: the quad's corners come from
// gl_VertexIndex, so there is no per-vertex buffer to bind.
func createGrassImpostorPipeline(deviceDriver core1_0.DeviceDriver, sh ShaderSet, renderPass core1_0.RenderPass, layout core1_0.PipelineLayout, extent core1_0.Extent2D, samples core1_0.SampleCountFlags) (core1_0.Pipeline, error) {
	vertModule, _, err := deviceDriver.CreateShaderModule(nil, core1_0.ShaderModuleCreateInfo{
		Code: bytesToUint32Slice(sh.GrassImpostorVert),
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

	pipelines, _, err := deviceDriver.CreateGraphicsPipelines(nil, nil, core1_0.GraphicsPipelineCreateInfo{
		Stages: []core1_0.PipelineShaderStageCreateInfo{
			{Stage: core1_0.StageVertex, Module: vertModule, Name: "main"},
			{Stage: core1_0.StageFragment, Module: fragModule, Name: "main"},
		},
		VertexInputState: &core1_0.PipelineVertexInputStateCreateInfo{
			VertexBindingDescriptions: []core1_0.VertexInputBindingDescription{{
				Binding:   1,
				Stride:    16, // sizeof(GrassInstance)
				InputRate: core1_0.VertexInputRateInstance,
			}},
			VertexAttributeDescriptions: []core1_0.VertexInputAttributeDescription{{
				Location: 4,
				Binding:  1,
				Format:   core1_0.FormatR32G32B32A32SignedFloat,
				Offset:   0,
			}},
		},
		InputAssemblyState: &core1_0.PipelineInputAssemblyStateCreateInfo{Topology: core1_0.PrimitiveTopologyTriangleList},
		ViewportState: &core1_0.PipelineViewportStateCreateInfo{
			Viewports: []core1_0.Viewport{{Width: float32(extent.Width), Height: float32(extent.Height), MinDepth: 0, MaxDepth: 1}},
			Scissors:  []core1_0.Rect2D{{Extent: extent}},
		},
		RasterizationState: &core1_0.PipelineRasterizationStateCreateInfo{
			PolygonMode: core1_0.PolygonModeFill,
			// No culling: the quad is built facing the camera, but the wind
			// shear can push its top past vertical and flip the winding.
			CullMode:  0,
			FrontFace: core1_0.FrontFaceCounterClockwise,
			LineWidth: 1.0,
		},
		MultisampleState: &core1_0.PipelineMultisampleStateCreateInfo{
			RasterizationSamples: samples,
			// The same alpha-to-coverage the blades use, which is what makes the
			// distance fade dissolve rather than shrink.
			AlphaToCoverageEnable: true,
		},
		DepthStencilState: &core1_0.PipelineDepthStencilStateCreateInfo{
			DepthTestEnable:  true,
			DepthWriteEnable: true,
			DepthCompareOp:   core1_0.CompareOpGreaterOrEqual, // reverse-Z
		},
		ColorBlendState: &core1_0.PipelineColorBlendStateCreateInfo{
			Attachments: []core1_0.PipelineColorBlendAttachmentState{{
				ColorWriteMask: core1_0.ColorComponentRed | core1_0.ColorComponentGreen | core1_0.ColorComponentBlue | core1_0.ColorComponentAlpha,
			}},
		},
		DynamicState: &core1_0.PipelineDynamicStateCreateInfo{
			DynamicStates: []core1_0.DynamicState{core1_0.DynamicStateViewport, core1_0.DynamicStateScissor},
		},
		Layout:     layout,
		RenderPass: renderPass,
		Subpass:    0,
	})
	if err != nil {
		return core1_0.Pipeline{}, fmt.Errorf("create grass impostor pipeline: %w", err)
	}
	log.Println("Grass impostor pipeline created")
	return pipelines[0], nil
}
