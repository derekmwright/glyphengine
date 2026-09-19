package renderer

import (
	"github.com/vkngwrapper/core/v3/core1_0"
)

// packUIFill writes the panel interior into the vec4 after params.
//
// A negative opacity is the "derive it from the tint" signal, and the sentinel
// has to be negative rather than zero because zero is a legitimate fill and so
// is one: a fully transparent interior leaves just the frame, and a fully
// opaque one is what a modal dialog needs. Neither can double as "unset".
//
// Split out so the packing can be tested without a device, the same way
// packLightingPC is.
func packUIFill(pc *[64]float32, fill *PanelFill) {
	if fill == nil {
		pc[43] = -1
		return
	}
	pc[40] = fill.Color[0]
	pc[41] = fill.Color[1]
	pc[42] = fill.Color[2]
	pc[43] = fill.Opacity
}

// recordUIComposite draws the screen-space overlay channels: onto the swapchain
// inside the tonemap pass and after its resolve triangle, which is the default,
// or into the UI glow layer when a game asked for one (recordUILayer calls this
// with premultiply set, and the composite of the finished layer then replaces
// these draws in the tonemap pass).
//
// One function for both because everything about WHAT is drawn is the same --
// the panels, the nine-slice fill, the texture mode, the text. What differs is
// the render pass the pipelines were built against and one push-constant float.
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
// Both of those still hold with the UI glow layer on, which is why the layer is
// a second target rather than a route back into the scene's. The UI still never
// reaches the scene's bloom chain or the scene's exposure; what the layer adds
// is a glow that comes from the UI's own chain, over the UI's own image, at an
// exposure that does not move with the time of day. A colour at or below 1 is
// still the literal sRGB value it is here. See uilayer.go.
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
	intoLayer bool,
	scratch *commandScratch,
) {
	if len(uiOverlays) == 0 && len(msdfOverlays) == 0 {
		return
	}

	// Both halves of glow -- pc[44], the emission multiplier, and pc[45], the
	// premultiply flag -- are pushed only when these draws are going into the
	// UI glow layer.
	//
	// The premultiply half has to be: the layer's destination is transparent
	// and the blend factor is One, so the colour must arrive already scaled by
	// its own coverage; see ui.frag.
	//
	// The EMISSION half is a decision rather than a necessity, and it is the
	// one worth writing down. An element's Glow could be honoured here too --
	// the shader would multiply, the 8-bit target would clamp, and a mid-grey
	// panel asking for glow 1 would come out white. That is not "no glow", it
	// is a different colour, and it would break the one promise the direct
	// path exists to keep: that a UI colour is the literal sRGB value a game
	// wrote. There is nowhere above 1 to put emission without an HDR layer, so
	// without one it does nothing at all. TestGlowIsZeroOnTheDirectPath holds
	// that for panels; text carries its emission per vertex, where the recorder
	// cannot filter it, so msdf.frag gates on the same flag instead.
	//
	// Checked on the GPU rather than only in the recorder: 13-ui in its
	// layer-less `direct` mode, with the button, the warning and the label all
	// asking for the emission they ask for in `on`, renders a byte-identical
	// frame to the same mode with none of them asking.
	//
	// On the direct path both stay at the zero resetPC already put there, so
	// the shaders take the branch they always took and every pushed byte is
	// what it was before the layer existed.
	var glowPremultiply float32
	if intoLayer {
		glowPremultiply = 1
	}

	// Set explicitly rather than inherited from the resolve triangle. Dynamic
	// state does carry across a pipeline bind, but a pass that only works
	// because something before it happened to set the viewport is one edit away
	// from not working.
	viewport := core1_0.Viewport{
		Width: float32(extent.Width), Height: float32(extent.Height), MinDepth: 0, MaxDepth: 1,
	}
	scissor := core1_0.Rect2D{Offset: core1_0.Offset2D{X: 0, Y: 0}, Extent: extent}
	scratch.setViewport(deviceDriver, cmdBuf, viewport)
	scratch.setScissor(deviceDriver, cmdBuf, scissor)

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
			scratch.bindDescriptorSets(deviceDriver, cmdBuf, core1_0.PipelineBindPointGraphics, pipelineLayout, 0, tex.DescriptorSet)
			scratch.bindVertexBuffers(deviceDriver, cmdBuf, 0, d.Mesh.vertexBuffer)

			scratch.resetPC()
			copy(scratch.pc[:16], d.MVP[:])
			// model = identity
			scratch.pc[16] = 1
			scratch.pc[21] = 1
			scratch.pc[26] = 1
			scratch.pc[31] = 1
			// tint.rgb = 1 (vertex color already tinted), tint.a = opacity
			scratch.pc[32] = 1.0
			scratch.pc[33] = 1.0
			scratch.pc[34] = 1.0
			scratch.pc[35] = d.Opacity
			// sunDir.x reused as texture mode flag (0=panel 9-slice, 1=straight texture)
			if d.TextureMode {
				scratch.pc[36] = 1.0
			}
			// glow.x: the element's linear emission multiple, zero for every
			// panel that has never heard of it and for every frame with no
			// layer to put it in.
			if intoLayer {
				scratch.pc[44] = d.Glow
			}
			scratch.pc[45] = glowPremultiply
			packUIFill(&scratch.pc, d.Fill)
			scratch.pushConstants(deviceDriver, cmdBuf, pipelineLayout, core1_0.StageVertex|core1_0.StageFragment)

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
			scratch.bindDescriptorSets(deviceDriver, cmdBuf, core1_0.PipelineBindPointGraphics, pipelineLayout, 0, tex.DescriptorSet)
			scratch.bindVertexBuffers(deviceDriver, cmdBuf, 0, d.Mesh.vertexBuffer)

			// Push constants: MVP + identity model + tint(rgb=color, w=screenPxRange)
			scratch.resetPC()
			copy(scratch.pc[:16], d.MVP[:])
			// model = identity
			scratch.pc[16] = 1
			scratch.pc[21] = 1
			scratch.pc[26] = 1
			scratch.pc[31] = 1
			// tint.rgb = 1 (per-vertex color handles text color), tint.w = screenPxRange
			scratch.pc[32] = 1.0
			scratch.pc[33] = 1.0
			scratch.pc[34] = 1.0
			scratch.pc[35] = d.Color[0] // screenPxRange
			// No glow.x here: text carries its emission per vertex, because one
			// draw covers every line an overlay holds. See TextLine.Glow.
			scratch.pc[45] = glowPremultiply
			scratch.pushConstants(deviceDriver, cmdBuf, pipelineLayout, core1_0.StageVertex|core1_0.StageFragment)

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
