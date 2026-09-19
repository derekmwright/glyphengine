package glyphengine

import (
	"math"
	"math/rand"
	"testing"

	"github.com/go-gl/mathgl/mgl32"
)

// matDiff is the largest difference between two matrices' elements.
func matDiff(a, b mgl32.Mat4) float32 {
	var d float32
	for i := range a {
		if v := abs32(a[i] - b[i]); v > d {
			d = v
		}
	}
	return d
}

// TestTransformFromMatrixRoundTrips is what keeps TransformFromMatrix the
// inverse of ModelMatrix rather than of some other Euler order.
//
// It compares MATRICES. Comparing the angles that come back with the angles
// that went in would be wrong twice over: several triples describe one
// rotation, so a correct answer can differ from the input, and an extraction
// against the wrong order returns perfectly plausible angles -- it is only when
// they are fed back through ModelMatrix that the box is seen to point somewhere
// else.
//
// Verified to fail, each on its own: extracting against Rx*Ry*Rz instead (the
// usual textbook order) fails all 2000 random cases and the mirrored ones;
// reading the pole branch's angle from atan2(r(0,1), r(1,1)) fails the
// straight-up and straight-down cases and nothing else; and dropping the
// reflection handling fails the three mirrored cases, by about 1.6 in matrices
// whose elements are at most 3, and nothing else.
func TestTransformFromMatrixRoundTrips(t *testing.T) {
	const tol = 2e-5

	check := func(name string, in Transform) {
		t.Helper()
		m := in.ModelMatrix()
		got, exact := TransformFromMatrix(m)
		if !exact {
			t.Errorf("%s: %+v came back inexact, and a Transform's own matrix never is", name, in)
		}
		if d := matDiff(got.ModelMatrix(), m); d > tol*maxScale(in) {
			t.Errorf("%s: %+v came back as %+v, whose matrix differs by %g", name, in, got, d)
		}
	}

	rng := rand.New(rand.NewSource(66))
	angle := func() float32 { return (rng.Float32()*2 - 1) * math.Pi }
	fails := 0
	for i := 0; i < 2000; i++ {
		in := Transform{
			Position: mgl32.Vec3{rng.Float32()*200 - 100, rng.Float32()*50 - 25, rng.Float32()*200 - 100},
			// X is kept off the poles here; they are their own cases below.
			Rotation: mgl32.Vec3{(rng.Float32()*2 - 1) * 1.5, angle(), angle()},
			Scale:    mgl32.Vec3{0.2 + rng.Float32()*2.8, 0.2 + rng.Float32()*2.8, 0.2 + rng.Float32()*2.8},
		}
		m := in.ModelMatrix()
		got, exact := TransformFromMatrix(m)
		if d := matDiff(got.ModelMatrix(), m); !exact || d > tol*maxScale(in) {
			if fails == 0 {
				t.Errorf("random case %d: %+v came back as %+v (exact=%v), matrix differs by %g", i, in, got, exact, d)
			}
			fails++
		}
	}
	if fails > 0 {
		t.Errorf("%d of 2000 random transforms did not survive the round trip", fails)
	}

	half := float32(math.Pi / 2)
	check("identity", Transform{Scale: mgl32.Vec3{1, 1, 1}})
	check("the measured Blender Alt-D duplicate", Transform{
		Position: mgl32.Vec3{6, 1, -2}, Rotation: mgl32.Vec3{0, 0.6, 0}, Scale: mgl32.Vec3{1, 1.5, 2}})
	check("straight up, with yaw and roll", Transform{Rotation: mgl32.Vec3{half, 0.7, -0.4}, Scale: mgl32.Vec3{1, 2, 3}})
	check("straight down, with yaw and roll", Transform{Rotation: mgl32.Vec3{-half, -1.1, 0.9}, Scale: mgl32.Vec3{2, 1, 0.5}})
	check("mirrored on X", Transform{Rotation: mgl32.Vec3{0.3, 1.2, -0.5}, Scale: mgl32.Vec3{-1, 1.5, 2}})
	check("mirrored on Z", Transform{Rotation: mgl32.Vec3{-0.8, 0.2, 2.5}, Scale: mgl32.Vec3{1, 1, -3}})
	check("mirrored on all three", Transform{Rotation: mgl32.Vec3{0.1, -2, 0.4}, Scale: mgl32.Vec3{-1, -2, -0.5}})
}

func maxScale(t Transform) float32 {
	m := float32(1)
	for _, s := range t.Scale {
		if abs32(s) > m {
			m = abs32(s)
		}
	}
	return m
}

// TestTransformFromMatrixReportsWhatItCannotKeep: a matrix no Transform can
// reproduce comes back flagged, because the alternative is a level whose one
// sheared object is quietly the wrong shape with nothing to say why.
//
// Verified to fail: with the orthogonality check removed the sheared matrix
// comes back exact.
func TestTransformFromMatrixReportsWhatItCannotKeep(t *testing.T) {
	// A child rotated 45 degrees inside a parent stretched 3x on Y: the classic
	// way an editor produces shear.
	parent := Transform{Scale: mgl32.Vec3{1, 3, 1}}
	child := Transform{Rotation: mgl32.Vec3{0, 0, math.Pi / 4}, Scale: mgl32.Vec3{1, 1, 1}}
	if _, exact := TransformFromMatrix(parent.ModelMatrix().Mul4(child.ModelMatrix())); exact {
		t.Error("a sheared matrix came back exact")
	}

	// The same parent with an UNROTATED child is a plain non-uniform scale.
	still := Transform{Position: mgl32.Vec3{1, 2, 3}, Scale: mgl32.Vec3{2, 1, 1}}
	m := parent.ModelMatrix().Mul4(still.ModelMatrix())
	got, exact := TransformFromMatrix(m)
	if !exact || matDiff(got.ModelMatrix(), m) > 1e-5 {
		t.Errorf("a stretched parent over an unrotated child came back as %+v (exact=%v)", got, exact)
	}

	flat := Transform{Scale: mgl32.Vec3{1, 0, 1}}
	if _, exact := TransformFromMatrix(flat.ModelMatrix()); exact {
		t.Error("a matrix flattened to zero on Y came back exact")
	}
}
