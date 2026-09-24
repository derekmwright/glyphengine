package framegraph

import "github.com/vkngwrapper/core/v3/core1_0"

func accessState(kind NodeKind, u Use) imageState {
	s := imageState{}
	shaderStage := core1_0.PipelineStageFragmentShader
	if kind == Compute {
		shaderStage = core1_0.PipelineStageComputeShader
	}
	switch u.Access {
	case SampledRead:
		s.layout, s.stage, s.access = core1_0.ImageLayoutShaderReadOnlyOptimal, shaderStage, core1_0.AccessShaderRead
	case StorageRead, StorageWrite, StorageReadWrite:
		s.layout, s.stage = core1_0.ImageLayoutGeneral, shaderStage
		if reads(u.Access) {
			s.access |= core1_0.AccessShaderRead
		}
		if writes(u.Access) {
			s.access |= core1_0.AccessShaderWrite
		}
	case ColorWrite, ColorLoadWrite:
		s.layout, s.stage, s.access = core1_0.ImageLayoutColorAttachmentOptimal, core1_0.PipelineStageColorAttachmentOutput, core1_0.AccessColorAttachmentWrite
		if u.Access == ColorLoadWrite {
			s.access |= core1_0.AccessColorAttachmentRead
		}
	case DepthWrite, DepthLoadWrite:
		s.layout, s.stage, s.access = core1_0.ImageLayoutDepthStencilAttachmentOptimal,
			core1_0.PipelineStageEarlyFragmentTests|core1_0.PipelineStageLateFragmentTests, core1_0.AccessDepthStencilAttachmentWrite
		if u.Access == DepthLoadWrite {
			s.access |= core1_0.AccessDepthStencilAttachmentRead
		}
	case TransferSrc:
		s.layout, s.stage, s.access = core1_0.ImageLayoutTransferSrcOptimal, core1_0.PipelineStageTransfer, core1_0.AccessTransferRead
	case TransferDst:
		s.layout, s.stage, s.access = core1_0.ImageLayoutTransferDstOptimal, core1_0.PipelineStageTransfer, core1_0.AccessTransferWrite
	case Present:
		s.layout, s.stage = ImageLayoutPresentSrc, core1_0.PipelineStageBottomOfPipe
	case VertexRead:
		s.stage, s.access = core1_0.PipelineStageVertexInput, core1_0.AccessVertexAttributeRead
	case IndirectRead:
		s.stage, s.access = core1_0.PipelineStageDrawIndirect, core1_0.AccessIndirectCommandRead
	}
	if u.Stages != 0 {
		s.stage = u.Stages
	}
	if writes(u.Access) {
		s.writeStage = s.stage
	}
	return s
}

func stateForLayout(layout core1_0.ImageLayout) imageState {
	var a Access
	switch layout {
	case core1_0.ImageLayoutShaderReadOnlyOptimal:
		a = SampledRead
	case core1_0.ImageLayoutColorAttachmentOptimal:
		a = ColorLoadWrite
	case core1_0.ImageLayoutDepthStencilAttachmentOptimal:
		a = DepthLoadWrite
	case core1_0.ImageLayoutTransferSrcOptimal:
		a = TransferSrc
	case core1_0.ImageLayoutTransferDstOptimal:
		a = TransferDst
	case core1_0.ImageLayoutGeneral:
		a = StorageReadWrite
	case ImageLayoutPresentSrc:
		a = Present
	default:
		return imageState{layout: layout, stage: core1_0.PipelineStageTopOfPipe}
	}
	s := accessState(Graphics, Use{Access: a})
	if a == SampledRead || a == StorageReadWrite {
		s.stage |= core1_0.PipelineStageComputeShader
	}
	if writes(a) {
		s.writeStage = s.stage
	}
	return s
}

func needsBarrier(n Node, u compiledUse, before, after imageState) bool {
	if u.Access == Present && before.layout == after.layout {
		return false
	}
	if u.Access == SampledRead && before.pass && before.layout == after.layout && n.Kind == Graphics {
		// The reader's incoming dependency covers the attachment writer; the
		// producer's outgoing dependency also orders its final transition.
		// Optional joins can include earlier readers. Read-after-read needs no
		// ordering, but keep those stages in the tracked state for later writers.
		producer := before
		producer.stage = before.writeStage
		producer.access &= core1_0.AccessColorAttachmentWrite | core1_0.AccessDepthStencilAttachmentWrite
		if incomingCovers(n.Dependencies, producer, after) {
			return false
		}
	}
	const writeAccess = core1_0.AccessShaderWrite | core1_0.AccessTransferWrite |
		core1_0.AccessColorAttachmentWrite | core1_0.AccessDepthStencilAttachmentWrite
	return before.layout != after.layout || before.access != after.access || before.access&writeAccess != 0
}

