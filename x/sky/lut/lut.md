---
id: x-sky-lut
title: A cheap sky from a lookup table
summary: >
  Fill the engine's sky slot with a dome whose whole per-pixel cost is a texture
  fetch: a table baked on the CPU at startup, indexed by view elevation, sun
  elevation and proximity to the sun, with a four-key day driving it.
capability: environment
status: experimental
since: v0.1.0
api:
  - lut.Shaders
  - lut.Fill
  - lut.ShaderTextureSlot
  - lut.Options
  - lut.DefaultOptions
  - lut.Key
  - lut.DefaultKeys
  - lut.Bake
  - lut.Sky
  - lut.New
  - lut.Sky.Destroy
  - lut.Sky.Options
  - lut.Sky.TimeOfDay
  - lut.Sky.SetTimeOfDay
  - lut.Sky.Speed
  - lut.Sky.SetSpeed
  - lut.Sky.Advance
  - lut.Sky.State
example: examples/07-terrain
run: task example:07-terrain -- -sky lut
requires:
  - cgo
  - vulkan-runtime
assets: procedural
verified: 2026-10-03 # new package, ADR 0012 step 5; the second filling of the sky slot, with its own gate (task xskylut) and 18 recorded unit breaks
---

# x/sky/lut

A second sky, on the same seam as [`x/sky`](../sky.md), for a target that cannot
afford the first one. The dome is a texture lookup and there is nothing else in
it: no cloud march, no star field, no sun or moon billboard, no light shafts.

It exists for two reasons, and the first one is not the cheapness. ADR 0012's
claim is that the look left the engine and the engine kept a *slot*; a slot with
one filling is a slot by assertion. This is the second filling, and it is what
makes the claim checkable: `renderer.ShaderSet.SkyFrag` takes a dome that shares
no Go identifier, no shader and no table with `x/sky`, and the engine's pass
order, depth state, push-constant packing and alpha contract carry it unchanged.

```go
import (
	glyph "github.com/derekmwright/glyphengine"
	skylut "github.com/derekmwright/glyphengine/x/sky/lut"
)

func main() {
	e, err := glyph.New(&game{}, glyph.WithShaders(skylut.Shaders()))
	...
}

func (g *game) Init(e *glyph.Engine) error {
	opts := skylut.DefaultOptions()
	opts.Speed = 1.0 / 120 // a two-minute day; 0 freezes it
	sky, err := skylut.New(e.Renderer(), opts)
	if err != nil {
		return err
	}
	e.Scene.Env = sky
	return nil
}
```

## Two halves and a table

`x/sky`'s page records that a package filling the sky slot ships **two halves
that have to arrive together**, the Go seam and the SPIR-V, and that neither
errors without the other. This has a third: the table.

| Missing | What you see |
|---|---|
| `glyph.WithShaders(skylut.Shaders())` | No sky. The source asks for a dome, the renderer has no pipeline, the frame is the clear colour with the right light on it. Nothing errors. |
| `e.Scene.Env = sky` | A dome pipeline built and never used. |
| The table | Not possible: `New` takes the renderer and binds it, so there is no way to hold the source without it. An unbound slot samples the renderer's white fallback, which would be a white sky. |

`New` is where the table is baked, uploaded and bound, which is why it needs the
renderer and why it is called from `Init` rather than from `main`. `Destroy`
releases the slot and then the texture.

## The table

Three axes, 64 by 32 by 32 texels, 256 KB as RGBA8, laid out 1024 by 64 with
proximity across inside a sun-elevation slice, the slices across after it, and
view elevation down.

| Axis | Texels | Spacing |
|---|---|---|
| View elevation, -1 to 1 | 64 | Signed square: 0.001 of elevation at the horizon, 0.06 near the zenith |
| Sun elevation, -1 to 1 | 32 | Signed square: 0.004 at the horizon, 0.06 at the poles |
| `dot(dir, sunDir)`, -1 to 1 | 32 | Chord: 3.7 degrees per texel near the sun |

