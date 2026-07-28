package glyphengine

import (
	"math"

	"github.com/go-gl/mathgl/mgl32"

	"github.com/derekmwright/glyphengine/ecs"
)

// Collider is an ECS component that defines an axis-aligned bounding volume
// in local space via half-extents (half the width on each axis).
type Collider struct {
	HalfExtents mgl32.Vec3
}

// AABB is a world-space axis-aligned bounding box.
type AABB struct {
	Min, Max mgl32.Vec3
}

// RayHit describes the result of a raycast against the world.
type RayHit struct {
	Entity ecs.Entity
	T      float32    // distance along ray
	Point  mgl32.Vec3 // world-space hit point
	Normal mgl32.Vec3 // outward face normal at hit
}

// OverlapResult describes an entity whose collider overlaps a query AABB.
type OverlapResult struct {
	Entity ecs.Entity
	Box    AABB
}

// WorldAABB computes the world-space AABB for an entity given its transform
// and collider. Rotation is ignored (axis-aligned assumption).
func WorldAABB(t *Transform, c *Collider) AABB {
	scaled := mgl32.Vec3{
		c.HalfExtents.X() * t.Scale.X(),
		c.HalfExtents.Y() * t.Scale.Y(),
		c.HalfExtents.Z() * t.Scale.Z(),
	}
	return AABB{
		Min: t.Position.Sub(scaled),
		Max: t.Position.Add(scaled),
	}
}

// Overlaps returns true if two AABBs overlap (touching counts as overlap).
func (a AABB) Overlaps(other AABB) bool {
	return a.Min.X() < other.Max.X() && a.Max.X() > other.Min.X() &&
		a.Min.Y() < other.Max.Y() && a.Max.Y() > other.Min.Y() &&
		a.Min.Z() < other.Max.Z() && a.Max.Z() > other.Min.Z()
}

// Raycast tests a ray against the AABB using the slab method.
// Returns the hit distance t, outward normal at the entry face, and whether
// the ray hit. A ray originating inside the box is not considered a hit.
func (a AABB) Raycast(origin, dir mgl32.Vec3, maxDist float32) (t float32, normal mgl32.Vec3, hit bool) {
	tMin := float32(-math.MaxFloat32)
	tMax := float32(math.MaxFloat32)

	normals := [3]mgl32.Vec3{
		{1, 0, 0},
		{0, 1, 0},
		{0, 0, 1},
	}
	var entryNormal mgl32.Vec3

	for i := 0; i < 3; i++ {
		o := origin[i]
		d := dir[i]
		lo := a.Min[i]
		hi := a.Max[i]

		if abs32(d) < 1e-8 {
			// Ray is parallel to slab — miss if origin outside slab.
			if o < lo || o > hi {
				return 0, mgl32.Vec3{}, false
			}
			continue
		}

		t1 := (lo - o) / d
		t2 := (hi - o) / d

		nDir := float32(-1) // normal points toward -axis if entering from low side
		if t1 > t2 {
			t1, t2 = t2, t1
			nDir = 1
		}

		if t1 > tMin {
			tMin = t1
			entryNormal = normals[i].Mul(nDir)
		}
		if t2 < tMax {
			tMax = t2
		}

		if tMin > tMax {
			return 0, mgl32.Vec3{}, false
		}
	}

	if tMin < 0 || tMin > maxDist {
		return 0, mgl32.Vec3{}, false
	}

	return tMin, entryNormal, true
}

// buildCollisionSnapshot freezes every collider entity's world-space AABB for
// the duration of the parallel movement phase. Called sequentially before the
// goroutines spawn; the map is reused across ticks to avoid reallocation.
func (s *Scene) buildCollisionSnapshot() {
	if s.collisionAABBs == nil {
		s.collisionAABBs = make(map[ecs.Entity]AABB, 512)
	} else {
		clear(s.collisionAABBs)
	}
	ecs.Query2(s.C.Transform, s.C.Collider, func(e ecs.Entity, t *Transform, c *Collider) {
		s.collisionAABBs[e] = WorldAABB(t, c)
	})
	s.useCollisionSnapshot = true
}

// clearCollisionSnapshot returns collision queries to reading live Transforms.
// The backing map is retained for reuse next tick.
func (s *Scene) clearCollisionSnapshot() {
	s.useCollisionSnapshot = false
}

// colliderAABB returns the entity's world AABB, reading from the frozen
// snapshot during the parallel movement phase and from live components
// otherwise. ok is false if the entity has no collider geometry.
func (s *Scene) colliderAABB(entity ecs.Entity) (wb AABB, ok bool) {
	if s.useCollisionSnapshot {
		wb, ok = s.collisionAABBs[entity]
		return wb, ok
	}
	t, tok := s.C.Transform.Get(entity)
	c, cok := s.C.Collider.Get(entity)
	if !tok || !cok {
		return AABB{}, false
	}
	return WorldAABB(t, c), true
}

