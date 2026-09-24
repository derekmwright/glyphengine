package framegraph

import (
	"fmt"
	"math"

	"github.com/vkngwrapper/core/v3/core1_0"
)

// BuildError identifies the declaration and the violated rule, including errors
// discovered before walking execution. Image-only errors name node "<images>".
type BuildError struct {
	Node, Resource, Rule string
}

func (e *BuildError) Error() string {
	return fmt.Sprintf("framegraph: node %q resource %q: %s", e.Node, e.Resource, e.Rule)
}

type compiledUse struct {
	Use
	resolve bool
}

type resourceUses struct {
	usage       core1_0.ImageUsageFlags
	present     bool
	onlyResolve bool
	used        bool
	lastLayout  core1_0.ImageLayout
}

type imageState struct {
	layout        core1_0.ImageLayout
	stage         core1_0.PipelineStageFlags
	access        core1_0.AccessFlags
	pass          bool
	writeStage    core1_0.PipelineStageFlags
	visibleStage  core1_0.PipelineStageFlags
	visibleAccess core1_0.AccessFlags
}

// Build validates declarations first, then walks them in order. It owns every
// output slice, so mutating an old plan cannot influence another build.
func (g *Graph) Build() (*Plan, error) {
	uses, summaries, err := g.validate()
	if err != nil {
		return nil, err
	}
	if err := g.validateReads(uses); err != nil {
		return nil, err
	}
	p := &Plan{
		Steps: make([]Step, len(g.nodes)), Resources: make([]ResourceInfo, len(g.images)),
		Timed: make([]NodeID, 0), FinalBarriers: make([]Barrier, 0),
	}
	states := make([]imageState, len(g.images))
	groupEntry := make([]imageState, len(g.images))
	groupTouched := make([]bool, len(g.images))
	for i, d := range g.images {
		if b, ok := g.buffers[ResourceID(i)]; ok {
			for _, node := range uses {
				for _, u := range node {
					if int(u.Resource) == i {
						b.Usage |= bufferUsage(u.Access)
					}
				}
			}
			p.Resources[i] = ResourceInfo{Buffer: true, BufferDesc: b}
			if b.Persistent || b.Imported {
				states[i] = imageState{stage: core1_0.PipelineStageAllCommands, access: core1_0.AccessMemoryRead | core1_0.AccessMemoryWrite,
					writeStage: core1_0.PipelineStageAllCommands}
			}
			continue
		}
		s := summaries[i]
		d.Usage |= s.usage
		d.Instances = max(d.Instances, 1)
		d.Layers = max(d.Layers, 1)
		if d.Extent.Fixed != [2]uint32{} {
			d.Extent.Scale, d.Extent.RoundUp = 0, false
		}
		if s.onlyResolve && s.used && d.Samples != core1_0.Samples1 &&
			d.Usage & ^(core1_0.ImageUsageColorAttachment|core1_0.ImageUsageTransientAttachment) == 0 {
			d.Usage |= core1_0.ImageUsageTransientAttachment
		}
		resting := restingLayout(s)
		p.Resources[i] = ResourceInfo{Desc: d, Resting: resting, Prime: d.Persistent && !d.Imported && s.used}
		layout := core1_0.ImageLayoutUndefined
		if d.Persistent {
			layout = resting
		}
		if d.Imported {
			layout = d.InitialLayout
		}
		states[i] = stateForLayout(layout)
	}
	for ni, n := range g.nodes {
		if g.groupStarts(ni) {
			copy(groupEntry, states)
			clear(groupTouched)
		}
		step := Step{Node: NodeID(ni), Barriers: make([]Barrier, 0)}
		if n.Timed {
			p.Timed = append(p.Timed, NodeID(ni))
		}
		if n.Kind == Graphics {
			step.RenderPass = g.renderPass(ni, uses, p.Resources, states)
			n.Dependencies = step.RenderPass.Dependencies
		}
		for _, u := range uses[ni] {
			r := &p.Resources[u.Resource]
			before := states[u.Resource]
			if n.OptionalGroup != 0 {
				groupTouched[u.Resource] = true
			}
			after := accessState(n.Kind, u.Use)
			if r.Buffer {
				after = bufferUse(&step, u, before, after, r.BufferDesc.Size, n.Kind != Legacy)
				if n.Optional && n.OptionalGroup == 0 {
					after = optionalBufferState(before, after)
				}
				states[u.Resource] = after
				continue
			}
			if attachment(u.Access) {
				after.layout = r.Resting
				after.pass = true
			}
			if n.Kind == Legacy && u.FinalLayout != core1_0.ImageLayoutUndefined {
				after.layout = u.FinalLayout
			}
			if n.Optional && n.OptionalGroup == 0 && before.layout != after.layout {
				return nil, g.fail(n.Name, u.Resource, "optional node must be layout-neutral")
			}
			barrierBefore := before
			if u.Discard && u.Access == TransferDst {
				barrierBefore = stateForLayout(core1_0.ImageLayoutUndefined)
			}
			if n.Kind != Legacy && !attachment(u.Access) && needsBarrier(n, u, barrierBefore, after) {
				step.Barriers = append(step.Barriers, barrier(u.Resource, barrierBefore, after))
			}
			if !writes(u.Access) && before.layout == after.layout && before.access == after.access {
				// A later writer must wait for every reader, including readers in
				// different shader stages which needed no barrier between them.
				after.stage |= before.stage
			}
			if n.Optional && n.OptionalGroup == 0 {
				after = optionalState(before, after)
			}
			states[u.Resource] = after
		}
		if g.groupEnds(ni) {
			for id, touched := range groupTouched {
				if !touched {
					continue
				}
				if groupEntry[id].layout != states[id].layout {
					return nil, g.fail(n.Name, ResourceID(id), fmt.Sprintf("optional group %d must be layout-neutral", n.OptionalGroup))
				}
				if p.Resources[id].Buffer {
					states[id] = optionalBufferState(groupEntry[id], states[id])
				} else {
					states[id] = optionalState(groupEntry[id], states[id])
				}
			}
		}
		groupUndefinedBarriers(step.Barriers)
		p.Steps[ni] = step
	}
	for i, s := range states {
		if summaries[i].used && s.layout != p.Resources[i].Resting {
			p.FinalBarriers = append(p.FinalBarriers, barrier(ResourceID(i), s, stateForLayout(p.Resources[i].Resting)))
		}
	}
	groupUndefinedBarriers(p.FinalBarriers)
	return p, nil
}

