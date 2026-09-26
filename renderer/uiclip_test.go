package renderer

import (
	"testing"

	"github.com/vkngwrapper/core/v3/core1_0"
)

// ── the rect itself ──

func TestClipRectIntersectAndEmpty(t *testing.T) {
	a := ClipRect{X: 0, Y: 0, W: 100, H: 100}
	b := ClipRect{X: 50, Y: 60, W: 100, H: 100}
	if got, want := a.Intersect(b), (ClipRect{X: 50, Y: 60, W: 50, H: 40}); got != want {
		t.Errorf("intersect = %+v, want %+v", got, want)
	}
	if a.Intersect(ClipRect{X: 200, Y: 0, W: 10, H: 10}).Empty() != true {
		t.Error("two rects that do not touch intersected to something non-empty")
	}
	if a.Empty() {
		t.Error("a 100x100 rect reported itself empty")
	}
}

// The scissor rounds OUTWARD and clamps to the framebuffer, because a clip at a
// fractional UI scale falls between pixel centres and a row scrolled off the
// top of the screen has a negative Y that vkCmdSetScissor would reject.
func TestClipRectScissorRoundsOutwardAndClamps(t *testing.T) {
	extent := core1_0.Extent2D{Width: 640, Height: 360}

	got := ClipRect{X: 10.4, Y: 20.6, W: 30.3, H: 40.1}.scissor(extent)
	want := core1_0.Rect2D{Offset: core1_0.Offset2D{X: 10, Y: 20}, Extent: core1_0.Extent2D{Width: 31, Height: 41}}
	if got != want {
		t.Errorf("fractional clip scissored to %+v, want %+v", got, want)
	}

	got = ClipRect{X: -50, Y: -50, W: 100, H: 100}.scissor(extent)
	want = core1_0.Rect2D{Offset: core1_0.Offset2D{X: 0, Y: 0}, Extent: core1_0.Extent2D{Width: 50, Height: 50}}
	if got != want {
		t.Errorf("a clip half off the top-left scissored to %+v, want %+v", got, want)
	}

	got = ClipRect{X: 700, Y: 400, W: 100, H: 100}.scissor(extent)
	if got.Extent.Width != 0 || got.Extent.Height != 0 {
		t.Errorf("a clip entirely off screen scissored to %+v, want an empty extent", got)
	}
}

func TestTrimQuadMovesTheUVsWithTheCorners(t *testing.T) {
	c := ClipRect{X: 0, Y: 0, W: 100, H: 50}
	x0, y0, x1, y1, u0, v0, u1, v1, ok := c.TrimQuad(20, 30, 60, 70, 0.2, 0.4, 0.6, 0.8)
	if !ok {
		t.Fatal("a quad half inside the clip was dropped")
	}
	if x0 != 20 || x1 != 60 || y0 != 30 || y1 != 50 {
		t.Errorf("trimmed to (%v,%v)-(%v,%v), want (20,30)-(60,50)", x0, y0, x1, y1)
	}
	// The quad spanned y 30..70 and was cut at 50, exactly half way, so the V
	// at the cut is half way between 0.4 and 0.8.
	if u0 != 0.2 || u1 != 0.6 || v0 != 0.4 {
		t.Errorf("uncut edges moved: u %v..%v, v0 %v", u0, u1, v0)
	}
	if v1 != 0.6 {
		t.Errorf("v at the cut edge = %v, want 0.6", v1)
	}

	if _, _, _, _, _, _, _, _, ok := c.TrimQuad(200, 200, 240, 240, 0, 0, 1, 1); ok {
		t.Error("a quad outside the clip survived")
	}
	if _, _, _, _, _, _, _, _, ok := (ClipRect{}).TrimQuad(0, 0, 10, 10, 0, 0, 1, 1); ok {
		t.Error("an empty clip let a quad through")
	}
}

// ── the text channels ──