// OverlapAABB queries the world for all collider entities whose AABB overlaps
// the given box. The exclude entity (if non-zero) is skipped.
func (s *Scene) OverlapAABB(box AABB, exclude ecs.Entity) []OverlapResult {
	var results []OverlapResult

	testEntity := func(entity ecs.Entity) {
		if entity == exclude {
			return
		}
		wb, ok := s.colliderAABB(entity)
		if !ok {
			return
		}
		if box.Overlaps(wb) {
			results = append(results, OverlapResult{Entity: entity, Box: wb})
		}
	}

	// Use spatial grids for nearby entities + scene objects.
	// QueryRadiusAlloc is used (instead of QueryRadius) so this method is
	// safe to call concurrently from parallel player movement goroutines.
	center := box.Min.Add(box.Max).Mul(0.5)
	radius := box.Max.Sub(box.Min).Len()*0.5 + 5 // box half-diagonal + padding
	if s.SpatialGrid != nil {
		for _, entity := range s.SpatialGrid.QueryRadiusAlloc(center.X(), center.Z(), radius) {
			testEntity(entity)
		}
		if s.StaticGrid != nil {
			for _, entity := range s.StaticGrid.QueryRadiusAlloc(center.X(), center.Z(), radius) {
				testEntity(entity)
			}
		}
	} else {
		ecs.Query2(s.C.Transform, s.C.Collider, func(entity ecs.Entity, _ *Transform, _ *Collider) {
			testEntity(entity)
		})
	}

	return results
}

// Unstick nudges an entity out of any overlapping colliders on the XZ plane.
// Call after spawning or teleporting to prevent getting stuck inside obstacles.
func (s *Scene) Unstick(entity ecs.Entity) {
	t, ok := s.C.Transform.Get(entity)
	if !ok {
		return
	}
	col, ok := s.C.Collider.Get(entity)
	if !ok {
		return
	}

	for attempt := 0; attempt < 8; attempt++ {
		box := WorldAABB(t, col)
		overlaps := s.OverlapAABB(box, entity)
		if len(overlaps) == 0 {
			return
		}
		// Find the smallest XZ push to resolve the first overlap.
		other := overlaps[0].Box
		overlapXPos := box.Max.X() - other.Min.X()
		overlapXNeg := other.Max.X() - box.Min.X()
		overlapZPos := box.Max.Z() - other.Min.Z()
		overlapZNeg := other.Max.Z() - box.Min.Z()

		minPush := overlapXPos
		pushAxis, pushDir := 0, float32(-1) // -X
		if overlapXNeg < minPush {
			minPush = overlapXNeg
			pushAxis, pushDir = 0, 1 // +X
		}
		if overlapZPos < minPush {
			minPush = overlapZPos
			pushAxis, pushDir = 2, -1 // -Z
		}
		if overlapZNeg < minPush {
			minPush = overlapZNeg
			pushAxis, pushDir = 2, 1 // +Z
		}

		t.Position[pushAxis] += pushDir * (minPush + 0.01)
	}
}

// Raycast finds the nearest collider entity hit by a ray.
// The exclude entity (if non-zero) is skipped.
// Also tests the terrain heightmap for downward rays (checked first).
// Uses the SpatialGrid when available to reduce candidate entities from O(all) to O(nearby).
func (s *Scene) Raycast(origin, dir mgl32.Vec3, maxDist float32, exclude ecs.Entity) (RayHit, bool) {
	var best RayHit
	found := false

	// Check terrain heightmap FIRST for downward rays — most common hit.
	if s.Terrain != nil && dir.Y() < -0.99 {
		if dist, hitY, ok := s.Terrain.HeightAtRayDown(origin.X(), origin.Y(), origin.Z(), maxDist); ok {
			n := s.Terrain.NormalAt(origin.X(), origin.Z())
			best = RayHit{
				T:      dist,
				Point:  mgl32.Vec3{origin.X(), hitY, origin.Z()},
				Normal: mgl32.Vec3{n[0], n[1], n[2]},
			}
			found = true
		}
	}

	// testEntity performs AABB broad-phase + optional convex hull narrow-phase raycast.
	testEntity := func(entity ecs.Entity) {
		if entity == exclude {
			return
		}
		wb, ok := s.colliderAABB(entity)
		if !ok {
			return
		}
		dist, normal, hit := wb.Raycast(origin, dir, maxDist)
		if !hit {
			return
		}
		// Narrow-phase: use convex hull raycast if available. Hull entities are
		// static scene objects (not moved during the parallel phase), so reading
		// their live Transform here is safe even under the collision snapshot.
		if hc, hasHull := s.C.ConvexHullCollider.Get(entity); hasHull {
			t, tok := s.C.Transform.Get(entity)
			if !tok {
				return
			}
			hDist, hNormal, hOk := RaycastHull(hc, t, origin, dir, maxDist)
			if !hOk {
				return // AABB hit but hull miss.
			}
			dist, normal = hDist, hNormal
		}
		if !found || dist < best.T {
			best = RayHit{
				Entity: entity,
				T:      dist,
				Point: mgl32.Vec3{
					origin.X() + dir.X()*dist,
					origin.Y() + dir.Y()*dist,
					origin.Z() + dir.Z()*dist,
				},
				Normal: normal,
			}
			found = true
		}
	}

	// Test entity colliders — use spatial grids for O(nearby) instead of O(all).
	// QueryRadiusAlloc is used for concurrent safety (parallel player movement).
	if s.SpatialGrid != nil {
		for _, entity := range s.SpatialGrid.QueryRadiusAlloc(origin.X(), origin.Z(), maxDist) {
			testEntity(entity)
		}
		if s.StaticGrid != nil {
			for _, entity := range s.StaticGrid.QueryRadiusAlloc(origin.X(), origin.Z(), maxDist) {
				testEntity(entity)
			}
		}
	} else {
		ecs.Query2(s.C.Transform, s.C.Collider, func(entity ecs.Entity, _ *Transform, _ *Collider) {
			testEntity(entity)
		})
	}

	return best, found
}

func abs32(x float32) float32 {
	if x < 0 {
		return -x
	}
	return x
}
