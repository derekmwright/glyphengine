---
id: day-night
title: Day/night cycle and atmosphere
summary: >
  Understand how the sky, sunlight, moonlight, fog and star visibility are all
  derived from one value — the sun's elevation — and drive that value from a
  clock in x/sky.
capability: lighting
status: stable
since: v0.3.0
api:
  - sky.DayNight
  - sky.DayNight.Advance
  - sky.DayNight.SetTimeOfDay
  - sky.DayNight.SunDir
  - sky.DayNight.SunAboveHorizon
  - sky.DayNight.SunColor
  - sky.DayNight.SunIntensity
  - sky.DayNight.SunDiscColor
  - sky.DayNight.MoonDir
  - sky.DayNight.MoonColor
  - sky.DayNight.MoonDiscColor
  - sky.DayNight.MoonIntensity
  - sky.DayNight.MoonVisible
  - sky.DayNight.PrimaryLight
  - sky.DayNight.Daylight
  - sky.DayNight.Twilight
  - sky.DayNight.StarVisibility
  - sky.DayNight.AmbientColor
  - sky.DayNight.SkyColor
  - sky.DayCycleSource
  - glyphengine.EnvironmentState.SunElevation
  - glyphengine.EnvironmentState.RealSunDir
  - glyphengine.NightGrade
  - glyphengine.DefaultNightGrade
  - glyphengine.Scene.SetNightGrade
  - glyphengine.Scene.NightGrade
  - glyphengine.SkyPalette
  - glyphengine.DefaultSkyPalette
  - glyphengine.Scene.SetSkyPalette
  - glyphengine.Scene.SkyPalette
requires:
  - environment
assets: none
example: examples/09-water
run: go run ./09-water -time 0.78
verified: 2026-10-02 # the clock and its curves moved to x/sky as sky.DayNight and sky.DayCycleSource; the atmosphere model, the night grade and the palette stayed in the engine; Scene.DayNight/TimeOfDay/SetTimeOfDay/SetDayCycleSpeed are gone (#161 step 4)
---

# Day/night cycle

**The clock is not the engine's.** `sky.DayNight` and `sky.DayCycleSource` live
in [`x/sky`](../../x/sky/sky.md): a clock, a sun and moon orbit, and a set of
hand-placed keyframe curves are a look, and the engine ships no look. What the
engine owns is the *contract* a clock writes into — `EnvironmentState`, see
[environment](environment.md) — and the atmosphere model below, which is a
function of one number out of that state and of nothing else.

A scene therefore needs two things to have a moving sun, and one without the
other does nothing:

```go
import (
    glyph "github.com/derekmwright/glyphengine"
    xsky "github.com/derekmwright/glyphengine/x/sky"
)

// 1. the shaders that draw a sky at all (the sky slot; see environment.md)
e, err := glyph.New(&game{}, glyph.WithShaders(xsky.Shaders()))

// 2. a source that says where the sun is
env := xsky.DefaultEnvironment()
env.Cycle.SetTimeOfDay(0.30)  // 0=midnight, 0.25=sunrise, 0.5=noon, 0.75=sunset
env.Cycle.Speed = 1.0 / 300   // one full cycle every 300 seconds; 0 freezes it
e.Scene.Env = env
```

`SetTimeOfDay` wraps values outside `[0,1)`; assigning `Cycle.TimeOfDay`
directly does not. Set `Speed` and the clock advances on the fixed tick through
`EnvironmentSource.Advance`, so the hour is a function of the tick count rather
than of wall time.

There is no `Scene.SetTimeOfDay`, `Scene.DayNight` or `Engine.SetDayCycleSpeed`
any more. They could only work by knowing the concrete source type, which is the
coupling the environment seam exists to remove: a game holds its own source and
sets the clock on it. `Scene.StarVisibility()` survives because it reads the
resolved state and so answers for any source at all.

`examples/09-water -time 0.78` freezes the clock at a chosen point, which is
how to inspect or screenshot a specific hour reproducibly.

