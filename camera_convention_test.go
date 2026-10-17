package glyphengine

import (
	"testing"

	"github.com/go-gl/mathgl/mgl32"
)

// TestPitchConventionBothCamerasLookDown pins the one pitch convention both
// cameras share: positive pitch looks DOWN.
//
// The sources disagreed about it. Camera.Pitch's field comment and LookStick's
// comment said "positive = looking up", and docs/agents/input.md repeated that
// the two cameras differ; FPCamera's field comment, the first-person branch of
// Camera.ViewVectors and docs/agents/cameras.md said positive looks down. A
// consumer reported that positive Camera.Pitch looks down. Measured here:
//
//   - Camera, positive pitch: the eye is above the target (eye.Y > target.Y),
//     so the view direction points down. Positive pitch raises the camera and
//     tilts the view down; it never looked up.
//   - FPCamera, positive pitch: Forward().Y < 0, looking down.
//
// So the cameras agree and the "looks up" comments were wrong. Only the
// direction of the stick mapping differs, because a raised orbit camera is
// what "stick up" means there, while FPCamera negates to look up.
func TestPitchConventionBothCamerasLookDown(t *testing.T) {
	const pitch = 0.5

	o := NewCamera(8)
	o.Target = mgl32.Vec3{0, 1, 0}
	o.LookOffset = 0
	o.Yaw = 0
	o.Pitch = pitch
	eye, center, _ := o.ViewVectors()
	if eye.Y() <= o.Target.Y() {
		t.Errorf("orbit camera at positive pitch %g: eye.Y %g not above target.Y %g", pitch, eye.Y(), o.Target.Y())
	}
	if view := center.Sub(eye); view.Y() >= 0 {
		t.Errorf("orbit camera at positive pitch: view direction Y %g, want looking down", view.Y())
	}

	// Close enough to snap to first person, which must agree with the orbit
	// branch rather than flip the view upside down on the transition.
	fpo := NewCamera(0)
	fpo.Pitch = pitch
	fpo.effectiveDistance = 0
	feye, fcenter, _ := fpo.ViewVectors()
	if view := fcenter.Sub(feye); view.Y() >= 0 {
		t.Errorf("orbit camera in first-person mode at positive pitch: view Y %g, want looking down", view.Y())
	}

	f := NewFPCamera()
	f.Pitch = pitch
	if fwd := f.Forward(); fwd.Y() >= 0 {
		t.Errorf("FPCamera at positive pitch %g: Forward().Y %g, want looking down", pitch, fwd.Y())
	}

	f.Pitch = -pitch
	if fwd := f.Forward(); fwd.Y() <= 0 {
		t.Errorf("FPCamera at negative pitch: Forward().Y %g, want looking up", fwd.Y())
	}
}
