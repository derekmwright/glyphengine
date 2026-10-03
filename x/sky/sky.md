---
id: x-sky
title: The Earth sky — day cycle, dome, discs, stars and clouds
summary: >
  Plug in the sky the engine used to have: a day/night cycle with palette
  curves, the procedural dome, the sun and moon discs, the star field and the
  Milky Way, and the volumetric cloud layers.
capability: environment
status: stable
since: v0.1.0
api:
  - sky.Shaders
  - sky.Fill
  - sky.Environment
  - sky.DefaultEnvironment
  - sky.DayCycleSource
  - sky.Sky
  - sky.DefaultSky
  - sky.CloudsOff
  - sky.CloudsLow
  - sky.CloudsHigh
  - sky.DayNight
  - sky.DayNight.Advance
  - sky.DayNight.SetTimeOfDay
  - sky.DayNight.SunDir
  - sky.DayNight.SunAboveHorizon
  - sky.DayNight.SunIntensity
  - sky.DayNight.SunColor
  - sky.DayNight.SunDiscColor
  - sky.DayNight.MoonDir
  - sky.DayNight.MoonVisible
  - sky.DayNight.MoonIntensity
  - sky.DayNight.MoonColor
  - sky.DayNight.MoonDiscColor
  - sky.DayNight.PrimaryLight
  - sky.DayNight.AmbientColor
  - sky.DayNight.SkyColor
  - sky.DayNight.StarVisibility
  - sky.DayNight.Daylight
  - sky.DayNight.Twilight
example: examples/09-water
run: task example:09-water
requires:
  - cgo
  - vulkan-runtime
assets: procedural
verified: 2026-10-02 # moved out of the engine; the carve's pinned tables moved with it, the three .spv are byte-identical to the engine's, and task skymigration holds the pixels
---

# x/sky

The sky the engine shipped until it was an opinion with nowhere to go: a
day/night cycle, the keyframe curves it drives, the procedural dome, the sun and
moon discs, the star field with its galactic band, and the volumetric cloud
layers.

