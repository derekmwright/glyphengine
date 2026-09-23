package renderer

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"os"
	"strings"
	"testing"

	"github.com/derekmwright/glyphengine/renderer/framegraph"
	"github.com/vkngwrapper/core/v3/common"
	"github.com/vkngwrapper/core/v3/core1_0"
	"github.com/vkngwrapper/core/v3/loader"
)

func computeCode(t *testing.T) []byte {
	t.Helper()
	b, e := os.ReadFile("../cmd/apppasscheck/blur.comp.spv")
	if e != nil {
		t.Fatal(e)
	}
	return b
}

func (d *resizeFakeDriver) CreateComputePipelines(_ *core1_0.PipelineCache, _ *loader.AllocationCallbacks, _ ...core1_0.ComputePipelineCreateInfo) ([]core1_0.Pipeline, common.VkResult, error) {
	if d.shouldFail("CreateComputePipelines") {
		// Vulkan can return partial pipelines along with a failed batch.
		d.created["Pipeline"]++
		return []core1_0.Pipeline{d.h.pipeline()}, core1_0.VKErrorUnknown, errInjected
	}
	d.created["Pipeline"]++
	return []core1_0.Pipeline{d.h.pipeline()}, core1_0.VKSuccess, nil
}

func TestAppComputeCreationUnwinds(t *testing.T) {
	control := newResizeFakeDriver()
	create := func(d *resizeFakeDriver) (*Renderer, error) {
		r := newResizeFixture(d, 3)
		r.depth = &depthResources{format: core1_0.FormatD32SignedFloat}
		r.frameGraph.cache = make(map[framegraph.RenderPassKey]core1_0.RenderPass)
		target, err := r.CreateRenderTarget(RenderTargetDesc{Name: "compute target", Format: TargetR32F, Scale: 1, Storage: true, History: true})
		if err == nil {
			_, err = r.CreateAppCompute(AppComputeDesc{Name: "compute fixture", Stage: StageBeforeScene, Comp: computeCode(t), Reads: []*Texture{target.Texture()}, Writes: []*RenderTarget{target}})
		}
		return r, err
	}
	cleanup := func(r *Renderer, d *resizeFakeDriver) { r.destroyAppResources(); r.frameGraph.destroyPasses(d) }
	r, err := create(control)
	if err != nil {
		t.Fatal(err)
	}
	cleanup(r, control)
	balance := func(tb testing.TB, d *resizeFakeDriver) {
		assertBalanced(tb, d)
		for _, kind := range []string{"Pipeline", "PipelineLayout", "ShaderModule", "DescriptorSetLayout"} {
			if d.created[kind] != d.destroyed[kind] {
				tb.Errorf("%s: created %d destroyed %d", kind, d.created[kind], d.destroyed[kind])
			}
		}
	}
	balance(t, control)
	for _, call := range []string{"CreateImage", "AllocateMemory", "CreateImageView", "CreateSampler", "AllocateDescriptorSets", "CreateDescriptorSetLayout", "CreateShaderModule", "CreatePipelineLayout", "CreateComputePipelines"} {
		if control.calls[call] == 0 {
			t.Fatalf("no sites for %s", call)
		}
		for at := 1; at <= control.calls[call]; at++ {
			t.Run(fmt.Sprintf("%s/%d", call, at), func(t *testing.T) {
				d := newResizeFakeDriver()
				d.failCall, d.failAt = call, at
				r, err := create(d)
				if !errors.Is(err, errInjected) {
					t.Fatalf("error: %v", err)
				}
				if len(r.appPasses) != 0 {
					t.Fatal("failed compute remained registered")
				}
				if call == "CreateComputePipelines" && !strings.Contains(err.Error(), "compute fixture") {
					t.Fatal(err)
				}
				cleanup(r, d)
				balance(t, d)
			})
		}
		t.Logf("%s: all %d compute creation sites unwind without leaks", call, control.calls[call])
	}
	for kind, count := range control.created {
		if count == 0 {
			continue
		}
		control.destroyed[kind]--
		captured := &capturingT{TB: t}
		balance(captured, control)
		control.destroyed[kind]++
		if !captured.failed {
			t.Fatalf("balance meta-check missed %s", kind)
		}
	}
	t.Log("balance meta-check detects a missing destroy for every compute resource kind, including partial pipelines")
}

