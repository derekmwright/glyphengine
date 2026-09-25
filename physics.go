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
	// The magnitude of the scale, not the scale: a negative one is a mirror,
	// and the box around a mirrored box is the same box. Left signed it put Min
	// above Max on that axis, and an inverted AABB overlaps nothing -- the
	// collider was there and nothing could touch it. Negative scales arrive from
	// TransformFromMatrix, for anything an editor mirrored.
	scaled := mgl32.Vec3{
		c.HalfExtents.X() * abs32(t.Scale.X()),
		c.HalfExtents.Y() * abs32(t.Scale.Y()),
		c.HalfExtents.Z() * abs32(t.Scale.Z()),
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
//
// It freezes through the same code FrozenQueries hands a game, so the engine's
// parallel phase and a game's own see the same set of colliders and the same
// ordering rules rather than two copies of them.
func (s *Scene) buildCollisionSnapshot() {
	s.collisionAABBs = s.freezeQueries(s.collisionAABBs).aabb
	s.useCollisionSnapshot = true
}

// clearCollisionSnapshot returns collision queries to reading live Transforms.
// The backing map is retained for reuse next tick.
func (s *Scene) clearCollisionSnapshot() {
	s.useCollisionSnapshot = false
}

// phaseAABBs returns the frozen AABBs the built-in queries must answer from
// while the parallel movement phase is running, and nil — meaning "read the
// live components" — at every other time.
func (s *Scene) phaseAABBs() map[ecs.Entity]AABB {
	if s.useCollisionSnapshot {
		return s.collisionAABBs
	}
	return nil
}

// colliderAABB returns the entity's world AABB, reading from the frozen
// snapshot during the parallel movement phase and from live components
// otherwise. ok is false if the entity has no collider geometry.
func (s *Scene) colliderAABB(entity ecs.Entity) (wb AABB, ok bool) {
	return s.colliderAABBFrom(s.phaseAABBs(), entity)
}

// colliderAABBFrom returns the entity's world AABB out of snapshot, or from
// live components when snapshot is nil. ok is false if the entity has no
// collider geometry — which, against a snapshot, also covers an entity that
// gained its collider after the freeze.
func (s *Scene) colliderAABBFrom(snapshot map[ecs.Entity]AABB, entity ecs.Entity) (wb AABB, ok bool) {
	if snapshot != nil {
		wb, ok = snapshot[entity]
		return wb, ok
	}
	t, tok := s.C.Transform.Get(entity)
	c, cok := s.C.Collider.Get(entity)
	if !tok || !cok {
		return AABB{}, false
	}
	return WorldAABB(t, c), true
}

// eachBroadPhaseCandidate offers every entity the broad phase has for a query
// centred on (x, z) — each entity exactly once — and falls back to a linear
// scan of every collider when there is no SpatialGrid. Both built-in queries
// walk the world through it. It reads the grid cells directly, so it needs
// neither a candidate copy nor shared query scratch and is safe to call from
// the parallel movement phase's goroutines.
//
// Once is the whole reason this is a function. SpatialGrid holds every entity
// with a Transform and StaticGrid holds the Static ones, so a Static collider
// is in both grids, and both walks used to hand it over: OverlapAABB returned
// it twice, adjacent, and Raycast paid for a second AABB test and a second
// convex-hull narrow phase to reach the answer its tie-break already had
// (#141).
//
// The duplicate is dropped on the moving grid's side. That is the cheaper
// direction and the safe one:
//
//   - Cheaper, because "will the static walk produce this?" is a map lookup
//     against what RebuildStatics recorded (staticWalkProduces), paid once per
//     candidate, and it replaces a whole second candidate test — an AABB
//     rebuild and an overlap or slab test, plus a hull raycast for a static
//     that has one. The other direction has no equally exact test to make:
//     SpatialGrid is filled wholesale by Update, not entity by entity, so
//     "did the moving walk already produce this?" would have to be a set of
//     entity ids built per query, which allocates on every query — and for
//     Raycast, which keeps no result list to check against, it would be a set
//     and nothing else.
//   - Safe, because staticWalkProduces answers from what RebuildStatics
//     actually recorded rather than from the Static tag. A Static entity the
//     static grid does not have — one spawned since the last RebuildStatics —
//     is not skipped, so the moving walk still offers it and it stays in the
//     answer (TestOverlapAABBKeepsAStaticSpawnedSinceRebuildStatics); at worst
//     something that fills StaticGrid behind RebuildStatics' back produces the
//     old duplicate again, which is what every query used to pay anyway.
//     Dropping the static walk's copy instead would have to assume the moving
//     grid had the entity, and a SpatialGrid not updated since that entity
//     spawned would turn the assumption into a collider that silently is not
//     there.
//
// Measured on an AMD Ryzen 9 5900X (24 GOMAXPROCS) against the fixture both
// query benchmarks use — 64 static colliders, in both grids — by deleting the
// skip and running A/B/A, three runs each:
//
//	go test -run '^$' -bench 'RaycastGrid|OverlapGridMiss' -benchmem -count 3 .
//
//	                          duplicated     deduped
//	BenchmarkRaycastGrid       ~7.3 µs/op    ~4.5 µs/op
//	BenchmarkOverlapGridMiss   ~7.0 µs/op    ~4.3 µs/op
//
// Zero allocations per query either way, which is the part that had to hold:
// the skip is a map lookup and a cell-bounds comparison, not a per-query set
// (TestGridQueriesDoNotAllocateCandidates). Every collider in that fixture is
// static, so every candidate it has is a duplicate — the ~38% is the shape of
// the best case, not of a scene that is mostly moving entities.
func (s *Scene) eachBroadPhaseCandidate(x, z, radius float32, visit func(ecs.Entity)) {
	if s.SpatialGrid == nil {
		ecs.Query2(s.C.Transform, s.C.Collider, func(entity ecs.Entity, _ *Transform, _ *Collider) {
			visit(entity)
		})
		return
	}
	if s.StaticGrid == nil {
		s.SpatialGrid.eachInRadius(x, z, radius, visit)
		return
	}
	s.SpatialGrid.eachInRadius(x, z, radius, func(entity ecs.Entity) {
		if s.staticWalkProduces(entity, x, z, radius) {
			return // the static walk below hands this one over.
		}
		visit(entity)
	})
	s.StaticGrid.eachInRadius(x, z, radius, visit)
}

// QueryBackend answers the two collision queries every internal engine system
// runs against the world: a nearest-hit raycast and a box-overlap query.
// Scene.Queries is nil by default, which keeps this file's spatial-grid
// implementation; setting it replaces BOTH Scene.Raycast and Scene.OverlapAABB
// in one step, because every engine call site goes through those two methods
// and nothing else — IntegrateBodies, MoveCharacter, Unstick,
// Camera.ResolveCollision (via the Raycaster embedded below), and
// Engine.PickEntity. A game backing collision queries with its own broadphase
// — a BVH, say — implements this once instead of leaving OverlapAABB stuck on
// the built-in grid while only Raycast is swapped.
//
// A replacement must honour the order contracts #57 gave the built-in
// implementation, because engine code is written against them:
//
//   - OverlapAABB's results are ordered by ascending entity id, and hold each
//     entity at most once (#141).
//   - Raycast breaks an exact-distance tie on the lower entity id; a terrain
//     hit (Entity == 0, since no real entity ever has id 0) always wins a tie
//     against any real entity.
//
// Both are about reproducibility, not the specific ordering: the same query
// against the same frozen state must return the same answer on every call,
// not whichever candidate a map walk or a goroutine happened to reach first.
// Measured, not assumed: scrambling OverlapAABB's result order does not
// currently break Unstick or MoveCharacter — Unstick scans the whole result
// list itself and picks the smallest-push entry with its own tie-break
// (below), and MoveCharacter's "does anything block this move" check is an OR
// over the list, so neither reads a positional index. See
// TestUnstickToleratesScrambledOverlapOrder and
// TestMoveCharacterToleratesScrambledOverlapOrder in query_backend_test.go.
// The contract is required of a replacement anyway, because OverlapAABB
// documents it to ITS OWN callers (physics-queries.md) and a game may depend
// on it exactly the way Unstick used to.
//
// The collision snapshot is the other half of the contract, and deliberately
// is not a method here — there is no lifecycle hook to freeze or unfreeze a
// replacement, because Raycast and OverlapAABB are the whole of what this
// seam is for. During Scene.MoveCharactersParallel, both are called from
// multiple goroutines while other goroutines concurrently write Transform and
// Velocity through MoveCharacter. The built-in implementation is exposed to
// that race only because it reads those same live components, and it
// survives by freezing a copy of every collider's AABB before the phase
// starts (colliderAABB, buildCollisionSnapshot) and answering from the copy
// for the phase's duration. A replacement backed by its own index — rebuilt
// once per tick, which is the normal shape of a broadphase — satisfies the
// same requirement for free, because nothing in its index changes mid-phase
// in the first place. A replacement that instead queries Scene's live
// Transform/Velocity itself needs the same freeze the built-in one has, and
// Scene.FrozenQueries is that freeze, exported: it hands back this same
// implementation bound to a snapshot taken on the spot, for a game whose own
// movement integrator runs a parallel phase of its own (#133).
//
// Concurrency: Raycast and OverlapAABB are called from multiple goroutines
// during the parallel phase, so neither may keep scratch state shared across
// calls. The built-in implementation visits the frozen grid cells directly;
// SpatialGrid.QueryRadius reuses a buffer and is NOT safe for this. Each call
// must be independent or internally synchronized.
//
// A replacement must never call Scene.Raycast or Scene.OverlapAABB on the scene
// it is installed in: with Queries set those ARE the replacement, and the call
// recurses until the stack runs out. The built-in implementation is reached
// through Scene.BuiltinQueries instead.
type QueryBackend interface {
	Raycaster
	OverlapAABB(box AABB, exclude ecs.Entity) []OverlapResult
}

// BuiltinQueries returns the spatial-grid implementation as a QueryBackend,
// whatever Scene.Queries is set to. It is to Queries what the exported
// IntegrateBodies is to Integrator: a replacement that only wants to change
// part of the answer -- its own BVH for the static buildings and the engine's
// grid for everything that moves, a filter, a counter -- delegates the rest
// here rather than reimplementing the terrain fast path and the hull narrow
// phase, and it cannot delegate to Scene.Raycast, which is itself.
//
// It inherits the collision snapshot, so a backend built on it is safe in
// MoveCharactersParallel without a freeze of its own.
func (s *Scene) BuiltinQueries() QueryBackend {
	return builtinQueries{s}
}

type builtinQueries struct{ s *Scene }

func (b builtinQueries) Raycast(origin, dir mgl32.Vec3, maxDist float32, exclude ecs.Entity) (RayHit, bool) {
	return b.s.raycastBuiltin(origin, dir, maxDist, exclude)
}

func (b builtinQueries) OverlapAABB(box AABB, exclude ecs.Entity) []OverlapResult {
	return b.s.overlapAABBBuiltin(box, exclude)
}

// FrozenQueries returns a QueryBackend that answers Raycast and OverlapAABB
// from a snapshot of every collider's current world AABB, taken now. Safe to
// call from many goroutines while Transforms are being written, which is how
// MoveCharactersParallel uses it: a game running its own movement integrator
// in a parallel phase freezes once on the calling goroutine, then queries the
// returned backend from all of them.
//
//	frozen := scene.FrozenQueries()
//	// ... goroutines calling frozen.Raycast and frozen.OverlapAABB while
//	// other goroutines write Transforms, under scene.World().EnableLocking()
//	// the way MoveCharactersParallel does ...
//
// The answers are BuiltinQueries' answers, ordering and tie-breaks included —
// it is that same implementation reading AABBs from the snapshot instead of
// from live components, which is exactly what MoveCharactersParallel does.
//
// The snapshot holds the world AABB of every entity that had both a Transform
// and a Collider when it was taken, moving and Static alike, so a character
// sees its neighbours where they stood before the phase began. It copies
// nothing else: the terrain heightmap, convex hulls and the Transforms the
// hull narrow phase reads, and the two spatial grids that supply broad-phase
// candidates are all still read live. That is sound for the same reason
// MoveCharactersParallel is — hull entities are Static, and terrain and the
// grids are rebuilt between phases rather than during one.
//
// A snapshot goes stale the moment anything writes a Transform or a Collider,
// or spawns or despawns a collider entity: those writes are invisible to it by
// design. Take a new one each tick — holding one across ticks answers with
// last tick's world.
func (s *Scene) FrozenQueries() QueryBackend {
	return s.freezeQueries(nil)
}

// freezeQueries captures every collider entity's world AABB and returns the
// backend that answers from it. reuse, when non-nil, is emptied and refilled
// rather than replaced: the parallel movement phase hands its own map back
// every tick so the freeze does not reallocate, while FrozenQueries passes nil
// so that a caller's snapshot is a map nothing else can write.
func (s *Scene) freezeQueries(reuse map[ecs.Entity]AABB) frozenQueries {
	snapshot := reuse
	if snapshot == nil {
		snapshot = make(map[ecs.Entity]AABB, 512)
	} else {
		clear(snapshot)
	}
	ecs.Query2(s.C.Transform, s.C.Collider, func(e ecs.Entity, t *Transform, c *Collider) {
		snapshot[e] = WorldAABB(t, c)
	})
	return frozenQueries{s: s, aabb: snapshot}
}

// frozenQueries is the built-in implementation bound to one snapshot instead
// of to whatever the scene is doing now. buildCollisionSnapshot needs the
// concrete type: it installs that map as the phase's, so Scene.Raycast,
// Scene.OverlapAABB and any backend delegating to BuiltinQueries all read the
// same frozen copy for the phase's duration.
type frozenQueries struct {
	s    *Scene
	aabb map[ecs.Entity]AABB
}

func (f frozenQueries) Raycast(origin, dir mgl32.Vec3, maxDist float32, exclude ecs.Entity) (RayHit, bool) {
	return f.s.raycastFrom(f.aabb, origin, dir, maxDist, exclude)
}

func (f frozenQueries) OverlapAABB(box AABB, exclude ecs.Entity) []OverlapResult {
	return f.s.overlapAABBFrom(f.aabb, box, exclude)
}

// OverlapAABB queries the world for all collider entities whose AABB overlaps
// the given box. The exclude entity (if non-zero) is skipped.
//
// Results are sorted by ascending entity id — that is part of the contract,
// not an incidental detail. Candidates arrive from the spatial grid, whose
// cell lists are filled by a Go map walk (SpatialGrid.Update), so without an
// explicit sort a caller reading results[0] would get a different entity on
// every call. Unstick used to do exactly that (#57).
//
// Strictly ascending: an entity is in the results at most once, even though a
// Static collider is in both spatial grids and the broad phase walks both
// (#141, eachBroadPhaseCandidate).
//
// When Scene.Queries is set, this and Raycast delegate to it instead of the
// spatial-grid implementation below — see QueryBackend for the contract a
// replacement has to honour.
func (s *Scene) OverlapAABB(box AABB, exclude ecs.Entity) []OverlapResult {
	if s.Queries != nil {
		return s.Queries.OverlapAABB(box, exclude)
	}
	return s.overlapAABBBuiltin(box, exclude)
}

// overlapAABBBuiltin is the default OverlapAABB implementation. It reads the
// parallel movement phase's frozen AABBs while that phase is running and live
// components otherwise, which is what keeps it — and every backend that
// delegates to BuiltinQueries — safe there (see phaseAABBs and QueryBackend).
func (s *Scene) overlapAABBBuiltin(box AABB, exclude ecs.Entity) []OverlapResult {
	return s.overlapAABBFrom(s.phaseAABBs(), box, exclude)
}

// overlapAABBFrom is the one implementation of the overlap query and of its
// #57 ordering contract, over whichever AABBs it is given: a frozen snapshot,
// or the live components when snapshot is nil.
func (s *Scene) overlapAABBFrom(snapshot map[ecs.Entity]AABB, box AABB, exclude ecs.Entity) []OverlapResult {
	var results []OverlapResult

	testEntity := func(entity ecs.Entity) {
		if entity == exclude {
			return
		}
		wb, ok := s.colliderAABBFrom(snapshot, entity)
		if !ok {
			return
		}
		if !box.Overlaps(wb) {
			return
		}

		// Insertion sort as results come in. Overlap counts at a single query
		// are small — a handful of colliders near one box — so this costs
		// nothing measurable (see BenchmarkOverlapAABB) and keeps the result
		// exactly as allocation-free as the append-driven growth already was.
		results = append(results, OverlapResult{})
		i := len(results) - 1
		for i > 0 && results[i-1].Entity > entity {
			results[i] = results[i-1]
			i--
		}
		results[i] = OverlapResult{Entity: entity, Box: wb}
	}

	// Use the spatial grids for nearby entities + scene objects, each candidate
	// once — see eachBroadPhaseCandidate.
	center := box.Min.Add(box.Max).Mul(0.5)
	radius := box.Max.Sub(box.Min).Len()*0.5 + 5 // box half-diagonal + padding
	s.eachBroadPhaseCandidate(center.X(), center.Z(), radius, testEntity)

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

		// Resolve against whichever overlap needs the smallest push to clear —
		// the least disruptive correction, and a stand-in for "the thing
		// actually in the way" when several colliders overlap at once. Ties
		// (including the common case of a single overlap) break on the lowest
		// entity id, so the result no longer depends on the order OverlapAABB
		// happened to return: that order comes from the spatial grid, whose
		// cell lists are filled by a Go map walk, so picking overlaps[0] made
		// Unstick nondeterministic — same start, different entity pushed out
		// of, different final position on every call (#57). Colliders that
		// lose this round are still overlapping on the next attempt and get
		// their turn within the 8-iteration budget.
		haveBest := false
		var bestPush float32
		var bestAxis int
		var bestDir float32
		var bestEntity ecs.Entity

		for _, ov := range overlaps {
			other := ov.Box
			overlapXPos := box.Max.X() - other.Min.X()
			overlapXNeg := other.Max.X() - box.Min.X()
			overlapZPos := box.Max.Z() - other.Min.Z()
			overlapZNeg := other.Max.Z() - box.Min.Z()

			push := overlapXPos
			axis, dir := 0, float32(-1) // -X
			if overlapXNeg < push {
				push = overlapXNeg
				axis, dir = 0, 1 // +X
			}
			if overlapZPos < push {
				push = overlapZPos
				axis, dir = 2, -1 // -Z
			}
			if overlapZNeg < push {
				push = overlapZNeg
				axis, dir = 2, 1 // +Z
			}

			if !haveBest || push < bestPush || (push == bestPush && ov.Entity < bestEntity) {
				haveBest = true
				bestPush, bestAxis, bestDir, bestEntity = push, axis, dir, ov.Entity
			}
		}

		t.Position[bestAxis] += bestDir * (bestPush + 0.01)
	}
}

