package renderer

import (
	"github.com/vkngwrapper/core/v3/core1_0"
)

// recordTranslucent draws one half of the blended subset of the draw list.
//
// over selects the half: false is the group recorded inside the scene pass,
// before the water's refraction copy, and true is the group recorded after the
// water surface in the water pass. split decides which draw is which; with no
// water in the frame it puts everything in the first group and the second call
// never happens. See waterorder.go for the rule and what it approximates.
//
// The first group runs inside the scene render pass, after the sky and before
// particles. Both ends of that matter:
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
// Skinned meshes take the same path, through their own blended pipeline and
// their own descriptor layout. They have no double-sided twin, deliberately:
// the opaque skinned path has none either, so adding one would make a
// translucent character more capable than a solid one.
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
	skinnedTranslucentPipeline core1_0.Pipeline,
	litPipelineLayout core1_0.PipelineLayout,
	skinnedPipelineLayout core1_0.PipelineLayout,
	viewport core1_0.Viewport,
	scissor core1_0.Rect2D,
	draws []RenderObject,
	lighting SceneLighting,
	fallbackTexture *Texture,
	shadowDS core1_0.DescriptorSet,
	frame int,
	split blendSplit,
	over bool,
	scratch *commandScratch,
) {
	var lastTex *Texture
	var lastJoints *JointBuffer
	bound := false
	currentDoubleSided := false
	currentSkinned := false

	for i := range draws {
		d := &draws[i]
		if d.ShadowOnly || !d.IsTranslucent() {
			continue
		}
		// Filtering the sorted list rather than resorting each half is what
		// keeps the back-to-front order inside a group: a subsequence of a
		// sorted sequence is sorted.
		if cx, cy, cz := d.worldCenter(); !split.keep(over, cx, cy, cz) {
			continue
		}

		skinned := d.Joints != nil
		// Skinned draws have one blended pipeline and no double-sided twin, so
		// DoubleSided does not select between them; the opaque skinned path has
		// no double-sided variant either, and a translucent character should
		// not be more capable than a solid one.
		if !bound || skinned != currentSkinned || (!skinned && d.DoubleSided != currentDoubleSided) {
			var p core1_0.Pipeline
			switch {
			case skinned:
				p = skinnedTranslucentPipeline
			case d.DoubleSided:
				p = translucentDoubleSidedPipeline
			default:
				p = translucentPipeline
			}
			deviceDriver.CmdBindPipeline(cmdBuf, core1_0.PipelineBindPointGraphics, p)
			scratch.setViewport(deviceDriver, cmdBuf, viewport)
			scratch.setScissor(deviceDriver, cmdBuf, scissor)
			currentDoubleSided = d.DoubleSided
			currentSkinned = skinned
			// The pipeline bind invalidates nothing about descriptors, but the
			// cache below is keyed on "since we last bound", so reset it with
			// the pipeline rather than tracking two lifetimes.
			lastTex, lastJoints = nil, nil
			bound = true
		}

		tex := d.Texture
		if tex == nil {
			tex = fallbackTexture
		}
		activeLayout := litPipelineLayout
		if skinned {
			// Skinned: set 0 = tex, set 1 = joints, set 2 = shadow, the layout
			// the opaque skinned path has always used.
			activeLayout = skinnedPipelineLayout
			if tex != lastTex || d.Joints != lastJoints {
				scratch.bindDescriptorSets(deviceDriver, cmdBuf, core1_0.PipelineBindPointGraphics, activeLayout, 0,
					tex.DescriptorSet, d.Joints.descriptorSets[frame], shadowDS)
				lastTex, lastJoints = tex, d.Joints
			}
		} else if tex != lastTex {
			scratch.bindDescriptorSets(deviceDriver, cmdBuf, core1_0.PipelineBindPointGraphics, litPipelineLayout, 0,
				tex.DescriptorSet, shadowDS)
			lastTex = tex
		}

		scratch.bindVertexBuffers(deviceDriver, cmdBuf, 0, d.Mesh.vertexBuffer)

		scratch.resetPC()
		copy(scratch.pc[:16], d.MVP[:])
		copy(scratch.pc[16:32], d.Model[:])
		scratch.pc[32] = d.Color[0]
		scratch.pc[33] = d.Color[1]
		scratch.pc[34] = d.Color[2]
		scratch.pc[35] = 0.0
		if d.Emissive {
			scratch.pc[35] = 1.0
		} else if d.DoubleSided && !skinned {
			scratch.pc[35] = -1.0 // flat shading, same as the opaque path
		}
		packLightingPC(&scratch.pc, lighting)
		scratch.pc[39] = d.Alpha // sunDir.w — packLightingPC leaves it as padding
		roughness := d.Roughness
		if roughness == 0 {
			roughness = 0.5
		}
		scratch.pc[51] = roughness
		scratch.pc[55] = d.Metallic
		scratch.pushConstants(deviceDriver, cmdBuf, activeLayout, core1_0.StageVertex|core1_0.StageFragment)

		stats.addDraw(1, d.Mesh.IndexCount, d.Mesh.VertexCount)
		if d.Mesh.IndexCount > 0 {
			deviceDriver.CmdBindIndexBuffer(cmdBuf, d.Mesh.indexBuffer, 0, d.Mesh.indexType)
			deviceDriver.CmdDrawIndexed(cmdBuf, d.Mesh.IndexCount, 1, d.Mesh.firstIndex, d.Mesh.vertexOffset, 0)
		} else {
			deviceDriver.CmdDraw(cmdBuf, d.Mesh.VertexCount, 1, 0, 0)
		}
	}
}
