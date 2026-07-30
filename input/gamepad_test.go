package input

import (
	"math"
	"testing"
)

// fakePads is a padSource driven by the test rather than by hardware. It exists
// because no CI machine and few dev machines have a controller plugged in, and
// everything worth checking here — dead zones, edges, normalization — is on this
// side of the driver call.
type fakePads struct {
	state [MaxPads]padState
}

func (f *fakePads) readPad(p Pad) padState {
	if p < 0 || p >= MaxPads {
		return padState{}
	}
	return f.state[p]
}

// newTestInput returns an Input wired to a fake pad source. It deliberately does
// not call New, which needs a GLFW window.
func newTestInput() (*Input, *fakePads) {
	f := &fakePads{}
	return &Input{padSrc: f, deadzone: DefaultDeadzone}, f
}

// connect marks a pad present with a name, so PadPresent lets its state through.
func (f *fakePads) connect(p Pad, name string) {
	f.state[p].present = true
	f.state[p].name = name
}

// TestPadAxisNormalization pins the three conversions PadAxis performs, each of
// which contradicts what the underlying API reports.
//
// GLFW follows SDL: sticks are +1 *downward* and triggers rest at -1. Callers
// almost never want either. Normalizing in one place is the point — the failure
// mode otherwise is a game that negates Y in some places and not others, which
// looks like an inverted-look bug that only shows up on one axis.
//
// It has teeth: removing the Y negation in PadStick fails the stick cases, and
// dropping the trigger remap fails the trigger case.
func TestPadAxisNormalization(t *testing.T) {
	inp, pads := newTestInput()
	pads.connect(Pad0, "Test Pad")

	// Stick pushed fully "up", which the driver reports as -1.
	pads.state[Pad0].axes[AxisLeftY] = -1
	// Triggers at rest.
	pads.state[Pad0].axes[AxisLeftTrigger] = -1
	pads.state[Pad0].axes[AxisRightTrigger] = 1
	inp.pollPads()

	if got := inp.PadAxis(Pad0, AxisLeftY); got <= 0.99 {
		t.Errorf("stick up gives %g, want +1: up must be positive", got)
	}
	if got := inp.PadAxis(Pad0, AxisLeftTrigger); got != 0 {
		t.Errorf("released trigger gives %g, want 0", got)
	}
	if got := inp.PadAxis(Pad0, AxisRightTrigger); got != 1 {
		t.Errorf("pressed trigger gives %g, want 1", got)
	}

	// And down is negative.
	pads.state[Pad0].axes[AxisLeftY] = 1
	inp.pollPads()
	if got := inp.PadAxis(Pad0, AxisLeftY); got >= -0.99 {
		t.Errorf("stick down gives %g, want -1", got)
	}
}

// TestDeadzoneIsRadialNotPerAxis is the check that matters most for how a
// controller feels.
//
// A per-axis dead zone leaves a square hole around centre, so on a diagonal —
// where both axes are individually small — the stick reads zero even though it is
// pushed well past the dead zone. That is the bug that makes it impossible to walk
// slowly diagonally, and it is invisible on the cardinal directions where most
// testing happens.
//
// It has teeth: replacing applyDeadzone with independent per-axis clamping fails
// the diagonal case while passing every cardinal one.
func TestDeadzoneIsRadialNotPerAxis(t *testing.T) {
	inp, pads := newTestInput()
	pads.connect(Pad0, "Test Pad")
	inp.SetPadDeadzone(0.25)

	// The discriminating case, and it has to be chosen carefully: each axis must
	// sit *strictly inside* the dead zone while the vector's magnitude is outside
	// it. At 0.2 per axis the magnitude is 0.283, past the 0.25 threshold, so a
	// radial dead zone passes it and a per-axis one zeroes both components.
	//
	// An earlier version of this test used 0.2 with a 0.2 dead zone, where the
	// per-axis comparison is not strictly less than and the components survive by
	// accident. It passed against a deliberately per-axis implementation, which is
	// to say it proved nothing about the property it is named for.
	pads.state[Pad0].axes[AxisLeftX] = 0.2
	pads.state[Pad0].axes[AxisLeftY] = -0.2
	inp.pollPads()

	x, y := inp.PadStick(Pad0, StickLeft)
	if x == 0 && y == 0 {
		t.Error("diagonal push past the dead zone reads as centred; the dead zone is square, not radial")
	}
	if x <= 0 || y <= 0 {
		t.Errorf("diagonal push gives (%g, %g), want both positive", x, y)
	}

	// Inside the dead zone in every direction.
	for _, d := range [][2]float32{{0.1, 0}, {0, 0.1}, {0.1, 0.1}, {-0.12, 0.05}} {
		pads.state[Pad0].axes[AxisLeftX] = d[0]
		pads.state[Pad0].axes[AxisLeftY] = d[1]
		inp.pollPads()
		if x, y := inp.PadStick(Pad0, StickLeft); x != 0 || y != 0 {
			t.Errorf("push %v is inside the dead zone but reads (%g, %g)", d, x, y)
		}
	}
}

