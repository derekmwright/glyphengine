package renderer

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/derekmwright/glyphengine/shaders"
	"github.com/vkngwrapper/core/v3/common"
	"github.com/vkngwrapper/core/v3/core1_0"
	"github.com/vkngwrapper/core/v3/loader"
)

// paramsProbe records every descriptor write into a fixed array rather than
// appending to a slice, because TestAppPassParamsSlotDiscipline measures
// allocations through it: an append here would charge the frame path for the
// probe's own bookkeeping, which is how a zero-allocation check becomes noise.
type paramsProbe struct {
	*bufferTestDriver
	writes int
	recent [8]paramsWrite
	// watch narrows recording to one descriptor set. A frame's bindings also
	// rewrite the four global shader-texture slots on the light set, and
	// counting those as the pass's writes is how "exactly one extra write"
	// becomes a number that means nothing.
	watch core1_0.DescriptorSet
	// layoutSink, when set, receives every descriptor set layout the renderer
	// asks for. Reading the bindings back is the only way to assert what a
	// layout declares: the handle the driver returns says nothing about it.
	layoutSink func(core1_0.DescriptorSetLayoutCreateInfo)
}

func (d *paramsProbe) CreateDescriptorSetLayout(cb *loader.AllocationCallbacks, info core1_0.DescriptorSetLayoutCreateInfo) (core1_0.DescriptorSetLayout, common.VkResult, error) {
	if d.layoutSink != nil {
		d.layoutSink(info)
	}
	return d.bufferTestDriver.CreateDescriptorSetLayout(cb, info)
}

type paramsWrite struct {
	set     core1_0.DescriptorSet
	binding int
	kind    core1_0.DescriptorType
	buffer  core1_0.Buffer
	size    int
}

func (d *paramsProbe) UpdateDescriptorSets(writes []core1_0.WriteDescriptorSet, _ []core1_0.CopyDescriptorSet) error {
	for _, w := range writes {
		if d.watch.Handle() != 0 && w.DstSet.Handle() != d.watch.Handle() {
			continue
		}
		rec := paramsWrite{set: w.DstSet, binding: w.DstBinding, kind: w.DescriptorType}
		if len(w.BufferInfo) > 0 {
			rec.buffer, rec.size = w.BufferInfo[0].Buffer, w.BufferInfo[0].Range
		}
		d.recent[d.writes%len(d.recent)] = rec
		d.writes++
	}
	return nil
}

func paramsFixture(t *testing.T) (*Renderer, *paramsProbe, *RenderTarget) {
	t.Helper()
	d := &paramsProbe{bufferTestDriver: &bufferTestDriver{resizeFakeDriver: newResizeFakeDriver()}}
	r := newResizeFixture(d.resizeFakeDriver, 3)
	r.deviceDriver = d
	r.instanceDriver = bufferTestInstance{}
	r.depth = &depthResources{format: core1_0.FormatD32SignedFloat}
	r.fallbackTexture = &Texture{view: d.h.imageView(), sampler: d.h.sampler()}
	target, err := r.CreateRenderTarget(RenderTargetDesc{Name: "params target", Format: TargetR16F, Scale: 1})
	if err != nil {
		t.Fatal(err)
	}
	return r, d, target
}

func fullscreenDesc(name string, target *RenderTarget, params int) AppPassDesc {
	return AppPassDesc{Name: name, Stage: StageBeforeScene, Target: target, Fullscreen: true,
		Vert: shaders.DepthResolveVertSpv, Frag: shaders.DepthResolveFragSpv, Params: params}
}

