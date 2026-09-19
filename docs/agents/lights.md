---
id: lights
title: Light a scene with hundreds of point and spot lights
summary: >
  Unshadowed point and spot lights are binned every frame into a view-space
  froxel grid, so a fragment evaluates the lights that reach it rather than
  every light in the scene. Up to 1024 reach the GPU per frame.
capability: lighting
status: stable
since: v0.5.0
api:
  - glyphengine.PointLight
  - glyphengine.SpotLight
  - glyphengine.Scene.SetPointLights
  - glyphengine.Scene.SetSpotLights
  - glyphengine.Scene.PointLights
  - glyphengine.Scene.SpotLights
  - glyphengine.Engine.LightStats
  - glyphengine.LightDebugMode
  - glyphengine.LightDebugOff
  - glyphengine.LightDebugHeatmap
  - glyphengine.LightDebugBruteForce
  - glyphengine.Engine.SetLightDebugMode
  - renderer.MaxLights
  - renderer.MaxPointLights
  - lightcluster.MaxLights
  - lightcluster.MaxLightsPerCell
  - lightcluster.MaxLightIndices
  - lightcluster.Stats
example: examples/21-streetlights
run: task example:21-streetlights
requires:
  - cgo
  - vulkan-runtime
assets: none
verified: 2026-09-19
---

# Light a scene with hundreds of point and spot lights

```go
lamps := []glyph.PointLight{
    {Pos: mgl32.Vec3{-5, 3, 0}, Range: 12, Color: mgl32.Vec3{1.0, 0.74, 0.50}},
    {Pos: mgl32.Vec3{5, 3, 0}, Range: 12, Color: mgl32.Vec3{1.0, 0.74, 0.50}},
}
e.Scene.SetPointLights(lamps)

door := glyph.SpotLight{
    Pos:   mgl32.Vec3{0, 2.6, -10},
    Dir:   mgl32.Vec3{0, -1, 0.2}, // down and slightly outward; any length
    Range: 9,
    Color: mgl32.Vec3{1.0, 0.58, 0.28}.Mul(2.8), // intensity rides in Color
    Inner: 0.38, // half-angles, in radians
    Outer: 0.82,
}
e.Scene.SetSpotLights([]glyph.SpotLight{door})

if st := e.LightStats(); st.DroppedOverBudget > 0 || st.CellsOverflowed > 0 {
    e.Debugf("lights lost: %d over budget, %d cells overflowed",
        st.DroppedOverBudget, st.CellsOverflowed)
}
```

Both setters replace the whole set and keep the slice you pass rather than
copying it, so a game that animates its lights mutates one slice and calls the
setter again. `SetSpotLights(nil)` removes every spot; a zero `Color` removes
one light's contribution.

`Inner` and `Outer` are **half**-angles in radians: full strength inside
`Inner`, a smooth falloff to nothing at `Outer`. There is no separate intensity
field on either type — scale `Color`, exactly as `PointLight` always has.

The one **shadow-casting** point light (`Scene.SetPointLight`, a cube shadow
map) is a separate mechanism and is unchanged by any of this. There is still
only one of it.

## How it works

Every frame, before drawing, the engine hands the lights to
`renderer/lightcluster` — pure Go, no Vulkan, testable without a GPU — which:

1. frustum-culls each light by its bounding sphere (for a spot narrower than a
   60 degree half-angle, the tight sphere around its cone rather than the full
   range sphere);
2. orders the survivors and applies the `MaxLights` budget;
3. bins each one into a view-space froxel grid of **16 x 9 x 24** cells: 16 x 9
   tiles across the framebuffer, 24 depth slices that are logarithmic from 1 m
   to the far plane, with everything nearer than 1 m folded into slice 0.

The result goes to the GPU in three storage buffers — the engine's first — at
bindings 3, 4 and 5 of the shadow/light descriptor set: the light array with a
small header, one `{offset, count}` per cell, and the concatenated index lists.
`shaders/lights.inc` declares them once for all seven lit fragment shaders. A
fragment finds its cell from `gl_FragCoord.xy` and its view depth
(`1.0 / gl_FragCoord.w`, which needs neither near nor far) and loops over that
cell's list only.

Binning is **conservative**: if a point is inside a light's range (and cone)
and inside the frustum, the cell containing it lists that light. A false
positive costs a few shader instructions; a false negative is missing light.
`renderer/lightcluster/oracle_test.go` checks that against brute force over
random scenes, including a colony-builder orbit camera at minimum zoom — the
eye 1.31 m above a hex grid of lamps, with lit geometry closer than the 1 m
slice start.

