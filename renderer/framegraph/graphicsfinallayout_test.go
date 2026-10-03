package framegraph

import (
	"fmt"
	"testing"

	"github.com/vkngwrapper/core/v3/core1_0"
)

// prepassGraph is the renderer's depth-prepass shape, reduced to the part the
// compiler decides: a depth-only graphics node writing a transient depth image,
// then a hand-recorded scene that writes it again, then something that samples
// it. The sampler is what makes depth rest in the sampled layout, and that is the
// only case in which the exit transition this test is about exists at all.
func prepassGraph(explicit bool) (*Graph, ResourceID) {
	g := New()
	depth := g.AddImage(depthImage("depth"))
	u := Use{Resource: depth, Access: DepthWrite, Clear: &Clear{}, Discard: true}
	if explicit {
		u.FinalLayout = core1_0.ImageLayoutDepthStencilAttachmentOptimal
	}
	g.AddNode(Node{Name: "depth prepass", Kind: Graphics, Uses: []Use{u}})
	g.AddNode(Node{Name: "legacy scene", Kind: Legacy, Uses: []Use{{Resource: depth, Access: DepthLoadWrite,
		FinalLayout: core1_0.ImageLayoutDepthStencilAttachmentOptimal}}})
	g.AddNode(Node{Name: "depth resolve", Kind: Graphics, Uses: []Use{{Resource: depth, Access: SampledRead}}})
	return g, depth
}

// A graphics attachment goes back to its resting layout on the way out unless the
// declaration names the layout it leaves instead.
//
// The case that matters is the one where those two differ. Here a later node
// samples depth, so depth rests in ShaderReadOnlyOptimal, and without the
// override the prepass transitions out to it and the hand-recorded scene has to
// transition straight back — two barriers that move no data, the second of them a
// write-after-write against the prepass's own depth writes with nothing in its
// source scope that covers them.
//
// Verified to fail: with the `leave` override removed from Build, explicit=true
// reports `exit barriers: got 1; want 0`, and the barrier it reports is exactly
// that round trip.
func TestGraphicsFinalLayoutReplacesTheExitTransition(t *testing.T) {
	for _, explicit := range []bool{false, true} {
		t.Run(fmt.Sprintf("explicit=%v", explicit), func(t *testing.T) {
			g, depth := prepassGraph(explicit)
			p := mustBuild(t, g)
			equal(t, "resting layout", p.Resources[depth].Resting, core1_0.ImageLayoutShaderReadOnlyOptimal)
			if !explicit {
				equal(t, "exit barriers", len(p.Steps[0].AfterBarriers), 1)
				b := p.Steps[0].AfterBarriers[0]
				equal(t, "exit barrier new layout", b.NewLayout, core1_0.ImageLayoutShaderReadOnlyOptimal)
				t.Logf("without the override: %v -> %v", b.OldLayout, b.NewLayout)
				return
			}
			equal(t, "exit barriers", len(p.Steps[0].AfterBarriers), 0)
			// And nothing is left over at the end of the frame: the sampling node
			// is what brings depth back to its resting layout.
			equal(t, "nothing to restore", len(p.FinalBarriers), 0)
			t.Log("with the override: depth stays an attachment for the scene pass")
		})
	}
}

// FinalLayout on a graphics node describes the exit transition the compiler
// derives for an attachment. There is no such transition for a sampled read or a
// storage write, so naming one there is a promise about something that does not
// exist, and Build says so rather than ignoring it.
//
// Legacy keeps the older, broader meaning — the renderer's application
// previous-frame nodes declare storage states with an explicit final layout — and
// the second case below is what holds that distinction rather than letting a
// tightening here break them silently.
func TestGraphicsFinalLayoutRequiresAnAttachment(t *testing.T) {
	build := func(kind NodeKind) *Graph {
		g := New()
		id := g.AddImage(colorImage("target"))
		g.AddImage(colorImage("unused")) // keeps the resource table from being a single id
		g.AddNode(Node{Name: "writer", Kind: Graphics, Uses: []Use{{Resource: id, Access: ColorWrite, Discard: true}}})
		g.AddNode(Node{Name: "reader", Kind: kind, Uses: []Use{{Resource: id, Access: SampledRead,
			FinalLayout: core1_0.ImageLayoutDepthStencilAttachmentOptimal}}})
		return g
	}
	errorContains(t, build(Graphics), "reader", "target", "FinalLayout on a graphics node requires an attachment access")
	if _, err := build(Legacy).Build(); err != nil {
		t.Fatalf("a Legacy node's FinalLayout on a sampled read is how the application previous-frame nodes are declared: %v", err)
	}
}
