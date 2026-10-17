package renderer

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/derekmwright/glyphengine/renderer/framegraph"
	"github.com/vkngwrapper/core/v3/core1_0"
)

// foamInstance answers the format query CreateWaterFoam makes with the two
// features the foam target needs.
type foamInstance struct{ bufferTestInstance }

func (foamInstance) GetPhysicalDeviceFormatProperties(core1_0.PhysicalDevice, core1_0.Format) *core1_0.FormatProperties {
	return &core1_0.FormatProperties{OptimalTilingFeatures: core1_0.FormatFeatureStorageImage | core1_0.FormatFeatureSampledImageFilterLinear}
}

// foamDriver adds the one creation call the buffer driver cannot fail: the
// descriptor write that binds the target, the grid and the sampler.
type foamDriver struct {
	*bufferTestDriver
	writes []core1_0.WriteDescriptorSet
}

func (d *foamDriver) UpdateDescriptorSets(w []core1_0.WriteDescriptorSet, c []core1_0.CopyDescriptorSet) error {
	if d.shouldFail("UpdateDescriptorSets") {
		return errInjected
	}
	d.writes = append(d.writes, w...)
	return d.bufferTestDriver.UpdateDescriptorSets(w, c)
}

func foamFixture() (*Renderer, *foamDriver) {
	r, b := bufferFixture()
	d := &foamDriver{bufferTestDriver: b}
	r.deviceDriver = d
	r.instanceDriver = foamInstance{}
	r.caps.storageImageExtendedFormats = true
	return r, d
}

func foamDesc() WaterFoamDesc {
	depth := make([]float32, 9*7)
	for i := range depth {
		depth[i] = float32(i%5) * 0.5
	}
	return WaterFoamDesc{Origin: [2]float32{-4, -3}, Step: [2]float32{1, 1}, Width: 9, Height: 7, Depth: depth}
}

// foamBalance is bufferBalance plus the pipeline-side kinds the shared foam
// pipelines create, which assertBalanced does not track.
func foamBalance(tb testing.TB, d *foamDriver) {
	tb.Helper()
	assertBalanced(tb, d.resizeFakeDriver)
	for _, kind := range []string{"Buffer", "Pipeline", "PipelineLayout", "ShaderModule", "DescriptorSetLayout", "CommandBuffer"} {
		if d.created[kind] != d.destroyed[kind] {
			tb.Errorf("%s: created %d destroyed %d", kind, d.created[kind], d.destroyed[kind])
		}
	}
}

// Every creation site CreateWaterFoam reaches, failed in turn: the error
// names the foam, nothing stays registered, the mesh has no target, and
// everything made before the failure is given back -- including a partial
// shared pipeline set, which is rebuilt from nothing on the next attempt.
//
// The meta-check at the end is what makes the balance meaningful: removing any
// one destroy from the clean run's tally has to be noticed, or a site whose
// unwind forgot a kind would pass here. Verified by deleting the
// DestroyDescriptorSetLayout call in destroyWaterFoamPipelines: the clean run
// and every site past the layout report "DescriptorSetLayout: created 1
// destroyed 0".
//
// CmdCopyBuffer is the site that found something: createDeviceLocalBuffer
// left its command buffer allocated when the copy failed to record, which
// every caller inherited ("CommandBuffer: created 2 destroyed 1").
func TestWaterFoamCreationUnwinds(t *testing.T) {
	create := func(r *Renderer) (*WaterFoam, *Mesh, error) {
		m := &Mesh{}
		f, err := r.CreateWaterFoam(m, foamDesc())
		return f, m, err
	}
	teardown := func(r *Renderer) {
		r.flushAllDeferred()
		r.destroyWaterFoamResources()
	}

	r, control := foamFixture()
	f, m, err := create(r)
	if err != nil {
		t.Fatal(err)
	}
	if m.WaterFoam() != f || len(r.waterFoams) != 1 || !r.graphDirty {
		t.Fatal("a created target is not attached to its mesh, registered and in the next graph")
	}
	teardown(r)
	foamBalance(t, control)

	sites := []string{"CreateDescriptorSetLayout", "CreateShaderModule", "CreatePipelineLayout", "CreateComputePipelines",
		"CreateGraphicsPipelines", "CreateImage", "AllocateMemory", "CreateImageView", "CreateSampler",
		"AllocateDescriptorSets", "CreateBuffer", "BindBufferMemory", "MapMemory", "CmdCopyBuffer", "UpdateDescriptorSets"}
	for _, call := range sites {
		if control.calls[call] == 0 {
			t.Fatalf("CreateWaterFoam never reaches %s; this list proves nothing about it", call)
		}
		for at := 1; at <= control.calls[call]; at++ {
			t.Run(fmt.Sprintf("%s/%d", call, at), func(t *testing.T) {
				r, d := foamFixture()
				d.failCall, d.failAt = call, at
				f, m, err := create(r)
				if !errors.Is(err, errInjected) || f != nil {
					t.Fatalf("got %v, %v", f, err)
				}
				if !strings.Contains(err.Error(), "water foam") {
					t.Fatalf("error does not name the foam: %v", err)
				}
				if len(r.waterFoams) != 0 || m.waterFoam != nil {
					t.Fatal("a failed target stayed registered")
				}
				teardown(r)
				foamBalance(t, d)
			})
		}
	}
	for kind, n := range control.created {
		if n == 0 {
			continue
		}
		control.destroyed[kind]--
		probe := &capturingT{TB: t}
		foamBalance(probe, control)
		control.destroyed[kind]++
		if !probe.failed {
			t.Fatalf("the balance check misses a lost %s", kind)
		}
	}
	t.Logf("%d creation sites unwind clean; the balance check catches a lost destroy of every kind", len(sites))
}

