package renderer

import (
	"github.com/vkngwrapper/core/v3/core1_0"
)

// recordInstanced draws every InstanceSet in the draw list, one CmdDrawIndexed
// per set.
//
// This is the whole feature. A set of 900 habitat domes is one draw call, one
// push-constant upload and one frustum test, against 900 of each on the
// ordinary path. The GPU cost barely moves either way -- 900 domes of 200
// triangles is 180k triangles, which is nothing -- and the CPU cost of
// recording them is what disappears.
//
// It runs inside the opaque pass, after the individual lit draws, so instanced
// props depth-test against everything else exactly as they would if each had
// been drawn on its own.
//
// The push constants carry the view-projection in the slot the ordinary path
// uses for view-projection-model, and lit_instanced.vert does the model
// multiply from its per-instance attribute. Ordinary sets leave the model slot
// unused; LOD draws store their coverage phase and optional atlas framing there.
func recordInstanced(
	deviceDriver core1_0.DeviceDriver,
	stats *RenderStats,
	cmdBuf core1_0.CommandBuffer,
	instancedPipeline core1_0.Pipeline,
	instancedDoubleSidedPipeline core1_0.Pipeline,
	litPipelineLayout core1_0.PipelineLayout,
	viewport core1_0.Viewport,
	scissor core1_0.Rect2D,
	draws []RenderObject,
	lighting SceneLighting,
	fallbackTexture *Texture,
	shadowDS core1_0.DescriptorSet,
	scratch *commandScratch,
) {
	bound := false
	currentDoubleSided := false
	var currentPipeline core1_0.Pipeline
	var lastTex *Texture

	for i := range draws {
		d := &draws[i]
		set := d.Instances
		if set == nil || set.count == 0 || d.ShadowOnly {
			continue
		}

		p := instancedPipeline
		if d.DoubleSided {
			p = instancedDoubleSidedPipeline
		}
		var atlas *ImpostorAtlas
		if set.lod != nil {
			kind := 0
			if d.DoubleSided {
				kind = 1
			}
			if set.lodLevel == len(set.lod.levels) {
				kind = 2
				atlas = set.lod.impostor
			}
			p = set.lod.owner.lodPipelines[kind]
		}
		if !bound || d.DoubleSided != currentDoubleSided || currentPipeline != p {
			currentPipeline = p
			deviceDriver.CmdBindPipeline(cmdBuf, core1_0.PipelineBindPointGraphics, p)
			scratch.setViewport(deviceDriver, cmdBuf, viewport)
			scratch.setScissor(deviceDriver, cmdBuf, scissor)
			currentDoubleSided = d.DoubleSided
			lastTex = nil
			bound = true
		}

		tex := d.Texture
		if atlas != nil {
			tex = &atlas.texture
		}
		if tex == nil {
			tex = fallbackTexture
		}
		if tex != lastTex {
			scratch.bindDescriptorSets(deviceDriver, cmdBuf, core1_0.PipelineBindPointGraphics, litPipelineLayout, 0, tex.DescriptorSet, shadowDS)
			lastTex = tex
		}

		// Binding 0 is the mesh, binding 1 is the placements.
		if atlas != nil {
			scratch.bindVertexBuffers(deviceDriver, cmdBuf, 1, set.buffer)
		} else {
			scratch.bindVertexBuffers(deviceDriver, cmdBuf, 0, set.Mesh.vertexBuffer, set.buffer)
		}

		// Model transforms come from attributes. Ordinary sets leave the second
		// matrix zero; LOD draws use it for coverage phase and atlas framing.
		scratch.resetPC()
		copy(scratch.pc[:16], lighting.VP[:]) // view-projection, not MVP
		scratch.pc[32] = d.Color[0]
		scratch.pc[33] = d.Color[1]
		scratch.pc[34] = d.Color[2]
		scratch.pc[35] = 0.0
		if d.Emissive {
			scratch.pc[35] = 1.0
		} else if d.DoubleSided {
			scratch.pc[35] = -1.0
		}
		packLightingPC(&scratch.pc, lighting)
		roughness := d.Roughness
		if roughness == 0 {
			roughness = 0.5
		}
		scratch.pc[51] = roughness
		scratch.pc[55] = d.Metallic
		if set.lod != nil {
			// Opposite phases fill the neighbour's discarded pixels. At the
			// 25-lod-forest 40-unit crossing (frames 60/61, 1280x720 MSAA4),
			// width 6 changes 0.069279/255 MAE versus 3.698333 for width 0;
			// task lod also requires visible hard-switch pixels, not just a ratio.
			scratch.pc[31] = float32(set.lodLevel % 2)
		}
		if atlas != nil {
			copy(scratch.pc[16:19], atlas.center[:])
			scratch.pc[19] = atlas.radius
			scratch.pc[20] = 0.5 / float32(atlas.size)
		}
		scratch.pushConstants(deviceDriver, cmdBuf, litPipelineLayout, core1_0.StageVertex|core1_0.StageFragment)

		if atlas != nil {
			stats.addDraw(set.count, 0, 6)
			deviceDriver.CmdDraw(cmdBuf, 6, set.count, 0, 0)
			continue
		}
		stats.addDraw(set.count, set.Mesh.IndexCount, set.Mesh.VertexCount)
		if set.Mesh.IndexCount > 0 {
			deviceDriver.CmdBindIndexBuffer(cmdBuf, set.Mesh.indexBuffer, 0, set.Mesh.indexType)
			deviceDriver.CmdDrawIndexed(cmdBuf, set.Mesh.IndexCount, set.count, 0, 0, 0)
		} else {
			deviceDriver.CmdDraw(cmdBuf, set.Mesh.VertexCount, set.count, 0, 0)
		}
	}
}