// Raycast finds the nearest collider entity hit by a ray.
// The exclude entity (if non-zero) is skipped.
// Also tests the terrain heightmap for downward rays (checked first).
// Uses the SpatialGrid when available to reduce candidate entities from O(all) to O(nearby).
//
// When Scene.Queries is set, this delegates to it instead of the built-in
// implementation below — see QueryBackend for the contract a replacement has
// to honour.
func (s *Scene) Raycast(origin, dir mgl32.Vec3, maxDist float32, exclude ecs.Entity) (RayHit, bool) {
	if s.Queries != nil {
		return s.Queries.Raycast(origin, dir, maxDist, exclude)
	}
	return s.raycastBuiltin(origin, dir, maxDist, exclude)
}

// raycastBuiltin is the default Raycast implementation: terrain fast path,
// then spatial-grid broad phase, then convex-hull narrow phase. Like
// overlapAABBBuiltin it answers from the parallel movement phase's frozen
// AABBs while that phase is running — see phaseAABBs and QueryBackend for the
// collision-snapshot contract this depends on.
func (s *Scene) raycastBuiltin(origin, dir mgl32.Vec3, maxDist float32, exclude ecs.Entity) (RayHit, bool) {
	return s.raycastFrom(s.phaseAABBs(), origin, dir, maxDist, exclude)
}

