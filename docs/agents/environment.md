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
  - glyphengine.DayCycleSource
  - glyphengine.StaticSource
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
  - glyphengine.Engine.SetShadowCoverage
  - renderer.ShadowCoverage
  - renderer.ShadowCascadeCoverage
  - renderer.DefaultShadowCoverage
  - renderer.ComputeCascadeVPsWithCoverage
requires: []
assets: none
example: examples/09-water
run: go run ./09-water -alien
verified: 2026-10-02 # the seam carved: DayCycleSource and StaticSource are the two built-in implementations, MoonDiscColor, SkyPalette and NightGrade are on EnvironmentState, and env= in the state trace hashes all of it (#161 step 3)
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

## The seam: one source writes the frame's environment

`EnvironmentSource` is the producer and `EnvironmentState` is what it produces.
The engine asks for the state **once per frame**, before any renderer work, and
every system that lights or scatters reads that copy. Nothing in the engine
reaches past it into a day cycle, a palette curve or a keyframe table.

```go
type EnvironmentSource interface {
    Advance(dt float32)      // on the fixed tick; this is where change belongs
    State() EnvironmentState // once per rendered frame, no mutation
}
```

That is the whole contract, and the rule that makes it worth having is:
**a source writes every field, or leaves it at the zero value, and nothing else
writes any of them.** Two fields have a documented exception, and it is a
fallback rather than a second writer — see
[below](#the-palette-and-the-grade-have-two-homes-on-purpose).

### Every field, and who reads it

`Scene.Environment()` returns the resolved state. These are its fields and the
single place each one is consumed.

| Field | What it is | Who reads it |
|---|---|---|
| `SunDir` | Direction toward whichever body lights the scene — the moon at night | `SceneLighting.SunDir`, and `ComputeCascadeVPsWithCoverage` for the cascades |
| `SunColor` | That body's light colour, intensity already multiplied in. Black means no directional light | `SceneLighting.SunColor` |
| `RealSunDir` | Direction toward the *real* sun | `SceneLighting.RealSunDir` |
| `SunElevation` | `RealSunDir.y`. What the atmosphere derives its palette from | `SceneLighting.SunElevation` |
| `Ambient` | Uniform fill light | `SceneLighting.Ambient` |
| `FogDensity`, `FogHeight`, `FogBaseHeight` | The fog, and therefore also the medium a lamp's beam is made of | `SceneLighting.Fog*` |
| `ClearColor` | What the frame clears to when `DrawSky` is false | `SceneLighting.SkyColor` |
| `StarFade` | How far night has come, 0 to 1 | `SceneLighting.NightFactor` |
| `MilkyWay`, `StarDensity` | The galactic band's strength and the star count scale | `SceneLighting.MilkyWay`, `.StarDensity` |
| `DrawSky`, `DrawStars` | Whether the dome and the star field are drawn | `SceneLighting.DrawSky`, `.DrawStars` |
| `DrawSun`, `DrawMoon` | Whether the billboards exist at all | `renderFrame`, which builds each celestial or does not |
| `SunDiscDir`, `SunDiscColor` | Where the sun billboard goes and what colour it is | `buildSunObject`; the direction also anchors the light-shaft projection |
| `MoonDiscDir`, `MoonDiscColor` | The same for the moon | `buildMoonObject` |
| `CloudSteps`, `Cirrus` | The cumulus sample budget and the high layer's strength | `SceneLighting.CloudSteps`, `.Cirrus` |
| `LightShafts` | Shaft strength, elevation fade already applied | `SceneLighting.LightShafts`, after `renderFrame`'s screen-edge fade |
| `LightShaftShape` | Reach, decay and the source window | `SceneLighting.ShaftShape` |
| `CastShadows` | Whether the directional shadow pass runs | `SceneLighting.ShadowEnabled`, if the cascade matrices could be built |
| `SkyPalette` | The six colours the dome, the fog and the water's reflection blend between | `SceneLighting.SkyPalette` |
| `NightGrade` | The scotopic grade lit surfaces take on as daylight goes | `SceneLighting.NightGrade` |

Both disc colours arrive **finished** — horizon fade and brightness boost
included — because how bright a body is at a given elevation is a look, and a
source that places a moon has to be able to tint it.

Three values the state deliberately does **not** carry, each because it needs
something the environment does not have:

- **The screen-space shaft strength and the sun's screen position.** They need
  the camera; `LightShafts` is what the source asks for and the pass draws with
  what is left after the edge fade.
- **`Scene.Volumetrics`** — the scattering medium's anisotropy and march step
  count. The *density* is the fog's and is on the state; these two are a graphics
  setting and a phase function, and neither is the sky's to decide. See
  `Scene.SetVolumetrics`.
- **A moon phase.** There is none. `MoonDiscColor` is where one would land.

`applyEnvironment` in `app.go` is the one function that copies the state into the
lighting pack, and `TestEveryEnvironmentStateFieldHasAReader` fails if a field is
added to the state without being routed anywhere. `renderer/commands_test.go` and
`renderer/litubo_test.go` carry the pack the rest of the way into the push block
and the uniform buffer.

### Two built-in sources, and a composite over them

| Source | What it is |
|---|---|
| `DayCycleSource` | The built-in day cycle: a clock places the sun and the moon, and the keyframe curves derive the light, the ambient, the disc colours and the star fade from where they are |
| `StaticSource` | Fixed light and air: one direction, one colour, no clock. A `Sky` here is a sky frozen at `Sky.FixedSunElevation` |
| `Environment` | The composite, and what `DefaultEnvironment()` returns. It delegates to the first when `Cycle` is set and to the second otherwise |

`Environment` is why **a cycle overrides `Sun` and `Ambient`**: a cycle already
knows where the sun is, so a fixed light beside it would be a second answer to
the same question, and the composite picks one branch rather than mixing them.

`Scene.DayNight()` reaches the clock in either an `Environment` or a
`DayCycleSource`, so `SetTimeOfDay` and `SetDayCycleSpeed` work on both and are
no-ops on a `StaticSource` or a custom source. `Engine.SetFogDensity` reaches the
`Fog` of all three.

### Proving a replacement produces the same values

The state trace writes an `env=` field per frame, hashing **every** field of the
state plus the volumetrics the fog doubles as. Two runs of one build must agree
on it — `task determinism` checks that, with a control that must disagree — and
it is also how a replacement source is checked against the built-in one: run the
same scene both ways under `GLYPHENGINE_FIXED_FRAME_TIME` and diff the field.
See [state-trace](state-trace.md).

It replaced a `sky=` that hashed nine of the state's twenty-seven fields and the
built-in cycle's `TimeOfDay`. A source that moved the cirrus, the Milky Way, a
disc colour or the palette changed the frame and left that field agreeing, which
is the one thing a trace field must not do.

### What moves out next

[ADR 0012](../adr/0012-an-x-module-for-opinionated-systems.md) step 4 moves
`DayCycleSource`, the `Sky` dome and discs, the cloud layers and the palette
curves to `x/sky`, gated on every committed capture staying byte-identical with
`x/sky` plugged in where the built-in was. What stays in the engine is this page:
`EnvironmentSource`, `EnvironmentState`, `StaticSource`, and the readers above.
That is the whole reason the carve came first.

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

## Directional shadow coverage

`CastShadows` enables the directional maps. Their default half-extents remain
15 and 90 world units, at 2048 squared texels each. For larger scenes, call this
on the frame thread (`Init` or `Update`):

```go
coverage := renderer.DefaultShadowCoverage()
coverage.Cascades[0].TowardLight = 1500
coverage.Cascades[1] = renderer.ShadowCascadeCoverage{
    Radius: 2000, TowardLight: 3000, AwayFromLight: 3000,
}
if err := e.SetShadowCoverage(coverage); err != nil { return err }
```

The volumes are centred on the camera's **target**, as before. `Radius` is the
light-space XY half-extent; `TowardLight` places the light eye ahead of the
centre, with a 0.1-unit near plane, and `AwayFromLight` reaches behind it.
Increasing caster depth alone preserves XY texel density. Increasing radius
trades detail for coverage: radius 15 is 0.01465 units/texel, radius 90 is
0.08789, and radius 2000 is 1.953125. Two maps cannot guarantee detailed shadows
over arbitrary distances; measure your geometry and camera positions.

All fields must be finite and positive, and `TowardLight` must exceed 0.1.
An entirely zero `ShadowCoverage` restores defaults; partially specified
volumes are rejected without changing the active setting. This allocates no
new GPU resources and survives resize and scene replacement. Default shader
bindings, map count, resolution and sampling remain unchanged.

Casters outside the camera are kept if either cascade sees them, including
non-nested volumes. Renderer-only consumers can call
`ComputeCascadeVPsWithCoverage` and pass the returned matrices through
`SceneLighting.CascadeVPs`; they must likewise cull against both volumes.
`ComputeCascadeVPs` retains its original defaults. For camera-relative worlds,
use the same rebased coordinates for the centre, camera and all geometry.
Texel snapping operates in that coordinate system; changing the world origin
can change its snapping phase, so this is not a promise of phase continuity
across arbitrary rebases.

`examples/23-shadow-coverage` places a caster one kilometre toward the sun.
`-coverage=false` restores the old volumes; `-caster=false` removes the distant
caster. `task shadowcoverage` checks the receiver and a nearby prop under a
fixed clock. No planetary atmosphere or terrain policy is supplied.

Measured on RX 7900 XTX, 800x600: the central receiver drops from 181.67 to
119.33/255 (62.33, 1.52x) when extended coverage includes the caster; removing
the caster restores 181.67. The nearby prop's shadow stays at 119.33. Depth bias
is rescaled from the matrix's depth row to retain its historical world-space
size: without that adjustment the nearby shadow disappears (181.67). The check
fails with that adjustment removed, and the culling test fails when only the
last cascade is considered. A fixed-clock `02-cube -frames 60` capture remained
byte-identical to the preceding build with default coverage.

For this deliberately small scene, three 200-frame GPU means after 30 warmup
frames (validation off) measured default shadow-pass time at 0.0393–0.0407 ms
and extended coverage at roughly 0.039 ms. That establishes no meaningful
speed difference; a terrain scene can submit many more casters when enlarged.
The map allocation remains two 2048-square layers per frame in flight.
Custom shadow shaders must likewise scale normalized depth bias with their
matrix depth range; the stock shader derives it from the Z row, without changing
any descriptor layout.

## Clouds are a graphics setting

`Sky.CloudSteps` controls volumetric cumulus. It sets a coarse sample budget:
`CloudsOff` is 0, `CloudsLow` is 16, and `CloudsHigh` is 32. Occupied intervals
use quarter-sized steps. Lower counts can change cloud shape because they also
change which noise octaves resolve.

`Sky.Cirrus` independently adds a high, thin layer (0 disables, 1 is full
strength; default 0). For clear sky, set both to zero. Both settings can change
at runtime without rebuilding GPU resources.

The clouds render into a half-resolution target before the scene. The later
sky composite is depth-tested against terrain; the cloud march itself is not.
Measure the actual workload with `task bench` rather than assuming clouds or
grass dominate. See [clouds.md](clouds.md) for layering, sunset lighting,
current per-pass measurements and limits, and [profiling](profiling.md) for
measurement tools.

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

`task shafts` is the gate, on the default shape. It renders that dusk scene with
the shafts on and off and requires three things of the light added: the ground
lit through a gap gains at least twice what the ground in the pillar's streak
does; the foreground hillside, metres from the eye, gains almost nothing (+0.2
shipped, ceiling 4); and with the sun off screen the two captures are byte
identical. The middle one is what catches the effect turning into a dirty lens:
with the lobe removed the first check still passes at a ratio of 4.0, and the
foreground reads +6.7.

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

The fog is also the medium a lamp's beam is made of. A light with
`SpotLight.Volumetric` or `PointLight.Volumetric` above zero scatters off
*this* fog, at *this* density, through *this* height profile — the same
`Density * exp(-(y - BaseHeight) / Height)` the integral above closes over,
sampled a point at a time instead. So a scene with no `Fog`, or with
`Density` 0, shows no beams however high a light's `Volumetric` goes, and a
fog that rolls in thickens every beam in the scene with it. See
[lights.md](lights.md#a-beam-is-the-air-being-lit). The anisotropy and the
sample count are the one part that is not fog — they are
`Scene.SetVolumetrics`, because the fog has nothing to say about them.

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

Every field this leaves out is the zero value, and the zero value is the honest
answer for all of them but two: `SkyPalette` and `NightGrade` read all-zero as
"the scene's", so a source that says nothing about the colour of the air gets
Earth's rather than black. That is the only sentinel in the state; everything
else means what it says, and `SunElevation` left at 0 really is a permanent
sunset. See [the field table](#every-field-and-who-reads-it) and
[SunDir is not the sun](#sundir-is-not-the-sun).

A replacement that wants to start from the built-in cycle's numbers rather than
from nothing can embed a `DayCycleSource`, call its `State`, and change what it
cares about. That is also the shape the `x/sky` migration takes.

**Values and pixels are separate concerns.** `EnvironmentSource` decides the
numbers. To change how the sky is *drawn*, replace `sky.frag` through
`glyphengine.WithShaders` — or `renderer.WithShaders` if you drive the renderer
directly. Neither forces the other. See
[`game-loop.md`](game-loop.md#replacing-an-engine-shader).

Custom sky shaders can read the directional shadow map at set 1 binding 1;
see the [binding contract](game-loop.md#shadow-resources-in-custom-sky-shaders).
For application-owned atmosphere or other parameters, use
`Renderer.SetShaderParameters` ([uniform contract](game-loop.md#application-data-for-custom-shaders))
instead of placing non-colour data in the palette.

The shared shadow/light set has `ShadowData` at binding 0, directional and
point shadow samplers at 1 and 2, light/cluster storage buffers at 3-5, the
4096-byte application uniform block at 6, and four application image samplers
at 7-10. `SetShaderTexture` and `SetShaderTarget` populate those samplers for
vertex and fragment shaders; nil binds the white fallback. This is set 1 for
sky, static lit, terrain, water and application passes, and set 2 for skinned
lit. See [render targets](render-targets.md) for the fixed layouts, scheduling,
resolved scene depth and history semantics.


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
fade into a horizon colour. That is why it is not a field on `Sky`; where it
*does* live, and why it has two homes, is under [the palette and the
grade](#the-palette-and-the-grade-have-two-homes-on-purpose). A custom source can
return its own on `EnvironmentState` instead of calling this.

**What it does not cover.** Rayleigh-versus-Mie behaviour, a different
scattering model, a sky with two suns, and the cloud, star and sun-disc colours
are all still shader work, and `WithShaders` is the right escape hatch for
them. The sun's own glow keeps a fixed warm ember (`atmSunGlow` in
`shaders/include/atmosphere.inc`) after the directional light fades, which is a seventh
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

`Scene.SetTimeOfDay` and `SetDayCycleSpeed` reach through to the clock of an
`Environment` or a `DayCycleSource`; `Engine.SetFogDensity` reaches the `Fog` of
either, or of a `StaticSource`, creating one if there is none. They are
**no-ops** under a custom `EnvironmentSource`, and the clock ones are no-ops on a
`StaticSource` too — `Scene.DayNight()` returns nil in both cases, and that is the
signal to configure your own type directly.

### The palette and the grade have two homes on purpose

`Scene.SetNightGrade` and `Scene.SetSkyPalette` are **not** no-ops under a custom
source, and they are not overrides either. Both values are on
`EnvironmentState`, where a source that owns the look returns its own; both are
also on `Scene`, where `NewScene` initialises them to the engine's defaults; and
`Scene.Environment` resolves between them in exactly one place:

> An all-zero `SkyPalette` or `NightGrade` on the state means **the scene's**.

Both halves earn their place. The state needs the fields because the colour of
the air is part of what a replacement sky owns — without them an alien dome still
hazes into Earth-blue, which is the bug `task skypalette` exists for. The scene
needs to keep the values because `EnvironmentState` is produced wholesale, so a
game that replaced the environment model returns a struct written before the
fields existed: six black colours and `Strength: 0`. Without the sentinel that
game's sky, haze and water reflections go black and its nights go flat on a
dependency bump, with nobody choosing it.

What the sentinel costs is two values that cannot be asked for on the state: a
palette of six exact blacks, and a grade with `Strength: 0` *and* a zero `Tint`.
For a night with no scotopic shift, set `Strength: 0` and any tint — the tint is
unread at zero strength. For a black world, set `Sky: nil` and a black
`ClearColor`, which is what an empty environment already means.

What it does **not** cover is a half-filled palette. A source that sets
`ZenithDay` and leaves the other five alone gets five black endpoints and no
warning. That trap is real and it is why the scene keeps the values at all: a
source either owns the palette or says nothing about it, and saying nothing is
the zero value, not one field of six.

`Scene.SkyPalette()` and `Scene.NightGrade()` return the **scene's**, which is not
necessarily the frame's. Read `Scene.Environment().SkyPalette` for what was
actually used.

Either way, vary them per frame if they should move with the weather or the moon:
return them from `State`, or call the setters from `Update` where
`SetPointLights` is called from.

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
- **A custom source's nights went flat, or its sky went black, after an
  upgrade.** Not the grade or the palette — their zero value on the state means
  "the scene's" precisely so that cannot happen. Look for a different new
  `EnvironmentState` field the source is returning as its zero value, and check
  it against the field table above.
- **A custom source set one palette colour and got five black ones.** The
  sentinel is all-or-nothing by design; see
  [above](#the-palette-and-the-grade-have-two-homes-on-purpose). Start from
  `DefaultSkyPalette()` and change what you want.
- **A custom source places a moon and it is black.** `MoonDiscColor` is a field
  now, and it carries the horizon fade and the brightness boost. `DrawMoon` with
  a zero colour draws an invisible billboard.
- **A field on `EnvironmentState` reaches nothing.** It is not routed in
  `applyEnvironment`; `TestEveryEnvironmentStateFieldHasAReader` is the check,
  and it would have failed.
- **A custom sky shader gives a violet dome over Earth-blue haze.** The palette
  is shared with `applyFog` and the water's reflection, which the replaced
  shader does not touch. Use `SetSkyPalette` instead of replacing `sky.frag`.
- **The sky changed colour but the distant hills, or the clouds, did not.**
  Something is handing `atmSkyPalette` its own six colours rather than
  `shadow.skyPalette`. `task skypalette` is the check for exactly that.
