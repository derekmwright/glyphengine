---
id: water
title: Animated water surfaces
summary: >
  Fill a heightmap basin with a Gerstner-wave surface that refracts the lake
  bed, reflects the sky by Fresnel, and absorbs colour with depth.
capability: water
status: stable
since: v0.3.0
api:
  - glyphengine.WaterOptions
  - glyphengine.DefaultWaterOptions
  - glyphengine.WaterMesh
  - glyphengine.Engine.CreateWaterMesh
  - glyphengine.Water
  - renderer.WaterParams
requires:
  - terrain-heightmap
assets: none
example: examples/09-water
run: go run ./09-water
verified: 2026-09-19
---

# Water

```go
opts := glyph.DefaultWaterOptions(waterLevel)

surface, err := e.CreateWaterMesh(hm, opts)
if err != nil {
    return err
}
ent := e.Spawn()
e.C.Transform.Set(ent, &glyph.Transform{Scale: mgl32.Vec3{1, 1, 1}})
e.C.MeshRef.Set(ent, &glyph.MeshRef{Mesh: surface})
e.C.Water.Set(ent, &glyph.Water{Options: opts})
e.C.Static.Set(ent, &glyph.Static{})
```

The mesh is built from the same `Heightmap` the terrain is, so the two cannot
disagree about where the shoreline is. Pass the **same** `WaterOptions` to
`CreateWaterMesh` and to the `Water` component: the mesh bakes some of them in
and the shader reads the rest, and if they differ the surface and its shading
describe different lakes.

## What the depth baking buys

Every vertex carries the still-water depth beneath it, sampled from the
heightmap at build time. That one number drives four things, none of which need
a depth-buffer read:

| Effect | How depth is used |
|---|---|
| Shore fade | Surface fades in over the first 0.35 units of depth |
| Colour absorption | `mix(ShallowColor, DeepColor, 1-exp(-travel/AbsorptionDepth))` |
| Wave shoaling | Amplitude scales to zero as depth does, so waves meet the shore rather than cutting through it |
| Refraction falloff | Distortion fades in the shallows, which is what stops the shoreline smearing into the lake |

`travel` is the depth divided by `dot(N, V)`, not the depth itself — looking
along the surface crosses far more water than looking straight down, which is
why a lake is clear at your feet and opaque at the far shore.

## Quads over dry land are dropped

`WaterMesh` keeps a quad if **any** corner is underwater. Fully dry quads are
dropped, so the mesh follows the lake rather than covering the map. Partly-dry
quads are kept on purpose: they are the shoreline, and their dry corners have
depth zero, which is exactly what fades the surface out there.

If `Level` is above all terrain, `WaterMesh` returns an error rather than an
empty mesh — a surface with nothing under it is almost always a mistake in the
level, not something to render.

## What a lamp does to the water

Point and spot lights from the clustered set ([lights](lights.md)) reach the
surface as **specular only**: each one adds a reflection through the same lobe
the sun's glint uses, over the same normal the waves and the per-fragment
ripples built, so a lamp breaks up across the wavelets exactly where the sun
does. There is no diffuse contribution to the body colour.

That is a measurement, not a shortcut. The body term multiplies light by the
water's albedo, which is cyan by default, so it repaints the lamp in the lake's
colour. Measured on `09-water -lamps 9 -spots 2 -time 0.02` against the same
scene under `-lampsoff`, what the lamps ADD to a patch of water carrying a
reflection:

| body term, as a share of the sun's weight | R | G | B |
|---|---|---|---|
| 0.00 (shipping) | +21.4 | +18.0 | +14.9 |
| 0.10 | +24.9 | +26.1 | +24.6 |
| 0.25 | +35.1 | +38.9 | +36.1 |

A tenth of the sun's weight already puts green above red, which is the failure
`task nightlight` exists to catch on the ground. At 0.25 the open lake between
the reflections lifts by +13/+18/+18 with nothing on it: the pool of paint.
Calm-to-rippled water is overwhelmingly specular anyway, so this is also the
physical answer, and the light a lamp puts into the body is not lost — where
the water is shallow enough for the body to matter, the bed shows through the
refraction and the opaque pass has already lit that bed with the same lamp.

Everything goes through `resolveLightRange` / `lightAt` / `lightIrradiance`, so
the cluster grid, `LightDebugBruteForce` and the spot cone behave here exactly
as on the terrain beside the lake. `task lights` requires the two modes to
render the lake byte-identically, at three poses.

The **froxel heatmap** covers water too. It used to be the one hole in that
readout: the lake showed a refracted, absorbed, Fresnel-mixed picture of the
*bed's* heatmap, in colours that read as a smaller count than the cells there
hold. It is drawn at alpha 1.0 rather than the surface's own fade, because
blending a count over another count gives a colour that means neither.

