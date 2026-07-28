---
id: game-loop
title: Run a game loop with Engine and Game
summary: >
  Implement the Game interface, configure the window and renderer with
  options, and let Engine drive fixed-timestep simulation with variable-rate
  rendering.
capability: ecs
status: stable
since: v0.2.0
api:
  - glyphengine.New
  - glyphengine.Game
  - glyphengine.FixedUpdateGame
  - glyphengine.LateUpdateGame
  - glyphengine.ShutdownGame
  - glyphengine.ResizeGame
  - glyphengine.Scene.TickCount
  - glyphengine.Engine.Run
  - glyphengine.Engine.Destroy
  - glyphengine.Engine.SetCamera
  - glyphengine.Engine.ViewProjection
  - glyphengine.WithTitle
  - glyphengine.WithApplicationName
  - glyphengine.WithWindowSize
  - glyphengine.WithFullscreen
  - glyphengine.WithResizable
  - glyphengine.WithMSAA
  - glyphengine.WithVSync
  - glyphengine.WithInterpolation
  - glyphengine.Engine.Alpha
  - glyphengine.Engine.InterpolatedTransform
  - glyphengine.Scene.ClearInterpolation
  - glyphengine.WithMaxFrames
  - glyphengine.WithTickRate
  - glyphengine.WithMaxCatchUp
  - glyphengine.WithProjection
  - glyphengine.WithScene
example: examples/02-cube
run: task example:02-cube
requires:
  - cgo
  - vulkan-runtime
assets: none
verified: 2026-07-28
---

# Run a game loop with Engine and Game

The engine is a library: **your program owns `main()`**. You implement `Game`,
hand it to `glyphengine.New`, and call `Run`.

```go
package main

import (
	"log"
	"runtime"

	"github.com/go-gl/mathgl/mgl32"

	glyph "github.com/derekmwright/glyphengine"
	"github.com/derekmwright/glyphengine/input"
)

func init() {
	// GLFW must be called from the thread that initialized it.
	runtime.LockOSThread()
}

type game struct {
	camera *glyph.Camera
}

func (g *game) Init(e *glyph.Engine) error {
	cube, err := e.Renderer().CreateCube(1.0)
	if err != nil {
		return err
	}

	ent := e.Spawn()
	e.C.Transform.Set(ent, &glyph.Transform{
		Position: mgl32.Vec3{0, 1, 0},
		Scale:    mgl32.Vec3{1, 1, 1},
	})
	e.C.MeshRef.Set(ent, &glyph.MeshRef{Mesh: cube})

	g.camera = glyph.NewCamera(8)
	g.camera.Target = mgl32.Vec3{0, 1, 0}
	return nil
}

func (g *game) Update(e *glyph.Engine, dt float32) {
	if e.Input().KeyPressed(input.KeyEscape) {
		e.Close()
	}
	g.camera.Update(e.Input())
	g.camera.ResolveCollision(e.Scene, 0, dt)
	e.SetCamera(g.camera.ViewVectors())
}

func main() {
	e, err := glyph.New(&game{},
		glyph.WithTitle("My Game"),
		glyph.WithWindowSize(1280, 720),
		glyph.WithMSAA(4),
	)
	if err != nil {
		log.Fatal(err)
	}
	defer e.Destroy()

	e.Run()
}
```

Full program: `examples/02-cube`.

## `runtime.LockOSThread` is mandatory

GLFW must be called from the thread that initialized it. Without the `init`
above, the Go scheduler will eventually move the goroutine and window or input
calls start failing — usually intermittently, on someone else's machine.

## Two clocks, on purpose

`Run` executes this order every frame:

```
1. poll input
2. Game.Update          frame delta   -- read input, latch edges, mouse look
3. Scene.Tick           tick delta    -- zero or more times
   Game.FixedUpdate     tick delta    -- once per tick
4. TickAnimations       frame delta
5. Game.LateUpdate      frame delta   -- final transforms and poses
6. render
```

| Callback | Rate | Put here |
|---|---|---|
| `Update` | once per frame, real delta | Input, UI, mouse look, anything cosmetic |
| `FixedUpdate` | 0..N per frame, tick delta | Movement, physics, anything a server also runs |
| `LateUpdate` | once per frame, real delta | Camera follow, anything needing final state |

`Update`'s `dt` is a **frame** delta; `FixedUpdate`'s is the tick delta. Only
`Update` and `LateUpdate` are safe places to read input.

`FixedUpdate` and `LateUpdate` are optional interfaces — a game with no physics
just implements `Init` and `Update`.

Unity runs its `FixedUpdate` *before* `Update`, which delays input by up to a
frame. This runs `Update` first, so input sampled on a frame is consumed by
simulation on that same frame.

### Input must be latched, not read on the tick

`FixedUpdate` runs zero, one, or several times per frame. At 144Hz against a
60Hz tick, **about 59% of frames run no tick at all**. So an edge-triggered
query inside `FixedUpdate` is silently dropped on those frames, and fires twice
on a two-tick frame:

```go
func (g *game) FixedUpdate(e *glyph.Engine, dt float32) {
	intent.Jump = e.Input().KeyPressed(input.KeySpace) // WRONG - drops inputs
}
```

Sample per frame, latch the edge, consume it on the tick:

```go
func (g *game) Update(e *glyph.Engine, dt float32) {
	in := e.Input()
	g.intent = glyph.MoveIntent{Yaw: g.camera.Yaw}
	if in.KeyDown(input.KeyW) { g.intent.Forward++ }   // held: overwrite
	if in.KeyPressed(input.KeySpace) { g.jumpQueued = true } // edge: latch
}

func (g *game) FixedUpdate(e *glyph.Engine, dt float32) {
	intent := g.intent
	intent.Jump = g.jumpQueued
	g.jumpQueued = false                               // consume exactly once
	e.MoveCharacter(g.player, intent, dt)
}
```