func (g *Graph) groupStarts(ni int) bool {
	id := g.nodes[ni].OptionalGroup
	return id != 0 && (ni == 0 || g.nodes[ni-1].OptionalGroup != id)
}

func (g *Graph) groupEnds(ni int) bool {
	id := g.nodes[ni].OptionalGroup
	return id != 0 && (ni+1 == len(g.nodes) || g.nodes[ni+1].OptionalGroup != id)
}

// Contents and layouts are separate facts: a skipped group leaves the same
// layouts, but its writes cannot initialize contents on the skipped path.
func (g *Graph) validateReads(uses [][]compiledUse) error {
	written := make([]bool, len(g.images))
	entry := make([]bool, len(g.images))
	for i, d := range g.images {
		written[i] = d.Persistent || (d.Imported && d.InitialLayout != core1_0.ImageLayoutUndefined)
		if b, ok := g.buffers[ResourceID(i)]; ok {
			written[i] = b.Persistent || b.Imported
		}
	}
	for ni, n := range g.nodes {
		if g.groupStarts(ni) {
			copy(entry, written)
		}
		for _, u := range uses[ni] {
			if reads(u.Access) && !written[u.Resource] {
				return g.fail(n.Name, u.Resource, "read before a guaranteed write of transient contents")
			}
			if writes(u.Access) && (!n.Optional || n.OptionalGroup != 0) {
				written[u.Resource] = true
			}
		}
		if g.groupEnds(ni) {
			copy(written, entry)
		}
	}
	return nil
}

func optionalState(before, after imageState) imageState {
	// Retain every reader for a later writer. A later sampler only needs the
	// writers made available; a skipped path containing reads adds no RAW hazard.
	after.pass = (after.pass || after.writeStage == 0) && (before.pass || before.writeStage == 0)
	after.writeStage |= before.writeStage
	after.stage |= before.stage
	after.access |= before.access
	return after
}

func (g *Graph) fail(node string, id ResourceID, rule string) error {
	name := fmt.Sprintf("#%d", id)
	if id >= 0 && int(id) < len(g.images) {
		name = g.images[id].Name
	}
	return &BuildError{Node: node, Resource: name, Rule: rule}
}

