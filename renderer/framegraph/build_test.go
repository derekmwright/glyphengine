package framegraph

import (
	"errors"
	"fmt"
	"math"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/vkngwrapper/core/v3/core1_0"
)

func colorImage(name string) ImageDesc {
	return ImageDesc{Name: name, Format: core1_0.FormatR16G16B16A16SignedFloat,
		Extent: Extent{Scale: 1}, Samples: core1_0.Samples1, Aspect: core1_0.ImageAspectColor}
}

func depthImage(name string) ImageDesc {
	d := colorImage(name)
	d.Format, d.Aspect = core1_0.FormatD32SignedFloat, core1_0.ImageAspectDepth
	return d
}

// Verified break: replacing the explicit exit with the resting layout makes
// explicit=true report OldLayout ShaderReadOnly (5), wanted DepthAttachment (3).
// The omitted-override control continues to use ShaderReadOnly in the same graph.
func TestLegacyFinalLayout(t *testing.T) {
	for _, explicit := range []bool{false, true} {
		t.Run(fmt.Sprintf("explicit=%v", explicit), func(t *testing.T) {
			g := New()
			depth := g.AddImage(depthImage("legacy scene depth"))
			u := Use{Resource: depth, Access: DepthWrite}
			wantOld := core1_0.ImageLayoutShaderReadOnlyOptimal
			if explicit {
				u.FinalLayout = core1_0.ImageLayoutDepthStencilAttachmentOptimal
				wantOld = u.FinalLayout
			}
			g.AddNode(Node{Name: "scene", Kind: Legacy, Uses: []Use{u}})
			g.AddNode(Node{Name: "depth resolve", Kind: Graphics, Uses: []Use{{Resource: depth, Access: SampledRead}}})
			p := mustBuild(t, g)
			equal(t, "resting layout", p.Resources[depth].Resting, core1_0.ImageLayoutShaderReadOnlyOptimal)
			equal(t, "sampling barriers", len(p.Steps[1].Barriers), 1)
			b := p.Steps[1].Barriers[0]
			equal(t, "barrier old layout", b.OldLayout, wantOld)
			equal(t, "barrier new layout", b.NewLayout, core1_0.ImageLayoutShaderReadOnlyOptimal)
			t.Logf("explicit=%v: resting=%v, barrier %v -> %v", explicit, p.Resources[depth].Resting, b.OldLayout, b.NewLayout)
		})
	}
}

func mustBuild(t *testing.T, g *Graph) *Plan {
	t.Helper()
	p, err := g.Build()
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func equal[T any](t *testing.T, label string, got, want T) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%s: got %#v; want %#v", label, got, want)
	}
}

func errorContains(t *testing.T, g *Graph, node, resource, rule string) {
	t.Helper()
	p, err := g.Build()
	if err == nil {
		t.Fatalf("expected %s error; got plan %#v", rule, p)
	}
	var buildErr *BuildError
	if !errors.As(err, &buildErr) || p != nil {
		t.Fatalf("expected contextual BuildError and nil plan: %v, %v", p, err)
	}
	for _, text := range []string{node, resource, rule} {
		if !strings.Contains(err.Error(), text) {
			t.Fatalf("error %q does not contain %q", err, text)
		}
	}
}

