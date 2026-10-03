package renderer

import (
	"testing"

	"github.com/vkngwrapper/core/v3/common"
	"github.com/vkngwrapper/core/v3/core1_0"
	"github.com/vkngwrapper/core/v3/loader"
)

// withDepthPrepass turns the recording fixture's depth prepass on: five distinct
// pipeline handles and a graph compiled with the node in it.
//
// A separate helper rather than a change to buildFrame, exactly as withUILayer
// and withVolumetricLight are, and for the same reason. The option has to be FREE
// when nothing asks for it, so the fixture that pins the recorder's stream has to
// be able to stay exactly as it was -- and goldenStreamHash being UNCHANGED
// across this whole change is the evidence for that rather than a claim about it.
//
// The handles start at 100000 so they cannot collide with any handle buildFrame
// spent, which is what lets a test say a given pipeline was NOT bound.
func withDepthPrepass(fx *frame) *frame {
	h := &fakeHandles{next: 100000}
	fx.prepass = depthPrepassPipelines{
		depth: h.pipeline(), depthInstanced: h.pipeline(),
		lit: h.pipeline(), material: h.pipeline(), instanced: h.pipeline(),
		layout: fx.litPipelineLayout, mode: DepthPrepassOn, active: true,
	}
	g, err := newFrameGraph(core1_0.Samples4, core1_0.FormatD32SignedFloat, core1_0.FormatB8G8R8A8SRGB, 1, true)
	if err != nil {
		panic(err)
	}
	fx.graph = g
	g.sizeScratch(&fx.scratch)
	return fx
}

// prepassRecorder is fakeDriver that also keeps the pipelines it was handed, in
// order, so a test can say which variant a draw was recorded against rather than
// only that the stream as a whole changed.
type prepassRecorder struct {
	fakeDriver
	bound []uint64
}

func (d *prepassRecorder) CmdBindPipeline(cb core1_0.CommandBuffer, bp core1_0.PipelineBindPoint, p core1_0.Pipeline) {
	d.bound = append(d.bound, uint64(p.Handle()))
	d.fakeDriver.CmdBindPipeline(cb, bp, p)
}

func (d *prepassRecorder) first(p core1_0.Pipeline) int {
	for i, h := range d.bound {
		if h == uint64(p.Handle()) {
			return i
		}
	}
	return -1
}

// The option off is the default, and "off is free" is pinned by
// goldenStreamHash in commands_alloc_test.go rather than by anything here: that
// constant has not moved across this change, so a frame built without
// WithDepthPrepass records the same driver calls with the same arguments it
// always did. What this test adds is the other half of the same statement at the
// level of the graph -- no declaration, no node, no step, nothing to skip.
func TestDepthPrepassOffDeclaresNothing(t *testing.T) {
	f, err := newFrameGraph(core1_0.Samples4, core1_0.FormatD32SignedFloat, core1_0.FormatB8G8R8A8SRGB, 1, false)
	if err != nil {
		t.Fatal(err)
	}
	if f.prepass != -1 {
		t.Errorf("prepass node index = %d, want -1 with the option off", f.prepass)
	}
	for i, n := range f.nodes {
		if n.name == depthPrepassNode {
			t.Fatalf("node %d is %q with the option off", i, n.name)
		}
	}
}

