package glyphengine

import (
	"math"
	"testing"
)

// TestStickLookIsFrameRateIndependent is the property the whole seam exists for.
//
// A mouse reports how far it moved since the last frame, so the displacement is
// already the answer and no dt appears anywhere. A stick reports only how far it
// is pushed, which says nothing about elapsed time. Feeding stick deflection
// through the mouse path — the obvious mistake, since both are "two floats from
// an input device" — makes the camera turn once per frame instead of once per
// second, so it spins twice as fast at 120fps as at 60.
//
// Turning for one second must produce the same rotation however many frames that
// second is divided into.
//
// It has teeth: dropping the dt factor from either LookStick makes the 240-step
// case turn four times as far as the 60-step case.
func TestStickLookIsFrameRateIndependent(t *testing.T) {
	turnFor := func(steps int) (fp, orbit float32) {
		const seconds = 1.0
		dt := float32(seconds) / float32(steps)

		f := NewFPCamera()
		o := NewCamera(6)
		for i := 0; i < steps; i++ {
			f.LookStick(1, 0, dt)
			o.LookStick(1, 0, dt)
		}
		return f.Yaw, o.Yaw
	}

	fp60, orbit60 := turnFor(60)
	for _, steps := range []int{30, 120, 240} {
		fp, orbit := turnFor(steps)
		if d := math.Abs(float64(fp - fp60)); d > 1e-4 {
			t.Errorf("FPCamera: %d steps turned %g, 60 steps turned %g (differ by %g)",
				steps, fp, fp60, d)
		}
		if d := math.Abs(float64(orbit - orbit60)); d > 1e-4 {
			t.Errorf("Camera: %d steps turned %g, 60 steps turned %g (differ by %g)",
				steps, orbit, orbit60, d)
		}
	}

	// And a full second at full deflection should actually turn by StickLookRate,
	// so the units in the field's name mean what they say.
	f := NewFPCamera()
	if math.Abs(float64(-fp60-f.StickLookRate)) > 1e-4 {
		t.Errorf("one second of full deflection turned %g, want StickLookRate %g",
			-fp60, f.StickLookRate)
	}
}

// TestStickLookDirections pins the sign conventions, which differ between the two
// cameras — FPCamera's positive pitch looks down, the orbit camera's looks up. The
// field comments say so; this makes sure the stick paths honour it, because an
// inverted look axis is the sort of thing that ships.
func TestStickLookDirections(t *testing.T) {
	const dt = 1.0 / 60.0

	f := NewFPCamera()
	f.LookStick(1, 0, dt) // push right
	if f.Yaw >= 0 {
		t.Errorf("FPCamera yaw %g after pushing right; yaw decreases turning right", f.Yaw)
	}
	f = NewFPCamera()
	f.LookStick(0, 1, dt) // push up
	if f.Pitch >= 0 {
		t.Errorf("FPCamera pitch %g after pushing up; positive pitch looks down", f.Pitch)
	}

	o := NewCamera(6)
	start := o.Pitch
	o.LookStick(0, 1, dt) // push up
	if o.Pitch <= start {
		t.Errorf("orbit pitch %g after pushing up, was %g; positive pitch looks up", o.Pitch, start)
	}

	// InvertY flips the vertical axis for the stick as well as the mouse.
	f = NewFPCamera()
	f.InvertY = true
	f.LookStick(0, 1, dt)
	if f.Pitch <= 0 {
		t.Errorf("with InvertY, pushing up gave pitch %g; it should look down", f.Pitch)
	}
}

// TestStickLookClampsAndWraps checks a stick held for a long time cannot walk the
// camera past vertical or let yaw grow without bound.
//
// This matters more for a stick than for a mouse: a player can hold a stick at full
// deflection for minutes, accumulating far more rotation than a hand ever pushes a
// mouse, so unbounded yaw actually loses precision here.
func TestStickLookClampsAndWraps(t *testing.T) {
	const dt = 1.0 / 60.0

	f := NewFPCamera()
	for i := 0; i < 600; i++ {
		f.LookStick(0, 1, dt) // hold up for ten seconds
	}
	limit := float32(math.Pi/2 - 0.01)
	if f.Pitch < -limit-1e-5 {
		t.Errorf("pitch reached %g, past the -%g limit", f.Pitch, limit)
	}

	f = NewFPCamera()
	for i := 0; i < 6000; i++ {
		f.LookStick(1, 0, dt) // spin for a hundred seconds
	}
	if f.Yaw < -math.Pi || f.Yaw > math.Pi {
		t.Errorf("yaw drifted to %g, outside [-π, π]", f.Yaw)
	}

	o := NewCamera(6)
	for i := 0; i < 600; i++ {
		o.LookStick(0, 1, dt)
	}
	if o.Pitch > limit+1e-5 {
		t.Errorf("orbit pitch reached %g, past the %g limit", o.Pitch, limit)
	}
}

// TestZoomByIsFrameRateIndependentAndClamped covers the other rate-based control:
// a held trigger or bumper pulling the orbit camera in.
func TestZoomByIsFrameRateIndependentAndClamped(t *testing.T) {
	zoomFor := func(steps int) float32 {
		dt := 1.0 / float32(steps)
		c := NewCamera(10)
		for i := 0; i < steps; i++ {
			c.ZoomBy(1, dt)
		}
		return c.Distance
	}

	base := zoomFor(60)
	for _, steps := range []int{30, 240} {
		if d := math.Abs(float64(zoomFor(steps) - base)); d > 1e-4 {
			t.Errorf("%d steps zoomed to a different distance than 60 (differ by %g)", steps, d)
		}
	}

	// Held long past the limits, it must stop rather than invert.
	c := NewCamera(10)
	for i := 0; i < 600; i++ {
		c.ZoomBy(1, 1.0/60.0)
	}
	if c.Distance < cameraMinDistance {
		t.Errorf("distance fell to %g, below the %g minimum", c.Distance, float32(cameraMinDistance))
	}
	for i := 0; i < 1200; i++ {
		c.ZoomBy(-1, 1.0/60.0)
	}
	if c.Distance > cameraMaxDistance {
		t.Errorf("distance rose to %g, above the %g maximum", c.Distance, float32(cameraMaxDistance))
	}
}
