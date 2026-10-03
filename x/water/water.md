---
id: x-water
title: The underwater volume — absorption, the water's own colour, and sun shafts
summary: >
  Add what a body of water does to the light crossing it, between the eye and
  whatever the eye can see, once the camera goes under the engine's surface.
capability: water
status: experimental
since: v0.1.0
api:
  - water.Options
  - water.DefaultOptions
  - water.Water
  - water.New
  - water.Water.Options
  - water.Water.SetEnabled
  - water.Water.Enabled
  - water.Water.Update
  - water.Water.Destroy
example: examples/09-water
run: task example:09-water -- -water
requires:
  - cgo
  - vulkan-runtime
assets: procedural
verified: 2026-10-03 # the parameter blocks moved from the 128 application push bytes to each pass's own uniform block at set 2 binding 12 (AppPassDesc.Params, #170), byte layout and recorded sweep unchanged; new package; depth sweep, balance and allocation gates all broken and confirmed failing; the copied jitter replaced by the engine's volumetric_common.inc (#169)
---

# The underwater volume

This is an `x` package, not an engine one. The engine owns the water *surface*
([`water.md`](../../docs/agents/water.md)): a Gerstner mesh built over a
heightmap that refracts the bed, reflects the sky by Fresnel and absorbs colour
through the still depth baked into every vertex. That surface is correct and it
is complete — from above. None of it is visible from underneath, and the engine
has nothing to say about the water you are standing *in*.

This package is that half. Three application passes on the engine's public
seams; no engine change was needed to build it.

```go
import (
	glyph "github.com/derekmwright/glyphengine"
	xwater "github.com/derekmwright/glyphengine/x/water"
)

func (g *game) Init(e *glyph.Engine) error {
	// ... build the heightmap, the terrain and the engine's water surface ...
	opts := glyph.DefaultWaterOptions(waterLevel)
	surface, err := e.CreateWaterMesh(hm, opts)
	// ... spawn it ...

	// The SAME level. The engine draws the surface from its copy and this
	// package decides what is submerged from its own; if they disagree you get
	// a volume that starts in the wrong place.
	g.underwater, err = xwater.New(e.Renderer(), xwater.DefaultOptions(waterLevel))
	if err != nil {
		return err
	}
	g.underwater.SetEnabled(true)
	return nil
}

func (g *game) LateUpdate(e *glyph.Engine, _ float32) {
	eye, center, up := g.camera.ViewVectors()
	e.SetCamera(eye, center, up)

	// After the camera has moved. The package decides from the eye whether it
	// does anything at all, so a stale eye leaves the volume on for a frame
	// after surfacing.
	st := e.Scene.Environment()
	if err := g.underwater.Update(e.ViewProjection().Inv(), eye, st.SunDir, st.SunColor); err != nil {
		log.Fatalf("x/water update: %v", err)
	}
}
```

`task example:09-water -- -water` is the same thing in a scene you can walk
around. Walk into the lake with the flag and without it: the difference is the
whole package.

## What it does, and where

Three passes at `StageBeforeBloom`, in creation order:

| Pass | Target | What it does |
|---|---|---|
| `water scattering` | its own RGBA16F, `ScatterScale` of the window | Marches the view ray and sums single-scattered sunlight. Alpha carries the scene depth the sample was taken at. |
| `water composite` | its own RGBA16F, full size | Reads scene colour and depth. Absorbs the scene by its water path length, fills in `BodyColor`, and reconstructs the scattering with a depth-aware bilateral filter. |
| `water present` | the HDR scene, `Load: true` | Copies the composite over it. |

`StageBeforeBloom` is the first stage where the HDR scene is complete *including
the engine's water surface*, and the surface is one of the things the volume
absorbs. `StageAfterScene` runs before water copies the scene, so the surface
would be missing from what gets absorbed.

The present pass exists because the composite reads scene colour, and a pass may
not sample the destination it writes. One fullscreen triangle is the price of
replacing an image you had to read first.

**Above the surface it contributes nothing at all.** `SetEnabled` records the
game's intent; `Update` decides every frame whether that intent can do anything,
and with the eye at or above `Level` the answer is no and all three passes are
switched off. A frame rendered with the package created but out of the water is
identical to one rendered without the package existing — `task xwater` checks
both halves of that pixel for pixel. The submersion test lives here rather than
in every game, which is the part of this that is reusable.