// The node has to come before the scene, own the depth clear, keep its contents,
// and agree with the scene on the sample count. Each of those is a silent
// failure if it is wrong: ordered after the scene it would erase the frame,
// clearing nothing it would test against last frame's depth, a DONT_CARE store
// would throw it away, and a sample-count mismatch is a pipeline the scene pass
// cannot share depth with.
func TestDepthPrepassNodePrecedesTheSceneAndKeepsItsDepth(t *testing.T) {
	for _, samples := range []core1_0.SampleCountFlags{core1_0.Samples1, core1_0.Samples4} {
		f, err := newFrameGraph(samples, core1_0.FormatD32SignedFloat, core1_0.FormatB8G8R8A8SRGB, 3, true)
		if err != nil {
			t.Fatal(err)
		}
		if f.prepass < 0 {
			t.Fatal("no prepass node with the option on")
		}
		if f.nodes[f.prepass].name != depthPrepassNode {
			t.Fatalf("node %d is %q, want %q", f.prepass, f.nodes[f.prepass].name, depthPrepassNode)
		}
		if f.prepass >= f.engine[graphLegacy] {
			t.Fatalf("prepass node at %d is not before the scene at %d", f.prepass, f.engine[graphLegacy])
		}
		step := f.plan.Steps[f.prepass]
		if step.RenderPass == nil {
			t.Fatal("prepass node compiled to no rendering instance")
		}
		d := step.RenderPass
		if len(d.Color) != 0 {
			t.Errorf("prepass has %d colour attachments, want none", len(d.Color))
		}
		if d.Depth < 0 {
			t.Fatal("prepass has no depth attachment")
		}
		a := d.Attachments[d.Depth]
		if a.LoadOp != core1_0.AttachmentLoadOpClear {
			t.Errorf("depth load op = %v, want Clear: the prepass owns the clear the scene pass gave up", a.LoadOp)
		}
		if got := d.Clears[d.Depth].Depth; got != 0 {
			t.Errorf("depth clear = %v, want 0: reverse-Z clears to the far plane", got)
		}
		if a.StoreOp != core1_0.AttachmentStoreOpStore {
			t.Errorf("depth store op = %v, want Store: a discarded prepass is a prepass that did nothing", a.StoreOp)
		}
		if d.Samples != samples {
			t.Errorf("prepass samples = %v, want the scene's %v", d.Samples, samples)
		}
		if len(step.Barriers) == 0 {
			t.Error("no entry barrier: nothing orders the prepass's depth write against the frame before it")
		}
		// The tail still restores every layout by itself, which is what
		// TestFrameGraphTailLayouts says about the graph without this node.
		if len(f.plan.FinalBarriers) != 0 {
			t.Errorf("%d final barriers: the prepass left a layout the tail has to clean up", len(f.plan.FinalBarriers))
		}
		// The timer edges, both of them, so the pass has a number of its own
		// beside PassOpaque.
		if n := f.nodes[f.prepass]; n.begin != PassDepthPrepass || n.end != PassDepthPrepass {
			t.Errorf("timer bracket = %v..%v, want PassDepthPrepass on both edges", n.begin, n.end)
		}
	}
}

// With an application pass reading scene depth, depth's resting layout is the
// sampled one, and a graphics attachment is otherwise restored to resting on the
// way out. Here that restore would transition depth out of the attachment layout
// and the scene pass would transition it straight back -- two barriers that
// achieve nothing, the second of them a write-after-write against the prepass's
// own depth writes with nothing in its source scope that covers them. The
// FinalLayout on the declaration is what removes it.
func TestDepthPrepassLeavesDepthAnAttachment(t *testing.T) {
	r := &Renderer{msaaSamples: core1_0.Samples4,
		depth: &depthResources{format: core1_0.FormatD32SignedFloat},
		sc:    &swapchainDetails{imageFormat: core1_0.FormatB8G8R8A8SRGB, imageViews: make([]core1_0.ImageView, 1), extent: core1_0.Extent2D{Width: 640, Height: 360}}}
	r.depthResolve = &sceneDepthResources{r: r}
	f, err := newFrameGraph(r.msaaSamples, r.depth.format, r.sc.imageFormat, 1, true, r)
	if err != nil {
		t.Fatal(err)
	}
	if got := f.plan.Resources[f.depth].Resting; got != core1_0.ImageLayoutShaderReadOnlyOptimal {
		t.Fatalf("depth rests in %v; this test only says something when it rests in the sampled layout", got)
	}
	if got := f.plan.Steps[f.prepass].AfterBarriers; len(got) != 0 {
		t.Errorf("prepass emits %d exit barriers, want none: it leaves depth an attachment for the scene pass", len(got))
	}
}

