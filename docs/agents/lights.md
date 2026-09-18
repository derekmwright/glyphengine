---
id: lights
title: Clustered unshadowed lights
summary: >
  Scatter hundreds of point and spot lights around a scene. Each frame they are
  binned into a view-space froxel grid, so a fragment only evaluates the lights
  whose range reaches it rather than every light in the scene.
capability: lighting
status: stable
since: v0.6.0
api:
  - glyphengine.PointLight
  - glyphengine.SpotLight
  - glyphengine.Scene.SetPointLights
  - glyphengine.Scene.SetSpotLights
  - glyphengine.Scene.PointLights
  - glyphengine.Scene.SpotLights
  - glyphengine.NightGrade
  - glyphengine.Scene.SetNightGrade
  - glyphengine.Scene.NightGrade
  - glyphengine.Engine.LightStats
  - glyphengine.LightDebugMode
  - glyphengine.LightDebugOff
  - glyphengine.LightDebugHeatmap
  - glyphengine.LightDebugBruteForce
  - glyphengine.Engine.SetLightDebugMode
  - renderer.lightcluster.MaxLights
  - renderer.lightcluster.MaxLightsPerCell
  - renderer.lightcluster.MaxLightIndices
  - renderer.lightcluster.Stats
example: examples/11-lights
run: task example:11-lights
requires:
  - cgo
  - vulkan-runtime
assets: none
verified: 2026-09-18
---

# Clustered unshadowed lights

```go
// Point lights: unshadowed fill lights that reach the GPU in any order.
points := []glyph.PointLight{
    {Pos: mgl32.Vec3{-5, 1, 0}, Range: 15, Color: mgl32.Vec3{1, 0.2, 0.2}},
    {Pos: mgl32.Vec3{5, 1, 0}, Range: 15, Color: mgl32.Vec3{0.2, 0.2, 1}},
}
e.Scene.SetPointLights(points)

// Spot lights: point lights narrowed to a cone. Dir can be any length;
// zero means omnidirectional. Inner and Outer are half-angles in radians.
spots := []glyph.SpotLight{
    {
        Pos: mgl32.Vec3{0, 3, -10},
        Dir: mgl32.Vec3{0, -1, 0},    // points downward
        Range: 20,
        Color: mgl32.Vec3{1, 1, 0.8},
        Inner: 0.2,                   // tight center
        Outer: 0.6,                   // soft edge around it
    },
}
e.Scene.SetSpotLights(spots)

// Check how many lights made it to the GPU.
stats := e.LightStats()
if stats.DroppedOverBudget > 0 {
    e.Debugf("dropped %d lights over budget", stats.DroppedOverBudget)
}
if stats.CellsOverflowed > 0 {
    e.Debugf("dropped from %d cells", stats.CellsOverflowed)
}
```

The two light types are set per frame with `SetPointLights` and `SetSpotLights`.
A scene written before spot lights existed lights exactly as it did before, in
the same order: points first, then spots.

`Color` holds intensity; a zero `Color` (or an empty slice) removes the
contribution entirely. `Range` is the distance past which a light contributes
nothing. For `SpotLight`, `Inner` and `Outer` are half-angles in radians; full
intensity inside `Inner`, a smooth falloff between them, and nothing beyond
`Outer`. A zero-length `Dir` makes a spot light omnidirectional (deliberate: it
makes a caller's uninitialized `Dir` visibly wrong rather than silently
missing).

The slices are not copied, so a game that drives hundreds of lights reuses one
buffer across frames and calls the setter each frame with different data.

## What it is for

A dungeon lit by forty torches, a sprawling colony of habitats each with lit
windows, a village street lined with lanterns, a city at night — these are the
cases where one directional light and one shadow-casting point light are not
enough, and the alternative — one giant light covering the whole scene — reads
as wrong. Before clustering, adding that many lights meant every fragment
evaluated every one, which cost what a shadow-casting light does (or more). After
clustering, a fragment evaluates only the lights whose range reaches it, which is
why scattering hundreds around a level is reasonable.

The single shadow-casting light (`Scene.SetPointLight`, a cube shadow map) is
separate and unchanged. It is the one expensive light — the player's lantern or
a boss's aura — and this feature is for the fill lights around it.

## How it works

Every frame, the CPU-side binner (`renderer/lightcluster`, pure Go, no GPU
dependencies) frustum-culls lights and bins them into a view-space froxel grid:
16 x 9 tiles across the screen, 24 depth slices logarithmic from 1 m near plane
onward. A fragment finds its cell at runtime from `gl_FragCoord` and the depth,
and reads only that cell's light list.