Two more early-outs are hoisted out of the shader onto the CPU: past
`ScatterMaxDepth`, and with the sun below `ScatterDaylightOnset`, the scattering
term is exactly zero, so the march is skipped rather than summing nothing. The
pass still runs and still clears its target — a *disabled* pass would leave last
frame's shafts in the image for the composite to reconstruct.

## Options

Every number the look depends on is a field, and every default is the value it
was read out of. `TestDefaultsAreTheSourceConstants` pins all of them with the
citation beside each, and fails if a field is added without one.

| Field | Default | What it controls |
|---|---|---|
| `Level` | — | World Y of the still surface. Must match the engine's `WaterOptions.Level`. |
| `Clarity` | 2.0 | Divides `Absorption` and `BodyDepthFalloff`. One number for view distance, seabed light and the depth the body stops brightening, because they are one physical process. |
| `Absorption` | 0.20 / 0.075 / 0.035 | Per world unit at `Clarity` 1. Red first, which is why deep water is blue. |
| `BodyColor` | 0.006 / 0.035 / 0.047 | The water's own radiance, filling in what absorption took. |
| `BodyNightFloor` | 0.01 | The share of it that survives with no sun, so a night dive is dark rather than black. |
| `BodyDepthFalloff` | 0.025 | How fast the daylit part dies with the *camera's* depth, per world unit. |
| `BodyDaylightOnset` / `Full` | -0.12 / 0.25 | Sun elevation sine over which the body's daylit term ramps. Onset is negative: water is still faintly lit after sunset. |
| `ScatterColor` | 0.0002 / 0.0006 / 0.0008 | Scattering coefficient, multiplied by the sun's colour. |
| `ScatterSamples` | 6 | Points along the ray. Stratified and jittered per pixel; raising it trades grain for cost without changing the energy. |
| `ScatterSpan` | 60 | Bounds the integration path in world units. |
| `ScatterMaxDepth` | 80 | Camera depth past which the shafts switch off. |
| `ScatterPhase` | 0.65 | Henyey–Greenstein asymmetry. Positive is what brightens the shafts when you look toward the refracted sun. |
| `ScatterDaylightOnset` / `Full` | 0.08 / 0.4 | The shafts' own sun ramp — higher than the body's, because a visible shaft needs a higher sun than a faintly lit volume. |
| `RefractiveIndex` | 1.333 | Bends the sunlight at the surface: it tilts the shafts off the sun's own direction and lengthens the path to each sample. |
| `SunPathFloor` | 0.15 | Clamps the cosine between the refracted sun and up. Without it a horizon sun divides by nearly nothing and the volume goes black. |
| `ScatterScale` | 0.5 | The scattering target's size relative to the swapchain. |
| `DepthTolerance` | 0.08 | Relative scene-depth difference at which a neighbour stops counting during reconstruction. Relative because reverse-Z depth is not linear in distance. |

The zero value renders nothing and `New` says which field. Start from
`DefaultOptions`.

## What this package deliberately does not own

The source these passes came from is one game's whole atmosphere: a planetary
sky, a Rayleigh/Mie air integral, an analytic spherical ocean surface, a wave
field, a Jacobian caustic solver and a cached caustic atlas. Deciding where
water ends was most of the work here, so the reasoning is recorded rather than
left to be re-derived.

### The refracted sunlight field, and the caustics built on it — a sibling

The wave surface, its curvature, the Jacobian that turns a patch of incident
sunlight into an intensity, and the world-anchored atlas that caches it. Four
reasons it is not here:

1. **Who reads it.** In the source, the field's principal consumer is
   `seabedCaustics`, called from the game's **terrain fragment shader** to light
   the seabed. Only secondarily does the scattering integral sample it. A water
   package that owned the field would be writing a light field for a shader it
   does not own, and would have to dictate the game's terrain shading in order
   to deliver it. That is not a seam a second game can take.
