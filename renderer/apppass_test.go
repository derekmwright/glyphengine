package renderer

import (
	"strings"
	"testing"

	"github.com/derekmwright/glyphengine/shaders"
	"github.com/vkngwrapper/core/v3/core1_0"
)

func withAppFrame(fx *frame, passes bool, compute ...bool) *frame {
	h := &fakeHandles{next: 10000}
	r := &Renderer{msaaSamples: core1_0.Samples4, depth: &depthResources{format: core1_0.FormatD32SignedFloat}, sc: &swapchainDetails{imageFormat: core1_0.FormatB8G8R8A8SRGB, imageViews: make([]core1_0.ImageView, 1), extent: fx.extent}}
	r.fallbackTexture = fx.fallbackTexture
	r.depthResolve = &sceneDepthResources{r: r, pipeline: h.pipeline(), layout: h.layout(), sets: []core1_0.DescriptorSet{h.descSet()}}
	if passes {
		t := &RenderTarget{r: r, desc: RenderTargetDesc{Name: "fixture target", Format: TargetR16F, Scale: 1}, color: &appImages{extent: fx.extent}}
		t.texture.target = t
		r.appTargets = []*RenderTarget{t}
		mesh := &AppPass{r: r, desc: AppPassDesc{Name: "fixture mesh", Stage: StageBeforeScene, Target: t}, enabled: true, pipeline: h.pipeline(), layout: h.layout(), draws: fx.draws[:2]}
		mesh.sets = []core1_0.DescriptorSet{h.descSet(), h.descSet()}
		full := &AppPass{r: r, desc: AppPassDesc{Name: "fixture fullscreen", Stage: StageBeforeBloom, Load: true, Reads: []*Texture{t.Texture(), r.SceneDepth()}, Fullscreen: true}, enabled: true, pipeline: h.pipeline(), layout: h.layout(), sets: []core1_0.DescriptorSet{h.descSet(), h.descSet()}}
		r.appPasses = []*AppPass{mesh, full}
	}
	if len(compute) > 0 && compute[0] {
		target := &RenderTarget{r: r, desc: RenderTargetDesc{Name: "compute output", Format: TargetR32F, Scale: 1, History: true, Storage: true}, color: &appImages{extent: fx.extent}}
		target.texture.target = target
		r.appTargets = append(r.appTargets, target)
		c := &AppCompute{desc: AppComputeDesc{Name: "fixture compute", Stage: StageBeforeScene, Reads: []*Texture{r.appTargets[0].Texture(), target.Texture()}, Writes: []*RenderTarget{target}}, dispatch: [3]uint32{80, 45, 1}}
		p := &AppPass{r: r, desc: AppPassDesc{Name: c.desc.Name, Stage: c.desc.Stage, Reads: c.desc.Reads}, enabled: true, compute: c, pipeline: h.pipeline(), layout: h.layout(), sets: []core1_0.DescriptorSet{h.descSet(), h.descSet()}}
		c.pass = p
		r.appPasses = append(r.appPasses, p)
	}
	g, err := newFrameGraph(r.msaaSamples, r.depth.format, r.sc.imageFormat, 1, r)
	if err != nil {
		panic(err)
	}
	fx.graph = g
	for i := range g.nodes {
		n := &g.nodes[i]
		// The streamed upload node takes no fixture handle. fakeHandles is a
		// monotonic counter, so spending one here would renumber every image
		// below it and move the pinned hashes for a reason that has nothing to
		// do with what is recorded -- and those hashes being UNCHANGED is the
		// evidence that a frame with nothing queued records what it always did.
		// Verified: spending one here leaves the call counts at 3353 and 3382
		// and moves both hashes (0x4b9e77888c85b5ff, 0x629f25430c127a68).
		if n.name == "streamed uploads" {
			continue
		}
		h.n() // Preserve the old fixture's subsequent draw handles.
		n.extent = fx.extent
		if n.app != nil || i == g.depthNode {
			h.n()
		}
	}
	for i := range g.images {
		g.images[i].images = []core1_0.Image{h.image()}
	}
	g.sizeScratch(&fx.scratch)
	return fx
}

// Depth adds one barrier, begin/end and five draw calls: 3307 versus 3299.
// The app fixture adds 15 calls for two mesh draws, one visibility barrier,
// and eight for the fullscreen pass: 3331 total. Counts assert the direction
// independently of the hashes so an omitted pass cannot be repinned as success.
// Three-set bindings reuse draw textures at set 0 and add pass inputs at set 2:
// the app hash moved from 0xee218de19dd3548f without adding a driver call. The
// fixture now allocates two pass-input handles instead of four per-draw handles.
// The point-light instance-shadow fix: the cube pass now uses the
// instanced depth pipeline and placement count. The base fixture adds 18 driver
// calls (3299 -> 3317); application/UI/volumetric additions are unchanged.
// Removing only that fix restores 0x0db1df67cfc2a68b and fails
// TestPointShadowUsesInstanceTransforms (6 instances, want 18).
// Dynamic rendering (#120): full-stream hashes include explicit attachment
// barriers and CmdBegin/EndRendering. Base 3317 -> 3342 calls; depth 3353,
// application 3382, compute 3390, glow 3442, volumetric 3348, GPU LOD 1746.
// TestMigrationDrawStreams independently pins every non-rendering/barrier call
// to the measured pre-migration stream, including all draw-side arguments.
const goldenSceneDepthStreamHash Hasher = 0xa09ed6adcc0491ab
const goldenAppPassStreamHash Hasher = 0x3dcd635efb9f373c

