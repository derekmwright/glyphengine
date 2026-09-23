package framegraph

import (
	"slices"
	"testing"

	"github.com/vkngwrapper/core/v3/core1_0"
)

func copyPass(d *RenderPassDesc) *RenderPassDesc {
	out := *d
	out.Attachments = slices.Clone(d.Attachments)
	out.Color, out.Resolve = slices.Clone(d.Color), slices.Clone(d.Resolve)
	out.Dependencies, out.Clears = slices.Clone(d.Dependencies), slices.Clone(d.Clears)
	return &out
}

// Verified to fail: adding LoadOp to compatibility comparison makes the load
// case report "compatible: got false; want true".
func TestCompatibleIgnoresOpsAndLayouts(t *testing.T) {
	a := mustBuild(t, waterGraph(true, false)).Steps[2].RenderPass
	for _, tc := range []struct {
		name string
		edit func(*RenderPassDesc)
		want bool
	}{
		{"load", func(b *RenderPassDesc) { b.Attachments[0].LoadOp = core1_0.AttachmentLoadOpClear }, true},
		{"store", func(b *RenderPassDesc) { b.Attachments[0].StoreOp = core1_0.AttachmentStoreOpStore }, true},
		{"stencil", func(b *RenderPassDesc) { b.Attachments[0].StencilLoadOp = core1_0.AttachmentLoadOpClear }, true},
		{"initial", func(b *RenderPassDesc) { b.Attachments[0].InitialLayout = core1_0.ImageLayoutGeneral }, true},
		{"final", func(b *RenderPassDesc) { b.Attachments[0].FinalLayout = core1_0.ImageLayoutGeneral }, true},
		{"subpass layout", func(b *RenderPassDesc) { b.Attachments[0].SubpassLayout = core1_0.ImageLayoutGeneral }, true},
		{"resource", func(b *RenderPassDesc) { b.Attachments[0].Resource = 999 }, true},
		{"extent", func(b *RenderPassDesc) { b.Extent.Scale = 0.5 }, true},
		{"format", func(b *RenderPassDesc) { b.Attachments[0].Format = core1_0.FormatB8G8R8A8SRGB }, false},
		{"samples", func(b *RenderPassDesc) { b.Attachments[0].Samples = core1_0.Samples2 }, false},
		{"count", func(b *RenderPassDesc) { b.Attachments = b.Attachments[:1] }, false},
		{"color", func(b *RenderPassDesc) { b.Color[0] = 1 }, false},
		{"resolve", func(b *RenderPassDesc) { b.Resolve[0] = -1 }, false},
		{"depth", func(b *RenderPassDesc) { b.Depth = -1 }, false},
		{"dependencies", func(b *RenderPassDesc) { b.Dependencies[0].SrcStageMask = 0 }, false},
		{"flags", func(b *RenderPassDesc) { b.Dependencies[0].DependencyFlags = core1_0.DependencyByRegion }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := copyPass(a)
			tc.edit(b)
			equal(t, "compatible", Compatible(a, b), tc.want)
			equal(t, "symmetric", Compatible(b, a), tc.want)
		})
	}
	equal(t, "both nil", Compatible(nil, nil), true)
	equal(t, "one nil", Compatible(a, nil), false)
}

