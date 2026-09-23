package renderer

import (
	"errors"
	"testing"

	"github.com/derekmwright/glyphengine/renderer/framegraph"
	"github.com/vkngwrapper/core/v3/common"
	"github.com/vkngwrapper/core/v3/core1_0"
	"github.com/vkngwrapper/core/v3/loader"
)

// Bind the existing fake handles to the real executor. Allocating another fake
// handle would renumber the command stream and invalidate its pinned baseline.
func (fx *frame) initGraph() {
	var err error
	fx.graph, err = newFrameGraph(core1_0.Samples4, core1_0.FormatD32SignedFloat, core1_0.FormatB8G8R8A8SRGB, 1)
	if err != nil {
		panic(err)
	}
	fx.graph.sizeScratch(&fx.scratch)
	for i := range fx.graph.nodes {
		fx.graph.nodes[i].framebuffers = make([]core1_0.Framebuffer, 1)
	}
	for i := range fx.graph.images {
		fx.graph.images[i].images = make([]core1_0.Image, 1)
	}
}

type scenePassDriver struct {
	core1_0.DeviceDriver
	info core1_0.RenderPassCreateInfo
}

func (d *scenePassDriver) CreateRenderPass(_ *loader.AllocationCallbacks, info core1_0.RenderPassCreateInfo) (core1_0.RenderPass, common.VkResult, error) {
	d.info = info
	return core1_0.RenderPass{}, core1_0.VKSuccess, nil
}

// Use the real legacy constructor, not another declaration copied from water.
func TestFrameGraphWaterOrderCompatibleWithLegacyScene(t *testing.T) {
	for _, samples := range []core1_0.SampleCountFlags{core1_0.Samples1, core1_0.Samples4} {
		d := &scenePassDriver{}
		if _, err := createRenderPass(d, hdrFormat, core1_0.FormatD32SignedFloat, samples); err != nil {
			t.Fatal(err)
		}
		scene := &framegraph.RenderPassDesc{Depth: -1, Dependencies: d.info.SubpassDependencies}
		for _, a := range d.info.Attachments {
			scene.Attachments = append(scene.Attachments, framegraph.AttachmentDesc{Format: a.Format, Samples: a.Samples})
		}
		sub := d.info.Subpasses[0]
		for i, a := range sub.ColorAttachments {
			scene.Color = append(scene.Color, a.Attachment)
			resolve := -1
			if len(sub.ResolveAttachments) > 0 {
				resolve = sub.ResolveAttachments[i].Attachment
			}
			scene.Resolve = append(scene.Resolve, resolve)
		}
		if sub.DepthStencilAttachment != nil {
			scene.Depth = sub.DepthStencilAttachment.Attachment
		}
		g, err := newFrameGraph(samples, core1_0.FormatD32SignedFloat, core1_0.FormatB8G8R8A8SRGB, 3)
		if err != nil {
			t.Fatal(err)
		}
		water := g.plan.Steps[graphWater].RenderPass
		if !framegraph.Compatible(scene, water) {
			t.Fatal("water pipelines cannot run in the legacy scene pass")
		}
		water.Dependencies[0].SrcStageMask = 0
		if framegraph.Compatible(scene, water) {
			t.Fatal("compatibility check missed a broken dependency")
		}
	}
}

func TestFrameGraphCacheLifetimeAndFailure(t *testing.T) {
	for failAt := 0; failAt <= 5; failAt++ {
		d := newResizeFakeDriver()
		d.failCall, d.failAt = "CreateRenderPass", failAt
		g, err := newFrameGraph(core1_0.Samples4, core1_0.FormatD32SignedFloat, core1_0.FormatB8G8R8A8SRGB, 3)
		if err != nil {
			t.Fatal(err)
		}
		for i, step := range g.plan.Steps {
			if step.RenderPass == nil {
				continue
			}
			_, err = g.renderPass(d, i)
			if err != nil {
				break
			}
		}
		if failAt > 0 && !errors.Is(err, errInjected) {
			t.Fatalf("failure %d: %v", failAt, err)
		}
		if failAt == 0 && (err != nil || d.created["RenderPass"] != 5) {
			t.Fatalf("cache did not share descriptions: %v %+v", err, d.created)
		}
		g.destroyPasses(d)
		assertBalanced(t, d)
		if failAt == 0 {
			d.destroyed["RenderPass"]--
			capture := &capturingT{TB: t}
			assertBalanced(capture, d)
			if !capture.failed {
				t.Fatal("balance check missed a leaked cached pass")
			}
		}
	}
}