// TestDeadzoneRescalesAndPreservesDirection checks the two properties that make a
// dead zone feel continuous rather than like a step.
//
// Past the threshold the magnitude has to resume from zero, not jump to the dead
// zone value, or the stick snaps to a fifth of full speed the instant it responds.
// And the direction must survive the rescale, or the character veers off the way
// the stick is pointed.
func TestDeadzoneRescalesAndPreservesDirection(t *testing.T) {
	const dz = 0.25
	inp, pads := newTestInput()
	pads.connect(Pad0, "Test Pad")
	inp.SetPadDeadzone(dz)

	// Just past the threshold: magnitude must be near zero, not near dz.
	pads.state[Pad0].axes[AxisLeftX] = dz + 0.001
	inp.pollPads()
	if x, _ := inp.PadStick(Pad0, StickLeft); x > 0.02 {
		t.Errorf("just past the dead zone reads %g; it should resume from zero, not jump", x)
	}

	// Fully deflected: magnitude must reach one, or the top of the range is lost.
	pads.state[Pad0].axes[AxisLeftX] = 1
	inp.pollPads()
	if x, _ := inp.PadStick(Pad0, StickLeft); x < 0.999 {
		t.Errorf("full deflection reads %g, want 1", x)
	}

	// Direction preserved through the rescale: a 2:1 push stays 2:1.
	pads.state[Pad0].axes[AxisLeftX] = 0.8
	pads.state[Pad0].axes[AxisLeftY] = -0.4
	inp.pollPads()
	x, y := inp.PadStick(Pad0, StickLeft)
	if y == 0 {
		t.Fatal("y came out zero")
	}
	if ratio := x / y; math.Abs(float64(ratio)-2) > 0.01 {
		t.Errorf("x/y = %g after rescale, want 2: the dead zone bent the direction", ratio)
	}

	// Over-unit input, which square-reporting sticks produce at the corners, must
	// clamp to the unit circle rather than exceed full speed.
	pads.state[Pad0].axes[AxisLeftX] = 1
	pads.state[Pad0].axes[AxisLeftY] = -1
	inp.pollPads()
	x, y = inp.PadStick(Pad0, StickLeft)
	if mag := math.Hypot(float64(x), float64(y)); mag > 1.001 {
		t.Errorf("corner push has magnitude %g, want at most 1", mag)
	}
}

