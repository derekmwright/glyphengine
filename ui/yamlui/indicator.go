package yamlui

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/derekmwright/glyphengine/renderer"
	"gopkg.in/yaml.v3"
)

// Indicator types.
const (
	IndicatorRoll  = "roll"  // linear wipe across the widget rect
	IndicatorSweep = "sweep" // radial fan from the widget centre
	IndicatorTint  = "tint"  // colour multiply, no geometry
)

// stateDim is what a false `state` multiplies a widget's tint by.
//
// 0.45 rather than the 0.4 `disabled` uses, because the two mean different
// things and a reader has to be able to tell them apart on screen: `disabled`
// says the widget cannot be used at all, `state` says a toggle is off. They are
// deliberately close -- both read as "not now" -- but not the same value.
const stateDim = 0.45

// sweepStepDeg is the largest angle a round sweep's rim may skip.
//
// Only `shape: circle` needs it. A square sweep's perimeter is four straight
// lines, so putting a rim point at every corner inside the arc reproduces it
// exactly and any extra point is on a segment that already existed. A circle
// has no such structure, and 6 degrees puts 60 segments around a full turn --
// under a third of a pixel of chord error on a 64px icon, which is below what
// the coverage ramp can show.
const sweepStepDeg = 6.0

// IndicatorOpacity is the alpha ramp an indicator runs as its value falls.
//
// Start is the alpha at frac 1 and End the alpha at frac 0, so a cooldown that
// fades out as it expires is the natural way round: {start: 0.6, end: 0.0}.
type IndicatorOpacity struct {
	Start float32 `yaml:"start"`
	End   float32 `yaml:"end"`
}

// IndicatorDef is a driven overlay effect attached to a widget: a cooldown
// sweep, a wipe, or a tint, bound to a value the game updates every frame.
//
// It is deliberately a separate block rather than more fields on the widget.
// `type` says what shape the effect is and `direction` says where it goes, so a
// diagonal wipe is a number instead of a new type, and a widget without an
// indicator carries a nil pointer and none of the fields.
type IndicatorDef struct {
	Type      string           `yaml:"type"`
	Direction string           `yaml:"direction"`
	Start     float32          `yaml:"start"`
	Shape     string           `yaml:"shape"`
	Fill      string           `yaml:"fill"`
	Value     string           `yaml:"value"`
	Max       string           `yaml:"max"`
	Color     string           `yaml:"color"`
	Opacity   IndicatorOpacity `yaml:"opacity"`

	// Everything below is filled in by UnmarshalYAML, so that a bad block is
	// rejected once at load rather than producing a quietly wrong shape on
	// every frame.

	// unknown holds keys the block carried that this schema does not define.
	// They are collected here rather than rejected in UnmarshalYAML because
	// the error has to name the widget, and a yaml.Unmarshaler has no way to
	// see which widget it is being decoded into.
	unknown []string

	// startSet and opacitySet record whether the YAML said anything, so that
	// `start: 0` (three o'clock, a real request) can be told from an absent
	// `start`, which defaults to twelve.
	//
	// Nothing else is cached from these strings. The words are re-resolved on
	// every draw instead, which costs a switch and a six-digit ParseUint per
	// indicator per frame and buys the property that a block built in Go
	// through SetChildren behaves exactly like the same block written in YAML.
	// A cached form would be correct only for the YAML path and silently wrong
	// for the other -- the failure would be a cooldown that wipes the wrong way
	// on a dynamically built action bar and nowhere else.
	startSet   bool
	opacitySet bool
}

// indicatorKeys is the complete set of keys an indicator block may carry.
// A key outside it is a load error rather than a silently ignored line: the
// whole point of the block is that a game drives it from outside the engine,
// and a typo in `direction` that renders a default wipe is the kind of failure
// nobody finds until a screenshot looks wrong.
var indicatorKeys = map[string]bool{
	"type": true, "direction": true, "start": true, "shape": true,
	"fill": true, "value": true, "max": true, "color": true, "opacity": true,
}