// The target is released through the deferred queue every GPU object here
// retires through, never at once: frames in flight can still be sampling it.
// DestroyMesh reaches the same release.
func TestWaterFoamDestroyRetiresThroughTheDeferredQueue(t *testing.T) {
	for _, viaMesh := range []bool{false, true} {
		r, d := foamFixture()
		m := &Mesh{}
		f, err := r.CreateWaterFoam(m, foamDesc())
		if err != nil {
			t.Fatal(err)
		}
		r.graphDirty = false
		before := d.destroyed["Image"]
		if viaMesh {
			r.releaseMesh(m)
		} else {
			r.DestroyWaterFoam(f)
		}
		if m.waterFoam != nil || len(r.waterFoams) != 0 || !r.graphDirty || !f.destroyed {
			t.Fatalf("viaMesh=%v: destroy did not detach the target and dirty the graph", viaMesh)
		}
		if d.destroyed["Image"] != before {
			t.Fatalf("viaMesh=%v: the image was destroyed immediately, under frames still in flight", viaMesh)
		}
		r.DestroyWaterFoam(f) // a second destroy is a no-op, not a double free
		r.flushAllDeferred()
		r.destroyWaterFoamResources()
		foamBalance(t, d)
	}
	t.Log("DestroyWaterFoam and DestroyMesh both detach at once and release after the frames in flight")
}

func TestWaterFoamDescriptionErrors(t *testing.T) {
	r, _ := foamFixture()
	for _, tc := range []struct {
		why  string
		edit func(*WaterFoamDesc)
	}{
		{"grid", func(d *WaterFoamDesc) { d.Width = 1 }},
		{"depths", func(d *WaterFoamDesc) { d.Depth = d.Depth[1:] }},
		{"step", func(d *WaterFoamDesc) { d.Step[1] = 0 }},
	} {
		desc := foamDesc()
		tc.edit(&desc)
		if _, err := r.CreateWaterFoam(&Mesh{}, desc); err == nil || !strings.Contains(err.Error(), tc.why) {
			t.Errorf("%s: %v", tc.why, err)
		}
	}
	if _, err := r.CreateWaterFoam(nil, foamDesc()); err == nil {
		t.Error("nil mesh accepted")
	}
	r.caps.storageImageExtendedFormats = false
	if _, err := r.CreateWaterFoam(&Mesh{}, foamDesc()); err == nil || !strings.Contains(err.Error(), fmt.Sprint(waterFoamFormat)) {
		t.Errorf("a device that cannot write R16F storage images: %v", err)
	}
}

