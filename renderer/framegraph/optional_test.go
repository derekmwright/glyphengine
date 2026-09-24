package framegraph

import (
	"fmt"
	"testing"

	"github.com/vkngwrapper/core/v3/core1_0"
)

func groupedWaterGraph(msaa bool) *Graph {
	fixture := waterGraph(msaa, false)
	// Neutrality includes the private copy target. Prime its resting layout once;
	// the full copy still discards old pixels and never reads uninitialized ones.
	fixture.images[2].Persistent = true
	fixture.nodes[1].Uses[1].Discard = true
	fixture.nodes[1].OptionalGroup = 1
	fixture.nodes[2].OptionalGroup = 1
	fixture.nodes[3].Name = "bloom prefilter"
	target := colorImage("bloom")
	target.Extent.Scale = 0.5
	id := fixture.AddImage(target)
	fixture.nodes[3].Uses = append(fixture.nodes[3].Uses, Use{Resource: id, Access: ColorWrite})
	g := New()
	for _, d := range fixture.images {
		g.AddImage(d)
	}
	for _, n := range fixture.nodes {
		g.AddNode(n)
	}
	return g
}

// Verified to fail: restoring per-node neutrality rejects copy/HDR in both
// fixtures. Removing implicit Optional separately reports false instead of true.
func TestOptionalWaterGroup(t *testing.T) {
	for _, msaa := range []bool{false, true} {
		t.Run(fmt.Sprintf("MSAA=%v", msaa), func(t *testing.T) {
			g := groupedWaterGraph(msaa)
			p := mustBuild(t, g)
			for _, id := range []int{1, 2} {
				equal(t, "group implies Optional", g.nodes[id].Optional, true)
			}
			equal(t, "copy target primed", p.Resources[2].Prime, true)
			equal(t, "copy barriers", p.Steps[1].Barriers, []Barrier{
				{Resource: 0, SrcStage: core1_0.PipelineStageColorAttachmentOutput, DstStage: core1_0.PipelineStageTransfer,
					SrcAccess: core1_0.AccessColorAttachmentWrite, DstAccess: core1_0.AccessTransferRead,
					OldLayout: core1_0.ImageLayoutShaderReadOnlyOptimal, NewLayout: core1_0.ImageLayoutTransferSrcOptimal},
				{Resource: 2, SrcStage: core1_0.PipelineStageColorAttachmentOutput, DstStage: core1_0.PipelineStageTransfer,
					SrcAccess: 0, DstAccess: core1_0.AccessTransferWrite,
					OldLayout: core1_0.ImageLayoutUndefined, NewLayout: core1_0.ImageLayoutTransferDstOptimal},
			})
			equal(t, "copy becomes sampled", p.Steps[2].Barriers[len(p.Steps[2].Barriers)-1:], []Barrier{{Resource: 2,
				SrcStage: core1_0.PipelineStageTransfer, DstStage: core1_0.PipelineStageFragmentShader,
				SrcAccess: core1_0.AccessTransferWrite, DstAccess: core1_0.AccessShaderRead,
				OldLayout: core1_0.ImageLayoutTransferDstOptimal, NewLayout: core1_0.ImageLayoutShaderReadOnlyOptimal}})
			equal(t, "water pass unchanged", p.Steps[2].RenderPass, mustBuild(t, waterGraph(msaa, false)).Steps[2].RenderPass)
			equal(t, "prefilter entry and skipped scene visibility", len(p.Steps[3].Barriers), 2)
			equal(t, "group restores resting layouts", len(p.FinalBarriers), 0)
			equal(t, "repeat build", mustBuild(t, g), p)
		})
	}
}

// Verified to fail: disabling the seen-group guard returns a plan instead of
// the expected "must be contiguous" error for the resumed group.
func TestOptionalGroupsMustBeContiguous(t *testing.T) {
	for _, ids := range [][]int{{1, 0, 1}, {1, 2, 1}, {-1, 0, -1}} {
		g := New()
		d := colorImage("target")
		d.Persistent = true
		g.AddImage(d)
		for i, id := range ids {
			g.AddNode(Node{Name: fmt.Sprintf("node %d", i), Kind: Graphics, OptionalGroup: id, Uses: []Use{{Access: ColorWrite}}})
		}
		errorContains(t, g, "node 2", "target", "must be contiguous")
	}
}

