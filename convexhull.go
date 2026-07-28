package glyphengine

import (
	"encoding/binary"
	"fmt"
	"math"

	"github.com/go-gl/mathgl/mgl32"
	"github.com/golang/geo/r3"
	quickhull "github.com/markus-wa/quickhull-go/v2"
	"github.com/qmuntal/gltf"
	"github.com/qmuntal/gltf/modeler"
)

// ConvexHullCollider is an ECS component that stores a convex hull in model-local
// space. Vertices are the hull points; Faces are triangulated with outward normals.
// The entity's Transform (position + Y rotation + uniform scale) is applied at
// runtime. The regular Collider (AABB) remains for broad-phase culling.
type ConvexHullCollider struct {
	Vertices []mgl32.Vec3
	Faces    []HullFace
}

// HullFace is a single triangulated face of the convex hull.
type HullFace struct {
	A, B, C int        // indices into Vertices
	Normal  mgl32.Vec3 // precomputed outward unit normal
}

// HullColliderInfo pairs a hull with its transform for NavGrid building.
type HullColliderInfo struct {
	Hull      *ConvexHullCollider
	Transform *Transform
}

// ---------------------------------------------------------------------------
// glTF vertex extraction (no renderer dependency)
// ---------------------------------------------------------------------------

// ExtractGLTFPositions opens a glTF/GLB file and returns all vertex positions
// across all meshes and primitives. No GPU or Renderer required.
func ExtractGLTFPositions(path string) ([][3]float32, error) {
	doc, err := gltf.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open gltf %q: %w", path, err)
	}

	var positions [][3]float32
	for _, mesh := range doc.Meshes {
		for _, prim := range mesh.Primitives {
			posIdx, ok := prim.Attributes[gltf.POSITION]
			if !ok {
				continue
			}
			pos, err := modeler.ReadPosition(doc, doc.Accessors[posIdx], nil)
			if err != nil {
				return nil, fmt.Errorf("read positions: %w", err)
			}
			positions = append(positions, pos...)
		}
	}
	if len(positions) == 0 {
		return nil, fmt.Errorf("no vertex positions found in %q", path)
	}
	return positions, nil
}

// ---------------------------------------------------------------------------
// Quickhull — compute convex hull from point cloud
// ---------------------------------------------------------------------------

// ComputeConvexHull computes the convex hull of a 3D point cloud.
// Uses the quickhull-go library for a correct, closed mesh.
// Returns a ConvexHullCollider with hull vertices and triangulated faces.
func ComputeConvexHull(points [][3]float32) *ConvexHullCollider {
	if len(points) < 4 {
		return &ConvexHullCollider{}
	}

	// Convert to r3.Vector for the quickhull library.
	cloud := make([]r3.Vector, len(points))
	for i, p := range points {
		cloud[i] = r3.Vector{X: float64(p[0]), Y: float64(p[1]), Z: float64(p[2])}
	}

	qh := new(quickhull.QuickHull)
	hull := qh.ConvexHull(cloud, false, false, 1e-6)

	if len(hull.Vertices) < 4 {
		return &ConvexHullCollider{}
	}

	// Convert vertices to mgl32.Vec3.
	verts := make([]mgl32.Vec3, len(hull.Vertices))
	for i, v := range hull.Vertices {
		verts[i] = mgl32.Vec3{float32(v.X), float32(v.Y), float32(v.Z)}
	}

	// Convert index buffer to faces with computed normals.
	// hull.Indices is a flat []int, every 3 = one triangle.
	nf := len(hull.Indices) / 3
	faces := make([]HullFace, nf)
	for i := 0; i < nf; i++ {
		a, b, c := hull.Indices[i*3], hull.Indices[i*3+1], hull.Indices[i*3+2]
		ab := verts[b].Sub(verts[a])
		ac := verts[c].Sub(verts[a])
		n := ab.Cross(ac)
		l := n.Len()
		if l > 1e-10 {
			n = n.Mul(1.0 / l)
		}
		faces[i] = HullFace{A: a, B: b, C: c, Normal: n}
	}

	return &ConvexHullCollider{Vertices: verts, Faces: faces}
}

// ---------------------------------------------------------------------------
// Serialization
// ---------------------------------------------------------------------------

const hullFormatVersion = 1

