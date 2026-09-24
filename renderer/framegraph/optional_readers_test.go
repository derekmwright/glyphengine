package framegraph

import (
	"testing"

	"github.com/vkngwrapper/core/v3/core1_0"
)

// A skipped bloom stage leaves primed sampled reads, not another writer to
// synchronize. The next sampler's incoming colour dependency covers every
// possible writer without waiting for the read-only skipped path.
func TestOptionalBloomRetainsAttachmentVisibility(t *testing.T) {
	g := bloomGraph(5)
	for i := 1; i < len(g.nodes)-1; i++ {
		g.nodes[i].Optional = true
	}
	p := mustBuild(t, g)
	for _, s := range p.Steps[1 : len(p.Steps)-1] {
		equal(t, "restores optional attachment", len(s.AfterBarriers), 1)
	}
	equal(t, "final barriers", len(p.FinalBarriers), 0)
}

func TestOptionalAttachmentDoesNotHideSkippedStorageWriter(t *testing.T) {
	d := colorImage("mixed writers")
	d.Imported, d.InitialLayout = true, core1_0.ImageLayoutShaderReadOnlyOptimal
	// A legacy block may leave storage output in the resource's resting layout.
	// Test the join directly because ordinary storage access rests in General,
	// which is deliberately not neutral with an attachment in this graph.
	before := accessState(Compute, Use{Access: StorageWrite})
	after := accessState(Graphics, Use{Access: ColorWrite})
	before.layout, after.layout = d.InitialLayout, d.InitialLayout
	merged := optionalState(before, after)
	read := accessState(Graphics, Use{Access: SampledRead})
	if !needsBarrier(compiledUse{Use: Use{Access: SampledRead}}, merged, read) {
		t.Fatal("colour dependency hid the skipped compute writer")
	}
	equal(t, "all writer stages", merged.writeStage, before.stage|after.stage)
}
