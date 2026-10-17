package renderer

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/derekmwright/glyphengine/renderer/framegraph"
	"github.com/vkngwrapper/core/v3/common"
	"github.com/vkngwrapper/core/v3/core1_0"
	"github.com/vkngwrapper/core/v3/loader"
)

// mipProbe records what a chain's construction and its frames hand the
// driver: image and view create infos by handle, sampler infos, the last
// descriptor written at each binding of each set, the ranges the prime clears,
// and every image barrier's subresource range.
type mipProbe struct {
	*resizeFakeDriver
	images   map[core1_0.Image]core1_0.ImageCreateInfo
	views    map[core1_0.ImageView]core1_0.ImageViewCreateInfo
	samplers map[core1_0.Sampler]core1_0.SamplerCreateInfo
	bound    map[core1_0.DescriptorSet]map[int]core1_0.DescriptorImageInfo
	clears   []core1_0.ImageSubresourceRange
	barriers []core1_0.ImageMemoryBarrier
}

func newMipProbe() *mipProbe {
	return &mipProbe{resizeFakeDriver: newResizeFakeDriver(),
		images: map[core1_0.Image]core1_0.ImageCreateInfo{}, views: map[core1_0.ImageView]core1_0.ImageViewCreateInfo{},
		samplers: map[core1_0.Sampler]core1_0.SamplerCreateInfo{}, bound: map[core1_0.DescriptorSet]map[int]core1_0.DescriptorImageInfo{}}
}

func (d *mipProbe) CreateImage(cb *loader.AllocationCallbacks, o core1_0.ImageCreateInfo) (core1_0.Image, common.VkResult, error) {
	img, res, err := d.resizeFakeDriver.CreateImage(cb, o)
	if err == nil {
		d.images[img] = o
	}
	return img, res, err
}

func (d *mipProbe) CreateImageView(cb *loader.AllocationCallbacks, o core1_0.ImageViewCreateInfo) (core1_0.ImageView, common.VkResult, error) {
	v, res, err := d.resizeFakeDriver.CreateImageView(cb, o)
	if err == nil {
		d.views[v] = o
	}
	return v, res, err
}

func (d *mipProbe) CreateSampler(cb *loader.AllocationCallbacks, o core1_0.SamplerCreateInfo) (core1_0.Sampler, common.VkResult, error) {
	s, res, err := d.resizeFakeDriver.CreateSampler(cb, o)
	if err == nil {
		d.samplers[s] = o
	}
	return s, res, err
}

func (d *mipProbe) UpdateDescriptorSets(w []core1_0.WriteDescriptorSet, _ []core1_0.CopyDescriptorSet) error {
	for _, v := range w {
		if len(v.ImageInfo) == 0 {
			continue
		}
		if d.bound[v.DstSet] == nil {
			d.bound[v.DstSet] = map[int]core1_0.DescriptorImageInfo{}
		}
		d.bound[v.DstSet][v.DstBinding] = v.ImageInfo[0]
	}
	return nil
}

func (d *mipProbe) CmdClearColorImage(_ core1_0.CommandBuffer, _ core1_0.Image, _ core1_0.ImageLayout, _ core1_0.ClearColorValue, ranges ...core1_0.ImageSubresourceRange) {
	d.clears = append(d.clears, ranges...)
}

func (d *mipProbe) CmdPipelineBarrier(_ core1_0.CommandBuffer, _, _ core1_0.PipelineStageFlags, _ core1_0.DependencyFlags, _ []core1_0.MemoryBarrier, _ []core1_0.BufferMemoryBarrier, imgs []core1_0.ImageMemoryBarrier) error {
	d.barriers = append(d.barriers, imgs...)
	return nil
}

func mipFixture() (*Renderer, *mipProbe) { return mipFixtureWith(newMipProbe()) }

