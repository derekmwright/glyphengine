package renderer

import (
	"errors"
	"strings"
	"testing"

	"github.com/derekmwright/glyphengine/renderer/framegraph"
	"github.com/vkngwrapper/core/v3/common"
	"github.com/vkngwrapper/core/v3/core1_0"
	"github.com/vkngwrapper/core/v3/loader"
	"github.com/vkngwrapper/extensions/v3/khr_dynamic_rendering"
)

// The ABI nine shaders and every application compute dispatch share. Bindings 0
// and 1 must name Compute: without it the validation layer rejects any compute
// pipeline whose shader declares them, with
// VUID-VkComputePipelineCreateInfo-layout-07988, and nothing else notices.
//
// Verified break: dropping core1_0.StageCompute from binding 1 reports
// "binding 1 stage flags: got 16 (Fragment); want 48 (Fragment|Compute)";
// dropping it from binding 0 reports the same for binding 0.
func TestShadowSetBindingStages(t *testing.T) {
	const graphics = core1_0.StageVertex | core1_0.StageFragment
	want := map[int]core1_0.ShaderStageFlags{
		0:  graphics | core1_0.StageCompute,
		1:  core1_0.StageFragment | core1_0.StageCompute,
		2:  core1_0.StageFragment,
		3:  core1_0.StageFragment,
		4:  core1_0.StageFragment,
		5:  core1_0.StageFragment,
		6:  graphics | core1_0.StageCompute,
		7:  graphics | core1_0.StageCompute,
		8:  graphics | core1_0.StageCompute,
		9:  graphics | core1_0.StageCompute,
		10: graphics | core1_0.StageCompute,
	}
	kinds := map[int]core1_0.DescriptorType{
		0: core1_0.DescriptorTypeUniformBuffer, 1: core1_0.DescriptorTypeCombinedImageSampler,
		2: core1_0.DescriptorTypeCombinedImageSampler, 3: core1_0.DescriptorTypeStorageBuffer,
		4: core1_0.DescriptorTypeStorageBuffer, 5: core1_0.DescriptorTypeStorageBuffer,
		6: core1_0.DescriptorTypeUniformBuffer, 7: core1_0.DescriptorTypeCombinedImageSampler,
		8: core1_0.DescriptorTypeCombinedImageSampler, 9: core1_0.DescriptorTypeCombinedImageSampler,
		10: core1_0.DescriptorTypeCombinedImageSampler,
	}
	bindings := shadowSetBindings()
	if len(bindings) != len(want) {
		t.Fatalf("bindings: got %d; want %d", len(bindings), len(want))
	}
	for i, b := range bindings {
		if b.Binding != i {
			t.Fatalf("binding %d is declared at index %d", b.Binding, i)
		}
		if b.StageFlags != want[i] {
			t.Fatalf("binding %d stage flags: got %d; want %d", i, b.StageFlags, want[i])
		}
		if b.DescriptorType != kinds[i] || b.DescriptorCount != 1 {
			t.Fatalf("binding %d: %v x%d", i, b.DescriptorType, b.DescriptorCount)
		}
	}
	t.Log("set 1 bindings 0 and 1 are visible to compute; 2-5 stay graphics-only")
}

// computeLayoutProbe records the descriptor set layouts a compute pipeline is
// built against, so the light set's presence at set 1 is a measured fact rather
// than a reading of createAppComputePipeline's argument list.
type computeLayoutProbe struct {
	*resizeFakeDriver
	sets [][]core1_0.DescriptorSetLayout
}

func (d *computeLayoutProbe) CreatePipelineLayout(cb *loader.AllocationCallbacks, o core1_0.PipelineLayoutCreateInfo) (core1_0.PipelineLayout, common.VkResult, error) {
	d.sets = append(d.sets, o.SetLayouts)
	return d.resizeFakeDriver.CreatePipelineLayout(cb, o)
}

