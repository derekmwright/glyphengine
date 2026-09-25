package ui

import (
	"testing"

	"github.com/derekmwright/glyphengine/renderer"
)

// newTestButton is a button with a layer but no GPU mesh behind it, which is
// as far as this package can build one without a device. That is enough for
// everything below: renderer.Panel.Rebuild reads the panel's fields and the
// layer's colour, and both are written by Build before it is called.
func newTestButton() (*Button, *renderer.Renderer) {
	p := renderer.NewPanel()
	p.Layers = append(p.Layers, &renderer.PanelLayer{
		NineSlice: renderer.NewNineSlice(nil, 48, 16),
		Color:     [3]float32{0.8, 0.7, 0.3},
		Opacity:   1,
	})
	b := &Button{
		panel:      p,
		fillColor:  [3]float32{0.8, 0.7, 0.3},
		LabelColor: [3]float32{1, 1, 1},
		FontSize:   12,
		Anchor:     AnchorCenter,
		Width:      160,
		Height:     40,
		Label:      "CONTINUE",
	}
	return b, &renderer.Renderer{}
}

func testFont() *renderer.Font {
	return &renderer.Font{Glyphs: map[rune]renderer.Glyph{}, PxPerEM: 32, PxRange: 4}
}

// watched is the part of the panel renderer.Panel.Rebuild compares against its
// last build. A Button input that does not move one of these is an input the
// rebuild will not notice.
type watched struct {
	x, y, w, h, scale float32
	color             [3]float32
}

func watch(b *Button) watched {
	return watched{
		x: b.panel.X, y: b.panel.Y, w: b.panel.Width, h: b.panel.Height,
		scale: b.panel.Scale, color: b.panel.Layers[0].Color,
	}
}

// TestButtonStateReachesTheRebuildInputs: every Button input that should
// change the panel's shape or colour has to land in a field Panel.Rebuild
// watches, or the skip rule will hold the old geometry forever. A button stuck
// showing its unhighlighted colour under the pointer is the visible half of
// that; a button that ignores a window resize is the other.
//
// Verified to fail: with `b.panel.Scale = borderScale(scale)` deleted from
// Build, the "border scale alone" case reports "changing border scale alone
// moved nothing the rebuild watches", the panel still carrying NewPanel's
// default 2. Only that case catches it, because every other way of changing
// the display scale moves the resolved bounds as well.
func TestButtonStateReachesTheRebuildInputs(t *testing.T) {
	font := testFont()

	for _, c := range []struct {
		name          string
		change        func(b *Button)
		scale, sw, sh float32
	}{
		{"anchor", func(b *Button) { b.Anchor = AnchorTopLeft }, 1, 1280, 720},
		{"x offset", func(b *Button) { b.OffsetX = 40 }, 1, 1280, 720},
		{"y offset", func(b *Button) { b.OffsetY = 40 }, 1, 1280, 720},
		{"width", func(b *Button) { b.Width = 200 }, 1, 1280, 720},
		{"height", func(b *Button) { b.Height = 56 }, 1, 1280, 720},
		{"fill colour", func(b *Button) { b.SetFillColor([3]float32{0.2, 0.4, 0.6}) }, 1, 1280, 720},
		{"hover", func(b *Button) { b.SetHighlighted(true) }, 1, 1280, 720},
		{"press", func(b *Button) { b.OnMouseDown() }, 1, 1280, 720},
		{"disable", func(b *Button) { b.Disabled = true }, 1, 1280, 720},
		{"scale", func(b *Button) {}, 2, 1280, 720},
		// A button a quarter the size on a display four times as dense covers
		// the same pixels, so every other input holds still and the border
		// scale is the only thing left that can move. borderScale is a square
		// root, which is why it does not cancel.
		{"border scale alone", func(b *Button) { b.Width, b.Height = 40, 10 }, 4, 1280, 720},
		{"screen width", func(b *Button) {}, 1, 1920, 720},
		{"screen height", func(b *Button) {}, 1, 1280, 1080},
	} {
		t.Run(c.name, func(t *testing.T) {
			b, r := newTestButton()
			b.Build(r, 1, 1280, 720, font)
			before := watch(b)

			c.change(b)
			b.Build(r, c.scale, c.sw, c.sh, font)
			if watch(b) == before {
				t.Errorf("changing %s moved nothing the rebuild watches: still %+v", c.name, before)
			}
		})
	}
}

// TestButtonBuildIsStableWhenNothingChanges: the other side of the same rule.
// A Build that produces different inputs from the same state would rebuild
// every frame however good the comparison in Rebuild is, which is the bug
// issue #137 is about.
func TestButtonBuildIsStableWhenNothingChanges(t *testing.T) {
	b, r := newTestButton()
	font := testFont()

	b.Build(r, 1.5, 1280, 720, font)
	first := watch(b)
	for i := range 4 {
		b.Build(r, 1.5, 1280, 720, font)
		if got := watch(b); got != first {
			t.Fatalf("build %d produced %+v from unchanged state, want %+v", i+2, got, first)
		}
	}
}

// TestButtonBuildSteadyStateAllocatesNothing covers Build's own share of the
// per-frame cost -- the label line and the anchor maths -- with the panel's
// share measured against a real mesh in
// renderer.TestPanelSteadyStateAllocatesNothing.
//
// Verified to fail: with the label put back to `text = append(text,
// renderer.TextLine{...})` this reports 1 allocation per frame, and with the
// skip deleted from renderer.Panel.Rebuild it reports 1 as well (the error
// UpdateMeshData returns for a layer with no mesh).
func TestButtonBuildSteadyStateAllocatesNothing(t *testing.T) {
	b, r := newTestButton()
	font := testFont()
	b.Build(r, 1, 1280, 720, font)

	n := testing.AllocsPerRun(200, func() {
		b.Build(r, 1, 1280, 720, font)
	})
	if n != 0 {
		t.Errorf("a steady-state Build allocated %v times per frame, want 0", n)
	}

	_, text := b.Build(r, 1, 1280, 720, font)
	if len(text) != 1 || text[0].Text != "CONTINUE" {
		t.Fatalf("the fixture stopped producing its label (%+v), so the measurement above covered nothing", text)
	}
}

// BenchmarkButtonBuildSteadyState is the measurement behind the docs: bytes
// per frame for one button that is not changing, which is what a HUD costs
// between one interaction and the next.
func BenchmarkButtonBuildSteadyState(bench *testing.B) {
	b, r := newTestButton()
	font := testFont()
	b.Build(r, 1, 1280, 720, font)

	bench.ReportAllocs()
	bench.ResetTimer()
	for range bench.N {
		b.Build(r, 1, 1280, 720, font)
	}
}
