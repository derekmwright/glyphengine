package renderer

import (
	"slices"

	"github.com/derekmwright/glyphengine/renderer/framegraph"
	"github.com/vkngwrapper/core/v3/core1_0"
)

func (r *Renderer) appendComputeGraph(f *frameGraph, p *AppCompute, group int, appendNode func(framegraph.Node, graphNode)) {
	n := framegraph.Node{Name: p.desc.Name, Kind: framegraph.Compute, OptionalGroup: group, Timed: p.desc.Timed}
	read := func(t *Texture) {
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
		for _, u := range n.Uses {
			if u.Resource == id {
				return
			}
		}
		n.Uses = append(n.Uses, framegraph.Use{Resource: id, Access: framegraph.SampledRead})
	}
	for _, t := range p.desc.Reads {
		read(t)
	}
	for _, t := range r.shaderTargets {
		if t != nil && !t.destroyed && (!slices.Contains(p.desc.Writes, t) || t.desc.History) {
			read(t.Texture())
		}
	}
	var rest []framegraph.Use
	for _, t := range p.desc.Writes {
		id := f.targets[t].write
		access := framegraph.StorageWrite
		if slices.Contains(p.desc.Reads, t.Texture()) {
			access = framegraph.StorageReadWrite
		}
		n.Uses = append(n.Uses, framegraph.Use{Resource: id, Access: access})
		rest = append(rest, framegraph.Use{Resource: id, Access: framegraph.SampledRead,
			Stages: core1_0.PipelineStageVertexShader | core1_0.PipelineStageFragmentShader | core1_0.PipelineStageComputeShader})
	}
	appendNode(n, graphNode{name: p.desc.Name, app: p.pass, record: p.record, enabled: p.active, begin: -1, end: -1, resolve: -1})
	// Storage writes enter General. Keep their derived return to sampled layout
	// in the same optional group so disabling a dispatch also skips both edges.
	appendNode(framegraph.Node{Name: p.desc.Name + " sampled outputs", Kind: framegraph.Transfer, OptionalGroup: group, Uses: rest},
		graphNode{name: p.desc.Name + " sampled outputs", enabled: p.active, begin: -1, end: -1, resolve: -1})
}
