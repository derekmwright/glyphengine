---
id: environment
title: Environment — sky, light, fog
summary: >
  Compose a scene's sky, directional light, ambient and fog from independent
  optional pieces, or replace the whole model with your own implementation.
capability: environment
status: stable
since: v0.3.0
api:
  - glyphengine.EnvironmentSource
  - glyphengine.EnvironmentState
  - glyphengine.Environment
  - glyphengine.DefaultEnvironment
  - glyphengine.Sky
  - glyphengine.DefaultSky
  - glyphengine.LightShaftShape
  - glyphengine.DefaultLightShaftShape
  - glyphengine.DirectionalLight
  - glyphengine.AmbientLight
  - glyphengine.Fog
  - glyphengine.Scene.Env
  - glyphengine.Scene.Environment
  - glyphengine.Scene.SetTimeOfDay
  - glyphengine.Scene.SetDayCycleSpeed
  - glyphengine.Engine.SetFogDensity
  - glyphengine.Scene.SetNightGrade
  - glyphengine.SkyPalette
  - glyphengine.DefaultSkyPalette
  - glyphengine.Scene.SetSkyPalette
  - glyphengine.Scene.SkyPalette
requires: []
assets: none
example: examples/09-water
run: go run ./09-water -alien
verified: 2026-09-19
---

# Environment

`Scene.Env` holds the sky, the light and the air. It is an interface, so it can
be composed from the engine's pieces or replaced entirely.

```go
// The default: full sky, a cycle frozen at sunrise, light haze.
scene.Env = glyph.DefaultEnvironment()

// An interior: no sky, no sun, no weather.
scene.Env = &glyph.Environment{
    Ambient:    &glyph.AmbientLight{Color: [3]float32{0.18, 0.17, 0.20}},
    Sun:        &glyph.DirectionalLight{
        Direction: [3]float32{0.4, 0.8, 0.3},
        Color:     [3]float32{0.7, 0.68, 0.62},
    },
    ClearColor: [3]float32{0.03, 0.03, 0.045},
}

// Nothing. Black background, and only the lights you place yourself.
scene.Env = nil
```

A new `Scene` gets `DefaultEnvironment()`, so a game that says nothing still
opens onto a lit world. Everything past that is a decision.

## The pieces are independent

| Piece | Nil means |
|---|---|
| `Cycle` | Time does not pass. `Sun` and `Ambient` supply the light instead |
| `Sky` | No dome, no discs, no stars ([stars](stars.md)). The frame clears to `ClearColor` |
| `Sun` | No directional light (unless `Cycle` provides one) |
| `Ambient` | No fill light (unless `Cycle` provides one) |
| `Fog` | No distance fog |

They combine in the ways you would expect, with one rule worth stating: **a
`Cycle` overrides `Sun` and `Ambient`.** A cycle already knows where the sun is
and what colour the sky is casting; a fixed light alongside it would be a second
answer to the same question.

Useful combinations:

- `Sky` without `Cycle` — a static sky at a fixed hour. Set
  `Sky.FixedSunElevation` to pick which one; the star fade derives from it too,
  so a fixed elevation below the horizon gets a real night sky rather than an
  empty one. See [stars](stars.md).
- `Cycle` without `Sky` — the engine's sun, moon and ambient driving *your*
  skybox. The light works; nothing is drawn.
- `Sky` with `SunDisc: false` — sky colour and light without a visible sun.

## Clouds are a graphics setting

`Sky.CloudSteps` controls the volumetric cloud raymarch and is the most
expensive thing the engine draws per pixel. It exists to be wired to a settings
menu, not left at a constant. Measured at 1280x720, MSAA 4x, on a Radeon RX
7900 XTX, whole frame:

| Setting | Frame | FPS |
|---|---|---|
| `CloudsOff` | 0.28 ms | 3593 |
| `CloudsLow` (16) | 0.76 ms | 1323 |
| `CloudsHigh` (32) | 1.11 ms | 898 |

Those are one GPU's numbers; the ratios transfer better than the absolutes.
Any integer works, not just the presets.

Those are also **whole-frame differences**, taken before the engine could time a
pass. `task bench` measures each pass directly now and broadly confirms them —
the sky pass is 83–93% of GPU time in `02-cube`, `07-terrain`, `09-water`,
`12-particles` and `16-materials`.

