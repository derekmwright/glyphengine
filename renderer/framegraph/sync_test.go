package framegraph

import (
	"testing"

	"github.com/vkngwrapper/core/v3/core1_0"
)

// Verified to fail: removing the equal-access write hazard produces zero
// barriers instead of one before the second StorageWrite.
func TestStorageAndTransferHazards(t *testing.T) {
	g := New()
	g.AddImage(colorImage("storage"))
	for _, n := range []Node{
		{Name: "write", Kind: Compute, Uses: []Use{{Access: StorageWrite}}},
		{Name: "write again", Kind: Compute, Uses: []Use{{Access: StorageWrite}}},
		{Name: "read", Kind: Compute, Uses: []Use{{Access: StorageRead}}},
		{Name: "read again", Kind: Compute, Uses: []Use{{Access: StorageRead}}},
		{Name: "sample", Kind: Graphics, Uses: []Use{{Access: SampledRead, Stages: core1_0.PipelineStageVertexShader}}},
		{Name: "copy", Kind: Transfer, Uses: []Use{{Access: TransferSrc}}},
	} {
		g.AddNode(n)
	}
	p := mustBuild(t, g)
	for i, count := range []int{1, 1, 1, 0, 1, 1} {
		equal(t, "barrier count", len(p.Steps[i].Barriers), count)
	}
	equal(t, "initial storage", p.Steps[0].Barriers[0], Barrier{Resource: 0,
		SrcStage: core1_0.PipelineStageTopOfPipe, DstStage: core1_0.PipelineStageComputeShader,
		DstAccess: core1_0.AccessShaderWrite, OldLayout: core1_0.ImageLayoutUndefined, NewLayout: core1_0.ImageLayoutGeneral})
	equal(t, "WAW", p.Steps[1].Barriers[0], Barrier{Resource: 0,
		SrcStage: core1_0.PipelineStageComputeShader, DstStage: core1_0.PipelineStageComputeShader,
		SrcAccess: core1_0.AccessShaderWrite, DstAccess: core1_0.AccessShaderWrite,
		OldLayout: core1_0.ImageLayoutGeneral, NewLayout: core1_0.ImageLayoutGeneral})
	equal(t, "stage override", p.Steps[4].Barriers[0].DstStage, core1_0.PipelineStageVertexShader)
	equal(t, "restore copy layout", p.FinalBarriers, []Barrier{{Resource: 0,
		SrcStage: core1_0.PipelineStageTransfer, DstStage: core1_0.PipelineStageFragmentShader | core1_0.PipelineStageComputeShader,
		SrcAccess: core1_0.AccessTransferRead, DstAccess: core1_0.AccessShaderRead,
		OldLayout: core1_0.ImageLayoutTransferSrcOptimal, NewLayout: core1_0.ImageLayoutShaderReadOnlyOptimal}})
	// A render pass's fragment dependency cannot synchronize a compute sampler.
	g = New()
	g.AddImage(colorImage("output"))
	g.AddNode(Node{Name: "draw", Kind: Graphics, Uses: []Use{{Access: ColorWrite}}})
	g.AddNode(Node{Name: "compute sample", Kind: Compute, Uses: []Use{{Access: SampledRead}}})
	p = mustBuild(t, g)
	equal(t, "compute visibility", p.Steps[1].Barriers[0].DstStage, core1_0.PipelineStageComputeShader)
}