// Verified to fail: disabling the group boundary check accepts the first node's
// Undefined -> ColorAttachment transition, reporting a missing neutrality error.
func TestOptionalGroupMustBeLayoutNeutral(t *testing.T) {
	for _, suffix := range []bool{false, true} {
		g := New()
		g.AddImage(colorImage("first node target"))
		other := colorImage("other target")
		other.Persistent = true
		g.AddImage(other)
		g.AddNode(Node{Name: "first", Kind: Graphics, OptionalGroup: 7, Uses: []Use{{Access: ColorWrite}}})
		g.AddNode(Node{Name: "last", Kind: Graphics, OptionalGroup: 7, Uses: []Use{{Resource: 1, Access: ColorWrite}}})
		if suffix {
			g.AddNode(Node{Name: "after", Kind: Graphics})
		}
		errorContains(t, g, "last", "first node target", "optional group 7 must be layout-neutral")
	}
}

// Verified to fail: treating grouped writes like independent optional writes
// rejects "read inside". Leaking grouped writes beyond the join separately
// loses the "read outside" error and reaches the later layout check instead.
func TestOptionalGroupContents(t *testing.T) {
	for _, access := range []Access{TransferDst, StorageWrite, ColorWrite} {
		g := New()
		g.AddImage(colorImage("local contents"))
		g.AddNode(Node{Name: "write inside", Kind: Legacy, OptionalGroup: 1, Uses: []Use{{Access: access}}})
		g.AddNode(Node{Name: "read inside", Kind: Compute, OptionalGroup: 1, Uses: []Use{{Access: StorageRead}}})
		uses, _, err := g.validate()
		if err != nil {
			t.Fatal(err)
		}
		// Test content availability independently of layout initialization: an
		// initially Undefined image cannot also be layout-neutral on group exit.
		if err := g.validateReads(uses); err != nil {
			t.Fatalf("group-local write must feed its reader: %v", err)
		}
		g.AddNode(Node{Name: "read outside", Kind: Compute, Uses: []Use{{Access: StorageRead}}})
		errorContains(t, g, "read outside", "local contents", "read before a guaranteed write")
	}
	// A preceding mandatory write survives the join, including across two groups.
	g := New()
	g.AddImage(colorImage("initialized"))
	g.AddNode(Node{Name: "initialize", Kind: Compute, Uses: []Use{{Access: StorageWrite}}})
	g.AddNode(Node{Name: "group 1", Kind: Compute, OptionalGroup: 1, Uses: []Use{{Access: StorageWrite}}})
	g.AddNode(Node{Name: "group 2", Kind: Compute, OptionalGroup: 2, Uses: []Use{{Access: StorageRead}}})
	g.AddNode(Node{Name: "read", Kind: Compute, Uses: []Use{{Access: StorageRead}}})
	mustBuild(t, g)
}

// Verified to fail: dropping the group-entry synchronization state emits zero
// barriers before the mandatory reader instead of one (source access 96).
func TestOptionalGroupPreservesSkippedPathHazards(t *testing.T) {
	g := New()
	g.AddImage(colorImage("producer"))
	g.AddNode(Node{Name: "write", Kind: Legacy, Uses: []Use{{Access: StorageWrite}}})
	g.AddNode(Node{Name: "optional read", Kind: Compute, OptionalGroup: 1, Uses: []Use{{Access: StorageRead}}})
	g.AddNode(Node{Name: "mandatory read", Kind: Compute, Uses: []Use{{Access: StorageRead}}})
	p := mustBuild(t, g)
	equal(t, "skip path still waits for writer", p.Steps[2].Barriers, []Barrier{{Resource: 0,
		SrcStage: core1_0.PipelineStageFragmentShader | core1_0.PipelineStageComputeShader,
		DstStage: core1_0.PipelineStageComputeShader, SrcAccess: core1_0.AccessShaderWrite | core1_0.AccessShaderRead,
		DstAccess: core1_0.AccessShaderRead, OldLayout: core1_0.ImageLayoutGeneral, NewLayout: core1_0.ImageLayoutGeneral}})
}
