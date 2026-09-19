---
id: physics-queries
title: Colliders, raycasts, overlap queries, and body integration
summary: >
  Attach AABB and convex-hull colliders, query the world with Raycast and
  OverlapAABB, pick entities from the screen, and let IntegrateBodies apply
  gravity and ground snapping.
capability: physics
status: stable
since: v0.2.0
api:
  - glyphengine.Collider
  - glyphengine.ConvexHullCollider
  - glyphengine.AABB
  - glyphengine.RayHit
  - glyphengine.OverlapResult
  - glyphengine.WorldAABB
  - glyphengine.Scene.Raycast
  - glyphengine.Scene.OverlapAABB
  - glyphengine.Scene.Unstick
  - glyphengine.Scene.UpdateSpatialGrid
  - glyphengine.Scene.RebuildStatics
  - glyphengine.IntegrateBodies
  - glyphengine.Scene.Integrator
  - glyphengine.QueryBackend
  - glyphengine.Scene.Queries
  - glyphengine.Scene.BuiltinQueries
  - glyphengine.Engine.PickEntity
  - glyphengine.Engine.ScreenRay
  - glyphengine.ComputeConvexHull
  - glyphengine.GJKOverlapAABB
  - glyphengine.RaycastHull
example: examples/03-physics
run: task example:03-physics
requires:
  - cgo
  - vulkan-runtime
assets: none
verified: 2026-09-19
---

# Colliders, raycasts, overlap queries, and body integration

## Colliders

`Collider` is an **axis-aligned** box given as half-extents in local space.
World-space extents are half-extents multiplied by `Transform.Scale`, centered
on `Transform.Position`. Rotation is ignored, and **there is no local offset**.

```go
// A 1x1x1 cube mesh is origin-centered, so its collider is 0.5 on each axis.
scene.C.Collider.Set(ent, &glyphengine.Collider{
	HalfExtents: mgl32.Vec3{0.5, 0.5, 0.5},
})
```

Because the box is centered on the entity origin, your visual mesh must be
origin-centered too, or the collider and the geometry will not line up.
`CreateCube` is centered; `CreateCylinder` and `CreateCone` put their base at
`Y=0`, so pair those with a scaled cube collider and position the entity at the
shape's midpoint.

For shapes an AABB approximates badly — angled walls, rocks, arches — add a
`ConvexHullCollider`. The AABB still runs as broad phase; the hull runs as
narrow phase.

```go
points, err := glyphengine.ExtractGLTFPositions(assetsFS, "models/rock.glb")
if err != nil {
	return err
}
hull := glyphengine.ComputeConvexHull(points)
scene.C.ConvexHullCollider.Set(ent, hull)

// The broad-phase AABB must enclose the hull, or narrow phase never runs.
min, max := glyphengine.HullBounds(hull)
scene.C.Collider.Set(ent, &glyphengine.Collider{
	HalfExtents: max.Sub(min).Mul(0.5),
})
scene.C.Static.Set(ent, &glyphengine.Static{})
```

Hull computation is offline work — do it at build time and ship the result with
`SerializeHull` / `DeserializeHull` rather than running quickhull at startup.

**Hull entities must be `Static`.** The parallel movement phase depends on it.

## Raycasting

```go
hit, ok := scene.Raycast(origin, dir, maxDist, exclude)
if ok {
	fmt.Println(hit.Entity, hit.T, hit.Point, hit.Normal)
}
```

- `dir` must be normalized — `hit.T` is a distance along it.
- `exclude` skips one entity; pass `0` to skip none. This is almost always the
  entity casting the ray.
- Terrain is tested first for near-vertical downward rays, since that is the
  common case and the heightmap answers it in O(1).
- A ray starting **inside** a box does not count as a hit.
- **Exact-distance ties keep the lower entity id.** Two colliders hit at
  precisely the same distance — coincident faces, a tiled floor of identical
  boxes — used to report whichever one the spatial grid's cell list visited
  first, which is a Go map walk (`SpatialGrid.Update`) and changed from call
  to call. That flipped `hit.Entity` and `hit.Normal` nondeterministically,
  which matters because the character controller's walkable-slope test reads
  `hit.Normal` (see `character-controller`). A terrain hit always wins its own
  tie against any collider — terrain is checked first, and its `RayHit` has
  the zero-value `Entity`, which is lower than any real entity id.

## Overlap queries

```go
box := glyphengine.WorldAABB(transform, collider)
for _, ov := range scene.OverlapAABB(box, self) {
	// ov.Entity, ov.Box
}
```

`OverlapAABB` allocates its result slice, which makes it safe to call from the
parallel movement goroutines.

