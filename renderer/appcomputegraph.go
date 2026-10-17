package renderer

import (
	"slices"

	"github.com/derekmwright/glyphengine/renderer/framegraph"
	"github.com/vkngwrapper/core/v3/core1_0"
)

func (r *Renderer) appendComputeGraph(f *frameGraph, p *AppCompute, group int, appendNode func(framegraph.Node, graphNode)) {
	n := framegraph.Node{Name: p.desc.Name, Kind: framegraph.Compute, OptionalGroup: group, Timed: p.desc.Timed}
	if p.desc.ReadsShadows {
		// Declared first so the barrier it derives heads the dispatch's entry
		// group: the cascade depth writes made visible to this shader's reads.
		// No return edge -- the map is not an application target, and nothing in
		// the graph touches it again before the next frame's cascade passes.
		n.Uses = append(n.Uses, framegraph.Use{Resource: f.sunShadow, Access: framegraph.DepthSampledRead})
	}
	for _, b := range p.desc.Buffers {
		n.Uses = append(n.Uses, framegraph.Use{Resource: f.storage[b], Access: framegraph.StorageReadWrite})
	}
	read := func(t *Texture, levels framegraph.LevelRange) {
		if t == nil || t.destroyed {
			return
		}
		var id framegraph.ResourceID
		if t.target != nil && !t.target.destroyed {
			id = f.targets[t.target].read
		} else if t.scene == r {
			id = f.hdr
			if t.sceneDepth {
				id = f.resolvedDepth
			}
		} else {
			return
		}
		if !covered(n.Uses, id, levels) {
			n.Uses = append(n.Uses, framegraph.Use{Resource: id, Access: framegraph.SampledRead, Levels: levels})
		}
	}
	for i, t := range p.desc.Reads {
		read(t, graphLevels(p.desc.ReadRanges, i))
	}
	for _, t := range r.shaderTargets {
		if t != nil && !t.destroyed && (!slices.Contains(p.desc.Writes, t) || t.desc.History) {
			read(t.Texture(), framegraph.LevelRange{})
		}
	}
	var rest []framegraph.Use
	for i, t := range p.desc.Writes {
		id := f.targets[t].write
		access := framegraph.StorageWrite
		// A chain read and written without History is read through a range
		// that excludes the written level (validateAppOverlap), so the read is
		// its own use and this one writes only.
		if slices.Contains(p.desc.Reads, t.Texture()) && (t.Levels() <= 1 || t.desc.History) {
			access = framegraph.StorageReadWrite
		}
		// A storage view is one level, so on a chain the write is exactly
		// that level and the barriers in and out of General name only it.
		var levels framegraph.LevelRange
		if t.Levels() > 1 {
			level := 0
			if i < len(p.desc.WriteLevels) {
				level = p.desc.WriteLevels[i]
			}
			levels = framegraph.LevelRange{Base: uint32(level), Count: 1}
		}
		n.Uses = append(n.Uses, framegraph.Use{Resource: id, Access: access, Levels: levels})
		rest = append(rest, framegraph.Use{Resource: id, Access: framegraph.SampledRead, Levels: levels,
			Stages: core1_0.PipelineStageVertexShader | core1_0.PipelineStageFragmentShader | core1_0.PipelineStageComputeShader})
	}
	appendNode(n, graphNode{name: p.desc.Name, app: p.pass, record: f.appRecorder(p.pass, p.record), enabled: p.active, begin: -1, end: -1, resolve: -1})
	// Storage writes enter General. Keep their derived return to sampled layout
	// in the same optional group so disabling a dispatch also skips both edges.
	appendNode(framegraph.Node{Name: p.desc.Name + " sampled outputs", Kind: framegraph.Transfer, OptionalGroup: group, Uses: rest},
		graphNode{name: p.desc.Name + " sampled outputs", enabled: p.active, begin: -1, end: -1, resolve: -1})
}