// TestAppPassParamsDeclaration covers the half of the block that is about what
// is declared rather than what is written: the sizes a pass may ask for, that a
// pass asking for none allocates from the layout it always did and makes no
// buffers, and that a pass asking for one gets the uniform binding on a second
// layout with the four samplers still in front of it.
func TestAppPassParamsDeclaration(t *testing.T) {
	r, d, target := paramsFixture(t)
	for _, size := range []int{-16, 1, 15, 17, AppParamBytes + 16} {
		err := r.validateAppPass(fullscreenDesc("bad size", target, size))
		if err == nil || !strings.Contains(err.Error(), "Params") {
			t.Fatalf("Params %d: %v", size, err)
		}
	}
	for _, size := range []int{0, 16, AppParamBytes} {
		if err := r.validateAppPass(fullscreenDesc("good size", target, size)); err != nil {
			t.Fatalf("Params %d: %v", size, err)
		}
	}

	plain, err := r.CreateAppPass(fullscreenDesc("no block", target, 0))
	if err != nil {
		t.Fatal(err)
	}
	if plain.params != nil || r.appParamsSetLayout.Handle() != 0 {
		t.Fatal("a pass declaring no Params created a block or the second layout")
	}
	if r.appLayoutFor(plain) != r.appSetLayout {
		t.Fatal("a pass declaring no Params left the plain layout")
	}
	if n := d.created["Buffer"]; n != 0 {
		t.Fatalf("a pass declaring no Params created %d buffers", n)
	}

	var layoutBindings []core1_0.DescriptorSetLayoutBinding
	d.layoutSink = func(info core1_0.DescriptorSetLayoutCreateInfo) { layoutBindings = info.Bindings }
	block, err := r.CreateAppPass(fullscreenDesc("with block", target, 64))
	d.layoutSink = nil
	if err != nil {
		t.Fatal(err)
	}
	if block.params == nil || len(block.params.staged) != 64 {
		t.Fatalf("Params 64 staged %v", block.params)
	}
	if r.appParamsSetLayout.Handle() == 0 || r.appLayoutFor(block) != r.appParamsSetLayout {
		t.Fatal("a pass declaring Params did not take the block layout")
	}
	if len(layoutBindings) != 5 {
		t.Fatalf("block layout declares %d bindings, want the four samplers plus the block", len(layoutBindings))
	}
	for i, want := range []core1_0.DescriptorType{
		core1_0.DescriptorTypeCombinedImageSampler, core1_0.DescriptorTypeCombinedImageSampler,
		core1_0.DescriptorTypeCombinedImageSampler, core1_0.DescriptorTypeCombinedImageSampler,
		core1_0.DescriptorTypeUniformBuffer,
	} {
		if layoutBindings[i].DescriptorType != want {
			t.Fatalf("binding %d is %v, want %v", i, layoutBindings[i].DescriptorType, want)
		}
	}
	last := layoutBindings[4]
	if last.Binding != appParamsBinding || last.DescriptorCount != 1 ||
		last.StageFlags != core1_0.StageVertex|core1_0.StageFragment {
		t.Fatalf("block binding %+v", last)
	}
	// One host-visible buffer and one mapping per frame slot, and nothing more.
	if d.created["Buffer"] != maxFramesInFlight || len(d.mapped) != maxFramesInFlight {
		t.Fatalf("%d buffers, %d mappings for %d slots", d.created["Buffer"], len(d.mapped), maxFramesInFlight)
	}

	if err := block.SetParams(make([]byte, 64)); err != nil {
		t.Fatal(err)
	}
	for _, size := range []int{1, 15, 17, 80} {
		err := block.SetParams(make([]byte, size))
		if err == nil || !strings.Contains(err.Error(), "with block") {
			t.Fatalf("SetParams %d bytes: %v", size, err)
		}
	}
	if err := plain.SetParams(make([]byte, 16)); err == nil || !strings.Contains(err.Error(), "no block") {
		t.Fatalf("SetParams on a pass with no block: %v", err)
	}
	r.destroyAppResources()
	r.flushAllDeferred()
	bufferBalance(t, d.bufferTestDriver)
	t.Logf("no Params: plain layout, 0 buffers; Params 64: %d-binding layout with a uniform buffer at %d, %d slot buffers",
		len(layoutBindings), appParamsBinding, maxFramesInFlight)
}