func TestAppComputeStreams(t *testing.T) {
	d := &fakeDriver{hashing: true}
	if err := withAppFrame(buildFrame(97), true, true).record(d, 1); err != nil {
		t.Fatal(err)
	}
	t.Logf("graphics plus compute: %d calls, hash %#x", d.calls, d.h)
	if d.h != goldenAppComputeStreamHash {
		t.Errorf("hash %#x want %#x", d.h, goldenAppComputeStreamHash)
	}
	if d.calls != goldenAppComputeCalls {
		t.Errorf("calls %d want %d", d.calls, goldenAppComputeCalls)
	}
}

// Verified dispatch removal: 3338 calls instead of 3339, hash
// 0xa0170632efdd823d; the direct command probe also reports zero dispatches.
// Four compute commands, two entry barrier groups, one return barrier, and
// one additional scene-input barrier add eight calls to the 3331-call graphics fixture.
const goldenAppComputeStreamHash Hasher = 0x7c76c535eabbcaa0
const goldenAppComputeCalls = 3339

func TestAppComputeAllocs(t *testing.T) {
	for _, n := range []int{7, 97, 511} {
		fx := withAppFrame(buildFrame(n), true, true)
		d := &fakeDriver{}
		if err := fx.record(d, 1); err != nil {
			t.Fatal(err)
		}
		a := testing.AllocsPerRun(50, func() {
			if err := fx.record(d, 1); err != nil {
				panic(err)
			}
		})
		t.Logf("%d engine draws plus graphics and compute: %.0f allocs/frame", n, a)
		if a != 0 {
			t.Errorf("%.0f allocations", a)
		}
	}
}

func TestAppComputeGraph(t *testing.T) {
	fx := withAppFrame(buildFrame(7), true, true)
	f := fx.graph
	var compute int
	var p *AppCompute
	for i, n := range f.nodes {
		if n.app != nil && n.app.compute != nil {
			compute = i
			p = n.app.compute
		}
	}
	n := f.declarations[compute]
	if n.Kind != framegraph.Compute || f.plan.Steps[compute].RenderPass != nil {
		t.Fatal("compute must dispatch outside a render pass")
	}
	if f.nodes[compute-1].app.desc.Name != "fixture mesh" || compute >= f.engine[graphLegacy] {
		t.Fatal("mixed creation/stage order lost")
	}
	target := p.desc.Writes[0]
	ids := f.targets[target]
	seenRead, seenWrite, incoming, outgoing := false, false, false, false
	for _, u := range n.Uses {
		seenRead = seenRead || u.Resource == ids.read && u.Access == framegraph.SampledRead
		seenWrite = seenWrite || u.Resource == ids.write && u.Access == framegraph.StorageReadWrite
	}
	for _, b := range f.plan.Steps[compute].Barriers {
		incoming = incoming || b.Resource == ids.write && b.NewLayout == core1_0.ImageLayoutGeneral && b.DstStage == core1_0.PipelineStageComputeShader
	}
	for _, b := range f.plan.Steps[compute+1].Barriers {
		outgoing = outgoing || b.Resource == ids.write && b.OldLayout == core1_0.ImageLayoutGeneral && b.NewLayout == core1_0.ImageLayoutShaderReadOnlyOptimal && b.SrcAccess&core1_0.AccessShaderWrite != 0
	}
	if !seenRead || !seenWrite || !incoming || !outgoing {
		t.Fatalf("history read/write=%v/%v; derived entry/exit=%v/%v", seenRead, seenWrite, incoming, outgoing)
	}
	if n.OptionalGroup < 2 || f.declarations[compute+1].OptionalGroup != n.OptionalGroup {
		t.Fatal("dispatch and restore are not one optional group")
	}
	t.Log("graphics -> compute -> sampled barriers derived; history has separate identities; mixed order preserved")
}

