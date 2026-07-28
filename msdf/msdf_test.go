package msdf

import (
	"math"
	"sort"
	"testing"

	"golang.org/x/image/font/gofont/goregular"
	"golang.org/x/image/font/sfnt"
	"golang.org/x/image/math/fixed"
)

// These tests encode what an atlas comparison against msdf-atlas-gen v1.4
// established. Five typefaces chosen to stress the generator -- a neutral sans,
// a Didone with extreme stroke contrast, a connected script, a serif, and a
// very fine-stroked garamond -- agreed with the reference implementation on
// 99.5-99.85% of sampled points, with no disagreement further than a pixel from
// an outline. Each assertion below corresponds to a bug that comparison found.

func generate(t *testing.T, opt Options) *Atlas {
	t.Helper()
	a, err := Generate(goregular.TTF, opt)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	return a
}

func findGlyph(t *testing.T, a *Atlas, r rune) GlyphJSON {
	t.Helper()
	for _, g := range a.Meta.Glyphs {
		if g.Unicode == int(r) {
			return g
		}
	}
	t.Fatalf("glyph %q not in atlas", r)
	return GlyphJSON{}
}

// TestPixelsPerEmMatchesSize is the regression test for the range semantics.
//
// msdf-atlas-gen's -pxrange is the TOTAL width of the distance field, so an
// outline needs half of it on each side. Padding the full range on both sides
// instead, and adding a spare pixel to the cell, inflated every glyph by the
// same factor: a 48px em came out at 50.4 px/em. Nothing looked obviously
// broken -- text still rendered -- but every glyph was 5% oversized against the
// metrics that describe it, which is exactly the sort of error that only shows
// up when mixing an atlas from one generator with metrics from another.
//
// The ratio of atlas pixels to em units is the invariant that pins it down.
func TestPixelsPerEmMatchesSize(t *testing.T) {
	for _, size := range []int{32, 48, 64} {
		a := generate(t, Options{Size: size})

		for _, r := range []rune{'H', 'o', 'g', '8', '@'} {
			g := findGlyph(t, a, r)
			if g.PlaneBounds == nil || g.AtlasBounds == nil {
				t.Fatalf("size %d: glyph %q has no bounds", size, r)
			}

			emW := g.PlaneBounds.Right - g.PlaneBounds.Left
			pxW := g.AtlasBounds.Right - g.AtlasBounds.Left
			emH := g.PlaneBounds.Top - g.PlaneBounds.Bottom
			pxH := g.AtlasBounds.Top - g.AtlasBounds.Bottom

			// Cells are snapped to whole pixels, so the ratio carries up to
			// half a pixel of rounding at each edge -- but not 5%.
			for _, c := range []struct {
				axis   string
				em, px float64
			}{{"x", emW, pxW}, {"y", emH, pxH}} {
				got := c.px / c.em
				if math.Abs(got-float64(size)) > 1.0 {
					t.Errorf("size %d glyph %q %s: %.2f px/em, want %d",
						size, r, c.axis, got, size)
				}
			}
		}
	}
}

// TestDistanceRangeIsTotalWidth guards the other half of the same bug: the
// reported distanceRange must be what was asked for, since the shader divides
// by it to recover screen-space distance.
func TestDistanceRangeIsTotalWidth(t *testing.T) {
	for _, rng := range []float64{2, 4, 8} {
		a := generate(t, Options{Range: rng})
		if a.Meta.Atlas.DistanceRange != rng {
			t.Errorf("Range %g: reported distanceRange %g", rng, a.Meta.Atlas.DistanceRange)
		}
	}
}