// The scene pass has to LOAD depth once the prepass owns the clear, and its entry
// barrier has to name the layout the prepass left. Clearing here instead is the
// quietest way to get this wrong: the frame renders, validation is silent, and
// every equal-compare draw tests against a cleared buffer and vanishes.
func TestDepthPrepassSceneTargetLoadsDepth(t *testing.T) {
	build := func(mode DepthPrepassMode) *renderingTarget {
		h := &fakeHandles{}
		r := &Renderer{
			sc:               &swapchainDetails{extent: core1_0.Extent2D{Width: 64, Height: 32}},
			hdr:              &hdrTarget{images: []core1_0.Image{h.image()}, views: []core1_0.ImageView{h.imageView()}},
			depth:            &depthResources{format: core1_0.FormatD32SignedFloat, images: []core1_0.Image{h.image()}, views: []core1_0.ImageView{h.imageView()}},
			depthPrepassMode: mode,
		}
		r.bindSceneTargets()
		return r.sceneTargets[0]
	}
	off, on := build(DepthPrepassOff), build(DepthPrepassOn)
	if got := off.info.DepthAttachment.LoadOp; got != core1_0.AttachmentLoadOpClear {
		t.Errorf("without the prepass the scene depth load op = %v, want Clear", got)
	}
	if got := on.info.DepthAttachment.LoadOp; got != core1_0.AttachmentLoadOpLoad {
		t.Errorf("with the prepass the scene depth load op = %v, want Load", got)
	}
	// The depth barrier is the second of the two transition() pushed (colour
	// first, then depth), and its old layout is what says which pass owns the
	// clear.
	if got := on.before[len(on.before)-1].OldLayout; got != core1_0.ImageLayoutDepthStencilAttachmentOptimal {
		t.Errorf("with the prepass the scene's depth barrier comes from %v, want DepthStencilAttachmentOptimal", got)
	}
	if got := off.before[len(off.before)-1].OldLayout; got != core1_0.ImageLayoutUndefined {
		t.Errorf("without the prepass the scene's depth barrier comes from %v, want Undefined", got)
	}
}

// prepassCase is one draw and whether the prepass may write its depth.
type prepassCase struct {
	name string
	d    RenderObject
	want bool
}

// The exclusions, named one at a time. Every false below is a draw whose depth
// the main pass must keep testing with Greater, and getting any of them wrong is
// either a surface that disappears (included here and discarded there) or a
// wasted second submission (written here and not re-tested there).
func TestDepthPrepassQualifies(t *testing.T) {
	mesh := &Mesh{IndexCount: 36, VertexCount: 24}
	plain := &InstanceSet{Mesh: mesh, count: 8}
	lodSet := &InstanceSet{Mesh: mesh, count: 8, lod: &InstanceSetLOD{}}
	cases := []prepassCase{
		{"plain lit", RenderObject{Mesh: mesh}, true},
		{"emissive", RenderObject{Mesh: mesh, Emissive: true}, true},
		{"material", RenderObject{Mesh: mesh, Material: &Material{}}, true},
		{"fully opaque alpha", RenderObject{Mesh: mesh, Alpha: 1}, true},
		{"instance set", RenderObject{Mesh: mesh, Instances: plain}, true},

		{"no mesh", RenderObject{}, false},
		{"shadow only", RenderObject{Mesh: mesh, ShadowOnly: true}, false},
		{"skinned", RenderObject{Mesh: mesh, Joints: &JointBuffer{}}, false},
		{"skinned material", RenderObject{Mesh: mesh, Joints: &JointBuffer{}, Material: &Material{}}, false},
		{"double sided", RenderObject{Mesh: mesh, DoubleSided: true}, false},
		{"terrain", RenderObject{Mesh: mesh, TerrainMat: &TerrainMaterial{}}, false},
		{"water", RenderObject{Mesh: mesh, Water: &WaterParams{}}, false},
		{"translucent", RenderObject{Mesh: mesh, Alpha: 0.5}, false},
		{"alpha-tested LOD set", RenderObject{Mesh: mesh, InstancesLOD: &InstanceSetLOD{}}, false},
		{"expanded LOD bucket", RenderObject{Mesh: mesh, Instances: lodSet}, false},
		{"double-sided instance set", RenderObject{Mesh: mesh, Instances: plain, DoubleSided: true}, false},
		{"empty instance set", RenderObject{Mesh: mesh, Instances: &InstanceSet{Mesh: mesh}}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := depthPrepassQualifies(&c.d); got != c.want {
				t.Errorf("depthPrepassQualifies = %v, want %v", got, c.want)
			}
		})
	}
}