// withFoamFrame gives buildFrame's water mesh a foam target, the way
// withAppFrame gives it application passes: a graph built by the same
// extendAppGraph a real renderer runs, over fake handles.
func withFoamFrame(fx *frame, foamed bool) (*frame, *Renderer) {
	h := &fakeHandles{next: 30000}
	r := &Renderer{msaaSamples: core1_0.Samples4, depth: &depthResources{format: core1_0.FormatD32SignedFloat},
		sc: &swapchainDetails{imageFormat: core1_0.FormatB8G8R8A8SRGB, imageViews: make([]core1_0.ImageView, 1), extent: fx.extent}}
	r.fallbackTexture = fx.fallbackTexture
	r.waterFoam = waterFoamShared{graphics: h.pipeline(), graphicsLayout: h.layout(), compute: h.pipeline(), computeLayout: h.layout()}
	for i := range fx.draws {
		d := &fx.draws[i]
		if d.Water == nil {
			continue
		}
		d.Water.FoamThreshold, d.Water.FoamDecay, d.Water.FoamColor = 0.8, 3, [3]float32{0.83, 0.88, 0.88}
		if foamed && d.Mesh.waterFoam == nil {
			f := &WaterFoam{r: r, mesh: d.Mesh, width: 31, height: 31, spacing: 1, set: h.descSet(),
				image: &appImages{images: []core1_0.Image{h.image()}}}
			d.Mesh.waterFoam = f
			r.waterFoams = append(r.waterFoams, f)
		}
	}
	g, err := newFrameGraph(r.msaaSamples, r.depth.format, r.sc.imageFormat, 1, false, r.ShadowCascadeCount(), r)
	if err != nil {
		panic(err)
	}
	fx.graph = g
	for i := range g.images {
		g.images[i].images = []core1_0.Image{h.image()}
	}
	g.sizeScratch(&fx.scratch)
	return fx, r
}

// foamProbe counts the calls the foam option is allowed to add.
type foamProbe struct {
	*fakeDriver
	foamLayout   core1_0.PipelineLayout
	dispatches   int
	computeBinds int
	waterSets    [][]core1_0.DescriptorSet
}

func (d *foamProbe) CmdDispatch(cb core1_0.CommandBuffer, x, y, z int) {
	d.dispatches++
	d.fakeDriver.CmdDispatch(cb, x, y, z)
}

func (d *foamProbe) CmdBindPipeline(cb core1_0.CommandBuffer, bp core1_0.PipelineBindPoint, p core1_0.Pipeline) {
	if bp == core1_0.PipelineBindPointCompute {
		d.computeBinds++
	}
	d.fakeDriver.CmdBindPipeline(cb, bp, p)
}

func (d *foamProbe) CmdBindDescriptorSets(cb core1_0.CommandBuffer, bp core1_0.PipelineBindPoint, layout core1_0.PipelineLayout, first int, sets []core1_0.DescriptorSet, offsets []int) {
	if layout == d.foamLayout && bp == core1_0.PipelineBindPointGraphics {
		d.waterSets = append(d.waterSets, append([]core1_0.DescriptorSet(nil), sets...))
	}
	d.fakeDriver.CmdBindDescriptorSets(cb, bp, layout, first, sets, offsets)
}

// No target means no node, no image and no dispatch, and a water draw that
// asks for foam on a mesh without a target records the stream it recorded
// before foam existed -- the pinned hash, unchanged.
//
// Verified to fail: writing the foam threshold and colour whenever the draw has
// a decay, rather than only when it is drawn with a target, reports "a foam
// decay on a surface with no target moved the stream: 0x8a9b6dceabcee7dd, want
// 0x8c25083b2f66a28d".
func TestWaterFoamOffAddsNothing(t *testing.T) {
	fx := buildFrame(97)
	for i := range fx.draws {
		if w := fx.draws[i].Water; w != nil {
			w.FoamThreshold, w.FoamDecay = 0.8, 3
		}
	}
	d := &fakeDriver{hashing: true}
	if err := fx.record(d, 1); err != nil {
		t.Fatal(err)
	}
	if d.h != goldenStreamHash {
		t.Fatalf("a foam decay on a surface with no target moved the stream: %#x, want %#x", d.h, goldenStreamHash)
	}

	plain, pr := withFoamFrame(buildFrame(7), false)
	for _, n := range plain.graph.declarations {
		if strings.HasPrefix(n.Name, "water foam") {
			t.Fatalf("a renderer with no foam target declares %q", n.Name)
		}
	}
	p := &foamProbe{fakeDriver: &fakeDriver{}, foamLayout: pr.waterFoam.graphicsLayout}
	if err := plain.record(p, 1); err != nil {
		t.Fatal(err)
	}
	if p.dispatches != 0 || len(p.waterSets) != 0 {
		t.Fatalf("no target, yet %d dispatches and %d foam binds", p.dispatches, len(p.waterSets))
	}
	t.Logf("pinned stream %#x unchanged; no foam node, image or dispatch", d.h)
}