// TestNonzeroWindingFillRule is the regression test for the fill rule.
//
// The generator originally used even-odd, which agrees with nonzero only when
// contours do not overlap. TrueType and CFF both define fills by winding, and
// real fonts do overlap contours -- glyphs assembled from components, or
// instances of a variable font. Under even-odd the overlap region inverts, so
// a solid stroke gets a hole punched through it.
//
// This was not a subtle difference where it occurred: it produced 311 sampled
// points per Inter atlas that disagreed with the reference by more than 0.10 in
// the median field, peaking at 0.85 -- solid-vs-empty, not edge noise. Fixing
// the rule took that to zero across all five test faces.
//
// Two overlapping squares wound the same direction: the overlap is inside under
// nonzero, and would be a hole under even-odd.
func TestNonzeroWindingFillRule(t *testing.T) {
	square := func(x0, y0, x1, y1 float64) contour {
		return contour{{x0, y0}, {x1, y0}, {x1, y1}, {x0, y1}}
	}
	cs := []contour{
		square(0, 0, 10, 10),
		square(5, 5, 15, 15), // overlaps the first in [5,10]^2
	}

	cases := []struct {
		name string
		p    vec2
		want bool
	}{
		{"first square only", vec2{2, 2}, true},
		{"second square only", vec2{12, 12}, true},
		{"overlap", vec2{7, 7}, true}, // even-odd would call this outside
		{"outside both", vec2{20, 20}, false},
		{"in neither, between", vec2{12, 2}, false},
	}
	for _, tc := range cases {
		if got := inside(tc.p, cs); got != tc.want {
			t.Errorf("%s: inside(%v) = %v, want %v", tc.name, tc.p, got, tc.want)
		}
	}
}

// TestWindingIgnoresContourDirection checks the counter-wound case: a contour
// inside another and wound the opposite way is a counter, which is how every
// 'o', 'a' and 'B' gets its hole.
func TestWindingIgnoresContourDirection(t *testing.T) {
	outer := contour{{0, 0}, {10, 0}, {10, 10}, {0, 10}}
	inner := contour{{3, 3}, {3, 7}, {7, 7}, {7, 3}} // reverse wound
	cs := []contour{outer, inner}

	if !inside(vec2{1, 5}, cs) {
		t.Error("point in the ring should be inside")
	}
	if inside(vec2{5, 5}, cs) {
		t.Error("point in the counter should be outside")
	}
}

// TestMedianReconstructsGlyph checks the property the shader actually depends
// on: the median of the three channels, thresholded at 0.5, is the glyph.
//
// Any one channel is meaningless on its own -- edge colouring assigns channels
// per smooth run, so which channel holds which distance is arbitrary and
// differs between correct implementations. The median is the invariant.
func TestMedianReconstructsGlyph(t *testing.T) {
	a := generate(t, Options{Size: 48})
	g := findGlyph(t, a, 'H')

	median := func(u, v float64) float64 {
		// u,v in 0..1 across the glyph's cell.
		px := g.AtlasBounds.Left + u*(g.AtlasBounds.Right-g.AtlasBounds.Left)
		// atlasBounds are measured from the bottom; the image is top-down.
		pyFromBottom := g.AtlasBounds.Bottom + v*(g.AtlasBounds.Top-g.AtlasBounds.Bottom)
		py := float64(a.Meta.Atlas.Height) - pyFromBottom

		c := a.Image.RGBAAt(int(px), int(py))
		ch := []float64{float64(c.R) / 255, float64(c.G) / 255, float64(c.B) / 255}
		sort.Float64s(ch)
		return ch[1]
	}

	// 'H' is two stems and a crossbar. The centre of each stem is solidly
	// inside; the gap above the crossbar is solidly outside.
	if m := median(0.1, 0.5); m <= 0.5 {
		t.Errorf("left stem: median %.3f, want > 0.5 (inside)", m)
	}
	if m := median(0.9, 0.5); m <= 0.5 {
		t.Errorf("right stem: median %.3f, want > 0.5 (inside)", m)
	}
	if m := median(0.5, 0.5); m <= 0.5 {
		t.Errorf("crossbar: median %.3f, want > 0.5 (inside)", m)
	}
	if m := median(0.5, 0.9); m >= 0.5 {
		t.Errorf("gap above crossbar: median %.3f, want < 0.5 (outside)", m)
	}
	if m := median(0.5, 0.1); m >= 0.5 {
		t.Errorf("gap below crossbar: median %.3f, want < 0.5 (outside)", m)
	}
}

