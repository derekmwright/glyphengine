package yamlui

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/derekmwright/glyphengine/renderer"
)

// parseYAML writes src to a temp file and loads it, so the tests exercise the
// same path a game does rather than a hand-built struct. Several of the
// defaults below (start = -90, opacity = opaque) are applied by UnmarshalYAML
// and only exist on this path.
func parseYAML(t *testing.T, src string) (*WidgetDef, error) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "test.yaml")
	if err := os.WriteFile(path, []byte(src), 0644); err != nil {
		t.Fatal(err)
	}
	return ParseFS(os.DirFS(dir), "test.yaml")
}

func mustParse(t *testing.T, src string) *WidgetDef {
	t.Helper()
	def, err := parseYAML(t, src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return def
}

func TestIndicatorParseEveryField(t *testing.T) {
	def := mustParse(t, `widget: panel
width: 100
height: 100
children:
  - widget: button
    id: slot_1
    state: "{ability_1_active}"
    indicator:
      type: sweep
      direction: counterclockwise
      start: 30
      shape: circle
      fill: elapsed
      value: "{cd}"
      max: "{cd_max}"
      color: "#3366CC"
      opacity: { start: 0.6, end: 0.1 }
`)
	btn := def.Children[0]
	if btn.State != "{ability_1_active}" {
		t.Errorf("state = %q, want {ability_1_active}", btn.State)
	}
	d := btn.Indicator
	if d == nil {
		t.Fatal("indicator is nil")
	}
	got := []struct {
		name string
		have any
		want any
	}{
		{"type", d.Type, IndicatorSweep},
		{"direction", d.Direction, "counterclockwise"},
		{"start", d.Start, float32(30)},
		{"shape", d.Shape, "circle"},
		{"fill", d.Fill, "elapsed"},
		{"value", d.Value, "{cd}"},
		{"max", d.Max, "{cd_max}"},
		{"color", d.Color, "#3366CC"},
		{"opacity.start", d.Opacity.Start, float32(0.6)},
		{"opacity.end", d.Opacity.End, float32(0.1)},
		{"ccw", d.sweepCCW(), true},
		{"circle", d.sweepCircle(), true},
		{"elapsed", d.fillElapsed(), true},
	}
	for _, c := range got {
		if c.have != c.want {
			t.Errorf("%s = %v, want %v", c.name, c.have, c.want)
		}
	}
	if want := [3]float32{0x33 / 255.0, 0x66 / 255.0, 0xCC / 255.0}; d.rgb() != want {
		t.Errorf("rgb = %v, want %v", d.rgb(), want)
	}
}

func TestIndicatorDefaults(t *testing.T) {
	def := mustParse(t, `widget: icon
id: slot
sprite: fireball
indicator:
  type: sweep
  value: "{cd}"
  max: "{cd_max}"
`)
	d := def.Indicator
	// Twelve o'clock, clockwise, hugging the rect, opaque, black, counting down.
	if d.Start != -90 {
		t.Errorf("start = %v, want -90 (twelve o'clock)", d.Start)
	}
	if d.sweepCCW() || d.sweepCircle() || d.fillElapsed() {
		t.Errorf("ccw/circle/elapsed = %v/%v/%v, want all false", d.sweepCCW(), d.sweepCircle(), d.fillElapsed())
	}
	if r := d.ramp(); r.Start != 1 || r.End != 1 {
		t.Errorf("opacity = %v, want an opaque {1, 1}", r)
	}
	if d.rgb() != ([3]float32{}) {
		t.Errorf("rgb = %v, want black", d.rgb())
	}
}

// TestIndicatorZeroOpacityIsOpaque covers the block built in Go rather than
// parsed: SetChildren never runs UnmarshalYAML, so the all-zero opacity it
// leaves behind has to mean the same thing the absent YAML key does. Without
// this an action bar assembled from Go structs draws no indicator at all, and
// nothing anywhere says why.
func TestIndicatorZeroOpacityIsOpaque(t *testing.T) {
	d := &IndicatorDef{Type: IndicatorRoll, Direction: "down", Value: "10", Max: "10"}
	if got := d.alpha(1); got != 1 {
		t.Errorf("alpha at frac 1 = %v, want 1", got)
	}
	explicit := &IndicatorDef{Opacity: IndicatorOpacity{Start: 0.5, End: 0}}
	if got := explicit.alpha(1); got != 0.5 {
		t.Errorf("explicit ramp overridden: alpha = %v, want 0.5", got)
	}
}

func TestIndicatorParseErrors(t *testing.T) {
	// Every one of these has to name the widget, because the message is all a
	// game author gets: the YAML never reaches a renderer to be looked at.
	cases := []struct {
		name string
		body string
		want string
	}{
		{"unknown key", "type: roll\n      direction: down\n      colour: \"#000000\"", `unknown key "colour"`},
		{"missing type", "direction: down", "type is required"},
		{"unknown type", "type: spin\n      direction: down", `unknown type "spin"`},
		{"roll without direction", "type: roll", "direction is required for type roll"},
		{"roll with bad direction", "type: roll\n      direction: clockwise", `unknown direction "clockwise" for type roll`},
		{"sweep with bad direction", "type: sweep\n      direction: down", `unknown direction "down" for type sweep`},
		{"tint with direction", "type: tint\n      direction: down", "direction is not used by type tint"},
		{"tint with fill", "type: tint\n      fill: elapsed", "fill is not used by type tint"},
		{"shape on roll", "type: roll\n      direction: down\n      shape: circle", "shape is only used by type sweep"},
		{"start on roll", "type: roll\n      direction: down\n      start: 30", "start is only used by type sweep"},
		{"bad shape", "type: sweep\n      shape: oval", `unknown shape "oval"`},
		{"bad fill", "type: roll\n      direction: down\n      fill: half", `unknown fill "half"`},
		{"bad color", "type: tint\n      color: \"#12345\"", `color "#12345" is not a #rrggbb colour`},
		{"not a mapping", "[]", "want a mapping"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			src := fmt.Sprintf(`widget: panel
width: 100
height: 100
children:
  - widget: icon
    id: slot_1
    sprite: fireball
    indicator:
      %s
`, c.body)
			_, err := parseYAML(t, src)
			if err == nil {
				t.Fatalf("parsed without error, want %q", c.want)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("error = %v, want it to contain %q", err, c.want)
			}
			if c.name != "not a mapping" && !strings.Contains(err.Error(), `"slot_1"`) {
				// The mapping check runs inside UnmarshalYAML, which cannot see
				// which widget it is decoding into; everything else can and must.
				t.Errorf("error = %v, want it to name the widget slot_1", err)
			}
		})
	}
}