func mipFixtureWith(d *mipProbe) (*Renderer, *mipProbe) {
	r := newResizeFixture(d.resizeFakeDriver, 3)
	r.deviceDriver = d
	r.depth = &depthResources{format: core1_0.FormatD32SignedFloat}
	r.fallbackTexture = &Texture{view: d.h.imageView(), sampler: d.h.sampler()}
	r.instanceDriver = targetFormatProbe{props: core1_0.FormatProperties{OptimalTilingFeatures: core1_0.FormatFeatureSampledImageFilterLinear | core1_0.FormatFeatureStorageImage}}
	return r, d
}

// chainDesc is the hardware check's target: 64x32, the full seven levels.
func chainDesc() RenderTargetDesc {
	return RenderTargetDesc{Name: "chain", Format: TargetRGBA16F, Width: 64, Height: 32, MipLevels: 7, Storage: true, Filter: FilterLinear, Wrap: WrapRepeat}
}

func TestTargetMipChainAllocation(t *testing.T) {
	for _, history := range []bool{false, true} {
		r, d := mipFixture()
		desc := chainDesc()
		desc.History = history
		target, err := r.CreateRenderTarget(desc)
		if err != nil {
			t.Fatal(err)
		}
		if target.Levels() != 7 {
			t.Fatalf("Levels %d, want 7", target.Levels())
		}
		c := target.color
		for i, img := range c.images {
			if info := d.images[img]; info.MipLevels != 7 {
				t.Fatalf("image %d: MipLevels %d, want 7", i, info.MipLevels)
			}
			// Level 0 stays the single-level view attachments render through.
			if v := d.views[c.views[i]].SubresourceRange; v.BaseMipLevel != 0 || v.LevelCount != 1 {
				t.Fatalf("image %d: base view %+v", i, v)
			}
			if v := d.views[c.chain[i]]; v.Image != img || v.SubresourceRange.BaseMipLevel != 0 || v.SubresourceRange.LevelCount != 7 {
				t.Fatalf("image %d: sampled view %+v, want all seven levels", i, v.SubresourceRange)
			}
			if c.textures[i].view != c.chain[i] {
				t.Fatalf("image %d: texture view is not the whole-chain view", i)
			}
			if got := d.bound[c.textures[i].DescriptorSet][0]; got.ImageView != c.chain[i] {
				t.Fatalf("image %d: texture descriptor names %v, not the chain view", i, got.ImageView)
			}
			for l := range 7 {
				v := d.views[c.levelView(i, l)]
				if v.Image != img || v.SubresourceRange.BaseMipLevel != l || v.SubresourceRange.LevelCount != 1 {
					t.Fatalf("image %d: storage view of level %d covers %+v", i, l, v.SubresourceRange)
				}
			}
		}
		// One base view, one sampled chain view and six storage views per image.
		if want := 8 * len(c.images); d.created["ImageView"] != want {
			t.Fatalf("%d views for %d images, want %d", d.created["ImageView"], len(c.images), want)
		}
		s := d.samplers[c.sampler]
		if s.MaxLod != 7 || s.MinLod != 0 || s.MipmapMode != core1_0.SamplerMipmapModeLinear || s.AddressModeU != core1_0.SamplerAddressModeRepeat {
			t.Fatalf("sampler %+v: want MaxLod 7, linear between levels, repeat", s)
		}
		for _, rg := range d.clears {
			if rg.LevelCount != 7 {
				t.Fatalf("prime clears %+v, want every level", rg)
			}
		}
		r.destroyAppResources()
		assertBalanced(t, d.resizeFakeDriver)
	}

	// A nearest chain steps between levels; a single-level target keeps the
	// sampler, the one view and the one-level clear it always had.
	r, d := mipFixture()
	desc := chainDesc()
	desc.Filter = FilterNearest
	nearest, err := r.CreateRenderTarget(desc)
	if err != nil {
		t.Fatal(err)
	}
	if s := d.samplers[nearest.color.sampler]; s.MaxLod != 7 || s.MipmapMode != core1_0.SamplerMipmapModeNearest {
		t.Fatalf("nearest chain sampler %+v", s)
	}
	views, clears := d.created["ImageView"], len(d.clears)
	flat, err := r.CreateRenderTarget(RenderTargetDesc{Name: "flat", Format: TargetRGBA16F, Width: 64, Height: 32, Storage: true, Filter: FilterLinear})
	if err != nil {
		t.Fatal(err)
	}
	if s := d.samplers[flat.color.sampler]; s != (opts(flat)).samplerInfo() || s.MaxLod != 0 || s.MipmapMode != 0 {
		t.Fatalf("single-level sampler %+v", s)
	}
	if d.created["ImageView"]-views != 1 || flat.color.chain != nil || flat.color.levelViews != nil || flat.Levels() != 1 {
		t.Fatalf("single-level target made %d views", d.created["ImageView"]-views)
	}
	if d.clears[clears].LevelCount != 1 || d.images[flat.color.images[0]].MipLevels != 1 {
		t.Fatal("single-level target is not one level")
	}
	r.destroyAppResources()
	assertBalanced(t, d.resizeFakeDriver)
}

