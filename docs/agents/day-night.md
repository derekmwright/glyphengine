---
id: day-night
title: Day/night cycle and atmosphere
summary: >
  Drive time of day, and understand how the sky, sunlight, moonlight, fog and
  star visibility are all derived from one value: the sun's elevation.
capability: lighting
status: stable
since: v0.3.0
api:
  - glyphengine.DayNight
  - glyphengine.DayNight.SunDir
  - glyphengine.DayNight.SunAboveHorizon
  - glyphengine.DayNight.SunColor
  - glyphengine.DayNight.SunIntensity
  - glyphengine.DayNight.SunDiscColor
  - glyphengine.DayNight.MoonColor
  - glyphengine.DayNight.MoonIntensity
  - glyphengine.DayNight.PrimaryLight
  - glyphengine.DayNight.Daylight
  - glyphengine.DayNight.Twilight
  - glyphengine.DayNight.StarVisibility
  - glyphengine.DayNight.AmbientColor
  - glyphengine.Engine.SetTimeOfDay
  - glyphengine.Engine.SetDayCycleSpeed
requires:
  - environment
assets: none
example: examples/09-water
run: go run ./09-water -time 0.78
verified: 2026-08-06
---

# Day/night cycle

The cycle is one piece of the scene's `Environment`, and optional like the
rest — see [environment](environment.md). A scene with no `Cycle` has no
passage of time; a scene with no `Env` at all has no sky or sun either.

```go
e.SetTimeOfDay(0.30)          // 0=midnight, 0.25=sunrise, 0.5=noon, 0.75=sunset
e.SetDayCycleSpeed(1.0 / 300) // one full cycle every 300 seconds; 0 freezes it
```

These reach through to the built-in `Environment` and are no-ops under a custom
`EnvironmentSource`. `Scene.DayNight()` returns nil there.

`examples/09-water -time 0.78` freezes the clock at a chosen point, which is
how to inspect or screenshot a specific hour reproducibly.

## Everything derives from sun elevation

The one rule worth knowing: **the atmosphere is a function of where the sun is,
not what the clock says.** `SunDir()[1]` — the sun's height, from -1 to 1 — is
the input to all of it.

| Term | Curve | What it drives |
|---|---|---|
| `Daylight()` | `smoothstep(-0.18, 0.10, sunY)` | Sky's day/night blend, night desaturation |
| `Twilight()` | `exp(-(sunY/w)²)`, `w = 0.20` above the horizon and `0.115` below | Warm horizon scattering, lit cloud undersides |
| `SunIntensity()` | `smoothstep(-0.14, 0.06, sunY)` | Sun's contribution as a light |
| `MoonIntensity()` | `smoothstep(0.14, 0.34, moonY)` | Moon's contribution as a light |
| `StarVisibility()` | `1 - smoothstep(-0.30, -0.02, sunY)` | Star fade |

`shaders/atmosphere.inc` holds the shader half, driven by `pc.sunColor.w` — the
sun's elevation, which rides there because `pc.sunDir` is whichever body is
*currently* lighting the scene and is the moon all night. `sky.frag`,
`clouds.frag` and `lighting.inc` all include it, so the sky, the clouds and the
fog that geometry melts into cannot drift apart.

**They are not the whole story, though.** Two curves cross the Go/GLSL line, and
they cross it in opposite directions:

- `Twilight()` and `atmTwilight` are the **same function written twice**. Change
  one and you must change the other. They have already drifted once, when only
  the shader was made asymmetric.
- The star fade goes the other way: only Go has it. `StarVisibility()` reaches
  the shader as `Environment.StarFade` → `tint.y` in `stars.frag`. There is no
  GLSL copy, and the dead one that used to sit in `atmosphere.inc` — with
  different constants — has been removed.

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

## Night is desaturated, not merely dim

`atmNightShift` in `atmosphere.inc` blends surface colours toward a blue-shifted
grey as daylight goes, applied inside `applyFog` because every lit shader calls
that exactly once as its last step.

This is not decoration. Without it a night scene is a dimmed day scene: albedos
keep announcing themselves, grass stays green, and the ground ends up brighter
and more colourful than the sky above it — which is backwards, and reads as
dusk that never finishes rather than night.

Moonlight is correspondingly weak. It started at roughly a sixth of the sun's
intensity, which is where that inversion came from.

## Changing the sky's appearance

The visible sky comes from `shaders/atmosphere.inc` via `sky.frag`;
`atmSkyPalette` is the place. To replace it wholesale, swap the sky shaders
through `renderer.WithShaders`.

The clear colour behind it is `Environment.ClearColor`, and is only seen when
`Sky` is nil — the dome is opaque and drawn first.

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
  water's reflection. `Twilight()` and `atmTwilight` have drifted apart.
- **Editing the star fade changes nothing.** It lives in Go, not in GLSL —
  `StarVisibility()`, not `atmosphere.inc`. See [stars](stars.md).

## Watching it happen

Elevation curves are much easier to reason about with the number on screen. Both
`Engine.Debugf` — see [game-loop](game-loop.md) — and the `-time` flag are there
for this:

```go
if dn := e.Scene.DayNight(); dn != nil { // nil under a custom EnvironmentSource
    e.Debugf("t=%.3f  sunY=%+.3f  twilight=%.2f  stars=%.2f",
        dn.TimeOfDay, dn.SunDir()[1], dn.Twilight(), dn.StarVisibility())
}
```

`examples/08-grass` does exactly this and is the quickest scene to check a
curve in.

Freeze the clock with `-time` to compare two builds at the same instant, and set
`GLYPHENGINE_FIXED_FRAME_TIME` if you are diffing screenshots — see `AGENTS.md`
rule 13. Without it two runs of the same build differ by more than most changes.

## Regression tests

`daynight_test.go` encodes the above. `TestCycleIsContinuous` is the important
one: it samples every curve 400 times per cycle and fails if any crosses more
than a fifth of its own range in one step. The original bug crossed 100% — the
sun's light went from 0.585 to 0.000 between two adjacent samples.

`Twilight()`'s two widths meet at `sunY = 0`, and that join is smoother than it
looks: a Gaussian's slope is zero at its peak whichever width it has, so value
*and* slope are continuous there. Only the curvature jumps, which is why the
asymmetry is invisible on screen and why this test does not object to it.
