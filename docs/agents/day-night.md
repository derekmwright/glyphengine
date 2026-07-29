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
verified: 2026-07-28
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
| `Twilight()` | Gaussian peaking at `sunY = 0` | Warm horizon scattering, lit cloud undersides |
| `SunIntensity()` | `smoothstep(-0.14, 0.06, sunY)` | Sun's contribution as a light |
| `MoonIntensity()` | `smoothstep(0.14, 0.34, moonY)` | Moon's contribution as a light |
| `StarVisibility()` | `1 - smoothstep(-0.30, -0.02, sunY)` | Star fade |

The shaders compute the same curves from `pc.sunColor.w` — see
`shaders/atmosphere.inc`, which the sky dome and `lighting.inc` both include, so
the sky and the fog that geometry melts into cannot drift apart. They used to be
two copies of the same constants kept in step by a comment.

Note the lower edges sit **below** zero. The sun still lights the sky after it
has set; that is what twilight is, and cutting it off at the horizon is the
single most visible way to get this wrong.

## Twilight is independent of daylight

`Twilight()` is deliberately not gated on `Daylight()` or on star visibility.
The warm scattering is strongest *just after* the sun goes down, so anything
that fades it as night arrives deletes the sunset at the moment it should be at
its best. `TestTwilightPeaksAtTheHorizon` asserts this.

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

## Regression tests

`daynight_test.go` encodes the above. `TestCycleIsContinuous` is the important
one: it samples every curve 400 times per cycle and fails if any crosses more
than a fifth of its own range in one step. The original bug crossed 100% — the
sun's light went from 0.585 to 0.000 between two adjacent samples.