### Where it is approximate

The **night grade's local share** — how much of what leaves a fragment came
from a lamp, which is what stops lamplit surfaces taking the full scotopic
shift; see [day-night](day-night.md) — is set from the glint alone. Two things
are counted as sky that are not purely sky:

- **The refracted scene.** It is a pixel of the opaque pass, already lit,
  already fogged and already graded, and nothing in the water shader can say
  how much of it was lamplight. So water over a lamplit bed keeps more of the
  night grade than the bed beside it does. The error is bounded by how much of
  the bed survives absorption and goes to nothing as the water deepens.
- **The Fresnel sky reflection**, even where what it reflects is a lit shore:
  `fogColor()` is the atmosphere's horizon gradient and knows nothing about
  lamps.

Both err toward calling light "sky", so a lit surface comes out slightly cooler
than the shore rather than warmer.

A lamp's reflection is **unshadowed**, like every clustered light in the engine.

### What it costs

`gpu water` on this machine, 1280x720, 200 frames, six configurations
interleaved over three rounds, with `-lampposts=false` so all six draw the same
geometry and only the light count differs:

| lamps in range | before | after |
|---|---|---|
| 0 | 0.050 / 0.050 / 0.051 ms | 0.055 / 0.055 / 0.058 ms |
| 32 | 0.049 / 0.050 / 0.050 ms | 0.111 / 0.113 / 0.113 ms |
| 400 | 0.049 / 0.049 / 0.049 ms | 0.542 / 0.556 / 0.570 ms |

The "before" column is flat across all three, which is the bug stated as a
number: the surface was not reading the light list at all.

An empty lake costs 0.005 ms more than it did — the cluster lookup and its
branch, paid once per water fragment whether or not any light is in the cell.
It is consistent across all three rounds and the spreads do not overlap, so it
is real rather than drift; it is about 11% of a pass that is 2% of the frame.

400 lamps packed over one patch of lake cost 0.51 ms on a surface covering
roughly the lower half of the frame, against 0.31 ms for the opaque pass over
the same lights. A game with a lot of shoreline should watch this pass the way
it watches the opaque one — the surface is cheap per fragment and there are a
great many of them.

## Refraction costs a second render pass

A fragment shader cannot read the attachment it is writing, and refraction is
precisely a read of a *different* pixel. So a frame containing water splits:

1. the scene pass draws the opaque world, the sky, and whatever blended
   geometry is **behind** the water surface;
2. that result is copied into a sampled image;
3. a second pass draws the water, sampling the copy at an offset taken from the
   wave normal, then the light shafts, then the blended geometry that is **in
   front** of it.

The shafts sit between the surface and the blended draws on purpose. They are
built out of the copy, which holds the opaque world and the sky and nothing
else, so the water — part of the same world — is fair to haze over, while a
flame, a particle or a world overlay recorded after the copy contributed nothing
to the smear and would only be washed by it.

No example can show the difference, and that is worth saying rather than
implying otherwise: moving the shafts below the blended draws renders
byte-identical frames for `09-water -plume -ghost -marker -submerged` at every
pose where the shafts are strong, because the sun's azimuth is always on the +Z
side by construction and 09-water's blended effects sit toward the lake about 90
degrees away, against a 72-degree field of view. The order is a decision about
what the smear is made of, and a recorder test pins it so the first scene able
to see it does not get the other one by accident. See `recordLightShafts` and
[`environment.md`](environment.md).

Both passes share the depth buffer, which is why the first now stores depth
instead of discarding it — water still has to be occluded by terrain in front
of it. Under MSAA they also share the multisample colour buffer, and the frame
resolves twice.

The two passes are deliberately **render pass compatible**: same attachments,
same subpass, and — this is the part that is easy to lose — the same subpass
dependency, which `sceneEntryDependency` exists to keep identical. That is what
lets the blended pipelines, built against the scene pass, be bound inside the
water pass without a second set of them. Give the two passes different
dependencies and every blended draw in the water pass becomes
`VUID-vkCmdDrawIndexed-renderPass-02684`; `task validate` catches it on the
first frame.

**A scene with no water begins the second pass only for light shafts.** Since
#50 they are drawn in it, so a frame with `Sky.LightShafts` in effect pays for
the copy and the resolve even with no lake in it: measured with no water in
frame, `gpu water` 0.019–0.022 ms for the copy and `gpu waterresolve` 0.020 ms
in `07-terrain`, 0.041–0.177 ms in `08-grass`. A frame where the shafts cannot
contribute — sun down, behind the camera, or past the screen-edge fade — skips
the pass exactly as before, and the cost to everyone else is still two changed
store ops.

## Blended draws split on the surface