func TestAppPassStreams(t *testing.T) {
	base := &fakeDriver{hashing: true}
	if err := buildFrame(97).record(base, 1); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name   string
		passes bool
		hash   Hasher
		extra  int
	}{{"scene-depth", false, goldenSceneDepthStreamHash, 11}, {"app-passes", true, goldenAppPassStreamHash, 40}} {
		t.Run(test.name, func(t *testing.T) {
			d := &fakeDriver{hashing: true}
			if err := withAppFrame(buildFrame(97), test.passes).record(d, 1); err != nil {
				t.Fatal(err)
			}
			t.Logf("base: %d calls, hash %#x; %s: %d calls, hash %#x; added %d calls", base.calls, base.h, test.name, d.calls, d.h, d.calls-base.calls)
			if d.h != test.hash {
				t.Errorf("hash %#x want %#x", d.h, test.hash)
			}
			if d.calls-base.calls != test.extra {
				t.Fatalf("added %d driver calls, want %d", d.calls-base.calls, test.extra)
			}
		})
	}
}

func TestAppPassAllocs(t *testing.T) {
	for _, n := range []int{7, 97, 511} {
		fx := withAppFrame(buildFrame(n), true)
		d := &fakeDriver{}
		if err := fx.record(d, 1); err != nil {
			t.Fatal(err)
		}
		a := testing.AllocsPerRun(50, func() {
			if err := fx.record(d, 1); err != nil {
				panic(err)
			}
		})
		t.Logf("%d engine draws plus mesh and fullscreen/depth passes: %.0f allocs/frame", n, a)
		if a != 0 {
			t.Errorf("%.0f allocations", a)
		}
	}
}

func TestAppPassDescriptions(t *testing.T) {
	r := &Renderer{}
	target := &RenderTarget{r: r, desc: RenderTargetDesc{Format: TargetR16F, Scale: 1}}
	target.texture.target = target
	good := AppPassDesc{Name: "test", Stage: StageBeforeScene, Target: target, Fullscreen: true, Vert: shaders.DepthResolveVertSpv, Frag: shaders.DepthResolveFragSpv}
	for _, test := range []struct {
		field string
		edit  func(*AppPassDesc)
	}{
		{"Stage", func(d *AppPassDesc) { d.Stage = 0 }},
		{"Target", func(d *AppPassDesc) { d.Target = nil }},
		{"Load", func(d *AppPassDesc) { d.Target = nil; d.Stage = StageBeforeBloom }},
		{"DepthTest", func(d *AppPassDesc) { d.DepthTest = true }},
		{"Reads", func(d *AppPassDesc) { d.Reads = []*Texture{target.Texture()} }},
		{"Reads", func(d *AppPassDesc) { d.Reads = make([]*Texture, 5) }},
		{"Reads", func(d *AppPassDesc) { d.Reads = []*Texture{r.SceneDepth()} }},
		{"Fullscreen", func(d *AppPassDesc) { d.Fullscreen = false }},
		{"Vert", func(d *AppPassDesc) { d.Vert = nil }},
		{"Blend", func(d *AppPassDesc) { d.Blend = 99 }},
	} {
		d := good
		test.edit(&d)
		if err := r.validateAppPass(d); err == nil || !strings.Contains(err.Error(), test.field) {
			t.Errorf("%s: %v", test.field, err)
		}
	}
	target.desc.History = true
	good.Reads = []*Texture{target.Texture()}
	if err := r.validateAppPass(good); err != nil {
		t.Fatal(err)
	}
	for range maxAppTimings {
		r.appPasses = append(r.appPasses, &AppPass{desc: AppPassDesc{Timed: true}})
	}
	good.Timed = true
	if err := r.validateAppPass(good); err == nil || !strings.Contains(err.Error(), "Timed") {
		t.Fatalf("17th timing: %v", err)
	}
	for _, d := range []RenderTargetDesc{{Format: 99, Scale: 1}, {Format: TargetR16F}, {Format: TargetR16F, Width: 1}} {
		if validateTarget(d) == nil {
			t.Errorf("accepted %+v", d)
		}
	}
	p := &AppPass{}
	data := make([]byte, 128)
	data[3] = 63
	if err := p.SetPushConstants(data); err != nil {
		t.Fatal(err)
	}
	old := p.push
	for _, n := range []int{1, 127, 144} {
		if p.SetPushConstants(make([]byte, n)) == nil || p.push != old {
			t.Fatalf("bad push size %d", n)
		}
	}
	if err := p.SetPushConstants(nil); err != nil || p.push != [32]float32{} {
		t.Fatal("nil did not clear push block")
	}
}