func sceneDependency() []core1_0.SubpassDependency {
	// Literal copied from sceneEntryDependency, independent of derivation.
	return []core1_0.SubpassDependency{{
		SrcSubpass: core1_0.SubpassExternal, DstSubpass: 0,
		SrcStageMask: core1_0.PipelineStageTransfer | core1_0.PipelineStageColorAttachmentOutput |
			core1_0.PipelineStageEarlyFragmentTests | core1_0.PipelineStageLateFragmentTests,
		DstStageMask:  core1_0.PipelineStageFragmentShader | core1_0.PipelineStageColorAttachmentOutput | core1_0.PipelineStageEarlyFragmentTests,
		SrcAccessMask: core1_0.AccessTransferWrite | core1_0.AccessColorAttachmentWrite | core1_0.AccessDepthStencilAttachmentWrite,
		DstAccessMask: core1_0.AccessShaderRead | core1_0.AccessColorAttachmentRead | core1_0.AccessColorAttachmentWrite |
			core1_0.AccessDepthStencilAttachmentRead | core1_0.AccessDepthStencilAttachmentWrite,
	}, {
		SrcSubpass: 0, DstSubpass: core1_0.SubpassExternal,
		SrcStageMask: core1_0.PipelineStageColorAttachmentOutput |
			core1_0.PipelineStageEarlyFragmentTests | core1_0.PipelineStageLateFragmentTests,
		DstStageMask: core1_0.PipelineStageFragmentShader | core1_0.PipelineStageColorAttachmentOutput |
			core1_0.PipelineStageEarlyFragmentTests | core1_0.PipelineStageLateFragmentTests | core1_0.PipelineStageTransfer,
		SrcAccessMask: core1_0.AccessColorAttachmentWrite | core1_0.AccessDepthStencilAttachmentWrite,
		DstAccessMask: core1_0.AccessShaderRead | core1_0.AccessColorAttachmentRead | core1_0.AccessColorAttachmentWrite |
			core1_0.AccessDepthStencilAttachmentRead | core1_0.AccessDepthStencilAttachmentWrite | core1_0.AccessTransferRead,
	}}
}

func waterGraph(msaa bool, optional bool) *Graph {
	g := New()
	hdr := colorImage("HDR")
	hdr.Imported, hdr.InitialLayout = true, core1_0.ImageLayoutShaderReadOnlyOptimal
	g.AddImage(hdr) // 0
	depth := depthImage("depth")
	depth.Imported, depth.InitialLayout = true, core1_0.ImageLayoutDepthStencilAttachmentOptimal
	if msaa {
		depth.Samples = core1_0.Samples4
	}
	g.AddImage(depth)                     // 1
	g.AddImage(colorImage("sceneColour")) // 2
	color := ResourceID(0)
	if msaa {
		d := colorImage("MSAA")
		d.Samples, d.Imported, d.InitialLayout = core1_0.Samples4, true, core1_0.ImageLayoutColorAttachmentOptimal
		color = g.AddImage(d)
	}
	g.AddNode(Node{Name: "scene", Kind: Legacy, Timed: true, Uses: []Use{
		{Resource: color, Access: ColorWrite, HasResolve: msaa, ResolveTo: 0}, {Resource: 1, Access: DepthWrite},
	}})
	g.AddNode(Node{Name: "copy", Kind: Transfer, Uses: []Use{{Resource: 0, Access: TransferSrc}, {Resource: 2, Access: TransferDst}}})
	var order []ResourceID
	if msaa {
		order = []ResourceID{color, 1, 0}
	}
	g.AddNode(Node{Name: "water", Kind: Graphics, Optional: optional, Timed: true, Dependencies: sceneDependency(), AttachmentOrder: order, Uses: []Use{
		{Resource: color, Access: ColorLoadWrite, HasResolve: msaa, ResolveTo: 0},
		{Resource: 1, Access: DepthLoadWrite}, {Resource: 2, Access: SampledRead},
	}})
	g.AddNode(Node{Name: "sample HDR", Kind: Graphics, Uses: []Use{{Resource: 0, Access: SampledRead}}})
	return g
}

