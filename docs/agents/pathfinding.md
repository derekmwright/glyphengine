---
id: pathfinding
title: A* pathfinding over a navigation grid
summary: >
  Build a walkability grid from terrain and colliders, then request smoothed
  paths through an amortized queue that spreads A* cost across ticks.
capability: navigation
status: stable
since: v0.2.0
api:
  - glyphengine.NavGrid
  - glyphengine.BuildNavGrid
  - glyphengine.NavGrid.FindPath
  - glyphengine.NavGrid.SmoothPath
  - glyphengine.NavGrid.IsWalkable
  - glyphengine.NavGrid.WorldToGrid
  - glyphengine.NavGrid.GridToWorld
  - glyphengine.NavGrid.WalkableCount
  - glyphengine.PathFinder
  - glyphengine.NewPathFinder
  - glyphengine.PathFinder.Request
  - glyphengine.PathFinder.Cancel
  - glyphengine.PathFinder.Tick
  - glyphengine.PathFinder.Pending
  - glyphengine.Vec2
requires:
  - cgo
assets: none
verified: 2026-07-28
---

# A* pathfinding over a navigation grid

A `NavGrid` is a boolean walkability grid on the XZ plane. Build one from
terrain and static colliders, hand it to a `PathFinder`, and put the finder on
the scene — `Scene.Tick` will drain its queue.

```go
// Collect the static world geometry the grid should treat as blocked.
var boxes []glyphengine.AABB
var hulls []glyphengine.HullColliderInfo
ecs.Query3(scene.C.Transform, scene.C.Collider, scene.C.Static,
	func(e ecs.Entity, t *glyphengine.Transform, c *glyphengine.Collider, _ *glyphengine.Static) {
		boxes = append(boxes, glyphengine.WorldAABB(t, c))
		if h, ok := scene.C.ConvexHullCollider.Get(e); ok {
			hulls = append(hulls, glyphengine.HullColliderInfo{Hull: h, Transform: t})
		}
	})

const cellSize, slopeThreshold = 1.0, 0.7
scene.NavGrid = glyphengine.BuildNavGrid(scene.Terrain, boxes, hulls, cellSize, slopeThreshold)
scene.PathFinder = glyphengine.NewPathFinder(scene.NavGrid, 8) // 8 paths per tick
```

`slopeThreshold` is the minimum surface normal Y for a terrain cell to count as
walkable — 0.7 is roughly a 45° limit.

Requesting a path is asynchronous — the callback fires on a later `Tick`:

```go
scene.PathFinder.Request(
	glyphengine.Vec2{X: from.X(), Z: from.Z()},
	glyphengine.Vec2{X: to.X(), Z: to.Z()},
	func(path []glyphengine.Vec2) {
		if path == nil {
			return // unreachable
		}
		mob.Path = path
	},
)
```

`Request` returns an ID; `Cancel(id)` drops a still-queued request, which is
what you do when the target dies or the requester despawns.

## Why a queue

A* over a large grid is expensive and bursty: fifty mobs all repathing on the
same tick is a visible frame spike. `maxPerTick` caps how many paths are solved
per `Tick`, so the cost amortizes across frames instead of landing on one.

Tune it against your budget: `Pending()` growing without bound means
`maxPerTick` is too low for your request rate, and paths will arrive stale.

Each path also has a node budget (2,000 by default). A search that exceeds it
gives up and returns nil rather than stalling the tick — so a nil path means
"no route found **or** the route was too expensive to search", and callers
should treat both the same way.

## Concurrency

`Request` and `Cancel` are safe to call from any goroutine, which matters
because AI systems commonly request paths off the tick.

`Tick` is **not**: it invokes callbacks that typically write ECS components, so
it must run on the goroutine that owns the world. `Scene.Tick` calls it for
you at the right point. Do not call it from a worker.

Internally `Tick` detaches its batch under the lock and solves outside it, so a
callback that issues another `Request` does not deadlock.

## Smoothing

`FindPath` returns grid-aligned waypoints, which look robotic when followed
directly. `PathFinder` runs `SmoothPath` on every result, which removes
waypoints that a straight line can skip. Call `SmoothPath` yourself if you use
`FindPath` directly.

## Keeping the grid current

`NavGrid` is a snapshot. Destroying a wall does not make the grid walkable —
rebuild it, or flip the affected cells in `Walkable` yourself. Because it is
built from `Static` geometry, rebuilding is cheap enough to do on world
changes and far too expensive to do per tick.

## Failure modes

- **`BuildNavGrid` panics.** It sizes the grid from the heightmap's world
  extent and does not accept a nil one. Call `SetTerrain` first.
- **Every path comes back nil.** Start or goal is outside the grid, or on an
  unwalkable cell. Snap both endpoints to the nearest walkable cell first —
  `WorldToGrid` plus `IsWalkable` is enough. `WalkableCount()` returning 0
  means the grid is entirely blocked, usually a `slopeThreshold` set too high.
- **Paths arrive several seconds late.** `maxPerTick` is too low, or `Tick` is
  not being called — check that `Scene.PathFinder` is actually set, since
  `Scene.Tick` skips a nil finder silently.
- **Agents walk into walls that were added after startup.** The grid is stale.
- **Deadlock inside a path callback.** You called `Tick` from a goroutine while
  the main loop was also ticking. Only the world-owning goroutine may tick.
