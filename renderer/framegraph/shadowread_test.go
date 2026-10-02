package framegraph

import (
	"testing"

	"github.com/vkngwrapper/core/v3/core1_0"
)

// cascadeGraph is the renderer's shadow shape: an imported per-frame depth
// array an opaque recorder fills, declared by a Legacy node, then sampled by a
// compute dispatch and by the hand-recorded scene. readerFirst puts the
// dispatch ahead of the declaration, which is the ordering mistake the Rewrites
// flag exists to refuse.
func cascadeGraph(readerFirst bool) (*Graph, ResourceID) {
	g := New()
	d := depthImage("sun shadow maps")
	d.Imported, d.InitialLayout = true, core1_0.ImageLayoutDepthStencilReadOnlyOptimal
	d.Layers = 2
	id := g.AddImage(d)
	cascades := Node{Name: "sun shadow cascades", Kind: Legacy,
		Uses: []Use{{Resource: id, Access: DepthWrite, Rewrites: true}}}
	dispatch := Node{Name: "air scatter", Kind: Compute,
		Uses: []Use{{Resource: id, Access: DepthSampledRead}}}
	if readerFirst {
		g.AddNode(dispatch)
		g.AddNode(cascades)
	} else {
		g.AddNode(cascades)
		g.AddNode(dispatch)
	}
	g.AddNode(Node{Name: "legacy scene", Kind: Legacy, Uses: []Use{{Resource: id, Access: DepthSampledRead}}})
	return g, id
}

// Verified breaks (2026-10-02):
//   - dropping "ni < at" from validateRewrites: the reader-first graph compiles,
//     this reports `expected read before "sun shadow cascades" rewrites error;
//     got plan ...`, and that plan has no barriers at all -- the dispatch would
//     have sampled whatever the previous frame left.
//   - declaring the dispatch's read as SampledRead instead: "entry barrier new
//     layout: got 5; want 4" -- ShaderReadOnlyOptimal where the image is in
//     DepthStencilReadOnlyOptimal, so the compiler would have had the driver
//     transition from a layout the image was never in.
func TestCascadeRewriteOrdersComputeReads(t *testing.T) {
	g, id := cascadeGraph(false)
	p := mustBuild(t, g)
	equal(t, "resting layout", p.Resources[id].Resting, core1_0.ImageLayoutDepthStencilReadOnlyOptimal)
	equal(t, "declaration derives no barrier", len(p.Steps[0].Barriers)+len(p.Steps[0].AfterBarriers), 0)
	equal(t, "entry barriers", len(p.Steps[1].Barriers), 1)
	b := p.Steps[1].Barriers[0]
	equal(t, "entry barrier old layout", b.OldLayout, core1_0.ImageLayoutDepthStencilReadOnlyOptimal)
	equal(t, "entry barrier new layout", b.NewLayout, core1_0.ImageLayoutDepthStencilReadOnlyOptimal)
	equal(t, "cascade depth writes", b.SrcAccess, core1_0.AccessDepthStencilAttachmentWrite)
	equal(t, "depth attachment stages", b.SrcStage,
		core1_0.PipelineStageEarlyFragmentTests|core1_0.PipelineStageLateFragmentTests)
	equal(t, "compute consumer", b.DstStage, core1_0.PipelineStageComputeShader)
	equal(t, "compute reads", b.DstAccess, core1_0.AccessShaderRead)
	equal(t, "no return edge", len(p.Steps[1].AfterBarriers), 0)
	equal(t, "nothing to restore", len(p.FinalBarriers), 0)
	t.Logf("cascade write -> compute read: %v -> %v, stages %v -> %v", b.OldLayout, b.NewLayout, b.SrcStage, b.DstStage)

	g, _ = cascadeGraph(true)
	errorContains(t, g, "air scatter", "sun shadow maps", `read before "sun shadow cascades" rewrites`)
}

// A graph that declares no Rewrites use is unconstrained: the renderer compiles
// one in Renderer.New, before any application node exists, and its legacy node
// samples the same imported maps with nothing having declared the write.
func TestImportedDepthReadNeedsNoRewriteDeclaration(t *testing.T) {
	g := New()
	d := depthImage("sun shadow maps")
	d.Imported, d.InitialLayout = true, core1_0.ImageLayoutDepthStencilReadOnlyOptimal
	id := g.AddImage(d)
	g.AddNode(Node{Name: "legacy scene", Kind: Legacy, Uses: []Use{{Resource: id, Access: DepthSampledRead}}})
	p := mustBuild(t, g)
	equal(t, "barriers", len(p.Steps[0].Barriers), 0)
	equal(t, "final barriers", len(p.FinalBarriers), 0)
}

func TestDepthSampledReadDeclarationErrors(t *testing.T) {
	g := New()
	colour := g.AddImage(colorImage("colour"))
	g.AddNode(Node{Name: "read colour", Kind: Compute, Uses: []Use{{Resource: colour, Access: DepthSampledRead}}})
	errorContains(t, g, "read colour", "colour", "depth sampling requires a depth aspect")

	g = New()
	depth := g.AddImage(depthImage("depth"))
	g.AddNode(Node{Name: "read depth", Kind: Compute, Uses: []Use{{Resource: depth, Access: DepthSampledRead, Rewrites: true}}})
	errorContains(t, g, "read depth", "depth", "Rewrites requires a write access")

	g = New()
	buffer := g.AddBuffer(BufferDesc{Name: "data", Size: 16})
	g.AddNode(Node{Name: "read buffer", Kind: Compute, Uses: []Use{{Resource: buffer, Access: DepthSampledRead}}})
	errorContains(t, g, "read buffer", "data", "access incompatible with resource kind")
}
