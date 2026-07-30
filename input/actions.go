package input

import "fmt"

// This file is the layer that lets game code stop caring which device it is
// reading. A game asks whether "jump" happened, not whether the space bar or the
// bottom face button is down, which is what makes supporting a controller a
// binding change rather than a code change — and what makes rebinding possible
// at all.
//
// Handles rather than strings. Bindings.Action returns an Action that the caller
// holds, so a typo is a compile error. A string-keyed API reads nicely right up
// until Down("jmp") returns false forever and nothing anywhere says why. Names
// are still carried, because a rebinding screen has to show the player something.

// sourceKind is what device a Source reads.
type sourceKind uint8

const (
	srcNone sourceKind = iota
	srcKey
	srcMouse
	srcPadButton
	srcPadAxis // an analog axis treated as a button, past a threshold
)

// axisAsButtonThreshold is how far an analog axis must travel to count as a
// button press. Triggers rest at 0 and are commonly bound as buttons.
const axisAsButtonThreshold = 0.5

// Source is one thing that can drive an action: a key, a mouse button, a gamepad
// button, or a gamepad axis pushed past halfway.
//
// The zero Source is bound to nothing and always reads as released, which is what
// an unbound slot in a rebinding screen should do.
type Source struct {
	kind sourceKind
	code int
	sign float32 // for srcPadAxis: which direction counts
}

// Keyboard returns a Source for a key.
func Keyboard(k Key) Source { return Source{kind: srcKey, code: int(k)} }

// Mouse returns a Source for a mouse button.
func Mouse(b MouseButton) Source { return Source{kind: srcMouse, code: int(b)} }

// PadButton returns a Source for a gamepad button.
func PadButton(b Button) Source { return Source{kind: srcPadButton, code: int(b)} }

// PadAxisPositive returns a Source that fires when an analog axis is pushed
// positive past halfway — a trigger, or a stick used as a button.
func PadAxisPositive(a Axis) Source {
	return Source{kind: srcPadAxis, code: int(a), sign: 1}
}

// PadAxisNegative is PadAxisPositive for the other direction.
func PadAxisNegative(a Axis) Source {
	return Source{kind: srcPadAxis, code: int(a), sign: -1}
}

// Bound reports whether the Source refers to anything.
func (s Source) Bound() bool { return s.kind != srcNone }

// Action, AxisID and VectorID are handles into a Bindings. They are only valid
// for the Bindings that returned them.
type (
	Action   int
	AxisID   int
	VectorID int
)

type actionBinding struct {
	name    string
	sources []Source
}

type axisBinding struct {
	name         string
	negative     Source
	positive     Source
	analog       Axis
	analogBound  bool
	invertAnalog bool
}

type vectorBinding struct {
	name                  string
	up, down, left, right Source
	stick                 Stick
	stickBound            bool
}

// Bindings maps named actions onto input sources and answers questions about
// them in device-neutral terms.
//
// It reads through an *Input, which has already snapshotted every device once for
// the frame, so Pressed here means the same "this frame" it means there. That
// matters for the fixed-timestep rule: sample in Update, latch the edge, consume
// it in FixedUpdate. Querying edges straight from FixedUpdate drops them on
// zero-tick frames whether the query goes through this layer or not.
type Bindings struct {
	inp *Input

	// pad is which gamepad the bindings read. Zero value means "whichever is
	// plugged in", resolved per frame, so a single-player game does not break
	// when a controller lands in slot 1 after a replug.
	pad      Pad
	padFixed bool

	actions []actionBinding
	axes    []axisBinding
	vectors []vectorBinding
}

// NewBindings returns an empty Bindings reading from inp.
func NewBindings(inp *Input) *Bindings { return &Bindings{inp: inp} }

// SetPad pins the bindings to one gamepad, for local multiplayer. Without it they
// follow the first connected pad.
func (b *Bindings) SetPad(p Pad) {
	b.pad, b.padFixed = p, true
}

// activePad resolves which pad to read this frame.
func (b *Bindings) activePad() Pad {
	if b.padFixed {
		return b.pad
	}
	p, _ := b.inp.FirstPad()
	return p
}

// Action declares a button-like action bound to any number of sources. Any one of
// them firing fires the action, so a keyboard and a gamepad binding coexist
// without the game knowing.
func (b *Bindings) Action(name string, sources ...Source) Action {
	b.actions = append(b.actions, actionBinding{name: name, sources: sources})
	return Action(len(b.actions) - 1)
}

// Axis declares a scalar in [-1, 1] from a pair of digital sources.
//
// Attach an analog axis with SetAxisAnalog to make the same name work on a stick
// or trigger; the analog reading wins whenever it is off centre, so holding a key
// still works on a machine with a controller plugged in.
func (b *Bindings) Axis(name string, negative, positive Source) AxisID {
	b.axes = append(b.axes, axisBinding{name: name, negative: negative, positive: positive})
	return AxisID(len(b.axes) - 1)
}