**The third axis is why the table is the model rather than an approximation of
it.** The dome reads the view direction only through its `y` and through
`dot(dir, sunDir)`, and the sun only through its `y` — the gradient's
`pow(smoothstep(-0.08, 0.75, elevation), 0.65)`, the palette's two blends, the
halo's `pow(prox, 8)`, the wash's `pow(prox, 1.6) * exp(-3.5*abs(dir.y))` and the
below-horizon ground fade are all functions of those three scalars and nothing
else. Tabulating them tabulates the function. An azimuth-difference axis would
have held the same information in a worse place: equal steps in azimuth are not
equal angular steps away from the sun, which is what a `pow(prox, 8)` halo cares
about.

The spacings are not uniform because the sky is not. Most of the gradient's
movement is in the first tenth of the climb from the horizon, and the twilight
lobe is a Gaussian 0.115 wide centred on the horizon — on a linear sun axis it
would get two slices and dusk would band visibly as the clock moved.

The shader filters the first two axes with the sampler and the third itself: two
`textureLod` fetches of adjacent sun slices and a `mix`. It has to do that one
itself, because `u` already carries proximity and letting the sampler interpolate
along `u` between slices would blend the far side of one sky with the near side of
the next. Every axis is inset half a texel so the filter never crosses a slice
boundary, which is the whole of what prevents a wrong-coloured ring around the sun
at some hours and not others.

### The fetch is `textureLod`, and the reason is not the one you would guess

Level 0 is the only level a lookup has any use for, and `textureLod` says so
without depending on a derivative or on a mip chain. That is the whole of the
justification, and it is smaller than the one this page used to give.

The claim that was here — that the square-root view axis makes `dv/dy` unbounded
at the horizon, so an implicit-derivative `texture()` would pick a high mip along
the horizon line — is **false, measured**. With `texture()` in place of both
fetches the frame is pixel-identical, both with the horizon out of shot and with it
across the middle of a 640x480 frame (`lutskycheck -pitch 0`, compared with
`cmd/pngsame`). The axis is scaled so that near the horizon one screen pixel is
about one texel: at 480 pixels over a 60-degree vertical field of view the first
pixel above the horizon moves `v` by 1.05 texels, an LOD of 0.07. The chain was
reachable — `CreateDataTexture` builds 11 levels for this 1024x64 image and sets
`MaxLod` to the count — it is simply never reached, and those 11 levels are about
85 KB of waste on top of the table's 256.