// Verified to fail: encoding zero instead of LoadOp reports "different
// render-pass objects share a key" for the load case.
func TestKeyDistinguishesOpsAndLayouts(t *testing.T) {
	a := mustBuild(t, waterGraph(true, false)).Steps[2].RenderPass
	for _, tc := range []struct {
		name string
		edit func(*RenderPassDesc)
	}{
		{"load", func(b *RenderPassDesc) { b.Attachments[0].LoadOp = core1_0.AttachmentLoadOpClear }},
		{"store", func(b *RenderPassDesc) { b.Attachments[0].StoreOp = core1_0.AttachmentStoreOpStore }},
		{"stencil load", func(b *RenderPassDesc) { b.Attachments[0].StencilLoadOp = core1_0.AttachmentLoadOpLoad }},
		{"stencil store", func(b *RenderPassDesc) { b.Attachments[0].StencilStoreOp = core1_0.AttachmentStoreOpStore }},
		{"initial", func(b *RenderPassDesc) { b.Attachments[0].InitialLayout = core1_0.ImageLayoutGeneral }},
		{"final", func(b *RenderPassDesc) { b.Attachments[0].FinalLayout = core1_0.ImageLayoutGeneral }},
		{"subpass", func(b *RenderPassDesc) { b.Attachments[0].SubpassLayout = core1_0.ImageLayoutGeneral }},
		{"format", func(b *RenderPassDesc) { b.Attachments[0].Format = core1_0.FormatB8G8R8A8SRGB }},
		{"samples", func(b *RenderPassDesc) { b.Attachments[0].Samples = core1_0.Samples2 }},
		{"color", func(b *RenderPassDesc) { b.Color[0] = 1 }},
		{"resolve", func(b *RenderPassDesc) { b.Resolve[0] = -1 }},
		{"depth", func(b *RenderPassDesc) { b.Depth = -1 }},
		{"dependency source", func(b *RenderPassDesc) { b.Dependencies[0].SrcSubpass = 0 }},
		{"dependency dest", func(b *RenderPassDesc) { b.Dependencies[0].DstSubpass = 1 }},
		{"source stage", func(b *RenderPassDesc) { b.Dependencies[0].SrcStageMask = 0 }},
		{"dest stage", func(b *RenderPassDesc) { b.Dependencies[0].DstStageMask = 0 }},
		{"source access", func(b *RenderPassDesc) { b.Dependencies[0].SrcAccessMask = 0 }},
		{"dest access", func(b *RenderPassDesc) { b.Dependencies[0].DstAccessMask = 0 }},
		{"flags", func(b *RenderPassDesc) { b.Dependencies[0].DependencyFlags = core1_0.DependencyByRegion }},
		{"dependency count", func(b *RenderPassDesc) { b.Dependencies = nil }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := copyPass(a)
			tc.edit(b)
			if a.Key() == b.Key() {
				t.Fatal("different render-pass objects share a key")
			}
		})
	}
	b := copyPass(a)
	b.Attachments[0].Resource = 500
	b.Extent.Scale = 0.5
	b.Clears[0].Depth = 1
	equal(t, "framebuffer/clear data excluded", b.Key(), a.Key())
	cache := map[RenderPassKey]int{a.Key(): 4}
	equal(t, "cache lookup", cache[b.Key()], 4)
	var absent *RenderPassDesc
	if absent.Key() == (&RenderPassDesc{}).Key() {
		t.Fatal("nil and empty descriptions collide")
	}
}

// Verified to fail: replacing Ceil with Floor produces 640x360 instead of
// 641x361 for clouds. Separately shifting colour references by one reports
// [1 2] instead of [0 1], even though the attachment slice itself is unchanged.
func TestExtentRoundingAndAttachmentOrder(t *testing.T) {
	for _, tc := range []struct {
		e           Extent
		input, want [2]uint32
	}{
		{Extent{Scale: 0.5}, [2]uint32{1281, 721}, [2]uint32{640, 360}},
		{Extent{Scale: 0.5, RoundUp: true}, [2]uint32{1281, 721}, [2]uint32{641, 361}},
		{Extent{Scale: 1.0 / 32}, [2]uint32{1280, 720}, [2]uint32{40, 22}},
		{Extent{Scale: 1.0 / 32}, [2]uint32{1, 1}, [2]uint32{1, 1}},
		{Extent{Scale: 0.5, Fixed: [2]uint32{8, 9}}, [2]uint32{1280, 720}, [2]uint32{8, 9}},
	} {
		equal(t, "size", tc.e.Size(tc.input[0], tc.input[1]), tc.want)
	}
	g := New()
	g.AddImage(colorImage("resolve to zero"))
	depth, first, second := depthImage("depth"), colorImage("first"), colorImage("second")
	depth.Samples, first.Samples, second.Samples = core1_0.Samples4, core1_0.Samples4, core1_0.Samples4
	g.AddImage(depth)
	g.AddImage(first)
	g.AddImage(second)
	g.AddNode(Node{Name: "MRT", Kind: Graphics, Uses: []Use{
		{Resource: 1, Access: DepthWrite, Clear: &Clear{Depth: 0}},
		{Resource: 2, Access: ColorWrite, Clear: &Clear{Color: [4]float32{1, 2, 3, 4}}, HasResolve: true, ResolveTo: 0},
		{Resource: 3, Access: ColorWrite},
	}})
	p := mustBuild(t, g)
	rp := p.Steps[0].RenderPass
	ids := make([]ResourceID, len(rp.Attachments))
	for i, a := range rp.Attachments {
		ids[i] = a.Resource
	}
	equal(t, "attachment order", ids, []ResourceID{2, 3, 0, 1})
	equal(t, "color refs", rp.Color, []int{0, 1})
	equal(t, "resolve refs", rp.Resolve, []int{2, -1})
	equal(t, "depth ref", rp.Depth, 3)
	equal(t, "clear values", rp.Clears, []Clear{{Color: [4]float32{1, 2, 3, 4}}, {}, {}, {}})
	equal(t, "clear op", rp.Attachments[0].LoadOp, core1_0.AttachmentLoadOpClear)
	equal(t, "depth clear op", rp.Attachments[3].LoadOp, core1_0.AttachmentLoadOpClear)
}