Working example: `examples/04-first-person`.

### Why movement belongs on the tick

Running `MoveCharacter` on the frame delta makes it frame-rate dependent. Jump
apex measured on the frame clock:

```
   30 fps: 2.2454      1000 fps: 2.1295
```

A 5% difference in how high you jump, decided by your monitor. On the fixed
tick every rate produces `2.1843` exactly — which is also the precondition for
an authoritative server ever agreeing with a client.
`fixedstep_test.go` asserts this exactly, not approximately.

### Rendering between ticks

Simulation runs at 60Hz while rendering runs at the display rate, so without
help a 144Hz monitor draws each simulated position two or three times and then
jumps. `Scene.Tick` records every non-`Static` entity's transform before
simulating, and the draw list blends between the last two ticks by
`Engine.Alpha()` — the leftover accumulator as a fraction of a tick.

Measured on a body moving at constant velocity, drawn at 144fps against a 60Hz
tick, as the coefficient of variation of frame-to-frame drawn motion (0 is
perfectly even):

| | CV |
|---|---|
| interpolation off | 1.188 |
| interpolation on | **0.062** |

It is on by default under `Engine` and off by default on a bare `Scene`, since
a headless server has no use for it. `WithInterpolation(false)` disables it.

**Anything that must line up with the screen has to use the same blend.** A
camera reading the raw `Transform` sits at the last simulated position while
the world renders between ticks, so the view lurches at 60Hz even though
everything else is smooth — most obvious in first person:

```go
func (g *game) LateUpdate(e *glyph.Engine, _ float32) {
	if t, ok := e.InterpolatedTransform(g.player); ok {
		g.camera.Follow(&t)
	}
	e.SetCamera(g.camera.ViewVectors())
}
```

**Teleporting needs `Scene.ClearInterpolation(entity)`**, or the entity is drawn
sliding from where it was to where it now is — across a level, a smear over the
whole map. `RestoreCharacter` already does this, so prediction corrections do
not smear.

Rotation blends along the shortest arc (`LerpAngle`). Plain lerp of Euler
angles spins a character 358° the wrong way every time its facing crosses the
±π wrap.

### Falling behind, and the catch-up budget

When frames take longer than a tick, the accumulator builds a backlog.
`WithMaxCatchUp` bounds how much of it a single frame may repay — 250ms by
default. Past the budget the simulation falls behind instead of trying to
repay a debt it cannot, which is what stops the classic spiral where catch-up
work makes the next frame slower still.

**The budget is a duration, not a tick count, and that distinction is
load-bearing.** An earlier version capped it at two ticks. That is 33ms at
60Hz but only 16ms at 128Hz — less than a single frame at 60fps — so every
frame discarded time it could never make up:

| tick rate | 30 fps | 60 fps | 144 fps |
|---|---|---|---|
| 60 Hz | 100% | 100% | 100% |
| 128 Hz | **48%** | **95%** | 100% |
| 240 Hz | **25%** | **50%** | 90% |

Raising the tick rate for a fast-paced game silently put everyone below roughly
twice that rate into slow motion. With a duration budget all of those are 100%.

How big the budget should be is a **genre decision**:

- **Large budget** — in-game time tracks wall-clock time. After a stall the
  next frame simulates the backlog. Right for fast-paced and single-player
  games, where time passing must be honest. The cost is a burst of movement
  when a stalled client resumes.
- **Small budget** — a struggling client runs in slow motion rather than
  bursting. Often what a server-authoritative game wants: the server is the
  truth and corrects the client anyway, and a smooth slow client beats a
  lurching one. `WithMaxCatchUp(2 * time.Second / 60)` restores the old
  behaviour exactly.

A budget smaller than one tick is raised to one tick, since anything less
would starve the simulation completely.

## Optional lifecycle interfaces

`Game` itself is only `Init` and `Update`. Implement these when you need them;
the engine type-asserts for them.

```go
// Called once after Run returns, before renderer and window teardown, so
// resources are still valid. This is where you save.
func (g *game) Shutdown(e *glyph.Engine) { g.save() }

// Called when the framebuffer size changes, after the renderer is notified.
func (g *game) OnResize(e *glyph.Engine, width, height int) { g.ui.Layout(width, height) }
```

## Engine embeds Scene

`Engine` embeds `*Scene`, so `e.C`, `e.Spawn`, `e.Raycast`, `e.SetTerrain`, and
the rest are reachable directly on the engine. Where an API wants the scene
itself — `Camera.ResolveCollision`, or your own headless systems — pass
`e.Scene`.

That embedding is also the seam: `Scene` has no window or renderer dependency,
so a server or a test can build one with `NewScene()` and never open a window.

## Headless and CI

`WithMaxFrames(n)` stops the loop after `n` rendered frames. Every example
exposes it as `-frames N`:

```
go run ./02-cube -frames 60
```

Combined with a software rasterizer this makes the whole engine runnable on a
GPU-less CI runner. It is also the easiest way to profile a fixed workload.

## Failure modes

- **Black screen, no error.** The renderer is reverse-Z (depth clears to `0.0`,
  compares `CompareOpGreater`). Geometry authored for a conventional `0→1`
  depth range silently fails the depth test. See `render-triangle`.
- **Nothing renders but the window opens.** An entity needs *both* `Transform`
  and `MeshRef` to be drawn. A `Transform` with a zero `Scale` collapses the
  mesh to a point — always set `Scale: mgl32.Vec3{1, 1, 1}`.
- **`Init` returns an error and the process leaks a window.** It does not:
  `New` tears down whatever it built before returning the error. Do not call
  `Destroy` on a nil engine.
