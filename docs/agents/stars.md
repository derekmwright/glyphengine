---
id: stars
title: Star field and galactic band
summary: >
  Draw a night sky: a procedural star field, a procedural Milky Way, or a real
  all-sky panorama resampled into the engine's sky map.
capability: environment
status: stable
since: v0.4.0
api:
  - sky.Sky.Stars
  - sky.Sky.StarDensity
  - sky.Sky.MilkyWay
  - sky.Shaders
  - sky.Fill
  - glyphengine.Scene.StarVisibility
  - glyphengine.EnvironmentState.StarFade
  - glyphengine.EnvironmentState.StarDensity
  - glyphengine.EnvironmentState.MilkyWay
  - glyphengine.EnvironmentState.DrawStars
  - renderer.ShaderSet.StarsFrag
  - renderer.SceneLighting.StarDensity
  - renderer.SceneLighting.MilkyWay
  - renderer.Renderer.SetMilkyWayTexture
  - renderer.EquirectToSkyMap
requires:
  - environment
  - day-night
assets: none
example: examples/09-water
run: go run ./09-water -time 0.02
verified: 2026-10-02 # the procedural field and band are x/sky/stars.frag now, in the sky slot; the pass, the blend, the panorama slot, EquirectToSkyMap and the octahedral convention stayed in the engine (#161 step 4)
---

# Stars

The star field and the galactic band are **two halves in two modules**, and both
are needed. The engine owns the pass — one fullscreen additive draw at the
reverse-Z far plane, after the dome — and the per-frame values it draws with. The
*look* is `x/sky/stars.frag`, which the engine embeds nothing for: `StarsFrag` is
one of the three stages in the sky slot (see
[environment](environment.md)), and with it nil the pipeline is never created and
nothing is drawn, whatever `DrawStars` says.

```go
import (
    glyph "github.com/derekmwright/glyphengine"
    xsky "github.com/derekmwright/glyphengine/x/sky"
)

// the shader half
e, err := glyph.New(&game{}, glyph.WithShaders(xsky.Shaders()))

// the values half
env := xsky.DefaultEnvironment()
env.Sky.Stars = true       // the pass runs at all
env.Sky.StarDensity = 1    // scales the field; 0 leaves an empty sky
env.Sky.MilkyWay = 1       // galactic band strength, 0 to 1
e.Scene.Env = env
```

`sky.DefaultSky()` sets all three, so `sky.DefaultEnvironment()` needs nothing.
Everything is procedural — neither the engine nor `x/sky` ships a sky image. See
[`x/sky/sky.md`](../../x/sky/sky.md) for the package, and
[`x/README.md`](../../x/README.md) for why a look lives outside the engine.

`go run ./09-water -time 0.02` freezes the clock just past midnight, which is
where to look at any of this.

## What is actually drawn

One fullscreen pass, no vertex buffer, additive — the pipeline, the blend and the
far-plane depth state are the engine's, and so is `shaders/stars.vert`, which is
the same fullscreen triangle eight passes use. What follows is what
`x/sky/stars.frag` puts in it. It runs after the sky dome and composites far to
near:

1. **The galactic band** — a two-component profile across the galactic plane, a
   narrow bright spine (`exp(-lat²·75)`) inside a wide faint halo
   (`exp(-lat²·16)`). One low-frequency cloud field drives four layers that all
   read off it, so they nest rather than fight: an amber-to-purple midtone, hot
   white highlights confined to a lane along the spine, dust blocked in over the
   top, and grain.
2. **The band's own grain** — three dense layers of very faint points carrying
   the band's colour. These are the galaxy's unresolved stars, so the dust in
   front of them occludes them.
3. **The star field** — three layers at 60, 110 and 200 cells, bright and rare
   through faint and dense, each point tinted blue-white to amber by a hash and
   twinkling at a different rate.
4. Fade by `nightFactor` and a horizon `smoothstep(0, 0.08, dir.y)`.

The star field is **nearer than the galaxy**, so the dust does not occlude it. A
dust lane with foreground stars across it is what the sky looks like; occluding
them to make the dust read as solid puts the whole field behind the galaxy.

### Grain, not noise

The thing that stops a bright band reading as *weather* is that it is made of
stars. A smooth luminous mass with soft edges is a cloud whatever colour it is —
the first four attempts here all looked like it.

The grain is therefore drawn as points with a footprint
(`exp(-dist²·130)`), not as high-frequency noise. Noise crawls the moment the
camera turns; points sit still because they are anchored to a direction.

### Density is not uniform

A flat field reads as a texture — the eye finds the regularity immediately. A
low-frequency value noise, `smoothstep(0.30, 0.78, vnoise(dir·2.3))`, thins
whole regions to 20% and leaves others crowded.

One sample per pixel rather than per cell, which is safe only because the noise
is far coarser than a star: every pixel of a given star reads essentially the
same value, so they cannot disagree about whether it exists and flicker along
its edge.

## The fade lives in Go

