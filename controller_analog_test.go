package glyphengine

import (
	"math"
	"testing"

	"github.com/go-gl/mathgl/mgl32"
)

// horizontalSpeed returns how fast a character is moving across the ground,
// ignoring gravity's contribution on Y.
func horizontalSpeed(s *Scene, e Entity, t *testing.T) float32 {
	t.Helper()
	vel, ok := s.C.Velocity.Get(e)
	if !ok {
		t.Fatal("character has no Velocity")
	}
	return float32(math.Hypot(float64(vel.Vec[0]), float64(vel.Vec[2])))
}

// moveOnce spawns a character, applies one tick of the given intent, and returns
// its resulting horizontal speed.
func moveOnce(t *testing.T, intent MoveIntent) float32 {
	t.Helper()
	s := NewScene()
	s.SetTerrain(flatTerrain(t, 0))
	e := spawnCharacter(s, mgl32.Vec3{0, 0.9, 0})
	s.UpdateSpatialGrid()
	s.MoveCharacter(e, intent, 1.0/60.0)
	return horizontalSpeed(s, e, t)
}

// TestPartialDeflectionWalksSlower is the check that makes a thumbstick worth
// having.
//
// MoveCharacter used to reduce Forward and Right to their signs and then
// normalize, so any non-zero deflection produced full speed — a stick pushed a
// third of the way walked exactly as fast as one pushed to the stop. The engine
// had no analog movement at all, and MoveIntent documented the behaviour as
// intentional, which is why it survived.
//
// It has teeth: restoring the sign-and-normalize form makes every partial case
// here report full speed.
func TestPartialDeflectionWalksSlower(t *testing.T) {
	cc := NewCharacterController()

	full := moveOnce(t, MoveIntent{Forward: 1})
	if math.Abs(float64(full-cc.WalkSpeed)) > 0.001 {
		t.Fatalf("full deflection gives %g, want WalkSpeed %g", full, cc.WalkSpeed)
	}

	for _, deflection := range []float32{0.25, 0.5, 0.75} {
		got := moveOnce(t, MoveIntent{Forward: deflection})
		want := cc.WalkSpeed * deflection
		if math.Abs(float64(got-want)) > 0.001 {
			t.Errorf("deflection %g gives speed %g, want %g", deflection, got, want)
		}
		if got >= full {
			t.Errorf("deflection %g is not slower than full deflection (%g vs %g)",
				deflection, got, full)
		}
	}
}

// TestDigitalMovementIsUnchanged pins the compatibility claim: every existing
// caller passes ±1, and those must behave exactly as before.
//
// The diagonal case is the one that could regress. The old code normalized
// unconditionally, so a (1,1) intent came out at unit length; the new code clamps,
// which reaches the same answer by a different route. If it did not, holding two
// keys would move √2 times as fast — the strafe-running bug.
func TestDigitalMovementIsUnchanged(t *testing.T) {
	cc := NewCharacterController()

	cases := []struct {
		name   string
		intent MoveIntent
	}{
		{"forward", MoveIntent{Forward: 1}},
		{"back", MoveIntent{Forward: -1}},
		{"strafe", MoveIntent{Right: 1}},
		{"forward-right diagonal", MoveIntent{Forward: 1, Right: 1}},
		{"back-left diagonal", MoveIntent{Forward: -1, Right: -1}},
	}
	for _, c := range cases {
		got := moveOnce(t, c.intent)
		if math.Abs(float64(got-cc.WalkSpeed)) > 0.001 {
			t.Errorf("%s gives speed %g, want WalkSpeed %g", c.name, got, cc.WalkSpeed)
		}
	}

	// Sprint and SpeedScale still multiply the result rather than being replaced
	// by the magnitude.
	if got := moveOnce(t, MoveIntent{Forward: 1, Sprint: true}); math.Abs(float64(got-cc.RunSpeed)) > 0.001 {
		t.Errorf("sprint gives %g, want RunSpeed %g", got, cc.RunSpeed)
	}
	if got := moveOnce(t, MoveIntent{Forward: 0.5, Sprint: true}); math.Abs(float64(got-cc.RunSpeed*0.5)) > 0.001 {
		t.Errorf("half-deflected sprint gives %g, want half RunSpeed %g", got, cc.RunSpeed*0.5)
	}
	if got := moveOnce(t, MoveIntent{Forward: 1, SpeedScale: 2}); math.Abs(float64(got-cc.WalkSpeed*2)) > 0.001 {
		t.Errorf("SpeedScale 2 gives %g, want %g", got, cc.WalkSpeed*2)
	}
}

// TestOverUnitIntentIsClamped checks a caller — or a stick reporting a square
// range rather than a circular one — cannot exceed full speed by pushing into a
// corner.
func TestOverUnitIntentIsClamped(t *testing.T) {
	cc := NewCharacterController()
	for _, intent := range []MoveIntent{
		{Forward: 2},
		{Forward: 1, Right: 1},
		{Forward: 5, Right: -5},
	} {
		got := moveOnce(t, intent)
		if got > cc.WalkSpeed+0.001 {
			t.Errorf("intent %+v gives speed %g, above WalkSpeed %g", intent, got, cc.WalkSpeed)
		}
	}
}

// TestDeflectionPreservesDirection checks scaling the magnitude does not bend the
// heading: a quarter-deflected diagonal must still travel diagonally.
func TestDeflectionPreservesDirection(t *testing.T) {
	s := NewScene()
	s.SetTerrain(flatTerrain(t, 0))
	e := spawnCharacter(s, mgl32.Vec3{0, 0.9, 0})
	s.UpdateSpatialGrid()

	// Yaw zero: forward is -Z, right is +X, so an equal push should give equal
	// magnitudes on both.
	s.MoveCharacter(e, MoveIntent{Forward: 0.25, Right: 0.25}, 1.0/60.0)
	vel, _ := s.C.Velocity.Get(e)

	if vel.Vec[0] == 0 || vel.Vec[2] == 0 {
		t.Fatalf("diagonal intent produced velocity %v with a zero component", vel.Vec)
	}
	ratio := math.Abs(float64(vel.Vec[0] / vel.Vec[2]))
	if math.Abs(ratio-1) > 0.01 {
		t.Errorf("|x/z| = %g for an equal diagonal push, want 1: the direction was bent", ratio)
	}
}