func barrier(id ResourceID, before, after imageState) Barrier {
	if before.layout == core1_0.ImageLayoutUndefined {
		before.stage, before.access = core1_0.PipelineStageTopOfPipe, 0
	}
	return Barrier{Resource: id, SrcStage: before.stage, DstStage: after.stage,
		SrcAccess: before.access, DstAccess: after.access, OldLayout: before.layout, NewLayout: after.layout}
}

// Undefined barriers have no source work. Prefer the preceding group, or the
// following one for a leading run; a different destination stage is a boundary.
// Source stages of defined-layout barriers never change, nor does barrier order.
func groupUndefinedBarriers(barriers []Barrier) {
	for start := 0; start < len(barriers); {
		end := start + 1
		for end < len(barriers) && barriers[end].DstStage == barriers[start].DstStage {
			end++
		}
		stage := core1_0.PipelineStageTopOfPipe
		for i := start; i < end; i++ {
			if barriers[i].Buffer || barriers[i].OldLayout != core1_0.ImageLayoutUndefined {
				stage = barriers[i].SrcStage
				break
			}
		}
		for i := start; i < end; i++ {
			if !barriers[i].Buffer && barriers[i].OldLayout == core1_0.ImageLayoutUndefined {
				barriers[i].SrcStage = stage
			} else {
				stage = barriers[i].SrcStage
			}
		}
		start = end
	}
}

func bloomDependency() core1_0.SubpassDependency {
	return core1_0.SubpassDependency{
		SrcSubpass: core1_0.SubpassExternal, DstSubpass: 0,
		SrcStageMask:  core1_0.PipelineStageColorAttachmentOutput,
		DstStageMask:  core1_0.PipelineStageFragmentShader | core1_0.PipelineStageColorAttachmentOutput,
		SrcAccessMask: core1_0.AccessColorAttachmentWrite,
		// The initial layout transition must precede DONT_CARE/CLEAR writes,
		// not only LOAD reads. 02-cube/60 fixed frames: cloud WAW reports
		// 60 -> 0 by adding ColorAttachmentWrite.
		DstAccessMask: core1_0.AccessShaderRead | core1_0.AccessColorAttachmentRead | core1_0.AccessColorAttachmentWrite,
	}
}

// outgoingDependency makes attachment writes and final-layout transitions
// visible to their consumers. Include every declared use of each attachment:
// optional consumers can be skipped, and persistent images can be read next
// frame. Attachment reuse is included even when the next frame discards it;
// DONT_CARE is a write, not an absence of access. This also gives all levels
// of a bloom chain one compatible dependency pair for the render-pass cache.
// Measured on 13-ui -glow on, 60 fixed frames at 1280x720/4x: removing derived
// exits produces 660 RAW messages across clouds/UI/glow; intact, it reports 0.
func (g *Graph) outgoingDependency(ni int, uses [][]compiledUse) core1_0.SubpassDependency {
	d := core1_0.SubpassDependency{SrcSubpass: 0, DstSubpass: core1_0.SubpassExternal}
	for _, u := range uses[ni] {
		if !attachment(u.Access) {
			continue
		}
		producer := accessState(Graphics, u.Use)
		d.SrcStageMask |= producer.stage
		d.SrcAccessMask |= producer.access & (core1_0.AccessColorAttachmentWrite | core1_0.AccessDepthStencilAttachmentWrite)
		reuse := ColorLoadWrite
		if isDepth(u.Access) {
			reuse = DepthLoadWrite
		}
		next := accessState(Graphics, Use{Access: reuse})
		d.DstStageMask |= next.stage
		d.DstAccessMask |= next.access
		for j, consumers := range uses {
			for _, consumer := range consumers {
				if consumer.Resource == u.Resource {
					next = accessState(g.nodes[j].Kind, consumer.Use)
					d.DstStageMask |= next.stage
					d.DstAccessMask |= next.access
				}
			}
		}
	}
	// Attachment-free graphics declarations can still describe sampled inputs.
	if d.SrcStageMask == 0 {
		d.SrcStageMask, d.DstStageMask = core1_0.PipelineStageTopOfPipe, core1_0.PipelineStageBottomOfPipe
	}
	return d
}

func incomingCovers(deps []core1_0.SubpassDependency, before, after imageState) bool {
	// Read accesses in the old state need execution ordering, not availability.
	const writes = core1_0.AccessColorAttachmentWrite | core1_0.AccessDepthStencilAttachmentWrite |
		core1_0.AccessShaderWrite | core1_0.AccessTransferWrite
	for _, d := range deps {
		if d.SrcSubpass == core1_0.SubpassExternal && d.DstSubpass == 0 &&
			d.SrcStageMask&before.stage == before.stage && d.DstStageMask&after.stage == after.stage &&
			d.SrcAccessMask&(before.access&writes) == before.access&writes &&
			d.DstAccessMask&after.access == after.access {
			return true
		}
	}
	return false
}
