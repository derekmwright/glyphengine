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

func needsBarrier(u compiledUse, before, after imageState) bool {
	if u.Access == Present && before.layout == after.layout {
		return false
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
