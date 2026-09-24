package framegraph

import (
	"testing"

	"github.com/vkngwrapper/core/v3/core1_0"
)

// Verified to fail on 2026-09-24: removing outgoingDependency leaves one
// dependency, missing exit stages 1024 -> 1152 and accesses 256 -> 416.
func TestUILayerDependencyPair(t *testing.T) {
	g := New()
	d := colorImage("UI layer")
	d.Persistent = true
	id := g.AddImage(d)
	g.AddNode(Node{Name: "UI layer", Kind: Graphics, Optional: true, Uses: []Use{{Resource: id, Access: ColorWrite, Clear: &Clear{}, Discard: true}}})
	g.AddNode(Node{Name: "composite", Kind: Legacy, Uses: []Use{{Resource: id, Access: SampledRead}}})
	p := mustBuild(t, g)
	equal(t, "UI entry and exit", p.Steps[0].RenderPass.Dependencies, []core1_0.SubpassDependency{{
		SrcSubpass: core1_0.SubpassExternal, DstSubpass: 0,
		SrcStageMask:  core1_0.PipelineStageFragmentShader | core1_0.PipelineStageColorAttachmentOutput,
		DstStageMask:  core1_0.PipelineStageFragmentShader | core1_0.PipelineStageColorAttachmentOutput,
		SrcAccessMask: core1_0.AccessShaderRead | core1_0.AccessColorAttachmentWrite,
		DstAccessMask: core1_0.AccessShaderRead | core1_0.AccessColorAttachmentRead | core1_0.AccessColorAttachmentWrite,
	}, {
		SrcSubpass: 0, DstSubpass: core1_0.SubpassExternal,
		SrcStageMask:  core1_0.PipelineStageColorAttachmentOutput,
		DstStageMask:  core1_0.PipelineStageFragmentShader | core1_0.PipelineStageColorAttachmentOutput,
		SrcAccessMask: core1_0.AccessColorAttachmentWrite,
		DstAccessMask: core1_0.AccessShaderRead | core1_0.AccessColorAttachmentRead | core1_0.AccessColorAttachmentWrite,
	}})
}

// The same removal fails all three consumer cases with a missing pair.
func TestAttachmentExitCoversConsumerStages(t *testing.T) {
	for _, tc := range []struct {
		name   string
		kind   NodeKind
		access Access
		stage  core1_0.PipelineStageFlags
		mask   core1_0.AccessFlags
	}{
		{"copy", Transfer, TransferSrc, core1_0.PipelineStageTransfer, core1_0.AccessTransferRead},
		{"compute", Compute, SampledRead, core1_0.PipelineStageComputeShader, core1_0.AccessShaderRead},
		{"vertex", Graphics, SampledRead, core1_0.PipelineStageVertexShader, core1_0.AccessShaderRead},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := New()
			g.AddImage(colorImage("output"))
			g.AddNode(Node{Name: "producer", Kind: Graphics, Uses: []Use{{Access: ColorWrite}}})
			g.AddNode(Node{Name: "consumer", Kind: tc.kind, Uses: []Use{{Access: tc.access, Stages: tc.stage}}})
			p := mustBuild(t, g)
			deps := p.Steps[0].RenderPass.Dependencies
			if len(deps) != 2 {
				t.Fatalf("missing dependency pair: %v", deps)
			}
			equal(t, "consumer stage", deps[1].DstStageMask&tc.stage, tc.stage)
			equal(t, "consumer access", deps[1].DstAccessMask&tc.mask, tc.mask)
		})
	}
}