// One target per foamed surface, one dispatch per target per frame however
// many draws share its mesh, the compute write declared and its edge to the
// water pass's fragment stage derived, and the foamed water drawn with the
// foam pipeline and its third set.
//
// Verified to fail: dropping the water node's SampledRead of the foam image in
// extendAppGraph reports "entry to General true, write made visible to the
// fragment stage true, water reads foam false" -- the barrier survives on the
// restore node's declaration, but nothing ties the water pass to it, which
// only synchronization validation would otherwise see.
func TestWaterFoamAddsOneTargetAndOneDispatchPerSurface(t *testing.T) {
	fx, _ := withFoamFrame(buildFrame(7), true)
	g := fx.graph
	if len(g.foams) != 1 || len(g.foamImages) != 1 {
		t.Fatalf("buildFrame's two water draws share one mesh: want one target, got %d", len(g.foams))
	}
	foam := g.foams[0]
	img := g.foamImages[0]

	compute, restore, water := -1, -1, g.engine[graphWater]
	for i, n := range g.declarations {
		switch n.Name {
		case "water foam":
			compute = i
		case "water foam sampled":
			restore = i
		}
	}
	if compute < 0 || restore != compute+1 || compute >= g.engine[graphCopy] {
		t.Fatalf("dispatch at %d, restore at %d, copy at %d: the foam node must lead the copy with its restore beside it", compute, restore, g.engine[graphCopy])
	}
	n := g.declarations[compute]
	if n.Kind != framegraph.Compute || len(n.Uses) != 1 || n.Uses[0].Resource != img || n.Uses[0].Access != framegraph.StorageReadWrite {
		t.Fatalf("foam node declares %+v", n)
	}
	if n.OptionalGroup == 0 || g.declarations[restore].OptionalGroup != n.OptionalGroup {
		t.Fatal("the dispatch and its restore are not one optional group")
	}
	entry, exit, waterReads := false, false, false
	for _, b := range g.plan.Steps[compute].Barriers {
		entry = entry || b.Resource == img && b.NewLayout == core1_0.ImageLayoutGeneral && b.DstStage&core1_0.PipelineStageComputeShader != 0
	}
	for _, b := range g.plan.Steps[restore].Barriers {
		exit = exit || b.Resource == img && b.OldLayout == core1_0.ImageLayoutGeneral &&
			b.NewLayout == core1_0.ImageLayoutShaderReadOnlyOptimal && b.SrcAccess&core1_0.AccessShaderWrite != 0 &&
			b.DstStage&core1_0.PipelineStageFragmentShader != 0
	}
	for _, u := range g.declarations[water].Uses {
		waterReads = waterReads || u.Resource == img && u.Access == framegraph.SampledRead && u.Stages == core1_0.PipelineStageFragmentShader
	}
	if !entry || !exit || !waterReads {
		t.Fatalf("entry to General %v, write made visible to the fragment stage %v, water reads foam %v", entry, exit, waterReads)
	}
	images := 0
	for _, res := range g.plan.Resources {
		if strings.HasPrefix(res.Desc.Name, "water foam") {
			images++
		}
	}
	if images != 1 {
		t.Fatalf("%d foam images declared, want 1", images)
	}

	for frame := 0; frame < 3; frame++ {
		fx.lighting.Time = 1 + float32(frame)*0.5
		p := &foamProbe{fakeDriver: &fakeDriver{}, foamLayout: foam.r.waterFoam.graphicsLayout}
		if err := fx.record(p, frame%2); err != nil {
			t.Fatal(err)
		}
		if p.dispatches != 1 || p.computeBinds != 1 {
			t.Fatalf("frame %d: %d dispatches, %d compute binds; want one each", frame, p.dispatches, p.computeBinds)
		}
		if len(p.waterSets) != 2 {
			t.Fatalf("frame %d: %d water draws bound the foam set, want both", frame, len(p.waterSets))
		}
		for _, sets := range p.waterSets {
			if sets[2] != foam.set {
				t.Fatal("the foam pipeline's set 2 is not the target's")
			}
		}
		// The decay is the shader time since the last step over Decay, and
		// zero on the first.
		want := float32(0)
		if frame > 0 {
			want = 0.5 / 3
		}
		if foam.push[5] != want {
			t.Fatalf("frame %d: decay %v, want %v", frame, foam.push[5], want)
		}
	}
	t.Log("one image, one dispatch per frame, General entry, fragment-visible exit, the water pass reads it")
}