// TestPlaneBoundsOrientation guards the Y-axis flip.
//
// sfnt reports glyph outlines Y-down with the baseline at zero, while the
// atlas schema is Y-up. Getting this backwards renders every line of text
// upside down, which is obvious on screen but silent in the metrics.
func TestPlaneBoundsOrientation(t *testing.T) {
	a := generate(t, Options{})

	// A cap-height letter sits entirely above the baseline.
	h := findGlyph(t, a, 'H')
	if h.PlaneBounds.Bottom < -0.05 {
		t.Errorf("'H' bottom %.3f: should sit on the baseline, not below",
			h.PlaneBounds.Bottom)
	}
	if h.PlaneBounds.Top <= 0 {
		t.Errorf("'H' top %.3f: should be above the baseline", h.PlaneBounds.Top)
	}

	// A descender goes below it.
	p := findGlyph(t, a, 'p')
	if p.PlaneBounds.Bottom >= 0 {
		t.Errorf("'p' bottom %.3f: descender should be below the baseline",
			p.PlaneBounds.Bottom)
	}

	// And 'H' is taller than 'x'.
	x := findGlyph(t, a, 'x')
	if h.PlaneBounds.Top <= x.PlaneBounds.Top {
		t.Errorf("cap height %.3f should exceed x-height %.3f",
			h.PlaneBounds.Top, x.PlaneBounds.Top)
	}
}

// TestGlyphsFitInAtlas checks the packer never places a cell out of bounds,
// and that whitespace carries an advance but no cell.
func TestGlyphsFitInAtlas(t *testing.T) {
	a := generate(t, Options{})
	w := float64(a.Meta.Atlas.Width)
	h := float64(a.Meta.Atlas.Height)

	var placed int
	for _, g := range a.Meta.Glyphs {
		if g.AtlasBounds == nil {
			// Space has no ink, so no cell -- but it must still advance, or
			// every word runs together.
			if g.Unicode == ' ' && g.Advance <= 0 {
				t.Error("space has no advance")
			}
			continue
		}
		placed++
		b := g.AtlasBounds
		if b.Left < 0 || b.Bottom < 0 || b.Right > w || b.Top > h {
			t.Errorf("glyph %q cell (%.1f,%.1f)-(%.1f,%.1f) outside %gx%g atlas",
				rune(g.Unicode), b.Left, b.Bottom, b.Right, b.Top, w, h)
		}
	}
	if placed == 0 {
		t.Fatal("no glyphs placed")
	}
}

// TestPackingIsReasonablyTight guards against the packer regressing to
// power-of-two sizes, which wasted up to 2.4x the texture memory: 95 glyphs
// that fit in 340x340 were being rounded up to 512x512.
func TestPackingIsReasonablyTight(t *testing.T) {
	a := generate(t, Options{})

	var ink float64
	for _, g := range a.Meta.Glyphs {
		if b := g.AtlasBounds; b != nil {
			ink += (b.Right - b.Left) * (b.Top - b.Bottom)
		}
	}
	total := float64(a.Meta.Atlas.Width * a.Meta.Atlas.Height)
	if used := ink / total; used < 0.60 {
		t.Errorf("packing wastes too much: %.1f%% of %dx%d used",
			100*used, a.Meta.Atlas.Width, a.Meta.Atlas.Height)
	}
}

// TestCharsetIsHonoured checks an explicit charset produces exactly those
// glyphs, since a game shipping a subset atlas depends on it.
func TestCharsetIsHonoured(t *testing.T) {
	want := []rune("Hello, world!")
	a := generate(t, Options{Charset: want})

	got := map[rune]bool{}
	for _, g := range a.Meta.Glyphs {
		got[rune(g.Unicode)] = true
	}

	uniq := map[rune]bool{}
	for _, r := range want {
		uniq[r] = true
	}
	if len(got) != len(uniq) {
		t.Errorf("got %d glyphs, want %d unique", len(got), len(uniq))
	}
	for r := range uniq {
		if !got[r] {
			t.Errorf("charset rune %q missing from atlas", r)
		}
	}
}

// TestGenerateRejectsGarbage checks a bad font is an error rather than a panic
// or a blank atlas, since the input is usually a file the developer chose.
func TestGenerateRejectsGarbage(t *testing.T) {
	for _, tc := range []struct {
		name string
		data []byte
	}{
		{"empty", nil},
		{"truncated", goregular.TTF[:64]},
		{"not a font", []byte("this is definitely not a font file")},
	} {
		if _, err := Generate(tc.data, Options{}); err == nil {
			t.Errorf("%s: want an error, got nil", tc.name)
		}
	}
}