The exception is flora, and it inverts the advice. Grass overdraws itself
heavily while the sky is one layer deep and depth-rejected wherever terrain
covers it, so in `08-grass` the split is grass 3.95 ms against sky 1.68 ms, and
in `15-kitchen-sink` 4.30 against 1.20. In a scene with ground cover, clouds are
no longer the first thing to reach for. Measure — in either direction. See
[profiling](profiling.md).

It is safe to change every frame — the value is read when the environment
resolves, so a slider takes effect on the next frame with nothing to rebuild.

The sky is drawn **after** opaque geometry and depth-tested against the far
plane, so none of this runs for a pixel the terrain covers. That reordering is
worth about 12% on its own and is what makes a raymarched sky affordable at
all; before it, a fullscreen sky shaded every pixel and the world painted over
most of them.

## Light shafts are on, and only near the sun

`Sky.LightShafts` is the strength of screen-space light shafts — the smear of
brightness radiating from the sun past whatever occludes it. `DefaultSky()` sets
**0.25**, so a game that says nothing gets them in every frame with the sun
above the horizon and in view.

They are a **screen-space radial blur**, not volumetrics. The pass samples the
copy of the scene the water pass already makes, keeps only what is bright enough
to be sky or sun, and smears it outward from the sun's projected position.
Geometry standing between the eye and the sun is darker than the sky behind it,
so it contributes nothing and leaves a gap — and a gap in a radial smear reads
as a shaft. That is the whole mechanism, and its limits follow from it:

- **Only while the sun is on screen.** The effect is built from pixels, so there
  is nothing to build from once the sun leaves the frame. The strength fades out
  as the sun approaches the edge rather than cutting, and reaches zero once the
  sun is 0.175 of a frame past it. As a fraction of what the game asked for,
  measured off the strength the renderer is handed: 0.98 with the sun at u =
  0.90, 0.80 at 0.96, 0.46 at 1.03, 0.08 at 1.12 and nothing at 1.27.
- **Only while the sun disc is drawn.** Below the horizon the strength fades
  with the disc, over elevation −0.02 to −0.15, and at night it is exactly zero.
  The moon gets none: the pass is anchored to the sun, and issue #47 is the ask
  for shafts from other lights.
- **No depth.** The pass cannot tell air in front of a distant hill from the
  ground two metres away, so what keeps it off the foreground is distance from
  the sun *on screen* — a lobe 0.9 screen heights wide by default
  (`LightShaftShape.Radius`). Point the camera
  so that the sun is directly over near ground and that ground will haze, because
  from the pass's point of view it is exactly where the air should be.
- **A `Sky` without a `Cycle` gets none.** The shafts radiate from the sun
  billboard, and only a `Cycle` places one.

### Choosing a strength

Measured on `09-water -time 0.72 -yaw 1.771 -pitch -0.185 -pillars` — dusk, the
sun coming up over a ridge behind a row of pillars — as mean sRGB luma added
against the same frame with the shafts off:

| `LightShafts` | Ground lit through a gap | The occluder itself | Whole frame |
|---|---|---|---|
| 0.20 | +26.0 | +29.1 | +5.7 |
| 0.25 (default) | +31.2 | +34.7 | +6.8 |
| 0.35 | +40.7 | +44.7 | +8.9 |
| 0.50 | +53.1 | +57.7 | +11.6 |

**Read the second column, not the first.** The sky in the gaps beside a setting
sun is already at the top of the display range, so the only pixels with headroom
left to brighten are the dark ones — which means the number that decides whether
this reads as light or as a dirty lens is the one on the silhouette. Pillar 4 in
that scene sits at 67 with the shafts off, against 245 for the sky beside it; at
0.25 it goes to 102 and still reads as a silhouette, at 0.35 to 112, and at 1.0
to 158, by which point it is a pale shape in front of a white sky rather than a
dark one.

0.25 is the default for that reason: a deliberate step down from the 0.35 this
field carried for seven weeks without ever drawing a pixel, because 0.35 is the
setting that, the first time it was drawn, was described as heavy enough to
flatten the pillars' silhouettes. It is still well past where the shafts are
obvious — the gap gains 31 levels and the streak the pillar casts down the
hillside is plain at 1:1 — and a game that wants the drama back has one field to
change.