// Verified to fail: changing the discarded copy destination's source stage to
// Transfer reports 4096 instead of 1024 in the second barrier, with and without MSAA.
func TestWaterCopyBarriersMatchHandWrittenOnes(t *testing.T) {
	for _, msaa := range []bool{false, true} {
		t.Run(fmt.Sprintf("MSAA=%v", msaa), func(t *testing.T) {
			p := mustBuild(t, waterGraph(msaa, false))
			equal(t, "copy barriers", p.Steps[1].Barriers, []Barrier{
				{Resource: 0, SrcStage: core1_0.PipelineStageColorAttachmentOutput, DstStage: core1_0.PipelineStageTransfer,
					SrcAccess: core1_0.AccessColorAttachmentWrite, DstAccess: core1_0.AccessTransferRead,
					OldLayout: core1_0.ImageLayoutShaderReadOnlyOptimal, NewLayout: core1_0.ImageLayoutTransferSrcOptimal},
				{Resource: 2, SrcStage: core1_0.PipelineStageColorAttachmentOutput, DstStage: core1_0.PipelineStageTransfer,
					SrcAccess: 0, DstAccess: core1_0.AccessTransferWrite,
					OldLayout: core1_0.ImageLayoutUndefined, NewLayout: core1_0.ImageLayoutTransferDstOptimal},
			})
			equal(t, "trailing copy barrier", p.Steps[2].Barriers, []Barrier{{Resource: 2,
				SrcStage: core1_0.PipelineStageTransfer, DstStage: core1_0.PipelineStageFragmentShader,
				SrcAccess: core1_0.AccessTransferWrite, DstAccess: core1_0.AccessShaderRead,
				OldLayout: core1_0.ImageLayoutTransferDstOptimal, NewLayout: core1_0.ImageLayoutShaderReadOnlyOptimal}})
			rp := p.Steps[2].RenderPass
			equal(t, "dependencies", rp.Dependencies, sceneDependency())
			colorInitial, colorFinal := core1_0.ImageLayoutTransferSrcOptimal, core1_0.ImageLayoutShaderReadOnlyOptimal
			colorStore := core1_0.AttachmentStoreOpStore
			depthIndex := 1
			if msaa {
				colorInitial, colorFinal = core1_0.ImageLayoutColorAttachmentOptimal, core1_0.ImageLayoutColorAttachmentOptimal
				colorStore = core1_0.AttachmentStoreOpDontCare
				equal(t, "resolve index", rp.Resolve, []int{2})
				equal(t, "resolve discard", rp.Attachments[2].InitialLayout, core1_0.ImageLayoutUndefined)
				equal(t, "resolve final", rp.Attachments[2].FinalLayout, core1_0.ImageLayoutShaderReadOnlyOptimal)
				equal(t, "resolve load", rp.Attachments[2].LoadOp, core1_0.AttachmentLoadOpDontCare)
			}
			for _, tc := range []struct {
				index          int
				initial, final core1_0.ImageLayout
				store          core1_0.AttachmentStoreOp
			}{
				{0, colorInitial, colorFinal, colorStore},
				{depthIndex, core1_0.ImageLayoutDepthStencilAttachmentOptimal, core1_0.ImageLayoutDepthStencilAttachmentOptimal, core1_0.AttachmentStoreOpDontCare},
			} {
				a := rp.Attachments[tc.index]
				equal(t, "initial", a.InitialLayout, tc.initial)
				equal(t, "final", a.FinalLayout, tc.final)
				equal(t, "load", a.LoadOp, core1_0.AttachmentLoadOpLoad)
				equal(t, "store", a.StoreOp, tc.store)
			}
			equal(t, "depth index", rp.Depth, depthIndex)
			equal(t, "final barriers", len(p.FinalBarriers), 0)
			equal(t, "sample barriers", len(p.Steps[3].Barriers), 0)
		})
	}
	// Copy + draw can be skipped together by a caller, but neither split step
	// is neutral on its own. Accepting this would leave an incorrect next layout.
	errorContains(t, waterGraph(false, true), "water", "HDR", "layout-neutral")
}

func bloomGraph(levels int) *Graph {
	g := New()
	d := colorImage("scene")
	d.Imported, d.InitialLayout = true, core1_0.ImageLayoutShaderReadOnlyOptimal
	scene := g.AddImage(d)
	g.AddNode(Node{Name: "scene", Kind: Legacy, Uses: []Use{{Resource: scene, Access: ColorWrite}}})
	for i := 0; i < levels; i++ {
		d = colorImage(fmt.Sprintf("bloom %d", i))
		d.Extent.Scale = float32(math.Pow(0.5, float64(i+1)))
		d.Persistent = true // Primed targets may be bound even when the chain is off.
		id := g.AddImage(d)
		g.AddNode(Node{Name: fmt.Sprintf("down %d", i), Kind: Graphics, Uses: []Use{
			{Resource: id - 1, Access: SampledRead}, {Resource: id, Access: ColorWrite, Discard: true},
		}})
	}
	for i := levels; i > 1; i-- {
		g.AddNode(Node{Name: fmt.Sprintf("up %d", i), Kind: Graphics, Uses: []Use{
			{Resource: ResourceID(i), Access: SampledRead}, {Resource: ResourceID(i - 1), Access: ColorLoadWrite},
		}})
	}
	g.AddNode(Node{Name: "composite", Kind: Graphics, Uses: []Use{{Resource: 1, Access: SampledRead}}})
	return g
}