**Results are ordered by ascending entity id.** That is part of the contract,
not an implementation detail: candidates come off the spatial grid, whose
cell lists are filled by a Go map walk, so without an explicit sort a caller
reading `results[0]` would get a different entity on every call — which is
exactly what made `Unstick` nondeterministic before this was fixed (#57). The
sort is an insertion as results are appended, so it costs nothing measurable
at the overlap counts a single query actually sees (a handful of nearby
colliders) and adds no allocations beyond the result slice's own growth;
`BenchmarkOverlapAABB` in `physics_order_test.go` has the measured numbers.

## Swapping the query backend

`Scene.Queries` replaces Raycast and OverlapAABB together, for a game backing
collision queries with its own broadphase (a BVH, say) instead of the
spatial-grid implementation above:

```go
type myBackend struct{ /* your own broadphase */ }

func (b *myBackend) Raycast(origin, dir mgl32.Vec3, maxDist float32, exclude glyphengine.Entity) (glyphengine.RayHit, bool) {
	// ...
}

func (b *myBackend) OverlapAABB(box glyphengine.AABB, exclude glyphengine.Entity) []glyphengine.OverlapResult {
	// ...
}

scene.Queries = &myBackend{}
```

Nil (the default) keeps the built-in implementation. Setting it is a one-step
swap because every internal consumer calls `Scene.Raycast` or
`Scene.OverlapAABB` and nothing else — `IntegrateBodies`'s grounding fallback,
`MoveCharacter`'s ground detection and collision check, `Unstick`,
`Camera.ResolveCollision` (through the `Raycaster` it takes — pass the
`*Scene` itself, not a narrower value, or the swap will not reach it), and
`Engine.PickEntity`. A game that only implements `Raycaster` and hands it to
`Camera.ResolveCollision` only affects the camera; `Scene.Queries` is what
reaches the engine's own physics.

**A replacement must not call `scene.Raycast` or `scene.OverlapAABB`.** With
`Queries` set those are the replacement, so the call is the backend calling
itself until the stack runs out. `Scene.BuiltinQueries()` is the engine's
implementation as a `QueryBackend`, whatever `Queries` is set to, and it is how
a backend that only wants to change part of the answer delegates the rest —
the same way a custom `Integrator` calls `IntegrateBodies`:

```go
type ghostBackend struct {
	inner glyphengine.QueryBackend // scene.BuiltinQueries()
	ghost glyphengine.Entity
}

func (b *ghostBackend) OverlapAABB(box glyphengine.AABB, exclude glyphengine.Entity) []glyphengine.OverlapResult {
	var kept []glyphengine.OverlapResult
	for _, r := range b.inner.OverlapAABB(box, exclude) {
		if r.Entity != b.ghost {
			kept = append(kept, r)
		}
	}
	return kept
}
```

A backend built on it inherits the collision snapshot and needs no freeze of
its own. `query_builtin_test.go` is that backend, written in the external test
package so that it has only what a game has.

**A replacement must honour the #57 order contracts** — `OverlapAABB`
ascending by entity id, `Raycast` breaking exact ties on the lower entity id
with terrain first — documented in full on `QueryBackend`. Measured, not
assumed: scrambling `OverlapAABB`'s order does not currently break `Unstick`
or `MoveCharacter`, because both scan the whole result list themselves rather
than reading a positional index (see `QueryBackend`'s doc comment and
`query_backend_test.go`). The contract is still required of a replacement
because `OverlapAABB` documents it to callers outside the engine too, the same
guarantee `Unstick` itself used to violate before #57.

**A replacement must honour the collision snapshot**, or
`MoveCharactersParallel` becomes timing-dependent. There is no lifecycle hook
for this — `QueryBackend` is deliberately just the two query methods. A
replacement backed by its own index that is only rebuilt between ticks
satisfies the freeze requirement automatically, since nothing in it changes
during the parallel phase in the first place; a replacement that reads
Scene's live `Transform`/`Velocity` itself needs to freeze its own copy the
way `colliderAABB` does.

**A replacement must not keep scratch state shared across calls.** Raycast and
OverlapAABB run concurrently from multiple goroutines during
`MoveCharactersParallel` — the built-in implementation uses
`SpatialGrid.QueryRadiusAlloc`, which allocates a fresh slice per call, rather
than `QueryRadius`, which reuses one and is documented NOT safe for this.
`controller_race_test.go`-style coverage with a custom backend installed is in
`query_backend_test.go` (`TestMoveCharactersParallelRaceWithCustomQueryBackend`).

## Screen picking

```go
mx, my := e.Input().MousePos()
if hit, ok := e.PickEntity(mx, my, 100, 0); ok {
	e.C.Highlighted.Set(hit.Entity, &glyphengine.Highlighted{})
}
```

`PickEntity` is `ScreenRay` plus `Raycast`. Use `ScreenRay` directly when you
want the ray for something else — a placement preview, a laser, a
line-of-sight test.

## Broad phase

Two grids, with different rebuild costs:

| Grid | Contents | Rebuild |
|---|---|---|
| `SpatialGrid` | everything with a `Transform` | `UpdateSpatialGrid()`, once per tick |
| `StaticGrid` | entities tagged `Static` | `RebuildStatics()`, only when world geometry changes |

With no grid, queries fall back to a linear scan of every collider. That is
correct but O(n) per query, and it is the usual reason a scene that ran fine
with 50 entities crawls at 5,000.

## Body integration

`Scene.Tick` runs `Scene.Integrator`, which defaults to `IntegrateBodies`:
gravity and velocity integration for every entity with `Transform` +
`Velocity`:

- With a `Collider`, it also snaps to the ground — the terrain heightmap when
  there is one, a downward raycast otherwise.
- Without a `Collider`, it just integrates. That is what you want for
  projectiles and debris that should not touch the floor.
- Entities with a `CharacterController` are skipped entirely; `MoveCharacter`
  does their gravity and collision. Integrating them here too would apply
  gravity twice per tick.

Set `Scene.Gravity` to change the rate (`DefaultGravity` is 20 units/s²), or to
`0` to disable it.

**This is not a rigid-body solver.** Bodies resolve against the world, not
against each other. There is no stacking, no restitution, no angular velocity.
Body-to-body response is the character controller's job or a game-side system's.

### Replacing the integrator

`Scene.Integrator` is called in `IntegrateBodies`'s place, every `Tick`. A game
whose bodies need different dynamics — a real rigid-body solver, buoyancy,
whatever `IntegrateBodies`'s single ground-snap model does not cover —
replaces it:

```go
scene.Integrator = func(s *glyphengine.Scene, dt float32) {
	myPhysics.Step(s, dt)
	// Delegate anything myPhysics does not want to own to the built-in
	// integrator — it stays exported for exactly this.
	glyphengine.IntegrateBodies(s, dt)
}
```

`NewScene` sets `Integrator` to `IntegrateBodies`, so the default path is
unaffected. **Nil does not mean "skip integration."** Unlike `Env`, `Terrain`,
`PathFinder`, and `SpatialGrid` — where nil is a supported "off" — `Tick` falls
back to `IntegrateBodies` whenever `Integrator` is nil, including for a Scene
built some way other than `NewScene`. A game that wants no built-in
integration at all sets a real no-op instead of relying on the zero value:

```go
scene.Integrator = func(*glyphengine.Scene, float32) {}
```

The character-controller split is unchanged: whatever runs in `Integrator`'s
place still should not touch `CharacterController` entities — those are
`MoveCharacter`'s job, and integrating them twice applies gravity twice per
tick.

## Getting unstuck

`Unstick(entity)` nudges an entity out of overlapping colliders on the XZ
plane, up to eight iterations. Call it after spawning or teleporting.

Each iteration resolves against whichever overlap needs the **smallest push**
to clear — the least disruptive correction, and the best available proxy for
"the thing actually in the way" when several colliders overlap at once. Ties,
including the ordinary case of a single overlap, break on the **lowest entity
id**. That tie-break is why the single-overlap case behaves exactly as
before: with nothing to choose between, the smallest-push search picks the
same collider a first-match search would have.

This scan is what makes `Unstick` deterministic. Before this was fixed
(#57), it read `overlaps[0]` — whichever collider `OverlapAABB` happened to
list first, which came from the spatial grid's map-walk-filled cell lists and
changed from call to call. An entity spawned overlapping two or three
colliders could be pushed out of a different one, and land in a different
final spot, on every call, every tick, every run. Colliders that lose a given
round are still overlapping on the next attempt and get their turn within the
8-iteration budget, so a character boxed in on multiple sides still gets
fully freed — just via a fixed sequence instead of whatever order the map
walk produced.

## Failure modes

- **Raycast never hits.** `dir` is not normalized, or `maxDist` is too short,
  or the target entity has no `Collider` (a `MeshRef` alone is invisible to
  physics).
- **A hull is ignored.** Narrow phase only runs on entities that passed AABB
  broad phase. A hull without an enclosing `Collider` is never tested.
- **Characters walk up walls.** Only `ConvexHullCollider` hits get the
  walkable-slope test (`normal.Y > 0.5`). A plain AABB has axis-aligned faces
  and is treated as walkable when hit from above.
- **Everything is slow.** Missing `UpdateSpatialGrid()` in the tick.
- **`Unstick` moved an entity somewhere unexpected with several overlapping
  colliders.** Check which collider actually needed the smallest push — it
  resolves the shallowest overlap first, not necessarily the one that looks
  most "in the way" visually.
- **A custom `Integrator` silently stopped physics.** Assigning `nil` does
  not do this — `Tick` falls back to `IntegrateBodies`. Assigning a function
  that does nothing does; that is the only way integration turns off.
- **The game dies with a stack overflow the first time anything queries the
  world.** A `Queries` backend is calling `scene.Raycast` or
  `scene.OverlapAABB`, which is itself. Delegate to `scene.BuiltinQueries()`.
- **A custom `Queries` backend only affects some of the engine.** It must be
  set on the `Scene` (`scene.Queries = ...`), and passed to
  `Camera.ResolveCollision` as the `*Scene` itself rather than a narrower
  `Raycaster`-only value — otherwise the camera collides against whatever it
  was handed while everything else uses the replacement.
- **`MoveCharactersParallel` behaves differently tick to tick with a custom
  `Queries` backend.** The backend is not honouring the collision snapshot —
  see "Swapping the query backend" above.
