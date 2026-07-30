package input

import (
	"math"

	"github.com/go-gl/glfw/v3.3/glfw"
)

// MaxPads is how many gamepads are tracked. GLFW supports sixteen; four is what
// couch multiplayer actually uses, and every pad costs a poll per frame.
const MaxPads = 4

// Pad identifies a connected gamepad. Pad0 is the first one plugged in.
type Pad int

const (
	Pad0 Pad = iota
	Pad1
	Pad2
	Pad3
)

// Button is a gamepad button.
//
// These are positions in the SDL layout, not labels on any particular pad, which
// is what makes one binding work across hardware: GLFW maps every controller it
// knows onto this layout, so ButtonA is the bottom face button whether the pad
// prints an A on it or a cross.
type Button int

const (
	ButtonA Button = Button(glfw.ButtonA) // bottom face button; cross on PlayStation
	ButtonB Button = Button(glfw.ButtonB) // right face button; circle
	ButtonX Button = Button(glfw.ButtonX) // left face button; square
	ButtonY Button = Button(glfw.ButtonY) // top face button; triangle

	ButtonLeftBumper  Button = Button(glfw.ButtonLeftBumper)
	ButtonRightBumper Button = Button(glfw.ButtonRightBumper)
	ButtonBack        Button = Button(glfw.ButtonBack)
	ButtonStart       Button = Button(glfw.ButtonStart)
	ButtonGuide       Button = Button(glfw.ButtonGuide)
	ButtonLeftThumb   Button = Button(glfw.ButtonLeftThumb)
	ButtonRightThumb  Button = Button(glfw.ButtonRightThumb)

	ButtonDPadUp    Button = Button(glfw.ButtonDpadUp)
	ButtonDPadRight Button = Button(glfw.ButtonDpadRight)
	ButtonDPadDown  Button = Button(glfw.ButtonDpadDown)
	ButtonDPadLeft  Button = Button(glfw.ButtonDpadLeft)

	buttonCount = 15
)

// Axis is a gamepad analog axis.
//
// Every axis here is normalized so that **up and forward are positive** and
// triggers run 0 to 1. That is deliberately not what the underlying API reports
// — SDL and GLFW give sticks +1 downward and triggers -1 at rest — and
// normalizing once here is cheaper than every caller remembering which of its
// axes to negate. PadAxis is the only place the flip happens.
type Axis int

const (
	AxisLeftX Axis = iota
	AxisLeftY
	AxisRightX
	AxisRightY
	AxisLeftTrigger
	AxisRightTrigger

	axisCount = 6
)

// Stick identifies one of the two thumbsticks, for the 2D accessor.
type Stick int

const (
	StickLeft Stick = iota
	StickRight
)

// DefaultDeadzone is the fraction of a stick's travel ignored around centre.
//
// Sticks do not return to exactly zero; a worn one can rest at 0.15 or more. Too
// small and the character drifts on its own, too large and precise slow movement
// becomes impossible.
const DefaultDeadzone = 0.18

// padState is one gamepad's raw state, in the underlying API's conventions.
// Kept separate from the normalized accessors so the source can be faked.
type padState struct {
	present bool
	name    string
	buttons [buttonCount]bool
	axes    [axisCount]float32
}

// padSource reads gamepad state. The real one asks GLFW; tests supply their own,
// because a test machine has no controller plugged into it and the interesting
// logic — dead zones, edges, binding resolution — is all on this side of the
// call anyway.
type padSource interface {
	readPad(p Pad) padState
}

// glfwPads is the production padSource.
type glfwPads struct{}

func (glfwPads) readPad(p Pad) padState {
	joy := glfw.Joystick(int(glfw.Joystick1) + int(p))
	if !joy.Present() || !joy.IsGamepad() {
		return padState{}
	}
	st := joy.GetGamepadState()
	if st == nil {
		return padState{}
	}

	out := padState{present: true, name: joy.GetGamepadName()}
	for i := 0; i < buttonCount; i++ {
		out.buttons[i] = st.Buttons[i] == glfw.Press
	}
	copy(out.axes[:], st.Axes[:])
	return out
}

// pollPads snapshots every pad, moving the current state to previous first so
// button edges work exactly as keyboard edges do.
//
// Called from Update, which runs once per frame before PollEvents. Gamepad state
// is queried rather than delivered by callback, so it does not depend on event
// pumping — but sampling it once per frame in the same place as the keyboard is
// what keeps Pressed meaning "this frame" for every device.
func (inp *Input) pollPads() {
	inp.prevPads = inp.pads
	for p := Pad(0); p < MaxPads; p++ {
		inp.pads[p] = inp.padSrc.readPad(p)
	}
}

// PadPresent reports whether a gamepad is connected at that index and GLFW has a
// mapping for it.
//
// A joystick with no mapping reads as absent, because without one its buttons are
// in an unknown order and any binding against them would be a guess. See
// AddPadMapping for how to teach GLFW about unusual hardware.
func (inp *Input) PadPresent(p Pad) bool {
	if p < 0 || p >= MaxPads {
		return false
	}
	return inp.pads[p].present
}