// Verified to fail: removing Discard's initial-layout selection reports
// ShaderReadOnly (5) instead of Undefined (0) on the first downsample attachment.
// Removing incoming colour-write availability separately produces one barrier
// instead of zero, proving the no-barrier assertion depends on synchronization.
// Removing outgoingDependency on 2026-09-24 fails the pinned pair: one
// dependency instead of two (exit stages 1024 -> 1152, accesses 256 -> 416).
func TestBloomChainNeedsNoBarriers(t *testing.T) {
	// Five targets is today's prefilter + four down + four up; six targets also
	// checks a prefilter + five down + five up without baking in a chain length.
	for _, levels := range []int{5, 6} {
		p := mustBuild(t, bloomGraph(levels))
		for i, step := range p.Steps {
			equal(t, "barrier count", len(step.Barriers), 0)
			if i == 0 || i == len(p.Steps)-1 {
				continue
			}
			rp := step.RenderPass
			load, initial := core1_0.AttachmentLoadOpDontCare, core1_0.ImageLayoutUndefined
			if i > levels {
				load, initial = core1_0.AttachmentLoadOpLoad, core1_0.ImageLayoutShaderReadOnlyOptimal
			}
			equal(t, "load", rp.Attachments[0].LoadOp, load)
			equal(t, "initial", rp.Attachments[0].InitialLayout, initial)
			equal(t, "final", rp.Attachments[0].FinalLayout, core1_0.ImageLayoutShaderReadOnlyOptimal)
			equal(t, "dependency", rp.Dependencies, []core1_0.SubpassDependency{{
				SrcSubpass: core1_0.SubpassExternal, DstSubpass: 0,
				SrcStageMask:  core1_0.PipelineStageColorAttachmentOutput | core1_0.PipelineStageFragmentShader,
				DstStageMask:  core1_0.PipelineStageFragmentShader | core1_0.PipelineStageColorAttachmentOutput,
				SrcAccessMask: core1_0.AccessColorAttachmentWrite | core1_0.AccessShaderRead,
				DstAccessMask: core1_0.AccessShaderRead | core1_0.AccessColorAttachmentRead | core1_0.AccessColorAttachmentWrite,
			}, {
				SrcSubpass: 0, DstSubpass: core1_0.SubpassExternal,
				SrcStageMask:  core1_0.PipelineStageColorAttachmentOutput,
				DstStageMask:  core1_0.PipelineStageFragmentShader | core1_0.PipelineStageColorAttachmentOutput,
				SrcAccessMask: core1_0.AccessColorAttachmentWrite,
				DstAccessMask: core1_0.AccessShaderRead | core1_0.AccessColorAttachmentRead | core1_0.AccessColorAttachmentWrite,
			}})
		}
		equal(t, "final barriers", len(p.FinalBarriers), 0)
	}
}