// A frame with foam allocates nothing either: the step, the push and the
// third set all come out of state that already exists.
func TestWaterFoamRecordsWithoutAllocating(t *testing.T) {
	for _, n := range []int{7, 97} {
		fx, _ := withFoamFrame(buildFrame(n), true)
		d := &fakeDriver{}
		if err := fx.record(d, 1); err != nil {
			t.Fatal(err)
		}
		a := testing.AllocsPerRun(50, func() {
			fx.lighting.Time += 0.016
			if err := fx.record(d, 1); err != nil {
				panic(err)
			}
		})
		t.Logf("%d engine draws with a foamed surface: %.0f allocs/frame", n, a)
		if a != 0 {
			t.Errorf("%.0f allocations per frame", a)
		}
	}
}

// A target created after the live graph was built has no node in it yet, and
// must not be marked as stepped -- the next graph will step it from zero.
func TestWaterFoamUnlistedTargetDoesNotStep(t *testing.T) {
	fx, _ := withFoamFrame(buildFrame(7), false)
	f := &WaterFoam{width: 4, height: 4}
	for i := range fx.draws {
		if fx.draws[i].Water != nil {
			fx.draws[i].Mesh.waterFoam = f
		}
	}
	prepareWaterFoam(fx.graph, fx.draws, 1)
	if f.active || f.stepped {
		t.Fatal("a target the graph does not list was stepped")
	}
	for i := range fx.draws {
		if fx.draws[i].Water != nil {
			fx.draws[i].Mesh.waterFoam = nil
		}
	}
}

func TestHalfToFloat(t *testing.T) {
	for _, c := range []struct {
		h    uint16
		want float32
	}{{0x0000, 0}, {0x3c00, 1}, {0x3800, 0.5}, {0x3555, 0.33325195}, {0x0400, 6.1035156e-05}, {0x0001, 5.9604645e-08}, {0xbc00, -1}} {
		if got := halfToFloat(c.h); got != c.want {
			t.Errorf("%#04x: %v, want %v", c.h, got, c.want)
		}
	}
}

// Two surfaces are two targets and two dispatches: the count is per surface,
// not per frame.
func TestWaterFoamTwoSurfacesTwoDispatches(t *testing.T) {
	fx := buildFrame(7)
	seen := 0
	for i := range fx.draws {
		if fx.draws[i].Water != nil {
			if seen == 1 {
				m := *fx.draws[i].Mesh
				fx.draws[i].Mesh = &m
			}
			seen++
		}
	}
	fx, r := withFoamFrame(fx, true)
	if len(fx.graph.foams) != 2 {
		t.Fatalf("two meshes, %d targets", len(fx.graph.foams))
	}
	p := &foamProbe{fakeDriver: &fakeDriver{}, foamLayout: r.waterFoam.graphicsLayout}
	if err := fx.record(p, 1); err != nil {
		t.Fatal(err)
	}
	if p.dispatches != 2 || len(p.waterSets) != 2 || p.waterSets[0][2] == p.waterSets[1][2] {
		t.Fatalf("%d dispatches, %d foam draws", p.dispatches, len(p.waterSets))
	}
}