func TestFrameGraphRecreateAllFramebufferFailures(t *testing.T) {
	// Two instances of water, nine scene bloom stages, UI, nine glow stages,
	// tonemap, plus two legacy scene framebuffers. Fail every graph allocation.
	const count = 2
	for failAt := count + 1; failAt <= count*(1+1+graphBloomStages+1+graphBloomStages+1); failAt++ {
		d := newResizeFakeDriver()
		r := newResizeFixture(d, count)
		r.sc.captureCapable, r.uiGlowRequested = true, true
		d.failCall, d.failAt = "CreateFramebuffer", failAt
		if err := attemptRebuild(r, r.sc.extent); !errors.Is(err, errInjected) {
			t.Fatalf("framebuffer %d: %v", failAt, err)
		}
		assertBalanced(t, d)
		for _, n := range r.frameGraph.nodes {
			if len(n.framebuffers) != 0 {
				t.Fatalf("%s survived failure %d", n.name, failAt)
			}
		}
		if r.uiLayer != nil || r.sceneColor != nil {
			t.Fatal("optional targets survived unwind")
		}
		if failAt == count+3 {
			d.destroyed["Framebuffer"]--
			capture := &capturingT{TB: t}
			assertBalanced(capture, d)
			if !capture.failed {
				t.Fatal("balance check missed a leaked graph framebuffer")
			}
			d.destroyed["Framebuffer"]++
		}
		// Successful retry must restore optional targets too.
		d.failCall = ""
		var undo rebuildUndo
		if err := r.rebuildSwapchainTargets(r.sc.extent, &undo); err != nil {
			t.Fatal(err)
		}
		if r.uiLayer == nil || r.sceneColor == nil {
			t.Fatal("retry omitted an optional target")
		}
		undo.unwind()
		assertBalanced(t, d)
	}
}

func (fx *frame) bindGraph() {
	f := fx.graph
	f.images[f.hdr].images[0] = fx.sceneImage
	if fx.sceneColor != nil {
		f.images[f.copy].images[0] = fx.sceneColor.image
	}
	bind := func(id int, pass core1_0.RenderPass, fb core1_0.Framebuffer, extent core1_0.Extent2D) {
		n := &f.nodes[f.engine[id]]
		n.pass, n.framebuffers[0], n.extent = pass, fb, extent
	}
	bind(graphWater, fx.waterRenderPass, fx.waterFramebuffer, fx.extent)

	bind(graphTonemap, fx.tonemap.renderPass, fx.tonemap.framebuffer, fx.extent)
}

func TestFrameGraphTailLayoutsAndCache(t *testing.T) {
	for _, samples := range []core1_0.SampleCountFlags{core1_0.Samples1, core1_0.Samples4} {
		g, err := newFrameGraph(samples, core1_0.FormatD32SignedFloat, core1_0.FormatB8G8R8A8SRGB, 3)
		if err != nil {
			t.Fatal(err)
		}
		if len(g.plan.FinalBarriers) != 0 {
			t.Fatalf("tail needs final barriers: %+v", g.plan.FinalBarriers)
		}
		for i, step := range g.plan.Steps {
			if i != graphCopy && i != graphWater && len(step.Barriers) != 0 {
				t.Fatalf("unexpected barriers at %s: %+v", g.nodes[i].name, step.Barriers)
			}
		}
		if len(g.plan.Steps[graphCopy].Barriers) != 2 || len(g.plan.Steps[graphWater].Barriers) != 1 {
			t.Fatal("copy barriers missing")
		}
		var scratch commandScratch
		g.sizeScratch(&scratch)
		if len(scratch.barriers) != 2 {
			t.Fatalf("widest group: %d", len(scratch.barriers))
		}
		keys := map[framegraph.RenderPassKey]int{}
		for _, step := range g.plan.Steps {
			if step.RenderPass != nil {
				keys[step.RenderPass.Key()]++
			}
		}
		if len(keys) != 5 {
			t.Fatalf("cache has %d descriptions, want water, down, up, UI and tonemap", len(keys))
		}
		if keys[g.plan.Steps[graphBloom].RenderPass.Key()] != 10 || keys[g.plan.Steps[graphBloom+bloomLevels].RenderPass.Key()] != 8 {
			t.Fatal("bloom descriptions no longer shared by both chains")
		}
	}
}
