package input

import (
	"math"
	"testing"
)

// harness drives an Input without a window, in the order the real engine does it.
//
// The ordering is the whole reason this is a type rather than a couple of methods
// on Input. Update snapshots the previous frame *first*, and only then do the GLFW
// callbacks deliver the new state during PollEvents. A helper that writes straight
// into inp.keys and then copies current to previous makes both frames identical, so
// every edge reads false and every Pressed test passes for the wrong reason. Held
// keys are staged here and promoted in frame, which reproduces the real sequence.
type harness struct {
	*Input
	pads *fakePads
	held map[Key]bool
}

func newHarness() *harness {
	inp, pads := newTestInput()
	return &harness{Input: inp, pads: pads, held: map[Key]bool{}}
}

func (h *harness) press(k Key)   { h.held[k] = true }
func (h *harness) release(k Key) { delete(h.held, k) }

// frame advances one frame: previous becomes current, then the staged state lands.
func (h *harness) frame() {
	h.prevKeys = h.keys
	h.prevButtons = h.buttons
	for i := range h.keys {
		h.keys[i] = false
	}
	for k := range h.held {
		h.keys[k] = true
	}
	h.pollPads()
}

// TestActionFiresFromAnySource is the point of the whole layer: a game asks about
// "jump", and it does not care that one player pressed a key and another pulled a
// trigger.
func TestActionFiresFromAnySource(t *testing.T) {
	h := newHarness()
	h.pads.connect(Pad0, "Test Pad")
	b := NewBindings(h.Input)

	jump := b.Action("jump", Keyboard(KeySpace), PadButton(ButtonA))
	h.frame()

	if b.Down(jump) {
		t.Fatal("action down with nothing pressed")
	}

	// Keyboard path.
	h.press(KeySpace)
	h.frame()
	if !b.Down(jump) || !b.Pressed(jump) {
		t.Error("key source did not fire the action")
	}
	h.release(KeySpace)
	h.frame()
	if !b.Released(jump) {
		t.Error("key release did not release the action")
	}

	// Gamepad path, same action, no game-side change.
	h.pads.state[Pad0].buttons[ButtonA] = true
	h.frame()
	if !b.Down(jump) || !b.Pressed(jump) {
		t.Error("pad source did not fire the same action")
	}
	h.pads.state[Pad0].buttons[ButtonA] = false
	h.frame()
	if !b.Released(jump) {
		t.Error("pad release did not release the action")
	}
}

// TestUnboundAndInvalidHandlesAreInert checks the failure modes that would
// otherwise be silent: an unbound source, and a handle from nowhere.
func TestUnboundAndInvalidHandlesAreInert(t *testing.T) {
	h := newHarness()
	b := NewBindings(h.Input)

	empty := b.Action("nothing")             // declared, no sources
	unbound := b.Action("unbound", Source{}) // declared with the zero Source
	h.frame()

	for name, a := range map[string]Action{"empty": empty, "zero source": unbound} {
		if b.Down(a) || b.Pressed(a) || b.Released(a) {
			t.Errorf("%s action reports activity", name)
		}
	}

	// Handles the Bindings never issued must not panic or report activity.
	for _, a := range []Action{-1, 99} {
		if b.Down(a) || b.Pressed(a) || b.Released(a) {
			t.Errorf("invalid handle %d reports activity", a)
		}
		if n := b.ActionName(a); n != "" {
			t.Errorf("invalid handle %d has name %q", a, n)
		}
		if s := b.ActionSources(a); s != nil {
			t.Errorf("invalid handle %d returned sources", a)
		}
	}
	if b.Value(AxisID(7)) != 0 {
		t.Error("invalid axis handle returned a value")
	}
	if x, y := b.Direction(VectorID(7)); x != 0 || y != 0 {
		t.Error("invalid vector handle returned a direction")
	}
}

// TestAxisDigitalAndAnalog checks an analog source supersedes the digital pair
// only while it is actually deflected.
//
// Getting that precedence wrong in either direction is bad: analog always winning
// means the keyboard stops working the moment a pad is plugged in, and digital
// always winning means the stick does nothing.
func TestAxisDigitalAndAnalog(t *testing.T) {
	h := newHarness()
	h.pads.connect(Pad0, "Test Pad")
	b := NewBindings(h.Input)

	throttle := b.Axis("throttle", Keyboard(KeyS), Keyboard(KeyW))
	b.SetAxisAnalog(throttle, AxisLeftY, false)
	h.frame()

	if v := b.Value(throttle); v != 0 {
		t.Errorf("idle axis reads %g", v)
	}

	// Digital only.
	h.press(KeyW)
	h.frame()
	if v := b.Value(throttle); v != 1 {
		t.Errorf("W held reads %g, want 1", v)
	}

	// Both digital directions cancel.
	h.press(KeyS)
	h.frame()
	if v := b.Value(throttle); v != 0 {
		t.Errorf("W and S together read %g, want 0", v)
	}
	h.release(KeyW)
	h.release(KeyS)

	// Analog deflection wins over the (now idle) digital pair, and partial
	// deflection survives — this is the value that makes a stick analog.
	h.pads.state[Pad0].axes[AxisLeftY] = -0.5 // driver reports up as negative
	h.frame()
	v := b.Value(throttle)
	if v <= 0.2 || v >= 0.95 {
		t.Errorf("half-deflected stick reads %g, want a partial positive value", v)
	}

	// Keyboard still works with the pad connected but centred.
	h.pads.state[Pad0].axes[AxisLeftY] = 0
	h.press(KeyW)
	h.frame()
	if v := b.Value(throttle); v != 1 {
		t.Errorf("W held with a centred stick reads %g, want 1", v)
	}
}

