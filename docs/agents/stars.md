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
  - glyphengine.Sky.Stars
  - glyphengine.Sky.StarDensity
  - glyphengine.Sky.MilkyWay
  - glyphengine.Scene.StarVisibility
  - glyphengine.EnvironmentState.StarFade
  - renderer.Renderer.SetMilkyWayTexture
  - renderer.EquirectToSkyMap
requires:
  - environment
  - day-night
assets: none
example: examples/09-water
run: go run ./09-water -time 0.02
verified: 2026-08-06
---

# Stars

Part of `Sky`, so it arrives with `DefaultEnvironment()` and needs nothing to
turn on:

```go
env.Sky = &glyph.Sky{
    Stars:       true, // the pass runs at all
    StarDensity: 1,    // scales the field; 0 leaves an empty sky
    MilkyWay:    1,    // galactic band strength, 0 to 1
}
```

`DefaultSky()` sets all three. Everything is procedural — the engine ships no
sky image.

`go run ./09-water -time 0.02` freezes the clock just past midnight, which is
where to look at any of this.

## What is actually drawn

One fullscreen pass, no vertex buffer, additive. It runs after the sky dome and
composites far to near:

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

`DayNight.StarVisibility()` → `EnvironmentState.StarFade` → `tint.y`. There is **no
GLSL copy** — a dead one used to sit in `atmosphere.inc` with different
constants, and it has been removed. Editing the shader will not move the fade.
See [day-night](day-night.md).

`Environment` also gates the pass entirely:

```go
s.DrawStars = env.Sky.Stars && s.StarFade > 0
```

so a daytime frame costs nothing, and the shader returns early on
`nightFactor <= 0` besides.

`Scene.StarVisibility()` reads the resolved value, which is how game code hangs
things off nightfall — `examples/12-particles` starts its fireflies on it.

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
guards the two conventions against each other.

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
expensive thing in this part of the frame. See
[environment](environment.md#clouds-are-a-graphics-setting).

A supplied panorama is *cheaper* than the procedural band: one texture fetch
against roughly fifty hashes.

## Failure modes

- **No stars at night.** `Sky` is nil, `Stars` is false, or the scene has no
  `Cycle` and `FixedSunElevation` is above the fade — `StarFade` is 0 and the
  pass never runs.
- **`StarDensity: 0` still shows a band.** It scales the star field only. Set
  `MilkyWay: 0` for the galaxy.
- **Editing `atmosphere.inc` does not change the fade.** It is in Go. See above.
- **A supplied panorama looks mirrored or smeared.** It was bound without
  `EquirectToSkyMap`.
- **The band looks like a cloud.** Something has smoothed the grain, or
  stretched the noise. The band's own falloff supplies the long shape; stretching
  the noise on top of it turns every feature into a streak.
- **Stars crawl or shimmer as the camera turns.** Something is sampling
  high-frequency noise per pixel instead of drawing points with a footprint, or
  the regional density noise has been made fine enough to vary within one star.
