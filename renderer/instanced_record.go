package renderer

import (
	"unsafe"

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
// multiply from its per-instance attribute. The model slot itself goes unused
// here; there is no room in the 256-byte block to give the instanced path its
// own layout, and no need, since the fragment stage never read pc.model.
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
) {
	bound := false
	currentDoubleSided := false
	var lastTex *Texture

	for i := range draws {
		d := &draws[i]
		set := d.Instances
		if set == nil || set.count == 0 || d.ShadowOnly {
			continue
		}

		if !bound || d.DoubleSided != currentDoubleSided {
			p := instancedPipeline
			if d.DoubleSided {
				p = instancedDoubleSidedPipeline
			}
			deviceDriver.CmdBindPipeline(cmdBuf, core1_0.PipelineBindPointGraphics, p)
			deviceDriver.CmdSetViewport(cmdBuf, viewport)
			deviceDriver.CmdSetScissor(cmdBuf, scissor)
			currentDoubleSided = d.DoubleSided
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

		// Binding 0 is the mesh, binding 1 is the placements.
		deviceDriver.CmdBindVertexBuffers(cmdBuf, 0,
			[]core1_0.Buffer{set.Mesh.vertexBuffer, set.buffer}, []int{0, 0})

		var pc [64]float32
		copy(pc[:16], lighting.VP[:]) // view-projection, not MVP
		pc[32] = d.Color[0]
		pc[33] = d.Color[1]
		pc[34] = d.Color[2]
		pc[35] = 0.0
		if d.Emissive {
			pc[35] = 1.0
		} else if d.DoubleSided {
			pc[35] = -1.0
		}
		packLightingPC(&pc, lighting)
		roughness := d.Roughness
		if roughness == 0 {
			roughness = 0.5
		}
		pc[51] = roughness
		pc[55] = d.Metallic
		pcBytes := unsafe.Slice((*byte)(unsafe.Pointer(&pc[0])), pushConstantSize)
		deviceDriver.CmdPushConstants(cmdBuf, litPipelineLayout, core1_0.StageVertex|core1_0.StageFragment, 0, pcBytes)

		stats.addDraw(set.count, set.Mesh.IndexCount, set.Mesh.VertexCount)
		if set.Mesh.IndexCount > 0 {
			deviceDriver.CmdBindIndexBuffer(cmdBuf, set.Mesh.indexBuffer, 0, set.Mesh.indexType)
			deviceDriver.CmdDrawIndexed(cmdBuf, set.Mesh.IndexCount, set.count, 0, 0, 0)
		} else {
			deviceDriver.CmdDraw(cmdBuf, set.Mesh.VertexCount, set.count, 0, 0)
		}
	}
}

// recordInstancedShadow draws the instance sets into one shadow cascade.
//
// Instanced geometry needs its own path here or it silently stops casting: the
// draw would still record against shadow.vert, which takes the model matrix
// from a push constant the instanced path does not write, and every instance
// would land on top of the first one. That is the failure mode the design
// called out, and it is invisible in a still frame of a scene whose props
// happen to sit where their shadows would.
//
// Culling is per set rather than per instance, so a set with one dome inside
// the cascade draws all of them. The alternative is a visible subset uploaded
// per cascade per frame, which is the CPU cost this feature exists to remove.
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
			deviceDriver.CmdSetViewport(cmdBuf, viewport)
			deviceDriver.CmdSetScissor(cmdBuf, scissor)
			bound = true
		}

		deviceDriver.CmdBindVertexBuffers(cmdBuf, 0,
			[]core1_0.Buffer{set.Mesh.vertexBuffer, set.buffer}, []int{0, 0})

		// The instanced depth stage reads the first matrix as the cascade's
		// view-projection; the model comes from the instance attribute.
		var shadowPC [32]float32
		copy(shadowPC[:16], cascadeVP[:])
		pcBytes := unsafe.Slice((*byte)(unsafe.Pointer(&shadowPC[0])), 128)
		deviceDriver.CmdPushConstants(cmdBuf, layout, core1_0.StageVertex, 0, pcBytes)

		stats.addDraw(set.count, set.Mesh.IndexCount, set.Mesh.VertexCount)
		if set.Mesh.IndexCount > 0 {
			deviceDriver.CmdBindIndexBuffer(cmdBuf, set.Mesh.indexBuffer, 0, set.Mesh.indexType)
			deviceDriver.CmdDrawIndexed(cmdBuf, set.Mesh.IndexCount, set.count, 0, 0, 0)
		} else {
			deviceDriver.CmdDraw(cmdBuf, set.Mesh.VertexCount, set.count, 0, 0)
		}
	}
}