func opts(t *RenderTarget) appImageOptions {
	return appImageOptions{sampled: true, storage: t.desc.Storage, filter: t.desc.Filter, wrap: t.desc.Wrap}
}

// A dispatch over level l must be sized from l's extent. The chain floors
// each axis to one on its own, so a non-square target reaches 1 in its short
// axis first and keeps halving the long one.
//
// Verified to fail: with LevelExtent returning the base extent (levelExtent
// ignoring l), it reports "64x32 level 1: 64x32, want 32x16".
func TestLevelExtentForDispatch(t *testing.T) {
	for _, tc := range []struct {
		w, h   uint32
		levels int
		want   [][2]uint32
	}{
		{64, 32, 7, [][2]uint32{{64, 32}, {32, 16}, {16, 8}, {8, 4}, {4, 2}, {2, 1}, {1, 1}}},
		{16, 2, 5, [][2]uint32{{16, 2}, {8, 1}, {4, 1}, {2, 1}, {1, 1}}},
		{3, 5, 3, [][2]uint32{{3, 5}, {1, 2}, {1, 1}}},
	} {
		r, d := mipFixture()
		target, err := r.CreateRenderTarget(RenderTargetDesc{Name: "extent", Format: TargetR16F, Width: tc.w, Height: tc.h, MipLevels: tc.levels, Storage: true})
		if err != nil {
			t.Fatal(err)
		}
		for l, want := range tc.want {
			if w, h := target.LevelExtent(l); w != want[0] || h != want[1] {
				t.Fatalf("%dx%d level %d: %dx%d, want %dx%d", tc.w, tc.h, l, w, h, want[0], want[1])
			}
		}
		if w, h := target.LevelExtent(tc.levels); w != 0 || h != 0 {
			t.Fatalf("%dx%d: level %d past the chain reports %dx%d", tc.w, tc.h, tc.levels, w, h)
		}
		_, err = r.CreateRenderTarget(RenderTargetDesc{Name: "too long", Format: TargetR16F, Width: tc.w, Height: tc.h, MipLevels: tc.levels + 1})
		if err == nil || !strings.Contains(err.Error(), fmt.Sprintf("MipLevels: %d levels exceed the %d a %dx%d chain has", tc.levels+1, tc.levels, tc.w, tc.h)) {
			t.Fatalf("a chain one level too long: %v", err)
		}
		r.destroyAppResources()
		assertBalanced(t, d.resizeFakeDriver)
	}
	_, err := new(Renderer).CreateRenderTarget(RenderTargetDesc{Name: "negative", Format: TargetR16F, Scale: 1, MipLevels: -1})
	if err == nil || !strings.Contains(err.Error(), "MipLevels: -1 is negative") {
		t.Fatalf("negative MipLevels: %v", err)
	}
}