Nothing blended writes depth. So before this split existed, a flame standing in
front of the lake left the depth buffer holding the lake *bed* at those pixels,
the water passed the depth test, and the surface was painted straight over the
flame: cut off dead flat at the waterline, visible against sky and gone against
water (issue #45). Everything blended was affected — particles, `Translucent`
meshes, world-space overlays.

Drawing all of it after the water would break the other half: something under
the surface has to be in the frame before the copy, or the water refracts a bed
the object is not part of and the object lands on top of the lake, unrefracted.

So each blended draw is classified:

> A blended draw is **behind** the water, and stays before the copy, when some
> water surface's still plane separates it from the eye. Otherwise it is **in
> front**, and is drawn after the water.

"Separates" is the whole rule, and it is why an underwater camera needs no case
of its own: from above the surface the things behind the water are the ones
below it, and from below, the ones above it. It is also the half that is easy to
drop — a surface above both the eye and the draw separates nothing — so the rule
is unit-tested in `renderer/waterorder_test.go` rather than left to a capture
nobody can take without walking into the lake.

`renderer/waterorder.go` is the one place that decides, and the page there is
the long version. What it does **not** do, in order of how likely you are to
meet it:

| Approximation | What it costs |
|---|---|
| The surface is its **still plane** | A draw within `WaveAmplitude` of the surface can be classified onto the wrong side |
| A draw is **one point**, its bound centre | A tall pane half in the water goes wholly one way. Particles split per instance, which is as fine as the instance buffer goes |
| The footprint is the surface mesh's **bounding disc** in XZ | Over a concave shore the disc covers dry land; that only changes the answer for a draw below the waterline over dry ground, which is a draw inside the terrain |
| Several bodies are tested **independently** | With a tarn above a lake and the eye above the tarn, a draw between the two levels is behind the tarn and goes before the copy — refracted by water it is not in. Deliberate: a wrong tint beats a missing object |

**World-space overlays are always drawn after the water**, whatever their
position, because "on top" has to mean on top of the lake too — and an overlay
recorded before the copy is also *inside* the refraction, smeared through the
waves. Screen-space overlays were moved out of this pass long ago for the same
reason; see [`overlay-composite.md`](overlay-composite.md).

Particles are one instanced draw per frame, and one system routinely straddles
the surface, so the **instance buffer** is what splits: a stable partition puts
the behind-the-water instances first and the frame issues two draws over the
two ranges. The renderer has no idea how many emitters produced them, which is
the right level — "per system" would be too coarse even if it were reachable.

### What it costs

`gpu overwater` in `task bench`, separately from `gpu water`, so the reorder is
attributable rather than charged to the surface, and `gpu waterresolve` for the
water pass's own MSAA resolve, which belongs to neither. Measured on this
machine at 1280x720, 200 frames, three runs each:

| scene | `gpu water` | `gpu overwater` | `gpu waterresolve` |
|---|---|---|---|
| `09-water` — water, nothing blended in front of it | 0.062 / 0.061 / 0.055 ms | 0.000 / 0.000 / 0.000 ms | 0.030 / 0.021 / 0.021 ms |
| `09-water -plume -ghost -marker -submerged` | 0.056 / 0.056 / 0.056 ms | 0.012 / 0.012 / 0.012 ms | 0.021 / 0.021 / 0.022 ms |

`gpu overwater` reads zero on a lake with nothing in front of it, which is what
the name promises. For a while it did not: it closed after the pass ended and so
carried the resolve, 0.021-0.026 ms of a number called "overwater" on a scene
with nothing over the water. The reorder itself is the 0.012 ms in the second
row, over a flame of ~340 instances, a double-sided pane and an overlay disc.

Against the same scene recorded in the old order, the blended passes sum to
0.088–0.128 ms before and 0.095–0.120 ms after, three runs each: the same work,
moved. Whole-frame totals say nothing here — the same build measured three times
spans 1.99 to 2.60 ms on this machine, which swamps anything above.

`task waterblend` is the gate.

### When refraction is unavailable

The copy needs `TRANSFER_SRC` usage on the swapchain images. Where the driver
will not allow it, the renderer logs

```
Swapchain images are not transfer-capable: water refraction disabled
```

and the shader falls back to ordinary alpha blending: waves, Fresnel
reflection, and depth colouring all still work, and the lake bed simply does
not ripple. Setting `RefractStrength` to 0 selects the same path deliberately.

## Failure modes

- **The lake is invisible.** Almost always the basin rim is higher than the
  eye. Water is not drawn where terrain is above `Level`, so if the shoreline
  sits in a bowl you have to be near it or above it. `examples/09-water` walks
  outward from the basin centre until the ground clears the waterline rather
  than hardcoding a spawn, so `-seed` keeps working.
- **The surface faceted into visible triangles.** `Resolution` is too low for
  `WaveLength`. Waves displace per vertex, so the grid bounds the shortest
  wavelength the surface can show.
- **The waves fold through themselves.** `WaveAmplitude` is large relative to
  `WaveLength`. Gerstner displacement is horizontal as well as vertical, and
  past roughly a 1:6 ratio crests overtake the spacing between vertices.
- **The shoreline smears out into the lake.** `RefractStrength` is too high.
  The offset is screen-space and has no way to know it has sampled a pixel that
  is not underwater; the depth falloff limits this but does not eliminate it.
- **Water renders over geometry standing in it.** Water is drawn after
  everything opaque and does not write depth. Something submerged must be in
  the opaque pass to occlude it correctly. Blended geometry is handled — see
  the split above — but only down to the still plane: a `Translucent` mesh
  whose bound centre sits inside the wave band can be classified onto either
  side, and on the wrong one it goes back to being painted over. Anything that
  has to be right at the waterline wants to be opaque.
- **A lamp on the shore puts no reflection on the water.** Check where its
  reflection would actually be. A specular surface returns a light from exactly
  one place — on the line between the eye and the light's mirror image, a
  fraction `eyeAboveSurface / (eyeAboveSurface + lightAboveSurface)` of the way
  out — and the surface shows nothing anywhere else, however bright the fixture
  is. A spot whose cone lands somewhere other than that point lights the bed
  and not the water. `examples/09-water`'s `-spots` aims each fixture from that
  arithmetic for exactly this reason.
- **A submerged effect looks flat and unrefracted.** It was classified as being
  in front of the water. Check its bound centre rather than its silhouette: the
  split reads one point per draw, so a mesh whose origin is above the surface
  goes after the water even if most of it is under.
- **~~The HUD vanishes where the lake is.~~** Fixed, and worth knowing because
  it is the shape of bug this pass invites. Screen-space overlays used to be
  recorded in the opaque pass, so step 2 above copied the HUD along with the
  scene and step 3 drew the surface over text that writes no depth. They are
  composited after the tonemap now and the copy cannot contain them; see
  [`overlay-composite.md`](overlay-composite.md). Anything else that reaches
  the scene image before this pass runs is subject to the same thing.

## Vertex attributes carry water data

The surface has no use for its vertex colour, normal, or UV in the usual sense,
so they carry water data instead and avoid a second vertex format:

| Attribute | Carries |
|---|---|
| `Color` | deep water colour |
| `Normal` | shallow water colour — the real normal comes from the wave derivatives |
| `UV.x` | still-water depth at this vertex |

Both `water.vert` and `WaterMesh` document this from their own side. Changing
one without the other produces a surface that renders but is coloured wrong,
with no error.

## Wave detail is bounded by the grid, not by taste

Two limits decide what the surface can actually show, and both used to be
silent. Exceeding either produced hard flat facets tearing out of the wave
field, with shading that did not match the water around them.

**Sampling.** The shortest of the five components is `WaveLength * 0.26`, and it
needs at least two vertices across it to be a wave rather than a beat pattern
the grid invented. Given a surface `w` units wide:

```
WaveLength * 0.26 >= 2 * w / Resolution
```

Below that the shader fades the component out. Asking for short waves on a coarse
grid therefore gets smooth water rather than choppy water — the detail is
dropped rather than faked, the same trade a mip level makes.

`DefaultWaterOptions` at `Resolution` 160 over a 200-unit lake sits just under
the limit, so its finest component is faded. `Resolution` 256 carries it.

**Folding.** Gerstner's horizontal displacement is invertible only while
`sum(steepness * k * amplitude)` stays below one. Past that adjacent vertices
swap order and the surface passes through itself; the same sum also appears in
the analytic normal, so those facets shade inside out. The shader clamps
steepness against the real sum, so this cannot happen — but it means crests stop
sharpening past roughly `WaveAmplitude = WaveLength / 11` and only get taller.

## Breaking up the periodicity

A sum of sinusoids is exactly periodic, so on their own the surface tiles visibly:
the same crest pattern marching away to the horizon, most obvious looking down
at the field from a shallow angle. `WaveNoise` adds drifting fractal noise to the
height — patches of chop, stretches of calm, crests that do not all agree.

It is a fraction of `WaveAmplitude`, so it scales with the sea state. Zero
restores the pure Gerstner sum. Its octaves are bounded by the grid exactly as
the wave components are, so raising it on a coarse mesh makes the water lumpier
rather than faceted.

The noise is added to the height rather than to the wave phases so that its
gradient can be differenced from the same function the displacement uses. That
keeps the analytic normal exactly consistent with the geometry; perturbing phases
instead would leave the normal describing a surface that is not the one drawn.