It fails all three parts of the
[rule-14 test](../README.md#the-test-for-whether-something-belongs-here) at once
— the keyframe tables are a specific look, the disc boosts and the twilight
width are tuned constants, and a game underground or on another planet wants a
different one. [ADR 0012](../../docs/adr/0012-an-x-module-for-opinionated-systems.md)
step 4 is the move; this page is how to use it.

## Two halves, and you need both

```go
e, err := glyph.New(&game{}, glyph.WithShaders(sky.Shaders()))
...
func (g *game) Init(e *glyph.Engine) error {
    env := sky.DefaultEnvironment()
    env.Cycle.SetTimeOfDay(0.35)
    env.Cycle.Speed = 1.0 / 120 // a two-minute day; 0 freezes it
    e.Scene.Env = env
    return nil
}
```

`sky.Shaders()` fills the engine's **sky slot** — the three fragment stages
`renderer.DefaultShaders()` leaves nil — and `sky.DefaultEnvironment()` is the
`EnvironmentSource` that asks for them. Neither is enough on its own:

- **Source without shaders**: the state says `DrawSky`, the renderer has no dome
  pipeline, and the frame is the clear colour with the right light on it. Nothing
  errors.
- **Shaders without a source**: three pipelines are built and never used.

A game that is already overriding other stages uses `sky.Fill` instead:

```go
sh := renderer.DefaultShaders()
sh.LitFrag = myLitFragSpv
e, err := glyph.New(&game{}, glyph.WithShaders(sky.Fill(sh)))
```

`Fill` leaves a stage that is already set, so a dome from here and clouds from
somewhere else is a supported arrangement. `examples/24-custom-passes` is the
worked example of `Fill`.

## The mechanism/opinion split, and why it fell where it did

The engine keeps **the slot**; this package paints it. Nothing about *when* or
*where* the sky is drawn is here, because none of it is a look:

| Stays in the engine | Why |
|---|---|
| `shaders/sky.vert`, the fullscreen triangle at the reverse-Z far plane, and the `GreaterOrEqual` depth state | A layout. It is also the vertex stage of eight passes, including the tonemap and the bloom chain |
| The pass order — clouds, dome, in-scattering, stars, discs — and the alpha the dome writes for the layers after it to blend against | An ordering and a blend contract |
| The half-resolution cloud target, its two-buffer history, its barriers, and the descriptor set that carries both the layer and the per-frame palette block | A lifetime and a set of barriers |
| The push-constant and UBO packing every shader in the frame shares | A format |
| The celestial billboard, placed just inside the far plane | A depth-range decision with two hard bounds; see `celestialDistance` |
| `shaders/skyvolumetric.frag`, the in-scattering march | A game with no dome still has air |
| `shaders/godray.frag` and `glyphengine.LightShaftShape` | A screen-space filter, fully parameterised. This package only says how much of it to ask for |
| `shaders/include/atmosphere.inc` | The fog, the water and every lit surface call the same functions; one copy is the only agreement that cannot drift |
| `renderer.SkyPalette`, `renderer.NightGrade` and their defaults | A scene with no sky at all still fogs and still grades |
| `renderer.EquirectToSkyMap` and the panorama texture slot | An image transform and a texture binding |

| Moved here | Why |
|---|---|
| `DayNight` and its keyframe tables | Hand-placed colour keys for sky, sun and ambient |
| The sun/moon handover, the intensity ramps, the star curve's lag behind sunset | Tuned windows with a look behind each |
| The disc brightness boosts (5 for the sun, 1.5 for the moon) | Constants chosen against a bloom threshold |
| `sky.frag` | The dome gradient, the Rayleigh-ish falloff exponent, the ground colour below the horizon |
| `stars.frag` | The star field's density and thinning, and the Milky Way's four-pass construction |
| `clouds.frag` | The cloud shape, its lighting and its temporal blend weights |
| `Sky`, `DefaultSky`, `CloudsOff/Low/High` | What a full sky is, and what "low" and "high" mean |
| `Environment`, `DefaultEnvironment` | That a default world is Earth at sunrise with light haze |

### The smallest mechanism, and what the alternative would have cost

The other shape available was an **application pass set**: the package creates
the dome, star and cloud passes through `renderer.AppPass`, and the engine loses
the sky passes entirely. It was rejected, and the reason is worth keeping
because it is the kind of thing that looks cleaner from a distance.

The five draws are not five independent passes. The cloud march writes a
half-resolution target the dome samples through a barrier; the dome writes an
alpha the stars and both discs blend against; the in-scattering shares the dome's
exact depth state so that it covers exactly the dome's pixels; the discs are
depth-tested against the scene so terrain occludes them, and drawn *after* the
dome so a cloud can pass in front of the sun. Moving that out means moving the
ordering, the blend factors, the depth states and the barriers with it — all five
of which are mechanism by the ADR's own test — and then the engine could no
longer draw an in-scattered beam without a sky package.

So the slot is the smaller mechanism in the sense that matters: it moves the
*look* and nothing else, and it is what makes the migration provable. The three
`.spv` in this directory are byte-identical to the ones that were in `shaders/`
before the move (9568, 17760 and 35296 bytes), because the only thing that
changed about them is which `-I` resolved `atmosphere.inc`. A pass set would have
rebuilt the draws, and "every committed capture is unchanged" would have been a
hope rather than a consequence.

### The one piece of look the engine kept

`celestialScale` in the engine's `app.go` draws a body at the zenith 45% smaller
than the same body on the horizon, which is roughly what the atmosphere does to
the apparent size of one. That is a look by any reading of the test, and it is
still in the engine.

It is there because `EnvironmentState` is frozen by the migration's own control.
`env=` in the state trace hashes every field of the state, and `task
skymigration` compares that hash against traces taken before the move — so
adding a `DiscScale` field for this package to write would change the hash and
destroy the only measurement that says the move was faithful. The honest order is
this change first, the field afterwards, which is the same reason the carve came
before the move. Recorded here rather than left as an oversight.

## Configuring it

`Environment` is the composite and takes the same pieces it did in the engine:

```go
// A moving sun.
scene.Env = sky.DefaultEnvironment()

// A sky frozen at one hour, with fixed light under it.
scene.Env = &sky.Environment{
    Sun:     &glyph.DirectionalLight{Direction: [3]float32{0.6, 0.8, 0.3}, Color: [3]float32{1, 0.96, 0.9}},
    Ambient: &glyph.AmbientLight{Color: [3]float32{0.10, 0.12, 0.16}},
    Sky:     &sky.Sky{FixedSunElevation: 0.4},
}

// The cycle on its own, for a game that wants no composite over it.
scene.Env = &sky.DayCycleSource{
    Cycle: sky.DayNight{TimeOfDay: 0.25, Speed: 1.0 / 300},
    Sky:   sky.DefaultSky(),
    Fog:   &glyph.Fog{Density: glyph.DefaultFogDensity},
}
```

`Cycle` set means the clock supplies the light, the ambient and the bodies, and
it **overrides** `Sun` and `Ambient`: a cycle already knows where the sun is, so
a fixed light beside it would be a second answer to the same question.

With `Cycle` nil the light and the air come from `glyphengine.StaticSource` —
the engine's own resolution, called rather than repeated — and this package adds
only the dome. `Sky.FixedSunElevation` picks the hour, the star fade derives from
it so a sky frozen below the horizon gets a real night, and the discs are not
drawn because only a cycle places bodies.

Nil `Sky` means no dome, no discs and no stars, and the frame clears to
`ClearColor`. Nil `Fog` means no distance fog. For a scene that wants no sky at
all, `glyphengine.StaticSource` is the engine's own and this package is not
needed.

### The clock

There is no `Scene.SetTimeOfDay` any more. It reached past the seam into a
concrete source type, which is the coupling the environment carve removed, and
`env=` had already dropped `TimeOfDay` for the same reason. A game holds its
source and sets the clock on it:

```go
env.Cycle.TimeOfDay = 0.35          // already in [0,1)
env.Cycle.SetTimeOfDay(fromAFlag)   // wraps, the way Scene.SetTimeOfDay did
env.Cycle.Speed = 0                 // freeze
```

`Advance` is called by `Scene.Tick` on the fixed tick, so the clock runs in
simulation seconds and a paused scene has a stationary sun. `Scene.StarVisibility()`
still works: it reads the resolved state rather than reaching for a clock.

### The palette is not here

`Scene.SetSkyPalette` is the engine's and it keeps working against this sky
exactly as it did against the built-in one. This package returns the zero value
for `EnvironmentState.SkyPalette` and `.NightGrade`, which is the sentinel for
"the scene's" — so `examples/09-water -alien` is still six colours and no shader
work. The colours are the whole atmosphere's, not the dome's: the fog distant
geometry fades into and the water's reflection read the same buffer, which is why
replacing `sky.frag` alone gives a violet dome over Earth-blue haze. See
[environment.md](../../docs/agents/environment.md#a-sky-that-is-not-earths).

## Shaders

The three fragment stages are committed as SPIR-V and embedded, for the reason
[AGENTS.md rule 2](../../AGENTS.md#rules-that-matter) gives for the engine's own:
a game that `go get`s this package has the module, not a Vulkan SDK.

`sky.frag` and `clouds.frag` `#include "atmosphere.inc"` by bare name, resolved
by `glslc -I` against the set `shaders/include` materializes. That is the
documented build step in [x/README.md](../README.md#shaders-compiling-against-the-engines-include-set),
and it runs from a test:

```
task xsky:shaders     # regenerate the .spv (needs VULKAN_SDK)
go test ./sky/ -run TestCommittedSPIRVMatchesGLSL
```

`spirv_test.go` is both the generator and the check, because two copies of a
compile command are two things that can disagree about a flag. It is also the
compile gate for the include seam: `atmSkyPalette`, `atmSunGlow`, `atmDaylight`
and `atmTwilight` are as much an API as a Go function, and a changed signature
fails here at build rather than at draw.

Two conventions these shaders share with the engine and cannot discover on their
own:

- **The fixed layouts** in [render-targets.md](../../docs/agents/render-targets.md):
  set 0 binding 0 is the cloud layer, set 0 binding 1 is the per-frame block the
  palette rides in, set 1 is the shadow/light set, and the push block's member
  order is what lands `fog.zw` where every other shader reads it from.
- **The octahedral fold** `stars.frag` projects the Milky Way panorama with.
  `renderer.EquirectToSkyMap` is its inverse, across a module boundary now, and
  `renderer/skymap_test.go` transcribes the shader expression to hold the two
  together. Getting it transposed renders a perfect, mirrored sky.

`DayNight.Twilight` in Go and `atmTwilight` in the engine's include set are the
same curve computed twice — the shader for the dome, the fog and the water, the
Go for game code — and they have already drifted once, when only the shader was
changed. Change both together.

## Gates

| What | Where |
|---|---|
| The day cycle resolves field for field to what it resolved to before the carve and before this move: 25 points round the clock, plus 9 at the curve edges a regular sweep steps over | `environment_test.go`, `dayCyclePins` / `dayCycleEdgePins` |
| A dome at a fixed hour, over the engine's `StaticSource`, resolves to its pre-move pins | `environment_test.go`, `fixedSkyCases` / `fixedSkyPins` |
| The moon disc's colour at 25 hours, to the bit | `environment_test.go`, `moonDiscPins` |
| The curves themselves — continuity across midnight, the sun lighting past the horizon, twilight peaking on it, stars lagging sunset, a quiet handover, a dark night, an HDR disc | `daynight_test.go` |
| Zero allocations per frame resolving, and per tick advancing, on all four shapes | `environment_test.go`, `TestEnvironmentResolvesWithoutAllocating` |
| `Shaders()` fills the slot and nothing else, and the engine's defaults leave it empty | `environment_test.go`, `TestShadersFillTheSkySlotAndNothingElse` |
| The committed `.spv` are what `glslc` produces through the exported include set | `spirv_test.go` |
| The pixels: three scenes against captures taken from the built-in path before the move, plus `env=` per frame | `task skymigration` |
| Every committed documentation image unchanged | `task screenshots` |
| The look, scene by scene | `task sky`, `skypalette`, `clouds`, `shafts`, `nightlight`, `waterlight`, `volumetric` |

The unit pins and the capture gate do not substitute for each other. The pins
compare every field of every resolved state exactly and need no GPU, but they
cannot see a shader; `task skymigration` compares pixels and cannot tell a
palette that shifted in the last bit from a frame that dithered.

## Failure modes

- **No sky at all, and the light is right.** `glyph.WithShaders(sky.Shaders())`
  is missing. The source asked for a dome and the renderer has no pipeline to
  draw one, which is not an error — see the two halves above.
- **A dome but no clouds, or no stars.** One stage of the slot is set and another
  is not. `Shaders()` sets all three; `Fill` leaves a stage that is already set,
  which is deliberate but easy to do by accident.
- **Everything is black.** `Scene.Env` is nil. `NewScene` leaves it nil now; the
  engine has no environment of its own beyond `StaticSource`.
- **`SetTimeOfDay` does not compile.** It is `env.Cycle.SetTimeOfDay` now. See
  [the clock](#the-clock).
- **`SetFogDensity` does not compile.** It is gone; set `env.Fog.Density`. It
  worked by type-switching on the built-in sources, and two of the three left.
- **An alien dome over Earth-blue haze.** The palette is the scene's and shared
  with the fog and the water. Use `Scene.SetSkyPalette`, not a replaced
  `sky.frag`.
- **A capture moved and `task ci` is green.** Run `task xsky:shaders` and see
  whether the `.spv` were stale; `spirv_test.go` reports it by size, and two
  shaders of equal size can still differ.
- **The sun is a dull orange ball at sunset.** The disc's boost is applied in
  `DayNight.SunDiscColor` and fades below the horizon on purpose; the window is
  recorded there with the measurement that placed it.