// Verified to fail: using General for presented resources reports final layout
// 1 instead of 1000001002. Removing the last-use guard also fails all three tails.
// Removing outgoingDependency also fails the pinned pair: the exit with stages
// 1024 -> 9216 and accesses 256 -> 384 is absent (2026-09-24).
func TestTonemapPresentsSwapchain(t *testing.T) {
	g := New()
	d := colorImage("swapchain")
	d.Imported, d.Format = true, core1_0.FormatB8G8R8A8SRGB
	id := g.AddImage(d)
	deps := []core1_0.SubpassDependency{{SrcSubpass: core1_0.SubpassExternal, DstSubpass: 0,
		SrcStageMask: core1_0.PipelineStageColorAttachmentOutput, DstStageMask: core1_0.PipelineStageFragmentShader | core1_0.PipelineStageColorAttachmentOutput,
		SrcAccessMask: core1_0.AccessColorAttachmentWrite, DstAccessMask: core1_0.AccessShaderRead | core1_0.AccessColorAttachmentRead | core1_0.AccessColorAttachmentWrite},
		{SrcSubpass: 0, DstSubpass: core1_0.SubpassExternal,
			SrcStageMask: core1_0.PipelineStageColorAttachmentOutput, DstStageMask: core1_0.PipelineStageColorAttachmentOutput | core1_0.PipelineStageBottomOfPipe,
			SrcAccessMask: core1_0.AccessColorAttachmentWrite, DstAccessMask: core1_0.AccessColorAttachmentRead | core1_0.AccessColorAttachmentWrite}}
	g.AddNode(Node{Name: "tonemap", Kind: Graphics, Uses: []Use{{Resource: id, Access: ColorWrite}}})
	g.AddNode(Node{Name: "present", Kind: Legacy, Uses: []Use{{Resource: id, Access: Present}}})
	p := mustBuild(t, g)
	equal(t, "swapchain final", p.Steps[0].RenderPass.Attachments[0].FinalLayout, core1_0.ImageLayout(1000001002))
	equal(t, "dependencies", p.Steps[0].RenderPass.Dependencies, deps)
	equal(t, "present barrier count", len(p.Steps[1].Barriers), 0)
	equal(t, "resting", p.Resources[0].Resting, ImageLayoutPresentSrc)
	for _, access := range []Access{Present, SampledRead, ColorWrite} {
		t.Run(fmt.Sprint(access), func(t *testing.T) {
			bad := *g
			bad.nodes = slices.Clone(g.nodes)
			bad.AddNode(Node{Name: "after present", Kind: Graphics, Uses: []Use{{Resource: id, Access: access}}})
			errorContains(t, &bad, "after present", "swapchain", "last use")
		})
	}
}

// Verified to fail: disabling the neutrality guard returns a plan instead of
// the expected layout-neutral error for each of the four node kinds.
func TestOptionalNodeMustBeLayoutNeutral(t *testing.T) {
	for _, kind := range []NodeKind{Graphics, Compute, Transfer, Legacy} {
		t.Run(fmt.Sprint(kind), func(t *testing.T) {
			g := New()
			g.AddImage(colorImage("target"))
			access := ColorWrite
			if kind == Compute {
				access = StorageWrite
			}
			if kind == Transfer {
				access = TransferDst
			}
			g.AddNode(Node{Name: "optional", Kind: kind, Optional: true, Uses: []Use{{Resource: 0, Access: access}}})
			errorContains(t, g, "optional", "target", "layout-neutral")
		})
	}
	g := New()
	d := colorImage("history")
	d.Persistent = true
	g.AddImage(d)
	g.AddNode(Node{Name: "optional", Kind: Graphics, Optional: true, Uses: []Use{{Resource: 0, Access: ColorWrite}}})
	g.AddNode(Node{Name: "reader", Kind: Graphics, Uses: []Use{{Resource: 0, Access: SampledRead}}})
	mustBuild(t, g)
}

// Verified to fail: disabling the content-read guard accepts all seven initial
// read forms and the opaque reader, reporting "expected read before error".
func TestTransientReadBeforeWriteIsAnError(t *testing.T) {
	for _, a := range []Access{SampledRead, StorageRead, StorageReadWrite, ColorLoadWrite, DepthLoadWrite, TransferSrc, Present} {
		t.Run(fmt.Sprint(a), func(t *testing.T) {
			g := New()
			d := colorImage("unwritten")
			if a == DepthLoadWrite {
				d = depthImage("unwritten")
			}
			if a == Present {
				d.Imported = true
			}
			g.AddImage(d)
			g.AddNode(Node{Name: "reader", Kind: Graphics, Uses: []Use{{Resource: 0, Access: a}}})
			errorContains(t, g, "reader", "unwritten", "read before")
		})
	}
	// An import with a layout but undefined contents is expressible with an
	// earlier opaque writer; optional writes alone never establish contents.
	g := New()
	g.AddImage(colorImage("unwritten"))
	g.AddNode(Node{Name: "reader", Kind: Legacy, Uses: []Use{{Resource: 0, Access: SampledRead}}})
	errorContains(t, g, "reader", "unwritten", "read before")
}

