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
  - glyphengine.Engine.Debugf
  - glyphengine.WithTitle
  - glyphengine.WithApplicationName
  - glyphengine.WithWindowSize
  - glyphengine.WithFullscreen
  - glyphengine.WithResizable
  - glyphengine.WithBackgroundWindow
  - glyphengine.WithMSAA
  - glyphengine.WithVSync
  - glyphengine.WithInterpolation
  - glyphengine.Engine.Alpha
  - glyphengine.Engine.InterpolatedTransform
  - glyphengine.Scene.ClearInterpolation
  - glyphengine.WithMaxFrames
  - glyphengine.WithFixedFrameTime
  - glyphengine.WithTickRate
  - glyphengine.WithMaxCatchUp
  - glyphengine.WithProjection
  - glyphengine.WithScene
  - glyphengine.Engine.SetTimeScale
  - glyphengine.Engine.TimeScale
  - glyphengine.Engine.Paused
  - glyphengine.Engine.Elapsed
  - glyphengine.Engine.UnscaledElapsed
  - glyphengine.WithShaders
  - renderer.Renderer.SetShaderParameters
  - renderer.ShaderParameterBytes
  - renderer.Renderer.Shaders
  - glyphengine.Engine.Capabilities
  - renderer.Capabilities
  - renderer.Renderer.Capabilities
  - renderer.ErrCapabilityUnavailable
example: examples/02-cube
run: task example:02-cube
requires:
  - cgo
  - vulkan-runtime
assets: none
verified: 2026-10-02 # the draw list's order and the rejected opaque policy; releasing mid-frame (#153); the capabilities report and who owns which fallback (#160)
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

## What the device granted, and who owns the fallback

`glyph.WithMSAA(4)` above is a request. The device answers it, and the answer
is not always yes. `Engine.Capabilities()` is that answer, fixed once the
engine is built:

```go
caps := e.Capabilities()
log.Printf("%s (%s): %dx MSAA, %gx anisotropy, timestamps=%v",
	caps.GPUName, caps.DriverName, caps.MSAASamples, caps.MaxAnisotropy, caps.GPUTimestamps)
```

