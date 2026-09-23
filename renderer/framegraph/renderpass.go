package framegraph

import (
	"encoding/binary"
	"slices"

	"github.com/vkngwrapper/core/v3/core1_0"
)

func (g *Graph) renderPass(ni int, uses [][]compiledUse, resources []ResourceInfo, states []imageState) *RenderPassDesc {
	n := g.nodes[ni]
	d := &RenderPassDesc{
		Attachments: make([]AttachmentDesc, 0), Color: make([]int, 0),
		Resolve: make([]int, 0), Depth: -1, Clears: make([]Clear, 0),
		Dependencies: make([]core1_0.SubpassDependency, 0), Samples: core1_0.Samples1, Extent: Extent{Scale: 1},
	}
	if n.Dependencies != nil {
		d.Dependencies = append(d.Dependencies, n.Dependencies...)
	} else {
		d.Dependencies = append(d.Dependencies, bloomDependency())
		// Depth-only and mixed passes also need their depth accesses ordered.
		// Explicit dependencies remain byte-for-byte caller-owned policy.
		for _, u := range uses[ni] {
			before := states[u.Resource]
			if attachment(u.Access) && (before.layout == core1_0.ImageLayoutGeneral ||
				before.access&(core1_0.AccessShaderWrite|core1_0.AccessTransferRead|core1_0.AccessTransferWrite) != 0) {
				// Attachments cannot use an image barrier, so a preceding storage
				// dispatch or copy must instead be covered by the entry dependency.
				dep := &d.Dependencies[0]
				after := accessState(Graphics, u.Use)
				dep.SrcStageMask |= before.stage
				dep.SrcAccessMask |= before.access
				dep.DstStageMask |= after.stage
				dep.DstAccessMask |= after.access
			}
			if isDepth(u.Access) {
				dep := &d.Dependencies[0]
				dep.SrcStageMask |= core1_0.PipelineStageEarlyFragmentTests | core1_0.PipelineStageLateFragmentTests
				dep.DstStageMask |= core1_0.PipelineStageEarlyFragmentTests | core1_0.PipelineStageLateFragmentTests
				dep.SrcAccessMask |= core1_0.AccessDepthStencilAttachmentWrite
				dep.DstAccessMask |= core1_0.AccessDepthStencilAttachmentRead | core1_0.AccessDepthStencilAttachmentWrite
			}
		}
	}
	appendAttachment := func(u compiledUse) int {
		r := resources[u.Resource]
		load := core1_0.AttachmentLoadOpDontCare
		if reads(u.Access) {
			load = core1_0.AttachmentLoadOpLoad
		} else if u.Clear != nil {
			load = core1_0.AttachmentLoadOpClear
		}
		initial := states[u.Resource].layout
		if u.resolve || u.Discard {
			initial = core1_0.ImageLayoutUndefined
		}
		store := core1_0.AttachmentStoreOpStore
		if (isDepth(u.Access) || u.HasResolve) && !r.Desc.Persistent && !readLater(uses, ni, u.Resource) {
			store = core1_0.AttachmentStoreOpDontCare
		}
		i := len(d.Attachments)
		d.Attachments = append(d.Attachments, AttachmentDesc{
			Resource: u.Resource, Format: r.Desc.Format, Samples: r.Desc.Samples,
			LoadOp: load, StoreOp: store,
			StencilLoadOp: core1_0.AttachmentLoadOpDontCare, StencilStoreOp: core1_0.AttachmentStoreOpDontCare,
			InitialLayout: initial, FinalLayout: r.Resting, SubpassLayout: accessState(Graphics, u.Use).layout,
		})
		clear := Clear{}
		if u.Clear != nil {
			clear = *u.Clear
		}
		d.Clears = append(d.Clears, clear)
		return i
	}
	for _, u := range uses[ni] {
		if attachment(u.Access) && !u.resolve {
			d.Extent, d.Samples = resources[u.Resource].Desc.Extent, resources[u.Resource].Desc.Samples
		}
		if (u.Access == ColorWrite || u.Access == ColorLoadWrite) && !u.resolve {
			d.Color = append(d.Color, appendAttachment(u))
			d.Resolve = append(d.Resolve, -1)
		}
	}
	color := 0
	for _, u := range uses[ni] {
		if (u.Access == ColorWrite || u.Access == ColorLoadWrite) && !u.resolve {
			if u.HasResolve {
				d.Resolve[color] = appendAttachment(compiledUse{Use: Use{Resource: u.ResolveTo, Access: ColorWrite}, resolve: true})
			}
			color++
		}
	}
	for _, u := range uses[ni] {
		if isDepth(u.Access) {
			d.Depth = appendAttachment(u)
		}
	}
	if n.AttachmentOrder != nil {
		reorderAttachments(d, n.AttachmentOrder)
	}
	return d
}

