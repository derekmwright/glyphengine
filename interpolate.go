package glyphengine

import (
	"math"

	"github.com/go-gl/mathgl/mgl32"

	"github.com/derekmwright/glyphengine/ecs"
)

// LerpAngle interpolates between two angles in radians along the shortest arc.
//
// Plain linear interpolation is wrong for angles: a character turning from
// 179° to -179° has moved 2°, but lerping the raw numbers sweeps it 358° the
// other way. At 144Hz that is a visible spin every time facing crosses the
// wrap point.
func LerpAngle(a, b, alpha float32) float32 {
	const twoPi = 2 * math.Pi
	d := float32(math.Mod(float64(b-a)+math.Pi, twoPi))
	if d < 0 {
		d += twoPi
	}
	d -= math.Pi
	return a + d*alpha
}

// LerpTransform blends two transforms by alpha in [0,1]. Position and scale
// interpolate linearly; each Euler rotation component takes the shortest arc.
func LerpTransform(a, b Transform, alpha float32) Transform {
	return Transform{
		Position: a.Position.Add(b.Position.Sub(a.Position).Mul(alpha)),
		Rotation: mgl32.Vec3{
			LerpAngle(a.Rotation[0], b.Rotation[0], alpha),
			LerpAngle(a.Rotation[1], b.Rotation[1], alpha),
			LerpAngle(a.Rotation[2], b.Rotation[2], alpha),
		},
		Scale: a.Scale.Add(b.Scale.Sub(a.Scale).Mul(alpha)),
	}
}

// snapshotTransforms copies every non-Static entity's Transform into
// PrevTransform. Called at the top of Scene.Tick, before anything mutates
// transforms, so rendering can blend from where things were to where they are.
//
// Static entities are skipped: they never move, so a previous transform would
// be a per-tick copy of an unchanging value. In a scene that is mostly world
// geometry, that exclusion is most of the cost.
func (s *Scene) snapshotTransforms() {
	s.C.Transform.Each(func(e ecs.Entity, t *Transform) {
		if s.C.Static.Has(e) {
			return
		}
		// Copy the value. ecs.Store keeps the pointer it is handed, so storing
		// &Transform here would alias the live component — "previous" and
		// "current" would be the same memory and interpolation would silently
		// do nothing.
		//
		// Overwrite in place when a slot already exists, so steady state costs
		// no allocation; only an entity's first tick allocates.
		if prev, ok := s.C.PrevTransform.Get(e); ok {
			*prev = PrevTransform(*t)
			return
		}
		prev := PrevTransform(*t)
		s.C.PrevTransform.Set(e, &prev)
	})
}

// ClearInterpolation makes an entity render at its current transform on the
// next frame instead of blending from its previous one.
//
// Call it after teleporting: a warp, a respawn, a scene load. Without it the
// entity is drawn sliding from where it used to be to where it now is, which
// across a level is a smear all the way over the map.
//
// RestoreCharacter already does this, so prediction corrections do not smear.
func (s *Scene) ClearInterpolation(entity ecs.Entity) {
	s.C.PrevTransform.Remove(entity)
}

// InterpolatedTransform returns where an entity should be drawn this frame:
// its previous tick transform blended toward its current one by alpha.
//
// It falls back to the current transform when interpolation is off, when the
// entity has no previous transform yet (just spawned, or just teleported), or
// when the entity is Static.
func (s *Scene) InterpolatedTransform(entity ecs.Entity, alpha float32) (Transform, bool) {
	t, ok := s.C.Transform.Get(entity)
	if !ok {
		return Transform{}, false
	}
	if !s.Interpolate {
		return *t, true
	}
	prev, ok := s.C.PrevTransform.Get(entity)
	if !ok {
		return *t, true
	}
	return LerpTransform(Transform(*prev), *t, alpha), true
}