// UnmarshalYAML decodes an indicator block, noting which optional keys were
// present and which keys are not part of the schema at all.
func (d *IndicatorDef) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind != yaml.MappingNode {
		return fmt.Errorf("indicator: want a mapping, got %s", nodeKindName(node.Kind))
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		key := node.Content[i].Value
		if !indicatorKeys[key] {
			d.unknown = append(d.unknown, key)
			continue
		}
		switch key {
		case "start":
			d.startSet = true
		case "opacity":
			d.opacitySet = true
		}
	}

	// A plain alias, so the decode below uses the struct tags and does not
	// re-enter this method.
	type plain IndicatorDef
	var p plain
	if err := node.Decode(&p); err != nil {
		return err
	}
	unknown, startSet, opacitySet := d.unknown, d.startSet, d.opacitySet
	*d = IndicatorDef(p)
	d.unknown, d.startSet, d.opacitySet = unknown, startSet, opacitySet

	if !d.opacitySet {
		d.Opacity = IndicatorOpacity{Start: 1, End: 1}
	}
	if !d.startSet {
		// Twelve o'clock. Screen space is Y-down, so angles run clockwise from
		// +X and straight up is -90.
		d.Start = -90
	}
	return nil
}

func nodeKindName(k yaml.Kind) string {
	switch k {
	case yaml.SequenceNode:
		return "a sequence"
	case yaml.ScalarNode:
		return "a scalar"
	case yaml.AliasNode:
		return "an alias"
	case yaml.DocumentNode:
		return "a document"
	}
	return "an unknown node"
}

// validate rejects anything the draw path could not resolve, naming the widget
// so the error points at a line of YAML rather than at the package.
func (d *IndicatorDef) validate(widget string) error {
	if len(d.unknown) > 0 {
		return fmt.Errorf("widget %s: indicator: unknown key %q", widget, d.unknown[0])
	}

	switch d.Type {
	case "":
		return fmt.Errorf("widget %s: indicator: type is required (roll, sweep or tint)", widget)
	case IndicatorRoll, IndicatorSweep, IndicatorTint:
	default:
		return fmt.Errorf("widget %s: indicator: unknown type %q (want roll, sweep or tint)", widget, d.Type)
	}

	if d.Type != IndicatorSweep {
		if d.Shape != "" {
			return fmt.Errorf("widget %s: indicator: shape is only used by type sweep, not %s", widget, d.Type)
		}
		if d.startSet {
			return fmt.Errorf("widget %s: indicator: start is only used by type sweep, not %s", widget, d.Type)
		}
	}
	if d.Type == IndicatorTint {
		if d.Direction != "" {
			return fmt.Errorf("widget %s: indicator: direction is not used by type tint", widget)
		}
		if d.Fill != "" {
			return fmt.Errorf("widget %s: indicator: fill is not used by type tint", widget)
		}
	}

	switch d.Fill {
	case "", "remaining", "elapsed":
	default:
		return fmt.Errorf("widget %s: indicator: unknown fill %q (want remaining or elapsed)", widget, d.Fill)
	}

	switch d.Type {
	case IndicatorRoll:
		if _, err := rollAxisDegrees(d.Direction); err != nil {
			return fmt.Errorf("widget %s: indicator: %w", widget, err)
		}
	case IndicatorSweep:
		switch d.Direction {
		case "", "clockwise", "counterclockwise":
		default:
			return fmt.Errorf("widget %s: indicator: unknown direction %q for type sweep "+
				"(want clockwise or counterclockwise)", widget, d.Direction)
		}
		switch d.Shape {
		case "", "square", "circle":
		default:
			return fmt.Errorf("widget %s: indicator: unknown shape %q (want square or circle)", widget, d.Shape)
		}
	}

	if _, err := parseHexColor(d.Color); err != nil {
		return fmt.Errorf("widget %s: indicator: %w", widget, err)
	}
	return nil
}

// rollAxis is the wipe axis in degrees. Direction has already been validated at
// load, so a parse failure here can only come from a block built in Go; zero is
// a wipe to the right, which is visible rather than absent.
func (d *IndicatorDef) rollAxis() float32 {
	deg, _ := rollAxisDegrees(d.Direction)
	return deg
}

func (d *IndicatorDef) sweepCCW() bool    { return d.Direction == "counterclockwise" }
func (d *IndicatorDef) sweepCircle() bool { return d.Shape == "circle" }
func (d *IndicatorDef) fillElapsed() bool { return d.Fill == "elapsed" }

// rgb is the overlay colour. An unparseable one is black, the same as an absent
// one -- load-time validation is what stops a typo getting this far.
func (d *IndicatorDef) rgb() [3]float32 {
	c, _ := parseHexColor(d.Color)
	return c
}

// ramp is the opacity ramp, with the all-zero value read as fully opaque.
//
// {start: 0, end: 0} draws nothing at any value, which nobody asks for on
// purpose; it is what an IndicatorDef built in Go without an opacity carries.
// A YAML block gets the same default written into it at unmarshal, so the two
// paths agree.
func (d *IndicatorDef) ramp() IndicatorOpacity {
	if d.Opacity.Start == 0 && d.Opacity.End == 0 {
		return IndicatorOpacity{Start: 1, End: 1}
	}
	return d.Opacity
}