// prepassDrawList is a hand-counted scene: four draws the prepass must write and
// five it must not. Hand-counted rather than derived from the predicate, so the
// expected number below is independent of the thing being tested.
func prepassDrawList(h *fakeHandles) ([]RenderObject, int) {
	mesh := fakeMesh(h, 24, 36, 1)
	tex := fakeTexture(h)
	mat := fakeMaterial(h)
	m := identityMat(1)
	set := fakeInstanceSet(h, mesh, 12, 2)
	lod := fakeInstanceSet(h, mesh, 12, 2)
	lod.lod = &InstanceSetLOD{}
	return []RenderObject{
		{Mesh: mesh, Texture: tex, MVP: m, Model: m},                             // in
		{Mesh: mesh, Material: mat, MVP: m, Model: m},                            // in
		{Mesh: mesh, Texture: tex, Emissive: true, MVP: m, Model: m},             // in
		{Mesh: mesh, Texture: tex, Instances: set, MVP: m, Model: m},             // in
		{Mesh: mesh, Texture: tex, DoubleSided: true, MVP: m, Model: m},          // out
		{Mesh: mesh, Texture: tex, Joints: fakeJointBuffer(h), MVP: m, Model: m}, // out
		{Mesh: mesh, Texture: tex, Alpha: 0.5, MVP: m, Model: m},                 // out
		{Mesh: mesh, TerrainMat: fakeTerrainMaterial(h), MVP: m, Model: m},       // out
		{Mesh: mesh, Texture: tex, Instances: lod, MVP: m, Model: m},             // out
	}, 4
}

// recordDepthPrepass over that list has to submit exactly the four, and the
// recorder has to charge them to PrepassDraws as well as to DrawCalls.
//
// Verified to fail, which is what makes it a check rather than decoration:
// deleting `case d.InstancesLOD != nil: return false` from depthPrepassQualifies
// and the `s.lod == nil` from its instance-set clause reports `prepass submitted
// 5 draws, want 4` here and two failures in TestDepthPrepassQualifies.
func TestDepthPrepassRecordsOnlyQualifyingDraws(t *testing.T) {
	h := &fakeHandles{}
	draws, want := prepassDrawList(h)
	var stats RenderStats
	var scratch commandScratch
	d := &prepassRecorder{}
	c := &graphFrame{driver: d, cmd: h.commandBuffer(), extent: core1_0.Extent2D{Width: 64, Height: 32},
		scratch: &scratch, stats: &stats, draws: draws,
		prepass: depthPrepassPipelines{depth: h.pipeline(), depthInstanced: h.pipeline(), layout: h.layout(), mode: DepthPrepassOn, active: true}}
	recordDepthPrepass(c)

	if stats.PrepassDraws != want {
		t.Errorf("prepass submitted %d draws, want %d", stats.PrepassDraws, want)
	}
	if stats.DrawCalls != want {
		t.Errorf("DrawCalls = %d, want %d: the prepass's draws are submitted work like the cascades' are", stats.DrawCalls, want)
	}
	// One bind of each prepass pipeline and no more: three individual meshes
	// then one set, which is why they are recorded in two runs rather than
	// interleaved.
	if got := len(d.bound); got != 2 {
		t.Errorf("%d pipeline binds, want 2 (static then instanced)", got)
	}
	if d.first(c.prepass.depth) != 0 || d.first(c.prepass.depthInstanced) != 1 {
		t.Errorf("pipeline order %v: want the static pipeline first and the instanced one after", d.bound)
	}
}

// The control that `task prepass` compares against has to be a real difference:
// it keeps the node and the clear and submits nothing, so every prepassed surface
// fails the EQUAL test. If it submitted the draws anyway, the gate's control pair
// would compare equal and the gate would prove nothing.
func TestDepthPrepassDebugEmptySubmitsNothing(t *testing.T) {
	h := &fakeHandles{}
	draws, want := prepassDrawList(h)
	if want == 0 {
		t.Fatal("the fixture list has nothing to withhold")
	}
	var stats RenderStats
	var scratch commandScratch
	d := &prepassRecorder{}
	c := &graphFrame{driver: d, cmd: h.commandBuffer(), extent: core1_0.Extent2D{Width: 64, Height: 32},
		scratch: &scratch, stats: &stats, draws: draws,
		prepass: depthPrepassPipelines{depth: h.pipeline(), depthInstanced: h.pipeline(), layout: h.layout(), mode: DepthPrepassOn, active: true, debug: DepthPrepassDebugEmpty}}
	recordDepthPrepass(c)
	if stats.PrepassDraws != 0 || len(d.bound) != 0 {
		t.Errorf("the empty mode submitted %d draws and %d binds, want none", stats.PrepassDraws, len(d.bound))
	}
}