2. **What it is.** It is a property of *sunlight crossing a wavy interface*,
   shared by the seabed and the volume. The source's own file layout says so:
   `waterlight.glsl` is a separate include, and its first line calls it "shared
   refracted sunlight field". Water samples it; water is not it.
3. **What it costs.** Its own compute dispatch and two wave targets, four more
   application passes, a 4096×2048 atlas, a 294,912-triangle projection mesh and
   a world-anchoring policy with a snap step and a drift threshold. It occupies
   three of the engine's four application texture slots. All of that belongs to
   the field's lifetime, not to compositing water.
4. **It is optional in the source.** `waterLightFocus` returns exactly 1.0
   wherever the atlas is inactive or the receiver is outside its coverage
   radius, and a feature flag switches the shafts off entirely. So the focusing
   factor of 1 that every sample in this package's integral carries is a path
   the source ships and runs, not one invented here.

The consequence is visible and worth stating plainly: **the shafts here are
smooth.** They dim and redden with depth and with distance from the surface, and
they have no caustic texture. The insertion point for one is a single multiply
inside the loop in `water-scatter.frag`.

### The air, and the surface shading that reflects it — a sibling

The Rayleigh/Mie integral, the sun transmission LUT, the sky seen through
Snell's window, the Fresnel surface, the glint lobe and the shoreline foam. All
of them integrate the atmosphere for their reflected or transmitted sky, and the
surface shading is a *replacement* for the engine's water surface rather than
something composited over it. That is an `x/atmosphere`, and ADR 0012's step 3
(the environment carve) is the seam it wants first.

### Left for whoever builds those

- A seam for a focusing field. This package would want to sample one, and the
  mapping from a world position to an atlas lookup is knowledge only the field's
  owner has, so the seam has to be designed with the field rather than guessed
  at ahead of it.
- The analytic ocean surface, so a game can replace the engine's Gerstner mesh
  with a spherical one. Its reflection needs the air.
- The underwater *window* — what you see looking up through the surface from
  below, including the dark reflective underside past Snell's angle. It is
  surface shading and it needs the sky.

## Where this came from, and which of its speed work came with it

The source is a consuming game's `atmosphere` package, read-only. These are the
files this port is based on and the state they were in, by modification time on
2026-10-02:

| File | Modified | Used for |
|---|---|---|
| `water_passes.go` | 2026-09-24 09:00 | The pass wiring: half-resolution scatter target, additive composite, which stage, `Timed`, created disabled |
| `water-scatter.frag` | 14:31 | The integral's pass shape and its depth reconstruction into alpha |
| `water-composite.frag` | 17:43 | The four-tap bilateral reconstruction, verbatim in structure |
| `ocean.glsl` | 15:54 | `underwaterShafts` and `underwaterColor`: every scattering and absorption constant |
| `waterlight.glsl` | 16:18 | Read for the boundary: the focusing field, left out |
| `water-wave.glsl` | 15:39 | Read for the boundary: the wave field, left out |
| `wave-spectrum.glsl` | 15:47 | Read for the boundary: the wave spectrum, left out |
| `wave_cache.go`, `wave-cache.comp` | 14:43 | Read for the boundary: the wave cache dispatch, left out |
| `caustic_cache.go`, `caustic-cache.{vert,frag}`, `caustic-resolve.frag`, `caustic-filter.frag`, `caustic-depths.glsl` | 15:38–17:36 | Read for the boundary: the caustic atlas, left out |
| `parameters.glsl` | 16:18 | The source's own uniform block, deliberately not reproduced |
| `common.glsl` | 17:48 | Read for the boundary: the air integral and its shadow sampling, left out |
| `air_passes.go`, `air-*.frag`, `air-*.glsl` | 14:33–14:55 | Read for the boundary, and for the pass shape this package reuses |
| `atmosphere.go`, `atmosphere_test.go` | 15:39 | How the passes are fed per frame |

### Carried over

- **Half resolution plus a depth-aware reconstruction, and no temporal
  history.** The integral is the expensive part and it is low-frequency, so it
  runs small and is put back against full-resolution depth. No history means no
  ghosting and, more usefully here, captures that repeat byte for byte.
- **The four-tap footprint written out rather than looped.** The source's own
  note is that expressing it directly leaves the compiler no nested per-pixel
  loop to keep. `reconstruct` in `water-composite.frag` is that shape.