// Verified to fail: retaining the caller's dependency slice changes the UI
// source stage from 1152 to 0 after the caller reuses that slice.
func TestDependencyOverridesAndDeclarationOwnership(t *testing.T) {
	g := New()
	g.AddImage(colorImage("UI"))
	deps := []core1_0.SubpassDependency{{
		SrcSubpass: core1_0.SubpassExternal, DstSubpass: 0,
		SrcStageMask:  core1_0.PipelineStageFragmentShader | core1_0.PipelineStageColorAttachmentOutput,
		DstStageMask:  core1_0.PipelineStageColorAttachmentOutput,
		SrcAccessMask: core1_0.AccessShaderRead | core1_0.AccessColorAttachmentWrite,
		DstAccessMask: core1_0.AccessColorAttachmentWrite | core1_0.AccessColorAttachmentRead,
	}}
	clear := &Clear{Color: [4]float32{0, 0, 0, 0}}
	uses := []Use{{Access: ColorWrite, Clear: clear}}
	g.AddNode(Node{Name: "UI", Kind: Graphics, Dependencies: deps, Uses: uses})
	g.AddNode(Node{Name: "composite", Kind: Graphics, Dependencies: []core1_0.SubpassDependency{}, Uses: []Use{{Access: SampledRead}}})
	wantDeps := slices.Clone(deps)
	deps[0].SrcStageMask = 0
	clear.Color[3] = 1
	uses[0].Access = SampledRead
	p := mustBuild(t, g)
	equal(t, "owned dependency", p.Steps[0].RenderPass.Dependencies, wantDeps)
	equal(t, "transparent clear", p.Steps[0].RenderPass.Clears[0], Clear{})
	equal(t, "UI layout", p.Steps[0].RenderPass.Attachments[0].FinalLayout, core1_0.ImageLayoutShaderReadOnlyOptimal)
	equal(t, "explicit empty override", len(p.Steps[1].RenderPass.Dependencies), 0)
	equal(t, "empty dependency requires visibility barrier", len(p.Steps[1].Barriers), 1)
}

// Verified to fail: ignoring AttachmentOrder produces [3 0 1] instead of
// [3 1 0]. Retaining caller-owned order memory separately makes the rebuilt
// graph reject resource #999 after the caller reuses its declaration.
func TestAttachmentOrderPreservesLegacyCompatibility(t *testing.T) {
	g := waterGraph(true, false)
	g.nodes[0].Kind = Graphics
	g.nodes[0].Dependencies = sceneDependency()
	g.nodes[0].AttachmentOrder = []ResourceID{3, 1, 0}
	g.nodes[0].Uses[0].Clear = &Clear{Color: [4]float32{1, 0, 0, 1}}
	g.nodes[0].Uses[1].Clear = &Clear{Depth: 0, Stencil: 7}
	p := mustBuild(t, g)
	scene, water := p.Steps[0].RenderPass, p.Steps[2].RenderPass
	for _, rp := range []*RenderPassDesc{scene, water} {
		equal(t, "legacy attachment order", []ResourceID{rp.Attachments[0].Resource, rp.Attachments[1].Resource, rp.Attachments[2].Resource}, []ResourceID{3, 1, 0})
		equal(t, "legacy depth reference", rp.Depth, 1)
		equal(t, "legacy resolve reference", rp.Resolve, []int{2})
	}
	equal(t, "scene pipeline works in water", Compatible(scene, water), true)
	equal(t, "reordered clears", scene.Clears, []Clear{{Color: [4]float32{1, 0, 0, 1}}, {Stencil: 7}, {}})
	// An override is a declaration, not caller-owned memory used during Build.
	owned := New()
	for _, d := range g.images {
		owned.AddImage(d)
	}
	for _, n := range g.nodes {
		owned.AddNode(n)
	}
	g.nodes[0].AttachmentOrder[0] = 999
	equal(t, "owned order", mustBuild(t, owned), p)
}