func TestAppComputeZeroAndDisabledTimings(t *testing.T) {
	for _, groups := range [][3]uint32{{0, 1, 1}, {1, 0, 1}, {1, 1, 0}, {1, 1, 1}} {
		fx := withAppFrame(buildFrame(7), true, true)
		var p *AppCompute
		for _, n := range fx.graph.nodes {
			if n.app != nil && n.app.compute != nil {
				p = n.app.compute
			}
		}
		p.pass.desc.Timed = true
		p.SetDispatch(groups[0], groups[1], groups[2])
		p.SetEnabled(groups != [3]uint32{1, 1, 1})
		fx.timer = &gpuTimer{supported: true, apps: []*AppPass{p.pass}}
		d := &computeRecordProbe{appTimestampProbe: &appTimestampProbe{fakeDriver: &fakeDriver{hashing: true}}}
		if err := fx.record(d, 1); err != nil {
			t.Fatal(err)
		}
		if d.dispatches != 0 || d.pushes != 0 || d.binds != 0 {
			t.Fatal("disabled or zero dispatch recorded compute commands")
		}
		counts := map[int]int{}
		for _, q := range d.queries {
			counts[q]++
		}
		for i := range 2 {
			if counts[appQueryBase(1)+i] != 1 {
				t.Fatal("missing timing edge")
			}
		}
		t.Logf("dispatch=%v enabled=%v: both timestamp edges", groups, p.pass.enabled)
	}
}

func TestAppComputeDescriptions(t *testing.T) {
	r := &Renderer{}
	target := &RenderTarget{r: r, desc: RenderTargetDesc{Name: "output", Storage: true}}
	target.texture.target = target
	good := AppComputeDesc{Name: "check", Stage: StageBeforeScene, Comp: computeCode(t), Writes: []*RenderTarget{target}}
	for _, tc := range []struct {
		field string
		edit  func(*AppComputeDesc)
	}{
		{"Stage", func(d *AppComputeDesc) { d.Stage = 0 }}, {"Comp", func(d *AppComputeDesc) { d.Comp = nil }},
		{"Writes", func(d *AppComputeDesc) { d.Writes = []*RenderTarget{nil} }}, {"Writes", func(d *AppComputeDesc) { d.Writes = make([]*RenderTarget, 5) }},
		{"Writes", func(d *AppComputeDesc) { d.Writes = []*RenderTarget{target, target} }},
		{"Reads", func(d *AppComputeDesc) { d.Reads = []*Texture{nil} }}, {"Reads", func(d *AppComputeDesc) { d.Reads = make([]*Texture, 5) }},
		{"Reads", func(d *AppComputeDesc) { d.Reads = []*Texture{target.Texture()} }},
		{"Reads", func(d *AppComputeDesc) { d.Reads = []*Texture{r.SceneColor()} }}, {"Reads", func(d *AppComputeDesc) { d.Reads = []*Texture{r.SceneDepth()} }},
	} {
		d := good
		tc.edit(&d)
		if err := r.validateAppCompute(d); err == nil || !strings.Contains(err.Error(), tc.field) {
			t.Errorf("%s: %v", tc.field, err)
		}
	}
	target.desc.Storage = false
	if err := r.validateAppCompute(good); err == nil || !strings.Contains(err.Error(), "output") {
		t.Fatal("missing named Storage error")
	}
	target.desc.Storage = true
	target.desc.History = true
	good.Reads = []*Texture{target.Texture()}
	if err := r.validateAppCompute(good); err != nil {
		t.Fatal(err)
	}
	for _, stage := range []PassStage{StageAfterScene, StageBeforeBloom, StageBeforeTonemap} {
		good.Stage = stage
		good.Reads = []*Texture{r.SceneColor(), r.SceneDepth()}
		if err := r.validateAppCompute(good); err != nil {
			t.Fatal(err)
		}
	}
	for i := range maxAppTimings {
		p := &AppPass{desc: AppPassDesc{Timed: true}}
		if i%2 == 0 {
			p.compute = &AppCompute{}
		}
		r.appPasses = append(r.appPasses, p)
	}
	good.Timed = true
	if err := r.validateAppCompute(good); err == nil || !strings.Contains(err.Error(), "Timed") {
		t.Fatal("shared timing capacity not enforced")
	}
}