// TestVectorNormalizesDigitalDiagonals checks holding two keys is not faster than
// holding one.
//
// The un-normalized version is a classic: diagonal movement comes out 1.41 times
// as fast, so players strafe-run everywhere, and it is easy to miss because each
// direction alone is correct.
//
// It has teeth: removing the invSqrt2 scaling fails the diagonal case.
func TestVectorNormalizesDigitalDiagonals(t *testing.T) {
	h := newHarness()
	b := NewBindings(h.Input)

	move := b.Vector("move", Keyboard(KeyW), Keyboard(KeyS), Keyboard(KeyA), Keyboard(KeyD))

	h.press(KeyW)
	h.frame()
	x, y := b.Direction(move)
	if mag := math.Hypot(float64(x), float64(y)); math.Abs(mag-1) > 0.001 {
		t.Errorf("single direction has magnitude %g, want 1", mag)
	}

	h.press(KeyD)
	h.frame()
	x, y = b.Direction(move)
	if mag := math.Hypot(float64(x), float64(y)); math.Abs(mag-1) > 0.001 {
		t.Errorf("diagonal has magnitude %g, want 1: diagonals are faster than cardinals", mag)
	}
	if x <= 0 || y <= 0 {
		t.Errorf("diagonal direction is (%g, %g), want both positive", x, y)
	}
}

// TestVectorStickCarriesPartialDeflection checks the stick's magnitude reaches the
// caller, which is the whole basis of analog movement, and that a centred stick
// falls back to the keys.
func TestVectorStickCarriesPartialDeflection(t *testing.T) {
	h := newHarness()
	h.pads.connect(Pad0, "Test Pad")
	b := NewBindings(h.Input)

	move := b.Vector("move", Keyboard(KeyW), Keyboard(KeyS), Keyboard(KeyA), Keyboard(KeyD))
	b.SetVectorStick(move, StickLeft)

	// Half deflection forward.
	h.pads.state[Pad0].axes[AxisLeftY] = -0.55
	h.frame()
	x, y := b.Direction(move)
	mag := math.Hypot(float64(x), float64(y))
	if mag < 0.2 || mag > 0.8 {
		t.Errorf("half-deflected stick gives magnitude %g, want a partial value", mag)
	}
	if y <= 0 {
		t.Errorf("forward push gives y=%g, want positive", y)
	}

	// Centred stick, key held: the keyboard still drives it.
	h.pads.state[Pad0].axes[AxisLeftY] = 0
	h.press(KeyD)
	h.frame()
	x, y = b.Direction(move)
	if x <= 0.99 {
		t.Errorf("with the stick centred, D gives x=%g, want 1", x)
	}
}

// TestRebindReplacesSources checks runtime rebinding, including that rebinding to
// nothing is allowed — a player may want an action with no key at all, and
// silently keeping the old binding would be worse than an empty row.
func TestRebindReplacesSources(t *testing.T) {
	h := newHarness()
	b := NewBindings(h.Input)

	fire := b.Action("fire", Keyboard(KeyF))
	b.Rebind(fire, Keyboard(KeyG))

	h.press(KeyF)
	h.frame()
	if b.Down(fire) {
		t.Error("the old source still fires after a rebind")
	}
	h.release(KeyF)
	h.press(KeyG)
	h.frame()
	if !b.Down(fire) {
		t.Error("the new source does not fire after a rebind")
	}

	// The name survives, because a rebinding screen still has to label the row.
	if b.ActionName(fire) != "fire" {
		t.Errorf("name became %q after rebinding", b.ActionName(fire))
	}

	// Rebinding to nothing.
	b.Rebind(fire)
	h.frame()
	if b.Down(fire) {
		t.Error("action still fires after being unbound")
	}
	if len(b.ActionSources(fire)) != 0 {
		t.Error("unbound action still reports sources")
	}
}

// TestActionSourcesIsACopy checks a rebinding screen cannot corrupt the bindings
// by writing into the slice it was handed.
func TestActionSourcesIsACopy(t *testing.T) {
	h := newHarness()
	b := NewBindings(h.Input)

	fire := b.Action("fire", Keyboard(KeyF))
	got := b.ActionSources(fire)
	got[0] = Keyboard(KeyZ)

	h.press(KeyF)
	h.frame()
	if !b.Down(fire) {
		t.Error("mutating the returned slice changed the live binding")
	}
}

