package yamlui

import (
	"fmt"
	"strings"

	"github.com/derekmwright/glyphengine/ui/ease"
	"gopkg.in/yaml.v3"
)

// TransitionDef tweens a widget in and out as its `visible` binding flips.
//
// It is a separate block for the same reason `indicator:` is: a widget without
// one carries a nil pointer and none of the fields, and every field here is
// about motion rather than about what the widget is. The block is driven
// entirely by `visible` -- there is no play(), no trigger and no timeline,
// because a game already has a bool for "is the dialog open" and the whole
// point is that it does not need a second one.
//
// Nothing here touches layout. The values below transform the resolved rect and
// the resolved opacity at draw time, which is what lets a dialog slide in
// without its siblings reflowing under it.
type TransitionDef struct {
	Duration float32 `yaml:"duration"` // seconds, unscaled; required
	Ease     string  `yaml:"ease"`     // an ui/ease name; default linear
	Opacity  bool    `yaml:"opacity"`  // fade from 0; default true
	Scale    float32 `yaml:"scale"`    // scale about the anchor from this to 1; default 1
	OffsetX  float32 `yaml:"offset_x"` // slide from this many reference pixels to 0
	OffsetY  float32 `yaml:"offset_y"` //
	Out      string  `yaml:"out"`      // a different curve for the out; default the in curve played back

	// Filled in by UnmarshalYAML, so a bad block is rejected once at load
	// rather than producing quietly wrong motion on every frame. Same shape as
	// IndicatorDef's, and for the same reasons -- see the comments there.
	unknown    []string
	opacitySet bool
	scaleSet   bool
}

// transitionKeys is the complete set of keys a transition block may carry.
// A key outside it is a load error rather than a silently ignored line: a
// misspelled `offset_y` is a dialog that fades without sliding, which looks
// deliberate and is nobody's intent.
var transitionKeys = map[string]bool{
	"duration": true, "ease": true, "opacity": true, "scale": true,
	"offset_x": true, "offset_y": true, "out": true,
}

// UnmarshalYAML decodes a transition block, noting which defaulted keys the
// YAML actually said something about and which keys are not in the schema.
func (d *TransitionDef) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind != yaml.MappingNode {
		return fmt.Errorf("transition: want a mapping, got %s", nodeKindName(node.Kind))
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		key := node.Content[i].Value
		if !transitionKeys[key] {
			d.unknown = append(d.unknown, key)
			continue
		}
		switch key {
		case "opacity":
			d.opacitySet = true
		case "scale":
			d.scaleSet = true
		}
	}

	// A plain alias, so the decode below uses the struct tags and does not
	// re-enter this method.
	type plain TransitionDef
	var p plain
	if err := node.Decode(&p); err != nil {
		return err
	}
	unknown, opacitySet, scaleSet := d.unknown, d.opacitySet, d.scaleSet
	*d = TransitionDef(p)
	d.unknown, d.opacitySet, d.scaleSet = unknown, opacitySet, scaleSet

	if !d.opacitySet {
		// A fade is what "transition" means to most people, and a block that
		// only slides says so with `opacity: false`.
		d.Opacity = true
	}
	if !d.scaleSet {
		d.Scale = 1
	}
	return nil
}

// validate rejects anything the draw path could not resolve, naming the widget
// so the error points at a line of YAML rather than at the package.
func (d *TransitionDef) validate(widget string) error {
	if len(d.unknown) > 0 {
		return fmt.Errorf("widget %s: transition: unknown key %q", widget, d.unknown[0])
	}
	if d.Duration <= 0 {
		return fmt.Errorf("widget %s: transition: duration is required and must be positive, got %v",
			widget, d.Duration)
	}
	if d.Ease != "" {
		if _, ok := ease.ByName(d.Ease); !ok {
			return fmt.Errorf("widget %s: transition: unknown ease %q (want %s)",
				widget, d.Ease, strings.Join(ease.Names(), ", "))
		}
	}
	if d.Out != "" {
		if _, ok := ease.ByName(d.Out); !ok {
			return fmt.Errorf("widget %s: transition: unknown out %q (want %s)",
				widget, d.Out, strings.Join(ease.Names(), ", "))
		}
	}
	if d.scaleSet && d.Scale <= 0 {
		return fmt.Errorf("widget %s: transition: scale must be positive, got %v", widget, d.Scale)
	}
	return nil
}

// inCurve is the curve the in-transition plays. An absent `ease` is linear,
// which is the only curve with no opinion.
func (d *TransitionDef) inCurve() ease.Func {
	if f, ok := ease.ByName(d.Ease); ok {
		return f
	}
	return ease.Linear
}

// outCurve is the separate curve `out:` names, if it names one.
func (d *TransitionDef) outCurve() (ease.Func, bool) { return ease.ByName(d.Out) }

// scaleFrom is the factor the widget grows from, with a Go-built zero read as
// 1 (no scale).
//
// A block assembled through SetChildren never runs UnmarshalYAML, so its Scale
// is the zero value; taking that literally would collapse the widget to nothing
// and hold it there, which is never what anyone means. Written in YAML, `scale:
// 0` is rejected at load instead, so the two paths cannot disagree about a
// number someone actually typed.
func (d *TransitionDef) scaleFrom() float32 {
	if d.Scale == 0 {
		return 1
	}
	return d.Scale
}