func TestIndicatorOnlyOnSupportedWidgets(t *testing.T) {
	_, err := parseYAML(t, `widget: panel
width: 100
height: 100
children:
  - widget: label
    id: caption
    text: hi
    font_size: 12
    indicator:
      type: tint
`)
	if err == nil || !strings.Contains(err.Error(), "only supported on panel, button and icon") {
		t.Errorf("label with an indicator: err = %v, want a rejection naming the supported widgets", err)
	}
	if err != nil && !strings.Contains(err.Error(), `"caption"`) {
		t.Errorf("err = %v, want it to name the widget caption", err)
	}

	_, err = parseYAML(t, `widget: panel
id: frame
width: 100
height: 100
state: "{on}"
`)
	if err == nil || !strings.Contains(err.Error(), "state is only supported on button and icon") {
		t.Errorf("panel with a state: err = %v, want a rejection", err)
	}
}

// TestIndicatorWidgetWithoutIDIsStillNamed keeps the error useful for the YAML
// that most often carries an indicator: a slot in a grid, written without an id
// because nothing looks it up.
func TestIndicatorWidgetWithoutIDIsStillNamed(t *testing.T) {
	_, err := parseYAML(t, `widget: panel
width: 100
height: 100
children:
  - widget: icon
    sprite: fireball
    indicator:
      type: spin
`)
	if err == nil {
		t.Fatal("parsed without error")
	}
	if !strings.Contains(err.Error(), `of type "icon" with no id`) {
		t.Errorf("error = %v, want it to fall back to the widget type", err)
	}
}

func TestIndicatorFracClamping(t *testing.T) {
	d := &IndicatorDef{Type: IndicatorRoll, Direction: "down", Value: "{v}", Max: "{m}"}
	cases := []struct {
		v, m string
		want float32
		ok   bool
	}{
		{"50", "100", 0.5, true},
		{"150", "100", 1, true},  // over max clamps rather than overflowing the rect
		{"-10", "100", 0, true},  // under zero clamps rather than inverting the wipe
		{"50", "0", 0, false},    // no scale to measure against: nothing to draw
		{"50", "-100", 0, false}, // and a negative max is not a division either
		{"0", "100", 0, true},
	}
	for _, c := range cases {
		got, ok := d.frac(map[string]string{"v": c.v, "m": c.m})
		if got != c.want || ok != c.ok {
			t.Errorf("frac(value=%s, max=%s) = %v, %v; want %v, %v", c.v, c.m, got, ok, c.want, c.ok)
		}
	}
}

func TestIndicatorOpacityInterpolation(t *testing.T) {
	d := &IndicatorDef{Opacity: IndicatorOpacity{Start: 0.6, End: 0.0}}
	for _, c := range []struct{ frac, want float32 }{{1, 0.6}, {0.5, 0.3}, {0, 0}} {
		if got := d.alpha(c.frac); math.Abs(float64(got-c.want)) > 1e-6 {
			t.Errorf("alpha(%v) = %v, want %v", c.frac, got, c.want)
		}
	}

	// A ramp that rises as the value falls is just as valid: a warning that
	// gets more insistent the less time is left.
	rising := &IndicatorDef{Opacity: IndicatorOpacity{Start: 0.1, End: 0.9}}
	if got := rising.alpha(0.5); math.Abs(float64(got-0.5)) > 1e-6 {
		t.Errorf("rising alpha(0.5) = %v, want 0.5", got)
	}
}

func TestIndicatorCoverFill(t *testing.T) {
	remaining := &IndicatorDef{}
	elapsed := &IndicatorDef{Fill: "elapsed"}
	for _, c := range []struct{ frac, rem, ela float32 }{{1, 1, 0}, {0.25, 0.25, 0.75}, {0, 0, 1}} {
		if got := remaining.cover(c.frac); got != c.rem {
			t.Errorf("remaining cover(%v) = %v, want %v", c.frac, got, c.rem)
		}
		if got := elapsed.cover(c.frac); got != c.ela {
			t.Errorf("elapsed cover(%v) = %v, want %v", c.frac, got, c.ela)
		}
	}
}