// The whole frame, through the real recorder: the prepass pipelines are bound
// before anything in the scene pass, and the qualifying draws in the scene pass
// are recorded against the equal-compare twins rather than the Greater ones.
//
// Verified to fail: deleting the `case equal:` and `case material && equal:` arms
// from the opaque loop's pipeline switch reports `the lit equal pipeline was
// built and handed to the recorder and never bound` and the same for the material
// one. Nothing else in the repository would -- the frame still renders,
// the stream still hashes to something, and the pixels are identical, because a
// complete prepass makes Greater and EQUAL agree. It only stops being identical
// on a scene where the prepass is incomplete, which is every real one.
func TestDepthPrepassBindsTheEqualCompareVariants(t *testing.T) {
	fx := withDepthPrepass(buildFrame(97))
	d := &prepassRecorder{}
	if err := fx.record(d, 1); err != nil {
		t.Fatalf("record: %v", err)
	}
	p := fx.prepass
	for name, pipe := range map[string]core1_0.Pipeline{
		"depth":           p.depth,
		"depth instanced": p.depthInstanced,
		"lit equal":       p.lit,
		"material equal":  p.material,
		"instanced equal": p.instanced,
	} {
		if d.first(pipe) < 0 {
			t.Errorf("the %s pipeline was built and handed to the recorder and never bound", name)
		}
	}
	// Order, not just presence: depth has to be written before anything tests
	// EQUAL against it.
	for name, pipe := range map[string]core1_0.Pipeline{"lit equal": p.lit, "material equal": p.material, "instanced equal": p.instanced} {
		if first, after := d.first(p.depth), d.first(pipe); first >= 0 && after >= 0 && after < first {
			t.Errorf("the %s pipeline is bound at %d, before the prepass at %d", name, after, first)
		}
	}
	if fx.stats.PrepassDraws == 0 {
		t.Error("the prepass recorded no draws over a fixture full of qualifying ones")
	}
	t.Logf("prepass on: %d driver calls, %d prepass draws of %d, hash %#x",
		d.calls, fx.stats.PrepassDraws, fx.stats.DrawCalls, uint64(d.h))
}

// Allocations per frame, with the option on, at two draw counts. The recorder is
// allocation-free and the prepass is part of it; a per-draw allocation in the new
// loop would be invisible in every other check here.
func TestDepthPrepassAllocsAreConstant(t *testing.T) {
	small := withDepthPrepass(buildFrame(40))
	big := withDepthPrepass(buildFrame(160))
	warm := &fakeDriver{}
	for _, fx := range []*frame{small, big} {
		if err := fx.record(warm, 0); err != nil {
			t.Fatalf("warm-up record: %v", err)
		}
	}
	d := &fakeDriver{}
	allocs := func(fx *frame) float64 {
		return testing.AllocsPerRun(20, func() {
			if err := fx.record(d, 0); err != nil {
				t.Fatal(err)
			}
		})
	}
	a, b := allocs(small), allocs(big)
	t.Logf("allocs/op with the prepass on: %v at %d draws, %v at %d", a, len(small.draws), b, len(big.draws))
	if a != 0 || b != 0 {
		t.Errorf("the prepass allocates: %v allocs/op at %d draws, %v at %d", a, len(small.draws), b, len(big.draws))
	}
}

// pipelineStateDriver captures the GraphicsPipelineCreateInfo that reaches the
// driver, which is the only place the depth state a pipeline was actually built
// with can be read without a GPU. Asserting against the constructor's own return
// value would be comparing a constant to itself.
type pipelineStateDriver struct {
	*resizeFakeDriver
	infos []core1_0.GraphicsPipelineCreateInfo
}

func (d *pipelineStateDriver) CreateGraphicsPipelines(cache *core1_0.PipelineCache, cb *loader.AllocationCallbacks, infos ...core1_0.GraphicsPipelineCreateInfo) ([]core1_0.Pipeline, common.VkResult, error) {
	d.infos = append(d.infos, infos...)
	return d.resizeFakeDriver.CreateGraphicsPipelines(cache, cb, infos...)
}

