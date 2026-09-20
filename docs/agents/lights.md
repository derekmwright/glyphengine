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
  - glyphengine.PointLight.Volumetric
  - glyphengine.SpotLight
  - glyphengine.SpotLight.Volumetric
  - glyphengine.Volumetrics
  - glyphengine.DefaultVolumetrics
  - glyphengine.Scene.SetVolumetrics
  - glyphengine.Scene.Volumetrics
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
  - renderer.Volumetrics
  - renderer.DefaultVolumetrics
  - renderer.MaxVolumetricSteps
  - renderer.LightFlagVolumetric
  - renderer.GpuLight.Params
  - lightcluster.MaxLights
  - lightcluster.MaxLightsPerCell
  - lightcluster.MaxLightIndices
  - lightcluster.Stats
  - renderer.Model.Lights
  - renderer.ModelLight
  - renderer.Model.LightWorldPosDir
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
  ones, where set 1 is the joint matrices). `GpuLight` is **64 bytes**, four
  vec4s — a stale 48-byte copy reads light 0 correctly and every later light
  through a window sliding into its neighbour, so a one-lamp scene looks
  perfect and a lit street flickers. The uniform block at binding 0 is **256
  bytes**: `mat4 cascadeVP[2]`, then `vec4 nightGrade`, `vec4 skyPalette[6]`
  and `vec4 volumetric`, in that order. (This page said 144 bytes, which
  stopped being true when the palette moved into that buffer; corrected here.)
  A stale shader declares the wrong descriptor type at binding 3 or the wrong
  block size at binding 0; run it under `task validate`, which will say so.

## Lights from a glTF level

A level authored in an editor can carry its own point and spot lights via
`KHR_lights_punctual`, and `renderer.LoadGLTF`/`LoadGLTFSkinned` read them
into `Model.Lights` — see the "Loading a level" section of
[`models.md`](models.md) for the full pattern and `examples/22-level` for a
worked example. The short version, specific to this page:

- **`ModelLight.Intensity` is candela (point/spot) or lux (directional) —
  glTF's units, not this engine's.** `PointLight`/`SpotLight` above carry no
  photometric unit at all; intensity is folded entirely into `Color`, the
  same as it always has been for a hand-placed light. A glTF light's raw
  intensity has to be scaled into that `Color` by the game — `Model.Lights`
  does not do this conversion, and there is no universal factor to convert
  by, only a look that reads right in the scene.
- **`ModelLight.Range == 0` means the document said "unbounded", which this
  engine's lights cannot represent.** A game supplies a finite `Range`
  itself. This is not a rare case: Blender's glTF exporter never writes
  `range` at all, so every light from a Blender-authored level needs one.
- **`InnerCone`/`OuterCone` use the exact same half-angle convention as
  `Inner`/`Outer` above** — verified against this page, not assumed — so a
  glTF spot's cone angles carry straight into a `SpotLight` with no
  conversion.

## A beam is the air being lit

A spot light lights whatever its cone lands on. To make the cone itself
visible — the shaft in the dust, the headlight in the fog — set
`Volumetric` on the light:

```go
door := glyph.SpotLight{
    Pos:   mgl32.Vec3{0, 2.6, -10},
    Dir:   mgl32.Vec3{0, -1, 0.2},
    Range: 9,
    Color: mgl32.Vec3{1.0, 0.58, 0.28}.Mul(2.8),
    Inner: 0.38,
    Outer: 0.82,

    Volumetric: 1.0, // 0 is the default: no beam, and no cost
}
```

`PointLight.Volumetric` does the same for a point light, where the result is
a glow around the bulb rather than a cone. Both default to 0, so every scene
written before this existed renders byte-identically and pays nothing.