// reduction is the dispatch the hardware check runs: read level n through a
// range of its own, write level n+1 of the same target.
func reduction(t *testing.T, chain *RenderTarget, n int) AppComputeDesc {
	return AppComputeDesc{Name: fmt.Sprintf("reduce %d", n), Stage: StageBeforeScene, Comp: computeCode(t),
		Reads: []*Texture{chain.Texture()}, ReadRanges: []LevelRange{{Base: n, Count: 1}},
		Writes: []*RenderTarget{chain}, WriteLevels: []int{n + 1}}
}

// Every declaration error, in every direction, names the pass, the index and
// the counts; a disjoint read and write of one chain is accepted.
//
// Verified to fail: with validateAppOverlap checking nothing, creation is
// still refused, by the frame graph's own same-node rule, but without the
// index or the counts: "whole-chain read of a written chain: framegraph: node
// "reduce 2" resource "chain": the whole image and levels [3,4) overlap in one
// node ..., want "Reads: index 0 reads levels [0,7) of target "chain",
// overlapping level 3 that Writes index 0 writes"".
func TestMipDeclarationErrors(t *testing.T) {
	r, d := mipFixture()
	chain, err := r.CreateRenderTarget(chainDesc())
	if err != nil {
		t.Fatal(err)
	}
	flat, err := r.CreateRenderTarget(RenderTargetDesc{Name: "flat", Format: TargetRGBA16F, Width: 8, Height: 8, Storage: true})
	if err != nil {
		t.Fatal(err)
	}
	plain := &Texture{view: d.h.imageView(), sampler: d.h.sampler()}
	base := func() AppComputeDesc { return reduction(t, chain, 2) }
	for _, tc := range []struct {
		name, want string
		edit       func(*AppComputeDesc)
	}{
		{"more ranges than reads", `app compute "reduce 2": ReadRanges: 2 entries for 1 reads`, func(c *AppComputeDesc) { c.ReadRanges = append(c.ReadRanges, LevelRange{}) }},
		{"a negative base", "ReadRanges: index 0: {Base -1, Count 1} is not a level range", func(c *AppComputeDesc) { c.ReadRanges[0].Base = -1 }},
		{"a base with no count", "ReadRanges: index 0: {Base 2, Count 0} is not a level range", func(c *AppComputeDesc) { c.ReadRanges[0].Count = 0 }},
		{"a range past the chain", `ReadRanges: index 0 selects levels [5,8) of target "chain", which has 7 levels`, func(c *AppComputeDesc) { c.ReadRanges[0] = LevelRange{Base: 5, Count: 3} }},
		{"a range on a single-level target", `ReadRanges: index 0 selects levels [0,1) of target "flat", which has one level`, func(c *AppComputeDesc) { c.Reads[0] = flat.Texture(); c.ReadRanges[0] = LevelRange{Count: 1} }},
		{"a range on a texture", "ReadRanges: index 0 restricts a texture that is not a render target", func(c *AppComputeDesc) { c.Reads[0] = plain }},
		{"more levels than writes", "WriteLevels: 2 entries for 1 writes", func(c *AppComputeDesc) { c.WriteLevels = []int{3, 4} }},
		{"a negative level", `WriteLevels: index 0 selects level -1 of target "chain"; levels count from 0`, func(c *AppComputeDesc) { c.WriteLevels[0] = -1 }},
		{"a level past the chain", `WriteLevels: index 0 selects level 7 of target "chain", which has 7 levels`, func(c *AppComputeDesc) { c.WriteLevels[0] = 7 }},
		{"a level on a single-level target", `WriteLevels: index 0 selects level 1 of target "flat", which has one level`, func(c *AppComputeDesc) {
			c.Writes[0] = flat
			c.WriteLevels[0] = 1
		}},
		{"whole-chain read of a written chain", `Reads: index 0 reads levels [0,7) of target "chain", overlapping level 3 that Writes index 0 writes`, func(c *AppComputeDesc) { c.ReadRanges = nil }},
		{"an overlapping range", `Reads: index 0 reads levels [2,4) of target "chain", overlapping level 3 that Writes index 0 writes`, func(c *AppComputeDesc) { c.ReadRanges[0].Count = 2 }},
		{"the written level", `Reads: index 0 reads levels [3,4) of target "chain", overlapping level 3`, func(c *AppComputeDesc) { c.ReadRanges[0].Base = 3 }},
		{"a single-level target read and written", `target "flat" needs History when also written`, func(c *AppComputeDesc) {
			c.Reads[0], c.Writes[0], c.ReadRanges, c.WriteLevels = flat.Texture(), flat, nil, nil
		}},
	} {
		desc := base()
		desc.Reads, desc.ReadRanges = append([]*Texture(nil), desc.Reads...), append([]LevelRange(nil), desc.ReadRanges...)
		desc.Writes, desc.WriteLevels = append([]*RenderTarget(nil), desc.Writes...), append([]int(nil), desc.WriteLevels...)
		tc.edit(&desc)
		views := d.created["ImageView"]
		_, err := r.CreateAppCompute(desc)
		switch {
		case err == nil:
			t.Fatalf("%s: accepted", tc.name)
		case !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), `"reduce 2"`):
			t.Fatalf("%s: %v, want %q", tc.name, err, tc.want)
		case d.created["ImageView"] != views || len(r.appPasses) != 0:
			t.Fatalf("%s: a refusal created views or registered a pass", tc.name)
		}
		t.Logf("%s: %v", tc.name, err)
	}
	// The graphics pass takes the same read declaration.
	_, err = r.CreateAppPass(AppPassDesc{Name: "sample", Stage: StageBeforeScene, Target: flat, Fullscreen: true,
		Reads: []*Texture{chain.Texture()}, ReadRanges: []LevelRange{{Base: 6, Count: 2}}, Vert: computeCode(t), Frag: computeCode(t)})
	if err == nil || !strings.Contains(err.Error(), `app pass "sample": ReadRanges: index 0 selects levels [6,8) of target "chain", which has 7 levels`) {
		t.Fatalf("graphics range past the chain: %v", err)
	}
	if _, err := r.CreateAppCompute(base()); err != nil {
		t.Fatalf("a disjoint read and write of one chain: %v", err)
	}
	r.destroyAppResources()
	assertBalanced(t, d.resizeFakeDriver)
}