Binning is conservative: if a world point lies inside a light's range (and, for
a spot, inside its cone) and inside the frustum, the cell containing that point
lists that light. A false positive costs a few shader instructions. A false
negative is a tile-shaped hole in the light, which reads as silently wrong; those
are the ones binning is designed to never produce, and testing against a
brute-force oracle in lightcluster_test.go guards it.

Data goes to the GPU in three storage buffers on the shadow/light descriptor set
(bindings 3, 4, 5): the light array, a grid of cell metadata, and an index
buffer holding each cell's light list. `shaders/lights.inc` declares them and
exposes `resolveLightRange` and `lightAt` for shader code to iterate.

Spot lights use the packed form `DirCone.xyz = unit direction`, `DirCone.w =
cos(outer half-angle)`, `Color.a = cos(inner half-angle)`. A zero `DirCone.xyz`
signals a point light. The shader's `lightSpotFactor` applies a smooth cone
falloff, which is why the two angles pack as cosines rather than radians.

## Budgets and grid occupancy

Three hard limits govern how many lights reach the GPU:

```go
const MaxLights = 1024                  // lights after culling
const MaxLightsPerCell = 128            // per-froxel cap
const MaxLightIndices = 262144          // total index buffer size
```

Lights are kept in priority order: nearest surface first, ties broken by
submission order. A full froxel keeps the first 128 in that order. If the index
buffer fills, the last cells in cell order are truncated. All three are counted
in `Engine.LightStats`, which returns a `renderer/lightcluster.Stats`:

```go
type Stats struct {
    // Submitted, Culled, Uploaded: the flow of lights through the binner.
    // If Culled is high, lights are outside the frustum or have non-positive range.
    Submitted          int
    Culled             int
    Uploaded           int

    // DroppedOverBudget: lights that passed frustum test but lost the MaxLights
    // budget. Check this first — it is the ceiling on GPU work.
    DroppedOverBudget  int

    // CellsOverflowed, CellsTruncated: ways a cell can lose lights.
    // Overflow keeps the 128 nearest surfaces; truncation loses whole cells.
    CellsOverflowed    int
    CellsTruncated     int

    // MaxCellDemand: the highest count before the per-cell cap. If it exceeds
    // 128, a dense cluster of lights is losing its backmost members.
    MaxCellDemand      int

    // ScreenWideLights: lights that reached every tile of at least one slice.
    // These are the ones clustering did not remove from, so they evaluate for
    // every fragment in that slice. A pathological case (1024 large lights
    // surrounding the camera) produces high ScreenWideLights and high CPU cost
    // binning them. Watch this number.
    ScreenWideLights   int

    // NonEmptyCells, TotalCellLights: average list length per populated cell,
    // for tuning the grid. Most cells are empty sky.
    NonEmptyCells      int
    TotalCellLights    int

    // Remaining fields are for diagnosing the binner, not the scene.
    // See renderer/lightcluster for their definitions.
    MaxCellLights      int
    IndexCount         int
    UnboundedLights    int
    CellsTested        int
    CellsBinned        int
}
```

A game should watch `DroppedOverBudget`, `CellsOverflowed`, `CellsTruncated`,
and `ScreenWideLights`. Each is a way light can go missing without crashing or
erroring.

## Night grade

Night-time surfaces are desaturated and blue-shifted so a night scene reads as
night rather than dim day. This is not a global filter: it blends each fragment
toward a blue-grey based on how much of that fragment's light came from lamps
versus from the sun or moon. A warm lamp at night therefore keeps its color in
the pool it throws on the ground, and a surface that receives only sunlight goes
blue-grey.

`SetNightGrade` controls the shift's strength and tint:

```go
e.Scene.SetNightGrade(glyph.NightGrade{
    Strength: 0.8,                             // 0 = off, 1 = full strength
    Tint: mgl32.Vec3{0.72, 0.86, 1.30},       // blue-biased
})
```

The default is what this engine has always used, chosen for artistic direction
rather than physical accuracy. A game with a different look should change both
fields. It lives on `Scene` rather than on `EnvironmentState` because custom
`EnvironmentSource` implementations would otherwise inherit a zero value
(desaturation off) on a dependency bump. See [`day-night.md`](day-night.md) for
the full story of scotopic shift, and `scene.go`'s `SetNightGrade` for why it is
initialized at construction and not on the environment.

## Failure modes