// transitionState is one widget's place in its transition. It lives on the Node
// rather than in a map on the tree so that rebuilding the tree from YAML --
// Load, or SetChildren -- discards it with the nodes, and a dialog rebuilt
// while open cannot come back half faded.
type transitionState struct {
	// progress is 0 at hidden and 1 at rest, in transition time rather than in
	// eased output. Reversing works on this and not on the eased value, which
	// is what makes a flip mid-transition continue from where the widget
	// actually is: easing the reversal separately would jump.
	progress float32

	// visible is what `visible` resolved to on the last build, and therefore
	// which direction this is going and which curve to read.
	visible bool

	// started records that a build has seen this node. Without it, a widget
	// whose `visible` is already true on the first build would begin at
	// progress 0 and fade in from nothing -- a HUD that flickers on every level
	// load, which is exactly the bug a transition feature is expected to avoid.
	started bool
}

// advance moves progress by one frame.
//
// clocked is whether the host has fed the tree a clock at all. Without one the
// transition is an instant cut: dt would be zero forever, so a widget that
// became visible would sit at progress 0 and never appear, and "my dialog never
// opens" is a far worse failure than "my dialog does not fade". See
// WidgetTree.SetTime.
func (s *transitionState) advance(d *TransitionDef, visible, clocked bool, dt float32) {
	if !s.started || !clocked || d.Duration <= 0 {
		s.started = true
		s.visible = visible
		s.progress = 0
		if visible {
			s.progress = 1
		}
		return
	}

	s.visible = visible
	step := dt / d.Duration
	if visible {
		s.progress += step
	} else {
		s.progress -= step
	}
	if s.progress < arrived {
		s.progress = 0
	}
	if s.progress > 1-arrived {
		s.progress = 1
	}
}

// arrived is how close to an endpoint counts as having reached it.
//
// progress is a running sum of dt/duration, and that division is inexact in
// float32: a transition stepped at a rate that divides its duration exactly
// still lands a few parts in a hundred million off, on either side. Landing
// short of 1 is invisible; landing short of 0 is not, because a widget at
// progress 4e-8 is still drawn, still holds its layout box and still costs a
// render object -- so a dialog closed once would never leave.
//
// The drift does not grow with the frame count. Each step carries at most half
// an ulp of relative error and the steps sum to 1, so the total is bounded by
// about 6e-8 whether the transition takes six frames or six hundred. 1e-6
// leaves sixteen times that, and is a hundred-thousandth of a transition:
// smaller than any frame of it can show.
const arrived = 1e-6

// atRest reports that the widget is fully in and has nothing left to transform.
//
// It is the cheap path and NOT the correct one: at progress 1 every curve here
// returns exactly 1, and transform is then the identity by arithmetic rather
// than by this branch. The offsets multiply by 1 - 1, and the scale factor is
// a + 1*(1 - a), which rounds to exactly 1 for every float32 a in (0, 1) --
// checked over 20 million random values, zero mismatches. Removing this branch
// renders the same pixels and the same render objects; `task transition` and
// the whole package test stay green with `atRest` stubbed to false, which is
// how that was established rather than assumed.
//
// What it buys is the two subtree walks and the opacity multiply that a settled
// widget would otherwise pay on every frame, for every block on screen. Worth a
// branch; not worth a claim about correctness.
func (s transitionState) atRest() bool { return s.visible && s.progress >= 1 }

// gone reports that the out has finished and the widget can be skipped.
func (s transitionState) gone() bool { return !s.visible && s.progress <= 0 }

// eased maps progress onto the curve, in the direction the widget is going.
//
// Going in, that is simply the in curve. Going out with no `out:` named, it is
// the same curve read at a descending progress -- the in transition played
// backwards, which is what makes a dialog retrace its own arrival. With an
// `out:` curve, the departure gets its own shape: u = 1 - progress is how far
// gone the widget is, the named curve says how that feels, and the result is
// turned back into a rest-to-hidden value.
func (d *TransitionDef) eased(s transitionState) float32 {
	if !s.visible {
		if f, ok := d.outCurve(); ok {
			return 1 - f(1-s.progress)
		}
	}
	return d.inCurve()(s.progress)
}

// transform turns an eased value into the three things a transition changes.
//
// scale is a reference-pixel scale the caller has already applied to the rect,
// and it multiplies the offsets so a slide is the same distance on screen at
// every UI scale, exactly like every other length in this schema.
//
// An eased value above 1 -- only out_back produces one -- carries through to
// the scale and the offset, which is the overshoot the curve exists for. It is
// clamped out of the opacity, because there is nothing above fully opaque.
func (d *TransitionDef) transform(eased, scale float32) (opacity, factor, dx, dy float32) {
	opacity = 1
	if d.Opacity {
		opacity = clamp01(eased)
	}
	from := d.scaleFrom()
	factor = from + eased*(1-from)
	dx = d.OffsetX * scale * (1 - eased)
	dy = d.OffsetY * scale * (1 - eased)
	return opacity, factor, dx, dy
}

// transformRects scales a subtree about (cx, cy) and translates it.
//
// The whole subtree moves as one, about the transitioning widget's own anchor,
// so a dialog's buttons arrive with the dialog instead of sliding around inside
// it. The anchor is the widget's rect centre: this schema has no anchor field,
// and a modal growing from its middle is what `scale` means everywhere it is
// offered.
func transformRects(n *Node, factor, dx, dy, cx, cy float32) {
	n.Rect = Rect{
		X: cx + (n.Rect.X-cx)*factor + dx,
		Y: cy + (n.Rect.Y-cy)*factor + dy,
		W: n.Rect.W * factor,
		H: n.Rect.H * factor,
	}
	for _, c := range n.Children {
		transformRects(c, factor, dx, dy, cx, cy)
	}
}