// The five pipelines, as the driver receives them. Each clause is a silent
// failure if it is wrong:
//
//   - a colour attachment on the prepass makes it a second shading pass;
//   - Greater and a depth write are what put the values in the buffer, and a
//     depth BIAS -- which the shadow pipeline this is modelled on does have --
//     would make every one of them miss the EQUAL test afterwards;
//   - EQUAL and no depth write are the whole point of the main-pass twins;
//   - the sample counts have to agree, or the two passes cannot share depth.
//
// Verified to fail: changing depthEqualState's compare op to
// CompareOpGreaterOrEqual reports, for each of the three twins,
// `depth compare = Greater Than Or Equal, want Equal`.
func TestDepthPrepassPipelineStateReachesTheDriver(t *testing.T) {
	const samples = core1_0.Samples4
	base := newResizeFakeDriver()
	d := &pipelineStateDriver{resizeFakeDriver: base}
	sh := DefaultShaders()
	formats := colorDepthFormats(hdrFormat, core1_0.FormatD32SignedFloat)
	extent := core1_0.Extent2D{Width: 640, Height: 360}
	layout := base.h.layout()
	set0, shadowSet := base.h.descriptorSetLayout(), base.h.descriptorSetLayout()

	type want struct {
		name     string
		colour   bool
		compare  core1_0.CompareOp
		write    bool
		build    func() error
		infoAt   int
		biasFree bool
	}
	var checks []want
	add := func(name string, colour bool, compare core1_0.CompareOp, write, biasFree bool, build func() error) {
		checks = append(checks, want{name: name, colour: colour, compare: compare, write: write, build: build, infoAt: len(checks), biasFree: biasFree})
	}
	add("prepass static", false, core1_0.CompareOpGreater, true, true, func() error {
		_, err := createDepthPrepassPipeline(d, sh, sh.PrepassVert, "t", depthOnlyFormats(core1_0.FormatD32SignedFloat), layout, extent, samples,
			[]core1_0.VertexInputBindingDescription{vertexBindingDescription()}, vertexAttributeDescriptions())
		return err
	})
	add("prepass instanced", false, core1_0.CompareOpGreater, true, true, func() error {
		_, err := createDepthPrepassPipeline(d, sh, sh.PrepassInstancedVert, "t", depthOnlyFormats(core1_0.FormatD32SignedFloat), layout, extent, samples,
			[]core1_0.VertexInputBindingDescription{vertexBindingDescription(), instanceBindingDescription()}, instanceAttributeDescriptions())
		return err
	})
	add("lit equal", true, core1_0.CompareOpEqual, false, true, func() error {
		_, _, err := createDepthEqualPipeline(d, sh, formats, extent, set0, shadowSet, samples)
		return err
	})
	add("material equal", true, core1_0.CompareOpEqual, false, true, func() error {
		_, _, err := createDepthEqualMaterialPipeline(d, sh, formats, extent, set0, shadowSet, samples)
		return err
	})
	add("instanced equal", true, core1_0.CompareOpEqual, false, true, func() error {
		_, _, err := createDepthEqualInstancedPipeline(d, sh, formats, extent, set0, shadowSet, samples)
		return err
	})

	for _, c := range checks {
		if err := c.build(); err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
	}
	if len(d.infos) != len(checks) {
		t.Fatalf("%d pipeline create infos, want %d", len(d.infos), len(checks))
	}
	for _, c := range checks {
		info := d.infos[c.infoAt]
		t.Run(c.name, func(t *testing.T) {
			f, ok := info.NextOptions.Next.(renderingFormats)
			if !ok {
				t.Fatal("no dynamic rendering formats")
			}
			if got := len(f.ColorAttachmentFormats) > 0; got != c.colour {
				t.Errorf("has colour attachment = %v, want %v", got, c.colour)
			}
			if f.DepthAttachmentFormat != core1_0.FormatD32SignedFloat {
				t.Errorf("depth format = %v, want D32_SFLOAT", f.DepthAttachmentFormat)
			}
			ds := info.DepthStencilState
			if ds == nil {
				t.Fatal("no depth state")
			}
			if ds.DepthCompareOp != c.compare {
				t.Errorf("depth compare = %v, want %v", ds.DepthCompareOp, c.compare)
			}
			if ds.DepthWriteEnable != c.write {
				t.Errorf("depth write = %v, want %v", ds.DepthWriteEnable, c.write)
			}
			if !ds.DepthTestEnable {
				t.Error("depth test disabled")
			}
			if ms := info.MultisampleState; ms == nil || ms.RasterizationSamples != samples {
				t.Errorf("rasterization samples = %v, want %v", ms, samples)
			}
			if rs := info.RasterizationState; c.biasFree && rs != nil && rs.DepthBiasEnable {
				t.Error("depth bias enabled: a biased prepass misses its own EQUAL test")
			}
		})
	}
}