// chainFixture is a seed writing level 0 and six reductions, each reading
// level n and writing n+1, all through the real creation path.
func chainFixture(t *testing.T, history bool) (*Renderer, *mipProbe, *RenderTarget, []*AppCompute) {
	r, d := mipFixture()
	desc := chainDesc()
	desc.History = history
	chain, err := r.CreateRenderTarget(desc)
	if err != nil {
		t.Fatal(err)
	}
	seed, err := r.CreateAppCompute(AppComputeDesc{Name: "seed", Stage: StageBeforeScene, Comp: computeCode(t), Writes: []*RenderTarget{chain}})
	if err != nil {
		t.Fatal(err)
	}
	passes := []*AppCompute{seed}
	for n := range 6 {
		p, err := r.CreateAppCompute(reduction(t, chain, n))
		if err != nil {
			t.Fatal(err)
		}
		passes = append(passes, p)
	}
	return r, d, chain, passes
}

// The frame graph and the executor both confine each reduction's barriers to
// the two levels it touches: level n, read (a memory dependency on the
// sampled layout, because the group that wrote it can be skipped and so the
// graph cannot assume the write it published was this frame's), and level
// n+1, written (into General on entry, back to the sampled layout in its
// sampled-outputs node). No barrier names a level the dispatch does not use.
//
// Verified to fail: with the executor ignoring Barrier.Levels (every barrier
// recorded over the whole chain), it reports "reduce 0: recorded levels
// [0,7), want [0,1)".
func TestMipReductionBarrierRanges(t *testing.T) {
	r, d, chain, _ := chainFixture(t, false)
	f, err := r.buildAppGraph()
	if err != nil {
		t.Fatal(err)
	}
	r.frameGraph = f
	r.bindAppGraphImages()
	f.sizeScratch(&r.cmdScratch)
	f.frame = graphFrame{driver: d, scratch: &r.cmdScratch}
	id := f.targets[chain].write
	step := func(name string) framegraph.Step {
		for i, n := range f.nodes {
			if n.name == name {
				return f.plan.Steps[i]
			}
		}
		t.Fatalf("no node %q", name)
		return framegraph.Step{}
	}
	type want struct {
		level     int
		old, next core1_0.ImageLayout
	}
	sro, general := core1_0.ImageLayoutShaderReadOnlyOptimal, core1_0.ImageLayoutGeneral
	for n := range 6 {
		for node, wants := range map[string][]want{
			fmt.Sprintf("reduce %d", n):                 {{n, sro, sro}, {n + 1, sro, general}},
			fmt.Sprintf("reduce %d sampled outputs", n): {{n + 1, general, sro}},
		} {
			var mine []framegraph.Barrier
			for _, b := range step(node).Barriers {
				if b.Resource == id {
					mine = append(mine, b)
				}
			}
			if len(mine) != len(wants) {
				t.Fatalf("%s: chain barriers %+v, want %+v", node, mine, wants)
			}
			d.barriers = d.barriers[:0]
			if err := f.barriers(&f.frame, mine); err != nil {
				t.Fatal(err)
			}
			for i, w := range wants {
				b := mine[i]
				if b.Levels != (framegraph.LevelRange{Base: uint32(w.level), Count: 1}) || b.OldLayout != w.old || b.NewLayout != w.next {
					t.Fatalf("%s: barrier %+v, want level %d %v -> %v", node, b, w.level, w.old, w.next)
				}
				got := d.barriers[i].SubresourceRange
				if got.BaseMipLevel != w.level || got.LevelCount != 1 || d.barriers[i].Image != chain.color.images[0] {
					t.Fatalf("%s: recorded levels [%d,%d), want [%d,%d)", node, got.BaseMipLevel, got.BaseMipLevel+got.LevelCount, w.level, w.level+1)
				}
			}
		}
	}
	// The seed writes level 0 of a chain, so its barrier is level 0 alone.
	if b := step("seed").Barriers; len(b) != 1 || b[0].Levels != (framegraph.LevelRange{Count: 1}) {
		t.Fatalf("seed barriers %+v, want one on level 0", b)
	}
	if len(f.plan.FinalBarriers) != 0 {
		t.Fatalf("final barriers %+v: every level should be back at rest", f.plan.FinalBarriers)
	}
	// Recording every step's chain barriers allocates nothing per frame. The
	// fixture binds no engine image, so only the chain's are replayed.
	var steps [][]framegraph.Barrier
	for _, s := range f.plan.Steps {
		var mine []framegraph.Barrier
		for _, b := range s.Barriers {
			if b.Resource == id {
				mine = append(mine, b)
			}
		}
		steps = append(steps, mine)
	}
	d.barriers = make([]core1_0.ImageMemoryBarrier, 0, 4096)
	a := testing.AllocsPerRun(20, func() {
		d.barriers = d.barriers[:0]
		for _, s := range steps {
			if err := f.barriers(&f.frame, s); err != nil {
				panic(err)
			}
		}
	})
	if a != 0 {
		t.Fatalf("recording ranged barriers allocates %v per frame", a)
	}
	r.destroyAppResources()
	assertBalanced(t, d.resizeFakeDriver)
}