**Invariant:** all lit geometry is drawn from one camera. Water refraction
samples a copy of the scene rather than re-rendering it, and shadow passes are
depth-only. A second lit camera — a planar reflection, split screen — would need
its own grid; the one that exists is built for the main view.

## Budgets, and what happens past them

| Constant | Value | Meaning |
|---|---|---|
| `lightcluster.MaxLights` (also `renderer.MaxLights`) | 1024 | lights uploaded per frame, after frustum culling |
| `lightcluster.MaxLightsPerCell` | 128 | cap on one cell's list, and so on the shader's inner loop |
| `lightcluster.MaxLightIndices` | 262144 | total index entries across all cells |

`renderer.MaxPointLights` still exists as an alias of `MaxLights` so older code
compiles; it no longer means 32.

Nothing past a budget is silent, and all of it is deterministic:

- Over `MaxLights`, lights are kept in ascending *(distance from the camera
  minus range)* order — nearest lit surface first — with ties broken by
  submission order. The rest are counted in `DroppedOverBudget`.
- A cell that wants more than 128 keeps the first 128 in that same order and is
  counted in `CellsOverflowed`.
- If the whole index buffer fills, the cells that come last in cell order are
  cut short and counted in `CellsTruncated`. That one shows up as the far end of
  the grid going dark.

`Engine.LightStats()` returns the last frame's `lightcluster.Stats`. The fields
a game should put on a debug readout:

| Field | Watch it because |
|---|---|
| `DroppedOverBudget` | lights the GPU never saw |
| `CellsOverflowed`, `CellsTruncated` | lights missing from part of the screen |
| `MaxCellDemand` | the largest list any cell wanted before the cap; the number that says whether 128 is enough for your scene |
| `ScreenWideLights` | lights that landed in every tile of some depth slice, which clustering cannot help with |
| `Uploaded`, `IndexCount` | how much work the frame actually carries |

The remaining fields (`UnboundedLights`, `CellsTested`, `CellsBinned`, ...) are
for diagnosing the binner; their definitions are on the struct.

## Measured cost

AMD Radeon RX 7900 XTX, `cmd/bench` scenes `lights32` / `lights256` /
`lights1024` (`11-lights -lamps N`: a grid of range-4 lamps 1.6 m apart over a
floor, 200 frames), mean of three interleaved passes.

| Lamps | GPU opaque pass, brute force | clustered | CPU `cluster` phase |
|---|---|---|---|
| 32 | 0.122 ms | 0.075 ms | 0.06 ms |
| 256 | 0.727 ms | 0.188 ms | 0.33 ms |
| 1024 | 1.706 ms | 0.234 ms | 0.50 ms |

The worst case is not many lights, it is large lights wrapped around the
camera. 1024 lights on a ring with every one reaching every cell measured
9.18 -> 1.65 ms on the GPU but 3.9-5.3 ms of CPU binning, because a light that
genuinely touches all 3456 cells has to be written into all of them.
`ScreenWideLights` is the early warning.

These are one machine's numbers. `task bench -- -scene lights1024-clustered`
gives you yours; the CPU cost is the `cluster` phase of the engine's CPU timer
(`cpu_cluster` in the bench output).

## Failure modes

- **Lights go missing in a dense scene, with no error.** A budget was exceeded.
  Read `LightStats()`; see the table above for which counter means what. The
  budgets are constants in `renderer/lightcluster`, sized from the measurements
  recorded beside them.
- **A spot lights everything around it.** Its `Dir` is the zero vector, which
  makes it omnidirectional. That is deliberate — a flooded scene is a visible
  bug, a silently dropped light is not.
- **Cone angles that make no sense are clamped, not rejected.** `Inner > Outer`
  becomes a hard edge at `Outer`; both angles are clamped to [0, pi]; NaN
  becomes 0. A hard edge (`Inner == Outer`) is supported.