// Verified to fail: assigning derived Usage instead of ORing it drops the
// caller's TransferDst bit (sampled usage 4 instead of 6, among other cases).
func TestUsageAndRestingLayouts(t *testing.T) {
	for _, tc := range []struct {
		name   string
		access Access
		usage  core1_0.ImageUsageFlags
		layout core1_0.ImageLayout
	}{
		{"sampled", SampledRead, core1_0.ImageUsageSampled, core1_0.ImageLayoutShaderReadOnlyOptimal},
		{"storage read", StorageRead, core1_0.ImageUsageStorage, core1_0.ImageLayoutGeneral},
		{"storage write", StorageWrite, core1_0.ImageUsageStorage, core1_0.ImageLayoutGeneral},
		{"storage read write", StorageReadWrite, core1_0.ImageUsageStorage, core1_0.ImageLayoutGeneral},
		{"color", ColorWrite, core1_0.ImageUsageColorAttachment, core1_0.ImageLayoutColorAttachmentOptimal},
		{"color load", ColorLoadWrite, core1_0.ImageUsageColorAttachment, core1_0.ImageLayoutColorAttachmentOptimal},
		{"depth", DepthWrite, core1_0.ImageUsageDepthStencilAttachment, core1_0.ImageLayoutDepthStencilAttachmentOptimal},
		{"depth load", DepthLoadWrite, core1_0.ImageUsageDepthStencilAttachment, core1_0.ImageLayoutDepthStencilAttachmentOptimal},
		{"transfer src", TransferSrc, core1_0.ImageUsageTransferSrc, core1_0.ImageLayoutTransferSrcOptimal},
		{"transfer dst", TransferDst, core1_0.ImageUsageTransferDst, core1_0.ImageLayoutTransferDstOptimal},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := New()
			d := colorImage(tc.name)
			d.Persistent = true
			d.Usage = core1_0.ImageUsageTransferDst
			if isDepth(tc.access) {
				d.Aspect = core1_0.ImageAspectDepth
			}
			g.AddImage(d)
			g.AddNode(Node{Name: tc.name, Kind: Legacy, Uses: []Use{{Access: tc.access}}})
			p := mustBuild(t, g)
			equal(t, "usage", p.Resources[0].Desc.Usage, tc.usage|core1_0.ImageUsageTransferDst)
			equal(t, "resting", p.Resources[0].Resting, tc.layout)
			equal(t, "instances", p.Resources[0].Desc.Instances, 1)
			equal(t, "layers", p.Resources[0].Desc.Layers, uint32(1))
			equal(t, "legacy barriers", len(p.Steps[0].Barriers), 0)
			if p.Steps[0].RenderPass != nil {
				t.Fatal("Legacy must not derive a render pass")
			}
		})
	}
	for _, read := range []bool{false, true} {
		g := New()
		d := colorImage("MSAA")
		d.Samples = core1_0.Samples4
		g.AddImage(d)
		g.AddImage(colorImage("resolve"))
		g.AddNode(Node{Name: "resolve", Kind: Graphics, Uses: []Use{{Access: ColorWrite, HasResolve: true, ResolveTo: 1}}})
		if read {
			g.AddNode(Node{Name: "sample MSAA", Kind: Graphics, Uses: []Use{{Access: SampledRead}}})
		}
		p := mustBuild(t, g)
		equal(t, "transient MSAA usage", p.Resources[0].Desc.Usage&core1_0.ImageUsageTransientAttachment != 0, !read)
		equal(t, "resolve usage", p.Resources[1].Desc.Usage, core1_0.ImageUsageColorAttachment)
	}
	// Imported arrays and instance counts survive compilation as logical images.
	g := New()
	d := depthImage("cascades")
	d.Imported = true
	d.Layers = 4
	d.Instances = 3
	d.InitialLayout = core1_0.ImageLayoutDepthStencilAttachmentOptimal
	g.AddImage(d)
	g.AddNode(Node{Name: "sample shadows", Kind: Graphics, Uses: []Use{{Access: SampledRead}}})
	p := mustBuild(t, g)
	equal(t, "array", p.Resources[0].Desc.Layers, uint32(4))
	equal(t, "copies", p.Resources[0].Desc.Instances, 3)
	equal(t, "import initial", p.Steps[0].Barriers[0].OldLayout, core1_0.ImageLayoutDepthStencilAttachmentOptimal)
}

// Verified to fail: dropping the accumulated reader stages waits for compute
// alone (2048) instead of vertex and compute (2056).
func TestAllReadersPrecedeTheNextWriter(t *testing.T) {
	g := New()
	d := colorImage("readers")
	d.Imported, d.InitialLayout = true, core1_0.ImageLayoutGeneral
	g.AddImage(d)
	g.AddNode(Node{Name: "vertex read", Kind: Graphics, Uses: []Use{{Access: StorageRead, Stages: core1_0.PipelineStageVertexShader}}})
	g.AddNode(Node{Name: "compute read", Kind: Compute, Uses: []Use{{Access: StorageRead}}})
	g.AddNode(Node{Name: "write", Kind: Compute, Uses: []Use{{Access: StorageWrite}}})
	p := mustBuild(t, g)
	equal(t, "readers need no intermediate barrier", len(p.Steps[1].Barriers), 0)
	equal(t, "wait for both readers", p.Steps[2].Barriers[0].SrcStage,
		core1_0.PipelineStageVertexShader|core1_0.PipelineStageComputeShader)
}

