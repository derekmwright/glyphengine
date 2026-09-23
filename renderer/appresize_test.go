package renderer

import (
	"errors"
	"testing"

	"github.com/derekmwright/glyphengine/renderer/framegraph"
	"github.com/vkngwrapper/core/v3/core1_0"
)

// Real allocation and framebuffer paths, including a fixed-size target, a
// relative history target with depth, and the per-swapchain scene depth copy.
func appResizeFixture(d *resizeFakeDriver) *Renderer {
	r := newResizeFixture(d, 3)
	r.sc.captureCapable = true
	r.appSetLayout = d.h.descriptorSetLayout()
	r.depthResolve = &sceneDepthResources{r: r, pipeline: d.h.pipeline(), layout: d.h.layout()}
	for _, desc := range []RenderTargetDesc{{Name: "fixed", Format: TargetR16F, Width: 32, Height: 24}, {Name: "history", Format: TargetRGBA16F, Scale: 0.5, Depth: true, History: true}} {
		t := &RenderTarget{r: r, desc: desc}
		t.texture.target = t
		r.appTargets = append(r.appTargets, t)
		p := &AppPass{r: r, desc: AppPassDesc{Name: desc.Name, Stage: StageBeforeScene, Target: t, DepthTest: desc.Depth, Fullscreen: true}, enabled: true}
		if desc.History {
			p.desc.Reads = []*Texture{t.Texture()}
		}
		r.appPasses = append(r.appPasses, p)
	}
	r.graphDirty = true
	return r
}

func TestAppTargetRebuildFailures(t *testing.T) {
	control := newResizeFakeDriver()
	r := appResizeFixture(control)
	oldCache := appCacheKeys(r)
	var undo rebuildUndo
	if err := r.rebuildSwapchainTargets(r.sc.extent, &undo); err != nil {
		t.Fatal(err)
	}
	counts := control.calls
	undo.unwind()
	freeNewAppPasses(r, oldCache)
	assertBalanced(t, control)
	// Cached render passes deliberately outlive each rebuild attempt.
	for _, call := range []string{"CreateImage", "AllocateMemory", "CreateImageView", "CreateSampler", "AllocateDescriptorSets", "CreateFramebuffer"} {
		for at := 1; at <= counts[call]; at++ {
			d := newResizeFakeDriver()
			r := appResizeFixture(d)
			old := make(map[framegraph.RenderPassKey]bool)
			for k := range r.frameGraph.cache {
				old[k] = true
			}
			d.failCall, d.failAt = call, at
			var undo rebuildUndo
			err := r.rebuildSwapchainTargets(r.sc.extent, &undo)
			if !errors.Is(err, errInjected) {
				t.Fatalf("%s/%d: %v", call, at, err)
			}
			undo.unwind()
			// The lazy depth resource and cache may have been constructed before
			// the failing framebuffer; both remain renderer-owned until shutdown.
			r.depthResolve.releaseTargets()
			for k, p := range r.frameGraph.cache {
				if !old[k] {
					d.DestroyRenderPass(p, nil)
					delete(r.frameGraph.cache, k)
				}
			}
			assertBalanced(t, d)
		}
		t.Logf("%s: all %d creation sites unwind without leaks", call, counts[call])
	}
}

func TestAppTargetsSurviveResize(t *testing.T) {
	d := newResizeFakeDriver()
	r := appResizeFixture(d)
	oldCache := appCacheKeys(r)
	var first rebuildUndo
	if err := r.rebuildSwapchainTargets(r.sc.extent, &first); err != nil {
		t.Fatal(err)
	}
	fixed, relative := r.appTargets[0], r.appTargets[1]
	ptr := relative.Texture()
	fixedImage := fixed.color.images[0]
	oldImage := relative.color.images[0]
	r.releaseGraphFramebuffers()
	r.releaseAppResizeTargets()
	if fixed.color.images[0] != fixedImage {
		t.Fatal("fixed target was retired on resize")
	}
	var undo rebuildUndo
	r.sc.extent = core1_0.Extent2D{Width: 800, Height: 600}
	if err := r.rebuildAppTargets(&undo); err != nil {
		t.Fatal(err)
	}
	if relative.Texture() != ptr || relative.color.images[0] == oldImage {
		t.Fatal("resize did not preserve pointer and replace image")
	}
	if w, h := relative.Extent(); w != 400 || h != 300 {
		t.Fatalf("extent %dx%d", w, h)
	}
	relative.selectTexture(0)
	a := relative.Texture().image
	relative.selectTexture(1)
	b := relative.Texture().image
	relative.selectTexture(0)
	if a == b || relative.Texture().image != a {
		t.Fatal("history does not alternate by frame slot")
	}
	t.Log("fixed target unchanged; relative target resized to 400x300; texture pointer stable; history alternates")
	// Return the newly rebuilt target, then the first attempt's remaining
	// owners. Their destroy methods tolerate the already returned target sets.
	undo.unwind()
	first.unwind()
	freeNewAppPasses(r, oldCache)
	assertBalanced(t, d)
}

func appCacheKeys(r *Renderer) map[framegraph.RenderPassKey]bool {
	out := make(map[framegraph.RenderPassKey]bool)
	for k := range r.frameGraph.cache {
		out[k] = true
	}
	return out
}
func freeNewAppPasses(r *Renderer, old map[framegraph.RenderPassKey]bool) {
	for k, p := range r.frameGraph.cache {
		if !old[k] {
			r.deviceDriver.DestroyRenderPass(p, nil)
			delete(r.frameGraph.cache, k)
		}
	}
}