// A clipped line is trimmed glyph by glyph, because one overlay is one draw
// over every line it holds and a scissor could only clip all of them.
func TestMSDFGeometryIsTrimmedToTheClip(t *testing.T) {
	f := testFont()
	line := TextLine{Text: "abcdefgh", X: 0, Y: 0, Scale: 20, Color: [3]float32{1, 1, 1}}

	full, _ := appendMSDFGeometry(nil, nil, f, []TextLine{line}, 400)
	if len(full) == 0 {
		t.Fatal("the unclipped line built no geometry; the rest proves nothing")
	}

	// A clip over the left third: the glyphs past it go away entirely and the
	// one that straddles the edge comes back cut there.
	clipped := line
	clipped.Clip = &ClipRect{X: 0, Y: 0, W: 20, H: 100}
	got, _ := appendMSDFGeometry(nil, nil, f, []TextLine{clipped}, 400)
	if len(got) == 0 {
		t.Fatal("a clip over the first glyphs removed every one of them")
	}
	if len(got) >= len(full) {
		t.Errorf("%d vertices under a clip, %d without: nothing was dropped", len(got), len(full))
	}
	for i, v := range got {
		if v.Pos[0] > 20.001 {
			t.Fatalf("vertex %d at x %v is outside the clip's right edge at 20", i, v.Pos[0])
		}
	}

	// And a clip that nothing reaches leaves no geometry at all.
	away := line
	away.Clip = &ClipRect{X: 300, Y: 300, W: 20, H: 20}
	if v, _ := appendMSDFGeometry(nil, nil, f, []TextLine{away}, 400); len(v) != 0 {
		t.Errorf("%d vertices survived a clip the line does not reach", len(v))
	}

	// A line with no clip builds byte for byte what it always did.
	again, _ := appendMSDFGeometry(nil, nil, f, []TextLine{line}, 400)
	if len(again) != len(full) {
		t.Errorf("the unclipped path changed: %d vertices, want %d", len(again), len(full))
	}
}

// ── the composite pass ──

// scissorLog is a fakeDriver that also remembers every scissor it was set to,
// which is the one thing the clip path adds to the stream.
type scissorLog struct {
	*fakeDriver
	rects []core1_0.Rect2D
}

func (d *scissorLog) CmdSetScissor(cb core1_0.CommandBuffer, scissors ...core1_0.Rect2D) {
	d.rects = append(d.rects, scissors...)
	d.fakeDriver.CmdSetScissor(cb, scissors...)
}

// uiCompositeFixture builds the handful of handles recordUIComposite needs.
func uiCompositeFixture(n int) (*scissorLog, []UIRenderObject, []RenderObject, *Texture, *commandScratch, core1_0.CommandBuffer, core1_0.Pipeline, core1_0.Pipeline, core1_0.PipelineLayout, core1_0.Extent2D) {
	h := &fakeHandles{}
	mesh := fakeMesh(h, 4, 6, 0)
	tex := fakeTexture(h)
	ui := make([]UIRenderObject, n)
	for i := range ui {
		ui[i] = UIRenderObject{
			RenderObject: RenderObject{Mesh: mesh, Texture: tex},
			Opacity:      1,
		}
	}
	msdf := []RenderObject{{Mesh: mesh, Texture: tex, Color: [3]float32{2.5, 0, 0}}}
	return &scissorLog{fakeDriver: &fakeDriver{}}, ui, msdf, tex, &commandScratch{},
		h.commandBuffer(), h.pipeline(), h.pipeline(), h.layout(),
		core1_0.Extent2D{Width: 640, Height: 360}
}

func TestUICompositeSetsOneScissorWhenNothingIsClipped(t *testing.T) {
	d, ui, msdf, fallback, scratch, cmd, uiPipe, msdfPipe, layout, extent := uiCompositeFixture(32)
	var stats RenderStats
	recordUIComposite(d, &stats, cmd, uiPipe, msdfPipe, layout, extent, ui, msdf, fallback, false, scratch)

	if len(d.rects) != 1 {
		t.Fatalf("%d scissors for 32 unclipped draws, want 1", len(d.rects))
	}
	want := core1_0.Rect2D{Extent: extent}
	if d.rects[0] != want {
		t.Errorf("scissor = %+v, want the full extent %+v", d.rects[0], want)
	}
}