- **Lights vanish when a scene hits the budget.** If `Engine.LightStats()
  .DroppedOverBudget > 0` or `.CellsOverflowed > 0`, the GPU is not seeing all
  the lights the scene placed. Reduction in order of visibility cost: reduce
  range (one light's max distance matters more than its count), scatter smaller
  lights, or raise the budget (costs GPU memory, not time). `ScreenWideLights`
  says whether the binning is helping.

- **A scene looks dark, but the lights are there.** The scene may have no
  `Environment` or an `Environment` with no ambient light. Unshadowed fill
  lights are fill — they assume the scene has an ambient baseline and add color
  and warmth on top. If there is no baseline, the unshadowed lights are the only
  thing lighting it, and they have nothing to add color to.

- **A warm lamp reads as white at night.** Lamp color is weighted by its share
  of light when computing the night shift. If the weight is zero, the blend
  produces the scene's ambient night color no matter what the lamp color is.
  Check `SetNightGrade`, or `LightStats().ScreenWideLights` — if it is high, a
  light reaching every fragment of a slice has its lamp nature diluted by
  moonlight and ambient.

- **Water stays dark and unlit by lamps.** Water has its own rendering path
  (`water.frag`) and does not evaluate the local light list. It is a known gap.
  Reflections off water can be lit through another body of water or through
  `Emissive` geometry, but direct lamplight on the water surface itself is out of
  reach today.

- **Migrating from an old shader, lamps disappeared.** The old `LightBlock` UBO
  at binding 3 is gone; it is now three storage buffers at bindings 3, 4, 5,
  declared once in `shaders/lights.inc` behind a `LIGHT_SET` macro. Games that
  replaced lit shaders via `WithShaders` must re-vendor the shader chain. Also,
  the per-frame UBO at binding 0 grew to 144 bytes: it now carries `vec4
  nightGrade` after `mat4 cascadeVP[2]`. If the UBO struct in your shader
  mismatches the packing expected, geometry moves, flickers or casts bad
  shadows.

## Debug modes

The shader evaluates lights two ways: the clustered path (normal play) and a
brute-force reference (per-light loop, ignoring the grid). Both must render
identically or the binning has a bug.

```go
e.SetLightDebugMode(glyph.LightDebugOff)           // normal: clustered
e.SetLightDebugMode(glyph.LightDebugHeatmap)       // per-cell light count
e.SetLightDebugMode(glyph.LightDebugBruteForce)    // reference: every light
```

`LightDebugHeatmap` replaces lit color with a blue-to-red ramp of light count
per cell, saturated at the cap (`MaxLightsPerCell`). `LightDebugBruteForce`
forces the reference path that ignores the grid entirely and evaluates every
uploaded light. It is the same lights in the same order — only the iteration
differs — so the two must render pixel-for-pixel alike. Any difference is a
missing light from a froxel it belongs in, and will be tile-shaped.

## Measured cost

Benchmark scenes: geometry lit by 32, 256, or 1024 unshadowed point lights
spread on a grid, each rendered 200 frames on an AMD Radeon RX 7900 XTX, under
`task bench` conventions (mean of three interleaved passes).

GPU opaque pass (brute force → clustered):
| Lights | Brute force | Clustered | Speedup |
|--------|-------------|-----------|---------|
| 32     | 0.122 ms    | 0.075 ms  | 1.6x    |
| 256    | 0.727 ms    | 0.188 ms  | 3.9x    |
| 1024   | 1.706 ms    | 0.234 ms  | 7.3x    |

CPU `cluster` phase (binning):
| Lights | Time    |
|--------|---------|
| 32     | 0.06 ms |
| 256    | 0.33 ms |
| 1024   | 0.50 ms |

A pathological case (1024 large lights all surrounding the camera, every light
in every cell): GPU 9.18 → 1.65 ms, but CPU binning 3.9–5.3 ms. The bottleneck
shifts to the binner; watch `ScreenWideLights` and `UnboundedLights` in that
case.

These are one machine's numbers. Run `task bench` on your hardware for yours.

## Verification

```sh
task lights      # Clustered and brute-force renderers byte-identical
task nightlight  # Warm lamps stay warm on the ground at night
```

`task lights` renders the same scene in both modes and checks every pixel. Both
should produce the same image; a difference is a binning bug. The scene is 400
lamps of range 4 on a tight grid (brute force would not help) plus spot lights,
from two camera poses: default distance and down among the lamps where the
froxel grid's near slice is working hardest.

`task nightlight` is a doorway pool under a warm spotlight, checking that the
pool stays warm (red minus blue > 0) after the night desaturation. It verifies
that lamp color survives the scotopic shift, which required weighting the blend
by how much of each fragment's light came from a lamp.
