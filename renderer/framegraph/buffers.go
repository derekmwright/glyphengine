package framegraph

import "github.com/vkngwrapper/core/v3/core1_0"

func bufferUsage(a Access) core1_0.BufferUsageFlags {
	switch a {
	case StorageRead, StorageWrite, StorageReadWrite:
		return core1_0.BufferUsageStorageBuffer
	case VertexRead:
		return core1_0.BufferUsageVertexBuffer
	case IndirectRead:
		return core1_0.BufferUsageIndirectBuffer
	case TransferSrc:
		return core1_0.BufferUsageTransferSrc
	case TransferDst:
		return core1_0.BufferUsageTransferDst
	}
	return 0
}

// A write remains a producer until every consumer scope has seen it. In
// particular vertex input and indirect fetch are independent readers, even
// when both are declared by the same graphics node.
func bufferUse(step *Step, u compiledUse, before, after imageState, size int, emit bool) imageState {
	after.layout = core1_0.ImageLayoutUndefined
	need := before.stage != 0 && writes(u.Access)
	if !writes(u.Access) {
		need = before.writeStage != 0 && (before.visibleStage&after.stage != after.stage || before.visibleAccess != after.access)
	}
	if emit && need {
		step.Barriers = append(step.Barriers, Barrier{Resource: u.Resource, Buffer: true, Size: size,
			SrcStage: before.stage, DstStage: after.stage, SrcAccess: before.access, DstAccess: after.access})
	}
	if writes(u.Access) {
		return after
	}
	after.visibleStage = after.stage
	if before.visibleAccess == after.access {
		after.visibleStage |= before.visibleStage
	}
	after.visibleAccess = after.access
	after.stage |= before.stage
	after.access |= before.access
	after.writeStage = before.writeStage
	return after
}

func optionalBufferState(before, after imageState) imageState {
	after = optionalState(before, after)
	// A skipped barrier cannot establish availability on the skipped path.
	after.visibleStage &= before.visibleStage
	if after.visibleAccess != before.visibleAccess {
		after.visibleAccess, after.visibleStage = 0, 0
	}
	return after
}