func TestIndicatorTintMath(t *testing.T) {
	d := &IndicatorDef{Type: IndicatorTint, Color: "#808080"}
	grey := float32(0x80) / 255

	// At alpha 0 the widget is untouched; at 1 it is fully multiplied.
	if got := d.tint([3]float32{1, 1, 1}, 0); got != ([3]float32{1, 1, 1}) {
		t.Errorf("tint at alpha 0 = %v, want the colour unchanged", got)
	}
	if got := d.tint([3]float32{1, 1, 1}, 1); math.Abs(float64(got[0]-grey)) > 1e-6 {
		t.Errorf("tint at alpha 1 = %v, want %v in every channel", got, grey)
	}
	want := 0.5 + 0.5*grey
	if got := d.tint([3]float32{1, 1, 1}, 0.5); math.Abs(float64(got[0]-want)) > 1e-6 {
		t.Errorf("tint at alpha 0.5 = %v, want %v", got[0], want)
	}

	// A red tint on a white widget removes the other two channels entirely.
	red := &IndicatorDef{Type: IndicatorTint, Color: "#FF0000"}
	if got := red.tint([3]float32{1, 1, 1}, 1); got != ([3]float32{1, 0, 0}) {
		t.Errorf("red tint = %v, want {1 0 0}", got)
	}
}

// ── roll geometry ──

// bounds is the axis-aligned box a polygon occupies, which is what a wipe's
// coverage can be stated in for every direction that produces a rectangle.
func bounds(pts []indicatorPoint) Rect {
	if len(pts) == 0 {
		return Rect{}
	}
	minX, minY := pts[0].X, pts[0].Y
	maxX, maxY := minX, minY
	for _, p := range pts[1:] {
		minX, maxX = minf(minX, p.X), maxf(maxX, p.X)
		minY, maxY = minf(minY, p.Y), maxf(maxY, p.Y)
	}
	return Rect{X: minX, Y: minY, W: maxX - minX, H: maxY - minY}
}

func minf(a, b float32) float32 {
	if a < b {
		return a
	}
	return b
}

func maxf(a, b float32) float32 {
	if a > b {
		return a
	}
	return b
}

func rectNear(a, b Rect, eps float32) bool {
	return absf(a.X-b.X) <= eps && absf(a.Y-b.Y) <= eps &&
		absf(a.W-b.W) <= eps && absf(a.H-b.H) <= eps
}

// TestRollCoversFromTheEdgeItPointsFrom is the statement the whole type rests
// on: `down` at half covers the TOP half, because the wipe starts at the edge
// the direction points away from and travels along it. Get this backwards and
// every cooldown in a game runs the wrong way -- which looks deliberate, so
// nothing but an assertion catches it.
//
// BROKEN ONCE to prove it: rollAxisDegrees mapped "down" to 270 instead of 90.
// go test ./ui/yamlui/ printed
//
//	--- FAIL: TestRollCoversFromTheEdgeItPointsFrom
//	    roll down at 0.50: bounds {0 50 100 50}, want {0 0 100 50}
//	    roll down at 0.25: bounds {0 75 100 25}, want {0 0 100 25}
//	--- FAIL: TestIndicatorGeometryUsesTheWidgetRect
//	    wipe covered {120 102 64 32}, want the top half of the slot at {120 70 64 32}
//
// The `direction: 90` rows stayed green, which is the point of testing the word
// and the number side by side: only the word had moved, and the two forms have
// to agree.
func TestRollCoversFromTheEdgeItPointsFrom(t *testing.T) {
	r := Rect{X: 0, Y: 0, W: 100, H: 100}
	cases := []struct {
		dir   string
		deg   float32
		cover float32
		want  Rect
	}{
		{"down", 90, 0.5, Rect{0, 0, 100, 50}},   // from the top edge
		{"up", 270, 0.5, Rect{0, 50, 100, 50}},   // from the bottom edge
		{"right", 0, 0.5, Rect{0, 0, 50, 100}},   // from the left edge
		{"left", 180, 0.5, Rect{50, 0, 50, 100}}, // from the right edge
		{"down", 90, 1, Rect{0, 0, 100, 100}},    // a full value covers the widget
		{"down", 90, 0.25, Rect{0, 0, 100, 25}},  // and shrinks toward the top edge
		{"right", 0, 0.25, Rect{0, 0, 25, 100}},
	}
	for _, c := range cases {
		word := rollPolygon(r, mustRollAxis(t, c.dir), c.cover)
		if got := bounds(word); !rectNear(got, c.want, 1e-3) {
			t.Errorf("roll %s at %.2f: bounds %v, want %v", c.dir, c.cover, got, c.want)
		}
		degrees := rollPolygon(r, c.deg, c.cover)
		if got := bounds(degrees); !rectNear(got, c.want, 1e-3) {
			t.Errorf("roll %v degrees at %.2f: bounds %v, want %v", c.deg, c.cover, got, c.want)
		}
	}
}