Kept anyway, because it is free and unconditional. A coarser axis or a smaller
table would be relying on that measurement rather than on the call, which is the
kind of thing [AGENTS.md rule 12](../../../AGENTS.md#rules-that-matter) is about:
the number is recorded next to the line so the next person can re-run it.

### Eight bits, and the engine gap behind them

The table is RGBA8, encoded as `sqrt(v/3)` and squared back in the shader. 3.0 is
the ceiling because the default palette's brightest texel is **2.505** — in red,
looking straight at a sun 1.5 degrees up, where the tight halo, the broad wash and
the twilight horizon colour all peak together — so 84 percent of the range is
used. `Bake` refuses a palette that would exceed it rather than clipping, because
a clipped table is a flat white patch where the sun is and that reads as a shader
bug rather than as an encoding limit.

What eight bits cost, measured over the whole default table against the model
evaluated at each texel's own coordinates:

| | Worst error |
|---|---|
| Absolute, anywhere | 0.0105 (half an encoded step at the top of the range is 0.0118) |
| Relative, above 0.1 | 2.1 percent |
| Relative, above 0.01 | 6.5 percent |
| Relative, above 0.002 | 13.4 percent |

The relative figure is `2*(0.5/255)/sqrt(v/3)`: it is the square transfer and
nothing else, and it cannot be improved by choosing a different curve — any
monotone transfer spending 255 levels over a 0-to-3 range lands within a few
percent of this. **It can only be improved by a wider format, and there is no
public way to upload one.** `CreateTexture`, `CreateDataTexture`,
`CreateTextureLinear` and `CreateTextureNearest` are all RGBA8, and
`CreateRenderTarget` takes no CPU pixels. That is a rule-14 gap and it is filed
as one rather than patched here; see [below](#the-engine-gap).

## The day: four keys

The clock is a `TimeOfDay` in `[0,1)` and a `Speed` in cycles per simulation
second, the same shape `x/sky`'s `DayNight` has, advanced by `Scene.Tick` on the
fixed tick so a paused scene has a stationary sun. What it drives is a list of
`Key`s, interpolated and wrapped through midnight.

`DefaultKeys` is **`x/sky`'s cycle sampled at four hours**, so the two skies are
two skies of the same world:

| Time | Sun | `SunColor` at full strength | `Ambient` |
|---|---|---|---|
| 0.00 midnight | straight down | 1.0, 0.6, 0.3 (unread; the fade is zero) | 0.010, 0.013, 0.030 |
| 0.25 sunrise | on the horizon | 1.0, 0.6, 0.3 | 0.140, 0.115, 0.105 |
| 0.50 noon | overhead | 1.0, 1.0, 0.95 | 0.250, 0.250, 0.300 |
| 0.75 sunset | on the horizon | 1.0, 0.5, 0.2 | 0.140, 0.105, 0.100 |

The sun directions are `x/sky`'s orbit expression transcribed, so they are
bit-identical at the keys; the colours are its `sunColorKeyframes` and
`ambientKeyframes` read at the same four times.
`TestDefaultKeysAreXSkysCycle` compares all three against `x/sky` itself, and it
is the only place in the package that mentions `x/sky` — a test, so nothing here
depends on it at run time.

`Key.SunColor` is the sun's colour **at full strength**, and the fade that takes
it out is a function of elevation (`smoothstep(-0.14, 0.06, sunY)`, `x/sky`'s
`SunIntensity` re-derived) rather than of the clock. That matters: with the fade
keyed instead, interpolating from a black midnight toward a lit sunrise lights the
scene from a sun 40 degrees below the horizon for a sixth of the cycle.
`TestTheSunGoesOutWhenItSets` is what holds it.

What four keys cost, measured over 2000 times of day: the largest disagreement
with `x/sky`'s sun elevation is **0.0689**, four degrees of altitude, at time
0.191 and its three mirrors — the quarter points of the four segments, where a
chord sags furthest from its arc. It is **not** a disagreement about sunrise and
sunset, because the keys sit on the horizon crossings and both skies put the sun
at elevation zero at exactly 0.25 and 0.75.

Dawn and dusk are shorter-shaped than `x/sky`'s. Its ambient table has twelve
keys, with extra ones two or three hundredths of a cycle either side of sunrise
and sunset put there deliberately to give dusk a duration; four keys cannot have
that, so the blue hour arrives earlier and more gradually. That is the look this
sky has. A game that wants the other one hands `Options.Keys` a longer list, or
uses `x/sky`.

## What it lacks

| | |
|---|---|
| Clouds | `CloudsFrag` stays nil, so the engine builds no cloud pipeline and records no march. The half-resolution target is still allocated — 3.7 MB at 1280x720 — because the engine allocates it unconditionally so that a dome supplied without a cloud stage still has a primed target to composite. No GPU work. |
| Stars and the Milky Way | `StarsFrag` stays nil and the state leaves `DrawStars` false. |
| The sun and moon discs | The state leaves `DrawSun` and `DrawMoon` false, so the engine places no billboards. The sun is visible as the scattering halo the table carries around its direction. |
| Light shafts | They radiate from the sun billboard, so there is nothing for them to radiate from. `LightShafts` is zero. |
| The moon | There is no second body and no handover: the directional light fades out with the sun, and night is ambient plus whatever lamps the game placed. `x/sky`'s handover needs a second intensity ramp and a window placed so no shadow flips direction in a frame — three tuned things for a light a twentieth of the sun's. |
| `Scene.SetSkyPalette` reaching the dome | The palette is baked into the table. See below. |

### The palette is baked, and that is the real cost

`Scene.SetSkyPalette` moves the fog distant geometry fades into, the water's
reflection and every lit surface's night grade — and it does **not** move this
dome, because the dome is a texture that was baked before the scene said
anything. A game with its own palette sets it in both places:

```go
pal := glyph.SkyPalette{ /* ... */ }
e.Scene.SetSkyPalette(pal) // the fog, the water, the night grade
opts := skylut.DefaultOptions()
opts.Palette = pal // the dome
```

Set only one and the result is the mistake
[environment.md](../../../docs/agents/environment.md#a-sky-that-is-not-earths)
describes from the other direction: a dome and a haze from different planets.
`Options.Palette`'s zero value is `glyph.DefaultSkyPalette`, which is what a new
`Scene` starts with, so a game that says nothing gets a matched pair.

This is the one thing `x/sky` does better and not merely differently, and it is
inherent: a per-frame palette means a per-frame bake.

## Where the numbers come from, and what can drift

Two different places.

**The keys are sampled** from `x/sky`, and a test compares them against it.

**The model is re-derived.** The dome lives in GLSL — the engine's
`shaders/include/atmosphere.inc` for the curves and the palette blend, and the
gradient and the below-horizon fade in `x/sky`'s `sky.frag` — and there is no way
to evaluate a shader from Go, so `bake.go` computes it again. That is a copy, and
the engine already has one of these that
[drifted once](../../../AGENTS.md#rules-that-matter): `DayNight.Twilight` against
`atmTwilight`.

Two things are done about it rather than hoping:

- `TestAtmosphereIncStillSaysWhatWeCopied` reads the engine's **own exported
  bytes** (`shaders/include`'s `embed.FS`, the export ADR 0012 added for exactly
  this kind of coupling) and fails if any of the thirteen expressions `bake.go`
  copied have been reworded. That covers everything in `atmosphere.inc`.
- `skylut.frag` `#include`s `atmosphere.inc` for the one function it calls,
  `atmSunDirFrom`, so `spirv_test.go` is a compile gate on the same file's
  signatures.

What is **not** covered: the gradient and the below-horizon fade came from
`x/sky`'s `sky.frag`, which is a sibling package this one deliberately does not
reach into, so those two expressions are quoted verbatim beside the Go that
reimplements them and nothing automatic holds them. Changing either means
changing `bake.go`.

## Seams this binds to

| Seam | Used for |
|---|---|
| `renderer.ShaderSet.SkyFrag` | The dome. `StarsFrag` and `CloudsFrag` left nil, which is what turns those draws off. |
| `glyphengine.EnvironmentSource` / `EnvironmentState` | The per-frame light, air and `DrawSky`. |
| `glyphengine.StaticSource` | Called, not repeated, for the light and the air — the same choice `x/sky`'s fixed-hour path makes, for the same reason. |
| `glyphengine.SkyPalette`, `DefaultSkyPalette` | The six endpoints the table is baked from. |
| `renderer.Renderer.CreateDataTexture`, `DestroyTexture` | The table. The api-listed constructor, and the right one: the table holds numbers, so an sRGB decode would corrupt every one of them. |
| `renderer.SetShaderTexture`, `renderer.ShaderTextureSlots` | Binding it. |
| `shaders/include`'s `FS` | Reading `atmosphere.inc` back in a test, and `-I` for the compile. |
| The fixed shader layouts in [render-targets.md](../../../docs/agents/render-targets.md) | Set 1 binding 10 for the table, and the push block member order that lands `sunColor.w` and `fog.zw` where the recorder writes them. |

`ShaderTextureSlot` is **3**, the last of the renderer's four, and it is fixed
rather than an option because `skylut.frag` names its binding at compile time: a
configurable slot would mean four compiled variants of the same shader. Last
rather than first so a game allocating slots from zero — which is what
`examples/24-custom-passes` does at binding 7 — collides with this one last. A
game that needs all four cannot use this sky.

## Cost

Measured by `task xskylut`, on a scene that is nothing but sky: a camera at the
origin pitched up 0.6 radians with no geometry at all, so every pixel is a dome
fragment and the sky pass's own GPU bracket (`renderer.PassSky`) is the cost of
shading a full frame of it. 640x480, MSAA off, 300 frames with the first 60
discarded, three interleaved trials of four configurations.

Three independent runs of the whole comparison, each figure the mean of that
run's three interleaved trials, in milliseconds. RX 7900 XTX, 2026-10-03.

| | sky pass | cloud pass | frame |
|---|---|---|---|
| empty slot | 0.0001 · 0.0001 · 0.0001 | 0.0001 | 0.0504 · 0.0505 · 0.0494 |
| **this package** | **0.0082 · 0.0082 · 0.0083** | 0.0001 | **0.0557 · 0.0554 · 0.0559** |
| `x/sky`, dome only | 0.0099 · 0.0101 · 0.0101 | 0.0001 | 0.0571 · 0.0575 · 0.0575 |
| `x/sky`, whole | 0.0091 · 0.0090 · 0.0091 | 0.4849 – 0.4853 | 0.6235 · 0.6218 · 0.6218 |

- **Against the empty slot**: the dome adds **10.5 / 9.6 / 13.0 percent** to the
  frame. That is the worst case for it — every pixel is dome, with no geometry
  covering any of them.
- **Against `x/sky`'s dome**, pass bracket to pass bracket, one draw each: **82.6 /
  81.5 / 82.1 percent**. Across all nine trials the samples are 0.0081–0.0085 here
  and 0.0099–0.0102 there, two ranges that do not overlap — which is what makes
  18 percent a reading rather than noise at eight microseconds.
- **Against `x/sky` whole**: **8.9 / 8.9 / 9.0 percent** of the frame, eleven times
  cheaper. Nearly all of the difference is the cloud march at 0.485 ms, which a nil
  `CloudsFrag` does not pay at all.

Two things in that table are worth knowing before quoting it.

`x/sky`'s sky pass reads **0.0091, lower than its own dome-only 0.0101**, while
drawing strictly more (the dome plus a sun billboard), and it does so in all three
runs. The explanation that fits is clock state: that configuration's frame is 0.62
ms of work against 0.057, so the card is in a higher clock state for the whole of
it and every pass inside it is faster. It is recorded rather than explained away
because it is exactly the trap a pass-bracket comparison across two different frame
loads falls into — which is why the dome-to-dome comparison is made against the
dome-only configuration, whose frame is the same size as this one's. The
whole-frame comparison crosses the same boundary in the direction that
*understates* this package's advantage.

And the ratio against the empty slot swings by 3.4 points between runs, because the
frames being divided are 50 microseconds and the empty slot's own frame-to-frame
spread reached 5.9 percent. The gate's budget is 20 percent for that reason rather
than because 13 percent needed the room.

## Gates

| What | Where |
|---|---|
| The keys are `x/sky`'s cycle at four hours: sun direction to the bit, directional light and ambient to 1e-6 | `TestDefaultKeysAreXSkysCycle` |
| What four keys cost: the largest sun-elevation disagreement over the whole cycle | `TestTheSunAgreesBetweenTheKeysToo` |
| Every axis inverts the shader's mapping, and the ends are the ends | `TestAxesInvertTheShadersMapping` |
| The shader's four grid constants are the bake's, and it fetches with `textureLod` | `TestShaderAndBakeAgreeOnTheGrid` |
| The engine's `atmosphere.inc` still says what `bake.go` copied | `TestAtmosphereIncStillSaysWhatWeCopied` |
| The gradient falls from horizon to zenith at all 32 hours, with the floor split where there is light to measure | `TestTheGradientFallsFromHorizonToZenith` |
| The sun's side is brighter at dawn, which is the proximity axis doing anything | `TestTheSunSideIsBrighterAtDawn` |
| The decoded table is the model, and by how much it is not | `TestTheTableIsTheModel`, `TestTheDefaultPaletteHeadroom` |
| A palette too bright for the encoding is refused rather than clipped | `TestBakeRefusesAPaletteItCannotEncode` |
| The state is `StaticSource` plus one flag, and every zero in it is load-bearing | `TestStateIsStaticSourcePlusTheDome` |
| The directional light goes out when the sun sets | `TestTheSunGoesOutWhenItSets` |
| The clock wraps, and advances in simulation seconds through `Scene.Tick` | `TestTheClockWrapsAndAdvancesInSeconds` |
| Zero allocations per frame resolving and per tick advancing | `TestStateAndAdvanceAllocateNothing` |
| `Shaders` fills the dome and nothing else, and the engine's defaults leave the slot empty | `TestShadersFillOnlyTheDome` |
| The committed `.spv` is what `glslc` produces through the exported include set | `spirv_test.go` |
| The pixels, three hours, the sun's side, the empty-slot control and the cost | `task xskylut` |
| It runs and is validation-clean in a real example | `task smoke`, `task validate`, `task syncvalidate`, through `07-terrain -sky lut` |

The unit checks and the gate do not substitute for each other. The units read the
gradient and the sun out of the baked bytes at all 32 hours with no tonemap, no
bloom and no camera in the way; the gate reads what a frame actually does with
them, through the sampler, the slice blend, the tonemap and eight more bits of
rounding.

Every unit check above was broken and watched to fail — eighteen breaks,
including the two in the shader and one in the engine's own `atmosphere.inc` —
and each one's message is recorded in the comment above it. The gate's break is
re-runnable and needs no SDK: `lutskycheck -flat` binds one constant texel over
the table through the same `SetShaderTexture` slot, from outside the package,
which is "sample a constant texel" without touching the shader.

## The engine gap

**There is no public way to upload a texture wider than eight bits per channel.**
All four of `CreateTexture`, `CreateDataTexture`, `CreateTextureLinear` and
`CreateTextureNearest` are `R8G8B8A8`, and `CreateRenderTarget` — which does have
`TargetRGBA16F` — takes no CPU pixels. A lookup table holding radiance is the
case that wants one: the numbers above are the entire reason this sky bands where
`x/sky`'s does not, and they are a property of the format rather than of anything
in this package.

Filed as a rule-14 issue against the engine rather than patched from here, which
is the loop [`x/README.md`](../../README.md#dependency-direction) describes. Two
smaller ones noticed with it, both documentation rather than code:
`renderer.ShaderTextureSlots` is not on an `api` list although
`renderer.SetShaderTexture` is, and neither `CreateTextureLinear` nor
`CreateTextureNearest` is on one either — this package uses `CreateDataTexture`
because it is the one that is.

## Failure modes

- **A white sky.** The table is not bound: the slot is sampling the renderer's
  white fallback. Either `New` was never called, or something else bound
  `ShaderTextureSlot` afterwards.
- **No sky at all, and the light is right.** `glyph.WithShaders(skylut.Shaders())`
  is missing. The source asked for a dome the renderer has no pipeline for, which
  is not an error.
- **An alien dome over Earth-blue haze, or the reverse.** The palette is set in
  one of the two places it has to be set. See
  [the palette is baked](#the-palette-is-baked-and-that-is-the-real-cost).
- **No clouds, no stars, no sun.** Working as intended; use `x/sky`.
- **`New` returns "the baked sky reaches ... above the 3.0 the 8-bit encoding
  spans".** A palette bright enough to clip the table. Dim it, or use `x/sky`,
  whose dome reads the palette per frame and has no ceiling.
- **A thin wrong-coloured ring around the sun at some hours.** The half-texel
  inset on the proximity axis has been lost, so the sampler's filter is reaching
  across a slice boundary into the next hour's sky.
- **Everything is black.** `Scene.Env` is nil. `NewScene` leaves it nil; the
  engine has no environment of its own beyond `StaticSource`.
