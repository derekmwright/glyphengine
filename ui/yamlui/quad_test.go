package yamlui

import (
	"math"
	"testing"

	"github.com/derekmwright/glyphengine/renderer"
)

// The flat quad stream is the half of BuildAt's output that carries no texture
// and no render object of its own: a `bg_color` panel, a non-nine-slice
// progress bar and the scroll_view thumb. Nothing downstream of appendQuad can
// repair a missing UV -- ui.frag reads the interpolated value and has no other
// source for the quad's extent -- so these are UV tests, and
// cmd/flatquadcheck is the frame that says the same thing in bytes.
//
// BROKEN, all six at once, by restoring the emitter issue #144 describes: four
// vertices at the requested corners with no UV field. `go test ./ui/yamlui/`
// printed, trimmed to one line of each kind:
//
//	--- FAIL: TestFlatQuadUVReachesZeroAndOneOnTheRequestedEdge
//	    100x40 at (10,20): u = 0.000 on the right edge, want 1
//	--- FAIL: TestFlatQuadIsGrownHalfAPixel
//	    vertex 0 at (10, 20), want (9.5, 19.5)
//	--- FAIL: TestFlatQuadCoversItsInteriorCompletely
//	    interior (60.5, 40.5): coverage 0.500, want 1
//	    a centre a pixel outside (9.5, 40.5): coverage 0.500, want 0
//	--- FAIL: TestFlatQuadDropsEmptyRects
//	    0x40 emitted 4 verts and 6 indices, want nothing
//	--- FAIL: TestFlatPanelAndProgressBarEmitSpanningUVs
//	    quad 0 (the panel background): u spans [0.0000, 0.0000], want past 0 and past 1
//	--- FAIL: TestProgressBarFillWidthTracksTheValue
//	    value 0: 3 quads, want 2
//
// The coverage lines are the bug in one number: 0.500 everywhere, inside the
// quad and a pixel outside it alike.

// coverage is shaders/ui.frag's edgeCoverage, transcribed:
//
//	vec2 d = min(uv, 1.0 - uv);
//	vec2 w = max(fwidth(uv), vec2(1e-8));
//	vec2 px = d / w;
//	return clamp(min(px.x, px.y) + 0.5, 0.0, 1.0);
//
// fwidth is abs(dFdx) + abs(dFdy), which for an axis-aligned quad whose u only
// varies along x and v only along y is the per-pixel UV step quadAt returns.
func coverage(u, v, du, dv float64) float64 {
	pxX := math.Min(u, 1-u) / math.Max(du, 1e-8)
	pxY := math.Min(v, 1-v) / math.Max(dv, 1e-8)
	return clampFloat(math.Min(pxX, pxY)+0.5, 0, 1)
}