// TestRollDiagonal is the reason direction is a number and not four words: a
// diagonal is the same wipe with its axis rotated, not a new type.
//
// At 45 degrees the boundary is perpendicular to (1,1), so the covered region
// is the triangle cut off at the top-left corner. The boundary sits at `cover`
// of the rect's extent PROJECTED onto the axis -- for a 100x100 rect that
// extent is 100*sqrt(2), so a quarter of it puts the cut at x+y = 50 and the
// triangle's legs at half the widget.
func TestRollDiagonal(t *testing.T) {
	r := Rect{X: 0, Y: 0, W: 100, H: 100}
	pts := rollPolygon(r, 45, 0.25)
	if len(pts) != 3 {
		t.Fatalf("45 degrees at 0.25 produced %d points, want a triangle", len(pts))
	}
	if got, want := bounds(pts), (Rect{0, 0, 50, 50}); !rectNear(got, want, 1e-3) {
		t.Errorf("bounds %v, want %v", got, want)
	}
	for _, p := range pts {
		if sum := p.X + p.Y; sum > 50+1e-3 {
			t.Errorf("point %v is past the x+y = 50 boundary", p)
		}
	}

	// Half of a 45-degree wipe is the anti-diagonal through the centre, so the
	// covered region reaches both far corners and its bounds are the whole rect.
	half := rollPolygon(r, 45, 0.5)
	if got, want := bounds(half), r; !rectNear(got, want, 1e-3) {
		t.Errorf("45 degrees at 0.5: bounds %v, want the whole rect %v", got, want)
	}
	if len(half) != 3 {
		t.Errorf("45 degrees at 0.5 produced %d points, want the corner triangle", len(half))
	}
}

func TestRollDrawsNothing(t *testing.T) {
	r := Rect{X: 0, Y: 0, W: 100, H: 100}
	if pts := rollPolygon(r, 90, 0); pts != nil {
		t.Errorf("cover 0 produced %v, want no geometry", pts)
	}
	if pts := rollPolygon(Rect{0, 0, 0, 100}, 90, 0.5); pts != nil {
		t.Errorf("a zero-width widget produced %v, want no geometry", pts)
	}
}

func mustRollAxis(t *testing.T, dir string) float32 {
	t.Helper()
	deg, err := rollAxisDegrees(dir)
	if err != nil {
		t.Fatalf("rollAxisDegrees(%q): %v", dir, err)
	}
	return deg
}

// ── sweep geometry ──

// onPerimeter reports how far a point sits from the widget's own edge, as a
// fraction: 1 is exactly on it.
//
// It is computed from the point alone rather than from the angle that produced
// it, so it cannot agree with a broken perimeterPoint by construction.
func onPerimeter(p indicatorRim, r Rect, circle bool) float32 {
	hw, hh := r.W/2, r.H/2
	dx, dy := p.X-(r.X+hw), p.Y-(r.Y+hh)
	if circle {
		rr := float64(minf(hw, hh))
		return float32(math.Hypot(float64(dx), float64(dy)) / rr)
	}
	return maxf(absf(dx)/hw, absf(dy)/hh)
}

// TestSweepRimIsOnThePerimeter is the check that keeps a square cooldown
// square. A fan whose rim drifts inside the rect leaves an un-darkened border
// around the icon at every angle that is not an axis, which reads as a sloppy
// asset rather than as a bug.
//
// BROKEN ONCE to prove it: perimeterPoint's square branch returned the point at
// t/2 instead of t. go test ./ui/yamlui/ printed
//
//	--- FAIL: TestSweepRimIsOnThePerimeter
//	    sweep square cover 0.25 rim 0: point {50 25} is at 0.500 of the perimeter, want 1
//	    sweep square cover 0.25 rim 1: point {75 25} is at 0.500 of the perimeter, want 1
//	    sweep square cover 0.25 rim 2: point {75 50} is at 0.500 of the perimeter, want 1
//	    ... 28 lines, every rim point of every case including the 160x60 one
//	--- FAIL: TestSweepQuarterFromTwelveIsTheTopRightQuadrant
//	    bounds = {50 25 25 25}, want the top-right quadrant {50 0 50 50}
//	--- FAIL: TestSweepFullTurnCoversTheWidget
//	    corner {0 0} is not on the rim of a full sweep
//
// The monotonic-angle test stayed green throughout: a fan can unwind perfectly
// and still be the wrong size, which is why the two are separate checks.
func TestSweepRimIsOnThePerimeter(t *testing.T) {
	r := Rect{X: 0, Y: 0, W: 100, H: 100}
	cases := []struct {
		name   string
		start  float32
		ccw    bool
		circle bool
		cover  float32
	}{
		{"square cover 0.25", -90, false, false, 0.25},
		{"square cover 0.5", -90, false, false, 0.5},
		{"square cover 1", -90, false, false, 1},
		{"square counterclockwise", -90, true, false, 0.7},
		{"square from three o'clock", 0, false, false, 0.6},
		{"circle cover 0.25", -90, false, true, 0.25},
		{"circle cover 1", -90, false, true, 1},
	}
	for _, c := range cases {
		rim := sweepRim(r, c.start, c.ccw, c.circle, c.cover)
		if len(rim) < 2 {
			t.Fatalf("sweep %s produced %d rim points", c.name, len(rim))
		}
		for i, p := range rim {
			if d := onPerimeter(p, r, c.circle); absf(d-1) > 1e-3 {
				t.Errorf("sweep %s rim %d: point {%g %g} is at %.3f of the perimeter, want 1",
					c.name, i, p.X, p.Y, d)
			}
		}
	}

	// And on a widget that is not square, so an aspect ratio baked into the
	// perimeter function would show up.
	wide := Rect{X: 10, Y: 20, W: 160, H: 60}
	for i, p := range sweepRim(wide, -90, false, false, 0.85) {
		if d := onPerimeter(p, wide, false); absf(d-1) > 1e-3 {
			t.Errorf("wide sweep rim %d: point {%g %g} is at %.3f of the perimeter, want 1", i, p.X, p.Y, d)
		}
	}
}

