package framegraph

import (
	"strings"
	"testing"

	"github.com/vkngwrapper/core/v3/core1_0"
)

// chainGraph is a persistent 64x32 image with the full seven-level chain, the
// shape the renderer declares for a storage render target with MipLevels 7.
func chainGraph() (*Graph, ResourceID) {
	g := New()
	id := g.AddImage(ImageDesc{Name: "chain", Format: core1_0.FormatR16G16B16A16SignedFloat, Extent: Extent{Fixed: [2]uint32{64, 32}},
		Samples: core1_0.Samples1, Aspect: core1_0.ImageAspectColor, Persistent: true, Levels: 7})
	return g, id
}

const allShaders = core1_0.PipelineStageVertexShader | core1_0.PipelineStageFragmentShader | core1_0.PipelineStageComputeShader

func level(l uint32) LevelRange { return LevelRange{Base: l, Count: 1} }

// reduce declares one reduction the way the renderer does for a dispatch that
// reads level n and writes level n+1 of the same target, followed by the
// sampled-outputs node that returns the written level to the sampled layout.
func reduce(g *Graph, id ResourceID, n uint32, group int) {
	g.AddNode(Node{Name: "reduce", Kind: Compute, OptionalGroup: group, Uses: []Use{
		{Resource: id, Access: SampledRead, Levels: level(n)},
		{Resource: id, Access: StorageWrite, Levels: level(n + 1)}}})
	g.AddNode(Node{Name: "reduce sampled outputs", Kind: Transfer, OptionalGroup: group, Uses: []Use{
		{Resource: id, Access: SampledRead, Levels: level(n + 1), Stages: allShaders}}})
}

func onlyBarrier(t *testing.T, where string, bs []Barrier) Barrier {
	t.Helper()
	if len(bs) != 1 {
		t.Fatalf("%s: %d barriers, want 1: %+v", where, len(bs), bs)
	}
	return bs[0]
}

// A node reading level 2 and writing level 3 of one image is legal, and the
// one barrier it needs -- level 3 into General -- names level 3 alone. Level 2
// is already sampled-layout and visible, so it gets none.
//
// Verified to fail: widening the range to the whole image (imageUse
// appending its barriers with LevelRange{} instead of levels) reports
// "entry: Levels the whole image, want levels [3,4)".
func TestDisjointReadWriteBarrierRanges(t *testing.T) {
	g, id := chainGraph()
	reduce(g, id, 2, 1)
	p, err := g.Build()
	if err != nil {
		t.Fatal(err)
	}
	entry := onlyBarrier(t, "entry", p.Steps[0].Barriers)
	if entry.Levels != level(3) {
		t.Fatalf("entry: Levels %+v, want %+v", entry.Levels, level(3))
	}
	if entry.OldLayout != core1_0.ImageLayoutShaderReadOnlyOptimal || entry.NewLayout != core1_0.ImageLayoutGeneral {
		t.Fatalf("entry: %v -> %v, want ShaderReadOnly -> General", entry.OldLayout, entry.NewLayout)
	}
	exit := onlyBarrier(t, "sampled outputs", p.Steps[1].Barriers)
	if exit.Levels != level(3) || exit.OldLayout != core1_0.ImageLayoutGeneral || exit.NewLayout != core1_0.ImageLayoutShaderReadOnlyOptimal {
		t.Fatalf("sampled outputs: %+v, want level 3 General -> ShaderReadOnly", exit)
	}
	if exit.SrcAccess != core1_0.AccessShaderWrite || exit.DstStage&core1_0.PipelineStageComputeShader == 0 {
		t.Fatalf("sampled outputs: %+v does not publish the compute write to compute reads", exit)
	}
	if len(p.FinalBarriers) != 0 {
		t.Fatalf("final barriers %+v: every level should be back at rest", p.FinalBarriers)
	}
}

// Overlapping read and write ranges in one node are the same subresource read
// and stored at once, which no barrier can order; Build refuses them.
//
// Verified to fail: with disjoint() returning true for every pair, the
// [2,4)/level-3 case builds and the test reports "overlap [2,4) and level 3
// compiled".
func TestOverlappingRangesRefused(t *testing.T) {
	for _, tc := range []struct {
		name        string
		read, write LevelRange
	}{
		{"overlap [2,4) and level 3", LevelRange{Base: 2, Count: 2}, level(3)},
		{"whole read and level 3", LevelRange{}, level(3)},
		{"same level", level(3), level(3)},
	} {
		g, id := chainGraph()
		g.AddNode(Node{Name: "reduce", Kind: Compute, Uses: []Use{
			{Resource: id, Access: SampledRead, Levels: tc.read},
			{Resource: id, Access: StorageWrite, Levels: tc.write}}})
		_, err := g.Build()
		if err == nil {
			t.Fatalf("%s compiled", tc.name)
		}
		t.Logf("%s: %v", tc.name, err)
	}
}