// Verified to fail: forcing Prime false reports "prime: got false; want true".
func TestPersistentHistoryPrimes(t *testing.T) {
	for _, imported := range []bool{false, true} {
		g := New()
		d := colorImage("history")
		d.Persistent, d.Imported = true, imported
		if imported {
			d.InitialLayout = core1_0.ImageLayoutShaderReadOnlyOptimal
		}
		g.AddImage(d)
		g.AddNode(Node{Name: "history read", Kind: Graphics, Uses: []Use{{Resource: 0, Access: SampledRead}}})
		g.AddNode(Node{Name: "history write", Kind: Graphics, Uses: []Use{{Resource: 0, Access: ColorWrite}}})
		p := mustBuild(t, g)
		equal(t, "prime", p.Resources[0].Prime, !imported)
		equal(t, "resting", p.Resources[0].Resting, core1_0.ImageLayoutShaderReadOnlyOptimal)
		equal(t, "history initial", p.Steps[1].RenderPass.Attachments[0].InitialLayout, core1_0.ImageLayoutShaderReadOnlyOptimal)
	}
}

// Verified to fail: disabling the seen-resource conflict guard accepts sampled
// feedback, reporting "expected same-node error" on the first pair.
func TestSameNodeReadWriteIsAnError(t *testing.T) {
	for _, pair := range [][2]Access{{SampledRead, ColorWrite}, {StorageRead, StorageWrite}, {ColorWrite, ColorWrite}, {ColorLoadWrite, SampledRead}, {SampledRead, StorageRead}} {
		g := New()
		d := colorImage("target")
		d.Persistent = true
		g.AddImage(d)
		g.AddNode(Node{Name: "feedback", Kind: Graphics, Uses: []Use{{Resource: 0, Access: pair[0]}, {Resource: 0, Access: pair[1]}}})
		errorContains(t, g, "feedback", "target", "same-node")
	}
	for _, a := range []Access{ColorLoadWrite, DepthLoadWrite, StorageReadWrite, SampledRead} {
		g := New()
		d := colorImage("allowed")
		d.Persistent = true
		if a == DepthLoadWrite {
			d.Aspect, d.Format = core1_0.ImageAspectDepth, core1_0.FormatD32SignedFloat
		}
		g.AddImage(d)
		u := []Use{{Resource: 0, Access: a}}
		if a == SampledRead {
			u = append(u, u[0])
		}
		g.AddNode(Node{Name: "allowed", Kind: Graphics, Uses: u})
		mustBuild(t, g)
	}
}

// Verified to fail: disabling ordinary extent/sample agreement accepts both bad
// attachments; separately disabling resolve agreement accepts all three bad targets.
func TestAttachmentsMustAgree(t *testing.T) {
	for _, tc := range []struct {
		name    string
		change  func(*ImageDesc)
		resolve bool
	}{
		{"extent", func(d *ImageDesc) { d.Extent.Scale = 0.5 }, false},
		{"samples", func(d *ImageDesc) { d.Samples = core1_0.Samples4 }, false},
		{"resolve format", func(d *ImageDesc) { d.Format = core1_0.FormatB8G8R8A8SRGB }, true},
		{"resolve samples", func(d *ImageDesc) { d.Samples = core1_0.Samples4 }, true},
		{"resolve extent", func(d *ImageDesc) { d.Extent.Scale = 0.5 }, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := New()
			a, b := colorImage("a"), colorImage("b")
			if tc.resolve {
				a.Samples = core1_0.Samples4
			}
			tc.change(&b)
			g.AddImage(a)
			g.AddImage(b)
			uses := []Use{{Resource: 0, Access: ColorWrite}, {Resource: 1, Access: ColorWrite}}
			rule := "attachments must agree"
			if tc.resolve {
				uses = []Use{{Resource: 0, Access: ColorWrite, HasResolve: true, ResolveTo: 1}}
				rule = "resolve target"
			}
			g.AddNode(Node{Name: "attachments", Kind: Graphics, Uses: uses})
			errorContains(t, g, "attachments", "b", rule)
		})
	}
}