func TestSweepAnglesAreMonotonic(t *testing.T) {
	r := Rect{X: 0, Y: 0, W: 100, H: 100}
	cases := []struct {
		name   string
		start  float32
		ccw    bool
		circle bool
		cover  float32
	}{
		// 0.6 clockwise from twelve crosses six o'clock, where an atan2 taken
		// after the fact would wrap and report the fan doubling back.
		{"clockwise past six", -90, false, false, 0.6},
		{"counterclockwise past six", -90, true, false, 0.6},
		{"full turn", -90, false, false, 1},
		{"circle full turn", -90, false, true, 1},
		{"from 200 degrees", 200, false, false, 0.9},
	}
	for _, c := range cases {
		rim := sweepRim(r, c.start, c.ccw, c.circle, c.cover)
		for i := 1; i < len(rim); i++ {
			delta := rim[i].Angle - rim[i-1].Angle
			if c.ccw && delta >= 0 {
				t.Errorf("sweep %s: angle %d -> %d went %+.2f, want strictly decreasing",
					c.name, i-1, i, delta)
			}
			if !c.ccw && delta <= 0 {
				t.Errorf("sweep %s: angle %d -> %d went %+.2f, want strictly increasing",
					c.name, i-1, i, delta)
			}
		}
		if got, want := absf(rim[len(rim)-1].Angle-rim[0].Angle), c.cover*360; absf(got-want) > 1e-3 {
			t.Errorf("sweep %s spans %.2f degrees, want %.2f", c.name, got, want)
		}
	}
}

// TestSweepQuarterFromTwelveIsTheTopRightQuadrant is the concrete case the
// whole shape is judged by: a cooldown a quarter of the way round, starting at
// twelve and running clockwise, covers the top-right quadrant of the icon --
// corner included, which is what "clipped to the rect, not a circle" means.
func TestSweepQuarterFromTwelveIsTheTopRightQuadrant(t *testing.T) {
	r := Rect{X: 0, Y: 0, W: 100, H: 100}
	d := &IndicatorDef{Type: IndicatorSweep, Start: -90}
	pts := d.indicatorGeometry(r, 0.25)
	if len(pts) != 4 {
		t.Fatalf("quarter sweep produced %d points, want the centre plus three rim points: %v", len(pts), pts)
	}
	if got, want := pts[0], (indicatorPoint{50, 50}); got != want {
		t.Errorf("hub = %v, want the widget centre %v", got, want)
	}
	if got, want := bounds(pts), (Rect{50, 0, 50, 50}); !rectNear(got, want, 1e-3) {
		t.Errorf("bounds = %v, want the top-right quadrant %v", got, want)
	}
	// The corner itself, not an arc short of it.
	corner := false
	for _, p := range pts {
		if absf(p.X-100) < 1e-3 && absf(p.Y-0) < 1e-3 {
			corner = true
		}
	}
	if !corner {
		t.Errorf("the top-right corner (100, 0) is not on the rim: %v", pts)
	}

	// The circle shape is the same quadrant with the corner cut off, so its
	// bounds stop at the inscribed radius.
	round := &IndicatorDef{Type: IndicatorSweep, Start: -90, Shape: "circle"}
	if got, want := bounds(round.indicatorGeometry(r, 0.25)), (Rect{50, 0, 50, 50}); !rectNear(got, want, 1e-3) {
		t.Errorf("circle bounds = %v, want %v", got, want)
	}
	for _, p := range round.indicatorGeometry(r, 0.25)[1:] {
		dx, dy := float64(p.X-50), float64(p.Y-50)
		if d := math.Hypot(dx, dy); math.Abs(d-50) > 1e-3 {
			t.Errorf("circle rim point %v is at radius %.3f, want 50", p, d)
		}
	}
}

func TestSweepFullTurnCoversTheWidget(t *testing.T) {
	r := Rect{X: 0, Y: 0, W: 100, H: 100}
	d := &IndicatorDef{Type: IndicatorSweep, Start: -90}
	pts := d.indicatorGeometry(r, 1)
	if got, want := bounds(pts), r; !rectNear(got, want, 1e-3) {
		t.Errorf("full sweep bounds = %v, want the whole widget %v", got, want)
	}
	// All four corners have to be on the rim, or the fan leaves wedges of the
	// icon showing at full cooldown.
	for _, want := range []indicatorPoint{{0, 0}, {100, 0}, {100, 100}, {0, 100}} {
		found := false
		for _, p := range pts {
			if absf(p.X-want.X) < 1e-3 && absf(p.Y-want.Y) < 1e-3 {
				found = true
			}
		}
		if !found {
			t.Errorf("corner %v is not on the rim of a full sweep", want)
		}
	}
}

func TestSweepDrawsNothing(t *testing.T) {
	d := &IndicatorDef{Type: IndicatorSweep, Start: -90}
	if pts := d.indicatorGeometry(Rect{0, 0, 100, 100}, 0); pts != nil {
		t.Errorf("cover 0 produced %v, want no geometry", pts)
	}
}