// A write to level 4 in one node and a read of it in a later one is an edge on
// level 4 alone: the reader's barrier takes level 4 out of General and touches
// no other level.
//
// Verified to fail: tracking the image as one state (levels[i] left nil for
// every image, so ranges are ignored) makes the writer's barrier whole-image
// and reports "writer: Levels the whole image, want levels [4,5)". Widening
// the barrier range alone reports the same line.
func TestCrossPassEdgeOnOneLevel(t *testing.T) {
	g, id := chainGraph()
	g.AddNode(Node{Name: "writer", Kind: Compute, Uses: []Use{{Resource: id, Access: StorageWrite, Levels: level(4)}}})
	g.AddNode(Node{Name: "reader", Kind: Compute, Uses: []Use{{Resource: id, Access: SampledRead, Levels: level(4)}}})
	p, err := g.Build()
	if err != nil {
		t.Fatal(err)
	}
	w := onlyBarrier(t, "writer", p.Steps[0].Barriers)
	if w.Levels != level(4) {
		t.Fatalf("writer: Levels %+v, want %+v", w.Levels, level(4))
	}
	rd := onlyBarrier(t, "reader", p.Steps[1].Barriers)
	if rd.Levels != level(4) || rd.OldLayout != core1_0.ImageLayoutGeneral || rd.NewLayout != core1_0.ImageLayoutShaderReadOnlyOptimal ||
		rd.SrcAccess != core1_0.AccessShaderWrite || rd.DstAccess != core1_0.AccessShaderRead {
		t.Fatalf("reader: %+v, want level 4 General/ShaderWrite -> ShaderReadOnly/ShaderRead", rd)
	}
}

// Per-level layouts: level 1 is left in General by a storage write while a
// later node samples level 0, which needs no barrier because level 0 never
// left the sampled layout. A whole-chain sample after that splits into runs
// and transitions level 1 alone.
//
// Verified to fail: tracking the image as one state (levels[i] left nil)
// gives the level-0 read a whole-image General -> ShaderReadOnly barrier and
// reports "level-0 read: 1 barriers, want 0".
func TestPerLevelLayoutTracking(t *testing.T) {
	g, id := chainGraph()
	g.AddNode(Node{Name: "write 1", Kind: Compute, Uses: []Use{{Resource: id, Access: StorageWrite, Levels: level(1)}}})
	g.AddNode(Node{Name: "read 0", Kind: Compute, Uses: []Use{{Resource: id, Access: SampledRead, Levels: level(0)}}})
	g.AddNode(Node{Name: "read all", Kind: Graphics, Uses: []Use{
		{Resource: id, Access: SampledRead},
		{Resource: g.AddImage(ImageDesc{Name: "out", Format: core1_0.FormatR8G8B8A8UnsignedNormalized, Extent: Extent{Fixed: [2]uint32{8, 8}},
			Samples: core1_0.Samples1, Aspect: core1_0.ImageAspectColor, Persistent: true}), Access: ColorWrite, Discard: true}}})
	p, err := g.Build()
	if err != nil {
		t.Fatal(err)
	}
	if n := len(p.Steps[1].Barriers); n != 0 {
		t.Fatalf("level-0 read: %d barriers, want 0: %+v", n, p.Steps[1].Barriers)
	}
	var mine []Barrier
	for _, b := range p.Steps[2].Barriers {
		if b.Resource == id {
			mine = append(mine, b)
		}
	}
	b := onlyBarrier(t, "whole-chain read", mine)
	if b.Levels != level(1) || b.OldLayout != core1_0.ImageLayoutGeneral {
		t.Fatalf("whole-chain read: %+v, want level 1 out of General", b)
	}
}

// A whole-image use of a chain whose levels agree is one whole-image barrier,
// as it would be for a single-level image: the executor's whole-image path
// stays the whole-image path.
func TestWholeChainUseIsOneBarrier(t *testing.T) {
	g, id := chainGraph()
	g.AddNode(Node{Name: "write all", Kind: Compute, Uses: []Use{{Resource: id, Access: StorageWrite}}})
	g.AddNode(Node{Name: "read all", Kind: Compute, Uses: []Use{{Resource: id, Access: SampledRead}}})
	p, err := g.Build()
	if err != nil {
		t.Fatal(err)
	}
	for i, name := range []string{"write all", "read all"} {
		if b := onlyBarrier(t, name, p.Steps[i].Barriers); b.Levels != (LevelRange{}) {
			t.Fatalf("%s: Levels %+v, want the zero (whole) range", name, b.Levels)
		}
	}
	if p.Resources[id].Desc.Levels != 7 {
		t.Fatalf("plan levels %d, want 7", p.Resources[id].Desc.Levels)
	}
}