// The descriptors: each reduction reads level n through its own one-level
// view and stores through the level-(n+1) storage view, in the right history
// instance, and writing them allocates nothing.
func TestMipBindings(t *testing.T) {
	r, d, chain, passes := chainFixture(t, true)
	for frame := range 2 {
		chain.selectTexture(frame)
		for n, p := range passes[1:] {
			set := p.pass.sets[frame]
			if err := r.flushAppInputs(p.pass, set); err != nil {
				t.Fatal(err)
			}
			if err := r.flushComputeOutputs(p, set, frame); err != nil {
				t.Fatal(err)
			}
			read := d.bound[set][0]
			info := d.views[read.ImageView]
			if info.Image != chain.color.images[1-frame] || info.SubresourceRange.BaseMipLevel != n || info.SubresourceRange.LevelCount != 1 ||
				read.Sampler != chain.color.sampler || read.ImageLayout != core1_0.ImageLayoutShaderReadOnlyOptimal {
				t.Fatalf("frame %d reduce %d: read binds %+v of image %v", frame, n, info.SubresourceRange, info.Image)
			}
			store := d.bound[set][4]
			if store.ImageView != chain.color.levelView(frame, n+1) || store.ImageLayout != core1_0.ImageLayoutGeneral {
				t.Fatalf("frame %d reduce %d: storage binds the wrong view", frame, n)
			}
		}
	}
	// Six one-level ranges, one view each per history image; registered once
	// however many frames bind them.
	if len(chain.ranges) != 6 || len(chain.color.rangeViews) != 6 {
		t.Fatalf("%d ranges registered", len(chain.ranges))
	}
	p := passes[3]
	a := testing.AllocsPerRun(50, func() {
		if err := r.flushAppInputs(p.pass, p.pass.sets[0]); err != nil {
			panic(err)
		}
		if err := r.flushComputeOutputs(p, p.pass.sets[0], 0); err != nil {
			panic(err)
		}
	})
	if a != 0 {
		t.Fatalf("ranged and per-level descriptor writes allocate: %v", a)
	}
	r.destroyAppResources()
	assertBalanced(t, d.resizeFakeDriver)
}