// Verified to fail by separately disabling extent, sample-count, instances/array,
// node-kind, dependency-kind, access, clear/discard, attachment-kind, resolve-source,
// Present-import, aspect and depth-count guards. Each reports a missing expected
// error or an error from the wrong rule, rather than silently accepting the graph.
// Also verified order kind/count/identity guards and unknown resource/resolve
// diagnostics: removing the count guard panics on index 0 of an empty mapping;
// the others produce missing or wrong-rule errors.
func TestInvalidDeclarations(t *testing.T) {
	for _, tc := range []struct {
		name, rule string
		edit       func(*Graph)
	}{
		{"scale", "extent", func(g *Graph) { g.images[0].Extent.Scale = 0 }},
		{"nan", "extent", func(g *Graph) { g.images[0].Extent.Scale = float32(math.NaN()) }},
		{"infinite", "extent", func(g *Graph) { g.images[0].Extent.Scale = float32(math.Inf(1)) }},
		{"fixed half", "extent", func(g *Graph) { g.images[0].Extent.Fixed = [2]uint32{1, 0} }},
		{"samples", "sample-count", func(g *Graph) { g.images[0].Samples = 3 }},
		{"instances", "instances", func(g *Graph) { g.images[0].Instances = -1 }},
		{"array", "arrays", func(g *Graph) { g.images[0].Layers = 2 }},
		{"kind", "node kind", func(g *Graph) { g.nodes[0].Kind = 0 }},
		{"dependency kind", "dependencies require", func(g *Graph) { g.nodes[0].Kind = Legacy; g.nodes[0].Dependencies = []core1_0.SubpassDependency{} }},
		{"resource", "unknown resource", func(g *Graph) { g.nodes[0].Uses[0].Resource = 7 }},
		{"negative resource", "unknown resource", func(g *Graph) { g.nodes[0].Uses[0].Resource = -1 }},
		{"access", "unknown access", func(g *Graph) { g.nodes[0].Uses[0].Access = 0 }},
		{"clear", "clear/discard", func(g *Graph) { g.nodes[0].Uses[0] = Use{Access: SampledRead, Clear: &Clear{}} }},
		{"discard load", "clear/discard", func(g *Graph) { g.nodes[0].Uses[0] = Use{Access: ColorLoadWrite, Discard: true} }},
		{"compute attachment", "attachments require", func(g *Graph) { g.nodes[0].Kind = Compute }},
		{"transfer attachment", "attachments require", func(g *Graph) { g.nodes[0].Kind = Transfer }},
		{"resolve single sample", "multisampled colour", func(g *Graph) { g.nodes[0].Uses[0].HasResolve = true }},
		{"resolve depth", "multisampled colour", func(g *Graph) { g.nodes[0].Uses[0].Access = DepthWrite; g.nodes[0].Uses[0].HasResolve = true }},
		{"resolve id", "unknown resolve", func(g *Graph) {
			g.images[0].Samples = core1_0.Samples4
			g.nodes[0].Uses[0].HasResolve = true
			g.nodes[0].Uses[0].ResolveTo = 7
		}},
		{"present import", "imported", func(g *Graph) { g.nodes[0].Uses[0].Access = Present }},
		{"aspect", "aspect", func(g *Graph) { g.images[0].Aspect = core1_0.ImageAspectDepth }},
		{"two depths", "one depth", func(g *Graph) {
			g.images[0] = depthImage("target")
			g.AddImage(depthImage("other"))
			g.nodes[0].Uses = []Use{{Access: DepthWrite}, {Resource: 1, Access: DepthWrite}}
		}},
		{"resolve feedback", "same-node", func(g *Graph) {
			g.images[0].Samples = core1_0.Samples4
			g.AddImage(colorImage("resolved"))
			g.nodes[0].Uses = []Use{{Access: ColorWrite, HasResolve: true, ResolveTo: 1}, {Resource: 1, Access: SampledRead}}
		}},
		{"order kind", "order requires", func(g *Graph) { g.nodes[0].Kind = Legacy; g.nodes[0].AttachmentOrder = []ResourceID{0} }},
		{"order count", "every attachment", func(g *Graph) { g.nodes[0].AttachmentOrder = []ResourceID{} }},
		{"order unknown", "unknown or duplicate", func(g *Graph) { g.nodes[0].AttachmentOrder = []ResourceID{7} }},
		{"order duplicate", "unknown or duplicate", func(g *Graph) {
			g.AddImage(colorImage("other"))
			g.nodes[0].Uses = append(g.nodes[0].Uses, Use{Resource: 1, Access: ColorWrite})
			g.nodes[0].AttachmentOrder = []ResourceID{0, 0}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := New()
			g.AddImage(colorImage("target"))
			g.AddNode(Node{Name: "invalid", Kind: Graphics, Uses: []Use{{Access: ColorWrite}}})
			tc.edit(g)
			_, err := g.Build()
			if err == nil {
				t.Fatalf("expected %s error", tc.rule)
			}
			var be *BuildError
			if !errors.As(err, &be) || be.Node == "" || be.Resource == "" || !strings.Contains(be.Rule, tc.rule) {
				t.Fatalf("wrong error: %v", err)
			}
		})
	}
}

