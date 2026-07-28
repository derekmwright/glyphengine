---
id: character-controller
title: Move a character with MoveIntent
summary: >
  Drive a walking, jumping, wall-sliding character from a device-agnostic
  intent struct, on one goroutine or many.
capability: physics
status: stable
since: v0.2.0
api:
  - glyphengine.CharacterController
  - glyphengine.NewCharacterController
  - glyphengine.MoveIntent
  - glyphengine.Scene.MoveCharacter
  - glyphengine.Scene.MoveCharactersParallel
  - glyphengine.MoveBatchEntry
  - glyphengine.CharacterState
  - glyphengine.Scene.SnapshotCharacter
  - glyphengine.Scene.RestoreCharacter
  - glyphengine.GroundEpsilon
example: examples/04-first-person
run: task example:04-first-person
requires:
  - cgo
  - vulkan-runtime
assets: none
verified: 2026-07-28
---

# Move a character with MoveIntent

A character entity needs four components. `CharacterController` is what marks
it as controller-driven — `IntegrateBodies` skips those entities, because
`MoveCharacter` does its own gravity, grounding, and collision.

```go
player := scene.Spawn()
scene.C.Transform.Set(player, &glyphengine.Transform{
	Position: mgl32.Vec3{0, 0.9, 0},
	Scale:    mgl32.Vec3{1, 1, 1},
})
scene.C.Collider.Set(player, &glyphengine.Collider{
	HalfExtents: mgl32.Vec3{0.4, 0.9, 0.4},
})
scene.C.Velocity.Set(player, &glyphengine.Velocity{})
cc := glyphengine.NewCharacterController() // walk 4, run 8, jump 7
scene.C.CharacterController.Set(player, &cc)
```

Then split input from movement across the two clocks — **read input per frame,
move per tick**:

```go
func (g *game) Update(e *glyph.Engine, dt float32) {
	in := e.Input()

	g.intent = glyph.MoveIntent{Yaw: g.camera.Yaw}
	if in.KeyDown(input.KeyW) { g.intent.Forward++ }
	if in.KeyDown(input.KeyS) { g.intent.Forward-- }
	if in.KeyDown(input.KeyD) { g.intent.Right++ }
	if in.KeyDown(input.KeyA) { g.intent.Right-- }
	g.intent.Sprint = in.KeyDown(input.KeyLeftShift)

	// Edge-triggered input must be latched: a frame may run no tick at all.
	if in.KeyPressed(input.KeySpace) { g.jumpQueued = true }
}

func (g *game) FixedUpdate(e *glyph.Engine, dt float32) {
	e.UpdateSpatialGrid()

	intent := g.intent
	intent.Jump = g.jumpQueued
	g.jumpQueued = false // consume exactly once

	e.MoveCharacter(g.player, intent, dt)
}
```

Full program: `examples/04-first-person`.

**Call it from `FixedUpdate`, not `Update`.** `MoveCharacter` integrates
gravity and sweeps collision one step per call, so a variable `dt` makes its
results frame-rate dependent — jump apex varies about 5% between 30fps and
1000fps. On the fixed tick every machine agrees exactly, which is what makes
the same controller usable on an authoritative server. See `game-loop` for the
full ordering and the input-latching rule.

## MoveIntent is not a keyboard struct

That is the whole design. The same value can come from a gamepad, an AI, a
replay, or a decoded network packet, which is what lets one controller run
identically on a client and an authoritative server.

| Field | Meaning |
|---|---|
| `Forward`, `Right` | XZ movement, relative to `Yaw`. Only the **sign** is used — magnitude does not scale speed. |
| `Yaw` | Reference heading in radians, normally the camera yaw. |
| `Turn` | Rotate in place at `TurnRate`. Non-zero `Turn` makes `Forward`/`Right` relative to the character's *new* facing, so keyboard turning and camera-relative movement compose. |
| `Sprint` | Use `RunSpeed` instead of `WalkSpeed`. |
| `Jump` | Apply `JumpSpeed` if grounded. Feed this an edge-triggered `KeyPressed`, not `KeyDown`. |
| `SpeedScale` | Multiplies final horizontal speed — haste, snares, roots. Zero means 1. |

Call it with the **frame** delta from `Game.Update`, not a fixed tick delta.

## Facing

Moving forward turns the character to face the movement direction. Backpedaling
keeps the current facing — otherwise it flips 180° every tick and visibly
oscillates. Pure strafing faces `Yaw`.

## Grounded state

`CharacterController.Grounded` is written by every `MoveCharacter` call. Read it
to gate jump input, pick airborne animations, or play a landing sound.

```go
if cc, ok := scene.C.CharacterController.Get(player); ok && !cc.Grounded {
	anim.PlayLoop(glyphengine.FindClip(model, "jump"), 1.0)
}
```

It is false on the tick a jump is applied.

## Collision

Movement is axis-separated: each of X, Y, Z is tried independently, and a
blocked axis is zeroed while the others still move. That is what produces
sliding along a wall instead of stopping dead against it.

Convex-hull entities get a GJK narrow-phase test against a **feet-only** box —
the bottom 0.5 units of the character's AABB — so a character collides with the
hull's actual shape rather than its oversized bounding box, and can walk under
an arch.

Ground detection prefers the terrain heightmap (O(1)) and only falls back to a
raycast when off the heightmap or near static geometry.

## Moving many characters in parallel

For a server stepping hundreds of characters per tick:

```go
batch := make([]glyphengine.MoveBatchEntry, 0, len(clients))
for _, c := range clients {
	batch = append(batch, glyphengine.MoveBatchEntry{Entity: c.Entity, Intent: c.Intent})
}
scene.UpdateSpatialGrid()
scene.MoveCharactersParallel(batch, dt)
```

A single-player game should just call `MoveCharacter` in a loop — the batch
version only pays for itself at scale.

Two invariants make it safe, and both are easy to break from the outside:

1. **Hull entities must be `Static`.** During the phase, AABB queries read a
   frozen snapshot of every collider's world box, but hull narrow phase reads
   *live* transforms. That is sound only because hull entities do not move.
   Attaching a `ConvexHullCollider` to a moving entity introduces a data race.

2. **Work is partitioned by entity hash, not batch position.** If the same
   entity appears twice in one batch — which happens when a client's frame
   clock delivers two inputs inside one tick window — both must apply
   sequentially, in order. A positional split would race on that entity's own
   `Transform` and silently drop a frame of movement.

`controller_race_test.go` covers both under `-race`.

## Failure modes

- **Character falls through the world.** No terrain and no collider under it.
  `SetTerrain` or give the ground an entity with a `Collider`.
- **Character never jumps.** `Jump` wired to `KeyDown` instead of
  `KeyPressed`, or it is not actually grounded — check `cc.Grounded`.
- **Jumps are dropped intermittently, worse on a high-refresh monitor.**
  `KeyPressed` is being read inside `FixedUpdate`, which does not run on every
  frame. Latch it in `Update` instead.
- **Character does not move at all.** `MoveCharacter` is a no-op unless the
  entity has all four of `Transform`, `Collider`, `Velocity`, and
  `CharacterController`.
- **Character jitters against walls.** Its collider is larger than its visual
  mesh, or the mesh is not origin-centered. Colliders are always centered on
  the transform position.
- **Character sinks slightly into the floor each frame.** Its `Transform.Scale`
  is not `{1,1,1}` — collider half-extents are multiplied by scale.