`sky.DayNight.StarVisibility` → `EnvironmentState.StarFade` →
`SceneLighting.NightFactor` → `tint.y`. There is **no GLSL copy** — a dead one
used to sit in `atmosphere.inc` with different constants, and it has been
removed. Editing the shader will not move the fade. See
[day-night](day-night.md).

The source also gates the pass entirely, in `x/sky`'s `resolveSky`:

```go
s.DrawStars = a.sky.Stars && s.StarFade > 0
```

so a daytime frame costs nothing, and the shader returns early on
`nightFactor <= 0` besides.

`Scene.StarVisibility()` reads the resolved value, and it is the one day/night
convenience the engine kept, because it asks the frame rather than reaching for a
clock: it answers for a custom source too. That is how game code hangs things off
nightfall — `examples/12-particles` starts its fireflies on it, and
`examples/09-water` brightens its lamps on it.

## A real panorama

`MilkyWay` can be replaced with a photographic all-sky survey:

```go
img := // decoded RGBA equirect, longitude across, latitude down
skyMap := renderer.EquirectToSkyMap(img.Pix, w, h, w/4)
tex, err := e.Renderer().CreateTexture(skyMap, w/4, w/4)
if err != nil {
    return err
}
e.Renderer().SetMilkyWayTexture(tex)
```

`examples/09-water -milkyway path.png` does exactly this.

**The resample is not optional.** The pass samples a *hemi-octahedral* map — the
upper hemisphere folded onto a square — and binding a raw equirect draws a
mirrored, smeared sky rather than failing. `TestSkyMapMatchesTheShaderProjection`
in `renderer/skymap_test.go` guards the two conventions against each other, by
transcribing the shader's fold into Go and checking it inverts
`EquirectToSkyMap`.

**That fold is now a cross-module convention**, and worth knowing before changing
either side. `EquirectToSkyMap` and the texture slot are the engine's; the
expression that decodes them is in `x/sky/stars.frag`. The test's whole premise is
that nothing checks the two against each other at build time — it was already a
language boundary, and it is a module boundary as well now, so a replacement star
shader that folds the square differently gets a mirrored panorama and a green
test. Any shader supplied in the `StarsFrag` slot that samples the Milky Way
texture is binding to this convention, the same way it binds to the descriptor
numbers in [render-targets](render-targets.md).

Hemi-octahedral rather than equirect because an equirect wastes half its texels
below a horizon the pass never samples, seams where `atan2` wraps — visibly,
since the UV derivative jumps there and takes mip selection with it — and
pinches at the zenith. The octahedral square has none of those and decodes with
arithmetic instead of trig. The galactic rotation is baked into the map, so the
shader does not rebuild a basis per fragment.

**Strip the point stars from the source.** The renderer draws its own and they
are sharp at any resolution; a panorama's are not. A 6000-pixel-wide equirect is
16.7 pixels per degree against the 23 a 1280-wide window at 55° needs, so its
stars arrive soft while its band — which has no detail that fine — arrives
intact. A median filter removes the points and leaves the band.

A supplied panorama replaces the procedural band outright rather than blending
with it, and `MilkyWay` still scales it.

## Cost

The band's noise is branched around when `MilkyWay` is zero, and the whole pass
is skipped in daylight, so neither costs anything when off. When on it is one
fullscreen pass of hash lookups — cheap next to the cloud raymarch, which is the
expensive thing in this part of the frame. See [clouds](clouds.md).

A supplied panorama is *cheaper* than the procedural band: one texture fetch
against roughly fifty hashes.

## Failure modes

- **No stars at night, and no dome either.** The sky slot is empty: nothing was
  passed to `WithShaders`, so the engine built no star pipeline. The values half
  alone draws nothing. Pass `glyph.WithShaders(xsky.Shaders())`.
- **No stars at night.** `Sky` is nil, `Stars` is false, or the scene has no
  `Cycle` and `FixedSunElevation` is above the fade — `StarFade` is 0 and the
  pass never runs.
- **`StarDensity: 0` still shows a band.** It scales the star field only. Set
  `MilkyWay: 0` for the galaxy.
- **Editing `atmosphere.inc` does not change the fade.** It is in Go. See above.
- **Editing `shaders/` does not change the field.** The star shader is
  `x/sky/stars.frag`; `shaders/stars.vert` is only the fullscreen triangle.
  Recompile with `task xsky:shaders` — the committed `.spv` is what is embedded,
  and `x/sky/spirv_test.go` fails if the two have parted company.
- **A supplied panorama looks mirrored or smeared.** It was bound without
  `EquirectToSkyMap`, or a replacement star shader folds the octahedral square
  the other way. See above.
- **The band looks like a cloud.** Something has smoothed the grain, or
  stretched the noise. The band's own falloff supplies the long shape; stretching
  the noise on top of it turns every feature into a streak.
- **Stars crawl or shimmer as the camera turns.** Something is sampling
  high-frequency noise per pixel instead of drawing points with a footprint, or
  the regional density noise has been made fine enough to vary within one star.