// TestAppPassParamsSlotDiscipline is the block's reason for existing in the
// shape it has: a frame in flight must never read a half-written block.
//
// The setter writes a staging copy and nothing else; the copy into a slot's
// mapped buffer happens where DrawFrame has already waited on that slot's
// fence. So the check is that a staged value lands in the slot being prepared
// and that the other slot's bytes do not move -- and that the descriptor
// written for that slot names that slot's buffer, since a block copied into the
// right memory and read through the wrong descriptor is the same bug.
//
// Verified by breaking it: flushAppParams copying into mapped[1-frame] fails
// with "slot 0 holds 00 after a flush of A" -- see the recorded message in
// .task/params-report.md.
func TestAppPassParamsSlotDiscipline(t *testing.T) {
	r, d, target := paramsFixture(t)
	p, err := r.CreateAppPass(fullscreenDesc("slot block", target, 32))
	if err != nil {
		t.Fatal(err)
	}
	a := bytes.Repeat([]byte{0xa1}, 32)
	b := bytes.Repeat([]byte{0xb2}, 16)
	if err := p.SetParams(a); err != nil {
		t.Fatal(err)
	}
	a[0] = 0xff // the setter must have copied, not aliased
	for _, slot := range p.params.mapped {
		if !bytes.Equal(slot, make([]byte, 32)) {
			t.Fatal("the setter wrote GPU memory before any fence")
		}
	}

	d.watch, d.writes = p.sets[0], 0
	if err := r.flushAppFrameBindings(0, 0); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(p.params.mapped[0], bytes.Repeat([]byte{0xa1}, 32)) {
		t.Fatalf("slot 0 holds %x after a flush of A", p.params.mapped[0])
	}
	if !bytes.Equal(p.params.mapped[1], make([]byte, 32)) {
		t.Fatalf("slot 1 holds %x; the flush touched a slot that may be in flight", p.params.mapped[1])
	}
	// Exactly the four pass-input samplers and one block write, in that order,
	// all into the slot being prepared.
	if d.writes != 5 {
		t.Fatalf("%d descriptor writes for one pass with a block, want 4 samplers + 1", d.writes)
	}
	last := d.recent[4]
	if last.binding != appParamsBinding || last.kind != core1_0.DescriptorTypeUniformBuffer ||
		last.buffer != p.params.buffers[0] || last.size != 32 || last.set != p.sets[0] {
		t.Fatalf("block descriptor %+v, want slot 0's buffer at binding %d", last, appParamsBinding)
	}

	if err := p.SetParams(b); err != nil {
		t.Fatal(err)
	}
	d.watch, d.writes = p.sets[1], 0
	if err := r.flushAppFrameBindings(1, 0); err != nil {
		t.Fatal(err)
	}
	if d.writes != 5 {
		t.Fatalf("%d descriptor writes into slot 1, want 4 samplers + 1", d.writes)
	}
	if !bytes.Equal(p.params.mapped[0], bytes.Repeat([]byte{0xa1}, 32)) {
		t.Fatalf("slot 0 moved to %x while slot 1 was prepared", p.params.mapped[0])
	}
	want := append(bytes.Repeat([]byte{0xb2}, 16), make([]byte, 16)...)
	if !bytes.Equal(p.params.mapped[1], want) {
		t.Fatalf("slot 1 holds %x, want B with a zeroed tail", p.params.mapped[1])
	}
	if d.recent[4].buffer != p.params.buffers[1] || d.recent[4].binding != appParamsBinding {
		t.Fatalf("slot 1's block descriptor %+v does not name slot 1's buffer", d.recent[4])
	}
	d.watch = core1_0.DescriptorSet{}

	if err := p.SetParams(nil); err != nil {
		t.Fatal(err)
	}
	if err := r.flushAppFrameBindings(1, 0); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(p.params.mapped[1], make([]byte, 32)) {
		t.Fatalf("nil left %x in the block", p.params.mapped[1])
	}

	n := testing.AllocsPerRun(50, func() {
		p.flushAppParams(0)
		if err := r.writeAppParams(p, p.sets[0], 0); err != nil {
			panic(err)
		}
	})
	if n != 0 {
		t.Fatalf("the per-frame block path allocates %v times", n)
	}
	r.destroyAppResources()
	r.flushAllDeferred()
	bufferBalance(t, d.bufferTestDriver)
	t.Log("staged bytes reach only the prepared slot, each slot's descriptor names its own buffer, nil clears, 0 allocations per frame")
}