// TestCapturePressedFindsFreshInput covers the loop a rebinding screen runs: show
// a prompt, poll until something is pressed, hand it to Rebind.
func TestCapturePressedFindsFreshInput(t *testing.T) {
	h := newHarness()
	h.pads.connect(Pad0, "Test Pad")
	b := NewBindings(h.Input)

	h.frame()
	if _, ok := b.CapturePressed(); ok {
		t.Error("captured something with nothing pressed")
	}

	h.press(KeyQ)
	h.frame()
	src, ok := b.CapturePressed()
	if !ok {
		t.Fatal("captured nothing after a key press")
	}
	if got := SourceLabel(src); got != "Q" {
		t.Errorf("captured source labels as %q, want \"Q\"", got)
	}

	// Held rather than freshly pressed: a capture screen must not latch the same
	// key twice while the player is still holding it.
	h.frame()
	if _, ok := b.CapturePressed(); ok {
		t.Error("captured a held key a second time")
	}
	h.release(KeyQ)

	// Pad button.
	h.pads.state[Pad0].buttons[ButtonRightBumper] = true
	h.frame()
	src, ok = b.CapturePressed()
	if !ok {
		t.Fatal("captured nothing after a pad press")
	}
	if got := SourceLabel(src); got != "Right Bumper" {
		t.Errorf("captured pad source labels as %q", got)
	}
}

// TestSourceLabels checks the strings a rebinding screen prints, including that an
// unbound source renders visibly rather than as an empty cell.
func TestSourceLabels(t *testing.T) {
	cases := []struct {
		src  Source
		want string
	}{
		{Keyboard(KeyA), "A"},
		{Keyboard(Key7), "7"},
		{Keyboard(KeyF5), "F5"},
		{Keyboard(KeySpace), "Space"},
		{Keyboard(KeyLeftShift), "Left Shift"},
		{Mouse(MouseButtonLeft), "Left Mouse"},
		{PadButton(ButtonA), "A / Cross"},
		{PadButton(ButtonDPadUp), "D-Pad Up"},
		{PadAxisPositive(AxisRightTrigger), "Right Trigger +"},
		{PadAxisNegative(AxisLeftX), "Left Stick X -"},
		{Source{}, "—"},
	}
	for _, c := range cases {
		if got := SourceLabel(c.src); got != c.want {
			t.Errorf("SourceLabel = %q, want %q", got, c.want)
		}
	}
}

// TestTriggerAsButtonHasAnEdge checks an analog axis bound as a button produces
// press and release edges.
//
// A trigger has no digital state to diff, so the edge has to come from the
// threshold crossing between two frames. Without that, Pressed on a trigger is
// either always false or true every frame it is held — and "fire once per pull"
// is exactly what a trigger binding is for.
func TestTriggerAsButtonHasAnEdge(t *testing.T) {
	h := newHarness()
	h.pads.connect(Pad0, "Test Pad")
	b := NewBindings(h.Input)

	shoot := b.Action("shoot", PadAxisPositive(AxisRightTrigger))

	// At rest the driver reports -1.
	h.pads.state[Pad0].axes[AxisRightTrigger] = -1
	h.frame()
	if b.Down(shoot) || b.Pressed(shoot) {
		t.Error("released trigger reads as pressed")
	}

	// Squeezed past halfway.
	h.pads.state[Pad0].axes[AxisRightTrigger] = 1
	h.frame()
	if !b.Down(shoot) {
		t.Error("pulled trigger is not down")
	}
	if !b.Pressed(shoot) {
		t.Error("pulled trigger produced no press edge")
	}

	// Still held: down, but no longer a fresh press.
	h.frame()
	if !b.Down(shoot) {
		t.Error("held trigger stopped being down")
	}
	if b.Pressed(shoot) {
		t.Error("held trigger produced a press edge every frame")
	}

	h.pads.state[Pad0].axes[AxisRightTrigger] = -1
	h.frame()
	if b.Down(shoot) {
		t.Error("released trigger is still down")
	}
	if !b.Released(shoot) {
		t.Error("released trigger produced no release edge")
	}
}

// TestBindingsFollowTheFirstPad checks a single-player game keeps working when the
// controller is not in slot zero, which replugging does not guarantee.
func TestBindingsFollowTheFirstPad(t *testing.T) {
	h := newHarness()
	b := NewBindings(h.Input)
	jump := b.Action("jump", PadButton(ButtonA))

	// Controller in slot 2, nothing in slot 0.
	h.pads.connect(Pad2, "Third Slot")
	h.pads.state[Pad2].buttons[ButtonA] = true
	h.frame()
	if !b.Down(jump) {
		t.Error("bindings did not follow the pad into slot 2")
	}

	// Pinned to a specific pad, the empty slot wins — which is what local
	// multiplayer needs, even though it means player 1 goes quiet.
	b.SetPad(Pad0)
	h.frame()
	if b.Down(jump) {
		t.Error("pinned bindings read a different pad's buttons")
	}
}