// SetAxisAnalog attaches a gamepad axis to an Axis. invert flips it, for the
// players who want pull-back-to-climb.
func (b *Bindings) SetAxisAnalog(a AxisID, axis Axis, invert bool) {
	if !b.validAxis(a) {
		return
	}
	b.axes[a].analog, b.axes[a].analogBound, b.axes[a].invertAnalog = axis, true, invert
}

// Vector declares a 2D direction from four digital sources, with +y up and +x
// right.
//
// Diagonals are normalized, so holding two keys does not move 1.41 times as fast
// as holding one — the bug that makes strafe-running quicker than walking.
func (b *Bindings) Vector(name string, up, down, left, right Source) VectorID {
	b.vectors = append(b.vectors, vectorBinding{
		name: name, up: up, down: down, left: left, right: right,
	})
	return VectorID(len(b.vectors) - 1)
}

// SetVectorStick attaches a thumbstick to a Vector. The stick's own radial dead
// zone and magnitude carry through, which is what gives analog movement its
// partial deflection.
func (b *Bindings) SetVectorStick(v VectorID, s Stick) {
	if !b.validVector(v) {
		return
	}
	b.vectors[v].stick, b.vectors[v].stickBound = s, true
}

// --- querying ---

// Down reports whether any of the action's sources is held.
func (b *Bindings) Down(a Action) bool {
	if !b.validAction(a) {
		return false
	}
	for _, s := range b.actions[a].sources {
		if b.sourceDown(s) {
			return true
		}
	}
	return false
}

// Pressed reports whether any of the action's sources went down this frame.
//
// Deliberately not "went down and nothing else was already down": two keys bound
// to the same action, pressed on the same frame, fire once, but pressing the
// second while the first is held fires again. Suppressing that would need a
// held-count and would make chorded rebinds behave surprisingly.
func (b *Bindings) Pressed(a Action) bool {
	if !b.validAction(a) {
		return false
	}
	for _, s := range b.actions[a].sources {
		if b.sourcePressed(s) {
			return true
		}
	}
	return false
}

// Released reports whether any of the action's sources came up this frame.
func (b *Bindings) Released(a Action) bool {
	if !b.validAction(a) {
		return false
	}
	for _, s := range b.actions[a].sources {
		if b.sourceReleased(s) {
			return true
		}
	}
	return false
}

// Value returns an Axis in [-1, 1].
//
// An attached analog axis wins while it is off centre; otherwise the digital pair
// decides. Both held cancel to zero, which is what a player pressing left and
// right together expects.
func (b *Bindings) Value(a AxisID) float32 {
	if !b.validAxis(a) {
		return 0
	}
	ax := &b.axes[a]

	if ax.analogBound {
		v := b.inp.PadAxis(b.activePad(), ax.analog)
		if ax.invertAnalog {
			v = -v
		}
		if v != 0 {
			return v
		}
	}

	var v float32
	if b.sourceDown(ax.positive) {
		v++
	}
	if b.sourceDown(ax.negative) {
		v--
	}
	return v
}

// Direction returns a Vector as (x, y), +y up and +x right, with a magnitude of
// at most one.
//
// An attached stick wins while it is off centre, and its partial deflection
// carries through — that is where analog movement comes from. The digital
// fallback is normalized so diagonals are not faster.
func (b *Bindings) Direction(v VectorID) (x, y float32) {
	if !b.validVector(v) {
		return 0, 0
	}
	vb := &b.vectors[v]

	if vb.stickBound {
		sx, sy := b.inp.PadStick(b.activePad(), vb.stick)
		if sx != 0 || sy != 0 {
			return sx, sy
		}
	}

	if b.sourceDown(vb.right) {
		x++
	}
	if b.sourceDown(vb.left) {
		x--
	}
	if b.sourceDown(vb.up) {
		y++
	}
	if b.sourceDown(vb.down) {
		y--
	}
	if x != 0 && y != 0 {
		const invSqrt2 = 0.70710678
		x *= invSqrt2
		y *= invSqrt2
	}
	return x, y
}

// --- rebinding ---

// Rebind replaces an action's sources. Passing none leaves it unbound, which is a
// legitimate state: a player may want no key for an action at all.
func (b *Bindings) Rebind(a Action, sources ...Source) {
	if !b.validAction(a) {
		return
	}
	b.actions[a].sources = sources
}

// ActionName returns the name an action was declared with, for display.
func (b *Bindings) ActionName(a Action) string {
	if !b.validAction(a) {
		return ""
	}
	return b.actions[a].name
}

// ActionSources returns a copy of an action's current sources, so a rebinding
// screen can show what is bound without being able to corrupt it.
func (b *Bindings) ActionSources(a Action) []Source {
	if !b.validAction(a) {
		return nil
	}
	out := make([]Source, len(b.actions[a].sources))
	copy(out, b.actions[a].sources)
	return out
}