// A single-level image never takes a range, and its plan names one level.
func TestSingleLevelPlanUnchanged(t *testing.T) {
	g := New()
	id := g.AddImage(ImageDesc{Name: "flat", Format: core1_0.FormatR16G16B16A16SignedFloat, Extent: Extent{Fixed: [2]uint32{4, 4}},
		Samples: core1_0.Samples1, Aspect: core1_0.ImageAspectColor, Persistent: true})
	g.AddNode(Node{Name: "write", Kind: Compute, Uses: []Use{{Resource: id, Access: StorageWrite}}})
	g.AddNode(Node{Name: "read", Kind: Compute, Uses: []Use{{Resource: id, Access: SampledRead}}})
	p, err := g.Build()
	if err != nil {
		t.Fatal(err)
	}
	if p.Resources[id].Desc.Levels != 1 {
		t.Fatalf("plan levels %d, want 1", p.Resources[id].Desc.Levels)
	}
	for i, s := range p.Steps {
		for _, b := range s.Barriers {
			if b.Levels != (LevelRange{}) {
				t.Fatalf("step %d: single-level barrier names levels %+v", i, b.Levels)
			}
		}
	}
}

// Disabling a reduction must leave every level where it found it, so a group
// whose write leaves its level in General is refused, naming the level.
func TestOptionalGroupNeutralPerLevel(t *testing.T) {
	g, id := chainGraph()
	g.AddNode(Node{Name: "reduce", Kind: Compute, OptionalGroup: 1, Uses: []Use{
		{Resource: id, Access: SampledRead, Levels: level(0)},
		{Resource: id, Access: StorageWrite, Levels: level(1)}}})
	_, err := g.Build()
	if err == nil || !strings.Contains(err.Error(), "level 1") {
		t.Fatalf("a group leaving level 1 in General: %v", err)
	}
	g, id = chainGraph()
	reduce(g, id, 0, 1)
	if _, err := g.Build(); err != nil {
		t.Fatalf("a group returning level 1 to rest: %v", err)
	}
}

func TestLevelRangeRefusals(t *testing.T) {
	for _, tc := range []struct {
		name, want string
		build      func() *Graph
	}{
		{"past the chain", "outside the image's 7 levels", func() *Graph {
			g, id := chainGraph()
			g.AddNode(Node{Name: "n", Kind: Compute, Uses: []Use{{Resource: id, Access: SampledRead, Levels: LevelRange{Base: 6, Count: 2}}}})
			return g
		}},
		{"empty range", "outside the image's 7 levels", func() *Graph {
			g, id := chainGraph()
			g.AddNode(Node{Name: "n", Kind: Compute, Uses: []Use{{Resource: id, Access: SampledRead, Levels: LevelRange{Base: 2}}}})
			return g
		}},
		{"on a single-level image", "outside the image's 1 levels", func() *Graph {
			g := New()
			id := g.AddImage(ImageDesc{Name: "flat", Format: core1_0.FormatR8G8B8A8UnsignedNormalized, Extent: Extent{Scale: 1}, Samples: core1_0.Samples1, Aspect: core1_0.ImageAspectColor, Persistent: true})
			g.AddNode(Node{Name: "n", Kind: Compute, Uses: []Use{{Resource: id, Access: SampledRead, Levels: level(1)}}})
			return g
		}},
		{"on an attachment", "needs a sampled, storage or transfer use", func() *Graph {
			g, id := chainGraph()
			g.AddNode(Node{Name: "n", Kind: Graphics, Uses: []Use{{Resource: id, Access: ColorWrite, Levels: level(0)}}})
			return g
		}},
		{"on a buffer", "needs a sampled, storage or transfer use", func() *Graph {
			g := New()
			id := g.AddBuffer(BufferDesc{Name: "b", Size: 16, Persistent: true})
			g.AddNode(Node{Name: "n", Kind: Compute, Uses: []Use{{Resource: id, Access: StorageRead, Levels: level(0)}}})
			return g
		}},
		{"a multisampled chain", "multisampled image has one level", func() *Graph {
			g := New()
			id := g.AddImage(ImageDesc{Name: "ms", Format: core1_0.FormatR8G8B8A8UnsignedNormalized, Extent: Extent{Scale: 1}, Samples: core1_0.Samples4, Aspect: core1_0.ImageAspectColor, Levels: 2})
			g.AddNode(Node{Name: "n", Kind: Graphics, Uses: []Use{{Resource: id, Access: ColorWrite}}})
			return g
		}},
	} {
		_, err := tc.build().Build()
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v, want %q", tc.name, err, tc.want)
			continue
		}
		t.Logf("%s: %v", tc.name, err)
	}
}
