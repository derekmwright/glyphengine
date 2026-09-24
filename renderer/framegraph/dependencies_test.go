package framegraph

import (
	"github.com/vkngwrapper/core/v3/core1_0"
	"testing"
)

// Negative controls: omitting entry or exit emission reports 0 barriers, want 1
// in this UI clear/composite graph (2026-09-24). Both directions are required.
func TestUILayerAttachmentBarriers(t *testing.T) {
	g := New()
	d := colorImage("UI")
	d.Persistent = true
	id := g.AddImage(d)
	g.AddNode(Node{Name: "UI", Kind: Graphics, Optional: true, Uses: []Use{{Resource: id, Access: ColorWrite, Discard: true, Clear: &Clear{}}}})
	g.AddNode(Node{Name: "composite", Kind: Legacy, Uses: []Use{{Resource: id, Access: SampledRead}}})
	p := mustBuild(t, g)
	entry, exit := p.Steps[0].Barriers, p.Steps[0].AfterBarriers
	equal(t, "entry count", len(entry), 1)
	equal(t, "exit count", len(exit), 1)
	equal(t, "wait for previous readers", entry[0].SrcAccess, core1_0.AccessShaderRead)
	equal(t, "attachment entry", entry[0].NewLayout, core1_0.ImageLayoutColorAttachmentOptimal)
	equal(t, "write visibility", exit[0].SrcAccess, core1_0.AccessColorAttachmentWrite)
	equal(t, "restores sampled layout", exit[0].NewLayout, core1_0.ImageLayoutShaderReadOnlyOptimal)
}

func TestAttachmentExitCoversConsumerStages(t *testing.T) {
	for _, stage := range []core1_0.PipelineStageFlags{core1_0.PipelineStageVertexShader, core1_0.PipelineStageFragmentShader, core1_0.PipelineStageComputeShader} {
		g := New()
		g.AddImage(colorImage("output"))
		g.AddNode(Node{Name: "produce", Kind: Graphics, Uses: []Use{{Access: ColorWrite}}})
		g.AddNode(Node{Name: "read", Kind: Graphics, Uses: []Use{{Access: SampledRead, Stages: stage}}})
		p := mustBuild(t, g)
		b := p.Steps[0].AfterBarriers[0]
		equal(t, "consumer stage", b.DstStage&stage, stage)
		equal(t, "shader visibility", b.DstAccess, core1_0.AccessShaderRead)
	}
}
