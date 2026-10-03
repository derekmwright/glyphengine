package renderer

import (
	"github.com/vkngwrapper/core/v3/core1_0"
	"github.com/vkngwrapper/core/v3/loader"
	"reflect"
	"testing"
)

func (fx *frame) initGraph() {
	var err error
	fx.graph, err = newFrameGraph(core1_0.Samples4, core1_0.FormatD32SignedFloat, core1_0.FormatB8G8R8A8SRGB, 1, false)
	if err != nil {
		panic(err)
	}
	fx.graph.sizeScratch(&fx.scratch)
	fx.initGraphBindings()
}
func (fx *frame) initGraphBindings() {
	f := fx.graph
	for i := range f.images {
		if len(f.images[i].images) == 0 {
			f.images[i].images = []core1_0.Image{core1_0.InternalImage(0, loader.VkImage(20000+i), 0)}
		}
		if len(f.images[i].views) == 0 {
			f.images[i].views = []core1_0.ImageView{core1_0.InternalImageView(0, loader.VkImageView(21000+i), 0)}
		}
	}
	for i, step := range f.plan.Steps {
		if step.RenderPass == nil {
			continue
		}
		n := &f.nodes[i]
		// A depth-only node (the prepass) has no colour attachment, so the
		// colour test alone rebuilt its target on every record and showed up as
		// three constant allocations per frame in the recorder's own alloc test.
		if len(n.targets) > 0 && (len(n.targets[0].info.ColorAttachments) > 0 || n.targets[0].info.DepthAttachment != nil) {
			continue
		}
		n.extent = fx.extent
		n.targets = []*renderingTarget{f.bindRenderingTarget(i, 0)}
	}
	if fx.target == nil {
		r := &Renderer{sc: &swapchainDetails{extent: fx.extent}, hdr: &hdrTarget{images: []core1_0.Image{fx.sceneImage}, views: f.images[f.hdr].views}, depth: &depthResources{images: f.images[f.depth].images, views: f.images[f.depth].views}, msaa: &msaaResources{images: f.images[f.color].images, views: f.images[f.color].views}}
		// Keyed off the graph rather than set by the caller: a prepass node in
		// the plan and a scene target that still CLEARS depth is the exact
		// combination that throws the prepass away, so the fixture must not be
		// able to pin a stream the real renderer cannot produce.
		if f.prepass >= 0 {
			r.depthPrepassMode = DepthPrepassOn
		}
		r.bindSceneTargets()
		fx.target = r.sceneTargets[0]
		fx.target.info.ColorAttachments[0].ClearValue = &fx.scratch.colorClear
	}
}
func (fx *frame) bindGraph() {
	fx.initGraphBindings()
	f := fx.graph
	f.images[f.hdr].images[0] = fx.sceneImage
	if fx.sceneColor != nil {
		f.images[f.copy].images[0] = fx.sceneColor.image
	}
}

// Water and the scene have to agree on their attachment formats, because they
// share pipelines.
//
// Run with the depth prepass off and on, and the second half is not decoration.
// The graph* constants are DECLARATION indices while the plan's steps are indexed
// by node, and the prepass is the first thing the engine inserts ahead of its own
// tail in the graph Renderer.New compiles -- so a lookup by the raw constant
// stops naming the node it means. Verified: with engineFormats reverted to
// pipelineFormats(graphWater), prepass=true panics here on a nil RenderPass,
// which is what it did in New on real hardware.
func TestFrameGraphWaterFormatsMatchScene(t *testing.T) {
	for _, prepass := range []bool{false, true} {
		for _, samples := range []core1_0.SampleCountFlags{core1_0.Samples1, core1_0.Samples4} {
			f, err := newFrameGraph(samples, core1_0.FormatD32SignedFloat, core1_0.FormatB8G8R8A8SRGB, 3, prepass)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(f.engineFormats(graphWater), colorDepthFormats(hdrFormat, core1_0.FormatD32SignedFloat)) {
				t.Fatalf("prepass=%v: water and scene attachment formats differ", prepass)
			}
		}
	}
}

// Every engine tail node New builds a pipeline against, by the declaration index
// it is named by, with the prepass inserted ahead of them.
//
// The panic this catches is not subtle once it happens -- New dereferences a nil
// RenderPass -- but nothing in the package reached it before, because the only
// graph whose declaration and node indices coincided was the one New compiles,
// and the prepass is what stopped them coinciding.
func TestEngineFormatsSurviveTheDepthPrepass(t *testing.T) {
	f, err := newFrameGraph(core1_0.Samples4, core1_0.FormatD32SignedFloat, core1_0.FormatB8G8R8A8SRGB, 3, true)
	if err != nil {
		t.Fatal(err)
	}
	if f.prepass < 0 {
		t.Fatal("no prepass node, so this test says nothing")
	}
	for _, c := range []struct {
		name string
		decl int
	}{
		{"water", graphWater},
		{"bloom down", graphBloom},
		{"bloom up", graphBloom + bloomLevels},
		{"UI layer", graphUILayer},
		{"tonemap", graphTonemap},
	} {
		node := f.engine[c.decl]
		if node <= f.prepass {
			t.Errorf("%s: node %d is not past the prepass at %d", c.name, node, f.prepass)
		}
		if f.plan.Steps[node].RenderPass == nil {
			t.Fatalf("%s: declaration %d maps to node %d, which has no rendering instance", c.name, c.decl, node)
		}
		got := f.engineFormats(c.decl)
		if len(got.ColorAttachmentFormats) == 0 {
			t.Errorf("%s: no colour attachment format", c.name)
		}
	}
}

// Measured on 2724ad5: this two-image, UI-glow rebuild creates 5 render passes
// and 44 framebuffers with an empty cache. Dynamic rendering must create 0/0.
func TestResizeCreatesNoRenderPassesOrFramebuffers(t *testing.T) {
	d := newResizeFakeDriver()
	r := newResizeFixture(d, 2)
	r.sc.captureCapable, r.uiGlowRequested = true, true
	var undo rebuildUndo
	if err := r.rebuildSwapchainTargets(r.sc.extent, &undo); err != nil {
		t.Fatal(err)
	}
	t.Logf("RenderPass=%d Framebuffer=%d", d.created["RenderPass"], d.created["Framebuffer"])
	if d.created["RenderPass"] != 0 || d.created["Framebuffer"] != 0 {
		t.Fatalf("obsolete Vulkan objects: %v", d.created)
	}
	if len(r.sceneTargets) != 2 || len(r.frameGraph.nodes[graphTonemap].targets) != 2 {
		t.Fatal("bindings absent")
	}
	undo.unwind()
	assertBalanced(t, d)
}
func TestFrameGraphTailLayouts(t *testing.T) {
	for _, samples := range []core1_0.SampleCountFlags{core1_0.Samples1, core1_0.Samples4} {
		f, err := newFrameGraph(samples, core1_0.FormatD32SignedFloat, core1_0.FormatB8G8R8A8SRGB, 3, false)
		if err != nil {
			t.Fatal(err)
		}
		if len(f.plan.FinalBarriers) != 0 {
			t.Fatal("tail not restored")
		}
		for _, step := range f.plan.Steps {
			if step.RenderPass != nil && len(step.RenderPass.Attachments) > 0 && len(step.Barriers) == 0 {
				t.Fatal("attachment entry missing")
			}
		}
	}
}
