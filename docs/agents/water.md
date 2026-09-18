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
verified: 2026-09-18
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

## Refraction costs a second render pass

A fragment shader cannot read the attachment it is writing, and refraction is
precisely a read of a *different* pixel. So a frame containing water splits:

1. the scene pass draws the opaque world, the sky, and whatever blended
   geometry is **behind** the water surface;
2. that result is copied into a sampled image;
3. a second pass draws the water, sampling the copy at an offset taken from the
   wave normal, and then the blended geometry that is **in front** of it.

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

**A scene with no water never begins the second pass.** The cost to everyone
else is two changed store ops.

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

The cost shows up as `gpu overwater` in `task bench`, separately from
`gpu water`, so the reorder is attributable rather than charged to the surface.
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
