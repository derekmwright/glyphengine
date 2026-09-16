package renderer

import (
	"unsafe"

	"github.com/vkngwrapper/core/v3/core1_0"
)

// recordUIComposite draws the screen-space overlay channels onto the swapchain,
// inside the tonemap pass and after its resolve triangle.
//
// They used to be recorded in the scene pass with everything else, and that put
// them in the HDR target before three passes that are meant to operate on the
// scene ran over it. Water was the one that showed: recordWaterPass copies the
// HDR image to refract through, so the copy contained the HUD, and the water
// surface then drew straight over text that writes no depth. A block of debug
// lines crossing the waterline in 09-water lost five of them outright and got
// four back as ghosts warped along the wave pattern. `task hud` measures it.
//
// Being tonemapped and feeding bloom were the other two consequences of the
// same ordering, and they go away with it: a UI colour is a literal sRGB value
// now rather than an HDR one that moves with scene exposure, and bright text no
// longer glows into the scene behind it.
//
// Compositing here rather than in a render pass of its own is deliberate. The
// tonemap pass already owns the swapchain image at the right extent, already
// gets recreated on resize, and already ends in PRESENT_SRC. A separate pass
// would add a render pass, a framebuffer per swapchain image, a full-target
// LoadOp Load, and another teardown step to unwind -- to reach the same
// attachment one command earlier.
//
// The world-space overlay channel stays in the scene pass. SetOverlays takes
// RenderObjects with a caller-supplied MVP and is documented as world space, so
// it wants the scene's depth buffer and the scene's MSAA; only the two channels
// that build an orthographic screen-space MVP belong here.
func recordUIComposite(
	deviceDriver core1_0.DeviceDriver,
	stats *RenderStats,
	cmdBuf core1_0.CommandBuffer,
	uiPipeline core1_0.Pipeline,
	msdfPipeline core1_0.Pipeline,
	pipelineLayout core1_0.PipelineLayout,
	extent core1_0.Extent2D,
	uiOverlays []UIRenderObject,
	msdfOverlays []RenderObject,
	fallbackTexture *Texture,
) {
	if len(uiOverlays) == 0 && len(msdfOverlays) == 0 {
		return
	}

	// Set explicitly rather than inherited from the resolve triangle. Dynamic
	// state does carry across a pipeline bind, but a pass that only works
	// because something before it happened to set the viewport is one edit away
	// from not working.
	viewport := core1_0.Viewport{
		Width: float32(extent.Width), Height: float32(extent.Height), MinDepth: 0, MaxDepth: 1,
	}
	scissor := core1_0.Rect2D{Offset: core1_0.Offset2D{X: 0, Y: 0}, Extent: extent}
	deviceDriver.CmdSetViewport(cmdBuf, viewport)
	deviceDriver.CmdSetScissor(cmdBuf, scissor)

	// Draw UI panels (alpha blended, textured, 9-slice)
	if len(uiOverlays) > 0 {
		deviceDriver.CmdBindPipeline(cmdBuf, core1_0.PipelineBindPointGraphics, uiPipeline)

		for i := range uiOverlays {
			d := &uiOverlays[i]
			if d.Mesh.IndexCount == 0 && d.Mesh.VertexCount == 0 {
				continue
			}

			tex := d.Texture
			if tex == nil {
				tex = fallbackTexture
			}
			deviceDriver.CmdBindDescriptorSets(cmdBuf, core1_0.PipelineBindPointGraphics, pipelineLayout, 0, []core1_0.DescriptorSet{tex.DescriptorSet}, nil)
			deviceDriver.CmdBindVertexBuffers(cmdBuf, 0, []core1_0.Buffer{d.Mesh.vertexBuffer}, []int{0})

			var pc [64]float32
			copy(pc[:16], d.MVP[:])
			// model = identity
			pc[16] = 1
			pc[21] = 1
			pc[26] = 1
			pc[31] = 1
			// tint.rgb = 1 (vertex color already tinted), tint.a = opacity
			pc[32] = 1.0
			pc[33] = 1.0
			pc[34] = 1.0
			pc[35] = d.Opacity
			// sunDir.x reused as texture mode flag (0=panel 9-slice, 1=straight texture)
			if d.TextureMode {
				pc[36] = 1.0
			}
			pcBytes := unsafe.Slice((*byte)(unsafe.Pointer(&pc[0])), pushConstantSize)
			deviceDriver.CmdPushConstants(cmdBuf, pipelineLayout, core1_0.StageVertex|core1_0.StageFragment, 0, pcBytes)

			stats.addDraw(1, d.Mesh.IndexCount, d.Mesh.VertexCount)
			if d.Mesh.IndexCount > 0 {
				deviceDriver.CmdBindIndexBuffer(cmdBuf, d.Mesh.indexBuffer, 0, d.Mesh.indexType)
				deviceDriver.CmdDrawIndexed(cmdBuf, d.Mesh.IndexCount, 1, 0, 0, 0)
			} else {
				deviceDriver.CmdDraw(cmdBuf, d.Mesh.VertexCount, 1, 0, 0)
			}
		}
	}

	// Draw MSDF text overlays (alpha blended, textured)
	if len(msdfOverlays) > 0 {
		deviceDriver.CmdBindPipeline(cmdBuf, core1_0.PipelineBindPointGraphics, msdfPipeline)

		for i := range msdfOverlays {
			d := &msdfOverlays[i]
			if d.Mesh.IndexCount == 0 && d.Mesh.VertexCount == 0 {
				continue
			}

			// Bind the MSDF atlas texture descriptor set
			tex := d.Texture
			if tex == nil {
				tex = fallbackTexture
			}
			deviceDriver.CmdBindDescriptorSets(cmdBuf, core1_0.PipelineBindPointGraphics, pipelineLayout, 0, []core1_0.DescriptorSet{tex.DescriptorSet}, nil)
			deviceDriver.CmdBindVertexBuffers(cmdBuf, 0, []core1_0.Buffer{d.Mesh.vertexBuffer}, []int{0})

			// Push constants: MVP + identity model + tint(rgb=color, w=screenPxRange)
			var pc [64]float32
			copy(pc[:16], d.MVP[:])
			// model = identity
			pc[16] = 1
			pc[21] = 1
			pc[26] = 1
			pc[31] = 1
			// tint.rgb = 1 (per-vertex color handles text color), tint.w = screenPxRange
			pc[32] = 1.0
			pc[33] = 1.0
			pc[34] = 1.0
			pc[35] = d.Color[0] // screenPxRange
			pcBytes := unsafe.Slice((*byte)(unsafe.Pointer(&pc[0])), pushConstantSize)
			deviceDriver.CmdPushConstants(cmdBuf, pipelineLayout, core1_0.StageVertex|core1_0.StageFragment, 0, pcBytes)

			stats.addDraw(1, d.Mesh.IndexCount, d.Mesh.VertexCount)
			if d.Mesh.IndexCount > 0 {
				deviceDriver.CmdBindIndexBuffer(cmdBuf, d.Mesh.indexBuffer, 0, d.Mesh.indexType)
				deviceDriver.CmdDrawIndexed(cmdBuf, d.Mesh.IndexCount, 1, 0, 0, 0)
			} else {
				deviceDriver.CmdDraw(cmdBuf, d.Mesh.VertexCount, 1, 0, 0)
			}
		}
	}
}