// glyphGeometry mirrors what Generate does to one glyph, so the field tests
// below can work on real outlines rather than synthetic polygons.
func glyphGeometry(t *testing.T, r rune, size int, cornerAngle float64) ([]contour, []edge) {
	t.Helper()
	f, err := sfnt.Parse(goregular.TTF)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	var buf sfnt.Buffer
	ppem := fixed.I(size)
	idx, err := f.GlyphIndex(&buf, r)
	if err != nil || idx == 0 {
		t.Fatalf("glyph %q not in font", r)
	}
	segs, err := f.LoadGlyph(&buf, idx, ppem, nil)
	if err != nil {
		t.Fatalf("load %q: %v", r, err)
	}
	cs := flatten(segs)
	cc := cosDegrees(cornerAngle)
	var es []edge
	for _, c := range cs {
		es = append(es, colorEdges(c, cc)...)
	}
	return cs, es
}

// TestMedianMatchesCoverage is the regression test for the artifact class that
// a whole-atlas comparison nearly missed.
//
// Three independently-signed channels are not guaranteed to median to the
// right answer, and where they fail the damage is local and small: a notch
// bitten out of a stem, a speckle in a counter, a thin ray shooting off a
// corner along an edge's direction. Averaged over a glyph these barely move
// any summary statistic -- an earlier version of this generator agreed with
// msdf-atlas-gen on 99.8% of sampled points while visibly chewing up every
// glyph at small sizes.
//
// So this asserts the invariant pointwise instead of on average: at every
// sample, the median must agree with whether the point is actually inside the
// outline. One bad pixel fails the test.
func TestMedianMatchesCoverage(t *testing.T) {
	const size = 48
	// Letters with corners, curves, counters, diagonals, and thin joins.
	for _, r := range []rune{'x', 'p', 'k', 'B', '8', 'M', '%', '@', 'W', 's', '3'} {
		cs, es := glyphGeometry(t, r, size, 30)
		minX, minY, maxX, maxY, ok := bounds(cs)
		if !ok {
			t.Fatalf("glyph %q has no outline", r)
		}

		const rng = 4.0
		pad := rng / 2

		bad := 0
		var firstX, firstY, firstMed float64
		// Quarter-pixel steps: finer than the atlas, so this also catches
		// artifacts that fall between pixel centres.
		for y := minY - pad; y <= maxY+pad; y += 0.25 {
			for x := minX - pad; x <= maxX+pad; x += 0.25 {
				p := vec2{x, y}
				m := median(sample(p, es, cs, rng))
				in := inside(p, cs)
				if (m > 0.5) == in {
					continue
				}
				// A sample within half a pixel of the outline can legitimately
				// land either side of the threshold.
				if nearOutline(p, es, 0.5) {
					continue
				}
				if bad == 0 {
					firstX, firstY, firstMed = x, y, m
				}
				bad++
			}
		}
		if bad > 0 {
			t.Errorf("glyph %q: %d samples where the median contradicts coverage; "+
				"first at (%.2f,%.2f) median %.3f", r, bad, firstX, firstY, firstMed)
		}
	}
}

// nearOutline reports whether p is within d of any edge.
func nearOutline(p vec2, es []edge, d float64) bool {
	for _, e := range es {
		sd, _ := e.distance(p)
		if math.Abs(sd.dist) <= d {
			return true
		}
	}
	return false
}

// TestCornersStaySharp checks the median actually reconstructs a corner rather
// than rounding it, which is the entire reason for using three channels.
//
// A single-channel field rounds every corner to the field's radius. At the
// outer corner of an 'L' the true shape fills the full quadrant; a rounded one
// leaves a bite out of it. This samples just inside the corner, diagonally,
// where the two constructions differ most.
func TestCornersStaySharp(t *testing.T) {
	cs, es := glyphGeometry(t, 'L', 48, 30)
	_, minY, maxX, _, ok := bounds(cs)
	if !ok {
		t.Fatal("no outline")
	}

	// Walk in from the bottom-right corner of the stem's foot along the
	// diagonal. Every step should still read as inside.
	const rng = 4.0
	for _, d := range []float64{0.75, 1.0, 1.5, 2.0} {
		p := vec2{maxX - d, minY + d}
		if !inside(p, cs) {
			continue // not actually in the foot for this face
		}
		if m := median(sample(p, es, cs, rng)); m <= 0.5 {
			t.Errorf("%.2fpx diagonally inside the corner: median %.3f, want > 0.5 "+
				"(corner was rounded off)", d, m)
		}
	}
}