func clampFloat(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// quadAt samples the UV a quad's four vertices interpolate to at a screen
// point, and the per-pixel UV step the rasterizer would hand fwidth.
func quadAt(verts []renderer.Vertex, x, y float64) (u, v, du, dv float64) {
	x0, x1 := float64(verts[0].Pos[0]), float64(verts[1].Pos[0])
	y0, y1 := float64(verts[0].Pos[1]), float64(verts[2].Pos[1])
	u0, u1 := float64(verts[0].UV[0]), float64(verts[1].UV[0])
	v0, v1 := float64(verts[0].UV[1]), float64(verts[2].UV[1])

	dudx := (u1 - u0) / (x1 - x0)
	dvdy := (v1 - v0) / (y1 - y0)
	return u0 + (x-x0)*dudx, v0 + (y-y0)*dvdy, math.Abs(dudx), math.Abs(dvdy)
}

// TestFlatQuadUVReachesZeroAndOneOnTheRequestedEdge is the property ui.frag
// depends on: the geometry is grown half a pixel and the UVs are grown with it
// by the same fraction, so UV 0 and UV 1 still land on the rect the caller
// asked for rather than on the grown one.
func TestFlatQuadUVReachesZeroAndOneOnTheRequestedEdge(t *testing.T) {
	cases := []struct {
		name       string
		x, y, w, h float32
	}{
		{"100x40 at (10,20)", 10, 20, 100, 40},
		{"3x3 at (0,0)", 0, 0, 3, 3},
		{"1x1 at (7.5,2.25)", 7.5, 2.25, 1, 1},
	}
	for _, c := range cases {
		verts, idxs := appendQuad(nil, nil, c.x, c.y, c.w, c.h, [3]float32{1, 0, 0})
		if len(verts) != 4 || len(idxs) != 6 {
			t.Fatalf("%s: emitted %d verts and %d indices, want 4 and 6", c.name, len(verts), len(idxs))
		}

		x, y, w, h := float64(c.x), float64(c.y), float64(c.w), float64(c.h)
		for _, p := range []struct {
			edge         string
			px, py       float64
			wantU, wantV float64
		}{
			{"left", x, y + h/2, 0, 0.5},
			{"right", x + w, y + h/2, 1, 0.5},
			{"top", x + w/2, y, 0.5, 0},
			{"bottom", x + w/2, y + h, 0.5, 1},
		} {
			u, v, _, _ := quadAt(verts, p.px, p.py)
			if math.Abs(u-p.wantU) > 1e-6 {
				t.Errorf("%s: u = %.3f on the %s edge, want %v", c.name, u, p.edge, p.wantU)
			}
			if math.Abs(v-p.wantV) > 1e-6 {
				t.Errorf("%s: v = %.3f on the %s edge, want %v", c.name, v, p.edge, p.wantV)
			}
		}
	}
}

// TestFlatQuadIsGrownHalfAPixel is the skirt itself. The rasterizer only makes
// fragments whose centre is inside the geometry, so without it the outside half
// of the coverage ramp has nowhere to be evaluated and the shape sits half a
// pixel fat.
func TestFlatQuadIsGrownHalfAPixel(t *testing.T) {
	verts, _ := appendQuad(nil, nil, 10, 20, 100, 40, [3]float32{1, 0, 0})
	want := [4][2]float32{{9.5, 19.5}, {110.5, 19.5}, {110.5, 60.5}, {9.5, 60.5}}
	for i, w := range want {
		if verts[i].Pos[0] != w[0] || verts[i].Pos[1] != w[1] {
			t.Errorf("vertex %d at (%v, %v), want (%v, %v)",
				i, verts[i].Pos[0], verts[i].Pos[1], w[0], w[1])
		}
	}
}

// TestFlatQuadCoversItsInteriorCompletely is issue #144 at the one place the
// answer is decided. A quad with no UV hands edgeCoverage a distance of 0 and
// an fwidth clamped to 1e-8, so px is 0 and the ramp lands on its +0.5 for
// every fragment: the flat panel is composited at half alpha edge to edge,
// whatever opacity asked for.
func TestFlatQuadCoversItsInteriorCompletely(t *testing.T) {
	verts, _ := appendQuad(nil, nil, 10, 20, 100, 40, [3]float32{1, 0, 0})

	for _, p := range []struct {
		what string
		x, y float64
		want float64
		tol  float64
	}{
		// Pixel centres well inside. Nothing here may be dimmed at all: a
		// panel at opacity 1 means the requested colour, not the midpoint.
		{"interior", 60.5, 40.5, 1, 0},
		{"interior", 11.5, 21.5, 1, 0},
		{"interior", 108.5, 58.5, 1, 0},
		// A pixel centre exactly on the edge is half covered, which is what
		// antialiasing the edge means and what the skirt buys.
		{"a centre on the left edge", 10, 40.5, 0.5, 1e-6},
		{"a centre on the top edge", 60.5, 20, 0.5, 1e-6},
		// A full pixel beyond it is not covered at all. The tolerance is for
		// the float32 UVs, not for the shape: the skirt's -0.005 comes back as
		// -0.00499999988, which leaves coverage a hundred-millionth above zero.
		{"a centre a pixel outside", 9.5, 40.5, 0, 1e-6},
	} {
		u, v, du, dv := quadAt(verts, p.x, p.y)
		if got := coverage(u, v, du, dv); math.Abs(got-p.want) > p.tol {
			t.Errorf("%s (%v, %v): coverage %.3f, want %v", p.what, p.x, p.y, got, p.want)
		}
	}
}

// TestFlatQuadDropsEmptyRects: an empty progress bar asks for a zero-width
// fill, and a skirt around nothing is a visible one-pixel sliver where the bar
// is supposed to be empty.
func TestFlatQuadDropsEmptyRects(t *testing.T) {
	for _, c := range []struct{ w, h float32 }{{0, 40}, {100, 0}, {0, 0}, {-3, 40}} {
		verts, idxs := appendQuad(nil, nil, 10, 20, c.w, c.h, [3]float32{1, 0, 0})
		if len(verts) != 0 || len(idxs) != 0 {
			t.Errorf("%vx%v emitted %d verts and %d indices, want nothing",
				c.w, c.h, len(verts), len(idxs))
		}
	}
}

const flatQuadYAML = `widget: panel
id: root
width: 200
height: 100
padding: 10
bg_color: [0.05, 0.06, 0.08]
children:
  - widget: progress_bar
    id: bar
    width: 180
    height: 20
    value: "{v}"
    max: "{v_max}"
    bg_color: [0.2, 0.2, 0.2]
    fg_color: [0.9, 0.3, 0.2]
`

// TestFlatPanelAndProgressBarEmitSpanningUVs walks the two widgets the issue
// names through the tree rather than through appendQuad, so a widget that grows
// its own quad emitter later is caught here rather than on a screenshot.
func TestFlatPanelAndProgressBarEmitSpanningUVs(t *testing.T) {
	tree, assets, _ := loadTree(t, flatQuadYAML)
	tree.BindFloat("v", 50)
	tree.BindFloat("v_max", 100)

	_, verts, idxs, _ := tree.BuildAt(nil, assets, 0, 0, 1, 400, 300)

	// The panel background, the bar background and the bar fill.
	if len(verts) != 12 || len(idxs) != 18 {
		t.Fatalf("emitted %d verts and %d indices, want 12 and 18 (three quads)", len(verts), len(idxs))
	}

	names := []string{"the panel background", "the bar background", "the bar fill"}
	for q := range names {
		v := verts[q*4 : q*4+4]
		uMin, uMax := v[0].UV[0], v[1].UV[0]
		vMin, vMax := v[0].UV[1], v[2].UV[1]
		if uMin >= 0 || uMax <= 1 {
			t.Errorf("quad %d (%s): u spans [%.4f, %.4f], want past 0 and past 1", q, names[q], uMin, uMax)
		}
		if vMin >= 0 || vMax <= 1 {
			t.Errorf("quad %d (%s): v spans [%.4f, %.4f], want past 0 and past 1", q, names[q], vMin, vMax)
		}
	}
}

// TestProgressBarFillWidthTracksTheValue: the fill is the flat quad that
// changes size every frame, so its skirt has to be applied to the value's width
// rather than to the widget rect. The half pixel lands on both sides, so the
// emitted quad is always exactly one pixel wider than the fill asked for.
func TestProgressBarFillWidthTracksTheValue(t *testing.T) {
	for _, c := range []struct {
		value     float32
		wantQuads int
		wantFillW float32
	}{
		{0, 2, 0},   // empty: the two backgrounds and no fill at all
		{25, 3, 45}, // a quarter of 180
		{100, 3, 180},
	} {
		tree, assets, _ := loadTree(t, flatQuadYAML)
		tree.BindFloat("v", c.value)
		tree.BindFloat("v_max", 100)
		_, verts, _, _ := tree.BuildAt(nil, assets, 0, 0, 1, 400, 300)

		if got := len(verts) / 4; got != c.wantQuads {
			t.Fatalf("value %v: %d quads, want %d", c.value, got, c.wantQuads)
		}
		if c.wantQuads < 3 {
			continue
		}
		fill := verts[8:12]
		gotW := fill[1].Pos[0] - fill[0].Pos[0] - 2*edgeSkirt
		if math.Abs(float64(gotW-c.wantFillW)) > 1e-4 {
			t.Errorf("value %v: the fill quad is %v wide, want %v", c.value, gotW, c.wantFillW)
		}
	}
}