func (g *Graph) validate() ([][]compiledUse, []resourceUses, error) {
	all := make([][]compiledUse, len(g.nodes))
	summaries := make([]resourceUses, len(g.images))
	groups := make(map[int]bool)
	for i, d := range g.images {
		if b, ok := g.buffers[ResourceID(i)]; ok {
			if b.Size <= 0 {
				return nil, nil, g.fail("<buffers>", ResourceID(i), "buffer size must be positive")
			}
			continue
		}
		fail := func(rule string) ([][]compiledUse, []resourceUses, error) {
			return nil, nil, g.fail("<images>", ResourceID(i), rule)
		}
		if !validExtent(d.Extent) {
			return fail("extent needs two fixed dimensions or a positive finite scale")
		}
		if d.Samples <= 0 || d.Samples > core1_0.Samples64 || d.Samples&(d.Samples-1) != 0 {
			return fail("samples must be one supported sample-count bit")
		}
		if d.Instances < 0 || (d.Layers > 1 && !d.Imported) {
			return fail("instances must be nonnegative; image arrays must be imported")
		}
		summaries[i].onlyResolve = true
	}
	for ni, n := range g.nodes {
		if g.groupStarts(ni) {
			if groups[n.OptionalGroup] {
				id := ResourceID(-1)
				if len(n.Uses) != 0 {
					id = n.Uses[0].Resource
				}
				return nil, nil, g.fail(n.Name, id, fmt.Sprintf("optional group %d must be contiguous", n.OptionalGroup))
			}
			groups[n.OptionalGroup] = true
		}
		if n.Kind < Graphics || n.Kind > Legacy {
			return nil, nil, g.fail(n.Name, -1, "unknown node kind")
		}
		if n.Kind != Graphics && n.Dependencies != nil {
			return nil, nil, g.fail(n.Name, -1, "dependencies require a graphics node")
		}
		for _, u := range n.Uses {
			if u.Resource < 0 || int(u.Resource) >= len(g.images) {
				return nil, nil, g.fail(n.Name, u.Resource, "unknown resource")
			}
			if u.Access < SampledRead || u.Access > VertexRead {
				return nil, nil, g.fail(n.Name, u.Resource, "unknown access")
			}
			_, buffer := g.buffers[u.Resource]
			if (buffer && bufferUsage(u.Access) == 0) || (!buffer && (u.Access == VertexRead || u.Access == IndirectRead)) {
				return nil, nil, g.fail(n.Name, u.Resource, "access incompatible with resource kind")
			}
			if buffer && (u.HasResolve || u.FinalLayout != core1_0.ImageLayoutUndefined) {
				return nil, nil, g.fail(n.Name, u.Resource, "buffers have no image layout or resolve")
			}
			if (u.Clear != nil && u.Access != ColorWrite && u.Access != DepthWrite) ||
				(u.Discard && u.Access != ColorWrite && u.Access != DepthWrite && u.Access != TransferDst) {
				return nil, nil, g.fail(n.Name, u.Resource, "clear/discard requires a non-loading attachment; TransferDst also permits discard")
			}
			if attachment(u.Access) && n.Kind != Graphics && n.Kind != Legacy {
				return nil, nil, g.fail(n.Name, u.Resource, "attachments require graphics or legacy nodes")
			}
			all[ni] = append(all[ni], compiledUse{Use: u})
			if u.HasResolve {
				if (u.Access != ColorWrite && u.Access != ColorLoadWrite) || g.images[u.Resource].Samples == core1_0.Samples1 {
					return nil, nil, g.fail(n.Name, u.Resource, "resolve requires a multisampled colour attachment")
				}
				if u.ResolveTo < 0 || int(u.ResolveTo) >= len(g.images) {
					return nil, nil, g.fail(n.Name, u.ResolveTo, "unknown resolve target")
				}
				d, dst := g.images[u.Resource], g.images[u.ResolveTo]
				if _, buffer := g.buffers[u.ResolveTo]; buffer || dst.Samples != core1_0.Samples1 || dst.Format != d.Format || !sameExtent(dst.Extent, d.Extent) {
					return nil, nil, g.fail(n.Name, u.ResolveTo, "resolve target must be single-sample with matching format and extent")
				}
				all[ni] = append(all[ni], compiledUse{Use: Use{Resource: u.ResolveTo, Access: ColorWrite, Stages: u.Stages}, resolve: true})
			}
		}
		seen := make(map[ResourceID]Access)
		firstAttachment, depth := ResourceID(-1), false
		for _, u := range all[ni] {
			d := g.images[u.Resource]
			s := &summaries[u.Resource]
			if s.present {
				return nil, nil, g.fail(n.Name, u.Resource, "Present must be unique and the last use")
			}
			if u.Access == Present && !d.Imported {
				return nil, nil, g.fail(n.Name, u.Resource, "Present requires an imported image")
			}
			if old, ok := seen[u.Resource]; ok && (writes(old) || writes(u.Access) ||
				(g.buffers[u.Resource].Size == 0 && accessState(n.Kind, Use{Access: old}).layout != accessState(n.Kind, u.Use).layout)) {
				return nil, nil, g.fail(n.Name, u.Resource, "same-node read/write, repeated write, or incompatible read layouts; use a load form or StorageReadWrite")
			}
			seen[u.Resource] = u.Access
			if attachment(u.Access) {
				want := core1_0.ImageAspectColor
				if isDepth(u.Access) {
					want = core1_0.ImageAspectDepth
				}
				if d.Aspect&want == 0 {
					return nil, nil, g.fail(n.Name, u.Resource, "attachment aspect does not match access")
				}
				if n.Kind == Graphics && !u.resolve {
					if firstAttachment >= 0 {
						first := g.images[firstAttachment]
						if !sameExtent(d.Extent, first.Extent) || d.Samples != first.Samples {
							return nil, nil, g.fail(n.Name, u.Resource, "attachments must agree on extent and samples")
						}
					}
					firstAttachment = u.Resource
					if isDepth(u.Access) && depth {
						return nil, nil, g.fail(n.Name, u.Resource, "one depth attachment per subpass")
					}
					depth = depth || isDepth(u.Access)
				}
			}
			s.used = true
			s.present = u.Access == Present
			s.usage |= usageFor(u.Access)
			s.onlyResolve = s.onlyResolve && u.HasResolve
			s.lastLayout = accessState(n.Kind, u.Use).layout
		}
		if n.AttachmentOrder != nil {
			if n.Kind != Graphics {
				return nil, nil, g.fail(n.Name, -1, "attachment order requires a graphics node")
			}
			attachments := make(map[ResourceID]bool)
			for _, u := range all[ni] {
				if attachment(u.Access) {
					attachments[u.Resource] = true
				}
			}
			if len(n.AttachmentOrder) != len(attachments) {
				return nil, nil, g.fail(n.Name, -1, "attachment order must list every attachment exactly once")
			}
			for _, id := range n.AttachmentOrder {
				if !attachments[id] {
					return nil, nil, g.fail(n.Name, id, "attachment order contains an unknown or duplicate attachment")
				}
				delete(attachments, id)
			}
		}
	}
	return all, summaries, nil
}