At midday the same setting is a soft halo around the disc and little else, which
is what a midday sun does: the plain sky measures 0.566 in linear right beside
the disc and the pass's threshold starts at 0.62, so only the disc and the
clouds contribute.

### Tuning the shape

Strength is one number; how the shafts *look* is three more, on
`Sky.LightShaftShape`. Each field's zero value keeps the engine's default for
that field, so a game sets only the one it cares about:

```go
sky.LightShaftShape.Radius = 1.3 // reach further across the frame
```

| Field | Default | What it does |
|---|---|---|
| `Radius` | 0.90 | How far from the sun the shafts reach, in screen heights. It stands in for the depth this pass does not have, so widening it is what brings the haze on near ground back. |
| `Decay` | 0.96 | The weight each of the 48 steps toward the sun keeps from the one before, in (0, 1]. Lower gives an occluder a harder streak and the shafts less reach; 1 is an even wash. |
| `Threshold` | {0.62, 0.88} | The linear-luminance window a pixel has to clear to count as a source. It is what makes terrain an occluder. |

Measured on the same dusk scene at the default strength, as light added through
the gap, in the pillar's streak, and their ratio:

| Shape | Gap | Streak | Ratio |
|---|---|---|---|
| default | +31.2 | +7.4 | 4.23 |
| `Radius: 1.3` | +37.2 | +9.1 | 4.11 |
| `Decay: 0.90` | +7.4 | +1.1 | 6.93 |
| `Threshold: {0.30, 0.50}` | +31.4 | +7.4 | 4.25 |

`Threshold` barely moves that scene, because at dusk every source -- the disc at
5.0, the glow and the cloud above 0.85 -- clears either window. Where it matters
is a sky whose *plain* blue sits near the window. At midday
(`-time 0.5 -yaw 3.14159 -pitch -1.371`) the sky beside the sun gains +8.0 with
the default window and +21.3 with `{0.30, 0.50}`, which admits the plain sky and
starts smearing the dome into itself. That is the reason the window is data:
its defaults are measurements of the **default sky palette**, and a game that
calls `SetSkyPalette` is changing what they measured. A much brighter sky wants
the window raised, or it floods; a much dimmer one wants it lowered, or nothing
clears it and the pass draws nothing while still being paid for. The alien
palette `09-water -alien` ships with needs no change -- the dusk gate reads
+30.5 / +7.3 under it, against +31.2 / +7.4 -- but that is a property of that
palette, not a guarantee.

Values that cannot be drawn with are replaced rather than rejected: a radius or
decay that is zero, negative or NaN takes the default, a decay above 1 is 1, and
a window whose edges meet or cross becomes a hard cut at the lower edge. A
default render is byte-identical to the build in which these were constants.

The defaults themselves, and the ablations they were chosen on, are beside the
constants in `renderer/commands.go`.

### Cost

One fullscreen pass of 48 texture taps per pixel, inside the water render pass.
Measured on a Radeon RX 7900 XTX at 1280x720, MSAA 4x, as `PassShafts` over five
interleaved 200-frame runs:

| Sun | Cost |
|---|---|
| Middle of the frame | 0.163 ms (0.144–0.169) |
| At the frame edge | 0.095 ms (0.089–0.097) |
| Off screen | 0.000 ms — the pass does not run |

A scene with **no water** pays more than that, because the shafts are what make
the frame enter the water pass at all: a full copy of the scene colour and a
second render pass to resolve. Measured with that pass forced on in scenes that
have no water, `PassWater` (the copy) is 0.019–0.022 ms and `PassWaterResolve`
0.020 ms in `07-terrain` and 0.041–0.177 ms in `08-grass`. So a sky scene with
no water and the sun in frame spends roughly 0.2 to 0.35 ms on shafts, against
frames of 1.4 and 4.8 ms. A scene that already has water pays only the 0.163 ms,
because the copy and the resolve were happening anyway.

Nothing is spent when the effect cannot contribute. The strength that reaches
the renderer already has the edge fade and the elevation fade in it, so a sun
below the horizon, behind the camera or past the frame edge takes it to zero,
and a zero keeps the frame out of the water pass entirely. `08-grass`,
`07-terrain` and `15-kitchen-sink` never point at the sun and report
`gpu_shafts 0.000` and `gpu_water 0.000` throughout.