- **Stratified samples with a stable per-pixel jitter.** Fixed midpoints line up
  into visible sample-plane bands; the jitter trades those for fine grain the
  reconstruction then smooths. It is a function of the pixel and nothing else,
  so a still camera does not shimmer.
- **The scatter/composite/present pass shape**, which the source arrived at for
  its air passes: compose into an own target and copy it over the HDR scene,
  because a pass may not sample what it writes.
- **Early-outs hoisted to the CPU.** The source returns `vec3(0)` from the whole
  integral past 80 units of depth and below the daylight onset. Here those two
  tests are per frame rather than per pixel, and the march is skipped entirely
  rather than summing nothing; the pass still runs so it still clears.
- **One number for the medium.** `Clarity` divides absorption and the body's
  depth falloff together, which is the source's "one shared setting controls
  view absorption, seabed light, and cutoff distance".

### Deliberately left behind

- **The wave cache.** A compute dispatch bakes the 48-component Fourier spectrum
  into two 512² repeat-wrapped textures once, so a wave lookup is two bilinear
  reads instead of a 48-term sum with a Hessian. It is a real and substantial
  speed-up — and it is a speed-up *for the wave field*, which reaches water only
  through the focusing term this package does not own. Taking the cache without
  the field it feeds would be taking an optimisation with nothing to optimise.
- **The caustic atlas and its filter chain.** Twelve depth slices summed by
  forward-projecting a 384² grid, resolved 4096×2048 → 2048×1024 by a bilinear
  2×2 integration, then blurred by two separable passes that get a 9×9 Gaussian
  footprint out of five bilinear reads per axis — and a two-triangle shortcut
  for the zero-depth slice where the refraction map is the identity, replacing
  294,912 microtriangles that would all write 1.0. Excellent work, and all of it
  belongs to the field, for the reasons above.
- **The source's twelve-sample integral.** Its comment says the cached focusing
  is what makes the extra samples affordable, and that they buy grain reduction.
  With no focusing term there is no grain for them to reduce, so this package
  takes the uncached six. `ScatterSamples` is a field; a game that plugs a
  focusing field in later raises it.
- **Everything the air pays for**: the sun-transmission lookup table that caches
  the spherical sun-path integral by altitude and zenith angle, the single
  hardware-filtered cascade tap for haze instead of eight surface PCF taps, the
  shadow supersampling that scales with interval length, and the near-cascade
  early-out. All of them are atmosphere, and none is reachable from the two
  passes this package is.

## Seams this binds to

Everything below is on an `api` list in `docs/agents/`, which is the
compatibility surface `x/README.md` describes.

- Targets and passes: `renderer.CreateRenderTarget`, `renderer.CreateAppPass`,
  `renderer.SceneColor`, `renderer.SceneDepth`, `AppPass.SetEnabled`,
  `AppPassDesc.Params`, `AppPass.SetParams`, `DestroyAppPass`,
  `DestroyRenderTarget`, `RenderTarget.Extent` —
  [`render-targets.md`](../../docs/agents/render-targets.md).
- The fixed shader layouts in the same page: set 2 bindings 0–3 for pass inputs,
  and set 2 binding 12 for the pass's own uniform block.
- `shaders.DepthResolveVertSpv` for the fullscreen triangle.
- The caller supplies `glyphengine.Engine.ViewProjection` inverted, the camera
  eye, and `glyphengine.Scene.Environment`'s `SunDir` and `SunColor`. They are
  parameters rather than an `*Engine` so the packing can be tested without a
  window, and so a game with its own camera type is not forced through
  `Camera.ViewVectors`.

### Each pass carries its own 128 bytes, in its own block

Both parameter blocks are 128 bytes in a uniform block private to the pass, at
set 2 binding 12, declared by `AppPassDesc.Params` and written by `SetParams`.
The inverse view-projection alone is half of each one.