// raycastFrom is the one implementation of the nearest-hit raycast and of its
// #57 tie-break, over whichever AABBs it is given: a frozen snapshot, or the
// live components when snapshot is nil. The terrain fast path and the hull
// narrow phase below read live state either way — neither changes during a
// parallel phase, since hull entities are Static (see QueryBackend).
func (s *Scene) raycastFrom(snapshot map[ecs.Entity]AABB, origin, dir mgl32.Vec3, maxDist float32, exclude ecs.Entity) (RayHit, bool) {
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
		wb, ok := s.colliderAABBFrom(snapshot, entity)
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
		// On an exact distance tie, keep the lower entity id rather than
		// whichever the spatial grid happened to visit first — that visit
		// order comes from a Go map walk (SpatialGrid.Update) and changes
		// call to call, which let coincident faces (a tiled floor of
		// identical boxes, two boxes sharing a wall) flip the reported
		// normal, and with it the character controller's walkability test,
		// nondeterministically (#57). best.Entity is 0 for a terrain hit —
		// no real entity id is ever 0 — so this never lets a collider beat
		// terrain on a tie, keeping terrain's existing first-checked win.
		if !found || dist < best.T || (dist == best.T && entity < best.Entity) {
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

	// Test entity colliders — use the spatial grids for O(nearby) instead of
	// O(all), each candidate once so the hull narrow phase above runs once per
	// candidate rather than twice for a static one (see
	// eachBroadPhaseCandidate).
	s.eachBroadPhaseCandidate(origin.X(), origin.Z(), maxDist, testEntity)

	return best, found
}

func abs32(x float32) float32 {
	if x < 0 {
		return -x
	}
	return x
}
