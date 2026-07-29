package glyphengine

import (
	"testing"

	"github.com/go-gl/mathgl/mgl32"
)

// depthAt returns the reverse-Z depth a point lands at when it is dist units
// straight in front of the camera. View space looks down -Z, so clip w comes out
// as dist and the division is well defined for anything in front of the eye.
func depthAt(proj mgl32.Mat4, dist float32) float32 {
	clip := proj.Mul4x1(mgl32.Vec4{0, 0, -dist, 1})
	return clip.Z() / clip.W()
}

// TestCelestialDiscsStayBehindGeometry pins the two bounds celestialDistance has
// to sit between, both of which were violated by the fixed 80 units it used to
// use: the sun disc drew in front of every piece of terrain further away than
// that.
//
// This test has teeth. Restoring the 80 fails the occlusion half at every far
// plane tested, and moving the discs onto the far plane exactly (multiplier 1.0)
// fails the sky half.
func TestCelestialDiscsStayBehindGeometry(t *testing.T) {
	// A short far plane for an interior, the engine default, and a long one for
	// open terrain. The discs have to behave at all three.
	for _, far := range []float32{100, 500, 8000} {
		e := &Engine{fov: 45, near: 0.1, far: far}
		proj := reverseZProjection(e.fov, 16.0/9.0, e.near, e.far)

		dist := e.celestialDistance()
		if dist >= far {
			t.Errorf("far=%g: disc distance %g is outside the far plane", far, dist)
		}

		// The sky draws after all opaque geometry with CompareOpGreaterOrEqual,
		// so a disc at exactly the far plane's depth loses the tie and vanishes.
		discDepth := depthAt(proj, dist)
		if discDepth <= 0 {
			t.Errorf("far=%g: disc depth %g is at or beyond the far plane; the sky will erase it",
				far, discDepth)
		}

		// Everything the frustum can hold has to win the lit pass's
		// CompareOpGreater against the disc, or the disc occludes the world.
		for _, frac := range []float32{0.01, 0.1, 0.5, 0.9, 0.97} {
			d := depthAt(proj, far*frac)
			if d <= discDepth {
				t.Errorf("far=%g: geometry at %g has depth %g, not in front of the disc's %g — the disc would draw over it",
					far, far*frac, d, discDepth)
			}
		}
	}
}

// TestCelestialApparentSizeIsIndependentOfFarPlane checks the discs are scaled by
// the distance they are placed at, so a game that changes its far plane does not
// silently resize the sun.
//
// Breaking the scaling — dropping the dist/celestialTunedDistance factor from
// celestialModel — fails this at every far plane but the tuned one.
func TestCelestialApparentSizeIsIndependentOfFarPlane(t *testing.T) {
	dir := [3]float32{0, 0.5, 0.866} // arbitrary, mid-elevation

	var want float32
	for i, far := range []float32{100, 500, 8000} {
		e := &Engine{fov: 45, near: 0.1, far: far, cameraEye: mgl32.Vec3{0, 0, 0}}
		model := e.celestialModel(dir)

		// Column 0 is the billboard's right vector, scaled. Its length over the
		// distance to the disc is the tangent of the apparent radius.
		scale := mgl32.Vec3{model[0], model[1], model[2]}.Len()
		apparent := scale / e.celestialDistance()

		if i == 0 {
			want = apparent
			continue
		}
		if d := apparent - want; d > 1e-6 || d < -1e-6 {
			t.Errorf("far=%g: apparent radius %g differs from %g at far=100", far, apparent, want)
		}
	}
}