// ── the render path ──

// builderLog records what each injected builder was asked for, in the order it
// was asked, so the draw order inside a widget is observable from outside.
type builderLog struct {
	kinds  []string
	colors [][3]float32
	verts  []renderer.Vertex
	alpha  float32
}

func (l *builderLog) install(tree *WidgetTree) {
	tree.PanelFn = func(r *renderer.Renderer, slice *renderer.NineSlice, color [3]float32, opacity float32, x, y, w, h, scale, sw, sh float32) []renderer.UIRenderObject {
		l.kinds = append(l.kinds, "panel")
		l.colors = append(l.colors, color)
		return []renderer.UIRenderObject{{RenderObject: renderer.RenderObject{Color: color}, Opacity: opacity}}
	}
	tree.IconFn = func(name string, color [3]float32, opacity float32, x, y, w, h, sw, sh float32) []renderer.UIRenderObject {
		l.kinds = append(l.kinds, "sprite")
		l.colors = append(l.colors, color)
		return []renderer.UIRenderObject{{RenderObject: renderer.RenderObject{Color: color}, Opacity: opacity, TextureMode: true}}
	}
	tree.ShapeFn = func(verts []renderer.Vertex, idxs []uint16, opacity float32) []renderer.UIRenderObject {
		l.kinds = append(l.kinds, "indicator")
		l.verts = verts
		l.alpha = opacity
		var col [3]float32
		if len(verts) > 0 {
			col = verts[0].Color
		}
		l.colors = append(l.colors, col)
		return []renderer.UIRenderObject{{RenderObject: renderer.RenderObject{Color: col}, Opacity: opacity}}
	}
}

func loadTree(t *testing.T, src string) (*WidgetTree, *AssetProvider, *builderLog) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "ui.yaml")
	if err := os.WriteFile(path, []byte(src), 0644); err != nil {
		t.Fatal(err)
	}
	tree, err := Load(os.DirFS(dir), "ui.yaml")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	log := &builderLog{}
	log.install(tree)
	assets := &AssetProvider{NineSlices: map[string]*renderer.NineSlice{"slot": {}}}
	return tree, assets, log
}

const drawOrderYAML = `widget: panel
id: root
width: 100
height: 100
nine_slice: slot
color: [1, 1, 1]
children:
  - widget: icon
    id: slot
    sprite: fireball
    width: 64
    height: 64
    color: [1, 1, 1]
    indicator:
      type: sweep
      value: "{cd}"
      max: "{cd_max}"
      color: "#000000"
      opacity: { start: 0.6, end: 0.0 }
  - widget: label
    id: caption
    overlay: slot
    text: "READY"
    font_size: 12
`

// TestIndicatorDrawsBetweenSpriteAndText pins the order the issue asks for:
// the widget, then its sprite, then the indicator over it, and the text last of
// all. Text is in a stream of its own that is composited after the panels, so
// the half this can get wrong is the indicator and the sprite -- and getting it
// wrong hides the cooldown under the icon it is supposed to cover, on a frame
// that still looks plausible.
//
// BROKEN ONCE to prove it: renderIcon called renderIndicator before appending
// the sprite's objects. go test ./ui/yamlui/ printed
//
//	--- FAIL: TestIndicatorDrawsBetweenSpriteAndText
//	    draw order = [panel indicator sprite], want [panel sprite indicator]
//	    panel 1 is the indicator, want the sprite over the widget
//	    panel 2 is the sprite, want the indicator over it
//	--- FAIL: TestIndicatorAlphaReachesTheRenderObject
//	    value 10: render object opacity = 1, want 0.6
//	--- FAIL: TestStateDimsIndependentlyOfTheIndicator
//	    sprite colour = [0 0 0], want it dimmed to [0.45 0.45 0.45]
//
// The last two are the same swap read through the last object in the slice --
// which is the sprite once the order is wrong, so its opacity and its colour
// are the indicator's.
func TestIndicatorDrawsBetweenSpriteAndText(t *testing.T) {
	tree, assets, log := loadTree(t, drawOrderYAML)
	tree.BindFloat("cd", 5)
	tree.BindFloat("cd_max", 10)

	panels, _, _, text := tree.BuildAt(nil, assets, 0, 0, 1, 200, 200)

	if got, want := strings.Join(log.kinds, " "), "panel sprite indicator"; got != want {
		t.Errorf("draw order = [%s], want [%s]", got, want)
	}
	if len(panels) != 3 {
		t.Fatalf("panels = %d objects, want 3", len(panels))
	}
	if !panels[1].TextureMode {
		t.Errorf("panel 1 is the indicator, want the sprite over the widget")
	}
	if panels[2].TextureMode {
		t.Errorf("panel 2 is the sprite, want the indicator over it")
	}
	// The label is in the text stream, which the renderer composites after
	// every panel; nothing this package draws can land on top of it.
	if len(text) != 1 || text[0].Text != "READY" {
		t.Fatalf("text = %v, want the single label READY", text)
	}
}