// Disable and enable, rebuild the graph, rotate history: no view is created or
// replaced. Resize recreates the chain on both history images and every
// registered range, and a pass selecting a level the shorter chain lacks is
// refused at that rebuild, naming itself.
func TestMipLifetimeAndResize(t *testing.T) {
	r, d := mipFixture()
	desc := RenderTargetDesc{Name: "relative chain", Format: TargetRGBA16F, Scale: 0.5, MipLevels: 8, Storage: true, History: true, Filter: FilterLinear}
	chain, err := r.CreateRenderTarget(desc)
	if err != nil {
		t.Fatal(err)
	}
	var passes []*AppCompute
	for n := range 7 {
		p, err := r.CreateAppCompute(reduction(t, chain, n))
		if err != nil {
			t.Fatal(err)
		}
		passes = append(passes, p)
	}
	views := d.created["ImageView"]
	handles := append([]core1_0.ImageView(nil), chain.color.rangeViews[2]...)
	for _, on := range []bool{false, true} {
		passes[2].SetEnabled(on)
		if err := r.replaceAppGraph(false); err != nil {
			t.Fatal(err)
		}
		for frame := range 2 {
			chain.selectTexture(frame)
		}
	}
	if d.created["ImageView"] != views || chain.color.rangeViews[2][0] != handles[0] || chain.color.rangeViews[2][1] != handles[1] {
		t.Fatal("disable, enable, a graph rebuild or history rotation replaced a view")
	}

	resize := func(w, h int) error {
		r.releaseAppResizeTargets()
		r.sc.extent = core1_0.Extent2D{Width: w, Height: h}
		var undo rebuildUndo
		err := r.rebuildAppTargets(&undo)
		if err != nil {
			undo.unwind()
		}
		return err
	}
	// 640x360 at half scale is 320x180, nine levels; 400x200 is 200x100, eight.
	r.graphDirty = false
	if err := resize(400, 200); err != nil {
		t.Fatal(err)
	}
	if chain.Levels() != 8 || !r.graphDirty {
		t.Fatalf("Levels %d after a resize that keeps eight; graph dirty %v", chain.Levels(), r.graphDirty)
	}
	for i, img := range chain.color.images {
		if d.images[img].MipLevels != 8 || d.images[img].Extent.Width != 200 {
			t.Fatalf("history image %d: %+v", i, d.images[img])
		}
		if v := d.views[chain.color.rangeViews[2][i]]; v.Image != img || v.SubresourceRange.BaseMipLevel != 2 {
			t.Fatalf("history image %d: range view not rebuilt on the new image", i)
		}
	}
	if chain.color.rangeViews[2][0] == handles[0] {
		t.Fatal("a resize kept a view of the old image")
	}
	// 40x20 at half scale is 20x10, five levels: reduce 4 writes level 5.
	err = resize(40, 20)
	if err == nil || !strings.Contains(err.Error(), `app compute "reduce 4": WriteLevels: index 0 selects level 5 of target "relative chain", which has 5 levels after its target was rebuilt`) {
		t.Fatalf("a level the shorter chain lacks: %v", err)
	}
	t.Logf("refused at the rebuild: %v", err)
	for _, p := range passes[4:] {
		r.DestroyAppCompute(p)
	}
	if err := resize(40, 20); err != nil {
		t.Fatalf("with the passes past the chain gone: %v", err)
	}
	if chain.Levels() != 5 || chain.color.rangeViews[5] != nil {
		t.Fatalf("Levels %d; range [5,6) still has views", chain.Levels())
	}
	if _, err := r.buildAppGraph(); err != nil {
		t.Fatalf("graph over the shorter chain: %v", err)
	}
	r.DestroyRenderTarget(chain)
	if len(r.appPasses) != 0 {
		t.Fatal("destroying the chain left its writers")
	}
	if err := r.replaceAppGraph(false); err != nil {
		t.Fatal(err)
	}
	r.flushAllDeferred()
	r.destroyAppResources()
	assertBalanced(t, d.resizeFakeDriver)
}

