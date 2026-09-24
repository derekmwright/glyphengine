package renderer

import (
	"fmt"

	"github.com/derekmwright/glyphengine/renderer/framegraph"
	"github.com/vkngwrapper/core/v3/core1_0"
)

func (r *Renderer) appendGPULODGraph(f *frameGraph, g *framegraph.Graph, add func(framegraph.Node, graphNode)) {
	f.lodBuffers = make(map[framegraph.ResourceID]graphImage)
	for i, s := range r.lodSets {
		if s.gpu == nil || s.destroyed {
			continue
		}
		gpu := s.gpu
		if r.gpuLODTimer == nil {
			r.gpuLODTimer = &AppPass{desc: AppPassDesc{Name: "lodselect", Timed: true}}
		}
		f.lodTimer = r.gpuLODTimer
		resource := func(name string, buffers ...lodBuffer) framegraph.ResourceID {
			id := g.AddBuffer(framegraph.BufferDesc{Name: fmt.Sprintf("LOD %d %s", i, name), Size: buffers[0].size, Persistent: true})
			binding := graphImage{frameInstance: len(buffers) > 1}
			for _, b := range buffers {
				binding.buffers = append(binding.buffers, b.buffer)
			}
			f.lodBuffers[id] = binding
			return id
		}
		placements := resource("placements", gpu.placements)
		output := resource("buckets", gpu.output[:]...)
		commands := resource("commands", gpu.commands[:]...)
		readback := resource("readback", gpu.readback[:]...)
		group := len(r.appPasses) + 2 + i
		node := func(name string, kind framegraph.NodeKind, uses []framegraph.Use, record func(*graphFrame)) {
			add(framegraph.Node{Name: name, Kind: kind, OptionalGroup: group, Uses: uses}, graphNode{name: name, enabled: s.gpuActive, record: record, begin: -1, end: -1, resolve: -1})
		}
		scratch := resource("scan", gpu.scratch[:]...)
		node("LOD classify", framegraph.Compute, []framegraph.Use{{Resource: placements, Access: framegraph.StorageRead}, {Resource: scratch, Access: framegraph.StorageWrite}}, func(c *graphFrame) { s.dispatchGPU(c, 0) })
		node("LOD prefix", framegraph.Compute, []framegraph.Use{{Resource: scratch, Access: framegraph.StorageReadWrite}, {Resource: commands, Access: framegraph.StorageWrite}}, func(c *graphFrame) { s.dispatchGPU(c, 1) })
		node("LOD scatter", framegraph.Compute, []framegraph.Use{{Resource: placements, Access: framegraph.StorageRead}, {Resource: scratch, Access: framegraph.StorageRead}, {Resource: output, Access: framegraph.StorageWrite}}, func(c *graphFrame) { s.dispatchGPU(c, 2) })
		node("LOD counts", framegraph.Transfer, []framegraph.Use{{Resource: commands, Access: framegraph.TransferSrc}, {Resource: readback, Access: framegraph.TransferDst}}, func(c *graphFrame) {
			if err := c.driver.CmdCopyBuffer(c.cmd, gpu.commands[c.frame].buffer, gpu.readback[c.frame].buffer, gpu.copy[:]...); err != nil {
				panic(err)
			}
			gpu.readValid[c.frame] = true
			gpu.lastFrame = c.frame
		})
		// This synchronization-only step precedes the shadow and scene passes.
		node("LOD draw inputs", framegraph.Transfer, []framegraph.Use{{Resource: output, Access: framegraph.VertexRead}, {Resource: commands, Access: framegraph.IndirectRead}}, nil)
	}
	f.beforeShadows = len(f.nodes)
}

func (s *commandScratch) bindInstanceVertices(d core1_0.DeviceDriver, cmd core1_0.CommandBuffer, set *InstanceSet, atlas bool) {
	s.vertexOffsets = [2]int{0, set.offset}
	if atlas {
		s.vertexBufs[0] = set.buffer
		s.vertexOffsets[0] = set.offset
		d.CmdBindVertexBuffers(cmd, 1, s.vertexBufs[:1], s.vertexOffsets[:1])
	} else {
		s.vertexBufs[0], s.vertexBufs[1] = set.Mesh.vertexBuffer, set.buffer
		d.CmdBindVertexBuffers(cmd, 0, s.vertexBufs[:], s.vertexOffsets[:])
	}
}

func (s *InstanceSet) drawIndirect(d core1_0.DeviceDriver, cmd core1_0.CommandBuffer) {
	if s.Mesh != nil && s.Mesh.IndexCount > 0 {
		d.CmdBindIndexBuffer(cmd, s.Mesh.indexBuffer, 0, s.Mesh.indexType)
		d.CmdDrawIndexedIndirect(cmd, s.indirect, s.indirectOffset, 1, 20)
	} else {
		d.CmdDrawIndirect(cmd, s.indirect, s.indirectOffset, 1, 20)
	}
}