// rollAxisDegrees turns a roll direction into the angle its wipe travels along.
//
// Screen space is Y-down, so the angle runs clockwise from +X: 0 right,
// 90 down, 180 left, 270 up. The words are mapped onto exactly those numbers
// rather than onto hand-written unit vectors, so `direction: 90` and
// `direction: down` go down the same code path and cannot drift apart.
func rollAxisDegrees(dir string) (float32, error) {
	switch dir {
	case "right":
		return 0, nil
	case "down":
		return 90, nil
	case "left":
		return 180, nil
	case "up":
		return 270, nil
	case "":
		return 0, fmt.Errorf("direction is required for type roll " +
			"(up, down, left, right or a number of degrees)")
	}
	deg, err := strconv.ParseFloat(strings.TrimSpace(dir), 32)
	if err != nil {
		return 0, fmt.Errorf("unknown direction %q for type roll "+
			"(want up, down, left, right or a number of degrees)", dir)
	}
	return float32(deg), nil
}

// parseHexColor reads #rrggbb into the 0..1 triple the UI shader expects.
//
// The division is by 255 and nothing else: ui.frag runs srgbToLinear over a
// widget's colour, so the value a widget carries is an sRGB display value --
// the same number the hex digits already are. Squaring it here would decode it
// twice and every indicator would come out darker than the hex a designer
// picked.
func parseHexColor(s string) ([3]float32, error) {
	if s == "" {
		return [3]float32{}, nil // black: the cooldown overlay everything starts from
	}
	h := strings.TrimPrefix(s, "#")
	if len(h) != 6 {
		return [3]float32{}, fmt.Errorf("color %q is not a #rrggbb colour", s)
	}
	v, err := strconv.ParseUint(h, 16, 32)
	if err != nil {
		return [3]float32{}, fmt.Errorf("color %q is not a #rrggbb colour", s)
	}
	return [3]float32{
		float32((v>>16)&0xff) / 255,
		float32((v>>8)&0xff) / 255,
		float32(v&0xff) / 255,
	}, nil
}

// frac returns the bound fraction, clamped to 0..1, and whether there is
// anything to draw at all.
//
// A max of zero or less is not a division that returns infinity or NaN and then
// paints the whole widget; it is a widget with no scale to measure against, so
// it draws nothing. That is the case a game hits on its very first frame, before
// it has bound anything.
func (d *IndicatorDef) frac(bindings map[string]string) (float32, bool) {
	max := resolveFloat(d.Max, bindings)
	if max <= 0 {
		return 0, false
	}
	f := resolveFloat(d.Value, bindings) / max
	if f < 0 {
		f = 0
	}
	if f > 1 {
		f = 1
	}
	return f, true
}

// cover is how much of the widget the effect covers, 0..1.
//
// `fill: remaining` is the classic cooldown: the covered region is the value
// still to run down, so it shrinks to nothing. `fill: elapsed` inverts it for a
// cast bar or a charge-up, which fills in as the value rises.
func (d *IndicatorDef) cover(frac float32) float32 {
	if d.fillElapsed() {
		return 1 - frac
	}
	return frac
}

// alpha interpolates the opacity ramp. Start is the alpha at frac 1 and End at
// frac 0, which is the direction a cooldown runs.
func (d *IndicatorDef) alpha(frac float32) float32 {
	r := d.ramp()
	return r.End + frac*(r.Start-r.End)
}

// tint returns base multiplied toward base*color by the interpolated alpha.
//
// At alpha 0 the widget's colour is untouched, at alpha 1 it is fully
// multiplied, and in between it crossfades -- so a tint that fades out as a
// cooldown expires leaves the widget exactly as it was when it reaches zero.
func (d *IndicatorDef) tint(base [3]float32, alpha float32) [3]float32 {
	var out [3]float32
	rgb := d.rgb()
	for i := range out {
		out[i] = clamp01(base[i]*(1-alpha) + base[i]*rgb[i]*alpha)
	}
	return out
}

// indicatorPoint is a vertex of the covered region in screen space.
type indicatorPoint struct{ X, Y float32 }

// indicatorRim is one point where a sweep's fan meets the widget's edge,
// carrying the angle that put it there.
//
// The angle travels with the point on purpose. Recovering it with atan2 wraps
// at +-180, which reports a perfectly monotonic fan that crosses nine o'clock
// as jumping backwards; the unwrapped angle is the only form in which "this fan
// unwinds in one direction" is checkable.
type indicatorRim struct {
	Angle float32 // degrees, unwrapped: monotonic along the fan
	X, Y  float32
}