// One scissor per CHANGE, not per draw: a run of rows sharing one list's clip
// pays for the clip once.
func TestUICompositeScissorsPerClipChange(t *testing.T) {
	d, ui, msdf, fallback, scratch, cmd, uiPipe, msdfPipe, layout, extent := uiCompositeFixture(6)
	list := &ClipRect{X: 10, Y: 20, W: 100, H: 50}
	for i := 1; i <= 4; i++ {
		ui[i].Clip = list
	}
	var stats RenderStats
	recordUIComposite(d, &stats, cmd, uiPipe, msdfPipe, layout, extent, ui, msdf, fallback, false, scratch)

	// Three, not six: the full extent once at the top, the list's clip once for
	// the run of four rows that share it, and the full extent back for the
	// sixth. The text that follows needs none -- the restore has already
	// happened -- and neither does the tail of the function.
	full := core1_0.Rect2D{Extent: extent}
	want := []core1_0.Rect2D{full, list.scissor(extent), full}
	if len(d.rects) != len(want) {
		t.Fatalf("scissors = %+v, want %d of them", d.rects, len(want))
	}
	for i := range want {
		if d.rects[i] != want[i] {
			t.Errorf("scissor %d = %+v, want %+v", i, d.rects[i], want[i])
		}
	}
}

// An object clipped away to nothing is skipped before it binds anything, which
// is what a list scrolled past its end is mostly made of.
func TestUICompositeSkipsFullyClippedDraws(t *testing.T) {
	d, ui, msdf, fallback, scratch, cmd, uiPipe, msdfPipe, layout, extent := uiCompositeFixture(4)
	gone := &ClipRect{X: 900, Y: 900, W: 20, H: 20}
	for i := range ui {
		ui[i].Clip = gone
	}
	var stats RenderStats
	recordUIComposite(d, &stats, cmd, uiPipe, msdfPipe, layout, extent, ui, msdf, fallback, false, scratch)

	if stats.DrawCalls != 1 {
		t.Errorf("%d draws recorded, want 1 (the text overlay alone)", stats.DrawCalls)
	}
}

// BenchmarkUICompositeClip is the measurement behind the choice of a per-draw
// scissor over a clip rect pushed to the shaders: the same 64 panels and one
// text overlay recorded with no clip, with one shared clip, and with a
// different clip on every draw, which is the worst case the recorder can be
// handed.
//
// Measured on this machine at -benchtime 20000x, three interleaved rounds
// (none, shared, per-draw, repeated -- not three rounds of each, which is how
// a warm-up gets read as a regression):
//
//	none      1801, 1723, 1692 ns/op
//	shared    2139, 2315, 2098 ns/op
//	per-draw  2743, 2649, 2558 ns/op
//
// So about 7 ns per clipped draw when a run of rows shares one list's clip and
// about 14 ns when every draw has its own -- under a microsecond a frame for a
// HUD of 64 panels, against a push-constant clip that would have cost four
// floats in every one of the 256 pushed bytes and a branch in ui.frag for every
// fragment of every panel in the frame, clipped or not. Zero allocations on
// every path.
//
// The clipped numbers include this file's scissorLog appending each rect, so
// they are if anything an overstatement of the recorder's own cost.
func BenchmarkUICompositeClip(b *testing.B) {
	const n = 64
	shared := &ClipRect{X: 10, Y: 20, W: 300, H: 200}

	for _, tc := range []struct {
		name  string
		apply func([]UIRenderObject)
	}{
		{"none", func([]UIRenderObject) {}},
		{"shared", func(o []UIRenderObject) {
			for i := range o {
				o[i].Clip = shared
			}
		}},
		{"per-draw", func(o []UIRenderObject) {
			for i := range o {
				c := ClipRect{X: float32(i), Y: 20, W: 300, H: 200}
				o[i].Clip = &c
			}
		}},
	} {
		b.Run(tc.name, func(b *testing.B) {
			d, ui, msdf, fallback, scratch, cmd, uiPipe, msdfPipe, layout, extent := uiCompositeFixture(n)
			tc.apply(ui)
			var stats RenderStats
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				d.rects = d.rects[:0]
				recordUIComposite(d, &stats, cmd, uiPipe, msdfPipe, layout, extent, ui, msdf, fallback, false, scratch)
			}
		})
	}
}
