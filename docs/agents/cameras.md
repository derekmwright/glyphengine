---
id: cameras
title: Third-person orbit and first-person cameras
summary: >
  Drive the built-in orbit camera or first-person camera from mouse input, keep
  the orbit camera out of walls, and feed camera yaw back into movement.
capability: rendering
status: stable
since: v0.2.0
api:
  - glyphengine.Camera
  - glyphengine.NewCamera
  - glyphengine.Camera.Update
  - glyphengine.Camera.ResolveCollision
  - glyphengine.Camera.ViewVectors
  - glyphengine.Camera.WasClick
  - glyphengine.FPCamera
  - glyphengine.NewFPCamera
  - glyphengine.FPCamera.Update
  - glyphengine.FPCamera.Follow
  - glyphengine.FPCamera.ViewVectors
  - glyphengine.FPCamera.Forward
  - glyphengine.Raycaster
  - glyphengine.Engine.SetCamera
example: examples/04-first-person
run: task example:04-first-person
requires:
  - cgo
  - vulkan-runtime
assets: none
verified: 2026-07-28
---

# Third-person orbit and first-person cameras

Both cameras return the same three vectors from `ViewVectors`, so
`e.SetCamera(cam.ViewVectors())` works with either, and swapping between them
is a one-line change.

Both use the same angle convention: **yaw 0 looks down −Z, positive pitch looks
down**. That means `MoveIntent.Yaw` can be fed from either camera unchanged.

## First-person

```go
g.camera = glyphengine.NewFPCamera()
g.camera.EyeHeight = 0.7
e.Input().SetCursorLocked(true)

// each frame:
g.camera.Update(in)                    // mouse look; no-op unless cursor locked
if t, ok := e.C.Transform.Get(player); ok {
	g.camera.Follow(t)                 // anchor to the character
}
e.SetCamera(g.camera.ViewVectors())
```

`Update` only looks while the cursor is locked, so releasing the cursor for a
menu freezes the camera with no extra state to manage.

`EyeHeight` is measured from the entity **origin**, not from the ground. The
character controller keeps the origin at the collider's center, so for a
character with `HalfExtents.Y = 0.9` an `EyeHeight` of 0.7 puts the eye near the
top of the capsule.

`Forward()` gives the look direction — use it for interaction raycasts:

```go
if hit, ok := e.Raycast(g.camera.Eye(), g.camera.Forward(), 3.0, player); ok {
	g.interactWith(hit.Entity)
}
```

## Third-person orbit

```go
g.camera = glyphengine.NewCamera(8) // orbit distance
g.camera.Target = mgl32.Vec3{0, 1, 0}

// each frame:
g.camera.Update(in)                          // drag orbits, scroll zooms
g.camera.ResolveCollision(e.Scene, player, dt)
e.SetCamera(g.camera.ViewVectors())
```

Drag with either mouse button to orbit; the cursor locks while a button is
held. Scroll adjusts `Distance` between 0 and 20. Below `0.5` the camera snaps
to first person, with the eye at `Target + LookOffset`.

Set `EditorMode = true` to reserve left-click for picking, leaving only
right-drag to orbit.

`WasClick(in)` distinguishes a pick click from an orbit drag — true only when
the left button is released after less than 5 pixels of movement:

```go
if g.camera.WasClick(in) {
	mx, my := in.MousePos()
	if hit, ok := e.PickEntity(mx, my, 100, 0); ok { ... }
}
```

## Camera collision takes an interface, not the engine

`ResolveCollision` raycasts from the target toward the ideal eye position and
pulls the camera in when geometry blocks the view. It takes a `Raycaster`:

```go
type Raycaster interface {
	Raycast(origin, dir mgl32.Vec3, maxDist float32, exclude ecs.Entity) (RayHit, bool)
}
```

`*Scene` implements it. Taking the interface rather than `*Engine` keeps the
camera testable and lets a game substitute its own broadphase.

Pass the **frame** `dt`. The pull-in is fast (so the camera snaps out of walls)
and the ease-out is slow (so it drifts back smoothly); both are exponential and
frame-rate independent.

Always pass the player entity as `exclude`, or the camera collides with the
character it is following and jams at minimum distance.

## Projection

`Engine.ViewProjection` builds a reverse-Z, Y-flipped perspective matrix.
Override the field of view and clip planes with `WithProjection(fov, near,
far)` — a large open world wants a farther far plane than the 500-unit default.

## Failure modes

- **Camera does not respond to the mouse.** For `FPCamera`, the cursor is not
  locked. For `Camera`, no mouse button is held.
- **Camera is stuck at minimum distance.** `ResolveCollision` is hitting the
  followed entity — pass it as `exclude`.
- **First-person view is inside the character's head geometry.** Expected: hide
  the character's mesh with the `Hidden` tag in first person, or offset the eye
  forward.
- **Look feels jerky.** `Camera.Update` and `FPCamera.Update` consume mouse
  delta and must be called exactly once per frame, from `Game.Update` — not
  from a fixed-timestep system, which would consume the delta several times or
  none.
