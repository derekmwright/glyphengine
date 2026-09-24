package framegraph

import (
	"fmt"
	"testing"

	"github.com/vkngwrapper/core/v3/core1_0"
)

// Verified break: suppressing buffer barriers leaves zero draw barriers instead
// of two. Removing kind and content guards separately fails the tests below.
func TestBufferDrawFixture(t *testing.T) {
	g := New()
	b := g.AddBuffer(BufferDesc{Name: "instances and arguments", Size: 4096})
	color := g.AddImage(colorImage("colour"))
	g.AddNode(Node{Name: "select", Kind: Compute, Uses: []Use{{Resource: b, Access: StorageWrite}}})
	g.AddNode(Node{Name: "draw", Kind: Graphics, Uses: []Use{{Resource: b, Access: VertexRead}, {Resource: b, Access: IndirectRead}, {Resource: color, Access: ColorWrite}}})
	p := mustBuild(t, g)
	equal(t, "initial write", len(p.Steps[0].Barriers), 0)
	equal(t, "draw barriers", p.Steps[1].Barriers[:2], []Barrier{
		{Resource: b, Buffer: true, Size: 4096, SrcStage: core1_0.PipelineStageComputeShader, DstStage: core1_0.PipelineStageVertexInput, SrcAccess: core1_0.AccessShaderWrite, DstAccess: core1_0.AccessVertexAttributeRead},
		{Resource: b, Buffer: true, Size: 4096, SrcStage: core1_0.PipelineStageComputeShader | core1_0.PipelineStageVertexInput, DstStage: core1_0.PipelineStageDrawIndirect, SrcAccess: core1_0.AccessShaderWrite | core1_0.AccessVertexAttributeRead, DstAccess: core1_0.AccessIndirectCommandRead},
	})
	equal(t, "usage", p.Resources[b].BufferDesc.Usage, core1_0.BufferUsageStorageBuffer|core1_0.BufferUsageVertexBuffer|core1_0.BufferUsageIndirectBuffer)
	equal(t, "attachment entry", p.Steps[1].Barriers[2].NewLayout, core1_0.ImageLayoutColorAttachmentOptimal)
	equal(t, "buffer has no prime", p.Resources[b].Prime, false)
	equal(t, "buffer not an attachment", len(p.Steps[1].RenderPass.Attachments), 1)

	equal(t, "no layout return", len(p.FinalBarriers), 0)
	equal(t, "repeat build", mustBuild(t, g), p)
}

func TestBufferAccessKinds(t *testing.T) {
	for _, a := range []Access{SampledRead, ColorWrite, ColorLoadWrite, DepthWrite, DepthLoadWrite, Present} {
		g := New()
		g.AddBuffer(BufferDesc{Name: "buffer", Size: 80, Persistent: true})
		g.AddNode(Node{Name: "invalid", Kind: Graphics, Uses: []Use{{Access: a}}})
		errorContains(t, g, "invalid", "buffer", "resource kind")
	}
	for _, a := range []Access{VertexRead, IndirectRead} {
		g := New()
		d := colorImage("image")
		d.Persistent = true
		g.AddImage(d)
		g.AddNode(Node{Name: "invalid", Kind: Graphics, Uses: []Use{{Access: a}}})
		errorContains(t, g, "invalid", "image", "resource kind")
	}
	for _, size := range []int{0, -1} {
		g := New()
		g.AddBuffer(BufferDesc{Name: "size", Size: size})
		errorContains(t, g, "<buffers>", "size", "positive")
	}
	for _, u := range []Use{{Access: StorageWrite, HasResolve: true}, {Access: StorageWrite, FinalLayout: core1_0.ImageLayoutGeneral}} {
		g := New()
		g.AddBuffer(BufferDesc{Name: "buffer", Size: 80})
		g.AddNode(Node{Name: "invalid", Kind: Compute, Uses: []Use{u}})
		errorContains(t, g, "invalid", "buffer", "no image layout")
	}
}

func TestBufferHazardsAndOptionalContents(t *testing.T) {
	for _, a := range []Access{StorageRead, StorageReadWrite, VertexRead, IndirectRead, TransferSrc} {
		g := New()
		g.AddBuffer(BufferDesc{Name: "buffer", Size: 80})
		g.AddNode(Node{Name: "read", Kind: Compute, Uses: []Use{{Access: a}}})
		errorContains(t, g, "read", "buffer", "read before")
	}
	for _, persistent := range []bool{false, true} {
		g := New()
		g.AddBuffer(BufferDesc{Name: "buffer", Size: 80, Persistent: persistent})
		g.AddNode(Node{Name: "optional write", Kind: Compute, OptionalGroup: 1, Uses: []Use{{Access: StorageWrite}}})
		g.AddNode(Node{Name: "inside", Kind: Compute, OptionalGroup: 1, Uses: []Use{{Access: StorageRead}}})
		mustBuild(t, g)
		g.AddNode(Node{Name: "outside", Kind: Graphics, Uses: []Use{{Access: VertexRead}}})
		if !persistent {
			errorContains(t, g, "outside", "buffer", "read before")
		} else {
			p := mustBuild(t, g)
			if len(p.Steps[2].Barriers) != 1 {
				t.Fatal("skipped writer disappeared")
			}
		}
	}
	for _, pair := range [][2]Access{{StorageWrite, StorageWrite}, {StorageWrite, StorageRead}, {StorageRead, StorageWrite}, {TransferDst, VertexRead}, {TransferSrc, TransferDst}, {VertexRead, StorageReadWrite}} {
		t.Run(fmt.Sprint(pair), func(t *testing.T) {
			g := New()
			g.AddBuffer(BufferDesc{Name: "b", Size: 64, Imported: true})
			for _, a := range pair {
				g.AddNode(Node{Kind: Compute, Uses: []Use{{Access: a}}})
			}
			p := mustBuild(t, g)
			equal(t, "hazard", len(p.Steps[1].Barriers), 1)
		})
	}
	g := New()
	g.AddBuffer(BufferDesc{Name: "b", Size: 64})
	for _, a := range []Access{StorageWrite, VertexRead, VertexRead, IndirectRead, StorageRead, StorageWrite} {
		g.AddNode(Node{Kind: Compute, Uses: []Use{{Access: a}}})
	}
	p := mustBuild(t, g)
	for i, n := range []int{0, 1, 0, 1, 1, 1} {
		equal(t, fmt.Sprintf("step %d", i), len(p.Steps[i].Barriers), n)
	}
	b := p.Steps[5].Barriers[0]
	equal(t, "all readers", b.SrcStage, core1_0.PipelineStageComputeShader|core1_0.PipelineStageVertexInput|core1_0.PipelineStageDrawIndirect)
}

// Verified break: treating buffers as Undefined images changes the second
// buffer's source from VertexInput (4) to ComputeShader (2048).
func TestMixedBarrierGroupingPreservesBufferSource(t *testing.T) {
	b := []Barrier{{Buffer: true, SrcStage: core1_0.PipelineStageComputeShader, DstStage: core1_0.PipelineStageTransfer}, {Buffer: true, SrcStage: core1_0.PipelineStageVertexInput, DstStage: core1_0.PipelineStageTransfer}, {DstStage: core1_0.PipelineStageTransfer}}
	groupUndefinedBarriers(b)
	equal(t, "buffer source", b[0].SrcStage, core1_0.PipelineStageComputeShader)
	equal(t, "second buffer source", b[1].SrcStage, core1_0.PipelineStageVertexInput)
	equal(t, "image shares source", b[2].SrcStage, b[1].SrcStage)
}