// ActionCount returns how many actions are declared, so a rebinding screen can
// walk Action(0) to Action(ActionCount()-1) without the game keeping its own list.
func (b *Bindings) ActionCount() int { return len(b.actions) }

// CapturePressed returns the first source that went down this frame, across every
// device, and whether there was one.
//
// This is what a rebinding screen is built on: show "press a key for Jump", call
// this until it returns something, hand that to Rebind. Keyboard first, then
// mouse, then the pad, so a key wins over a stick that happens to be drifting.
func (b *Bindings) CapturePressed() (Source, bool) {
	for k := Key(0); k <= maxCaptureKey; k++ {
		if b.inp.KeyPressed(k) {
			return Keyboard(k), true
		}
	}
	for m := MouseButton(0); m <= maxCaptureMouse; m++ {
		if b.inp.MousePressed(m) {
			return Mouse(m), true
		}
	}

	p := b.activePad()
	if b.inp.PadPresent(p) {
		for btn := Button(0); btn < buttonCount; btn++ {
			if b.inp.PadPressed(p, btn) {
				return PadButton(btn), true
			}
		}
		// Triggers last: they are axes, so they have no press edge of their own
		// and a resting trigger on a worn pad can sit just off zero.
		for _, a := range []Axis{AxisLeftTrigger, AxisRightTrigger} {
			if b.inp.PadAxis(p, a) > axisAsButtonThreshold {
				return PadAxisPositive(a), true
			}
		}
	}
	return Source{}, false
}

// SourceLabel returns a short human-readable name for a source, for a rebinding
// screen to print. Unbound sources render as "—" rather than as empty, so a blank
// row in a key list is visibly deliberate.
func (b *Bindings) SourceLabel(s Source) string { return SourceLabel(s) }

// SourceLabel is the package-level form, usable without a Bindings.
func SourceLabel(s Source) string {
	switch s.kind {
	case srcKey:
		return keyLabel(Key(s.code))
	case srcMouse:
		switch MouseButton(s.code) {
		case MouseButtonLeft:
			return "Left Mouse"
		case MouseButtonRight:
			return "Right Mouse"
		case MouseButtonMiddle:
			return "Middle Mouse"
		default:
			return fmt.Sprintf("Mouse %d", s.code+1)
		}
	case srcPadButton:
		return buttonLabel(Button(s.code))
	case srcPadAxis:
		name := axisLabel(Axis(s.code))
		if s.sign < 0 {
			return name + " -"
		}
		return name + " +"
	default:
		return "—"
	}
}

// --- source evaluation ---

func (b *Bindings) sourceDown(s Source) bool {
	switch s.kind {
	case srcKey:
		return b.inp.KeyDown(Key(s.code))
	case srcMouse:
		return b.inp.MouseDown(MouseButton(s.code))
	case srcPadButton:
		return b.inp.PadDown(b.activePad(), Button(s.code))
	case srcPadAxis:
		return b.axisAsButton(s)
	default:
		return false
	}
}

func (b *Bindings) sourcePressed(s Source) bool {
	switch s.kind {
	case srcKey:
		return b.inp.KeyPressed(Key(s.code))
	case srcMouse:
		return b.inp.MousePressed(MouseButton(s.code))
	case srcPadButton:
		return b.inp.PadPressed(b.activePad(), Button(s.code))
	case srcPadAxis:
		// An axis has no edge of its own, so the edge is the threshold crossing.
		return b.axisAsButton(s) && !b.axisAsButtonPrev(s)
	default:
		return false
	}
}

func (b *Bindings) sourceReleased(s Source) bool {
	switch s.kind {
	case srcKey:
		return b.inp.KeyReleased(Key(s.code))
	case srcMouse:
		return b.inp.MouseReleased(MouseButton(s.code))
	case srcPadButton:
		return b.inp.PadReleased(b.activePad(), Button(s.code))
	case srcPadAxis:
		return !b.axisAsButton(s) && b.axisAsButtonPrev(s)
	default:
		return false
	}
}

func (b *Bindings) axisAsButton(s Source) bool {
	v := b.inp.PadAxis(b.activePad(), Axis(s.code))
	return v*s.sign > axisAsButtonThreshold
}

func (b *Bindings) axisAsButtonPrev(s Source) bool {
	v := b.inp.padAxisPrev(b.activePad(), Axis(s.code))
	return v*s.sign > axisAsButtonThreshold
}

func (b *Bindings) validAction(a Action) bool { return a >= 0 && int(a) < len(b.actions) }
func (b *Bindings) validAxis(a AxisID) bool   { return a >= 0 && int(a) < len(b.axes) }
func (b *Bindings) validVector(v VectorID) bool {
	return v >= 0 && int(v) < len(b.vectors)
}