// Verified to fail: incrementing the graph's stored Usage during Build changes
// the second plan (depth usage 32 -> 33), failing "repeated plan".
func TestBuildIsDeterministic(t *testing.T) {
	g := waterGraph(true, false)
	a := mustBuild(t, g)
	for range 8 {
		equal(t, "repeated plan", mustBuild(t, g), a)
	}
	equal(t, "timed slots", a.Timed, []NodeID{0, 2})
	b := mustBuild(t, g)
	a.Resources[0].Desc.Name = "changed"
	a.Steps[2].RenderPass.Dependencies[0].DstAccessMask = 0
	a.Steps[2].RenderPass.Attachments[0].Format = 0
	equal(t, "owned plans", mustBuild(t, g), b)
}

// Verified to fail: returning nil Timed instead of an allocated empty slice
// reports "nil plan slice []framegraph.NodeID", including for the empty graph.
func TestPlanHasNoPerFrameAllocations(t *testing.T) {
	var inspectType func(reflect.Type)
	inspectType = func(typ reflect.Type) {
		switch typ.Kind() {
		case reflect.Map, reflect.Func, reflect.Interface:
			t.Fatalf("hot-path type %v", typ)
		case reflect.Pointer, reflect.Slice, reflect.Array:
			inspectType(typ.Elem())
		case reflect.Struct:
			for i := 0; i < typ.NumField(); i++ {
				inspectType(typ.Field(i).Type)
			}
		}
	}
	inspectType(reflect.TypeFor[Plan]())
	var inspectValue func(reflect.Value)
	inspectValue = func(v reflect.Value) {
		switch v.Kind() {
		case reflect.Pointer:
			if !v.IsNil() {
				inspectValue(v.Elem())
			}
		case reflect.Struct:
			for i := 0; i < v.NumField(); i++ {
				inspectValue(v.Field(i))
			}
		case reflect.Slice:
			if v.IsNil() {
				t.Fatalf("nil plan slice %v", v.Type())
			}
			for i := 0; i < v.Len(); i++ {
				inspectValue(v.Index(i))
			}
		}
	}
	for _, g := range []*Graph{New(), waterGraph(true, false), bloomGraph(6)} {
		p := mustBuild(t, g)
		inspectValue(reflect.ValueOf(p))
		equal(t, "step sizing", len(p.Steps), len(g.nodes))
		equal(t, "resource sizing", len(p.Resources), len(g.images))
		for _, s := range p.Steps {
			if s.RenderPass != nil {
				equal(t, "clear sizing", len(s.RenderPass.Clears), len(s.RenderPass.Attachments))
				equal(t, "resolve sizing", len(s.RenderPass.Resolve), len(s.RenderPass.Color))
			}
		}
	}
}
