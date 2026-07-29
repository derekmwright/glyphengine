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
verified: 2026-07-29
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

1. the opaque pass draws everything and presents as usual;
2. that result is copied into a sampled image;
3. a second pass draws only the water, sampling the copy at an offset taken
   from the wave normal.

Both passes share the depth buffer, which is why the first now stores depth
instead of discarding it — water still has to be occluded by terrain in front
of it. Under MSAA they also share the multisample colour buffer, and the frame
resolves twice.

**A scene with no water never begins the second pass.** The cost to everyone
else is two changed store ops.

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
  the opaque pass to occlude it correctly.

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

**Sampling.** The shortest of the four components is `WaveLength * 0.23`, and it
needs at least two vertices across it to be a wave rather than a beat pattern
the grid invented. Given a surface `w` units wide:

```
WaveLength * 0.23 >= 2 * w / Resolution
```

Below that the shader fades the component out. Asking for short waves on a coarse
grid therefore gets smooth water rather than choppy water � the detail is
dropped rather than faked, the same trade a mip level makes.

`DefaultWaterOptions` at `Resolution` 160 over a 200-unit lake sits just under
the limit, so its finest component is faded. `Resolution` 256 carries it.

**Folding.** Gerstner's horizontal displacement is invertible only while
`sum(steepness * k * amplitude)` stays below one. Past that adjacent vertices
swap order and the surface passes through itself; the same sum also appears in
the analytic normal, so those facets shade inside out. The shader clamps
steepness against the real sum, so this cannot happen � but it means crests stop
sharpening past roughly `WaveAmplitude = WaveLength / 11` and only get taller.
