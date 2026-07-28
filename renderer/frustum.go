package renderer

import (
	"math"

	"github.com/go-gl/mathgl/mgl32"
)

// Frustum holds the six clip planes extracted from a view-projection matrix.
// Each plane is stored as [nx, ny, nz, d] where nx²+ny²+nz² = 1.
type Frustum struct {
	Planes [6][4]float32 // left, right, bottom, top, near, far
}

// ExtractFrustum extracts 6 normalized clip planes from a view-projection
// matrix using the Gribb-Hartmann method.
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
	// left:   row3 + row0
	f.Planes[0] = addRow(r3, r0)
	// right:  row3 - row0
	f.Planes[1] = subRow(r3, r0)
	// bottom: row3 + row1
	f.Planes[2] = addRow(r3, r1)
	// top:    row3 - row1
	f.Planes[3] = subRow(r3, r1)
	// near:   row3 + row2
	f.Planes[4] = addRow(r3, r2)
	// far:    row3 - row2
	f.Planes[5] = subRow(r3, r2)

	// Normalize each plane
	for i := range f.Planes {
		p := &f.Planes[i]
		invLen := float32(1.0 / math.Sqrt(float64(p[0]*p[0]+p[1]*p[1]+p[2]*p[2])))
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