**That block did not exist when this package was written, and the first version
of it was the evidence that it should.** An application pass had 128 push bytes
and nothing else. The 4096-byte block at set 1 binding 6 is a single global the
*game* owns through `SetShaderParameters`, which replaces the whole thing, so an
`x` package cannot claim a slice of it without the game hand-partitioning bytes
between its own shaders and every package it uses. So 128 was the ceiling, and
this package fit inside it — which it was able to do, and the caustic field
below would not have been: its atlas anchoring alone is eleven `vec4`s in the
source, and the atmosphere's sun table and layer parameters are larger again.
That was filed as a rule-14 issue with this package as the evidence that 128 is
reachable but not generous, and the engine closed it (#170).

The move cost nothing and changed no pixel. `std140` lays a `mat4` followed by a
run of `vec4`s out at exactly the offsets the CPU was already packing, so the Go
side is the same bytes in the same order, the shaders differ by one declaration,
and `task xwater` reproduces its recorded sweep. The ceiling is now
`renderer.AppParamBytes`, 4096 per pass, with no partitioning and no other
owner.

**The folding stayed, on its own merits.** The per-frame scalars are still
collapsed on the CPU rather than evaluated per pixel: both daylight ramps to one
number each, the camera-depth falloff to one more, and the refraction of the sun
at the surface to a direction and a reciprocal. That was originally two
arguments at once — cheaper *and* smaller — and only the first one is load
bearing now. It is still the right call: a per-frame scalar evaluated per
fragment is the same number computed a million times. The one trick that was
purely about size is also kept, because it is still free: the camera position is
recovered from the inverse view-projection in the shader rather than sent, since
the eye is the one world point whose clip-space image has `w == 0`, so
`inverseVP * vec4(0,0,1,0)` is the eye scaled by a constant the perspective
divide cancels. The derivation is in `water-path.glsl`.

## Two engine gaps found while building this, and closed

The first is the per-pass uniform block above: 128 push bytes and a global block
with one owner, reported as a rule-14 issue and closed as `AppPassDesc.Params`
(#170), which this package now uses. The second is below.

The scattering integral needs a stable screen-space jitter, and the engine
already had exactly the right one with exactly the right reasoning attached:
`volStartJitter`, whose comment explains that it must not be animated because
there is no temporal filter and because renders under
`GLYPHENGINE_FIXED_FRAME_TIME` have to repeat byte for byte. The
Henyey–Greenstein `volPhase` was next to it.

Neither was reachable. Both lived in `shaders/include/volumetric.inc`, which
cannot be included without first declaring the clustered light buffers, the
shadow UBO and a `pc` block with `cameraPos` and `fog` — a light set this
package's shaders never read, declared only to get at two leaf functions. So
`water-scatter.frag` carried its own copy of the jitter, which is precisely the
vendored-copy failure the include export exists to remove.

**Reported, not patched — then fixed in the engine** (#169), because splitting
the leaf helpers out is an engine change and under rule 14 that is an issue to
file rather than something to do from here. The two functions now live in
`shaders/include/volumetric_common.inc`, which binds to nothing;
`volumetric.inc` includes it, so nothing in the engine changed shape. This
package's copy is gone: `water-scatter.frag` includes the leaf fragment and
calls `volStartJitter`. Remove that one line and `go test ./water/` fails with
`'volStartJitter' : no matching overloaded function found`, which is the gate
that says the function really arrives from the engine's exported set rather than
from anywhere local.

`volPhase` is deliberately *not* taken the same way, and the shader says why:
`ScatterPhase` is passed through unclamped, so `g == 1.0` is a value a game can
set, and at `g == 1` looking down the refracted sun's direction the denominator
is exactly 0. The engine's `volPhase` has no floor because `packLitUBO` clamps
its own `|g|` to 0.99. The curve is the same; the floor is this package's, for
an input the engine's version never sees.

## Gates

`task xwater`, and the package's own `go test ./water/` under `task ci`.

The pixel gate is `x/water/internal/watercheck`: a heightmap bowl, the engine's
water surface filling it, and one flat wall at a fixed 20 units in front of a
camera whose depth is a flag. The scene is the gate's own rather than an
example's because the entire measurement is "the same wall, the same distance,
from three different depths", and no amount of pitch and yaw walks a player to
an exact depth repeatably. Nothing in it is shadowed and nothing moves, so the
wall reads identically at every depth with the package off — that is the
control, and it is what makes "the water darkened it" distinguishable from "the
wall was lit differently".

640×360, frame 120, RX 7900 XTX, 2026-10-02. Mean RGB over the wall box, with
the package minus without it:

| Camera depth | Red | Green | Blue |
|---|---|---|---|
| 4 | −55.394 | −19.646 | −10.135 |
| 20 | −57.003 | −22.002 | −11.479 |
| 60 | −59.001 | −25.732 | −14.000 |

The no-package wall reads exactly 95.000 / 100.000 / 111.000 at all three
depths. Red falls more than five times as far as blue, which is the colour
shift; every channel deepens with depth, which is the falloff.

Also checked: the above-water frame is pixel-identical to the frame without the
package; a package created and never enabled is pixel-identical to one that does
not exist; two runs of the same capture are identical under the fixed clock; the
renderer's application-pass roster goes 0 → 3 → 0 across three create/destroy
cycles; and `Update` allocates zero bytes and zero objects over 2000 calls with
real passes behind it.

### Breaks, and what they said

The standard in [`x/README.md`](../README.md) is that a check that has never
failed is decoration. These were broken on purpose.

- **`BodyDepthFalloff` 0.025 → 0.** `TestDefaultsAreTheSourceConstants` fails:
  `BodyDepthFalloff = 0, want 0.025`. **`task xwater` passed.** The three-depth
  sweep stayed monotonic because the shafts dim with depth too — the blue spread
  across the depths only fell from 2.481 to 1.110 and the ordering survived. The
  first version of this gate was decoration for the one thing it was named
  after. It now sweeps a second time with `ScatterColor` zeroed, where nothing
  but that falloff can move the image: 1.000 and 2.000 counts of blue per depth
  step with it intact, 0.000 and 0.000 with it dead, and the run fails with
  `without shafts, blue darkened by only 0.000 between depth 4 and 20;
  BodyDepthFalloff is not reaching the image`.
- **The control caught its own scene.** Moving the deepest station to 60 units
  put the camera below the sloping bed at the wall's distance, so the "wall" in
  that capture was terrain. The run failed before reporting a single water
  number: `control: the no-package wall red differs between depth 4 and depth 60
  (95.000 vs 137.918); the depth comparison below would be measuring the scene`.
  The basin was deepened; the control is not decoration either.
- **The balance meta-check.** `-balance -disabled` skips the `Destroy` and must
  fail: `after destroy: 3 passes, want the baseline 0`.
- **The sun-path sign.** `TestPackedScatterBlockMatchesTheShaderLayout` caught a
  real bug on its first run — the refracted sun's cosine was negated, so the
  clamp took the floor instead of the value and every sample's light path was
  6.7× too long. `sunPathScale = 6.6666665, want 1` for a sun at the zenith.
- **`refract`.** `TestRefractedSunMatchesGLSL` caught hand arithmetic done with
  1.3333 instead of 1.333 on the same run. Both were mistakes in this package,
  found before a pixel was rendered.
- **A stale `.spv`.** Editing `water-scatter.frag` without recompiling fails
  `TestCommittedSPIRVMatchesGLSL`; the message names the file and both byte
  counts. Re-run on 2026-10-03 after the parameter block moved off push
  constants: `water-scatter.frag.spv is stale (7532 bytes committed, 7552
  fresh)`, against 7496/7516 on the push-constant version of the same shader.

## Failure modes

- **Nothing happens.** The eye is at or above `Level`, or `SetEnabled(true)` was
  never called, or `Update` is not being called every frame. `Enabled()` reports
  the intent, not whether the passes ran.
- **The volume starts in the wrong place.** `Options.Level` and the engine's
  `WaterOptions.Level` disagree.
- **The effect is a frame behind.** `Update` is being called before the camera
  moves. It belongs in `LateUpdate`, after `SetCamera`.
- **MSAA edges soften while submerged.** The present pass writes the HDR scene,
  and with MSAA that means loading the multisample colour and resolving it. The
  cost is real and it is the same one the source's atmosphere composite pays.
- **It is dark and flat with no shafts.** The sun is below
  `ScatterDaylightOnset`, or the camera is past `ScatterMaxDepth`. Both are
  deliberate; both are fields.