// recordInstancedShadow draws instance sets into one cascade or point-light face.
//
// Instanced geometry needs its own path here or it silently stops casting: the
// draw would still record against shadow.vert, which takes the model matrix
// from a push constant the instanced path does not write, and every instance
// would land on top of the first one. That is the failure mode the design
// called out, and it is invisible in a still frame of a scene whose props
// happen to sit where their shadows would.
//
// Culling is per submitted set, so a set with one dome inside the cascade draws
// all its placements. LOD supplies only the chosen main-camera-visible bucket;
// neither path uploads a separate visible subset for each shadow view.
func recordInstancedShadow(
	deviceDriver core1_0.DeviceDriver,
	stats *RenderStats,
	cmdBuf core1_0.CommandBuffer,
	pipeline core1_0.Pipeline,
	layout core1_0.PipelineLayout,
	viewport core1_0.Viewport,
	scissor core1_0.Rect2D,
	draws []RenderObject,
	cascadeVP [16]float32,
	cascadeFrustum Frustum,
	scratch *commandScratch,
) {
	bound := false
	for i := range draws {
		d := &draws[i]
		set := d.Instances
		if set == nil || set.count == 0 {
			continue
		}
		if d.Emissive || d.NoCastShadow || d.IsTranslucent() {
			continue
		}
		if set.boundRadius > 0 &&
			!cascadeFrustum.SphereInFrustum(set.boundCenter[0], set.boundCenter[1], set.boundCenter[2], set.boundRadius) {
			continue
		}

		if !bound {
			deviceDriver.CmdBindPipeline(cmdBuf, core1_0.PipelineBindPointGraphics, pipeline)
			scratch.setViewport(deviceDriver, cmdBuf, viewport)
			scratch.setScissor(deviceDriver, cmdBuf, scissor)
			bound = true
		}

		scratch.bindVertexBuffers(deviceDriver, cmdBuf, 0, set.Mesh.vertexBuffer, set.buffer)

		// The instanced depth stage reads the first matrix as the cascade's
		// view-projection; the model comes from the instance attribute, so
		// [16:32) must read as zero -- explicit here because, unlike every
		// other shadowPC write, this one does not fill the whole array.
		scratch.shadowPC = [32]float32{}
		copy(scratch.shadowPC[:16], cascadeVP[:])
		scratch.pushShadowConstants(deviceDriver, cmdBuf, layout)

		stats.addDraw(set.count, set.Mesh.IndexCount, set.Mesh.VertexCount)
		if set.Mesh.IndexCount > 0 {
			deviceDriver.CmdBindIndexBuffer(cmdBuf, set.Mesh.indexBuffer, 0, set.Mesh.indexType)
			deviceDriver.CmdDrawIndexed(cmdBuf, set.Mesh.IndexCount, set.count, 0, 0, 0)
		} else {
			deviceDriver.CmdDraw(cmdBuf, set.Mesh.VertexCount, set.count, 0, 0)
		}
	}
}
