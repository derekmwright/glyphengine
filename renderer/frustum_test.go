package renderer

import (
	"math/rand/v2"
	"testing"

	"github.com/go-gl/mathgl/mgl32"
)

// reverseZForTest is app.go's reverseZProjection, copied because the root
// package imports this one. It is the matrix the camera frustum is actually
// extracted from, which is the point: a frustum test against a conventional
// OpenGL projection would have passed for years while the engine culled with
// the wrong planes.
func reverseZForTest(fovDegrees, aspect, near, far float32) mgl32.Mat4 {
	proj := mgl32.Perspective(mgl32.DegToRad(fovDegrees), aspect, near, far)
	proj[10] = near / (far - near)
	proj[14] = (far * near) / (far - near)
	proj[5] *= -1
	return proj
}

// insideClipVolume is the oracle: what the GPU keeps. Vulkan clips to
// -w <= x <= w, -w <= y <= w, 0 <= z <= w, and no pipeline here enables depth
// clamping, so this is the whole truth for every pass that culls with a
// Frustum. margin pulls the test in from the boundary so float rounding on a
// point sitting on a plane cannot decide the result.
func insideClipVolume(vp mgl32.Mat4, p mgl32.Vec3, margin float32) (inside, nearBoundary bool) {
	c := vp.Mul4x1(mgl32.Vec4{p[0], p[1], p[2], 1})
	w := c[3]
	d := [6]float32{w + c[0], w - c[0], w + c[1], w - c[1], c[2], w - c[2]}
	inside = true
	for _, v := range d {
		if v < 0 {
			inside = false
		}
		if v > -margin && v < margin {
			nearBoundary = true
		}
	}
	return inside, nearBoundary
}

type frustumCase struct {
	name string
	vp   mgl32.Mat4
	// box bounds the region points are sampled from, generously larger than
	// the volume so that every plane has points on both sides of it.
	lo, hi mgl32.Vec3
}

func frustumCases() []frustumCase {
	eye := mgl32.Vec3{3, 12, 40}
	view := mgl32.LookAtV(eye, mgl32.Vec3{0, 0, 0}, mgl32.Vec3{0, 1, 0})
	camera := reverseZForTest(45, 16.0/9.0, 0.1, 500).Mul4(view)

	// A short far plane, so the sampling box reaches well past it without the
	// points thinning out to nothing.
	shortFar := reverseZForTest(60, 1.5, 0.5, 60).Mul4(view)

	sun := [3]float32{0.4, 0.8, 0.3}
	cascade := computeLightVP(sun, mgl32.Vec3{5, 0, -5}, 40)

	face := ComputeCubeFaceVP(mgl32.Vec3{2, 3, -1}, 25, 4)

	return []frustumCase{
		{"reverse-Z camera", camera, mgl32.Vec3{-900, -900, -900}, mgl32.Vec3{900, 900, 900}},
		{"reverse-Z camera, far 60", shortFar, mgl32.Vec3{-150, -150, -150}, mgl32.Vec3{150, 150, 150}},
		{"sun cascade (ortho)", cascade, mgl32.Vec3{-200, -200, -200}, mgl32.Vec3{200, 200, 200}},
		{"point shadow cube face", face, mgl32.Vec3{-60, -60, -60}, mgl32.Vec3{60, 60, 60}},
	}
}

// TestFrustumMatchesTheClipVolume holds ExtractFrustum to the volume the GPU
// clips to, for every kind of matrix the engine extracts one from.
//
// It exists because the planes were OpenGL's (-w <= z <= w) on Vulkan matrices
// (0 <= z <= w) and nothing noticed, because the error was conservative. Under
// the camera's reverse-Z projection "row3 + row2" is satisfied by every point
// in front of the eye and "row3 - row2" is the NEAR plane, so the far plane was
// never tested: a point a million metres out reported inside. Under the shadow
// matrices the far plane was right and the near plane sat a full depth range
// too early.
//
// Verified to fail on that code. With the OpenGL planes restored the frustum
// keeps 11996 points the GPU clips against 1700 it shows under the camera matrix
// (far 500), 35967 against 1131 with the far plane at 60, and 1947 against 2060
// under the sun cascade, where it was the near plane that was loose; and
// TestFrustumRejectsBeyondTheFarPlane fails outright.
func TestFrustumMatchesTheClipVolume(t *testing.T) {
	for _, tc := range frustumCases() {
		t.Run(tc.name, func(t *testing.T) {
			f := ExtractFrustum(tc.vp)
			rng := rand.New(rand.NewPCG(41, 7))
			var in, out, keptButClipped, culledButVisible int
			for i := 0; i < 200000; i++ {
				p := mgl32.Vec3{
					tc.lo[0] + rng.Float32()*(tc.hi[0]-tc.lo[0]),
					tc.lo[1] + rng.Float32()*(tc.hi[1]-tc.lo[1]),
					tc.lo[2] + rng.Float32()*(tc.hi[2]-tc.lo[2]),
				}
				want, edge := insideClipVolume(tc.vp, p, 1e-3)
				if edge {
					continue
				}
				got := f.SphereInFrustum(p[0], p[1], p[2], 0)
				switch {
				case want:
					in++
				default:
					out++
				}
				if got && !want {
					keptButClipped++
				}
				if !got && want {
					culledButVisible++
				}
			}
			t.Logf("%d sampled points inside the clip volume, %d outside", in, out)
			// Both sides of the question have to have been asked.
			if in < 500 || out < 500 {
				t.Fatalf("sampling is lopsided (%d inside, %d outside): the box does not straddle the volume", in, out)
			}
			// The direction that loses geometry. Never acceptable.
			if culledButVisible > 0 {
				t.Errorf("%d points are inside the clip volume but culled by the frustum", culledButVisible)
			}
			// The direction that wastes work. For a POINT the planes are exact,
			// so this must be zero too; it is the half the old planes failed.
			if keptButClipped > 0 {
				t.Errorf("%d of %d outside points are kept by the frustum though the GPU clips them", keptButClipped, out)
			}
		})
	}
}