// SerializeHull encodes a convex hull into a compact binary blob.
// Format: [uint8 version][uint16 vertCount][float32×3 verts...][uint16 faceCount][uint16×3 faces...]
func SerializeHull(hull *ConvexHullCollider) []byte {
	nv := len(hull.Vertices)
	nf := len(hull.Faces)
	size := 1 + 2 + nv*12 + 2 + nf*6
	buf := make([]byte, size)

	buf[0] = hullFormatVersion
	binary.LittleEndian.PutUint16(buf[1:], uint16(nv))
	off := 3
	for _, v := range hull.Vertices {
		binary.LittleEndian.PutUint32(buf[off:], math.Float32bits(v.X()))
		binary.LittleEndian.PutUint32(buf[off+4:], math.Float32bits(v.Y()))
		binary.LittleEndian.PutUint32(buf[off+8:], math.Float32bits(v.Z()))
		off += 12
	}
	binary.LittleEndian.PutUint16(buf[off:], uint16(nf))
	off += 2
	for _, f := range hull.Faces {
		binary.LittleEndian.PutUint16(buf[off:], uint16(f.A))
		binary.LittleEndian.PutUint16(buf[off+2:], uint16(f.B))
		binary.LittleEndian.PutUint16(buf[off+4:], uint16(f.C))
		off += 6
	}
	return buf
}

// DeserializeHull decodes a binary blob into a ConvexHullCollider.
// Face normals are recomputed from vertex positions.
func DeserializeHull(data []byte) (*ConvexHullCollider, error) {
	if len(data) < 3 {
		return nil, fmt.Errorf("hull data too short")
	}
	if data[0] != hullFormatVersion {
		return nil, fmt.Errorf("unknown hull format version %d", data[0])
	}

	nv := int(binary.LittleEndian.Uint16(data[1:]))
	off := 3
	if len(data) < off+nv*12+2 {
		return nil, fmt.Errorf("hull data truncated (vertices)")
	}

	verts := make([]mgl32.Vec3, nv)
	for i := 0; i < nv; i++ {
		x := math.Float32frombits(binary.LittleEndian.Uint32(data[off:]))
		y := math.Float32frombits(binary.LittleEndian.Uint32(data[off+4:]))
		z := math.Float32frombits(binary.LittleEndian.Uint32(data[off+8:]))
		verts[i] = mgl32.Vec3{x, y, z}
		off += 12
	}

	nf := int(binary.LittleEndian.Uint16(data[off:]))
	off += 2
	if len(data) < off+nf*6 {
		return nil, fmt.Errorf("hull data truncated (faces)")
	}

	faces := make([]HullFace, nf)
	for i := 0; i < nf; i++ {
		a := int(binary.LittleEndian.Uint16(data[off:]))
		b := int(binary.LittleEndian.Uint16(data[off+2:]))
		c := int(binary.LittleEndian.Uint16(data[off+4:]))
		off += 6

		// Recompute normal from vertices.
		ab := verts[b].Sub(verts[a])
		ac := verts[c].Sub(verts[a])
		n := ab.Cross(ac)
		l := n.Len()
		if l > 1e-10 {
			n = n.Mul(1.0 / l)
		}
		faces[i] = HullFace{A: a, B: b, C: c, Normal: n}
	}

	return &ConvexHullCollider{Vertices: verts, Faces: faces}, nil
}

// ---------------------------------------------------------------------------
// GJK overlap test — convex hull vs AABB
// ---------------------------------------------------------------------------