// rollPolygon returns the part of r covered by a wipe of `cover` along the axis
// at axisDeg, as a convex polygon in order.
//
// The covered region starts at the edge the axis points FROM and grows along
// it, so `down` at 0.5 is the top half of the widget and the region's lower
// boundary rises as the value falls. The boundary sits at `cover` of the rect's
// extent PROJECTED onto the axis, which is what makes a rotated axis a rotation
// of the same wipe rather than a separate shape: at 0, 90, 180 and 270 it
// reproduces right, down, left and up exactly.
func rollPolygon(r Rect, axisDeg, cover float32) []indicatorPoint {
	if cover <= 0 || r.W <= 0 || r.H <= 0 {
		return nil
	}
	corners := []indicatorPoint{
		{r.X, r.Y},
		{r.X + r.W, r.Y},
		{r.X + r.W, r.Y + r.H},
		{r.X, r.Y + r.H},
	}
	if cover >= 1 {
		return corners
	}

	rad := float64(axisDeg) * math.Pi / 180
	dx, dy := float32(math.Cos(rad)), float32(math.Sin(rad))

	proj := func(p indicatorPoint) float32 { return p.X*dx + p.Y*dy }
	lo, hi := proj(corners[0]), proj(corners[0])
	for _, c := range corners[1:] {
		v := proj(c)
		if v < lo {
			lo = v
		}
		if v > hi {
			hi = v
		}
	}
	bound := lo + cover*(hi-lo)

	// Sutherland-Hodgman against the single half-plane proj(p) <= bound.
	var out []indicatorPoint
	add := func(p indicatorPoint) {
		// Drop a point that repeats the previous one. A near-axis-aligned wipe
		// (270 degrees is cos = -1.8e-16, not 0) otherwise emits a pair of
		// coincident vertices and a zero-area triangle with them.
		if n := len(out); n > 0 {
			d := out[n-1]
			if absf(d.X-p.X) < 1e-4 && absf(d.Y-p.Y) < 1e-4 {
				return
			}
		}
		out = append(out, p)
	}
	for i, cur := range corners {
		next := corners[(i+1)%len(corners)]
		dCur, dNext := proj(cur)-bound, proj(next)-bound
		if dCur <= 0 {
			add(cur)
		}
		if (dCur < 0) != (dNext < 0) {
			t := dCur / (dCur - dNext)
			add(indicatorPoint{cur.X + t*(next.X-cur.X), cur.Y + t*(next.Y-cur.Y)})
		}
	}
	if len(out) > 1 {
		first, last := out[0], out[len(out)-1]
		if absf(first.X-last.X) < 1e-4 && absf(first.Y-last.Y) < 1e-4 {
			out = out[:len(out)-1]
		}
	}
	if len(out) < 3 {
		return nil
	}
	return out
}

// sweepRim returns the rim of a radial fan covering `cover` of a full turn from
// startDeg, in order from the start angle onwards.
//
// The rim lies on the widget's own perimeter, not on a circle inscribed in it,
// unless shape is circle: a cooldown on a square icon is expected to reach the
// corners, and a fan clipped to a circle leaves them uncovered at every angle
// that is not an axis. Square needs no tessellation -- the perimeter is four
// straight lines, so a rim point at the start, at every corner the arc passes,
// and at the end reproduces it exactly.
func sweepRim(r Rect, startDeg float32, ccw, circle bool, cover float32) []indicatorRim {
	if cover <= 0 || r.W <= 0 || r.H <= 0 {
		return nil
	}
	if cover > 1 {
		cover = 1
	}
	hw, hh := r.W/2, r.H/2
	cx, cy := r.X+hw, r.Y+hh

	span := float64(cover) * 360
	if ccw {
		span = -span
	}
	start := float64(startDeg)
	end := start + span

	angles := []float64{start}
	if circle {
		// Even steps, small enough that the chord error is invisible.
		n := int(math.Ceil(math.Abs(span)/sweepStepDeg)) + 1
		for i := 1; i < n; i++ {
			angles = append(angles, start+span*float64(i)/float64(n))
		}
	} else {
		for _, a := range cornerAngles(hw, hh) {
			if v, ok := angleInArc(a, start, end); ok {
				angles = append(angles, v)
			}
		}
		sortFloats(angles[1:], span > 0)
	}
	angles = append(angles, end)

	rim := make([]indicatorRim, 0, len(angles))
	for _, a := range angles {
		x, y := perimeterPoint(cx, cy, hw, hh, a, circle)
		rim = append(rim, indicatorRim{Angle: float32(a), X: x, Y: y})
	}
	return rim
}