// TestFrustumRejectsBeyondTheFarPlane is the reported symptom, stated plainly.
func TestFrustumRejectsBeyondTheFarPlane(t *testing.T) {
	eye := mgl32.Vec3{0, 0, 0}
	view := mgl32.LookAtV(eye, mgl32.Vec3{0, 0, -1}, mgl32.Vec3{0, 1, 0})
	f := ExtractFrustum(reverseZForTest(45, 16.0/9.0, 0.1, 500).Mul4(view))

	for _, tc := range []struct {
		depth float32
		want  bool
	}{
		{0.05, false}, // nearer than near
		{0.2, true},
		{250, true},
		{499, true},
		{501, false},
		{1_000_000, false}, // reported inside before the fix
		{-10, false},       // behind the eye
	} {
		if got := f.SphereInFrustum(0, 0, -tc.depth, 0); got != tc.want {
			t.Errorf("on-axis point at depth %g: inside = %v, want %v", tc.depth, got, tc.want)
		}
	}

	// A sphere straddling the far plane is still in.
	if !f.SphereInFrustum(0, 0, -505, 10) {
		t.Error("a sphere reaching back across the far plane was culled")
	}
	if f.SphereInFrustum(0, 0, -520, 10) {
		t.Error("a sphere wholly beyond the far plane was kept")
	}
}

// TestFrustumIsConservativeForSpheres: a sphere that contains any visible point
// must never be culled. Rejecting on "centre further than radius behind one
// plane" guarantees it, and this is here so a cleverer test cannot quietly
// trade that away.
func TestFrustumIsConservativeForSpheres(t *testing.T) {
	for _, tc := range frustumCases() {
		t.Run(tc.name, func(t *testing.T) {
			f := ExtractFrustum(tc.vp)
			rng := rand.New(rand.NewPCG(9, 41))
			checked := 0
			for i := 0; i < 100000; i++ {
				p := mgl32.Vec3{
					tc.lo[0] + rng.Float32()*(tc.hi[0]-tc.lo[0]),
					tc.lo[1] + rng.Float32()*(tc.hi[1]-tc.lo[1]),
					tc.lo[2] + rng.Float32()*(tc.hi[2]-tc.lo[2]),
				}
				if in, edge := insideClipVolume(tc.vp, p, 1e-3); !in || edge {
					continue
				}
				// A sphere around a nearby centre, big enough to contain p.
				r := 0.5 + rng.Float32()*30
				dir := mgl32.Vec3{rng.Float32()*2 - 1, rng.Float32()*2 - 1, rng.Float32()*2 - 1}
				if dir.Len() < 1e-3 {
					continue
				}
				c := p.Add(dir.Normalize().Mul(r * 0.95))
				checked++
				if !f.SphereInFrustum(c[0], c[1], c[2], r) {
					t.Fatalf("sphere at %v r=%g contains visible point %v but was culled", c, r, p)
				}
			}
			if checked < 500 {
				t.Fatalf("only %d spheres checked", checked)
			}
		})
	}
}

// TestFrustumSurvivesADegenerateDepthRow: an infinite-far reverse-Z projection
// has no far plane at all -- its z row is (0, 0, 0, near), a plane with no
// normal. Normalising that divides by zero, and a NaN plane makes every
// comparison false, which SphereInFrustum reads as "inside": harmless here, but
// only by accident, and Inf is not. The plane must come out vacuous on purpose.
//
// Verified to fail with the zero-length guard removed: plane 4 comes out as
// [NaN NaN NaN +Inf].
func TestFrustumSurvivesADegenerateDepthRow(t *testing.T) {
	proj := mgl32.Perspective(mgl32.DegToRad(60), 1.5, 0.1, 100)
	proj[10] = 0
	proj[14] = 0.1
	proj[5] *= -1
	f := ExtractFrustum(proj)
	for i, p := range f.Planes {
		for _, v := range p {
			if v != v || v > 1e30 || v < -1e30 {
				t.Fatalf("plane %d is not finite: %v", i, p)
			}
		}
	}
	if !f.SphereInFrustum(0, 0, -1_000_000, 0) {
		t.Error("an infinite-far projection culled a distant on-axis point")
	}
	if f.SphereInFrustum(0, 0, 5, 0) {
		t.Error("a point behind the eye was kept")
	}
}