func reorderAttachments(d *RenderPassDesc, order []ResourceID) {
	attachments := make([]AttachmentDesc, len(order))
	clears := make([]Clear, len(order))
	indices := make([]int, len(order))
	for i, id := range order {
		for old, a := range d.Attachments {
			if a.Resource == id {
				attachments[i], clears[i], indices[old] = a, d.Clears[old], i
				break
			}
		}
	}
	for i, ref := range d.Color {
		d.Color[i] = indices[ref]
		if d.Resolve[i] >= 0 {
			d.Resolve[i] = indices[d.Resolve[i]]
		}
	}
	if d.Depth >= 0 {
		d.Depth = indices[d.Depth]
	}
	d.Attachments, d.Clears = attachments, clears
}

func readLater(uses [][]compiledUse, ni int, id ResourceID) bool {
	for _, node := range uses[ni+1:] {
		for _, u := range node {
			if u.Resource == id {
				if reads(u.Access) {
					return true
				}
				// Keeping an unnecessary store is safe when an intervening write
				// can be skipped. Looking through writes also matches scene depth.
			}
		}
	}
	return false
}

// Compatible compares this package's single-subpass attachment structure and
// dependencies, ignoring load/store operations, layouts, resource IDs and extent.
func Compatible(a, b *RenderPassDesc) bool {
	if a == nil || b == nil {
		return a == b
	}
	if len(a.Attachments) != len(b.Attachments) || !slices.Equal(a.Color, b.Color) ||
		!slices.Equal(a.Resolve, b.Resolve) || a.Depth != b.Depth || !slices.Equal(a.Dependencies, b.Dependencies) {
		return false
	}
	for i, x := range a.Attachments {
		y := b.Attachments[i]
		if x.Format != y.Format || x.Samples != y.Samples {
			return false
		}
	}
	return true
}

// RenderPassKey is an exact, length-delimited encoding, not a hash. It cannot
// collide, and excludes resource IDs, framebuffer extent, and clear values,
// which are not part of a VkRenderPass object. Compute it at build/cache time.
type RenderPassKey string

func (d *RenderPassDesc) Key() RenderPassKey {
	if d == nil {
		return ""
	}
	b := make([]byte, 0)
	put := func(v int64) { b = binary.LittleEndian.AppendUint64(b, uint64(v)) }
	put(int64(len(d.Attachments)))
	for _, a := range d.Attachments {
		for _, v := range []int64{int64(a.Format), int64(a.Samples), int64(a.LoadOp), int64(a.StoreOp),
			int64(a.StencilLoadOp), int64(a.StencilStoreOp), int64(a.InitialLayout), int64(a.FinalLayout), int64(a.SubpassLayout)} {
			put(v)
		}
	}
	for _, refs := range [][]int{d.Color, d.Resolve} {
		put(int64(len(refs)))
		for _, r := range refs {
			put(int64(r))
		}
	}
	put(int64(d.Depth))
	put(int64(len(d.Dependencies)))
	for _, dep := range d.Dependencies {
		for _, v := range []int64{int64(dep.SrcSubpass), int64(dep.DstSubpass), int64(dep.SrcStageMask),
			int64(dep.DstStageMask), int64(dep.SrcAccessMask), int64(dep.DstAccessMask), int64(dep.DependencyFlags)} {
			put(v)
		}
	}
	return RenderPassKey(b)
}