type computeDescriptorProbe struct {
	*appDescriptorProbe
	storage bool
}

func (d *computeDescriptorProbe) UpdateDescriptorSets(w []core1_0.WriteDescriptorSet, c []core1_0.CopyDescriptorSet) error {
	for _, v := range w {
		if v.DstBinding >= 4 {
			if v.DescriptorType != core1_0.DescriptorTypeStorageImage || v.ImageInfo[0].ImageLayout != core1_0.ImageLayoutGeneral {
				panic("wrong storage descriptor")
			}
			d.storage = true
		}
	}
	return d.appDescriptorProbe.UpdateDescriptorSets(w, c)
}
func TestAppComputeFrameBindingsAndDestroy(t *testing.T) {
	d := &computeDescriptorProbe{appDescriptorProbe: &appDescriptorProbe{resizeFakeDriver: newResizeFakeDriver()}}
	r := newResizeFixture(d.resizeFakeDriver, 3)
	r.deviceDriver = d
	r.depth = &depthResources{format: core1_0.FormatD32SignedFloat}
	r.frameGraph.cache = make(map[framegraph.RenderPassKey]core1_0.RenderPass)
	r.fallbackTexture = &Texture{view: d.h.imageView(), sampler: d.h.sampler()}
	target, err := r.CreateRenderTarget(RenderTargetDesc{Name: "storage", Format: TargetR32F, Scale: 1, History: true, Storage: true})
	if err != nil {
		t.Fatal(err)
	}
	p, err := r.CreateAppCompute(AppComputeDesc{Name: "writer", Stage: StageBeforeScene, Comp: computeCode(t), Reads: []*Texture{target.Texture()}, Writes: []*RenderTarget{target}})
	if err != nil {
		t.Fatal(err)
	}
	for frame := range 2 {
		target.selectTexture(frame)
		d.writes = 0
		if err := r.flushAppInputs(p.pass, p.pass.sets[frame]); err != nil {
			t.Fatal(err)
		}
		if err := r.flushComputeOutputs(p, p.pass.sets[frame], frame); err != nil {
			t.Fatal(err)
		}
		if d.recent[0].view != target.color.views[1-frame] || d.recent[4].view != target.color.views[frame] || d.recent[4].set != p.pass.sets[frame] || !d.storage {
			t.Fatal("compute bindings did not select opposite history images in the waited set")
		}
	}
	a := testing.AllocsPerRun(50, func() {
		if err := r.flushComputeOutputs(p, p.pass.sets[0], 0); err != nil {
			panic(err)
		}
	})
	if a != 0 {
		t.Fatalf("compute descriptor writes allocate: %v", a)
	}
	r.DestroyRenderTarget(target)
	if !p.pass.destroyed || len(r.appPasses) != 0 {
		t.Fatal("destroy target left writer live")
	}
	r.DestroyAppCompute(p)
	r.DestroyAppCompute(nil)
	r.flushAllDeferred()
	r.destroyAppResources()
	r.frameGraph.destroyPasses(d)
	assertBalanced(t, d.resizeFakeDriver)
	t.Log("waited compute sets select previous sampled/current storage images; 0 descriptor allocations; destroying an output retires its writer")
}

type noStorageInstance struct{ resizeFakeInstanceDriver }

