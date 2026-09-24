package framegraph

import (
	"github.com/vkngwrapper/core/v3/core1_0"
)

func (g *Graph) renderPass(ni int, uses [][]compiledUse, resources []ResourceInfo, states []imageState) *RenderPassDesc {
	d := &RenderPassDesc{
		Attachments: make([]AttachmentDesc, 0), Color: make([]int, 0),
		Resolve: make([]int, 0), Depth: -1, Clears: make([]Clear, 0),
		Samples: core1_0.Samples1, Extent: Extent{Scale: 1},
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
	return d
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
