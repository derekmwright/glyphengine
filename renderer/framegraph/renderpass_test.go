package framegraph

import (
	"github.com/vkngwrapper/core/v3/core1_0"
	"testing"
)

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
