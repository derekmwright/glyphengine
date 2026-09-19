package glyphengine

import (
	"testing"

	"github.com/go-gl/mathgl/mgl32"
)

// TestWorldAABBOfAMirroredEntity: a negative scale is a mirror, and the box
// around a mirrored box is the same box.
//
// It matters since TransformFromMatrix, which hands back a negative Scale.X for
// a mirrored matrix -- what a mirrored object in an editor exports as. Scaling
// the half extents by that sign put Min above Max on the axis, and an inverted
// AABB overlaps nothing: the collider was there and nothing could touch it.
//
// Verified to fail: without the absolute value, Min.X is 3 and Max.X is -1, and
// the overlap query below finds nothing.
func TestWorldAABBOfAMirroredEntity(t *testing.T) {
	tr := &Transform{Position: mgl32.Vec3{1, 0, 0}, Scale: mgl32.Vec3{-2, 1, 1}}
	c := &Collider{HalfExtents: mgl32.Vec3{1, 1, 1}}

	box := WorldAABB(tr, c)
	if box.Min != (mgl32.Vec3{-1, -1, -1}) || box.Max != (mgl32.Vec3{3, 1, 1}) {
		t.Errorf("mirrored box is %v..%v, want -1,-1,-1..3,1,1", box.Min, box.Max)
	}

	s := NewScene()
	e := s.Spawn()
	s.C.Transform.Set(e, tr)
	s.C.Collider.Set(e, c)
	probe := AABB{Min: mgl32.Vec3{2, -0.5, -0.5}, Max: mgl32.Vec3{2.5, 0.5, 0.5}}
	if got := s.OverlapAABB(probe, 0); len(got) != 1 || got[0].Entity != e {
		t.Errorf("a probe inside the mirrored collider overlaps %v, want entity %d", got, e)
	}
}