## Everything derives from sun elevation

The one rule worth knowing: **the atmosphere is a function of where the sun is,
not what the clock says.** `SunDir()[1]` — the sun's height, from -1 to 1 — is
the input to all of it. It reaches the engine as
`EnvironmentState.SunElevation`, and the whole of what the engine knows about
time of day is that one float.

| Term | Curve | What it drives |
|---|---|---|
| `Daylight()` | `smoothstep(-0.18, 0.10, sunY)` | Sky's day/night blend, night desaturation |
| `Twilight()` | `exp(-(sunY/w)²)`, `w = 0.20` above the horizon and `0.115` below | Warm horizon scattering, lit cloud undersides |
| `SunIntensity()` | `smoothstep(-0.14, 0.06, sunY)` | Sun's contribution as a light |
| `MoonIntensity()` | `smoothstep(0.14, 0.34, moonY)` | Moon's contribution as a light |
| `StarVisibility()` | `1 - smoothstep(-0.30, -0.02, sunY)` | Star fade |
| `MoonDiscColor()` | `{0.85, 0.88, 0.95} × 1.5 ×` the horizon fade at `moonY` | The moon billboard's colour |

Every one of those is a `sky.DayNight` method, so all six tables of constants
are in `x/sky/daynight.go`.

`MoonDiscColor` is the odd one out: it is driven by the *moon's* elevation, not
the sun's, and it is a disc colour rather than a scattering term. It is a cycle
method because `EnvironmentState.MoonDiscColor` has to carry a finished colour --
the same way `SunDiscColor` does -- so that a replacement sky can place a moon
*and* tint it. Until the environment contract was carved it was three constants
and a fade inside the engine's draw path, which left one colour the engine owned
after a source was meant to own all of them.

`shaders/include/atmosphere.inc` holds the shader half **and it stayed in the
engine**, driven by `pc.sunColor.w` — the sun's elevation, which rides there
because `pc.sunDir` is whichever body is *currently* lighting the scene and is
the moon all night. `x/sky/sky.frag`, `x/sky/clouds.frag` and the engine's own
`lighting.inc` all include it, so the sky, the clouds and the fog that geometry
melts into cannot drift apart. That is the reason it did not move with the dome:
it is not only the dome's, and the one copy is the only form of agreement that
cannot drift.

**They are not the whole story, though.** Two curves cross the Go/GLSL line, and
they cross it in opposite directions:

- `sky.DayNight.Twilight` and `atmTwilight` are the **same function written
  twice**. Change one and you must change the other. They have already drifted
  once, when only the shader was made asymmetric — and the two now sit in
  *different modules*, so no compiler, test or review diff puts them side by
  side. The engine's exported include set is the stable half (see
  [`x/README.md`](../../x/README.md)); a package that computes the curve in Go is
  binding to its signature *and* to its constants.
- The star fade goes the other way: only Go has it. `StarVisibility()` reaches
  the shader as `EnvironmentState.StarFade` → `tint.y` in `x/sky/stars.frag`.
  There is no GLSL copy, and the dead one that used to sit in `atmosphere.inc` —
  with different constants — has been removed.

Note the lower edges sit **below** zero. The sun still lights the sky after it
has set; that is what twilight is, and cutting it off at the horizon is the
single most visible way to get this wrong.

## Twilight is independent of daylight

`Twilight()` is deliberately not gated on `Daylight()` or on star visibility.
The warm scattering is strongest *just after* the sun goes down, so anything
that fades it as night arrives deletes the sunset at the moment it should be at
its best. `TestTwilightPeaksAtTheHorizon` asserts this.

It is asymmetric because the two sides are not the same event: approaching the
horizon the warmth builds while there is still a sun lighting the air, but below
it there is progressively less lit air left. A symmetric curve wide enough for
the approach still held the horizon 37% warm at an elevation of `-0.20`, with
the stars already 71% out — a sky calling itself night while holding a sunset.