// TestIndicatorFracZeroIsIdenticalToNoIndicator is the property the pixel gate
// measures on real frames: a finished cooldown must leave the widget exactly as
// it would be with no indicator block at all, not almost.
func TestIndicatorFracZeroIsIdenticalToNoIndicator(t *testing.T) {
	withInd, assets, log := loadTree(t, drawOrderYAML)
	withInd.BindFloat("cd", 0)
	withInd.BindFloat("cd_max", 10)
	a, av, ai, at := withInd.BuildAt(nil, assets, 0, 0, 1, 200, 200)
	if got := strings.Join(log.kinds, " "); got != "panel sprite" {
		t.Errorf("at frac 0 the draw order was [%s], want the indicator absent", got)
	}

	plain := strings.ReplaceAll(drawOrderYAML, `    indicator:
      type: sweep
      value: "{cd}"
      max: "{cd_max}"
      color: "#000000"
      opacity: { start: 0.6, end: 0.0 }
`, "")
	noInd, assets2, _ := loadTree(t, plain)
	noInd.BindFloat("cd", 0)
	noInd.BindFloat("cd_max", 10)
	b, bv, bi, bt := noInd.BuildAt(nil, assets2, 0, 0, 1, 200, 200)

	if len(a) != len(b) || len(av) != len(bv) || len(ai) != len(bi) || len(at) != len(bt) {
		t.Fatalf("stream lengths differ: %d/%d/%d/%d vs %d/%d/%d/%d",
			len(a), len(av), len(ai), len(at), len(b), len(bv), len(bi), len(bt))
	}
	for i := range a {
		if a[i].Color != b[i].Color || a[i].Opacity != b[i].Opacity || a[i].TextureMode != b[i].TextureMode {
			t.Errorf("panel %d differs: %+v vs %+v", i, a[i], b[i])
		}
	}

	// A max of zero has to be the same, because it is what a game carries
	// before it has bound anything at all.
	unbound, assets3, log3 := loadTree(t, drawOrderYAML)
	if _, _, _, _ = unbound.BuildAt(nil, assets3, 0, 0, 1, 200, 200); strings.Join(log3.kinds, " ") != "panel sprite" {
		t.Errorf("with nothing bound the draw order was [%s], want the indicator absent",
			strings.Join(log3.kinds, " "))
	}
}

func TestIndicatorAlphaReachesTheRenderObject(t *testing.T) {
	tree, assets, log := loadTree(t, drawOrderYAML)
	tree.BindFloat("cd_max", 10)
	for _, c := range []struct{ value, want float32 }{{10, 0.6}, {5, 0.3}} {
		log.kinds = nil
		tree.BindFloat("cd", c.value)
		panels, _, _, _ := tree.BuildAt(nil, assets, 0, 0, 1, 200, 200)
		if math.Abs(float64(log.alpha-c.want)) > 1e-6 {
			t.Errorf("value %v: ShapeFn opacity = %v, want %v", c.value, log.alpha, c.want)
		}
		if math.Abs(float64(panels[len(panels)-1].Opacity-c.want)) > 1e-6 {
			t.Errorf("value %v: render object opacity = %v, want %v", c.value, panels[len(panels)-1].Opacity, c.want)
		}
	}
}

func TestTintAddsNoGeometry(t *testing.T) {
	tree, assets, log := loadTree(t, `widget: panel
id: root
width: 100
height: 100
nine_slice: slot
color: [1, 1, 1]
children:
  - widget: icon
    id: slot
    sprite: fireball
    width: 64
    height: 64
    color: [1, 1, 1]
    indicator:
      type: tint
      value: "{cd}"
      max: "{cd_max}"
      color: "#FF0000"
      opacity: { start: 1, end: 0 }
`)
	tree.BindFloat("cd", 10)
	tree.BindFloat("cd_max", 10)
	panels, verts, idxs, _ := tree.BuildAt(nil, assets, 0, 0, 1, 200, 200)

	if got := strings.Join(log.kinds, " "); got != "panel sprite" {
		t.Errorf("draw order = [%s], want no indicator geometry at all", got)
	}
	if len(verts) != 0 || len(idxs) != 0 {
		t.Errorf("tint emitted %d verts and %d indices, want none", len(verts), len(idxs))
	}
	if len(panels) != 2 {
		t.Fatalf("panels = %d, want the widget and its sprite only", len(panels))
	}
	// The sprite's white is fully multiplied by the red at frac 1.
	if got, want := panels[1].Color, ([3]float32{1, 0, 0}); got != want {
		t.Errorf("sprite colour = %v, want %v", got, want)
	}

	// At frac 0 the alpha has run down to 0 and the widget is untouched.
	tree.BindFloat("cd", 0)
	panels, _, _, _ = tree.BuildAt(nil, assets, 0, 0, 1, 200, 200)
	if got, want := panels[1].Color, ([3]float32{1, 1, 1}); got != want {
		t.Errorf("sprite colour at frac 0 = %v, want it untouched %v", got, want)
	}
}