// GJKOverlapAABB returns true if the convex hull (in local space, transformed by t)
// overlaps the given world-space AABB.
func GJKOverlapAABB(hull *ConvexHullCollider, t *Transform, box AABB) bool {
	// Support function for the Minkowski difference: support(A, d) - support(B, -d).
	supportA := func(dir mgl32.Vec3) mgl32.Vec3 {
		// Transform direction to hull local space.
		localDir := hullWorldToLocal(t, dir, false)
		best := hull.Vertices[0]
		bestDot := best.Dot(localDir)
		for i := 1; i < len(hull.Vertices); i++ {
			d := hull.Vertices[i].Dot(localDir)
			if d > bestDot {
				bestDot = d
				best = hull.Vertices[i]
			}
		}
		return hullLocalToWorld(t, best)
	}

	supportB := func(dir mgl32.Vec3) mgl32.Vec3 {
		// AABB support: pick min or max on each axis based on direction sign.
		return mgl32.Vec3{
			aabbAxisSupport(dir.X(), box.Min.X(), box.Max.X()),
			aabbAxisSupport(dir.Y(), box.Min.Y(), box.Max.Y()),
			aabbAxisSupport(dir.Z(), box.Min.Z(), box.Max.Z()),
		}
	}

	support := func(dir mgl32.Vec3) mgl32.Vec3 {
		return supportA(dir).Sub(supportB(dir.Mul(-1)))
	}

	// Initial direction: center of A to center of B.
	hullCenter := hullLocalToWorld(t, hullCentroid(hull))
	boxCenter := box.Min.Add(box.Max).Mul(0.5)
	dir := boxCenter.Sub(hullCenter)
	if dir.LenSqr() < 1e-10 {
		dir = mgl32.Vec3{1, 0, 0}
	}

	// Simplex evolution.
	simplex := [4]mgl32.Vec3{}
	simplexSize := 0

	a := support(dir)
	if a.Dot(dir) < 0 {
		return false
	}
	simplex[0] = a
	simplexSize = 1
	dir = a.Mul(-1)

	for iter := 0; iter < 64; iter++ {
		a = support(dir)
		if a.Dot(dir) < 0 {
			return false
		}

		simplexSize++
		simplex[simplexSize-1] = a

		switch simplexSize {
		case 2:
			// Line: simplex[0]=B, simplex[1]=A
			ab := simplex[0].Sub(simplex[1])
			ao := simplex[1].Mul(-1)
			if ab.Dot(ao) > 0 {
				dir = tripleProduct3(ab, ao, ab)
				if dir.LenSqr() < 1e-10 {
					return true // Origin on line.
				}
			} else {
				simplex[0] = simplex[1]
				simplexSize = 1
				dir = ao
			}

		case 3:
			// Triangle: simplex[0]=C, simplex[1]=B, simplex[2]=A
			a := simplex[2]
			b := simplex[1]
			c := simplex[0]
			ab := b.Sub(a)
			ac := c.Sub(a)
			ao := a.Mul(-1)
			abc := ab.Cross(ac)

			if abc.Cross(ac).Dot(ao) > 0 {
				if ac.Dot(ao) > 0 {
					simplex[0] = c
					simplex[1] = a
					simplexSize = 2
					dir = tripleProduct3(ac, ao, ac)
				} else {
					simplex[0] = b
					simplex[1] = a
					simplexSize = 2
					dir = tripleProduct3(ab, ao, ab)
				}
			} else if ab.Cross(abc).Dot(ao) > 0 {
				simplex[0] = b
				simplex[1] = a
				simplexSize = 2
				dir = tripleProduct3(ab, ao, ab)
			} else {
				if abc.Dot(ao) > 0 {
					dir = abc
				} else {
					simplex[0], simplex[1] = simplex[1], simplex[0]
					dir = abc.Mul(-1)
				}
			}

		case 4:
			// Tetrahedron: simplex[0]=D, simplex[1]=C, simplex[2]=B, simplex[3]=A
			a := simplex[3]
			b := simplex[2]
			c := simplex[1]
			d := simplex[0]
			ab := b.Sub(a)
			ac := c.Sub(a)
			ad := d.Sub(a)
			ao := a.Mul(-1)

			abc := ab.Cross(ac)
			acd := ac.Cross(ad)
			adb := ad.Cross(ab)

			if abc.Dot(ao) > 0 {
				simplex[0] = c
				simplex[1] = b
				simplex[2] = a
				simplexSize = 3
				dir = abc
			} else if acd.Dot(ao) > 0 {
				simplex[0] = d
				simplex[1] = c
				simplex[2] = a
				simplexSize = 3
				dir = acd
			} else if adb.Dot(ao) > 0 {
				simplex[0] = b
				simplex[1] = d
				simplex[2] = a
				simplexSize = 3
				dir = adb
			} else {
				return true // Origin enclosed.
			}
		}

		if dir.LenSqr() < 1e-10 {
			return true
		}
	}
	return false // Max iterations — assume no overlap.
}

func aabbAxisSupport(dirComponent, lo, hi float32) float32 {
	if dirComponent >= 0 {
		return hi
	}
	return lo
}

func hullCentroid(hull *ConvexHullCollider) mgl32.Vec3 {
	var cx, cy, cz float32
	for _, v := range hull.Vertices {
		cx += v.X()
		cy += v.Y()
		cz += v.Z()
	}
	n := float32(len(hull.Vertices))
	return mgl32.Vec3{cx / n, cy / n, cz / n}
}