// TestAppComputeParamsBlock is the same declaration for a dispatch: the block
// lands at the same binding past the twelve a dispatch already has, visible to
// the compute stage, and SetParams reaches it through the shared pass.
func TestAppComputeParamsBlock(t *testing.T) {
	r, d, _ := paramsFixture(t)
	out, err := r.CreateRenderTarget(RenderTargetDesc{Name: "dispatch output", Format: TargetR32F, Scale: 1, Storage: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, size := range []int{-16, 17, AppParamBytes + 16} {
		desc := AppComputeDesc{Name: "bad size", Stage: StageBeforeScene, Comp: computeCode(t), Writes: []*RenderTarget{out}, Params: size}
		if err := r.validateAppCompute(desc); err == nil || !strings.Contains(err.Error(), "Params") {
			t.Fatalf("Params %d: %v", size, err)
		}
	}
	var layoutBindings []core1_0.DescriptorSetLayoutBinding
	d.layoutSink = func(info core1_0.DescriptorSetLayoutCreateInfo) { layoutBindings = info.Bindings }
	c, err := r.CreateAppCompute(AppComputeDesc{Name: "dispatch block", Stage: StageBeforeScene,
		Comp: computeCode(t), Writes: []*RenderTarget{out}, Params: 48})
	d.layoutSink = nil
	if err != nil {
		t.Fatal(err)
	}
	if len(layoutBindings) != 13 {
		t.Fatalf("dispatch block layout declares %d bindings, want twelve plus the block", len(layoutBindings))
	}
	last := layoutBindings[12]
	if last.Binding != appParamsBinding || last.DescriptorType != core1_0.DescriptorTypeUniformBuffer ||
		last.StageFlags != core1_0.StageCompute {
		t.Fatalf("dispatch block binding %+v", last)
	}
	if r.appLayoutFor(c.pass) != r.computeParamsSetLayout || r.computeSetLayout.Handle() != 0 {
		t.Fatal("a dispatch with a block took or created the plain compute layout")
	}
	if err := c.SetParams(bytes.Repeat([]byte{7}, 48)); err != nil {
		t.Fatal(err)
	}
	if err := c.SetParams(make([]byte, 64)); err == nil || !strings.Contains(err.Error(), "dispatch block") {
		t.Fatalf("oversized dispatch block: %v", err)
	}
	d.watch, d.writes = c.pass.sets[1], 0
	if err := r.flushAppFrameBindings(1, 0); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(c.pass.params.mapped[1], bytes.Repeat([]byte{7}, 48)) {
		t.Fatalf("dispatch slot 1 holds %x", c.pass.params.mapped[1])
	}
	found := false
	for _, w := range d.recent {
		found = found || w.binding == appParamsBinding && w.buffer == c.pass.params.buffers[1] && w.size == 48
	}
	if !found {
		t.Fatal("no block descriptor written for the prepared dispatch slot")
	}
	r.destroyAppResources()
	r.flushAllDeferred()
	bufferBalance(t, d.bufferTestDriver)
	t.Log("dispatch block at the same binding past the storage buffers, compute-visible, written per slot")
}

// TestAppParamsCreationUnwinds walks every driver call the block's buffers make
// and fails it, once per site, asserting the pass is not left registered and
// that everything made along the way came back.
//
// The meta-check at the end is the part that keeps the balance assertion honest:
// it removes one destroy of each kind and requires the assertion to notice. The
// first version of a teardown check in this repository passed because teardown
// never ran, so a balance that cannot fail is worth nothing.
func TestAppParamsCreationUnwinds(t *testing.T) {
	create := func(r *Renderer, target *RenderTarget) error {
		_, err := r.CreateAppPass(fullscreenDesc("unwound block", target, 64))
		return err
	}
	rc, control, target := paramsFixture(t)
	// The target's own allocations are not this check's subject, and counting
	// them would renumber every site below -- the first version of this did,
	// and the AllocateMemory site that fell on the target's image memory had to
	// be skipped rather than exercised.
	clear(control.calls)
	if err := create(rc, target); err != nil {
		t.Fatal(err)
	}
	rc.destroyAppResources()
	rc.flushAllDeferred()
	bufferBalance(t, control.bufferTestDriver)

	for _, call := range []string{"CreateBuffer", "AllocateMemory", "BindBufferMemory", "MapMemory"} {
		sites := control.calls[call]
		if sites == 0 {
			t.Fatalf("no sites for %s", call)
		}
		for at := 1; at <= sites; at++ {
			t.Run(fmt.Sprintf("%s/%d", call, at), func(t *testing.T) {
				r, d, target := paramsFixture(t)
				clear(d.calls)
				d.failCall, d.failAt = call, at
				err := create(r, target)
				if !errors.Is(err, errInjected) {
					t.Fatalf("%s call %d: %v", call, at, err)
				}
				if !strings.Contains(err.Error(), "unwound block") {
					t.Fatalf("error lost the pass name: %v", err)
				}
				if len(r.appPasses) != 0 {
					t.Fatal("failed pass remained registered")
				}
				r.destroyAppResources()
				r.flushAllDeferred()
				bufferBalance(t, d.bufferTestDriver)
			})
		}
		t.Logf("%s: all %d sites unwind without leaks", call, sites)
	}
	for kind, count := range control.created {
		if count == 0 {
			continue
		}
		control.destroyed[kind]--
		captured := &capturingT{TB: t}
		bufferBalance(captured, control.bufferTestDriver)
		control.destroyed[kind]++
		if !captured.failed {
			t.Fatalf("balance meta-check missed %s", kind)
		}
	}
	t.Log("balance meta-check detects a missing destroy for every kind the block's creation touches")
}

// TestAppParamsRecordNoCommands pins the claim the whole two-layout design is
// for: a declared block is a descriptor, not a command. The recorded stream of
// the application fixture with a block on every pass has to be the same stream,
// call for call, as the pinned one without.
//
// It is the companion of TestAppPassStreams rather than a replacement: that one
// pins what a pass with no block records, this one pins that declaring a block
// adds nothing to it. If the block ever needs a command -- a flush, a barrier --
// this is where that shows up instead of in a frame on someone's hardware.
func TestAppParamsRecordNoCommands(t *testing.T) {
	fx := withAppFrame(buildFrame(97), true, true)
	blocks := 0
	for i := range fx.graph.nodes {
		p := fx.graph.nodes[i].app
		if p == nil {
			continue
		}
		p.desc.Params = 64
		p.params = &appParams{staged: make([]byte, 64)}
		for slot := range p.params.mapped {
			p.params.mapped[slot] = make([]byte, 64)
		}
		blocks++
	}
	if blocks == 0 {
		t.Fatal("no application passes in the fixture; this would pass by checking nothing")
	}
	d := &fakeDriver{hashing: true}
	if err := fx.record(d, 1); err != nil {
		t.Fatal(err)
	}
	if d.h != goldenAppComputeStreamHash || d.calls != goldenAppComputeCalls {
		t.Errorf("%d blocks declared: %d calls, hash %#x; want %d, %#x",
			blocks, d.calls, d.h, goldenAppComputeCalls, goldenAppComputeStreamHash)
	}
	t.Logf("%d declared blocks add 0 commands: %d calls, hash %#x", blocks, d.calls, d.h)
}