// Failure at every view the chain adds -- the whole-chain view, each level's
// storage view, each range view -- unwinds what was made, at creation and at
// a resize.
func TestMipCreationUnwinds(t *testing.T) {
	create := func(d *mipProbe) (*Renderer, error) {
		r, _ := mipFixtureWith(d)
		desc := chainDesc()
		desc.History, desc.Scale, desc.Width, desc.Height = true, 0.1, 0, 0
		chain, err := r.CreateRenderTarget(desc)
		if err != nil {
			return r, err
		}
		for n := range 2 {
			if _, err := r.CreateAppCompute(reduction(t, chain, n)); err != nil {
				return r, err
			}
		}
		r.releaseAppResizeTargets()
		var undo rebuildUndo
		if err := r.rebuildAppTargets(&undo); err != nil {
			undo.unwind()
			return r, err
		}
		return r, nil
	}
	balance := func(tb testing.TB, d *resizeFakeDriver) { assertBalanced(tb, d) }
	control := newMipProbe()
	r, err := create(control)
	if err != nil {
		t.Fatal(err)
	}
	r.destroyAppResources()
	balance(t, control.resizeFakeDriver)
	sites := control.calls["CreateImageView"]
	for at := 1; at <= sites; at++ {
		d := newMipProbe()
		d.failCall, d.failAt = "CreateImageView", at
		r, err := create(d)
		if !errors.Is(err, errInjected) {
			t.Fatalf("CreateImageView/%d: %v", at, err)
		}
		r.destroyAppResources()
		balance(t, d.resizeFakeDriver)
	}
	t.Logf("all %d view creation sites unwind without leaks", sites)
	control.destroyed["ImageView"]--
	captured := &capturingT{TB: t}
	balance(captured, control.resizeFakeDriver)
	if !captured.failed {
		t.Fatal("balance meta-check missed a leaked view")
	}
	t.Log("balance meta-check detects one missing view destroy")
}
