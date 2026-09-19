package glyphengine

import (
	"math"

	"github.com/go-gl/mathgl/mgl32"
)

// TransformFromMatrix is Transform.ModelMatrix run backwards: the Position,
// Euler Rotation and Scale whose ModelMatrix is m.
//
// It is here because ModelMatrix's order is this engine's own --
// Translate * RotY * RotX * RotZ * Scale -- and a matrix from anywhere else
// (a glTF node's World, a physics library, a parent's matrix times a child's)
// has to be taken apart against that same product or the entity comes out
// rotated about the wrong axes. A game cannot know the order without reading
// ModelMatrix, and a copy of this function in a game does not find out when
// ModelMatrix changes. TestTransformFromMatrixRoundTrips is what ties the two
// together: it asserts on matrices, not on angles, because several Euler
// triples describe one rotation and only the matrix is the fact.
//
// exact is false when no Transform can reproduce m, and the Transform returned
// is then the closest one that ignores the part that cannot be kept:
//
//   - Shear. A rotated child under a non-uniformly scaled parent is sheared in
//     world space, and Position/Rotation/Scale has nowhere to put that. In a
//     level exported from an editor it means an object was rotated inside a
//     parent that was stretched; applying the parent's scale in the editor
//     before exporting removes it.
//   - A scale of zero on an axis, which leaves that axis's direction unknown.
//
// A mirrored matrix (negative determinant, which is what a mirror modifier or
// a negative scale in an editor exports as) IS representable and comes back
// exact, with the mirror on Scale's X.
func TransformFromMatrix(m mgl32.Mat4) (t Transform, exact bool) {
	t.Position = mgl32.Vec3{m[12], m[13], m[14]}

	cols := [3]mgl32.Vec3{
		{m[0], m[1], m[2]},
		{m[4], m[5], m[6]},
		{m[8], m[9], m[10]},
	}
	exact = true
	for i := range cols {
		l := cols[i].Len()
		t.Scale[i] = l
		if l < 1e-12 {
			// The axis has no direction left. Identity keeps the rest usable.
			cols[i] = mgl32.Vec3{}
			cols[i][i] = 1
			exact = false
			continue
		}
		cols[i] = cols[i].Mul(1 / l)
	}

	// A reflection has no Euler angles. Put the mirror in the scale, on X, and
	// what is left is a rotation again.
	if cols[0].Cross(cols[1]).Dot(cols[2]) < 0 {
		t.Scale[0] = -t.Scale[0]
		cols[0] = cols[0].Mul(-1)
	}

	// Shear shows up as basis vectors that are no longer at right angles. The
	// tolerance is loose enough for float32 matrices composed down a few
	// parents and far tighter than any shear that could be seen.
	const orthoTol = 1e-4
	if abs32(cols[0].Dot(cols[1])) > orthoTol || abs32(cols[0].Dot(cols[2])) > orthoTol || abs32(cols[1].Dot(cols[2])) > orthoTol {
		exact = false
	}

	// r(row, col) of R = Ry(y) * Rx(x) * Rz(z), multiplied out:
	//
	//	r(1,2) = -sin x
	//	r(1,0) =  cos x sin z      r(1,1) = cos x cos z
	//	r(0,2) =  cos x sin y      r(2,2) = cos x cos y
	r := func(row, col int) float64 { return float64(cols[col][row]) }

	sx := -r(1, 2)
	if sx > 1 {
		sx = 1
	} else if sx < -1 {
		sx = -1
	}
	x := math.Asin(sx)
	var y, z float64
	if math.Abs(sx) < 1-1e-6 {
		y = math.Atan2(r(0, 2), r(2, 2))
		z = math.Atan2(r(1, 0), r(1, 1))
	} else {
		// Looking straight up or down the Y and Z rotations turn about the
		// same axis, so only their combination is determined. Give all of it
		// to Y; the matrix that comes back out is the same one.
		//
		// With z = 0: r(0,0) = cos y and r(2,0) = -sin y, whatever x's sign.
		y = math.Atan2(-r(2, 0), r(0, 0))
		z = 0
	}
	t.Rotation = mgl32.Vec3{float32(x), float32(y), float32(z)}
	return t, exact
}
