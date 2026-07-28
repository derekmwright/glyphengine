---
id: scene-entities
title: Build a scene from entities and components
summary: >
  Spawn entities, attach engine components, register per-tick systems, and keep
  game-specific components in a struct the engine never sees.
capability: ecs
status: stable
since: v0.2.0
api:
  - glyphengine.NewScene
  - glyphengine.Scene.Spawn
  - glyphengine.Scene.Despawn
  - glyphengine.Scene.Tick
  - glyphengine.Scene.AddSystem
  - glyphengine.Scene.World
  - glyphengine.Components
  - glyphengine.NewComponents
  - glyphengine.Transform
  - glyphengine.MeshRef
  - glyphengine.Color
  - glyphengine.Static
  - ecs.NewStore
  - ecs.Query2
  - ecs.Query3
example: examples/03-physics
run: task example:03-physics
requires:
  - cgo
  - vulkan-runtime
assets: none
verified: 2026-07-28
---

# Build a scene from entities and components

A `Scene` owns the ECS world, the engine's component stores, physics
acceleration structures, terrain, and the day/night cycle. It has **no window
or renderer dependency**, so a headless server or a test can create one
directly.

```go
scene := glyphengine.NewScene()

ent := scene.Spawn()
scene.C.Transform.Set(ent, &glyphengine.Transform{
	Position: mgl32.Vec3{0, 1, 0},
	Scale:    mgl32.Vec3{1, 1, 1}, // a zero Scale collapses the mesh
})
scene.C.MeshRef.Set(ent, &glyphengine.MeshRef{Mesh: cube})
scene.C.Color.Set(ent, &glyphengine.Color{R: 0.8, G: 0.4, B: 0.2})

scene.Tick(1.0 / 60.0)
```

Under `Engine`, `New` builds a scene for you (or takes yours with
`WithScene`) and `Run` calls `Tick`. You only call `Tick` yourself when driving
a scene headlessly.

## The engine/game component boundary

`Scene.C` holds **only** the stores the engine itself reads: physics and
spatial queries, draw-list building, and animation sampling.

| Group | Stores |
|---|---|
| Physics & spatial | `Transform`, `PrevTransform`, `Velocity`, `Collider`, `ConvexHullCollider`, `CharacterController`, `Static` |
| Animation & rendering | `AnimationState`, `SkeletonRef`, `MeshRef`, `MaterialRef`, `Color` |
| Render flags | `Hidden`, `Highlighted`, `DoubleSided`, `Emissive`, `NoCastShadow` |

Your components go in **your own struct**, registered on the same `World`:

```go
type Health struct{ HP, MaxHP float32 }
type Inventory struct{ Items []ItemID }

type GameComponents struct {
	Health    *ecs.Store[Health]
	Inventory *ecs.Store[Inventory]
}

func NewGameComponents(w *ecs.World) *GameComponents {
	return &GameComponents{
		Health:    ecs.NewStore[Health](w),
		Inventory: ecs.NewStore[Inventory](w),
	}
}

gc := NewGameComponents(scene.World())
gc.Health.Set(ent, &Health{HP: 100, MaxHP: 100})
```

Same entity IDs, same world, same queries — the engine simply cannot see them.
That is the point: the boundary is enforced by the compiler, not by a
convention or a lint rule.

## Systems

`AddSystem` registers a per-tick callback. Systems run in registration order at
the end of `Tick`, after day/night, body integration, and pathfinding.

```go
scene.AddSystem(func(s *glyphengine.Scene, dt float32) {
	ecs.Query2(gc.Health, s.C.Transform, func(e ecs.Entity, h *Health, t *glyphengine.Transform) {
		if h.HP <= 0 {
			s.Despawn(e)
		}
	})
})
```

This is a deliberately minimal ordered list, not a scheduler: no dependency
graph, no parallel stages, no automatic component-access analysis. If you need
those, build them above this.

## Querying

`ecs.Query2` / `Query3` / `Query4` iterate entities that have all the listed
components, driving from the smallest store. They avoid the reflection and
interface boxing of `World.Each`, so use them on hot paths.

```go
ecs.Query3(s.C.Transform, s.C.Velocity, s.C.Collider,
	func(e ecs.Entity, t *Transform, v *Velocity, c *Collider) { ... })
```

The pointers handed to the callback are **live** — write through them to mutate
the component.

## Lifecycle

- `Spawn()` returns a fresh entity with no components.
- `Despawn(e)` removes the entity and every component attached to it, including
  components in stores your game registered on the same world.
- Do not `Despawn` from inside a query over a store you are iterating. Collect
  the entities first, then destroy after the query returns.

`PrevTransform` is engine-managed: `Scene.Tick` writes it so rendering can
interpolate between ticks. Games never set it, but they do call
`Scene.ClearInterpolation` after teleporting an entity. See `game-loop`.

## The `Static` tag is load-bearing

`Static` marks geometry that never moves. It puts the entity in
`Scene.StaticGrid`, rebuilt only when you call `RebuildStatics`, instead of the
per-tick `SpatialGrid`.

It is not only an optimization. The parallel movement phase runs convex-hull
narrow-phase tests against *live* transforms while AABB queries read a frozen
snapshot, which is sound only because hull entities do not move during that
phase. Attaching a `ConvexHullCollider` to a moving entity breaks that
invariant. See `character-controller`.

## Failure modes

- **Entity does not render.** It needs both `Transform` and `MeshRef`, and a
  non-zero `Scale`.
- **Physics queries are slow.** Call `Scene.UpdateSpatialGrid()` once per tick.
  Without a grid, `OverlapAABB` and `Raycast` fall back to a linear scan over
  every collider — correct, but O(n) per query.
- **Static geometry is invisible to queries.** `Static` entities are only
  indexed by `RebuildStatics`. Call it after loading or changing world geometry.