// cornerAngles returns the four corner directions of a rect with the given half
// extents, in (-180, 180].
func cornerAngles(hw, hh float32) [4]float64 {
	w, h := float64(hw), float64(hh)
	return [4]float64{
		deg(math.Atan2(-h, w)), // top right
		deg(math.Atan2(h, w)),  // bottom right
		deg(math.Atan2(h, -w)), // bottom left
		deg(math.Atan2(-h, -w)),
	}
}

// angleInArc shifts base by whole turns until it lands strictly inside the arc
// from start to end, and reports whether it can.
func angleInArc(base, start, end float64) (float64, bool) {
	lo, hi := start, end
	if lo > hi {
		lo, hi = hi, lo
	}
	// base + 360k > lo for the smallest such k.
	k := math.Ceil((lo - base) / 360)
	v := base + 360*k
	if v > lo && v < hi {
		return v, true
	}
	return 0, false
}

// perimeterPoint returns where the ray at angle a leaves the widget: its own
// rectangular edge, or the inscribed circle when circle is set.
func perimeterPoint(cx, cy, hw, hh float32, a float64, circle bool) (float32, float32) {
	rad := a * math.Pi / 180
	dx, dy := math.Cos(rad), math.Sin(rad)
	if circle {
		rr := float64(hw)
		if float64(hh) < rr {
			rr = float64(hh)
		}
		return cx + float32(dx*rr), cy + float32(dy*rr)
	}
	t := math.Inf(1)
	if dx != 0 {
		t = math.Min(t, float64(hw)/math.Abs(dx))
	}
	if dy != 0 {
		t = math.Min(t, float64(hh)/math.Abs(dy))
	}
	if math.IsInf(t, 1) {
		return cx, cy
	}
	return cx + float32(dx*t), cy + float32(dy*t)
}

// appendIndicatorFan writes a convex polygon as a triangle fan around pts[0].
//
// Every vertex carries UV (0.5, 0.5) rather than a corner UV. ui.frag derives
// its edge coverage from the distance to UV 0 and 1, which is right for a quad
// that was grown half a pixel past its own edge; this geometry is a fan with no
// such skirt, and giving it corner UVs would ramp its alpha down to a half along
// every interior seam of the fan, drawing spokes across the indicator. A
// constant UV has zero fwidth, so the ramp saturates and the whole shape is
// fully covered.
func appendIndicatorFan(verts []renderer.Vertex, idxs []uint16, pts []indicatorPoint, col [3]float32) ([]renderer.Vertex, []uint16) {
	if len(pts) < 3 {
		return verts, idxs
	}
	base := uint16(len(verts))
	for _, p := range pts {
		verts = append(verts, renderer.Vertex{
			Pos:   [3]float32{p.X, p.Y, 0},
			Color: col,
			UV:    [2]float32{0.5, 0.5},
		})
	}
	for i := 1; i+1 < len(pts); i++ {
		idxs = append(idxs, base, base+uint16(i), base+uint16(i+1))
	}
	return verts, idxs
}

// indicatorGeometry returns the covered region for this indicator over rect r,
// or nil when there is nothing to draw.
func (d *IndicatorDef) indicatorGeometry(r Rect, cover float32) []indicatorPoint {
	switch d.Type {
	case IndicatorRoll:
		return rollPolygon(r, d.rollAxis(), cover)
	case IndicatorSweep:
		rim := sweepRim(r, d.Start, d.sweepCCW(), d.sweepCircle(), cover)
		if len(rim) < 2 {
			return nil
		}
		pts := make([]indicatorPoint, 0, len(rim)+1)
		pts = append(pts, indicatorPoint{r.X + r.W/2, r.Y + r.H/2})
		for _, p := range rim {
			pts = append(pts, indicatorPoint{p.X, p.Y})
		}
		return pts
	}
	return nil
}

func absf(v float32) float32 {
	if v < 0 {
		return -v
	}
	return v
}

func deg(rad float64) float64 { return rad * 180 / math.Pi }

// sortFloats insertion-sorts in place; the slice is never more than four long.
func sortFloats(v []float64, ascending bool) {
	for i := 1; i < len(v); i++ {
		for j := i; j > 0; j-- {
			if (ascending && v[j] >= v[j-1]) || (!ascending && v[j] <= v[j-1]) {
				break
			}
			v[j], v[j-1] = v[j-1], v[j]
		}
	}
}
