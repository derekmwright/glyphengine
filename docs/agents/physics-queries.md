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
verified: 2026-07-28
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

## Overlap queries

```go
box := glyphengine.WorldAABB(transform, collider)
for _, ov := range scene.OverlapAABB(box, self) {
	// ov.Entity, ov.Box
}
```

`OverlapAABB` allocates its result slice, which makes it safe to call from the
parallel movement goroutines.

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

`Scene.Tick` runs `IntegrateBodies`, which applies gravity and integrates
velocity for every entity with `Transform` + `Velocity`:

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

## Getting unstuck

`Unstick(entity)` nudges an entity out of overlapping colliders on the XZ
plane, up to eight iterations. Call it after spawning or teleporting.

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