Sampling for a regression test is fiddly, and worth knowing before you write
one: over most of the range the correct and the gated curves are close enough
that no threshold separates them. They diverge most at `sunY = -0.117`, a
quarter of the way into the stars appearing — 0.376 against 0.278. That is where
the test samples, and reintroducing the gate does make it fail.

## One directional light, two bodies

The renderer has a single directional light. `PrimaryLight()` returns the sun
while it has any strength left and the moon afterwards, and the two fade ranges
are chosen so **both are exactly zero at the handover**. Otherwise the light
direction flips 180° in one frame and every shadow in the scene snaps with it.

Do not compare `TimeOfDay` against 0.25/0.75 to decide which is up. That is what
the old model did, and it put the swap at a clock boundary where the light was
still bright. Use `SunAboveHorizon()`, which now tests actual elevation.

## Night is desaturated, not merely dim — except under a lamp

`atmNightShift` in `atmosphere.inc` blends surface colours toward a blue-shifted
grey as daylight goes, applied inside `applyFog` because every lit shader calls
that exactly once as its last step.

This is not decoration. Without it a night scene is a dimmed day scene: albedos
keep announcing themselves, grass stays green, and the ground ends up brighter
and more colourful than the sky above it — which is backwards, and reads as
dusk that never finishes rather than night.

Moonlight is correspondingly weak. It started at roughly a sixth of the sun's
intensity, which is where that inversion came from.

**How much it applies is not a function of the hour alone.** Rods taking over is
a statement about how much light reaches the eye from a *surface*, and a doorstep
under a lamp is not dark. So `lighting.inc` records, per fragment, how much
luminance arrived from local lights — the clustered point and spot lights plus
the shadowed point light in the push constants — against how much came from the
sun or moon, ambient and the fog, and `applyFog` passes `atmNightShift` that
share as a third argument. The blend is scaled by `1 - share`, which is the same
thing as mixing the shifted colour back toward the unshifted one and keeps a
single early-out covering both "it is daytime" and "this is lamplight".

**How far it goes and what colour it goes are yours.** The strength and the tint
were constants in `atmosphere.inc`, which every lit surface includes — so a game
whose nights were "too grey-blue and dulled out" had to vendor the whole
lighting chain to reach two numbers. They are Scene state now:

```go
g := e.Scene.NightGrade()   // DefaultNightGrade(): Strength 0.8, Tint 0.72/0.86/1.30
g.Strength = 0.5            // a gentler night
g.Tint = mgl32.Vec3{0.80, 0.88, 1.15}
e.Scene.SetNightGrade(g)
```