func tripleProduct3(a, b, c mgl32.Vec3) mgl32.Vec3 {
	// (A × B) × C
	return a.Cross(b).Cross(c)
}

// ---------------------------------------------------------------------------
// Transform helpers — hull local space ↔ world space
// ---------------------------------------------------------------------------

// hullLocalToWorld transforms a point from hull local space to world space.
// Applies uniform scale, Y rotation, then translation (matching Transform.ModelMatrix).
func hullLocalToWorld(t *Transform, local mgl32.Vec3) mgl32.Vec3 {
	s := t.Scale.X() // uniform scale
	scaled := local.Mul(s)
	sinY := float32(math.Sin(float64(t.Rotation.Y())))
	cosY := float32(math.Cos(float64(t.Rotation.Y())))
	return mgl32.Vec3{
		scaled.X()*cosY + scaled.Z()*sinY + t.Position.X(),
		scaled.Y() + t.Position.Y(),
		-scaled.X()*sinY + scaled.Z()*cosY + t.Position.Z(),
	}
}

// hullWorldToLocal transforms a direction or point from world space to hull local space.
// If isPoint is true, subtracts translation first. If false, treats as direction (no translate).
func hullWorldToLocal(t *Transform, world mgl32.Vec3, isPoint bool) mgl32.Vec3 {
	v := world
	if isPoint {
		v = v.Sub(t.Position)
	}
	// Inverse Y rotation.
	sinY := float32(math.Sin(float64(-t.Rotation.Y())))
	cosY := float32(math.Cos(float64(-t.Rotation.Y())))
	rotated := mgl32.Vec3{
		v.X()*cosY + v.Z()*sinY,
		v.Y(),
		-v.X()*sinY + v.Z()*cosY,
	}
	// Inverse uniform scale.
	s := t.Scale.X()
	if s < 1e-6 {
		s = 1.0
	}
	if isPoint {
		return rotated.Mul(1.0 / s)
	}
	// For direction vectors, don't need to inverse-scale (used for dot product direction).
	return rotated
}

// ---------------------------------------------------------------------------
// Ray-hull intersection via Moller-Trumbore
// ---------------------------------------------------------------------------

// RaycastHull tests a ray against a convex hull. The ray and hull are in world space.
// Transforms the ray to hull local space for efficiency, then tests each face triangle.
// Returns the nearest hit distance, the world-space outward normal, and whether it hit.
func RaycastHull(hull *ConvexHullCollider, t *Transform, origin, dir mgl32.Vec3, maxDist float32) (float32, mgl32.Vec3, bool) {
	// Transform ray to hull local space.
	localOrigin := hullWorldToLocal(t, origin, true)
	localDir := hullWorldToLocal(t, dir, false)
	// Scale maxDist by inverse scale (ray travels shorter in local space if scaled up).
	s := t.Scale.X()
	if s < 1e-6 {
		s = 1.0
	}
	localMaxDist := maxDist / s

	bestT := float32(math.MaxFloat32)
	bestFace := -1

	for fi, face := range hull.Faces {
		v0 := hull.Vertices[face.A]
		v1 := hull.Vertices[face.B]
		v2 := hull.Vertices[face.C]

		hitT, hit := mollerTrumbore(localOrigin, localDir, v0, v1, v2)
		if hit && hitT >= 0 && hitT < localMaxDist && hitT < bestT {
			bestT = hitT
			bestFace = fi
		}
	}

	if bestFace < 0 {
		return 0, mgl32.Vec3{}, false
	}

	// Convert hit distance back to world scale.
	worldT := bestT * s

	// Transform normal to world space (rotate only, normals don't translate or scale for uniform scale).
	localNormal := hull.Faces[bestFace].Normal
	sinY := float32(math.Sin(float64(t.Rotation.Y())))
	cosY := float32(math.Cos(float64(t.Rotation.Y())))
	worldNormal := mgl32.Vec3{
		localNormal.X()*cosY + localNormal.Z()*sinY,
		localNormal.Y(),
		-localNormal.X()*sinY + localNormal.Z()*cosY,
	}

	return worldT, worldNormal, true
}