`task shafts` is the gate: it renders that dusk scene with the shafts on and
off, requires the ground lit through a gap to gain more than the ground in the
pillar's streak, and requires the two captures to be byte identical when the sun
is off screen.

## Fog settles, if you ask it to

`Fog.Density` alone gives uniform fog: the only thing that thickens it is
distance, so a valley floor has exactly the haze of the ridge above it. Set
`Fog.Height` and density falls off exponentially with altitude instead, which
is what real fog does.

```go
env.Fog = &glyph.Fog{
    Density:    0.005,
    Height:     4,          // density falls to 1/e over 4 units
    BaseHeight: waterLevel, // where density equals Density
}
```

Mist then pools in the low ground and elevated terrain rises out of it. The
visible difference is mostly on the *distant* hills: uniform fog washes them
out along with everything else, while height fog leaves them clear because the
sightline to them spends most of its length above the haze.

The integral along the view ray has a closed form, so this costs two
exponentials rather than a raymarch. `Density` means the same thing in both
modes — the shader applies the same exp-squared curve either way and `Height`
only redistributes fog vertically. Getting that wrong is easy and was the first
version of this: a plain Beer term is linear in distance where the uniform mode
is quadratic, so turning `Height` on thickened every scene at the same density.

Sensible `Height` values are on the order of the terrain's vertical scale.

## Replacing it entirely

Implement `EnvironmentSource`:

```go
type EnvironmentSource interface {
    Advance(dt float32)          // on the fixed tick
    State() EnvironmentState     // once per frame, no mutation
}
```

`Advance` runs at the simulation rate, so anything driven from it is frame-rate
independent. `State` runs once per rendered frame; the engine resolves it a
single time and uses that for the whole frame, so a stateful implementation
cannot light half a frame one way and half another.

```go
type weather struct{ storm float32 }

func (w *weather) Advance(dt float32) { w.storm = ... }

func (w *weather) State() glyph.EnvironmentState {
    return glyph.EnvironmentState{
        SunDir:     [3]float32{0.3, 0.5, 0.2},
        SunColor:   [3]float32{0.35, 0.36, 0.40},
        Ambient:    [3]float32{0.10, 0.11, 0.13},
        FogDensity: 0.02 + w.storm*0.05,
        DrawSky:    true,
    }
}

scene.Env = &weather{}
```