| Field | What it says |
|---|---|
| `MSAASamples` | The negotiated count: 1, 2, 4 or 8. Halved until the device supports it for colour **and** depth |
| `MaxAnisotropy` | The sampler anisotropy in use, or `0` when the device has no anisotropic filtering |
| `MultiDrawIndirect`, `DrawIndirectFirstInstance` | Whether `SetMeshRangeBatching` collapses a group into one indirect draw or issues one draw per range |
| `GPUTimestamps` | Whether `GPUTimings` can ever be valid, and whether a `Timed` application pass can be created |
| `PortabilitySubset` | The device implements a subset of Vulkan — MoltenVK, in practice |
| `RayQuery` | Whether this renderer can issue a ray query. `false` on every device today: the extensions are not enabled yet (#159) |
| `GPUName`, `DriverName`, `DriverVersion`, `APIVersion`, `VendorID`, `DeviceID` | Who the device is. `DriverName` is empty on a device without `VK_KHR_driver_properties` |

It is a plain value. Reading it costs nothing, mutating the copy you get
changes nothing, and nothing in the renderer moves it after `New`.

### The split

**The engine owns the fallback when the output is identical, and reports it
here.** Multi-draw indirect is the clearest case: with it a batched group is
one `vkCmdDrawIndexedIndirect`, without it the same ranges are one
`vkCmdDrawIndexed` each, and the two produce the same pixels. There is nothing
for a game to decide, so nothing refuses — but `Capabilities` says which path
the run took, because "the same pixels, slower" is still worth knowing when a
frame budget is missed on one machine and met on another. MSAA and anisotropy
are the same shape: the engine negotiates down, the picture changes only in the
way a lower setting changes it, and the report says what it settled on.

**The game owns the fallback when the technique changes the look, and gets the
report and a typed error to do it.** Screen-space probes instead of ray-traced
ones is not a slower path to the same image; it is a different image, and
choosing between them is a decision about the game's look. The engine will not
make it. Read `Capabilities` once at load, pick the path, and build for it.

A constructor that needs an optional capability wraps
`renderer.ErrCapabilityUnavailable` rather than degrading quietly:

```go
p, err := r.CreateAppPass(desc) // desc.Timed = true
if errors.Is(err, renderer.ErrCapabilityUnavailable) {
	// This device cannot timestamp the graphics queue, so the pass would
	// never appear in GPUTimings().App. Create it unmeasured instead.
	desc.Timed = false
	p, err = r.CreateAppPass(desc)
}
if err != nil {
	return err
}
```

The refusal is deliberate and the reasoning is the split above. A `Timed` pass
asks for a measurement; on a device with no usable timestamps there is no
measurement to give, and a pass created anyway would run forever while its row
stayed missing from the frame breakdown — a game would read that as the effect
being free. Asking the report first, as `examples/24-custom-passes` and
`cmd/apppasscheck` do, is one line and no error handling at all:

```go
timed := r.Capabilities().GPUTimestamps
```

The negotiated sample count and the device identity in this report are also what
the state trace's `config=` field folds, so two runs on different GPUs or across
a driver update cannot diff clean, and a report disagreeing with what the
pipelines were built for fails a test rather than misleading someone -- see
[`state-trace.md`](state-trace.md). What `Timed` and the other optional
capabilities an application pass or target can need do when they are missing is
on [`render-targets.md`](render-targets.md).

## Background captures

Set `GLYPHENGINE_BACKGROUND=1` to launch the window hidden and without input
focus, or pass `glyph.WithBackgroundWindow()` in code. Nothing appears on
screen or in the taskbar; the hidden window still has a surface and a
swapchain of the requested size, so frames render, present and capture as
they do in a visible one. (Not requesting focus was tried first and was not
enough: an unfocused window still opens on top of whatever the user is doing.) Direct window
users can pass `window.WithBackground()`. Fullscreen is rejected in this mode
because GLFW ignores the initial-focus hint for fullscreen windows.

Automated tasks (`smoke`, `validate`, `determinism`, `bench`, and the image
checks) enable this automatically. `task example:<name>` remains interactive.
For an individual water/cloud capture, `09-water` also accepts `-background`:

```sh
go run -C examples ./09-water -background -frames 90 -screenshot sky.png
```

This keeps the window visible and renders normally; click it when you want to
interact. It avoids pulling keyboard input away from another application during
a batch of examples. The platform window manager ultimately controls focus.

## `runtime.LockOSThread` is mandatory

GLFW must be called from the thread that initialized it. Without the `init`
above, the Go scheduler will eventually move the goroutine and window or input
calls start failing — usually intermittently, on someone else's machine.

## Uploading from a worker

GPU resource creation belongs to the frame thread, always. A game that
generates geometry on a worker -- terrain patches, a streamed world -- sends
the finished vertex and index slices over a channel and calls the constructor
in `Update` or `LateUpdate`, on this thread. There is no second queue and no
second thread inside the renderer.

What the worker buys is the *generation*. What the renderer's asynchronous
constructors buy is the *stall*: `CreateIndexedMesh32Async` and friends queue
their copy into the next frame's command buffer instead of submitting one and
waiting for the graphics queue to drain. See
[streaming geometry in](models.md#streaming-geometry-in-while-frames-render)
for the constructors, the readiness ticket and the rule that an unfinished
mesh is simply not drawn.

Releasing from this thread, mid-frame, is also fine, and needs no accounting
for the frames in flight: `DestroyMesh`, `DestroyTexture` and every other
public release stop the resource being drawn at the call and destroy its Vulkan
objects once the frames that were in flight have retired — which is
`maxFramesInFlight` more of the loop above, since each iteration's fence wait
is what lets one countdown tick. There is no device idle in it, and no variant
to choose by allocation or upload state. See
[one contract](models.md#releasing-anything-at-runtime-one-contract).

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

## Replacing an engine shader

`WithShaders` hands the renderer a `renderer.ShaderSet`. Fields left nil fall
back to the embedded shader for that stage, so overriding one pipeline does not
mean supplying all of them:

```go
custom := renderer.DefaultShaders()
custom.LitFrag = toonLitSpv // //go:embed your own .spv

e, err := glyph.New(&game{},
    glyph.WithTitle("Banded"),
    glyph.WithShaders(custom),
)
```

This is a passthrough to `renderer.WithShaders`, and it is the only way to reach
that seam without giving up `Engine` entirely. Before it existed, a game that
wanted its own shading had to call `renderer.New` directly and then reimplement
the frame loop, the fixed timestep, interpolation, the draw-list build and the
environment resolve that `Run` already provides.

**Do not reach for it to recolour the sky.** The example above is the one thing
`SkyFrag` is the wrong tool for: the sky's palette is shared with the fog
distant geometry fades into and with the water's reflection of the dome, so
replacing `sky.frag` alone gives a violet sky over an Earth-blue landscape, and
getting the rest means vendoring `atmosphere.inc` and `lighting.inc` and every
`.frag` that includes them — 430 lines of engine internals with no version
handshake. Those colours are data: `Scene.SetSkyPalette`, see
[environment](environment.md#a-sky-that-is-not-earths). `WithShaders` is for
the sky's *behaviour* — a different scattering model, two suns — not its
colours.

`renderer.ShaderSet` documents what a replacement has to match: the vertex input
layout, descriptor set layout and push-constant ranges the engine's pipelines
declare. A mismatch is a pipeline-creation failure at startup or — worse — a
shader that links and draws nothing, so develop one under `WithValidation`.

For instance LOD, custom `LitLODVert` forwards instance `Tint.w` to fragment
location 5; `LitLODFrag` specialises constant 0 for coverage. Ordinary
`LitVert`, `LitFrag` and `LitInstancedVert` keep their existing interfaces.
`ImpostorVert` and `ImpostorFrag` replace
the far billboard stages independently. See [LOD instancing](lod-instancing.md)
for the attributes, reserved push data and measured transition checks.

Authoring is unchanged: write GLSL, run `task shaders`, commit the `.spv`, and
`go:embed` it. There is no hot reload.

`Renderer.Shaders()` reports what is actually in effect, defaults filled in, for
a harness that wants to check rather than assume.

### Shadow resources in custom sky shaders

Regular `SkyFrag` now binds the same shadow/light set as `LitFrag` at **set 1**.
No volumetric local light is required. Existing set 0 and push-constant offsets
are unchanged, and stock sky shaders render as before.

| Set | Binding | Fragment resource |
|---|---|---|
| 0 | 0 | Combined sampler for the half-resolution cloud result |
| 0 | 1 | Shared environment UBO (same buffer as set 1 binding 0) |
| 1 | 0 | `ShadowData` UBO, beginning with `mat4 cascadeVP[2]` |
| 1 | 1 | `sampler2DArrayShadow`, two directional cascades |
| 1 | 2 | Point-shadow depth sampler (`samplerCube`, manual comparison) |
| 1 | 3–5 | Clustered lights, grid, and light-index storage buffers; see `shaders/include/lights.inc` |
| 1 | 6 | Application-owned std140 uniform block, 4096 bytes, vertex and fragment stages |

Minimal declarations and a lookup, also exercised by `cmd/skyshadowcheck`:

```glsl
layout(set=1, binding=0) uniform ShadowData { mat4 cascadeVP[2]; } shadow;
layout(set=1, binding=1) uniform sampler2DArrayShadow shadowMap;
vec3 p = (shadow.cascadeVP[cascade] * vec4(worldPoint, 1)).xyz;
float visible = texture(shadowMap, vec4(p.xy * 0.5 + 0.5, cascade, p.z - bias));
```

Directional shadow depth is conventional 0..1, despite the main camera using
reverse-Z. Select a cascade containing the sample (including its Z range),
treat points outside both as lit, and leave a margin for filtering near the
XY edges. Bias is in normalized shadow depth; custom shaders should account
for configured depth coverage as described in [environment](environment.md#directional-shadow-coverage).

The maps are cleared and rendered before opaque surfaces and the sky. The
regular sky follows opaque geometry and clouds, at camera depth zero; stars,
celestials and additive local-light sky scattering follow it. With shadows
disabled, the maps are still cleared to 1 and available to sample. The engine
may supply zero cascade matrices then: a zero matrix means no coverage, so
treat it as fully lit. Never sample using stale matrices from an earlier frame.
The renderer owns these resources through resize and shutdown; do not cache
their Vulkan handles. A game supplies its scattering model and sample points.
`task skyshadows` checks matched surface/sky visibility with shadows on, off,
on after resize, and off again under Vulkan validation.
On RX 7900 XTX the paired diagnostic reads 89/255 in both passes with shadows,
231/255 without, before and after resizing 400x300 to 640x480.

### Application data for custom shaders

`e.Renderer().SetShaderParameters(data)` supplies a fixed 4096-byte application
block without repurposing the sky palette or rebuilding pipelines. The renderer
copies the slice immediately, clears the unused tail, and uploads to the current
frame slot only after its fence completes. Sky and lit shaders read the same
frame's data. Call from `Init`, `Update` or `LateUpdate` on the frame thread;
renderer-only consumers call before `DrawFrame`. It is not a concurrent API.

```go
// Two std140 vec4s. Pack explicitly; a Go struct is not a std140 layout.
data := make([]byte, 32)
values := [8]float32{cameraX, cameraY, cameraZ, radius, density, scaleHeight, 0, 1}
for i, value := range values {
    binary.LittleEndian.PutUint32(data[i*4:], math.Float32bits(value))
}
if err := e.Renderer().SetShaderParameters(data); err != nil { return err }
```

```glsl
// SkyFrag, LitFrag, LitMaterialFrag, TerrainFrag, GrassFrag, WaterFrag:
layout(set=1, binding=6, std140) uniform ApplicationParameters {
    vec4 cameraRadius;
    vec4 scattering;
} app;
// Skinned lit pipelines use set=2, binding=6 instead (set 1 holds joints).
```

The block is available in vertex and fragment stages of pipelines that bind the
shadow/light set, including both sky passes. It is not bound to clouds, UI,
particles, postprocessing or shadow-caster pipelines. Custom declarations may
use any prefix up to 4096 bytes. Use std140 alignment (including 16-byte array
strides), column-major GLSL matrices and little-endian scalars. The setter
checks capacity and a 16-byte padded length; it cannot verify a shader's field
semantics. Invalid input leaves the preceding data intact. Run custom layouts
under Vulkan validation.

The initial block is zero; nil clears it. Data persists until replaced and
survives resize/recreation. The renderer owns two frame copies in its existing
mapped uniform allocations (8192 extra bytes plus device alignment padding)
and releases them with those allocations. No new per-update GPU allocation or
descriptor replacement occurs. Existing environment offsets, push constants
and bindings 0–5 are unchanged; stock shaders ignore binding 6. Unsupported
uniform range/descriptor limits produce a startup error naming the requirement.

`task shaderparameters` changes values every frame in paired custom sky/lit
shaders, uses the last vec4 of the block, replaces a full block with a short
one, clears it, and resizes with frames in flight. Unit tests check immediate
copy ownership, validation errors, zero padding and isolation of frame slots.
With the upload deliberately removed, the first GPU probe is 0/255 instead of
137/255 and the check fails. The state trace hashes the uploaded slot as
`shaderparams`, so custom data also participates in determinism diagnostics.

## Pausing, and slow motion

```go
e.SetTimeScale(0)   // paused
e.SetTimeScale(0.25) // quarter speed
e.SetTimeScale(1)   // back to real time
```

**A game cannot pause itself.** Returning early from `FixedUpdate` stops the
game's own simulation and none of the engine's: `Scene.Tick` is called before
`FixedUpdate` and keeps integrating rigid bodies, moving character controllers
and taking the transform snapshots interpolation reads, while animation advances
separately again on the frame delta. A game with no physics and no skinned
meshes gets away with it by luck, and the moment it gains either, a crate keeps
sliding behind the menu with nothing to say so.

What stops at scale 0: `Scene.Tick`, `FixedUpdate`, animation, and the elapsed
clock the shaders read for grass wind, water waves and the cloud march.

What keeps running: `Update`, `LateUpdate`, and rendering — everything a paused
game needs to still be a program. They get the **real** frame delta, not the
scaled one, so a menu animates and a camera moves at full speed over a stopped
world. A game that wants its camera slowed too multiplies by `TimeScale` itself.

### The two clocks

```go
e.Elapsed()          // simulation time: SCALED, stops at scale 0
e.UnscaledElapsed()  // wall time since Run started, whatever the scale
```

`Elapsed` is the clock the shaders animate on, and it is scaled — pausing the
game has to stop the grass moving. It was documented as wall-clock time until
2026-09-26 and was never that; a fade written against that description works in
every test and never in a paused menu, which is where fades mostly live.

`UnscaledElapsed` is for anything that must keep moving while the world is
stopped: a modal fading in, a spinner on a loading screen, a menu sliding.

```go
tree.SetTime(e.UnscaledElapsed())   // ui/yamlui transitions
```

Both advance in `advanceSimulation`, with the ticks, so neither can drift past
the other when a frame is skipped, and under `WithFixedFrameTime` both advance
by the fixed delta — so a transition lands on the same frame in every run and a
capture of it is comparable. `task transition` is the gate: it renders a yamlui
dialog fading in at `SetTimeScale(0)`, and driving it off `Elapsed` instead
leaves the dialog missing from every frame.

**Scaling goes into the accumulator, never into the tick delta.** A fixed
timestep is only fixed if `tickDt` never moves; slow motion that shortened the
step would change how the integrator behaves and take determinism with it. Half
speed is half as many ticks of the same size.

Negative values clamp to zero. The integrator is not reversible, so the useful
reading of a negative scale is "stopped".

`task determinism` gates it: `09-water` captured 60 frames apart after a pause
must be byte-identical. That scene is the one to use because the things moving
in it run off different clocks — waves off the elapsed clock, the sun off
`Scene.Tick` — so the captures match only if both stopped. Deleting either
scaling makes it fail, which was checked rather than assumed.

## Screens, menus and swapping scenes

The engine ships no scene manager, no screen stack and no transition system.
That is a deliberate omission rather than a missing feature, and the reason is
worth knowing before you go looking for one: **swapping scenes is an
assignment.**

```go
e.Scene = g.menuScene   // that is the whole of it
```

`Engine` embeds `*Scene` as an exported field, and GPU resources — meshes,
textures, materials, pipelines — live on the `Renderer` rather than the `Scene`,
so a swap re-uploads nothing and leaks nothing. Build both scenes up front and a
screen change costs one pointer write.

A manager on top of that would add no capability, only structure, and the shape
of a game's screens is the game's business. `examples/20-screens` is the whole
pattern — a main menu, a world, and a pause menu over it — in about ninety lines
of game code, using three primitives that *are* the engine's business:

| | why the engine has to provide it |
| --- | --- |
| `e.Scene = other` | already there; `Scene` is an exported embedded field |
| `e.SetTimeScale(0)` | a game cannot stop `Scene.Tick`; see above |
| `ui.UIManager` traversal | the toolkit owns the widgets |

### Pausing to a menu

```go
func (g *game) pause(e *glyph.Engine) {
    g.screen = screenPaused
    e.SetTimeScale(0)     // the world stops
    g.showPauseMenu(e)    // Update and rendering carry on, so the menu works
}
```

The camera still moves and the menu still animates, because those run in
`Update` and `LateUpdate` on the real frame delta. What stops is everything that
would make the world move on without the player.

### What to watch for

- **Clear the widget lists when a menu is replaced.** `ClearNavigables` and
  `ClearClickables` exist for this. A rebuilt menu that keeps its old
  registrations has a highlight pointing into a screen that is gone, and clicks
  landing on widgets nobody can see.
- **Do not call `Button.UpdateHover` on a registered widget.** The manager
  drives the highlight from both the keyboard and the pointer; `UpdateHover`
  recomputes it from the pointer alone, so the two fight and the arrow keys
  appear to do nothing.
- **A scene keeps its own environment.** `SetTimeOfDay` on a menu scene does not
  touch the world's, which is how `20-screens` gets a dusk menu over a midday
  world.

### What is genuinely missing

Renderer-side world state has no teardown: `InitGrass` and `InitParticles` have
no counterpart, so swapping from a grassy world to a menu scene leaves the grass
drawing. Clear it game-side for now.

## The draw list and its order

Every frame the engine walks the entities with `Transform` and `MeshRef`, culls
them, and hands the renderer one list. The order of that list is a contract, and
it has two halves that are decided for opposite reasons:

- **Opaque draws** are grouped by `RenderObject.SortKey` — pipeline variant
  first, then the resource bound at set 0 — so the recorder does as few pipeline
  binds and descriptor switches as possible. Inside a group the order is
  `SortID`, which is the entity id, which is spawn order.
- **Blended draws** come after all of them, back to front by
  `RenderObject.ViewDepth`. That ordering *is* the image: blending is not
  commutative.

The comparison is a total order — `SortID` is the final tiebreak on both halves —
and that is what makes the recorded sequence a function of the scene rather than
of the Go map walk the list arrives in. `scene-entities.md` has the detail, and
`task determinism` gates it.

### Opaque order inside a group was measured, and left alone

Inside an opaque group nothing is ordered by distance, so a near draw and the
draw hidden behind it are recorded in spawn order and the hidden one's fragments
reach an expensive fragment shader before the depth buffer can reject them.
Issue #152 asked whether ordering nearer first inside each group pays. It was
built, measured against a rule written before the run, and removed.

The rule, written first:

> Ship the option if front-to-back cuts GPU opaque time on the overlap arm by
> more than the within-mode scatter, while the no-overlap control moves by less
> than that scatter and the CPU sort time rises by less than the GPU time saved.
> Otherwise remove the option and record the numbers that said no.

The scene is `examples/28-overdraw`, which stayed in the tree when the policy did
not: a 32x32 grid of distinct heightmap patches tiled edge to edge, 2048 triangles
and 6 m of relief each, all sharing ONE five-map PBR material so the whole field
is a single `SortKey` group, lit by 196 clustered point lights of range 55, clouds
off, spawned FARTHEST ROW FIRST so `SortID` order is the worst case the default
can be handed — which makes the saving below an upper bound rather than a typical
figure. Two arms: a grazing eye 2.4 m up looking almost along the field, where the
measured screen-space depth complexity is 3.28, and the same field seen from
overhead through a narrow 12-degree lens, where it is 1.02. Each arm measures its
own depth complexity from the patch bounds and the frame's own view-projection,
and fails if it has drifted, so neither arm can quietly stop being the arm it is
named after. Three trials per cell, interleaved grouped / front-to-back / grouped
/ front-to-back with every sample kept, 200 frames each under
`GLYPHENGINE_FIXED_FRAME_TIME=16.667ms`, nothing else on the GPU. AMD Radeon
RX 7900 XTX.

`task bench -- -scene overdraw` is the two arms with one order, three trials,
interleaved, and `-extra "-width 3840 -height 2160"` is the other resolution.

`gpu_opaque`, in milliseconds:

| resolution | arm | depth complexity | grouped | front to back | mean change | within-mode scatter |
|---|---|---|---|---|---|---|
| 1280x720 | overlap | 3.28 | 1.781 / 1.794 / 1.855 | 1.589 / 1.703 / 1.809 | **-0.110** | 0.074 / 0.220 |
| 1280x720 | control | 1.02 | 7.241 / 7.219 / 7.251 | 8.368 / 8.359 / 8.221 | **+1.079** | 0.032 / 0.147 |
| 3840x2160 | overlap | 3.28 | 7.114 / 6.975 / 7.102 | 6.332 / 6.296 / 6.101 | **-0.821** | 0.139 / 0.231 |
| 3840x2160 | control | 1.02 | 9.451 / 9.193 / 9.308 | 10.255 / 10.365 / 10.349 | **+1.006** | 0.258 / 0.110 |

`gpu_total` moves with it — 1.939 to 1.810 and 7.600 to 6.665 on the overlap
arm, 7.368 to 8.431 and 9.715 to 10.706 on the control — so the opaque column is
not hiding a cost somewhere else. `cpu_drawsort`, the phase timer around the
sort, rose from 0.141 to 0.186 ms at 720p and from 0.133 to 0.188 ms at 4K on
795 draws: real, and an order of magnitude below the GPU time at stake. `cpu_total`
sat at 16.4 to 16.7 ms in all 24 samples, which is vsync, not the policy. Draw
counts were identical within each arm (795 and 1025), so no cell was rendering
less than another.

**The rule's verdict: do not ship.** Clause one passes at 4K and squeaks past at
720p. Clause three passes everywhere. Clause two fails at both resolutions by
four to thirty-four times the scatter: ordering by distance costs about one
millisecond of the opaque pass on a field where nothing is hidden at all, which
is the opposite of free.

The cause is screen-space locality, and a fourth run nailed it. Spawn the same
overlap field NEAREST row first, so `SortKey` order is already front to back and
row-major, and grouped beats the policy outright — 5.965 / 6.011 / 5.874 against
6.160 / 6.363 / 6.313 at 3840x2160. The best opaque time measured anywhere in
this scene, 5.950 ms, belongs to the DEFAULT policy over a spatially coherent
spawn order, and front-to-back never reaches it. Distance order is not
screen-space order: from the overhead camera, sorting by eye distance turns a
row-major submission into concentric rings, and every ring touches most of the
framebuffer's tiles. What early-Z gives back on the hidden fragments, the
scramble spends on cache and ROP throughput.

So the engine keeps one order and the option does not exist. What a game can do
about overdraw today is what it could do before: spawn or stream its world in an
order that is spatially coherent AND roughly front to back, because `SortID` is
spawn order and that is already the knob — worth 1.1 ms of 7.1 on this scene, more
than the rejected policy was worth.

To redo this measurement, the policy itself is two lines: in `sortDraws`, give an
opaque draw `o.depth = -d.ViewDepth(eye)` instead of leaving it at zero. The
comparison already reads "farther first", so a negated depth reads as "nearer
first" for opaque draws and leaves the blended tail alone, and `drawOrder` stays
24 bytes.

### A depth prepass is still worth asking about, and the baseline is kept

The overlap arm says there is recoverable work: 0.82 ms of a 7.06 ms opaque pass
at 4K, found by draw-level rejection alone, on a field with 3.28x depth
complexity — and since that figure counts one DRAW hiding another, a per-pixel
mechanism would see more. A prepass rejects per pixel rather than per draw, so it
would find more of it, and — this is the part the numbers above argue for — it
would not pay the locality penalty, because it does not reorder the main pass.
Against that it costs a second geometry pass over 1.6 M triangles and has to get
alpha test, displacement, reverse-Z and MSAA depth matching right.

`examples/28-overdraw` and `task bench -- -scene overdraw` are kept as the
baseline for exactly that, and for hierarchical-Z occlusion culling after it. Both
are bets that what they remove outweighs what they cost unconditionally, and that
is a bet only a pair of arms can settle: the overlap arm says how much there is to
win, and the control says what every scene without hidden work is being charged
for it. The control is not a formality — it is what rejected the sort, on numbers
the overlap arm alone would have approved.

One result from that same scene is worth carrying into either design. Spawning the
field nearest row first, so submission order is both front to back and
row-major, takes the opaque pass from 7.06 ms to 5.95 ms at 3840x2160 with no
engine change at all (`-spawn fronttoback`). Any mechanism proposed here has to
beat 5.95, not 7.06.

## Headless and CI

`WithMaxFrames(n)` stops the loop after `n` frames. Every example exposes it as
`-frames N`:

```
go run ./02-cube -frames 60
```

Combined with a software rasterizer this makes the whole engine runnable on a
GPU-less CI runner. It is also the easiest way to profile a fixed workload.

## Repeatable renders

`-frames N` fixes how *many* frames run, not what they show. The clock is still
wall-clock, so wind, clouds, water, particle spawns, animation and the day-night
cycle land somewhere slightly different on every run. Two captures of the same
build differ, and the difference is not small: measured on `08-grass`, two
identical 150-frame runs came out RMS 0.009–0.05 apart, which is the same order
as some of the changes worth measuring.

`WithFixedFrameTime(d)` advances the clock by exactly `d` per frame instead, so
a run becomes a function of its frame count. The environment forces it on a
binary that never asked:

```
GLYPHENGINE_FIXED_FRAME_TIME=16.667ms go run ./08-grass -frames 90 -screenshot a.png
GLYPHENGINE_FIXED_FRAME_TIME=16.667ms go run ./08-grass -frames 90 -screenshot b.png
# a.png and b.png are byte-identical
```

It does not pace the loop — the frame still takes as long as it takes, and the
CPU and GPU timers still report real time. It only changes what the simulation
is told.

Reach for it whenever a visual difference is the thing being measured: A/B
screenshots, bisecting a rendering artifact, or counting a sparkle that appears
a different number of times each run. Without it, "it looks different now"
cannot be separated from "the wind moved".

`task determinism` is the gate: it captures each animated example twice and
fails if the two differ, plus a control run with the real clock that must
differ, so the check cannot pass vacuously.

Particle spawn jitter is pinned alongside the clock, since `math/rand`'s global
source is reseeded at every process start.

### What a fixed clock does not fix by itself

Two runs agreeing only says the machine behaved the same way twice. The clock
is one input; the window system is another, and it arrives uninvited. An
out-of-date acquire — a window being shown, moved between monitors, a
compositor mode change — used to cost the frame outright, so the loop simulated
one more step than it drew and everything that spans frames sat behind for the
rest of the run. A swapchain rebuild threw away the cloud layer's temporal
history whether or not the size had changed. Neither is under the run's
control, and both moved the picture: a rebuild forced on the last frame of
`08-grass` moved 27.95 % of the pixels.

Both are fixed (see `docs/agents/state-trace.md`), and `task determinism` now
forces them on a named frame rather than waiting for one. Two things still
change a capture and are meant to:

- **A rebuild that changed the extent.** The frame in flight was built for the
  old size, so it is dropped; a resize changes the picture anyway.
- **A minimized window.** The loop keeps simulating and stops rendering, by
  design, so the frame that eventually gets drawn is further along than it
  would otherwise have been. Do not capture through one. Every simulation
  clock keeps running while it is down -- the ticks and the elapsed clock the
  shaders read advance together in `advanceSimulation`, precisely so that a
  `continue` in the frame loop cannot move one without the other. They used to
  separate: a minimized game kept ticking while its waves stood still.

When a capture does differ and it should not, set `GLYPHENGINE_STATE_TRACE` on
both runs and diff the two files: one line per loop iteration, and the first
one that differs names the frame and the subsystem.

## Debug text

`Engine.Debugf` puts a line in the top-left corner for one frame:

```go
func (g *game) Update(e *glyph.Engine, dt float32) {
    e.Debugf("pos  %.1f %.1f %.1f", p[0], p[1], p[2])
    e.Debugf("ToD  %.4f", e.Scene.TimeOfDay())
}
```

Queued, not retained — the lines are cleared every frame, so call it from
`Update` or `LateUpdate` for a value that changes. They render in call order,
top to bottom, in a channel of their own, so they neither disturb nor are
disturbed by `SetMSDFOverlays`.

The font is Go Mono, generated on first use rather than loaded, so this needs no
asset and no setup and a game that never calls it pays nothing. Generation costs
a few hundred milliseconds the first time; do not call `Debugf` in a shipping
build if that matters. Monospaced on purpose — a value changing width between
frames would shift every line after it.

It exists because a number you can *watch move* answers questions a screenshot
does not. `examples/08-grass` shows time of day and sun elevation this way,
which is how the sunset glow's tail was pinned down: "the glow lingers too long"
became "the glow is still warm at sun elevation -0.14", which is actionable.

This is for game state. Per-pass CPU and GPU timings are a separate facility and
print rather than draw — see [profiling](profiling.md).

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