// mollerTrumbore performs the Moller-Trumbore ray-triangle intersection test.
// Returns the parametric distance t and whether the ray hits the triangle.
func mollerTrumbore(origin, dir, v0, v1, v2 mgl32.Vec3) (float32, bool) {
	const epsilon = 1e-7

	edge1 := v1.Sub(v0)
	edge2 := v2.Sub(v0)
	h := dir.Cross(edge2)
	a := edge1.Dot(h)

	if a > -epsilon && a < epsilon {
		return 0, false // Ray parallel to triangle.
	}

	f := 1.0 / a
	s := origin.Sub(v0)
	u := f * s.Dot(h)
	if u < 0.0 || u > 1.0 {
		return 0, false
	}

	q := s.Cross(edge1)
	v := f * dir.Dot(q)
	if v < 0.0 || u+v > 1.0 {
		return 0, false
	}

	t := f * edge2.Dot(q)
	return t, t > epsilon
}

// ---------------------------------------------------------------------------
// NavGrid hull footprint — 2D XZ overlap with grid cell
// ---------------------------------------------------------------------------

// HullFootprintOverlapsCell returns true if the XZ projection of a transformed
// hull overlaps the given grid cell rectangle.
func HullFootprintOverlapsCell(hull *ConvexHullCollider, t *Transform, cellMinX, cellMinZ, cellMaxX, cellMaxZ float32) bool {
	// Project hull vertices to XZ world coordinates.
	projVerts := make([][2]float32, len(hull.Vertices))
	for i, v := range hull.Vertices {
		w := hullLocalToWorld(t, v)
		projVerts[i] = [2]float32{w.X(), w.Z()}
	}

	// 2D SAT (Separating Axis Theorem) between convex polygon and AABB.
	// Test axes from rectangle edges (X and Z axes) and polygon edges.

	// Rectangle vertices.
	rect := [4][2]float32{
		{cellMinX, cellMinZ},
		{cellMaxX, cellMinZ},
		{cellMaxX, cellMaxZ},
		{cellMinX, cellMaxZ},
	}

	// Test rectangle axes (X and Z).
	axes := [][2]float32{{1, 0}, {0, 1}}
	// Add polygon edge normals.
	for i := 0; i < len(projVerts); i++ {
		j := (i + 1) % len(projVerts)
		edge := [2]float32{
			projVerts[j][0] - projVerts[i][0],
			projVerts[j][1] - projVerts[i][1],
		}
		// Perpendicular.
		axes = append(axes, [2]float32{-edge[1], edge[0]})
	}

	for _, axis := range axes {
		l := float32(math.Sqrt(float64(axis[0]*axis[0] + axis[1]*axis[1])))
		if l < 1e-8 {
			continue
		}
		ax := [2]float32{axis[0] / l, axis[1] / l}

		// Project polygon.
		pMin, pMax := project2D(projVerts, ax)
		// Project rectangle.
		rMin, rMax := project2D(rect[:], ax)

		if pMax < rMin || rMax < pMin {
			return false // Separating axis found.
		}
	}
	return true // No separating axis — overlap.
}

func project2D(verts [][2]float32, axis [2]float32) (float32, float32) {
	min := verts[0][0]*axis[0] + verts[0][1]*axis[1]
	max := min
	for i := 1; i < len(verts); i++ {
		d := verts[i][0]*axis[0] + verts[i][1]*axis[1]
		if d < min {
			min = d
		}
		if d > max {
			max = d
		}
	}
	return min, max
}

// ---------------------------------------------------------------------------
// Hull bounding box (for auto-computing tight AABB half-extents)
// ---------------------------------------------------------------------------

// HullBounds returns the axis-aligned bounding box of a hull in local space.
func HullBounds(hull *ConvexHullCollider) (min, max mgl32.Vec3) {
	if len(hull.Vertices) == 0 {
		return mgl32.Vec3{}, mgl32.Vec3{}
	}
	min = hull.Vertices[0]
	max = hull.Vertices[0]
	for _, v := range hull.Vertices[1:] {
		if v.X() < min.X() {
			min[0] = v.X()
		}
		if v.Y() < min.Y() {
			min[1] = v.Y()
		}
		if v.Z() < min.Z() {
			min[2] = v.Z()
		}
		if v.X() > max.X() {
			max[0] = v.X()
		}
		if v.Y() > max.Y() {
			max[1] = v.Y()
		}
		if v.Z() > max.Z() {
			max[2] = v.Z()
		}
	}
	return
}