**Values and pixels are separate concerns.** `EnvironmentSource` decides the
numbers. To change how the sky is *drawn*, replace `sky.frag` through
`glyphengine.WithShaders` — or `renderer.WithShaders` if you drive the renderer
directly. Neither forces the other. See
[`game-loop.md`](game-loop.md#replacing-an-engine-shader).

If what you want is a sky that is a different **colour**, do not replace the
shader — see below.

## A sky that is not Earth's

`Scene.SetSkyPalette` sets the six colours the atmosphere blends between:
zenith and horizon for day, for twilight and for night.

```go
e.Scene.SetSkyPalette(glyph.SkyPalette{
    ZenithDay:       mgl32.Vec3{0.30, 0.10, 0.62}, // violet overhead
    HorizonDay:      mgl32.Vec3{0.95, 0.55, 0.22}, // amber at the rim
    ZenithTwilight:  mgl32.Vec3{0.18, 0.04, 0.30},
    HorizonTwilight: mgl32.Vec3{0.95, 0.22, 0.30},
    ZenithNight:     mgl32.Vec3{0.0040, 0.0012, 0.0060},
    HorizonNight:    mgl32.Vec3{0.0110, 0.0035, 0.0055},
})
```

`glyph.DefaultSkyPalette()` is Earth's and is what a new `Scene` starts with,
so a game that says nothing is unaffected. `examples/09-water -alien` is that
palette exactly; run it at `-time 0.5`, `-time 0.77` and `-time 0.0` to see day,
dusk and night.

**It is the whole atmosphere's palette, not the dome's.** `applyFog` blends
distant geometry toward the same horizon colour and water reflects the dome, so
these three are one value on purpose: change it and the sky, the haze and the
lake move together. That is also why replacing `sky.frag` alone does *not* give
an alien sky — it gives a violet dome over a landscape still fading into
Earth-blue haze, with a lake reflecting the wrong one of the two.

They reach the shaders in the per-frame `ShadowData` uniform block, after the
cascade matrices and the night grade. `sky.frag` and `clouds.frag` read the
same buffer through binding 1 of the cloud descriptor set — the same buffer,
not a copy, because a second copy is a second thing to get wrong. `task
skypalette` is the gate on that, and it fails if the palette reaches the dome
but not the fog, the water or the clouds. The clouds have their own box because
`clouds.frag` is a separate caller in a separate pipeline: fed the old colours
on its own, it left the other three boxes reading exactly what a correct build
reads, over a frame with 23% of its pixels wrong.

The palette works with `Sky` nil as well, because fog does not need a dome to
fade into a horizon colour. That, and the upgrade argument under [convenience
methods](#convenience-methods), is why it is `Scene` state rather than a field
on `Sky`.

**What it does not cover.** Rayleigh-versus-Mie behaviour, a different
scattering model, a sky with two suns, and the cloud, star and sun-disc colours
are all still shader work, and `WithShaders` is the right escape hatch for
them. The sun's own glow keeps a fixed warm ember (`atmSunGlow` in
`shaders/atmosphere.inc`) after the directional light fades, which is a seventh
colour this does not reach. This is the case that is pure palette, which is
most of what "another planet" means in practice.

## SunDir is not the sun

`EnvironmentState` has both `SunDir` and `SunElevation`, and they are not the
same thing.

`SunDir` is whichever body is currently lighting the scene — at night that is
the moon. `SunElevation` is the real sun's height, and it is what the
atmosphere derives its palette from.

Feeding the sky `SunDir.y` paints a noon sky at midnight, because the moon
rides highest exactly when the sky should be darkest. A custom implementation
that only sets `SunDir` gets `SunElevation` of 0 — permanent sunset. Set both.

## Convenience methods

`Scene.SetTimeOfDay`, `SetDayCycleSpeed` and `Engine.SetFogDensity` reach
through to the built-in `Environment`. They are **no-ops** under a custom
`EnvironmentSource`, which owns its own state — `Scene.DayNight()` returns nil
there, and that is the signal to configure your own type directly.

`Scene.SetNightGrade` and `Scene.SetSkyPalette` are deliberately **not** ones of
those. Both are Scene state initialised by `NewScene`, so they work the same
under a custom source as under the built-in one. Both would read more naturally
as fields on `EnvironmentState` beside fog and ambient, and neither is one for
the same reason: a source written before the field existed returns it as the
zero value. For the night grade — see
[day-night](day-night.md#night-is-desaturated-not-merely-dim--except-under-a-lamp)
— zero strength means no night shift at all, and that game's nights change on a
dependency bump with nobody choosing it. For the palette the zero value is six
black colours, so the sky, the fog and the water reflections all go black.

A sentinel would be more defensible for the palette than it was for the grade:
all-zero is a palette nobody wants, so reading it as "engine default" costs
nothing expressible. It is still not what was done, because it only covers the
all-zero case — a source that sets `ZenithDay` and leaves the other five alone
gets five black endpoints and no warning, which is the same trap one step
along. A field `NewScene` owns cannot be zeroed by a source that has never
heard of it.

Vary either per frame from `Update` if it should move with the moon phase or
the weather.

## Failure modes

- **A sky appears in an interior scene.** Something is still using
  `DefaultEnvironment()`. Set `Sky: nil`.
- **Everything is black.** `Env` is nil, or has no `Cycle`, `Sun` or `Ambient`.
  That is the documented meaning of an empty environment, not a bug.
- **`SetTimeOfDay` does nothing.** The scene has a custom `EnvironmentSource`
  or no `Cycle`.
- **A custom source gives a permanent sunset sky.** `SunElevation` was left at
  zero. See above.
- **Lighting flickers between frames.** A `State()` implementation is mutating.
  Move the change into `Advance`.
- **A custom source's nights went flat after an upgrade.** Not this field —
  `NightGrade` is on `Scene` precisely so that cannot happen. Look for a new
  `EnvironmentState` field the source is returning as its zero value.
- **A custom sky shader gives a violet dome over Earth-blue haze.** The palette
  is shared with `applyFog` and the water's reflection, which the replaced
  shader does not touch. Use `SetSkyPalette` instead of replacing `sky.frag`.
- **The sky changed colour but the distant hills, or the clouds, did not.**
  Something is handing `atmSkyPalette` its own six colours rather than
  `shadow.skyPalette`. `task skypalette` is the check for exactly that.