func (noStorageInstance) GetPhysicalDeviceFormatProperties(core1_0.PhysicalDevice, core1_0.Format) *core1_0.FormatProperties {
	return &core1_0.FormatProperties{}
}
func TestAppStorageFormatValidation(t *testing.T) {
	d := newResizeFakeDriver()
	r := newResizeFixture(d, 3)
	r.instanceDriver = noStorageInstance{}
	for _, format := range []TargetFormat{TargetR16F, TargetRG16F, TargetRGBA16F, TargetR32F, TargetRGBA32F} {
		_, err := r.CreateRenderTarget(RenderTargetDesc{Name: "unsupported", Format: format, Scale: 1, Storage: true})
		vk, _ := targetFormat(format)
		if err == nil || !strings.Contains(err.Error(), fmt.Sprint(vk)) {
			t.Fatalf("format error lacks %v: %v", vk, err)
		}
	}
	if d.created["Image"] != 0 {
		t.Fatal("unsupported format allocated an image")
	}
}

type computeRecordProbe struct {
	*appTimestampProbe
	dispatches, pushes, binds int
	push                      [256]byte
	groups                    [3]int
}

func (d *computeRecordProbe) CmdDispatch(_ core1_0.CommandBuffer, x, y, z int) {
	d.dispatches++
	d.groups = [3]int{x, y, z}
}
func (d *computeRecordProbe) CmdBindPipeline(cb core1_0.CommandBuffer, bp core1_0.PipelineBindPoint, p core1_0.Pipeline) {
	if bp == core1_0.PipelineBindPointCompute {
		d.binds++
	}
	d.fakeDriver.CmdBindPipeline(cb, bp, p)
}
func (d *computeRecordProbe) CmdPushConstants(cb core1_0.CommandBuffer, l core1_0.PipelineLayout, stages core1_0.ShaderStageFlags, offset int, data []byte) {
	if stages == core1_0.StageCompute {
		d.pushes++
		if offset != 0 || len(data) != 256 {
			panic("wrong compute push range")
		}
		copy(d.push[:], data)
	}
	d.fakeDriver.CmdPushConstants(cb, l, stages, offset, data)
}
func TestAppComputePushAndDispatch(t *testing.T) {
	fx := withAppFrame(buildFrame(7), true, true)
	var p *AppCompute
	for _, n := range fx.graph.nodes {
		if n.app != nil && n.app.compute != nil {
			p = n.app.compute
		}
	}
	data := make([]byte, 16)
	binary.LittleEndian.PutUint32(data, math.Float32bits(0.5))
	if err := p.SetPushConstants(data); err != nil {
		t.Fatal(err)
	}
	old := p.pass.push
	if p.SetPushConstants(make([]byte, 17)) == nil || p.pass.push != old {
		t.Fatal("invalid compute push changed data")
	}
	p.SetDispatch(3, 5, 7)
	d := &computeRecordProbe{appTimestampProbe: &appTimestampProbe{fakeDriver: &fakeDriver{}}}
	if err := fx.record(d, 1); err != nil {
		t.Fatal(err)
	}
	if d.dispatches != 1 || d.binds != 1 || d.pushes != 1 || d.groups != [3]int{3, 5, 7} {
		t.Fatalf("compute commands: %+v", d)
	}
	for i := range 64 {
		want := float32(0)
		if i < 16 {
			want = fx.lighting.VP[i]
		} else if i == 16 || i == 21 || i == 26 || i == 31 {
			want = 1
		} else if i == 32 {
			want = .5
		}
		if got := binary.LittleEndian.Uint32(d.push[i*4:]); got != math.Float32bits(want) {
			t.Fatalf("push[%d]: %#x want %v", i, got, want)
		}
	}
	if err := p.SetPushConstants(nil); err != nil || p.pass.push != [32]float32{} {
		t.Fatal("nil compute push did not clear")
	}
	t.Log("one dispatch (3,5,7), compute-only push range: VP + identity model + 128 application bytes")
}
