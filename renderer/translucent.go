package renderer

import (
	"unsafe"

	"github.com/vkngwrapper/core/v3/core1_0"
)

// recordTranslucent draws the blended subset of the draw list.
//
// It runs inside the scene render pass, after the sky and before particles.
// Both ends of that matter:
//
//   - After the sky. The sky is a fullscreen triangle drawn last on purpose, so
//     it only shades pixels nothing else landed on. Translucent geometry writes
//     no depth, so drawn before the sky it would be painted straight over
//     wherever it overhangs the horizon — a placement ghost on a ridgeline would
//     lose its top half.
//   - Before particles. Neither writes depth, so the order between them is
//     paint order and nothing else. Additive effects reading as being in front
//     of a translucent surface is the less surprising of the two, and it is the
//     order a sparks-over-glass scene wants.
//
// Draws arrive already sorted back to front; Engine.buildDrawList does it,
// because that is where the camera is. A game driving the renderer directly
// sorts its own, exactly as it already has to for the opaque path's batching.
//
// Alpha travels in sunDir.w rather than in tint.w, which is already a mode
// selector — positive emissive, negative flat-shaded foliage. The push-constant
// block is full at 256 bytes, so there was nowhere to put a fifth vec4, and
// sunDir.w is padding on this path: grass, the grass impostors and water each
// borrow that slot, and none of them is drawn by lit.frag.
func recordTranslucent(
	deviceDriver core1_0.DeviceDriver,
	stats *RenderStats,
	cmdBuf core1_0.CommandBuffer,
	translucentPipeline core1_0.Pipeline,
	translucentDoubleSidedPipeline core1_0.Pipeline,
	litPipelineLayout core1_0.PipelineLayout,
	viewport core1_0.Viewport,
	scissor core1_0.Rect2D,
	draws []RenderObject,
	lighting SceneLighting,
	fallbackTexture *Texture,
	shadowDS core1_0.DescriptorSet,
) {
	var lastTex *Texture
	bound := false
	currentDoubleSided := false

	for i := range draws {
		d := &draws[i]
		if d.ShadowOnly || !d.IsTranslucent() {
			continue
		}

		if !bound || d.DoubleSided != currentDoubleSided {
			p := translucentPipeline
			if d.DoubleSided {
				p = translucentDoubleSidedPipeline
			}
			deviceDriver.CmdBindPipeline(cmdBuf, core1_0.PipelineBindPointGraphics, p)
			deviceDriver.CmdSetViewport(cmdBuf, viewport)
			deviceDriver.CmdSetScissor(cmdBuf, scissor)
			currentDoubleSided = d.DoubleSided
			// The pipeline bind invalidates nothing about descriptors, but the
			// cache below is keyed on "since we last bound", so reset it with
			// the pipeline rather than tracking two lifetimes.
			lastTex = nil
			bound = true
		}

		tex := d.Texture
		if tex == nil {
			tex = fallbackTexture
		}
		if tex != lastTex {
			deviceDriver.CmdBindDescriptorSets(cmdBuf, core1_0.PipelineBindPointGraphics, litPipelineLayout, 0,
				[]core1_0.DescriptorSet{tex.DescriptorSet, shadowDS}, nil)
			lastTex = tex
		}

		deviceDriver.CmdBindVertexBuffers(cmdBuf, 0, []core1_0.Buffer{d.Mesh.vertexBuffer}, []int{0})

		var pc [64]float32
		copy(pc[:16], d.MVP[:])
		copy(pc[16:32], d.Model[:])
		pc[32] = d.Color[0]
		pc[33] = d.Color[1]
		pc[34] = d.Color[2]
		pc[35] = 0.0
		if d.Emissive {
			pc[35] = 1.0
		} else if d.DoubleSided {
			pc[35] = -1.0 // flat shading, same as the opaque path
		}
		packLightingPC(&pc, lighting)
		pc[39] = d.Alpha // sunDir.w — packLightingPC leaves it as padding
		roughness := d.Roughness
		if roughness == 0 {
			roughness = 0.5
		}
		pc[51] = roughness
		pc[55] = d.Metallic
		pcBytes := unsafe.Slice((*byte)(unsafe.Pointer(&pc[0])), pushConstantSize)
		deviceDriver.CmdPushConstants(cmdBuf, litPipelineLayout, core1_0.StageVertex|core1_0.StageFragment, 0, pcBytes)

		stats.addDraw(1, d.Mesh.IndexCount, d.Mesh.VertexCount)
		if d.Mesh.IndexCount > 0 {
			deviceDriver.CmdBindIndexBuffer(cmdBuf, d.Mesh.indexBuffer, 0, d.Mesh.indexType)
			deviceDriver.CmdDrawIndexed(cmdBuf, d.Mesh.IndexCount, 1, 0, 0, 0)
		} else {
			deviceDriver.CmdDraw(cmdBuf, d.Mesh.VertexCount, 1, 0, 0)
		}
	}
}
