package renderer

import (
	"github.com/vkngwrapper/core/v3/core1_0"
	"github.com/vkngwrapper/core/v3/loader"
	"reflect"
	"testing"
)

func (fx *frame) initGraph() {
	var err error
	fx.graph, err = newFrameGraph(core1_0.Samples4, core1_0.FormatD32SignedFloat, core1_0.FormatB8G8R8A8SRGB, 1)
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
		if len(n.targets) > 0 && len(n.targets[0].info.ColorAttachments) > 0 {
			continue
		}
		n.extent = fx.extent
		n.targets = []*renderingTarget{f.bindRenderingTarget(i, 0)}
	}
	if fx.target == nil {
		r := &Renderer{sc: &swapchainDetails{extent: fx.extent}, hdr: &hdrTarget{images: []core1_0.Image{fx.sceneImage}, views: f.images[f.hdr].views}, depth: &depthResources{images: f.images[f.depth].images, views: f.images[f.depth].views}, msaa: &msaaResources{images: f.images[f.color].images, views: f.images[f.color].views}}
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

func TestFrameGraphWaterFormatsMatchScene(t *testing.T) {
	for _, samples := range []core1_0.SampleCountFlags{core1_0.Samples1, core1_0.Samples4} {
		f, err := newFrameGraph(samples, core1_0.FormatD32SignedFloat, core1_0.FormatB8G8R8A8SRGB, 3)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(f.pipelineFormats(graphWater), colorDepthFormats(hdrFormat, core1_0.FormatD32SignedFloat)) {
			t.Fatal("water and scene attachment formats differ")
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
		f, err := newFrameGraph(samples, core1_0.FormatD32SignedFloat, core1_0.FormatB8G8R8A8SRGB, 3)
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