// Verified to fail: removing the producer dependency extension reports source
// stage 0 instead of ComputeShader (2048) on the first case.
// Ignoring the derived dependencies during visibility checks separately emits
// one redundant barrier instead of zero for the sampled depth case.
func TestAttachmentEntryWaitsForStorageAndTransfer(t *testing.T) {
	for _, tc := range []struct {
		kind   NodeKind
		access Access
		stage  core1_0.PipelineStageFlags
		mask   core1_0.AccessFlags
	}{
		{Compute, StorageWrite, core1_0.PipelineStageComputeShader, core1_0.AccessShaderWrite},
		{Transfer, TransferDst, core1_0.PipelineStageTransfer, core1_0.AccessTransferWrite},
	} {
		g := New()
		g.AddImage(colorImage("target"))
		g.AddNode(Node{Name: "produce", Kind: tc.kind, Uses: []Use{{Access: tc.access}}})
		g.AddNode(Node{Name: "load", Kind: Graphics, Uses: []Use{{Access: ColorLoadWrite}}})
		p := mustBuild(t, g)
		equal(t, "no attachment barrier", len(p.Steps[1].Barriers), 0)
		dep := p.Steps[1].RenderPass.Dependencies[0]
		equal(t, "source stage", dep.SrcStageMask&tc.stage, tc.stage)
		equal(t, "source write visibility", dep.SrcAccessMask&tc.mask, tc.mask)
		equal(t, "load/write destination", dep.DstAccessMask&(core1_0.AccessColorAttachmentRead|core1_0.AccessColorAttachmentWrite), core1_0.AccessColorAttachmentRead|core1_0.AccessColorAttachmentWrite)
	}
	g := New()
	g.AddImage(depthImage("sampled depth"))
	g.AddImage(depthImage("other depth"))
	g.AddNode(Node{Name: "write depth", Kind: Graphics, Uses: []Use{{Access: DepthWrite}}})
	g.AddNode(Node{Name: "read depth", Kind: Graphics, Uses: []Use{{Access: SampledRead}, {Resource: 1, Access: DepthWrite}}})
	p := mustBuild(t, g)
	equal(t, "derived depth dependency already covers the read", len(p.Steps[1].Barriers), 0)
}

// Verified to fail: using ColorAttachmentOutput as the fallback source stage
// reports 1024 instead of TopOfPipe (1) on the standalone copy barrier.
func TestUndefinedTransferBarrierAlone(t *testing.T) {
	g := New()
	g.AddImage(colorImage("copy destination"))
	g.AddNode(Node{Name: "copy", Kind: Transfer, Uses: []Use{{Access: TransferDst}}})
	p := mustBuild(t, g)
	equal(t, "standalone discard has no source work", p.Steps[0].Barriers, []Barrier{{Resource: 0,
		SrcStage: core1_0.PipelineStageTopOfPipe, DstStage: core1_0.PipelineStageTransfer,
		SrcAccess: 0, DstAccess: core1_0.AccessTransferWrite,
		OldLayout: core1_0.ImageLayoutUndefined, NewLayout: core1_0.ImageLayoutTransferDstOptimal}})
}

// Verified to fail: keeping TopOfPipe when joining a group reports 1 instead of
// 2048. Ignoring destination boundaries reports 2048 instead of 1. Retaining
// stale source access independently reports 64 instead of 0.
func TestUndefinedBarrierGrouping(t *testing.T) {
	const top = core1_0.PipelineStageTopOfPipe
	const compute = core1_0.PipelineStageComputeShader
	const fragment = core1_0.PipelineStageFragmentShader
	const transfer = core1_0.PipelineStageTransfer
	for _, tc := range []struct {
		name           string
		src, dst, want []core1_0.PipelineStageFlags
	}{
		{"leading", []core1_0.PipelineStageFlags{0, 0, compute}, []core1_0.PipelineStageFlags{transfer, transfer, transfer}, []core1_0.PipelineStageFlags{compute, compute, compute}},
		{"trailing", []core1_0.PipelineStageFlags{compute, 0, 0}, []core1_0.PipelineStageFlags{transfer, transfer, transfer}, []core1_0.PipelineStageFlags{compute, compute, compute}},
		{"different sources", []core1_0.PipelineStageFlags{compute, 0, fragment, 0}, []core1_0.PipelineStageFlags{transfer, transfer, transfer, transfer}, []core1_0.PipelineStageFlags{compute, compute, fragment, fragment}},
		{"destination boundary", []core1_0.PipelineStageFlags{compute, 0}, []core1_0.PipelineStageFlags{transfer, fragment}, []core1_0.PipelineStageFlags{compute, top}},
		{"all undefined", []core1_0.PipelineStageFlags{0, 0}, []core1_0.PipelineStageFlags{compute, compute}, []core1_0.PipelineStageFlags{top, top}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			barriers := make([]Barrier, len(tc.src))
			for i, src := range tc.src {
				before := imageState{layout: core1_0.ImageLayoutGeneral, stage: src, access: core1_0.AccessShaderWrite}
				if src == 0 {
					// Undefined must remove stale masks, not just happen to inherit
					// zero values from today's initial-state constructor.
					before.layout, before.stage = core1_0.ImageLayoutUndefined, fragment
				}
				barriers[i] = barrier(ResourceID(i), before, imageState{layout: core1_0.ImageLayoutTransferDstOptimal, stage: tc.dst[i], access: core1_0.AccessTransferWrite})
			}
			groupUndefinedBarriers(barriers)
			for i, b := range barriers {
				equal(t, "resource order", b.Resource, ResourceID(i))
				equal(t, "group stage", b.SrcStage, tc.want[i])
				access := core1_0.AccessShaderWrite
				if tc.src[i] == 0 {
					access = 0
				}
				equal(t, "source access", b.SrcAccess, access)
				equal(t, "destination stage", b.DstStage, tc.dst[i])
			}
		})
	}
}