// PadName returns the mapped controller's human-readable name, or "" if absent.
func (inp *Input) PadName(p Pad) string {
	if p < 0 || p >= MaxPads {
		return ""
	}
	return inp.pads[p].name
}

// FirstPad returns the lowest-numbered connected pad, and whether there is one.
// Single-player games want this rather than assuming Pad0: unplugging and
// replugging can leave the first slot empty.
func (inp *Input) FirstPad() (Pad, bool) {
	for p := Pad(0); p < MaxPads; p++ {
		if inp.pads[p].present {
			return p, true
		}
	}
	return Pad0, false
}

// PadDown returns true while the button is held.
func (inp *Input) PadDown(p Pad, b Button) bool {
	if !inp.validPad(p) || !validButton(b) {
		return false
	}
	return inp.pads[p].buttons[b]
}

// PadPressed returns true on the frame the button went down.
func (inp *Input) PadPressed(p Pad, b Button) bool {
	if !inp.validPad(p) || !validButton(b) {
		return false
	}
	return inp.pads[p].buttons[b] && !inp.prevPads[p].buttons[b]
}

// PadReleased returns true on the frame the button came up.
func (inp *Input) PadReleased(p Pad, b Button) bool {
	if !inp.validPad(p) || !validButton(b) {
		return false
	}
	return !inp.pads[p].buttons[b] && inp.prevPads[p].buttons[b]
}

// PadAxis returns one axis, normalized: sticks run -1 to 1 with up and forward
// positive and the dead zone applied, triggers run 0 to 1.
//
// The dead zone is radial per stick rather than per axis. Treating each axis
// separately leaves a square hole around centre, which is why a per-axis dead
// zone makes it impossible to walk slowly on a diagonal — the diagonal is
// exactly where both axes are small.
func (inp *Input) PadAxis(p Pad, a Axis) float32 {
	if !inp.validPad(p) || a < 0 || a >= axisCount {
		return 0
	}

	switch a {
	case AxisLeftTrigger, AxisRightTrigger:
		// Reported -1 at rest to +1 fully pressed; 0 to 1 is what callers want.
		return clamp01((inp.pads[p].axes[a] + 1) * 0.5)
	case AxisLeftX, AxisRightX:
		x, _ := inp.PadStick(p, stickOf(a))
		return x
	default: // AxisLeftY, AxisRightY
		_, y := inp.PadStick(p, stickOf(a))
		return y
	}
}

// PadStick returns a thumbstick's deflection as a vector, with up and forward
// positive and a radial dead zone applied.
//
// Past the dead zone the magnitude is rescaled to run from 0 to 1 again, so the
// stick does not jump to 0.18 the moment it starts responding. The direction is
// preserved exactly; only the length is remapped.
func (inp *Input) PadStick(p Pad, s Stick) (x, y float32) {
	if !inp.validPad(p) {
		return 0, 0
	}

	xi, yi := AxisLeftX, AxisLeftY
	if s == StickRight {
		xi, yi = AxisRightX, AxisRightY
	}
	rx := inp.pads[p].axes[xi]
	// Negated once, here: the underlying API points +Y down the screen.
	ry := -inp.pads[p].axes[yi]

	return applyDeadzone(rx, ry, inp.deadzone)
}

// SetPadDeadzone overrides the stick dead zone, as a fraction of full travel.
// Values outside [0, 0.9] are ignored rather than clamped, because a dead zone
// that large is a bug in the caller and silently accepting it hides it.
func (inp *Input) SetPadDeadzone(f float32) {
	if f < 0 || f > 0.9 {
		return
	}
	inp.deadzone = f
}

// PadDeadzone returns the current stick dead zone.
func (inp *Input) PadDeadzone() float32 { return inp.deadzone }

// AddPadMapping teaches GLFW about a controller it does not recognise, using an
// SDL_GameControllerDB mapping line. Returns false if the string is malformed.
//
// Without a mapping a joystick reports buttons in whatever order its firmware
// chose, so PadPresent reports it absent rather than let a game bind to indices
// that mean something different on the next controller.
func AddPadMapping(sdlMapping string) bool {
	return glfw.UpdateGamepadMappings(sdlMapping)
}

func (inp *Input) validPad(p Pad) bool {
	return p >= 0 && p < MaxPads && inp.pads[p].present
}

func validButton(b Button) bool { return b >= 0 && b < buttonCount }

func stickOf(a Axis) Stick {
	if a == AxisRightX || a == AxisRightY {
		return StickRight
	}
	return StickLeft
}

// applyDeadzone removes the slack around a stick's centre and rescales what is
// left, so the usable range is the full 0 to 1.
func applyDeadzone(x, y, dz float32) (float32, float32) {
	mag := sqrt32(x*x + y*y)
	if mag <= dz {
		return 0, 0
	}
	if mag > 1 {
		// Corners of a square-reporting stick can exceed unit length.
		x, y, mag = x/mag, y/mag, 1
	}
	scale := (mag - dz) / (1 - dz) / mag
	return x * scale, y * scale
}

func sqrt32(v float32) float32 { return float32(math.Sqrt(float64(v))) }

func clamp01(v float32) float32 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}