**What it scatters off is the scene's fog.** Not a separate volumetric
medium: `Fog.Density` and the `Fog.Height`/`Fog.BaseHeight` profile from
[environment.md](environment.md#fog-settles-if-you-ask-it-to), sampled a
point at a time along the view ray instead of integrated in closed form. The
same air that hazes the hills is the air the lamp lights.

So **a scene with no fog shows no beam**, however high `Volumetric` goes.
That is the answer rather than a gap — why would you see a beam in a vacuum?
If a lamp should have a shaft, the scene needs air for it to be in.

### How it works

Per pixel, in the shaders that already call `applyFog`. From the eye toward
the fragment, the march steps through the froxels the view ray crosses and
sums, for each light in each cell that has a non-zero `Volumetric`:

    cone falloff x distance attenuation x phase x medium density x Volumetric x step length

The cone falloff and the attenuation are `lightSpotFactor` and the same
inverse-square-ish curve `lightIrradiance` uses for surfaces, minus `NdotL` —
there is no surface in the air and no normal to take it against. That is the
only difference, which is why a beam and the pool it lands in cannot disagree
about where the cone is.

The cell a step lands in is found with `clusterIndex`, the same function a lit
fragment uses, on the same pixel column and the step's own view depth. There
is no second copy of the cell arithmetic to drift.

Every lit surface gets this inside `applyFog`, integrated to its own depth:
opaque geometry, terrain, grass, skinned meshes and the water surface. The sky
gets it from a draw of its own (`shaders/skyvolumetric.frag`), because the sky
is not a lit surface — that draw is what lets a beam aimed at the night sky be
seen at all.

**Steps go geometrically in view depth**, which is the froxel grid's own
metric: the grid's 24 slices are logarithmic, so equal steps in `log(depth)`
means equal steps in slice index and every cell the ray crosses gets the same
number of samples. Stepping linearly in metres instead gives the far slices
twenty samples each and the near ones a fraction of one, which is backwards —
a lamp is usually in the near cells. The first segment reaches back to the eye
and covers exactly the span the binner folds into slice 0.

### Tuning the medium

```go
v := e.Scene.Volumetrics()
v.Anisotropy = 0.4 // Henyey-Greenstein g: 0 isotropic, positive forward
v.Steps = 32       // samples per pixel; 0 turns the march off scene-wide
e.Scene.SetVolumetrics(v)
```

`Anisotropy` is how forward-scattering the air is. Positive makes a beam
coming toward the eye brighter than the same beam crossing it, which is what
a real beam does. It is a strong lever: measured across a door spot's cone in
`21-streetlights` at 1280x720 and the default 32 steps, the light added at
g = 0 / 0.2 / 0.4 / 0.7 is +7.33 / +7.04 / +6.04 / +3.36 mean luma. The default 0.4 keeps 82% of the isotropic
brightness side-on; 0.7 keeps 46% and a street lamp seen from the side nearly
disappears.

`Steps` is the cost knob and the banding knob at once. The march is fixed-step
with a per-pixel start jitter that is a pure function of `gl_FragCoord`
(nothing animated — deterministic renders have to repeat byte for byte), so
too few steps read as a crosshatch rather than as steps. Measured on the
upward beam of `21-streetlights -skylamp` where it crosses open sky, mean luma
added and the graininess of what was added:

| Steps | added | grain |
|---|---|---|
| 8 | +6.24 | 49.82 |
| 16 | +6.29 | 37.13 |
| 24 | +7.25 | 22.60 |
| 32 | +7.44 | 10.10 |
| 64 | +7.51 | 5.59 |

The added light converges — 32 is within 1% of 64, and 16 is 16% *short*,
because noise through a concave tonemap loses light rather than merely moving
it about. 32 is the default: 16 is visibly crosshatched where a cone crosses
the frame and 32 is not. `MaxVolumetricSteps` (64) caps what one mistyped
number can do to a frame.

Reproduce the table with
`21-streetlights -skylamp -volumetric 1 -volsteps N` and `cmd/volumetriccheck`
over the box `610,40,50,120`.

### Measured cost

AMD Radeon RX 7900 XTX, `task bench`, means of three interleaved runs of 200
frames each; the spread across those runs is under 0.02 ms everywhere except
`gpu total`, which also carries the other passes.

`21-streetlights` with **every** light volumetric plus the upward lamp
(`bench -scene streetlights-volumetric`), against the same scene with none
(`bench -scene streetlights`):

| Steps | 1280x720 gpu total | opaque | sky-inscatter | 1920x1080 gpu total | opaque | sky-inscatter |
|---|---|---|---|---|---|---|
| off | 0.152 | 0.065 | — | 0.263 | 0.118 | — |
| 8 | 0.308 | 0.204 | 0.019 | 0.551 | 0.369 | 0.040 |
| 16 | 0.429 | 0.312 | 0.033 | 0.791 | 0.578 | 0.066 |
| 24 | 0.547 | 0.416 | 0.047 | 1.030 | 0.783 | 0.095 |
| 32 | 0.661 | 0.517 | 0.058 | 1.253 | 0.986 | 0.115 |
| 64 | 1.131 | 0.935 | 0.103 | 2.155 | 1.803 | 0.196 |

The cost model is **steps x lights in the cell, per pixel**, and in practice
it is almost exactly linear in steps: 0.0141 ms per step at 720p and 0.0271 at
1080p on this scene, from the slope of the opaque column.

**It lands inside the passes that were already there.** The march lives in
`applyFog`, so it is charged to `gpu opaque`, `gpu terrain`, `gpu grass` and
`gpu water` — whichever shader ran. Only the sky's share is separable, and
only because that one is a draw of its own. There is no `gpu volumetric`
bracket and there cannot be an honest one.

Water is its own shape of cost, because the surface is one near-horizontal
draw covering most of the lower frame and every one of those fragments marches
on top of the opaque pass having already marched for the bed underneath.
`09-water -time 0.02 -lamps 32 -spots 0 -lampposts=false`, at the default 32
steps:

| | off | all 32 lamps volumetric |
|---|---|---|
| gpu opaque | 0.070 | 1.466 |
| gpu water | 0.118 | 1.133 |
| gpu sky | 0.060 | 0.481 |

With two lamps instead of thirty-two: opaque 0.043 -> 0.298, water 0.062 ->
0.241, sky 0.063 -> 0.156.

A scene that asks for no volumetrics pays **nothing**, and that is checked
rather than asserted: the recorded command stream is unchanged to the bit
(`goldenStreamHash`, same 3299 driver calls), and twelve captures across eight
examples are byte-identical to the commit this branched from. The march sits
behind one branch on a value that is uniform for the whole draw.

### What it does not do

- **Beams are not shadowed.** A beam through a wall keeps glowing on the far
  side. No local light in this engine casts a shadow, so the beam is
  consistent with the light it belongs to; shadowing one without the other
  would be the odd thing.
- **Particles do not receive it, and they dilute it.** `particle.frag` applies
  no fog and no lighting at all — it is a soft radial falloff on a tint. A
  sprite drawn inside a beam therefore blends over the in-scattering that was
  added to whatever is behind it, so a dense emitter reads as a hole in the
  beam. Giving particles fog is a change to their look in every existing
  scene, so it is not made here.
- **A water pixel counts the air between the eye and the surface twice** — once
  in the lake bed's own march, which is baked into the refracted scene copy
  `water.frag` samples, and once in the surface's. That is exactly what the
  height fog already does at the same place and for the same reason, so the
  two are consistent; fixing one without the other would make them disagree.
  It produces no seam: across the shoreline of `09-water -lamps 9 -spots 2`
  the final image steps by 1.9 to 5.2 of 255 with volumetrics on, against 35.8
  with them off — the in-scattering reduces the contrast at the waterline
  rather than creating a step there.
- **The march is grainy at the apex of a narrow cone**, where the cone is
  thinner than one step at any count worth paying for. More steps help and do
  not cure it (grain 10.10 at 32 steps, 5.59 at 64). The answer to that is the
  3D-texture inject/integrate with temporal reprojection that Unreal and HDRP
  use, which is the upgrade path from here rather than a rewrite of it.
- **`Volumetric` is per light and it adds up.** Eleven lamps at 1.0 around the
  camera in fog is a great deal of light in the air: `09-water -lamps 9
  -spots 2 -volumetric 1` is a bright haze, not a set of beams. The number is
  a look to tune, not a switch.

### Verifying a change

```sh
task volumetric  # a beam is in the air inside the cone and nowhere else
task lights      # clustered and brute force still match, with the march on
```

`task volumetric` is three poses of `21-streetlights` differenced against the
same frame with `Volumetric` 0: a door spot seen from the side, the same spot
with its fixture cropped off the top of the frame, and a lamp aimed straight
up at the sky with the camera level with the column. It asserts light in the
cone and a ceiling outside it — the exact measurement that condemned the
screen-space route in issue #47, inverted — plus an exact zero far from every
lamp and a consecutive-frames check. The Taskfile records what each of four
deliberate breaks printed.

`task lights` gains two volumetric pairs. They are not spares: the march walks
the froxel lists a second time at up to 32 points per pixel, so it reads cells
no surface sits in, and the sky pixels go through a different shader and a
different pipeline layout to reach the same buffers.

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
task volumetric  # a beam is in the air inside a cone and nowhere else
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