func TestStateDimsTheWidget(t *testing.T) {
	tree, assets, _ := loadTree(t, `widget: panel
id: root
width: 100
height: 100
nine_slice: slot
color: [1, 1, 1]
children:
  - widget: icon
    id: slot
    sprite: fireball
    width: 64
    height: 64
    color: [1, 1, 1]
    state: "{active}"
`)
	tree.Bind("active", "true")
	panels, _, _, _ := tree.BuildAt(nil, assets, 0, 0, 1, 200, 200)
	if got, want := panels[1].Color, ([3]float32{1, 1, 1}); got != want {
		t.Errorf("active sprite colour = %v, want %v", got, want)
	}

	tree.Bind("active", "false")
	panels, _, _, _ = tree.BuildAt(nil, assets, 0, 0, 1, 200, 200)
	if got, want := panels[1].Color, ([3]float32{stateDim, stateDim, stateDim}); got != want {
		t.Errorf("inactive sprite colour = %v, want %v", got, want)
	}

	// A widget with no state binding at all is not dimmed: silence is not off.
	plain, assets2, _ := loadTree(t, `widget: panel
id: root
width: 100
height: 100
nine_slice: slot
color: [1, 1, 1]
children:
  - widget: icon
    id: slot
    sprite: fireball
    width: 64
    height: 64
    color: [1, 1, 1]
`)
	panels, _, _, _ = plain.BuildAt(nil, assets2, 0, 0, 1, 200, 200)
	if got, want := panels[1].Color, ([3]float32{1, 1, 1}); got != want {
		t.Errorf("sprite with no state = %v, want %v", got, want)
	}
}

// TestStateDimsIndependentlyOfTheIndicator: an inactive ability still shows its
// cooldown. The two are separate statements about the same widget and one must
// not swallow the other.
func TestStateDimsIndependentlyOfTheIndicator(t *testing.T) {
	tree, assets, log := loadTree(t, `widget: panel
id: root
width: 100
height: 100
nine_slice: slot
color: [1, 1, 1]
children:
  - widget: icon
    id: slot
    sprite: fireball
    width: 64
    height: 64
    color: [1, 1, 1]
    state: "{active}"
    indicator:
      type: roll
      direction: down
      value: "{cd}"
      max: "{cd_max}"
      color: "#000000"
`)
	tree.Bind("active", "false")
	tree.BindFloat("cd", 5)
	tree.BindFloat("cd_max", 10)
	panels, _, _, _ := tree.BuildAt(nil, assets, 0, 0, 1, 200, 200)

	if got := strings.Join(log.kinds, " "); got != "panel sprite indicator" {
		t.Errorf("draw order = [%s], want the indicator drawn while the state is off", got)
	}
	if got, want := panels[1].Color, ([3]float32{stateDim, stateDim, stateDim}); got != want {
		t.Errorf("sprite colour = %v, want it dimmed to %v", got, want)
	}
}

// TestIndicatorGeometryUsesTheWidgetRect: the wipe has to land on the widget
// wherever layout put it, not at the origin.
func TestIndicatorGeometryUsesTheWidgetRect(t *testing.T) {
	tree, assets, log := loadTree(t, `widget: panel
id: root
width: 200
height: 200
padding: 20
nine_slice: slot
color: [1, 1, 1]
children:
  - widget: icon
    id: slot
    sprite: fireball
    width: 64
    height: 64
    color: [1, 1, 1]
    indicator:
      type: roll
      direction: down
      value: "{cd}"
      max: "{cd_max}"
      color: "#000000"
`)
	tree.BindFloat("cd", 5)
	tree.BindFloat("cd_max", 10)
	tree.BuildAt(nil, assets, 100, 50, 1, 400, 400)

	slot := tree.NodeByID("slot").Rect
	var minX, minY, maxX, maxY float32 = math.MaxFloat32, math.MaxFloat32, -math.MaxFloat32, -math.MaxFloat32
	for _, v := range log.verts {
		minX, maxX = minf(minX, v.Pos[0]), maxf(maxX, v.Pos[0])
		minY, maxY = minf(minY, v.Pos[1]), maxf(maxY, v.Pos[1])
	}
	want := Rect{X: slot.X, Y: slot.Y, W: slot.W, H: slot.H / 2}
	got := Rect{X: minX, Y: minY, W: maxX - minX, H: maxY - minY}
	if !rectNear(got, want, 1e-3) {
		t.Errorf("wipe covered %v, want the top half of the slot at %v", got, want)
	}
}

// TestIndicatorWithoutShapeFn: an indicator needs its builder, the same way a
// sprite needs IconFn. Drawing nothing is the documented outcome, not a panic.
func TestIndicatorWithoutShapeFn(t *testing.T) {
	tree, assets, _ := loadTree(t, drawOrderYAML)
	tree.ShapeFn = nil
	tree.BindFloat("cd", 5)
	tree.BindFloat("cd_max", 10)
	panels, _, _, _ := tree.BuildAt(nil, assets, 0, 0, 1, 200, 200)
	if len(panels) != 2 {
		t.Errorf("panels = %d, want the widget and its sprite only", len(panels))
	}
}

// TestDoubledBraceTemplate: the schema as filed spells its bindings {{key}}.
// Resolved single-brace-first that leaves {5} behind, which parses as zero --
// a cooldown that never runs and never errors.
func TestDoubledBraceTemplate(t *testing.T) {
	b := map[string]string{"cd": "5"}
	if got := resolve("{{cd}}", b); got != "5" {
		t.Errorf("resolve({{cd}}) = %q, want 5", got)
	}
	if got := resolveFloat("{{cd}}", b); got != 5 {
		t.Errorf("resolveFloat({{cd}}) = %v, want 5", got)
	}
	if got := resolveBool("{{on}}", map[string]string{"on": "true"}); !got {
		t.Error("resolveBool({{on}}) = false, want true")
	}
	if got := resolve("{cd}", b); got != "5" {
		t.Errorf("the single-brace form still resolves: got %q", got)
	}
}