- **A lamp beside a lake puts no pool of light on the water.** It is not meant
  to. Water takes local lights as a specular reflection only -- a streak
  breaking up across the wavelets -- and nothing diffuse, because the body term
  multiplies a lamp by the lake's cyan albedo and the lamp stops reading as
  warm; the measurement is in [water](water.md#what-a-lamp-does-to-the-water).
  What remains, and what does surprise people: a lamp shows on the surface at
  exactly one place, the point where the half-vector lines up with the normal,
  so a fixture whose cone is aimed anywhere else lights the lake BED and leaves
  the surface dark. `task waterlight` is the gate; `task lights` includes
  three water pairs.
- **A warm lamp reads cold at night.** It should not: the night grade is
  weighted by how much of a fragment's light came from lamps and emission, so
  lamplit surfaces keep their colour. If one does not, see the failure mode of
  the same name in [`day-night.md`](day-night.md). `Scene.SetNightGrade`, also
  documented there, is how a game tunes or turns off the night shift itself.
- **A game that replaced the lit shaders through `WithShaders` must re-vendor
  them.** The `LightBlock` uniform buffer that used to sit at binding 3 is gone:
  bindings 3, 4 and 5 are storage buffers now, declared in `shaders/lights.inc`
  behind a `LIGHT_SET` macro (set 1 for static pipelines, set 2 for skinned
  ones, where set 1 is the joint matrices). The uniform block at binding 0 also
  grew from 128 to 144 bytes — a `vec4 nightGrade` after `mat4 cascadeVP[2]`.
  A stale shader declares the wrong descriptor type at binding 3; run it under
  `task validate`, which will say so.

## Debug views

```go
e.SetLightDebugMode(glyph.LightDebugHeatmap)    // lights per cell, blue -> green -> red
e.SetLightDebugMode(glyph.LightDebugBruteForce) // ignore the grid, loop every uploaded light
e.SetLightDebugMode(glyph.LightDebugOff)        // clustered, the default
```

The heatmap's red end is `MaxLightsPerCell`, so a scene that is mostly red is
close to overflowing. Brute force is the reference implementation: the same
shader code over the same lights in the same order, minus the grid.

`11-lights` and `21-streetlights` both take `-lightdebug heatmap|bruteforce`
and `-lightstats`.

## Verifying a change

```sh
task lights      # clustered and brute force render byte-identical frames
task nightlight  # a doorway pool stays warm at night; the ground beside it does not
task waterlight  # a lamp beside a lake reaches the water, in the lamp's colour
```

`task lights` captures `11-lights -lamps 400 -spots 32` in both modes at two
poses — an overview, and one with the camera inside about forty lamp spheres
and half a metre from the floor — at two resolutions, plus two `21-streetlights`
pairs so the terrain and material shaders are covered and three `09-water`
pairs so the water surface is, and requires every pair to match **byte for
byte**. Exact equality is achievable because both modes run the same code over
lights in the same relative order, and a light that does not reach a fragment
adds exactly zero.

Water is there in its own right rather than assumed covered: `water.frag` is
not one of the shaders that call `evalLighting*`, it runs its own loop over the
same `resolveLightRange`/`lightAt`, and it is drawn in a second pass from the
same camera after the depth buffer is final.

It has to be exact. Shrinking the binner's sphere test to 0.8 of the radius —
a genuinely non-conservative binner — changed 25.79 % of the pixels in the near
pose, every one of them by 1/255, in no visibly tile-shaped pattern: what goes
missing is the dim tail of a falloff. Any tolerance would have passed it. The
gate also refuses to run on a scene that drops lights, since the two modes would
then not be doing the same work, and carries a control (one lamp fewer must
change the capture).

**Water fails louder than that**, and it is worth knowing before measuring one.
A lamp reaches water only as a specular lobe of exponent 220, so a light missing
from a froxel deletes a whole glint rather than dimming a gradient. Under the
same shrunk sphere test the dense water pose differs on 4.45 % of the frame with
a largest channel difference of **42** of 255, and every one of those pixels is
in the water band.

`task nightlight` samples the pool under a 2800 K door light in
`21-streetlights` and the moonlit ground beside it: the pool must come out
R > G > B with red at least 60/255 above blue, the ground must stay blue-grey.
It fails when the lamp weighting is removed or cut to 15 %; it cannot tell a cut
to 35 % from an honest retune of the lamp, and says so in its own comment.

`task waterlight` renders `09-water -lamps 9 -spots 2` at night twice — once
with the lights and once under `-lampsoff`, which builds every pile and fixture
and hands the scene nothing — and differences the pair. Water carrying a
reflection must gain warm light, R above G above B, and water out of every
lamp's range must be **byte-identical**, since `lightIrradiance` returns exactly
`+0.0` outside a range test. Removing the local light from `water.frag` takes
the lamp box from R +38.56 G +32.48 B +26.52 to +0.00 across the board;
removing only the colour, so the surface keeps a local share it has not earned,
brings it back as R −3.50 G +1.06 B +6.20 — lights on making the water bluer,
which no brightness measure would report.
