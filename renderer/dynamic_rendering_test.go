package renderer

import (
	"github.com/vkngwrapper/core/v3/core1_0"
	"github.com/vkngwrapper/extensions/v3/khr_depth_stencil_resolve"
	"testing"
)

// The hashes below were measured on 2724ad5 before dynamic rendering. Only
// rendering begin/end and synchronization calls are filtered; every draw,
// dispatch, binding, copy and push-constant argument must remain identical.
// Full-call diff (each begin/end becomes Begin/EndRendering; counts unchanged):
// fixture  begin/end pairs  barrier calls before -> after
// base          11                    2 -> 27
// depth         12                    3 -> 31
// app           14                    4 -> 37
// compute       14                    8 -> 41
// glow          21                    2 -> 47
// GPU LOD       11                   11 -> 36
// These counts were measured on the baseline and this executor, not inferred
// from the graph. The added barriers replace implicit attachment transitions.
func TestMigrationDrawStreams(t *testing.T) {
	for _, test := range []struct {
		name  string
		make  func() *frame
		calls int
		hash  uint64
	}{
		{"base", func() *frame { return buildFrame(97) }, 3293, 0xe0799690a2c563cc},
		{"depth", func() *frame { return withAppFrame(buildFrame(97), false) }, 3298, 0xd80c2e073328999c},
		{"app", func() *frame { return withAppFrame(buildFrame(97), true) }, 3317, 0xa9dc0112c248397f},
		{"compute", func() *frame { return withAppFrame(buildFrame(97), true, true) }, 3321, 0x0dedee50c183ab46},
		{"ui", func() *frame { return withUILayer(buildFrame(97), false) }, 3299, 0x57f5860ed5b4639b},
		{"volume", func() *frame { return withVolumetricLight(buildFrame(97)) }, 3299, 0x11d2647497ee8ea0},
		{"gpu", func() *frame {
			fx, r, input := gpuFrame(t, 40)
			fx.draws = r.prepareLOD(input, fx.lighting, 1)
			return fx
		}, 1688, 0x0e52132a36960def},
		{"glow", func() *frame { return withUILayer(buildFrame(97), true) }, 3353, 0x17b16b25e06a7951},
	} {
		d := &fakeDriver{hashing: true, drawsOnly: true}
		if err := test.make().record(d, 1); err != nil {
			t.Fatal(err)
		}
		if d.calls != test.calls || uint64(d.h) != test.hash {
			t.Errorf("%s draw stream: %d %#x; want %d %#x", test.name, d.calls, d.h, test.calls, test.hash)
		}
	}
}

func TestDynamicDepthTransitionsCoverFormat(t *testing.T) {
	for _, format := range []core1_0.Format{core1_0.FormatD32SignedFloat, core1_0.FormatD24UnsignedNormalizedS8UnsignedInt, core1_0.FormatD32SignedFloatS8UnsignedInt} {
		target := depthRenderingTarget(core1_0.Image{}, core1_0.ImageView{}, format, 4, core1_0.Extent2D{Width: 512, Height: 512})
		want := core1_0.ImageAspectDepth
		if format != core1_0.FormatD32SignedFloat {
			want |= core1_0.ImageAspectStencil
		}
		for _, barriers := range [][]core1_0.ImageMemoryBarrier{target.before, target.after} {
			if len(barriers) != 1 || barriers[0].SubresourceRange.AspectMask != want || barriers[0].SubresourceRange.BaseArrayLayer != 4 || barriers[0].SubresourceRange.LayerCount != 1 {
				t.Fatalf("format %v: %#v", format, barriers)
			}
		}
		if target.info.LayerCount != 1 || target.info.ViewMask != 0 || target.info.DepthAttachment.ClearValue != (core1_0.ClearValueDepthStencil{Depth: 1}) {
			t.Fatal("shadow rendering parameters changed")
		}
	}
}
func TestDynamicWaterBindings(t *testing.T) {
	for _, samples := range []core1_0.SampleCountFlags{core1_0.Samples1, core1_0.Samples4} {
		fx := buildFrame(7)
		var err error
		fx.graph, err = newFrameGraph(samples, core1_0.FormatD32SignedFloat, core1_0.FormatB8G8R8A8SRGB, 1)
		if err != nil {
			t.Fatal(err)
		}
		fx.initGraphBindings()
		info := fx.graph.nodes[graphWater].targets[0].info
		if info.RenderArea.Extent != fx.extent || info.LayerCount != 1 || info.ViewMask != 0 || len(info.ColorAttachments) != 1 || info.DepthAttachment == nil {
			t.Fatal("rendering dimensions or attachments differ")
		}
		color := info.ColorAttachments[0]
		if color.LoadOp != core1_0.AttachmentLoadOpLoad || info.DepthAttachment.LoadOp != core1_0.AttachmentLoadOpLoad {
			t.Fatal("water must retain scene color/depth")
		}
		if samples == core1_0.Samples4 {
			if color.ResolveMode != khr_depth_stencil_resolve.ResolveModeAverage || color.ResolveImageView != fx.graph.images[fx.graph.hdr].views[0] {
				t.Fatal("MSAA inline resolve missing")
			}
		} else if color.ResolveImageView.Handle() != 0 || color.ResolveMode != 0 {
			t.Fatal("single-sample rendering has a resolve")
		}
	}
}