// TestPadButtonEdges checks gamepad buttons behave exactly as keyboard keys do,
// so that a binding can treat the two interchangeably.
func TestPadButtonEdges(t *testing.T) {
	inp, pads := newTestInput()
	pads.connect(Pad0, "Test Pad")
	inp.pollPads()

	if inp.PadDown(Pad0, ButtonA) || inp.PadPressed(Pad0, ButtonA) {
		t.Error("button reads as active before anything was pressed")
	}

	pads.state[Pad0].buttons[ButtonA] = true
	inp.pollPads()
	if !inp.PadDown(Pad0, ButtonA) {
		t.Error("Down false while held")
	}
	if !inp.PadPressed(Pad0, ButtonA) {
		t.Error("Pressed false on the frame it went down")
	}
	if inp.PadReleased(Pad0, ButtonA) {
		t.Error("Released true on the frame it went down")
	}

	// Held for a second frame: still down, no longer a fresh press.
	inp.pollPads()
	if !inp.PadDown(Pad0, ButtonA) {
		t.Error("Down false on the second held frame")
	}
	if inp.PadPressed(Pad0, ButtonA) {
		t.Error("Pressed still true on the second held frame; the edge repeated")
	}

	pads.state[Pad0].buttons[ButtonA] = false
	inp.pollPads()
	if inp.PadDown(Pad0, ButtonA) {
		t.Error("Down true after release")
	}
	if !inp.PadReleased(Pad0, ButtonA) {
		t.Error("Released false on the frame it came up")
	}
}

// TestAbsentPadReadsAsNeutral checks an unplugged or unmapped pad reads as doing
// nothing, rather than as a stick jammed in a corner.
//
// This is the hot path on every machine with no controller, and it is also what
// happens mid-game when one is unplugged. An unmapped joystick counts as absent
// on purpose: without a mapping its button indices mean something different on
// every model, so binding to them would be a guess.
func TestAbsentPadReadsAsNeutral(t *testing.T) {
	inp, pads := newTestInput()

	// Present in the driver's eyes but never marked mapped, plus a stick that
	// would read hard-over if the absence check were missing.
	pads.state[Pad1].axes[AxisLeftX] = 1
	pads.state[Pad1].buttons[ButtonA] = true
	inp.pollPads()

	if inp.PadPresent(Pad1) {
		t.Error("unmapped pad reports present")
	}
	if x, y := inp.PadStick(Pad1, StickLeft); x != 0 || y != 0 {
		t.Errorf("absent pad's stick reads (%g, %g), want centred", x, y)
	}
	if inp.PadDown(Pad1, ButtonA) {
		t.Error("absent pad reports a held button")
	}
	if name := inp.PadName(Pad1); name != "" {
		t.Errorf("absent pad has name %q", name)
	}
	if _, ok := inp.FirstPad(); ok {
		t.Error("FirstPad found a pad when none is mapped")
	}

	// Out-of-range indices must not panic.
	if inp.PadPresent(Pad(-1)) || inp.PadPresent(Pad(MaxPads)) {
		t.Error("out-of-range pad reports present")
	}
	inp.PadAxis(Pad(MaxPads), AxisLeftX)
	inp.PadDown(Pad(-1), ButtonA)
}

// TestFirstPadSkipsEmptySlots checks single-player games can find the controller
// without assuming it landed in slot zero, which replugging does not guarantee.
func TestFirstPadSkipsEmptySlots(t *testing.T) {
	inp, pads := newTestInput()
	pads.connect(Pad2, "Third Slot")
	inp.pollPads()

	p, ok := inp.FirstPad()
	if !ok {
		t.Fatal("FirstPad found nothing with a pad in slot 2")
	}
	if p != Pad2 {
		t.Errorf("FirstPad returned %d, want %d", p, Pad2)
	}
}

// TestSetPadDeadzoneRejectsNonsense checks an out-of-range dead zone is refused
// rather than clamped. A dead zone of 2 is a caller bug, and silently clamping it
// to 0.9 leaves them with a stick that barely responds and no idea why.
func TestSetPadDeadzoneRejectsNonsense(t *testing.T) {
	inp, _ := newTestInput()
	original := inp.PadDeadzone()

	for _, bad := range []float32{-0.1, 1.0, 2.0} {
		inp.SetPadDeadzone(bad)
		if inp.PadDeadzone() != original {
			t.Errorf("dead zone %g was accepted; it should be refused", bad)
		}
	}

	inp.SetPadDeadzone(0.3)
	if inp.PadDeadzone() != 0.3 {
		t.Errorf("valid dead zone 0.3 not applied, got %g", inp.PadDeadzone())
	}
}