`Strength 0` turns the shift off entirely. `examples/21-streetlights -nightshift 0`
and `-nighttint r,g,b` are the quickest way to see what each does; measured on
that scene's moonlit ground, strength `0.8` gives `31/36/43`, `0.4` gives
`30/38/42`, `0` gives `29/40/41` (the grass tint's own green) and a reversed
tint at full strength gives `41/36/33`.

It is on `EnvironmentState` beside fog and ambient, *and* on `Scene`: a source
that owns the look returns its own grade, a source that says nothing gets the
scene's, and `Scene.SetNightGrade` is what sets that. See
[environment](environment.md#the-palette-and-the-grade-have-two-homes-on-purpose)
for the rule and why the zero value is the sentinel rather than an answer. The
values ride to the shaders in the per-frame `ShadowData` uniform
block, appended after the cascade matrices because the push constant block is
full at its 256-byte guaranteed minimum; all seven lit fragment shaders declare
that block and must agree with `renderer/shadow.go`'s `litUBOSize`.

Two consequences worth knowing:

- **A scene with no local lights is bit-for-bit unaffected.** The share is
  exactly `0.0` when nothing local contributed, and `night * (1 - 0)` is `night`.
  That is the property the weighting was chosen for: every example in the
  `screenshots` target plus eleven night and dusk scenes render byte-identical
  across the change, all but `11-lights` and `21-streetlights`.
- **Fog is inside the ratio.** What fog mixes in is scattered skylight, so a pool
  far enough away to have faded into the haze stops counting as local and takes
  the same full shift the sky does. Lamps do not punch warm holes in blue fog.
- **A material's emission counts as lamp light.** A glowing surface is not a dark
  surface. `material_shading.inc` adds `matl.emissive` to the local side, so an
  emissive panel keeps its own colour at night instead of washing to blue-white,
  and it blends — a dim emissive over moonlit albedo keeps a share of the grade
  in proportion to how much of the fragment's light it is, with no brightness at
  which it snaps. A material with no emissive factor contributes exactly `0.0`
  and cannot be moved by this.

The debug light heatmap (`Engine.SetLightDebugMode`) is exempt: it is a number
drawn in false colour, not a surface, and grading it turned the whole ramp
indigo at midnight. `applyFog` hands it back untouched.

Before this, the blend keyed on `sunY` alone, and at full night it was provably
one-sided: expanding it gives `g - b = 0.2*(c.g - c.b) - 0.352*luminance(c)`, and
the right-hand term wins for every colour with no negative channel. A warm lamp
could not produce red > green > blue on any surface at any intensity.
`21-streetlights` had been worked around with a saturated red light over a
purpose-built dirt patch and still read pink. `task nightlight` is the gate; see
`cmd/lampcheck` for the ablation that shows it fails when the weighting goes.

`water.frag` shades its own surface rather than calling either
`evalLighting*`, so it sets `lightLocalLum` and `lightSkyLum` by hand from its
own finished terms. Its local side is the lamp reflections and nothing else,
which means two things are counted as sky that are not purely sky: the
refracted scene, which arrives from the opaque pass already lit and already
graded with no way to say how much of it was lamplight, and the Fresnel sky
reflection. Both err toward calling light "sky", so lit water comes out
slightly cooler than the shore beside it rather than warmer. See
[water](water.md#where-it-is-approximate).

## Changing the sky's appearance

If what you want is different **colours**, they are data:

```go
p := e.Scene.SkyPalette()   // DefaultSkyPalette(): Earth's
p.ZenithDay = mgl32.Vec3{0.30, 0.10, 0.62}
p.HorizonDay = mgl32.Vec3{0.95, 0.55, 0.22}
e.Scene.SetSkyPalette(p)
```

Six endpoints — zenith and horizon for day, twilight and night — mixed by the
same two curves as everything else on this page: night toward day on
`Daylight()`, then toward twilight on `Twilight()`. `examples/09-water -alien`
sets a violet-and-amber one. See
[environment](environment.md#a-sky-that-is-not-earths).

They are one value for the whole atmosphere, which is the point: `applyFog`
fades distant geometry toward the same horizon colour and water reflects the
dome, so replacing `x/sky/sky.frag` alone gives a violet sky over a landscape
still hazing into Earth-blue. The palette rides in the per-frame `ShadowData`
block that `lighting.inc` already reads the night grade from, and
`x/sky/sky.frag` and `x/sky/clouds.frag` bind that same buffer at binding 1 of
the cloud descriptor set —
the same buffer rather than a second copy, so there is nothing for them to
drift from. `task skypalette` measures that the sky at the horizon, the fogged
terrain beside it, the water's reflection and the shaded core of a cloud all
move together.

For anything that is not a colour — a different scattering model, two suns, a
sky that owes nothing to Rayleigh — supply your own fragment stages in the sky
slot through `glyphengine.WithShaders` from `glyph.New`, or
`renderer.WithShaders` if you drive the renderer directly. `sky.Fill` is how to
take some of `x/sky`'s and replace the rest. See
[`game-loop.md`](game-loop.md#replacing-an-engine-shader).

The clear colour behind it is `EnvironmentState.ClearColor` — `sky.Environment`'s
`ClearColor` field for this package's source — and is only seen when no dome is
drawn, since the dome is opaque and drawn first.

## Failure modes

- **The sky is bright blue at midnight.** Something is feeding the atmosphere
  `pc.sunDir.y` instead of `pc.sunColor.w`. `pc.sunDir` is the *current light*,
  which is the moon at night, and the moon is high exactly when the sky should
  be darkest.
- **Shadows snap 180°.** Something is choosing sun-vs-moon on the clock rather
  than on intensity.
- **Sunset vanishes as it peaks.** Something has reintroduced a `(1 - night)`
  factor on the glow.
- **Dusk is over in a moment.** The transition width is set by the elevation
  windows above, not by cycle speed. Widen them rather than slowing the clock,
  or day and night get longer too.
- **The horizon glow is right in the sky and wrong on the terrain**, or in the
  water's reflection. `sky.DayNight.Twilight` and `atmTwilight` have drifted
  apart — they are in two modules now, so check both.
- **Editing the star fade changes nothing.** It lives in Go, not in GLSL —
  `sky.DayNight.StarVisibility`, not `atmosphere.inc`. See [stars](stars.md).
- **Nothing is drawn in the sky at all, at any hour.** The source is only half of
  it: with no sky shaders in the `ShaderSet` the renderer builds no dome, star or
  cloud pipeline, so `DrawSky` is overridden to false and the frame keeps its
  clear colour. Pass `glyph.WithShaders(xsky.Shaders())`. See
  [environment](environment.md).
- **`e.SetTimeOfDay` or `e.Scene.DayNight()` does not compile.** They are gone;
  hold the source and set `Cycle.SetTimeOfDay`. See the top of this page.
- **A warm lamp reads cold at night.** The night shift is not being told the
  fragment is lamplit. Either the shader shades without going through
  `evalLighting`/`evalLightingAO`/`evalLightingDiffuse` and without setting
  `lightLocalLum` itself — `water.frag` shades its own surface and does set it
  — or the light is not in the local set: the directional light is the moon
  after dusk and counts as sky, however warm it is made.

## Watching it happen

Elevation curves are much easier to reason about with the number on screen. Both
`Engine.Debugf` — see [game-loop](game-loop.md) — and the `-time` flag are there
for this.

The game keeps its own source, so there are two readouts and they answer
different questions:

```go
// g.env is the *xsky.Environment this game built in Init.
e.Debugf("t=%.3f  sunY=%+.3f  twilight=%.2f  stars=%.2f",
    g.env.Cycle.TimeOfDay, g.env.Cycle.SunDir()[1],
    g.env.Cycle.Twilight(), g.env.Cycle.StarVisibility())

// What the FRAME was actually lit by, whatever source produced it.
e.Debugf("sun %+.4f", e.Scene.Environment().SunElevation)
```

The second works for any source and is the one to reach for when the question is
"why does this frame look like that"; the first needs the concrete type and is
how to watch a curve. `examples/08-grass` prints both and is the quickest scene
to check a curve in — it holds the environment on its game struct for exactly
this reason.

Freeze the clock with `-time` to compare two builds at the same instant, and set
`GLYPHENGINE_FIXED_FRAME_TIME` if you are diffing screenshots — see `AGENTS.md`
rule 13. Without it two runs of the same build differ by more than most changes.

## Regression tests

`x/sky/daynight_test.go` encodes the above, and moved there with the curves.
`TestCycleIsContinuous` is the important one: it samples every curve 400 times
per cycle and fails if any crosses more than a fifth of its own range in one
step. The original bug crossed 100% — the sun's light went from 0.585 to 0.000
between two adjacent samples.

`x/sky/environment_test.go` carries the other half: twenty-five resolved states
round the clock, pinned at full float32 precision against what the engine
produced before the cycle moved, so "the curves came through the move unchanged"
is a measurement rather than a hope. `task skymigration` is the same claim about
the pixels.

`Twilight()`'s two widths meet at `sunY = 0`, and that join is smoother than it
looks: a Gaussian's slope is zero at its peak whichever width it has, so value
*and* slope are continuous there. Only the curvature jumps, which is why the
asymmetry is invisible on screen and why this test does not object to it.
