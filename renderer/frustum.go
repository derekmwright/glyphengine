package renderer

import (
	"math"

	"github.com/go-gl/mathgl/mgl32"
)

// Frustum holds the six clip planes extracted from a view-projection matrix.
// Each plane is stored as [nx, ny, nz, d] where nx²+ny²+nz² = 1.
//
// The last two are named for the clip inequality they come from, not "near"
// and "far": which is which depends on the projection. Under the camera's
// reverse-Z matrix z >= 0 is the FAR plane and z <= w the near one; under the
// shadow matrices it is the other way round. Nothing should index them by
// meaning.
type Frustum struct {
	Planes [6][4]float32 // x >= -w, x <= w, y >= -w, y <= w, z >= 0, z <= w
}

// ExtractFrustum extracts 6 normalized clip planes from a view-projection
// matrix using the Gribb-Hartmann method, for VULKAN's clip volume:
// -w <= x <= w, -w <= y <= w, 0 <= z <= w.
//
// The depth planes are the part that differs from every OpenGL write-up of
// this method, which use -w <= z <= w and so take row3 + row2 for the near
// plane. This used to as well. On a Vulkan matrix that plane sits a whole depth
// range too early, and on the camera's reverse-Z matrix it is worse: row3 +
// row2 is satisfied by every point in front of the eye, row3 - row2 turns out
// to be the near plane, and the far plane is never tested at all -- a point a
// million metres out was reported inside. It went unnoticed because the error
// is conservative: nothing vanished, the GPU clipped what the frustum kept, and
// the only cost was recording and shadow-testing geometry nobody could see.
//
// No pipeline here enables depth clamping, so 0 <= z <= w is exactly what
// survives rasterisation in every pass that culls with one of these, the shadow
// passes included. A pass that turns clamping on must stop culling against the
// plane it clamps.
func ExtractFrustum(vp mgl32.Mat4) Frustum {
	// mgl32.Mat4 is column-major: M[col*4+row].
	// Row i: vp[0*4+i], vp[1*4+i], vp[2*4+i], vp[3*4+i]
	row := func(i int) [4]float32 {
		return [4]float32{vp[i], vp[4+i], vp[8+i], vp[12+i]}
	}

	r0 := row(0)
	r1 := row(1)
	r2 := row(2)
	r3 := row(3)

	var f Frustum
	f.Planes[0] = addRow(r3, r0) // x >= -w
	f.Planes[1] = subRow(r3, r0) // x <=  w
	f.Planes[2] = addRow(r3, r1) // y >= -w
	f.Planes[3] = subRow(r3, r1) // y <=  w
	f.Planes[4] = r2             // z >=  0
	f.Planes[5] = subRow(r3, r2) // z <=  w

	// Normalize each plane, so the sphere test compares a distance.
	for i := range f.Planes {
		p := &f.Planes[i]
		length := math.Sqrt(float64(p[0]*p[0] + p[1]*p[1] + p[2]*p[2]))
		if length == 0 {
			// A plane with no normal is not a plane. An infinite-far reverse-Z
			// projection produces one -- its z row is (0, 0, 0, near) -- and
			// dividing by its length would fill the plane with NaN and Inf. It
			// bounds nothing, so make it say so: a constant positive distance
			// that no sphere can be behind.
			*p = [4]float32{0, 0, 0, 1}
			continue
		}
		invLen := float32(1.0 / length)
		p[0] *= invLen
		p[1] *= invLen
		p[2] *= invLen
		p[3] *= invLen
	}
	return f
}

// SphereInFrustum returns true if the sphere (center + radius) is at least
// partially inside all 6 frustum planes.
func (f *Frustum) SphereInFrustum(cx, cy, cz, radius float32) bool {
	for i := range f.Planes {
		p := &f.Planes[i]
		dist := p[0]*cx + p[1]*cy + p[2]*cz + p[3]
		if dist < -radius {
			return false
		}
	}
	return true
}

func addRow(a, b [4]float32) [4]float32 {
	return [4]float32{a[0] + b[0], a[1] + b[1], a[2] + b[2], a[3] + b[3]}
}

func subRow(a, b [4]float32) [4]float32 {
	return [4]float32{a[0] - b[0], a[1] - b[1], a[2] - b[2], a[3] - b[3]}
}