func TestAppComputeBindsTheLightSet(t *testing.T) {
	d := &computeLayoutProbe{resizeFakeDriver: newResizeFakeDriver()}
	r := newResizeFixture(d.resizeFakeDriver, 3)
	r.deviceDriver = d
	r.depth = &depthResources{format: core1_0.FormatD32SignedFloat}
	h := &fakeHandles{next: 50000}
	r.shadow.descriptorSetLayout = h.descriptorSetLayout()
	target, err := r.CreateRenderTarget(RenderTargetDesc{Name: "scatter", Format: TargetR32F, Scale: 0.5, Storage: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = r.CreateAppCompute(AppComputeDesc{Name: "air scatter", Stage: StageBeforeScene,
		Comp: computeCode(t), Writes: []*RenderTarget{target}, ReadsShadows: true}); err != nil {
		t.Fatal(err)
	}
	if len(d.sets) != 1 {
		t.Fatalf("pipeline layouts created: %d", len(d.sets))
	}
	if got := d.sets[0]; len(got) != 3 || got[1] != r.shadow.descriptorSetLayout || got[2] != r.computeSetLayout {
		t.Fatalf("compute set layouts: %v", got)
	}
	r.destroyAppResources()
	t.Log("a compute dispatch binds the shared light set at set 1 and its own inputs at set 2")
}

func shadowComputeFixture(t *testing.T, declares bool) (*frame, int, framegraph.ResourceID) {
	t.Helper()
	fx := withAppFrame(buildFrame(7), true, true, declares)
	dispatch := -1
	for i, n := range fx.graph.nodes {
		if n.app != nil && n.app.compute != nil {
			dispatch = i
		}
	}
	if dispatch < 0 {
		t.Fatal("fixture has no dispatch")
	}
	return fx, dispatch, fx.graph.sunShadow
}

// The declared edge, end to end through the renderer's own declarations: the
// cascade node sits at the shadow boundary, the dispatch declares the read, and
// the compiler derives the depth-write-to-compute-read barrier between them.
//
// Verified breaks (2026-10-02):
//   - removing the ReadsShadows arm of appendComputeGraph: "dispatch declares
//     no cascade read".
//   - appending the cascade declaration after the application stages instead
//     (moving the appendNode call below the decl loop in extendAppGraph):
//     withAppFrame panics out of newFrameGraph with `framegraph: node "fixture
//     compute" resource "sun shadow maps": read before "sun shadow cascades"
//     rewrites this frame's contents`, naming the dispatch.
func TestAppComputeShadowEdge(t *testing.T) {
	fx, dispatch, sun := shadowComputeFixture(t, true)
	f := fx.graph
	if f.shadowCascades != f.beforeShadows {
		t.Fatalf("cascade declaration at %d; shadow boundary at %d", f.shadowCascades, f.beforeShadows)
	}
	if f.declarations[f.shadowCascades].Name != shadowCascadeNode || f.shadowCascades >= dispatch {
		t.Fatalf("cascade declaration %q at %d does not precede the dispatch at %d",
			f.declarations[f.shadowCascades].Name, f.shadowCascades, dispatch)
	}
	declared := false
	for _, u := range f.declarations[dispatch].Uses {
		declared = declared || (u.Resource == sun && u.Access == framegraph.DepthSampledRead)
	}
	if !declared {
		t.Fatal("dispatch declares no cascade read")
	}
	var edge *framegraph.Barrier
	for i, b := range f.plan.Steps[dispatch].Barriers {
		if b.Resource == sun {
			edge = &f.plan.Steps[dispatch].Barriers[i]
		}
	}
	if edge == nil {
		t.Fatal("no derived barrier for the cascade map")
	}
	const depthStages = core1_0.PipelineStageEarlyFragmentTests | core1_0.PipelineStageLateFragmentTests
	if edge.OldLayout != core1_0.ImageLayoutDepthStencilReadOnlyOptimal || edge.NewLayout != edge.OldLayout ||
		edge.SrcStage != depthStages || edge.SrcAccess != core1_0.AccessDepthStencilAttachmentWrite ||
		edge.DstStage != core1_0.PipelineStageComputeShader || edge.DstAccess != core1_0.AccessShaderRead {
		t.Fatalf("cascade edge: %+v", *edge)
	}
	// Every cascade of the waited frame's own copy, not layer zero of image zero.
	if got := f.plan.Resources[sun].Desc.Layers; got != ShadowCascades {
		t.Fatalf("cascade layers: got %d; want %d", got, ShadowCascades)
	}
	if !f.images[sun].frameInstance && len(f.images[sun].images) > 1 {
		t.Fatal("cascade barrier would name a fixed frame's image")
	}
	t.Logf("cascade declaration at step %d, dispatch at %d, edge %v stages %v -> %v",
		f.shadowCascades, dispatch, edge.OldLayout, edge.SrcStage, edge.DstStage)

	// A dispatch that does not declare the read gets no edge at all.
	plain, step, id := shadowComputeFixture(t, false)
	for _, b := range plain.graph.plan.Steps[step].Barriers {
		if b.Resource == id {
			t.Fatal("undeclared dispatch still derived a cascade barrier")
		}
	}
	for _, u := range plain.graph.declarations[step].Uses {
		if u.Resource == id {
			t.Fatal("undeclared dispatch still declared the cascade map")
		}
	}
}

// Moving the declaration past the readers is the ordering mistake the Rewrites
// flag refuses, checked against the renderer's real declarations rather than a
// hand-built graph: the error has to name the dispatch a consumer created.
func TestShadowReadBeforeCascadesIsACompileError(t *testing.T) {
	fx, dispatch, _ := shadowComputeFixture(t, true)
	f := fx.graph
	reordered := framegraph.New()
	for _, d := range f.plan.Resources {
		if d.Buffer {
			reordered.AddBuffer(d.BufferDesc)
			continue
		}
		reordered.AddImage(d.Desc)
	}
	moved := f.declarations[f.shadowCascades]
	for i, n := range f.declarations {
		if i != f.shadowCascades {
			reordered.AddNode(n)
		}
	}
	reordered.AddNode(moved)
	_, err := reordered.Build()
	if err == nil {
		t.Fatal("a dispatch ahead of the cascade declaration compiled")
	}
	for _, text := range []string{f.declarations[dispatch].Name, "sun shadow maps", shadowCascadeNode} {
		if !strings.Contains(err.Error(), text) {
			t.Fatalf("error %q does not name %q", err, text)
		}
	}
	t.Logf("declaration moved past the readers: %v", err)
}

// cascadeProbe counts the sun cascade passes a recorded frame actually enters,
// and what they clear to.
type cascadeProbe struct {
	*fakeDriver
	cascade           core1_0.Image
	cascades, cleared int
	layouts           map[core1_0.ImageLayout]int
}

func (d *cascadeProbe) CmdBeginRendering(cb core1_0.CommandBuffer, o khr_dynamic_rendering.RenderingInfo) error {
	if o.DepthAttachment != nil && o.RenderArea.Extent.Width == ShadowMapSize && len(o.ColorAttachments) == 0 {
		d.cascades++
		if o.DepthAttachment.LoadOp == core1_0.AttachmentLoadOpClear {
			if v, ok := o.DepthAttachment.ClearValue.(core1_0.ClearValueDepthStencil); ok && v.Depth == 1 {
				d.cleared++
			}
		}
	}
	return d.fakeDriver.CmdBeginRendering(cb, o)
}

func (d *cascadeProbe) CmdPipelineBarrier(cb core1_0.CommandBuffer, src, dst core1_0.PipelineStageFlags, deps core1_0.DependencyFlags, mem []core1_0.MemoryBarrier, buf []core1_0.BufferMemoryBarrier, img []core1_0.ImageMemoryBarrier) error {
	for _, b := range img {
		if b.Image == d.cascade && dst&core1_0.PipelineStageComputeShader != 0 {
			d.layouts[b.NewLayout]++
		}
	}
	return d.fakeDriver.CmdPipelineBarrier(cb, src, dst, deps, mem, buf, img)
}

// Shadows off needs no dummy image and no second code path. Every cascade pass
// still runs and still clears its layer to depth 1.0, and the comparison
// sampler's CompareOpLessOrEqual then answers 1.0 -- fully lit -- for any
// reference a shader can produce. That is the documented unshadowed value, and
// it is the same for the fragment and the compute path because they sample the
// same descriptor.
//
// Verified break (2026-10-02): guarding the cascade `begin`/`end` pair with
// `if lighting.ShadowEnabled` -- the shape that would leave the layers
// unwritten -- reports "shadows off: 0 of 2 cascade passes entered, 0 cleared
// to 1.0" and leaves the compute edge pointing at an unwritten image.
func TestDisabledShadowsStillClearTheCascadesForCompute(t *testing.T) {
	for _, enabled := range []bool{true, false} {
		fx, _, sun := shadowComputeFixture(t, true)
		fx.lighting.ShadowEnabled = enabled
		d := &cascadeProbe{fakeDriver: &fakeDriver{}, layouts: map[core1_0.ImageLayout]int{},
			cascade: fx.graph.images[sun].images[0]}
		if err := fx.record(d, 1); err != nil {
			t.Fatal(err)
		}
		if d.cascades != ShadowCascades || d.cleared != ShadowCascades {
			t.Fatalf("shadows=%v: %d of %d cascade passes entered, %d cleared to 1.0",
				enabled, d.cascades, ShadowCascades, d.cleared)
		}
		// The cascade passes leave the map in DepthStencilReadOnlyOptimal and the
		// derived edge keeps it there, which is also the layout binding 1 names.
		if d.layouts[core1_0.ImageLayoutDepthStencilReadOnlyOptimal] == 0 || len(d.layouts) != 1 {
			t.Fatalf("shadows=%v: depth layouts exposed to compute: %v", enabled, d.layouts)
		}
		t.Logf("shadows=%v: %d cascade passes cleared to depth 1.0; compute reads them in %v",
			enabled, d.cleared, core1_0.ImageLayoutDepthStencilReadOnlyOptimal)
	}
}

// The cascade descriptors are not swapchain-sized, so a rebuild must leave
// bindings 0 and 1 alone -- and the graph's own binding has to be repointed at
// the same live images, because that is what a derived barrier resolves through.
func TestShadowBindingsSurviveRebuild(t *testing.T) {
	d := newResizeFakeDriver()
	r := newResizeFixture(d, 3)
	r.depth = &depthResources{format: core1_0.FormatD32SignedFloat}
	h := &fakeHandles{next: 60000}
	for i := range maxFramesInFlight {
		r.shadow.images[i] = h.image()
		r.shadow.arrayViews[i] = h.imageView()
		r.shadow.cubeImages[i] = h.image()
		r.shadow.cubeSamplerViews[i] = h.imageView()
	}
	before := [maxFramesInFlight]core1_0.Image(r.shadow.images)
	r.bindAppGraphImages()
	bound := r.frameGraph.images[r.frameGraph.sunShadow]
	if len(bound.images) != maxFramesInFlight || !bound.frameInstance {
		t.Fatalf("cascade binding: %d images, frameInstance=%v", len(bound.images), bound.frameInstance)
	}
	r.releaseAppResizeTargets()
	undo := &rebuildUndo{}
	if err := r.rebuildAppTargets(undo); err != nil {
		t.Fatal(err)
	}
	if [maxFramesInFlight]core1_0.Image(r.shadow.images) != before {
		t.Fatal("rebuild replaced the cascade images")
	}
	r.bindAppGraphImages()
	for i, img := range r.frameGraph.images[r.frameGraph.sunShadow].images {
		if img != r.shadow.images[i] {
			t.Fatalf("cascade binding %d names %v, live image is %v", i, img, r.shadow.images[i])
		}
	}
	t.Log("a rebuild leaves the cascade images and their light-set descriptors in place")
}

// One declared read is one extra driver call: the derived cascade barrier, in
// its own stage-pair group ahead of the dispatch's storage transitions. The
// undeclared stream is TestAppComputeStreams' 3390 calls and hash, unchanged --
// which is the evidence that declaring nothing costs nothing.
//
// Verified: removing the ReadsShadows arm of appendComputeGraph reports 3390
// calls and hash 0x42a9ec47c9236b15 here, which is the undeclared stream.
const goldenShadowComputeStreamHash Hasher = 0x5bd0031fc3536381
const goldenShadowComputeCalls = 3391

func TestAppComputeShadowStream(t *testing.T) {
	plain := &fakeDriver{hashing: true}
	if err := withAppFrame(buildFrame(97), true, true, false).record(plain, 1); err != nil {
		t.Fatal(err)
	}
	d := &fakeDriver{hashing: true}
	if err := withAppFrame(buildFrame(97), true, true, true).record(d, 1); err != nil {
		t.Fatal(err)
	}
	t.Logf("undeclared: %d calls, hash %#x; declaring shadows: %d calls, hash %#x",
		plain.calls, plain.h, d.calls, d.h)
	if plain.calls != goldenAppComputeCalls || plain.h != goldenAppComputeStreamHash {
		t.Errorf("undeclared stream moved: %d calls, hash %#x", plain.calls, plain.h)
	}
	if d.calls != goldenShadowComputeCalls || d.h != goldenShadowComputeStreamHash {
		t.Errorf("hash %#x want %#x; calls %d want %d", d.h, goldenShadowComputeStreamHash, d.calls, goldenShadowComputeCalls)
	}
	if d.calls-plain.calls != 1 {
		t.Errorf("declaring the cascade read added %d calls, not one barrier", d.calls-plain.calls)
	}
}

// No new Vulkan object is created for a declared read -- it is a graph
// declaration, not a resource -- so the existing creation sites are the only
// ones. This checks the flag travels the same unwinding path: a failed compute
// pipeline leaves nothing registered and nothing leaked.
func TestAppComputeShadowCreationUnwinds(t *testing.T) {
	d := newResizeFakeDriver()
	d.failCall, d.failAt = "CreateComputePipelines", 1
	r := newResizeFixture(d, 3)
	r.depth = &depthResources{format: core1_0.FormatD32SignedFloat}
	target, err := r.CreateRenderTarget(RenderTargetDesc{Name: "scatter", Format: TargetR32F, Scale: 0.5, Storage: true})
	if err != nil {
		t.Fatal(err)
	}
	_, err = r.CreateAppCompute(AppComputeDesc{Name: "air scatter", Stage: StageBeforeScene,
		Comp: computeCode(t), Writes: []*RenderTarget{target}, ReadsShadows: true})
	if !errors.Is(err, errInjected) || !strings.Contains(err.Error(), "air scatter") {
		t.Fatalf("error: %v", err)
	}
	if len(r.appPasses) != 0 {
		t.Fatal("failed compute remained registered")
	}
	r.destroyAppResources()
	assertBalanced(t, d)
}

// The declared edge is one more barrier through retained scratch, so recording
// it must still allocate nothing. sizeScratch widens the barrier arrays from the
// compiled plan; a group that outgrew them would allocate per frame instead.
func TestAppComputeShadowAllocs(t *testing.T) {
	for _, n := range []int{7, 97, 511} {
		fx := withAppFrame(buildFrame(n), true, true, true)
		d := &fakeDriver{}
		if err := fx.record(d, 1); err != nil {
			t.Fatal(err)
		}
		a := testing.AllocsPerRun(50, func() {
			if err := fx.record(d, 1); err != nil {
				panic(err)
			}
		})
		t.Logf("%d engine draws plus a shadow-reading dispatch: %.0f allocs/frame", n, a)
		if a != 0 {
			t.Errorf("%.0f allocations", a)
		}
	}
}