func validExtent(e Extent) bool {
	if e.Fixed != [2]uint32{} {
		return e.Fixed[0] != 0 && e.Fixed[1] != 0
	}
	return e.Scale > 0 && !math.IsInf(float64(e.Scale), 0) && !math.IsNaN(float64(e.Scale))
}

func sameExtent(a, b Extent) bool {
	if a.Fixed != [2]uint32{} || b.Fixed != [2]uint32{} {
		return a.Fixed == b.Fixed
	}
	return a == b
}

func restingLayout(s resourceUses) core1_0.ImageLayout {
	switch {
	case s.present:
		return ImageLayoutPresentSrc
	case s.usage&core1_0.ImageUsageSampled != 0:
		return core1_0.ImageLayoutShaderReadOnlyOptimal
	case s.usage&core1_0.ImageUsageDepthStencilAttachment != 0:
		return core1_0.ImageLayoutDepthStencilAttachmentOptimal
	case s.usage&core1_0.ImageUsageStorage != 0:
		return core1_0.ImageLayoutGeneral
	case s.usage&core1_0.ImageUsageColorAttachment != 0:
		return core1_0.ImageLayoutColorAttachmentOptimal
	default:
		// Copy-only images have no attachment or shader layout to return to.
		return s.lastLayout
	}
}

func attachment(a Access) bool { return a >= ColorWrite && a <= DepthLoadWrite }
func isDepth(a Access) bool    { return a == DepthWrite || a == DepthLoadWrite }
func reads(a Access) bool {
	return a == SampledRead || a == StorageRead || a == StorageReadWrite || a == ColorLoadWrite || a == DepthLoadWrite || a == TransferSrc || a == Present || a == VertexRead || a == IndirectRead
}
func writes(a Access) bool {
	return attachment(a) || a == StorageWrite || a == StorageReadWrite || a == TransferDst
}

func usageFor(a Access) core1_0.ImageUsageFlags {
	switch a {
	case SampledRead:
		return core1_0.ImageUsageSampled
	case StorageRead, StorageWrite, StorageReadWrite:
		return core1_0.ImageUsageStorage
	case ColorWrite, ColorLoadWrite:
		return core1_0.ImageUsageColorAttachment
	case DepthWrite, DepthLoadWrite:
		return core1_0.ImageUsageDepthStencilAttachment
	case TransferSrc:
		return core1_0.ImageUsageTransferSrc
	case TransferDst:
		return core1_0.ImageUsageTransferDst
	default:
		return 0
	}
}
